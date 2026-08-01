package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/nilutil"
	"workground2/internal/permission"
	"workground2/internal/session"
	"workground2/internal/work"
)

// WorkControl is the controller-side driving port shared by all frontends.
// The concrete Controller will forward these intents to internal/work.Service;
// this contract intentionally contains no Work business rules.
type WorkControl interface {
	work.WorkController
}

// WorkEnabled reports whether this Controller owns a complete writable Work
// surface. Frontends use this typed capability instead of probing operation
// errors or guessing configuration state.
func (c *Controller) WorkEnabled() bool {
	return c != nil && !nilutil.IsNil(c.workSvc) && c.workViews != nil
}

// WorkV2Enabled reports whether this Controller exposes the V2 collaboration
// intent surface. It is deliberately separate from WorkEnabled so disabling V2
// leaves the existing V1 Work surface unchanged.
func (c *Controller) WorkV2Enabled() bool {
	return c != nil && c.WorkEnabled() && c.workV2Enabled
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
	ListBlueprints(context.Context) ([]work.WorkBlueprint, error)
	CopyWork(context.Context, work.CopyWorkInput) (*work.Work, error)
	UpdateDraft(context.Context, work.UpdateDraftInput) (*work.WorkView, error)
	UpsertBlock(context.Context, work.BlockUpsertInput) (*work.WorkView, error)
	RunWork(context.Context, string, string) (*work.WorkflowRun, error)
	RetryTask(context.Context, work.RetryTaskInput) (*work.Attempt, error)
	CancelRun(context.Context, string, string, string) error
	PauseRun(context.Context, string, string, string) error
	ResumeRun(context.Context, work.ResumeRunInput) (*work.WorkflowRun, error)
	RestartRun(context.Context, string, string, string) (*work.WorkflowRun, error)
	Archive(context.Context, string, string) (*work.WorkRecord, error)
	Restore(context.Context, string, string) (*work.WorkView, error)
	Delete(context.Context, string, string) error
	RefreshBlock(context.Context, work.RefreshBlockInput, work.BlockSourceAdapter) (*work.BlockInstance, error)
	CancelBlockRefresh(context.Context, string, string, string) (*work.BlockInstance, error)
	ExecuteBlockAction(context.Context, work.BlockActionRequest) (*work.ActionReceipt, error)
	PinCornerstone(context.Context, string, work.PinCornerstoneInput) (*work.CornerstoneResult, error)
	RefreshCornerstone(context.Context, string, work.RefreshCornerstoneInput) (*work.CornerstoneResult, error)
	RemoveCornerstone(context.Context, string, work.RemoveCornerstoneInput) (*work.CornerstoneResult, error)
	UndoCornerstone(context.Context, string, work.UndoCornerstoneInput) (*work.CornerstoneResult, error)
	AcceptCornerstone(context.Context, string, work.AcceptCornerstoneInput) (*work.CornerstoneResult, error)
	FreezeCornerstone(context.Context, string, work.FreezeCornerstoneInput) (*work.CornerstoneResult, error)
	RepairCornerstone(context.Context, string, work.RepairCornerstoneInput) (*work.RepairResult, error)
	PrepareRerun(context.Context, work.PrepareRerunInput) (*work.RerunPlan, error)
	ExecuteRerun(context.Context, string, string) (*work.Work, error)

	// V2 Collaboration Controller
	BeginWorkPlanning(context.Context, work.BeginWorkPlanningInput) (*work.WorkView, error)
	BeginWorkPlanningWithResult(context.Context, work.BeginWorkPlanningInput) (*work.BeginWorkPlanningResult, error)
	BeginBlueprintPlanning(context.Context, work.BeginBlueprintPlanningInput) (*work.BeginBlueprintPlanningResult, error)
	ApplyDefinition(context.Context, work.ApplyDefinitionInput) (*work.ApplyDefinitionResult, error)
	CreateCandidateRevisionWithResult(context.Context, work.CreateCandidateRevisionInput) (*work.CreateCandidateRevisionResult, error)
	SubmitWorkInput(context.Context, work.SubmitInputRequest) (*work.SubmitInputResult, error)
	AddCustomWorkInput(context.Context, work.AddCustomWorkInputRequest) (*work.SubmitInputResult, error)
	InferWorkInputs(context.Context, work.InferWorkInputsRequest) (*work.InferWorkInputsResult, error)
	SetInputCornerstone(context.Context, work.SetInputCornerstoneRequest) (*work.CornerstonePinResult, error)
	PreviewWorkPatch(context.Context, work.PreviewWorkPatchInput) (*work.PreviewWorkPatchResult, error)
	ApplyWorkPatch(context.Context, work.ApplyWorkPatchInput) (*work.ApplyWorkPatchResult, error)
	RetryWorkNode(context.Context, work.RetryWorkNodeRequest) (*work.RetryWorkNodeResult, error)
	RetryArtifactSlot(context.Context, work.RetryArtifactSlotRequest) (*work.RetryArtifactSlotResult, error)
	PreviewArtifact(context.Context, work.PreviewArtifactRequest) (*work.PreviewArtifactResult, error)
	RequestArtifactConversion(context.Context, work.RequestArtifactConversionInput) (*work.RequestArtifactConversionResult, error)
	RecoverArtifactConversions(context.Context, string) (int, error)
}

// WorkViewBroadcaster fans out WorkViewEvents to multiple subscribers.
// EmitWorkView never blocks; events for slow subscribers are dropped and the
// overflow count is observable via OverflowCount. Per-work subscriptions
// (SubscribeWork) filter at emission time so unrelated Work traffic cannot
// squeeze a subscriber's buffer. Safe for concurrent use.
type WorkViewBroadcaster struct {
	mu            sync.RWMutex
	subs          map[string]*workViewSub
	nextID        atomic.Int64
	overflowCount atomic.Int64
}

type workViewSub struct {
	ch       chan work.WorkViewEvent
	overflow chan struct{}
	workID   string // non-empty → only receive events for this Work (filtered at emit)
	drops    atomic.Int64
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
	id, ch, _ = b.subscribeWork("", bufSize)
	return id, ch
}

// SubscribeWork adds a subscriber that only receives events for the given
// workID. Filtering happens at EmitWorkView time — before the channel buffer —
// so cross-Work traffic cannot squeeze a focused subscriber.
func (b *WorkViewBroadcaster) SubscribeWork(workID string, bufSize int) (id string, ch <-chan work.WorkViewEvent) {
	id, ch, _ = b.subscribeWork(workID, bufSize)
	return id, ch
}

// SubscribeWorkReliable adds a per-Work subscriber plus an independent sticky
// overflow signal. A slow subscriber can lose deltas, but cannot lose the
// resync request when the dropped event is terminal and has no successor.
func (b *WorkViewBroadcaster) SubscribeWorkReliable(workID string, bufSize int) (id string, ch <-chan work.WorkViewEvent, overflow <-chan struct{}) {
	return b.subscribeWork(workID, bufSize)
}

func (b *WorkViewBroadcaster) subscribeWork(workID string, bufSize int) (id string, ch <-chan work.WorkViewEvent, overflow <-chan struct{}) {
	if b == nil {
		closed := make(chan work.WorkViewEvent)
		close(closed)
		closedOverflow := make(chan struct{})
		close(closedOverflow)
		return "", closed, closedOverflow
	}
	if bufSize < 1 {
		bufSize = 64
	}
	c := make(chan work.WorkViewEvent, bufSize)
	o := make(chan struct{}, 1)
	sub := &workViewSub{ch: c, overflow: o}
	if workID != "" {
		sub.workID = workID
	}
	id = fmt.Sprintf("wv-%d", b.nextID.Add(1))
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[string]*workViewSub)
	}
	b.subs[id] = sub
	b.mu.Unlock()
	return id, c, o
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
		close(sub.overflow)
	}
	b.mu.Unlock()
}

// EmitWorkView fans out the event to all subscribers. It implements work.ViewSink.
// Per-work subscriptions are filtered here (before the channel buffer). Sends
// are non-blocking. When a subscriber's data buffer is full, a sticky token is
// written to its independent overflow channel so recovery never depends on a
// successor data event.
func (b *WorkViewBroadcaster) EmitWorkView(e work.WorkViewEvent) {
	if b == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		if sub.workID != "" && e.WorkID != sub.workID {
			continue
		}
		select {
		case sub.ch <- e:
		default:
			select {
			case sub.overflow <- struct{}{}:
			default:
			}
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
	v2      bool
	owner   *Controller
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
	view, err := w.svc.Get(ctx, workID)
	if err != nil || w.v2 {
		return view, err
	}
	return work.AsV1WorkView(view), nil
}

func (w workMethods) ListWorks(ctx context.Context, filter work.WorkFilter) (work.WorkPage, error) {
	if nilutil.IsNil(w.svc) {
		return work.WorkPage{}, errWorkDisabled
	}
	return w.svc.List(ctx, filter)
}

func (w workMethods) ListWorkBlueprints(ctx context.Context) ([]work.WorkBlueprint, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.ListBlueprints(ctx)
}

func (w workMethods) CopyWork(ctx context.Context, input work.CopyWorkInput) (*work.Work, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.CopyWork(ctx, input)
}

func (w workMethods) UpdateDraft(ctx context.Context, input work.UpdateDraftInput) (*work.WorkView, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.UpdateDraft(ctx, input)
}

func (w workMethods) UpsertWorkBlock(ctx context.Context, input work.BlockUpsertInput) (*work.WorkView, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.UpsertBlock(ctx, input)
}

func (w workMethods) RunWork(ctx context.Context, workID, requestID string) (*work.WorkflowRun, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.RunWork(ctx, workID, requestID)
}

func (w workMethods) RetryTask(ctx context.Context, input work.RetryTaskInput) (*work.Attempt, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.RetryTask(ctx, input)
}

func (w workMethods) CancelRun(ctx context.Context, workID, runID, requestID string) error {
	if nilutil.IsNil(w.svc) {
		return errWorkDisabled
	}
	return w.svc.CancelRun(ctx, workID, runID, requestID)
}

func (w workMethods) PauseRun(ctx context.Context, workID, runID, requestID string) error {
	if nilutil.IsNil(w.svc) {
		return errWorkDisabled
	}
	return w.svc.PauseRun(ctx, workID, runID, requestID)
}

func (w workMethods) ResumeRun(ctx context.Context, input work.ResumeRunInput) (*work.WorkflowRun, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.ResumeRun(ctx, input)
}

func (w workMethods) RestartRun(ctx context.Context, workID, runID, requestID string) (*work.WorkflowRun, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.RestartRun(ctx, workID, runID, requestID)
}

func (w workMethods) ArchiveWork(ctx context.Context, workID, requestID string) (*work.WorkRecord, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	if w.owner == nil {
		return w.svc.Archive(ctx, workID, requestID)
	}
	w.owner.workRefreshLifeMu.Lock()
	record, err := w.svc.Archive(ctx, workID, requestID)
	if err == nil && record != nil {
		w.owner.advanceWorkRefreshLocked(record.WorkID)
	}
	w.owner.workRefreshLifeMu.Unlock()
	if err == nil && record != nil && w.refresh != nil {
		w.refresh.UnsubscribeWork(record.WorkID)
	}
	return record, err
}

func (w workMethods) RestoreWork(ctx context.Context, workID, requestID string) (*work.WorkView, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	if w.owner == nil {
		return w.svc.Restore(ctx, workID, requestID)
	}
	w.owner.workRefreshLifeMu.Lock()
	view, err := w.svc.Restore(ctx, workID, requestID)
	var generation uint64
	if err == nil {
		generation = w.owner.advanceWorkRefreshLocked(workID)
	}
	w.owner.workRefreshLifeMu.Unlock()
	if err == nil && w.refresh != nil {
		w.refresh.UnsubscribeWork(workID)
		if w.sources != nil {
			if recoverErr := w.owner.recoverWorkRefreshView(ctx, view, generation); recoverErr != nil {
				w.sources.setRecoverError(fmt.Errorf("work: restore refresh intents: %w", recoverErr))
			}
		}
	}
	return view, err
}

func (w workMethods) DeleteWork(ctx context.Context, workID, requestID string) error {
	if nilutil.IsNil(w.svc) {
		return errWorkDisabled
	}
	if w.owner == nil {
		return w.svc.Delete(ctx, workID, requestID)
	}
	w.owner.workRefreshLifeMu.Lock()
	if err := w.svc.Delete(ctx, workID, requestID); err != nil {
		w.owner.workRefreshLifeMu.Unlock()
		return err
	}
	w.owner.advanceWorkRefreshLocked(workID)
	w.owner.workRefreshLifeMu.Unlock()
	if w.refresh != nil {
		w.refresh.UnsubscribeWork(workID)
	}
	return nil
}

func (w workMethods) PinCornerstone(ctx context.Context, workID string, input work.PinCornerstoneInput) (*work.CornerstoneResult, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.PinCornerstone(ctx, workID, input)
}

func (w workMethods) RefreshCornerstone(ctx context.Context, workID string, input work.RefreshCornerstoneInput) (*work.CornerstoneResult, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.RefreshCornerstone(ctx, workID, input)
}

func (w workMethods) RemoveCornerstone(ctx context.Context, workID string, input work.RemoveCornerstoneInput) (*work.CornerstoneResult, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.RemoveCornerstone(ctx, workID, input)
}

func (w workMethods) UndoCornerstone(ctx context.Context, workID string, input work.UndoCornerstoneInput) (*work.CornerstoneResult, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.UndoCornerstone(ctx, workID, input)
}

func (w workMethods) AcceptCornerstone(ctx context.Context, workID string, input work.AcceptCornerstoneInput) (*work.CornerstoneResult, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.AcceptCornerstone(ctx, workID, input)
}

func (w workMethods) FreezeCornerstone(ctx context.Context, workID string, input work.FreezeCornerstoneInput) (*work.CornerstoneResult, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.FreezeCornerstone(ctx, workID, input)
}

func (w workMethods) RepairCornerstone(ctx context.Context, workID string, input work.RepairCornerstoneInput) (*work.RepairResult, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.RepairCornerstone(ctx, workID, input)
}

func (w workMethods) RefreshBlock(ctx context.Context, workID, blockID, requestID string) (*work.BlockInstance, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	if w.refresh == nil || w.sources == nil {
		return nil, work.ErrBlockNotRefreshable
	}
	workID, blockID = strings.TrimSpace(workID), strings.TrimSpace(blockID)
	generation := uint64(0)
	if w.owner != nil {
		generation = w.owner.workRefreshGeneration(workID)
	}
	view, err := w.svc.Get(ctx, workID)
	if err != nil {
		return nil, err
	}
	if view == nil || view.Work == nil {
		return nil, errors.New("work: RefreshBlock: Work projection is unavailable")
	}
	var block *work.BlockInstance
	for i := range view.Work.Blocks {
		if view.Work.Blocks[i].ID == blockID {
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
	if w.owner != nil {
		err = func() error {
			w.owner.workRefreshLifeMu.Lock()
			defer w.owner.workRefreshLifeMu.Unlock()
			if w.owner.workRefreshGen[workID] != generation || w.owner.workRefreshStopPendingLocked(workID, generation) {
				return work.ErrBlockRefreshStopped
			}
			current, currentErr := w.svc.Get(ctx, workID)
			if currentErr != nil {
				return currentErr
			}
			if current == nil || current.Work == nil || current.Work.ArchiveState != work.ArchiveActive {
				return work.ErrBlockRefreshStopped
			}
			block = nil
			for i := range current.Work.Blocks {
				if current.Work.Blocks[i].ID == blockID {
					block = &current.Work.Blocks[i]
					break
				}
			}
			if block == nil || block.Tombstone {
				return fmt.Errorf("%w: %w: block %s not found or removed", work.ErrBlockRefreshStopped, work.ErrBlockNotFound, blockID)
			}
			adapter, source, schedule, ok = w.sources.resolve(*block)
			if !ok {
				return fmt.Errorf("%w: no source for %s/%s (%s)", work.ErrBlockNotRefreshable, workID, blockID, block.Kind)
			}
			return w.refresh.SubscribeAfter(workID, blockID, adapter, source, schedule, schedule.Interval)
		}()
	} else {
		err = w.refresh.SubscribeAfter(workID, blockID, adapter, source, schedule, schedule.Interval)
	}
	if err != nil {
		return nil, err
	}
	return w.refresh.RefreshRequest(ctx, workID, blockID, requestID)
}

func (w workMethods) ExecuteBlockAction(ctx context.Context, input work.BlockActionRequest) (*work.ActionReceipt, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	if w.owner != nil {
		return w.owner.executeBlockAction(ctx, w.svc, input)
	}
	return w.svc.ExecuteBlockAction(ctx, input)
}

func (c *Controller) executeBlockAction(ctx context.Context, svc WorkService, input work.BlockActionRequest) (*work.ActionReceipt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	actionCtx, finish, ok := c.beginBlockAction(ctx, input.WorkID, input.RequestID)
	if !ok {
		return nil, context.Canceled
	}
	defer finish()
	return svc.ExecuteBlockAction(actionCtx, input)
}

func (c *Controller) beginBlockAction(ctx context.Context, workID, requestID string) (context.Context, func(), bool) {
	c.actionMu.Lock()
	if c.actionClosed {
		c.actionMu.Unlock()
		return nil, nil, false
	}
	if c.actionRoot == nil {
		c.actionRoot, c.actionRootCancel = context.WithCancel(context.Background())
	}
	if c.actionRuns == nil {
		c.actionRuns = make(map[string]map[uint64]context.CancelFunc)
	}
	actionCtx, cancel := context.WithCancel(ctx)
	key := strings.TrimSpace(workID) + "\x00" + strings.TrimSpace(requestID)
	c.actionNext++
	id := c.actionNext
	if c.actionRuns[key] == nil {
		c.actionRuns[key] = make(map[uint64]context.CancelFunc)
	}
	c.actionRuns[key][id] = cancel
	c.actionWG.Add(1)
	root := c.actionRoot
	c.actionMu.Unlock()

	stopRoot := context.AfterFunc(root, cancel)
	var once sync.Once
	finish := func() {
		once.Do(func() {
			stopRoot()
			cancel()
			c.actionMu.Lock()
			delete(c.actionRuns[key], id)
			if len(c.actionRuns[key]) == 0 {
				delete(c.actionRuns, key)
			}
			c.actionMu.Unlock()
			c.actionWG.Done()
		})
	}
	return actionCtx, finish, true
}

func (c *Controller) cancelBlockActions() int {
	c.actionMu.Lock()
	cancels := make([]context.CancelFunc, 0)
	for _, runs := range c.actionRuns {
		for _, cancel := range runs {
			cancels = append(cancels, cancel)
		}
	}
	c.actionMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

func (c *Controller) closeBlockActions() {
	c.actionMu.Lock()
	if !c.actionClosed {
		c.actionClosed = true
		if c.actionRootCancel != nil {
			c.actionRootCancel()
		}
	}
	c.actionMu.Unlock()
	c.cancelBlockActions()
	c.approval.clearActionApprovals()
	c.actionWG.Wait()
}

// CheckPermission adapts Work actions to the same Policy and ApprovalRequest
// stream used by ordinary tool calls. It blocks until an Ask is answered so the
// Service can persist the final rejection/cancellation/execution outcome.
func (c *Controller) CheckPermission(ctx context.Context, input work.PermissionRequest) (work.PermissionDecision, error) {
	if c == nil {
		return work.PermissionDecision{Reason: "controller is unavailable"}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	grantKey := actionSessionGrantFor(input)
	subject := actionApprovalSubject(grantKey)
	readOnly := input.Risk == string(work.RiskRead) && !input.ConfirmRequired
	decision := c.policy.DecideSubject(input.ToolName, readOnly, subject)
	if decision == permission.Deny {
		return work.PermissionDecision{Reason: "denied by permission policy"}, nil
	}
	if decision == permission.Allow && !input.ConfirmRequired {
		return work.PermissionDecision{Allowed: true}, nil
	}

	summary := strings.TrimSpace(input.Summary)
	if summary == "" {
		summary = input.ToolName
	}
	reason := fmt.Sprintf("%s · handler=%s@%s · risk=%s · work=%s · block=%s · action=%s · request=%s",
		summary, input.HandlerID, input.HandlerVersion, input.Risk, input.WorkID, input.BlockID, input.ActionID, input.RequestID)
	args, _ := json.Marshal(map[string]string{
		"workId": input.WorkID, "blockId": input.BlockID, "actionId": input.ActionID,
		"requestId": input.RequestID, "handlerId": input.HandlerID, "handlerVersion": input.HandlerVersion,
		"risk": input.Risk, "summary": summary,
	})
	approval := event.Approval{
		WorkID: input.WorkID, BlockID: input.BlockID, ActionID: input.ActionID,
		RequestID: input.RequestID, Summary: summary,
		HandlerID: input.HandlerID, HandlerVersion: input.HandlerVersion,
	}
	reply, err := c.requestApprovalDecisionWithOptions(ctx, input.ToolName, subject, args, reason,
		approvalDecisionOptions{fresh: input.ConfirmRequired, approval: approval, actionSessionGrant: &grantKey})
	if err != nil {
		return work.PermissionDecision{}, err
	}
	if !reply.allow {
		return work.PermissionDecision{Reason: "approval rejected by user"}, nil
	}
	if reply.session && !input.ConfirmRequired {
		c.approval.grantActionSession(grantKey)
	}
	if reply.persist && !input.ConfirmRequired && c.onRemember != nil {
		c.emitRememberResult(c.onRemember(permission.RememberRuleForScope(input.ToolName, subject)))
	}
	return work.PermissionDecision{Allowed: true}, nil
}

// actionSessionGrantKey is the exact, in-memory identity for one Work Action
// session grant. Controller lifetime supplies the user/session boundary. A
// comparable struct avoids delimiter escaping and wildcard parsing entirely.
type actionSessionGrantKey struct {
	ToolName        string
	WorkID          string
	BlockID         string
	ActionID        string
	HandlerID       string
	HandlerVersion  string
	Risk            work.ActionRisk
	ConfirmRequired bool
}

func actionSessionGrantFor(input work.PermissionRequest) actionSessionGrantKey {
	return actionSessionGrantKey{
		ToolName: strings.TrimSpace(input.ToolName), WorkID: strings.TrimSpace(input.WorkID),
		BlockID: strings.TrimSpace(input.BlockID), ActionID: strings.TrimSpace(input.ActionID),
		HandlerID: strings.TrimSpace(input.HandlerID), HandlerVersion: strings.TrimSpace(input.HandlerVersion),
		Risk: work.ActionRisk(strings.TrimSpace(input.Risk)), ConfirmRequired: input.ConfirmRequired,
	}
}

func actionApprovalSubject(key actionSessionGrantKey) string {
	return fmt.Sprintf("work:%s/block:%s/action:%s/handler:%s@%s",
		key.WorkID, key.BlockID, key.ActionID, key.HandlerID, key.HandlerVersion)
}

func (w workMethods) PrepareRerun(ctx context.Context, input work.PrepareRerunInput) (*work.RerunPlan, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.PrepareRerun(ctx, input)
}

func (w workMethods) ExecuteRerun(ctx context.Context, planToken, requestID string) (*work.Work, error) {
	if nilutil.IsNil(w.svc) {
		return nil, errWorkDisabled
	}
	return w.svc.ExecuteRerun(ctx, planToken, requestID)
}

func (w workMethods) BeginWorkPlanning(ctx context.Context, input work.BeginWorkPlanningInput) (*work.WorkView, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.BeginWorkPlanning(ctx, input)
}

func (w workMethods) BeginWorkPlanningWithResult(ctx context.Context, input work.BeginWorkPlanningInput) (*work.BeginWorkPlanningResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.BeginWorkPlanningWithResult(ctx, input)
}

func (w workMethods) BeginBlueprintPlanning(ctx context.Context, input work.BeginBlueprintPlanningInput) (*work.BeginBlueprintPlanningResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.BeginBlueprintPlanning(ctx, input)
}

func (w workMethods) ApplyDefinition(ctx context.Context, input work.ApplyDefinitionInput) (*work.ApplyDefinitionResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.ApplyDefinition(ctx, input)
}

func (w workMethods) CreateCandidateRevision(ctx context.Context, input work.CreateCandidateRevisionInput) (*work.CreateCandidateRevisionResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.CreateCandidateRevisionWithResult(ctx, input)
}

func (w workMethods) SubmitWorkInput(ctx context.Context, input work.SubmitInputRequest) (*work.SubmitInputResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.SubmitWorkInput(ctx, input)
}

func (w workMethods) AddCustomWorkInput(ctx context.Context, input work.AddCustomWorkInputRequest) (*work.SubmitInputResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.AddCustomWorkInput(ctx, input)
}

func (w workMethods) InferWorkInputs(ctx context.Context, input work.InferWorkInputsRequest) (*work.InferWorkInputsResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.InferWorkInputs(ctx, input)
}

func (w workMethods) SetInputCornerstone(ctx context.Context, input work.SetInputCornerstoneRequest) (*work.CornerstonePinResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.SetInputCornerstone(ctx, input)
}

func (w workMethods) PreviewWorkPatch(ctx context.Context, input work.PreviewWorkPatchInput) (*work.PreviewWorkPatchResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.PreviewWorkPatch(ctx, input)
}

func (w workMethods) ApplyWorkPatch(ctx context.Context, input work.ApplyWorkPatchInput) (*work.ApplyWorkPatchResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.ApplyWorkPatch(ctx, input)
}

func (w workMethods) RetryWorkNode(ctx context.Context, input work.RetryWorkNodeRequest) (*work.RetryWorkNodeResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.RetryWorkNode(ctx, input)
}

func (w workMethods) RetryArtifactSlot(ctx context.Context, input work.RetryArtifactSlotRequest) (*work.RetryArtifactSlotResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.RetryArtifactSlot(ctx, input)
}

func (w workMethods) PreviewArtifact(ctx context.Context, input work.PreviewArtifactRequest) (*work.PreviewArtifactResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.PreviewArtifact(ctx, input)
}

func (w workMethods) RequestArtifactConversion(ctx context.Context, input work.RequestArtifactConversionInput) (*work.RequestArtifactConversionResult, error) {
	if err := w.requireV2(); err != nil {
		return nil, err
	}
	return w.svc.RequestArtifactConversion(ctx, input)
}

func (w workMethods) RecoverArtifactConversions(ctx context.Context, workID string) (int, error) {
	if err := w.requireV2(); err != nil {
		return 0, err
	}
	return w.svc.RecoverArtifactConversions(ctx, workID)
}

var (
	errWorkDisabled   = errors.New("work: feature is disabled; enable [work].enabled in config")
	errWorkV2Disabled = errors.New("work: collaboration workbench V2 is disabled; enable [work].collaboration_workbench_v2 in config")
)

func (w workMethods) requireV2() error {
	if nilutil.IsNil(w.svc) {
		return errWorkDisabled
	}
	if !w.v2 {
		return errWorkV2Disabled
	}
	return nil
}

// WorkControl returns the WorkController port for this session. It is nil-safe:
// when Work is disabled the returned port returns errWorkDisabled for every
// operation.
func (c *Controller) WorkControl() WorkControl {
	if c == nil {
		return workMethods{}
	}
	return workMethods{svc: c.workSvc, v2: c.workV2Enabled, owner: c, refresh: c.workRefresh, sources: c.workSources}
}

// WorkViews returns the WorkViewEvent broadcaster for this session, or nil when
// Work is disabled. Frontends subscribe to receive structured projection updates.
func (c *Controller) WorkViews() *WorkViewBroadcaster {
	if c == nil {
		return nil
	}
	return c.workViews
}

// TaskExecutor returns the Work Task executor, or nil when Work is disabled.
func (c *Controller) TaskExecutor() work.TaskExecutor {
	if c == nil {
		return nil
	}
	return c.taskExec
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

// LookupSessionTurn returns the actual zero-based user turn from a persisted
// Session. LookupSession performs the authoritative SessionDir confinement
// first; metadata previews are never used as turn content.
func (c *Controller) LookupSessionTurn(ctx context.Context, sessionID string, turn int) (string, bool, error) {
	if turn < 0 {
		return "", false, nil
	}
	ref, found, err := c.LookupSession(ctx, sessionID)
	if err != nil || !found {
		return "", found, err
	}
	messages, err := agent.LoadSessionUserMessages(ref.SessionPath)
	if err != nil {
		return "", false, errors.New("work: load Session turn failed")
	}
	if turn >= len(messages) {
		return "", false, nil
	}
	return messages[turn].Text, true, nil
}

var (
	_ WorkService            = (*work.Service)(nil)
	_ work.SessionLookup     = (*Controller)(nil)
	_ SessionTurnLookup      = (*Controller)(nil)
	_ work.PermissionChecker = (*Controller)(nil)
)
