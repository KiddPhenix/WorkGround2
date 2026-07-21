package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workground2/internal/nilutil"
)

// BlockRefreshService is the narrow state-writing port used by the controller-
// owned scheduler. Implementations must persist both successful and failed
// attempts before returning.
type BlockRefreshService interface {
	RefreshBlock(context.Context, RefreshBlockInput, BlockSourceAdapter) (*BlockInstance, error)
}

// BlockRefreshState is an observable scheduler snapshot.
type BlockRefreshState struct {
	Subscribed bool
	Online     bool
	Inflight   bool
	Failures   int
	RetryAt    *time.Time
	LastError  string
}

// BlockSourceResolver resolves trusted source metadata and policy for a block.
type BlockSourceResolver func(BlockInstance) (BlockSourceAdapter, BlockSource, RefreshSchedule, bool)

// refreshTrigger is internal scheduling context. Request IDs are deliberately
// absent: they identify an attempt for persistence but never decide whether or
// when another adapter call is allowed.
type refreshTrigger uint8

const (
	refreshTriggerManual refreshTrigger = iota + 1
	refreshTriggerSchedule
	refreshTriggerReconnect
	refreshTriggerRecover
	refreshTriggerRetry
)

func (trigger refreshTrigger) requestPrefix() string {
	switch trigger {
	case refreshTriggerManual:
		return "manual"
	case refreshTriggerReconnect:
		return "reconnect"
	case refreshTriggerRecover:
		return "recover"
	case refreshTriggerRetry:
		return "retry"
	default:
		return "auto"
	}
}

// BlockRefreshManager runs controller-owned polling with one goroutine and at
// most one in-flight fetch per work/block key.
type BlockRefreshManager struct {
	svc    BlockRefreshService
	clock  Clock
	ctx    context.Context
	cancel context.CancelFunc
	seq    atomic.Uint64

	mu        sync.Mutex
	subs      map[string]*refreshSub
	inflight  map[string]*refreshFlight
	online    bool
	closed    bool
	closeDone chan struct{}
	loopWG    sync.WaitGroup
	flightWG  sync.WaitGroup
}

type refreshFlight struct {
	cancel context.CancelFunc
	done   chan struct{}
	block  *BlockInstance
	err    error
}

type refreshSub struct {
	workID      string
	blockID     string
	adapter     BlockSourceAdapter
	source      BlockSource
	schedule    RefreshSchedule
	failures    int
	nextDelay   time.Duration
	nextTrigger refreshTrigger
	retryAt     *time.Time
	lastError   string
	wake        chan struct{}
	loopCtx     context.Context
	loopCancel  context.CancelFunc
	loopGen     uint64
}

func subKey(workID, blockID string) string { return workID + "/" + blockID }

// NewBlockRefreshManager creates a stopped-empty, online scheduler.
func NewBlockRefreshManager(svc BlockRefreshService, clock Clock) *BlockRefreshManager {
	if clock == nil {
		clock = RealClock{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &BlockRefreshManager{
		svc: svc, clock: clock, ctx: ctx, cancel: cancel, online: true,
		subs: make(map[string]*refreshSub), inflight: make(map[string]*refreshFlight), closeDone: make(chan struct{}),
	}
}

// Subscribe registers one intent. Positive intervals perform the first refresh
// immediately; a zero interval is manual-only and creates no automatic loop.
func (m *BlockRefreshManager) Subscribe(workID, blockID string, adapter BlockSourceAdapter, schedule RefreshSchedule) error {
	return m.subscribeAfter(workID, blockID, adapter, BlockSource{}, schedule, 0, refreshTriggerSchedule)
}

// SubscribeAfter registers one intent with trusted source metadata and an
// initial delay. It is used after a manual refresh and during reopen recovery.
func (m *BlockRefreshManager) SubscribeAfter(workID, blockID string, adapter BlockSourceAdapter, source BlockSource, schedule RefreshSchedule, delay time.Duration) error {
	return m.subscribeAfter(workID, blockID, adapter, source, schedule, delay, refreshTriggerSchedule)
}

func (m *BlockRefreshManager) subscribeAfter(workID, blockID string, adapter BlockSourceAdapter, source BlockSource, schedule RefreshSchedule, delay time.Duration, trigger refreshTrigger) error {
	if m == nil {
		return errors.New("work: BlockRefreshManager is nil")
	}
	if nilutil.IsNil(m.svc) {
		return errors.New("work: block refresh service is unavailable")
	}
	if nilutil.IsNil(adapter) {
		return errors.New("work: block source adapter is required")
	}
	workID, blockID = strings.TrimSpace(workID), strings.TrimSpace(blockID)
	if workID == "" || blockID == "" {
		return errors.New("work: refresh subscription requires workID and blockID")
	}
	var err error
	schedule, err = ValidateRefreshSchedule(schedule)
	if err != nil {
		return err
	}
	if delay < 0 {
		delay = 0
	}
	key := subKey(workID, blockID)
	var startCtx context.Context
	var startSub *refreshSub
	var startGen uint64
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrBlockRefreshStopped
	}
	if existing := m.subs[key]; existing != nil {
		wasAutomatic := existing.schedule.Interval > 0
		existing.adapter = adapter
		existing.source = normalizeRefreshSource(source)
		existing.schedule = schedule
		isAutomatic := schedule.Interval > 0
		if wasAutomatic != isAutomatic {
			existing.loopGen++
			existing.loopCancel()
			existing.loopCtx, existing.loopCancel = context.WithCancel(m.ctx)
			existing.nextDelay = delay
			existing.nextTrigger = trigger
			if isAutomatic {
				startSub, startCtx, startGen = existing, existing.loopCtx, existing.loopGen
				m.loopWG.Add(1)
			}
		}
		m.mu.Unlock()
		if startSub != nil {
			go m.loop(key, startSub, startCtx, startGen)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	sub := &refreshSub{
		workID: workID, blockID: blockID, adapter: adapter,
		source: normalizeRefreshSource(source), schedule: schedule,
		nextDelay: delay, nextTrigger: trigger, wake: make(chan struct{}, 1), loopCtx: ctx, loopCancel: cancel, loopGen: 1,
	}
	m.subs[key] = sub
	if schedule.Interval > 0 {
		startSub, startCtx, startGen = sub, ctx, sub.loopGen
		m.loopWG.Add(1)
	}
	m.mu.Unlock()
	if startSub != nil {
		go m.loop(key, startSub, startCtx, startGen)
	}
	return nil
}

// Unsubscribe cancels an intent and its current fetch. It is idempotent.
func (m *BlockRefreshManager) Unsubscribe(workID, blockID string) {
	if m == nil {
		return
	}
	done := m.BeginUnsubscribe(workID, blockID)
	if done != nil {
		<-done
	}
}

// BeginUnsubscribe atomically removes one intent and cancels its current owner
// flight, returning the stable-receipt barrier. Controller lifecycle code uses
// the two phases to persist cancellation without holding its lock while an
// adapter that ignores context finishes.
func (m *BlockRefreshManager) BeginUnsubscribe(workID, blockID string) <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.unsubscribeKey(subKey(workID, blockID))
}

func (m *BlockRefreshManager) unsubscribeKey(key string) <-chan struct{} {
	var done <-chan struct{}
	m.mu.Lock()
	sub := m.subs[key]
	if sub != nil {
		delete(m.subs, key)
		sub.loopCancel()
	}
	if flight := m.inflight[key]; flight != nil {
		flight.cancel()
		done = flight.done
	}
	m.mu.Unlock()
	return done
}

// UnsubscribeWork cancels every intent owned by one Work.
func (m *BlockRefreshManager) UnsubscribeWork(workID string) {
	if m == nil {
		return
	}
	prefix := strings.TrimSpace(workID) + "/"
	var waits []<-chan struct{}
	m.mu.Lock()
	for key, sub := range m.subs {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		delete(m.subs, key)
		sub.loopCancel()
		if flight := m.inflight[key]; flight != nil {
			flight.cancel()
			waits = append(waits, flight.done)
		}
	}
	m.mu.Unlock()
	for _, done := range waits {
		<-done
	}
}

// RefreshNow performs a synchronous single-flight refresh for a subscription.
func (m *BlockRefreshManager) RefreshNow(ctx context.Context, workID, blockID string) error {
	_, err := m.RefreshRequest(ctx, workID, blockID, "")
	return err
}

// RefreshRequest is RefreshNow with an explicit frontend request ID.
func (m *BlockRefreshManager) RefreshRequest(ctx context.Context, workID, blockID, requestID string) (*BlockInstance, error) {
	if m == nil {
		return nil, errors.New("work: BlockRefreshManager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := subKey(workID, blockID)
	m.mu.Lock()
	_, exists := m.subs[key]
	online := m.online
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, ErrBlockRefreshStopped
	}
	if !exists {
		return nil, fmt.Errorf("work: block %s/%s is not subscribed", workID, blockID)
	}
	if !online {
		return m.persistOffline(ctx, key, requestID, refreshTriggerManual)
	}
	return m.runRefresh(ctx, key, requestID, refreshTriggerManual)
}

type offlineBlockSource struct{}

func (offlineBlockSource) FetchBlock(context.Context, string, BlockInstance) (BlockRefreshResult, error) {
	return BlockRefreshResult{}, ErrSourceUnavailable
}

func (m *BlockRefreshManager) persistOffline(ctx context.Context, key, requestID string, trigger refreshTrigger) (block *BlockInstance, err error) {
	if m == nil || nilutil.IsNil(m.svc) {
		return nil, errors.New("work: block refresh service is unavailable")
	}
	m.mu.Lock()
	if flight := m.inflight[key]; flight != nil {
		done := flight.done
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
			return cloneBlock(flight.block), flight.err
		}
	}
	sub := m.subs[key]
	if m.closed {
		m.mu.Unlock()
		return nil, ErrBlockRefreshStopped
	}
	if sub == nil {
		m.mu.Unlock()
		return nil, ErrBlockNotRefreshable
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	flight := &refreshFlight{cancel: cancel, done: make(chan struct{})}
	m.inflight[key] = flight
	m.flightWG.Add(1)
	failureAttempt := sub.failures + 1
	source, schedule, workID, blockID := sub.source, sub.schedule, sub.workID, sub.blockID
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		flight.block = cloneBlock(block)
		flight.err = err
		delete(m.inflight, key)
		close(flight.done)
		m.mu.Unlock()
		m.flightWG.Done()
	}()
	now := m.clock.Now().UTC()
	delay := schedule.Backoff.Delay(failureAttempt)
	retryAt := now.Add(delay)
	if strings.TrimSpace(requestID) == "" {
		requestID = fmt.Sprintf("%s-offline-block-refresh/%s/%d/%d", trigger.requestPrefix(), key, now.UnixNano(), m.seq.Add(1))
	}
	block, err = m.svc.RefreshBlock(attemptCtx, RefreshBlockInput{
		WorkID: workID, BlockID: blockID, RequestID: requestID,
		Source: source, CheckedAt: now, RetryAt: &retryAt,
	}, offlineBlockSource{})
	if err != nil {
		if isTerminalRefreshError(err) {
			m.unsubscribeKey(key)
			return block, err
		}
		m.finishOfflineAttempt(key, err, delay, &retryAt)
	}
	return block, err
}

func (m *BlockRefreshManager) finishOfflineAttempt(key string, err error, delay time.Duration, retryAt *time.Time) {
	m.mu.Lock()
	sub := m.subs[key]
	if sub == nil {
		m.mu.Unlock()
		return
	}
	sub.failures++
	sub.lastError = clipBlockError(err.Error())
	sub.retryAt = cloneTimePtr(retryAt)
	sub.nextDelay = delay
	if m.online && sub.schedule.Interval > 0 {
		sub.nextDelay = 0
		sub.retryAt = nil
		sub.nextTrigger = refreshTriggerReconnect
		signalRefresh(sub.wake)
	}
	m.mu.Unlock()
}

// SetOnline applies a connectivity transition. Going offline cancels current
// fetches; reconnect wakes automatic intents only. Manual-only intents remain
// subscribed and are refreshed exclusively through RefreshNow/RefreshRequest.
func (m *BlockRefreshManager) SetOnline(online bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed || m.online == online {
		m.mu.Unlock()
		return
	}
	m.online = online
	for key, sub := range m.subs {
		if !online {
			if flight := m.inflight[key]; flight != nil {
				flight.cancel()
			}
		} else if sub.schedule.Interval > 0 {
			sub.nextDelay = 0
			sub.retryAt = nil
			sub.nextTrigger = refreshTriggerReconnect
		}
		if sub.schedule.Interval > 0 {
			signalRefresh(sub.wake)
		}
	}
	m.mu.Unlock()
}

// Online reports the current connectivity gate.
func (m *BlockRefreshManager) Online() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.online && !m.closed
}

// State returns a consistent per-block scheduler snapshot.
func (m *BlockRefreshManager) State(workID, blockID string) BlockRefreshState {
	if m == nil {
		return BlockRefreshState{}
	}
	key := subKey(workID, blockID)
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := m.subs[key]
	if sub == nil {
		return BlockRefreshState{Online: m.online && !m.closed}
	}
	return BlockRefreshState{
		Subscribed: true, Online: m.online && !m.closed,
		Inflight: m.inflight[key] != nil, Failures: sub.failures,
		RetryAt: cloneTimePtr(sub.retryAt), LastError: sub.lastError,
	}
}

func (m *BlockRefreshManager) SubscriberCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs)
}

func (m *BlockRefreshManager) InflightCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inflight)
}

// Close cancels and joins every loop/fetch. Repeated calls are safe.
func (m *BlockRefreshManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		<-done
		return nil
	}
	m.closed = true
	m.cancel()
	for _, sub := range m.subs {
		sub.loopCancel()
	}
	for _, flight := range m.inflight {
		flight.cancel()
	}
	m.subs = make(map[string]*refreshSub)
	m.mu.Unlock()
	m.flightWG.Wait()
	m.loopWG.Wait()
	close(m.closeDone)
	return nil
}

func (m *BlockRefreshManager) loop(key string, sub *refreshSub, loopCtx context.Context, loopGen uint64) {
	defer m.loopWG.Done()
	for {
		delay, online, trigger, ok := m.nextWait(key, sub, loopGen)
		if !ok {
			return
		}
		if !online {
			select {
			case <-loopCtx.Done():
				return
			case <-sub.wake:
				continue
			}
		}
		if delay > 0 {
			select {
			case <-loopCtx.Done():
				return
			case <-sub.wake:
				continue
			case <-m.clock.After(delay):
			}
		}
		_, _ = m.runRefresh(loopCtx, key, "", trigger)
	}
}

func (m *BlockRefreshManager) nextWait(key string, expected *refreshSub, loopGen uint64) (time.Duration, bool, refreshTrigger, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := m.subs[key]
	if sub == nil || sub != expected || sub.loopGen != loopGen || sub.schedule.Interval <= 0 || m.closed {
		return 0, false, 0, false
	}
	delay := sub.nextDelay
	trigger := sub.nextTrigger
	sub.nextDelay = sub.schedule.Interval
	sub.nextTrigger = refreshTriggerSchedule
	return delay, m.online, trigger, true
}

func (m *BlockRefreshManager) runRefresh(parent context.Context, key, requestID string, trigger refreshTrigger) (*BlockInstance, error) {
	if m == nil || nilutil.IsNil(m.svc) {
		return nil, errors.New("work: block refresh service is unavailable")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrBlockRefreshStopped
	}
	if !m.online {
		m.mu.Unlock()
		return nil, ErrSourceUnavailable
	}
	if flight := m.inflight[key]; flight != nil {
		done := flight.done
		m.mu.Unlock()
		select {
		case <-parent.Done():
			return nil, parent.Err()
		case <-done:
			return cloneBlock(flight.block), flight.err
		}
	}
	sub := m.subs[key]
	if sub == nil {
		m.mu.Unlock()
		return nil, ErrBlockNotRefreshable
	}
	adapter, source, schedule := sub.adapter, sub.source, sub.schedule
	if nilutil.IsNil(adapter) {
		m.mu.Unlock()
		return nil, errors.New("work: block source adapter is unavailable")
	}
	attemptCtx, cancel := context.WithCancel(parent)
	flight := &refreshFlight{cancel: cancel, done: make(chan struct{})}
	m.inflight[key] = flight
	m.flightWG.Add(1)
	failureAttempt := sub.failures + 1
	m.mu.Unlock()

	var block *BlockInstance
	var err error
	defer func() {
		cancel()
		m.mu.Lock()
		flight.block = cloneBlock(block)
		flight.err = err
		delete(m.inflight, key)
		close(flight.done)
		m.mu.Unlock()
		m.flightWG.Done()
	}()
	now := m.clock.Now().UTC()
	delay := schedule.Backoff.Delay(failureAttempt)
	retryAt := now.Add(delay)
	if strings.TrimSpace(requestID) == "" {
		requestID = fmt.Sprintf("%s-block-refresh/%s/%d/%d", trigger.requestPrefix(), key, now.UnixNano(), m.seq.Add(1))
	}
	block, err = m.svc.RefreshBlock(attemptCtx, RefreshBlockInput{
		WorkID: sub.workID, BlockID: sub.blockID, RequestID: requestID,
		Source: source, CheckedAt: now, RetryAt: &retryAt,
	}, adapter)
	if err == nil {
		m.finishAttempt(key, nil, 0, nil)
		return block, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return block, err
	}
	if isTerminalRefreshError(err) {
		m.unsubscribeKey(key)
		return block, err
	}
	if errors.Is(err, ErrBlockRefreshFailed) {
		return block, err
	}
	m.finishAttempt(key, err, delay, &retryAt)
	return block, err
}

func (m *BlockRefreshManager) finishAttempt(key string, err error, delay time.Duration, retryAt *time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := m.subs[key]
	if sub == nil {
		return
	}
	if err == nil {
		sub.failures, sub.lastError, sub.retryAt = 0, "", nil
		sub.nextDelay = sub.schedule.Interval
		sub.nextTrigger = refreshTriggerSchedule
		return
	}
	sub.failures++
	sub.lastError = clipBlockError(err.Error())
	sub.retryAt = cloneTimePtr(retryAt)
	sub.nextDelay = delay
	sub.nextTrigger = refreshTriggerRetry
	if sub.schedule.MaxFailures > 0 && sub.failures >= sub.schedule.MaxFailures {
		sub.nextDelay = sub.schedule.Interval
		sub.nextTrigger = refreshTriggerSchedule
	}
}

// RecoverFromProjection restores only persisted refresh intents. RetryAt is
// honoured, so restart cannot turn a backoff window into an immediate storm.
func (m *BlockRefreshManager) RecoverFromProjection(view *WorkView, resolve BlockSourceResolver) error {
	if m == nil || view == nil || view.Work == nil || resolve == nil || view.Work.ArchiveState != ArchiveActive {
		return nil
	}
	var recoverErr error
	for i := range view.Work.Blocks {
		block := view.Work.Blocks[i]
		if block.Tombstone || block.Freshness == nil {
			continue
		}
		adapter, source, schedule, ok := resolve(block)
		if !ok || nilutil.IsNil(adapter) {
			continue
		}
		delay := time.Duration(0)
		if block.Freshness.RetryAt != nil && block.Freshness.RetryAt.After(m.clock.Now()) {
			delay = block.Freshness.RetryAt.Sub(m.clock.Now())
		}
		if err := m.subscribeAfter(view.Work.ID, block.ID, adapter, source, schedule, delay, refreshTriggerRecover); err != nil {
			recoverErr = errors.Join(recoverErr, err)
		}
	}
	return recoverErr
}

// ValidateRefreshSchedule applies safe defaults without changing the meaning
// of Interval=0, which is the explicit manual-only mode.
func ValidateRefreshSchedule(schedule RefreshSchedule) (RefreshSchedule, error) {
	if schedule.Interval < 0 {
		return RefreshSchedule{}, errors.New("work: refresh interval must be non-negative")
	}
	if schedule.Backoff == nil {
		schedule.Backoff = NewExponentialBackoff()
	}
	return schedule, nil
}

func isTerminalRefreshError(err error) bool {
	return errors.Is(err, ErrBlockRefreshStopped) || errors.Is(err, ErrBlockNotRefreshable) ||
		errors.Is(err, ErrBlockNotFound) || errors.Is(err, ErrWorkNotFound)
}

func signalRefresh(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
