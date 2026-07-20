package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"workground2/internal/agent"
	"workground2/internal/nilutil"
	"workground2/internal/session"
	"workground2/internal/work"
)

// WorkControl is the controller-side driving port shared by all frontends.
// The concrete Controller will forward these intents to internal/work.Service;
// this contract intentionally contains no Work business rules.
type WorkControl interface {
	work.WorkController
}

// WorkViewSink is the controller-side transport sink. Persisted WorkEvent
// values never pass through this boundary.
type WorkViewSink = work.ViewSink

// WorkService is the narrow lifecycle port owned by Controller. The concrete
// work.Service remains behind this boundary so tests and future hosts can inject
// an equivalent implementation without exposing Store internals.
type WorkService interface {
	Create(context.Context, work.CreateWorkInput) (*work.Work, error)
	Get(context.Context, string) (*work.WorkView, error)
	List(context.Context, work.WorkFilter) (work.WorkPage, error)
	UpdateDraft(context.Context, work.UpdateDraftInput) (*work.WorkView, error)
	Archive(context.Context, string, string) (*work.WorkRecord, error)
	Restore(context.Context, string, string) (*work.WorkView, error)
	Delete(context.Context, string, string) error
	RefreshBlock(context.Context, work.RefreshBlockInput, work.BlockSourceAdapter) (*work.BlockInstance, error)
	CancelBlockRefresh(context.Context, string, string, string) (*work.BlockInstance, error)
}

// WorkViewBroadcaster fans out WorkViewEvents to multiple subscribers.
// EmitWorkView never blocks; events for slow subscribers are dropped and the
// overflow count is observable via OverflowCount. Safe for concurrent use.
type WorkViewBroadcaster struct {
	mu            sync.RWMutex
	subs          map[string]*workViewSub
	nextID        atomic.Int64
	overflowCount atomic.Int64
}

type workViewSub struct {
	ch    chan work.WorkViewEvent
	drops atomic.Int64
}

// NewWorkViewBroadcaster returns a ready-to-use broadcaster with no subscribers.
func NewWorkViewBroadcaster() *WorkViewBroadcaster {
	return &WorkViewBroadcaster{
		subs: make(map[string]*workViewSub),
	}
}

// Subscribe adds a subscriber with a buffered channel of the given size.
// Returns a unique subscription ID and a receive-only channel. The caller
// must consume events promptly; slow consumers cause drops that are counted
// on the broadcaster (OverflowCount) and per-subscriber (SubscriberDrops).
// Call Unsubscribe with the returned ID to release the subscription.
func (b *WorkViewBroadcaster) Subscribe(bufSize int) (id string, ch <-chan work.WorkViewEvent) {
	if b == nil {
		closed := make(chan work.WorkViewEvent)
		close(closed)
		return "", closed
	}
	if bufSize < 1 {
		bufSize = 64
	}
	c := make(chan work.WorkViewEvent, bufSize)
	sub := &workViewSub{ch: c}
	id = fmt.Sprintf("wv-%d", b.nextID.Add(1))
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[string]*workViewSub)
	}
	b.subs[id] = sub
	b.mu.Unlock()
	return id, c
}

// Unsubscribe removes a subscriber. Idempotent: calling with an unknown or
// already-removed ID is a safe no-op.
func (b *WorkViewBroadcaster) Unsubscribe(id string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	sub, ok := b.subs[id]
	if ok {
		delete(b.subs, id)
		close(sub.ch)
	}
	b.mu.Unlock()
}

// EmitWorkView fans out the event to all subscribers. It implements work.ViewSink.
// Sends are non-blocking: if a subscriber's buffer is full the event is dropped
// and both the per-subscriber and broadcaster overflow counters are incremented.
func (b *WorkViewBroadcaster) EmitWorkView(e work.WorkViewEvent) {
	if b == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		select {
		case sub.ch <- e:
		default:
			sub.drops.Add(1)
			b.overflowCount.Add(1)
		}
	}
}

// SubscriberCount returns the current number of active subscribers.
func (b *WorkViewBroadcaster) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// OverflowCount returns the total number of events dropped across all
// subscribers since the broadcaster was created. Observable for monitoring
// and recovery.
func (b *WorkViewBroadcaster) OverflowCount() int64 {
	if b == nil {
		return 0
	}
	return b.overflowCount.Load()
}

// SubscriberDrops returns the number of events dropped for a specific
// subscriber since it was created. Returns 0 for unknown IDs.
func (b *WorkViewBroadcaster) SubscriberDrops(id string) int64 {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if sub, ok := b.subs[id]; ok && sub != nil {
		return sub.drops.Load()
	}
	return 0
}

// workMethods delegates WorkController calls to the backing WorkService.
// Nil receiver is safe: every method returns an error.
type workMethods struct {
	svc     WorkService
	refresh *work.BlockRefreshManager
	sources *workSourceRegistry
}

func (w workMethods) CreateWork(ctx context.Context, input work.CreateWorkInput) (*work.Work, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.Create(ctx, input)
}

func (w workMethods) GetWork(ctx context.Context, workID string) (*work.WorkView, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.Get(ctx, workID)
}

func (w workMethods) ListWorks(ctx context.Context, filter work.WorkFilter) (work.WorkPage, error) {
	if nilutil.IsNil(w.svc) {
		return work.WorkPage{}, errWorkDisabled
	}
	return w.svc.List(ctx, filter)
}

func (w workMethods) UpdateDraft(ctx context.Context, input work.UpdateDraftInput) (*work.WorkView, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.UpdateDraft(ctx, input)
}

func (w workMethods) RunWork(ctx context.Context, workID, requestID string) (*work.WorkflowRun, error) {
	return nil, errWorkNotImplemented("RunWork")
}

func (w workMethods) RetryTask(ctx context.Context, input work.RetryTaskInput) (*work.Attempt, error) {
	return nil, errWorkNotImplemented("RetryTask")
}

func (w workMethods) CancelRun(ctx context.Context, workID, runID, requestID string) error {
	return errWorkNotImplemented("CancelRun")
}

func (w workMethods) ArchiveWork(ctx context.Context, workID, requestID string) (*work.WorkRecord, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	record, err := w.svc.Archive(ctx, workID, requestID)
	if err == nil && record != nil && w.refresh != nil {
		w.refresh.UnsubscribeWork(record.WorkID)
	}
	return record, err
}

func (w workMethods) RestoreWork(ctx context.Context, workID, requestID string) (*work.WorkView, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	view, err := w.svc.Restore(ctx, workID, requestID)
	if err == nil && w.refresh != nil && w.sources != nil {
		w.refresh.RecoverFromProjection(view, w.sources.resolve)
	}
	return view, err
}

func (w workMethods) DeleteWork(ctx context.Context, workID, requestID string) error {
	if nilutil.IsNil(w.svc) {
		return errWorkDisabled
	}
	if err := w.svc.Delete(ctx, workID, requestID); err != nil {
		return err
	}
	if w.refresh != nil {
		w.refresh.UnsubscribeWork(workID)
	}
	return nil
}

func (w workMethods) PinCornerstone(ctx context.Context, workID string, input work.CornerstoneInput) (*work.Cornerstone, error) {
	return nil, errWorkNotImplemented("PinCornerstone")
}

func (w workMethods) RefreshCornerstone(ctx context.Context, workID, cornerstoneID, requestID string) (*work.Cornerstone, error) {
	return nil, errWorkNotImplemented("RefreshCornerstone")
}

func (w workMethods) RemoveCornerstone(ctx context.Context, workID, cornerstoneID, requestID string) error {
	return errWorkNotImplemented("RemoveCornerstone")
}

func (w workMethods) RefreshBlock(ctx context.Context, workID, blockID, requestID string) (*work.BlockInstance, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	if w.refresh == nil || w.sources == nil {
		return nil, work.ErrBlockNotRefreshable
	}
	view, err := w.svc.Get(ctx, strings.TrimSpace(workID))
	if err != nil {
		return nil, err
	}
	if view == nil || view.Work == nil {
		return nil, errors.New("work: RefreshBlock: Work projection is unavailable")
	}
	var block *work.BlockInstance
	for i := range view.Work.Blocks {
		if view.Work.Blocks[i].ID == strings.TrimSpace(blockID) {
			block = &view.Work.Blocks[i]
			break
		}
	}
	if block == nil || block.Tombstone {
		return nil, fmt.Errorf("%w: block %s not found or removed", work.ErrBlockRefreshStopped, blockID)
	}
	adapter, source, schedule, ok := w.sources.resolve(*block)
	if !ok {
		return nil, fmt.Errorf("%w: no source for %s/%s (%s)", work.ErrBlockNotRefreshable, workID, blockID, block.Kind)
	}
	delay := schedule.Interval
	if delay <= 0 {
		delay = work.DefaultRefreshSchedule().Interval
	}
	w.refresh.SubscribeAfter(view.Work.ID, block.ID, adapter, source, schedule, delay)
	return w.refresh.RefreshRequest(ctx, view.Work.ID, block.ID, requestID)
}

func (w workMethods) ExecuteBlockAction(ctx context.Context, input work.BlockActionRequest) (*work.ActionReceipt, error) {
	return nil, errWorkNotImplemented("ExecuteBlockAction")
}

func (w workMethods) PrepareRerun(ctx context.Context, input work.PrepareRerunInput) (*work.RerunPlan, error) {
	return nil, errWorkNotImplemented("PrepareRerun")
}

func (w workMethods) ExecuteRerun(ctx context.Context, planToken, requestID string) (*work.Work, error) {
	return nil, errWorkNotImplemented("ExecuteRerun")
}

var (
	errWorkDisabled = errors.New("work: feature is disabled; enable [work].enabled in config")
)

func errWorkNotImplemented(method string) error {
	return fmt.Errorf("work: %s is not yet implemented", method)
}

// WorkControl returns the WorkController port for this session. It is nil-safe:
// when Work is disabled the returned port returns errWorkDisabled for every
// operation.
func (c *Controller) WorkControl() WorkControl {
	if c == nil {
		return workMethods{}
	}
	return workMethods{svc: c.workSvc, refresh: c.workRefresh, sources: c.workSources}
}

// WorkViews returns the WorkViewEvent broadcaster for this session, or nil when
// Work is disabled. Frontends subscribe to receive structured projection updates.
func (c *Controller) WorkViews() *WorkViewBroadcaster {
	if c == nil {
		return nil
	}
	return c.workViews
}

// LookupSession adapts Controller's persisted Session capability to the narrow
// work.SessionLookup port. It is read-only, context-aware, and confines lookups
// to this Controller's SessionDir so a persisted Work reference cannot become
// an arbitrary filesystem read.
func (c *Controller) LookupSession(ctx context.Context, sessionPath string) (work.SessionRef, bool, error) {
	if c == nil {
		return work.SessionRef{}, false, errors.New("work: Session lookup requires a Controller")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return work.SessionRef{}, false, err
	}

	root := strings.TrimSpace(c.SessionDir())
	path := strings.TrimSpace(sessionPath)
	if root == "" || path == "" {
		return work.SessionRef{}, false, errors.New("work: Session lookup requires sessionDir and sessionPath")
	}
	pathAbs, _, err := session.ValidatePath(root, path)
	if err != nil {
		return work.SessionRef{}, false, fmt.Errorf("work: Session path is outside SessionDir or invalid: %w", err)
	}
	info, err := os.Stat(pathAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return work.SessionRef{}, false, nil
		}
		return work.SessionRef{}, false, fmt.Errorf("work: inspect Session %q: %w", pathAbs, err)
	}
	if info.IsDir() {
		return work.SessionRef{}, false, fmt.Errorf("work: Session path %q is a directory", pathAbs)
	}

	meta, hasMeta, err := agent.LoadBranchMeta(pathAbs)
	if err != nil {
		return work.SessionRef{}, false, fmt.Errorf("work: load Session metadata: %w", err)
	}
	preview, turns := meta.Preview, meta.Turns
	if !hasMeta || meta.SchemaVersion < agent.BranchMetaCountsVersion {
		session, loadErr := agent.LoadSession(pathAbs)
		if loadErr != nil {
			return work.SessionRef{}, false, fmt.Errorf("work: load Session: %w", loadErr)
		}
		preview, turns = agent.SessionPreviewFromMessages(session.Snapshot())
	}
	branchID := strings.TrimSpace(meta.ID)
	if branchID == "" {
		branchID = agent.BranchID(pathAbs)
	}
	modelRef := strings.TrimSpace(meta.Model)
	if modelRef == "" && filepath.Clean(pathAbs) == filepath.Clean(c.SessionPath()) {
		modelRef = c.modelRef
	}
	startedAt := meta.CreatedAt
	if startedAt.IsZero() {
		startedAt = info.ModTime().UTC()
	}
	return work.SessionRef{
		SessionPath: pathAbs,
		BranchID:    branchID,
		ModelRef:    modelRef,
		TurnCount:   turns,
		Preview:     preview,
		StartedAt:   startedAt,
	}, true, nil
}

var (
	_ WorkService        = (*work.Service)(nil)
	_ work.SessionLookup = (*Controller)(nil)
)
