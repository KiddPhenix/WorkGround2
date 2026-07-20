package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	wg        sync.WaitGroup
}

type refreshFlight struct {
	cancel context.CancelFunc
	done   chan struct{}
	block  *BlockInstance
	err    error
}

type refreshSub struct {
	workID    string
	blockID   string
	adapter   BlockSourceAdapter
	source    BlockSource
	schedule  RefreshSchedule
	failures  int
	nextDelay time.Duration
	retryAt   *time.Time
	lastError string
	wake      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
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

// Subscribe registers one intent and performs the first refresh immediately.
// Repeating the same key is a no-op and never creates another loop.
func (m *BlockRefreshManager) Subscribe(workID, blockID string, adapter BlockSourceAdapter, schedule RefreshSchedule) {
	m.SubscribeAfter(workID, blockID, adapter, BlockSource{}, schedule, 0)
}

// SubscribeAfter registers one intent with trusted source metadata and an
// initial delay. It is used after a manual refresh and during reopen recovery.
func (m *BlockRefreshManager) SubscribeAfter(workID, blockID string, adapter BlockSourceAdapter, source BlockSource, schedule RefreshSchedule, delay time.Duration) {
	if m == nil || adapter == nil {
		return
	}
	workID, blockID = strings.TrimSpace(workID), strings.TrimSpace(blockID)
	if workID == "" || blockID == "" {
		return
	}
	schedule = normalizeRefreshSchedule(schedule)
	if delay < 0 {
		delay = 0
	}
	key := subKey(workID, blockID)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if existing := m.subs[key]; existing != nil {
		existing.adapter = adapter
		existing.source = normalizeRefreshSource(source)
		existing.schedule = schedule
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	sub := &refreshSub{
		workID: workID, blockID: blockID, adapter: adapter,
		source: normalizeRefreshSource(source), schedule: schedule,
		nextDelay: delay, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel,
	}
	m.subs[key] = sub
	m.wg.Add(1)
	m.mu.Unlock()
	go m.loop(key, sub)
}

// Unsubscribe cancels an intent and its current fetch. It is idempotent.
func (m *BlockRefreshManager) Unsubscribe(workID, blockID string) {
	if m == nil {
		return
	}
	m.unsubscribeKey(subKey(workID, blockID), true)
}

func (m *BlockRefreshManager) unsubscribeKey(key string, wait bool) {
	var done <-chan struct{}
	m.mu.Lock()
	sub := m.subs[key]
	if sub != nil {
		delete(m.subs, key)
		sub.cancel()
	}
	if flight := m.inflight[key]; flight != nil {
		flight.cancel()
		done = flight.done
	}
	m.mu.Unlock()
	if wait && done != nil {
		<-done
	}
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
		sub.cancel()
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
	m.mu.Unlock()
	if !exists {
		return nil, fmt.Errorf("work: block %s/%s is not subscribed", workID, blockID)
	}
	if !online {
		return m.persistOffline(ctx, key, requestID)
	}
	explicit := strings.TrimSpace(requestID) != ""
	block, err := m.runRefresh(ctx, key, requestID)
	if err != nil && !explicit && m.Online() {
		block, err = m.runRefresh(ctx, key, "")
	}
	return block, err
}

type offlineBlockSource struct{}

func (offlineBlockSource) FetchBlock(context.Context, string, BlockInstance) (BlockRefreshResult, error) {
	return BlockRefreshResult{}, ErrSourceUnavailable
}

func (m *BlockRefreshManager) persistOffline(ctx context.Context, key, requestID string) (block *BlockInstance, err error) {
	m.mu.Lock()
	if flight := m.inflight[key]; flight != nil {
		done := flight.done
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
			return m.persistOffline(ctx, key, requestID)
		}
	}
	sub := m.subs[key]
	if sub == nil || m.closed {
		m.mu.Unlock()
		return nil, ErrBlockNotRefreshable
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	flight := &refreshFlight{cancel: cancel, done: make(chan struct{})}
	m.inflight[key] = flight
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
	}()
	now := m.clock.Now().UTC()
	delay := schedule.Backoff.Delay(failureAttempt)
	retryAt := now.Add(delay)
	if strings.TrimSpace(requestID) == "" {
		requestID = fmt.Sprintf("offline-block-refresh/%s/%d/%d", key, now.UnixNano(), m.seq.Add(1))
	}
	block, err = m.svc.RefreshBlock(attemptCtx, RefreshBlockInput{
		WorkID: workID, BlockID: blockID, RequestID: requestID,
		Source: source, CheckedAt: now, RetryAt: &retryAt,
	}, offlineBlockSource{})
	if err != nil {
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
	if m.online {
		sub.nextDelay = 0
		sub.retryAt = nil
		signalRefresh(sub.wake)
	}
	m.mu.Unlock()
}

// SetOnline applies a connectivity transition. Going offline cancels current
// fetches; reconnect wakes every retained intent for an immediate retry.
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
		} else {
			sub.nextDelay = 0
			sub.retryAt = nil
		}
		signalRefresh(sub.wake)
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
		sub.cancel()
	}
	for _, flight := range m.inflight {
		flight.cancel()
	}
	m.subs = make(map[string]*refreshSub)
	m.mu.Unlock()
	m.wg.Wait()
	close(m.closeDone)
	return nil
}

func (m *BlockRefreshManager) loop(key string, sub *refreshSub) {
	defer m.wg.Done()
	for {
		delay, online, ok := m.nextWait(key)
		if !ok {
			return
		}
		if !online {
			select {
			case <-sub.ctx.Done():
				return
			case <-sub.wake:
				continue
			}
		}
		if delay > 0 {
			select {
			case <-sub.ctx.Done():
				return
			case <-sub.wake:
				continue
			case <-m.clock.After(delay):
			}
		}
		_, _ = m.runRefresh(sub.ctx, key, "")
	}
}

func (m *BlockRefreshManager) nextWait(key string) (time.Duration, bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := m.subs[key]
	if sub == nil || m.closed {
		return 0, false, false
	}
	delay := sub.nextDelay
	sub.nextDelay = sub.schedule.Interval
	return delay, m.online, true
}

func (m *BlockRefreshManager) runRefresh(parent context.Context, key, requestID string) (*BlockInstance, error) {
	m.mu.Lock()
	if m.closed || !m.online {
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
	attemptCtx, cancel := context.WithCancel(parent)
	flight := &refreshFlight{cancel: cancel, done: make(chan struct{})}
	m.inflight[key] = flight
	adapter, source, schedule := sub.adapter, sub.source, sub.schedule
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
	}()
	now := m.clock.Now().UTC()
	delay := schedule.Backoff.Delay(failureAttempt)
	retryAt := now.Add(delay)
	if strings.TrimSpace(requestID) == "" {
		requestID = fmt.Sprintf("auto-block-refresh/%s/%d/%d", key, now.UnixNano(), m.seq.Add(1))
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
	if errors.Is(err, ErrBlockRefreshStopped) {
		m.unsubscribeKey(key, false)
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
		return
	}
	sub.failures++
	sub.lastError = clipBlockError(err.Error())
	sub.retryAt = cloneTimePtr(retryAt)
	sub.nextDelay = delay
	if sub.schedule.MaxFailures > 0 && sub.failures >= sub.schedule.MaxFailures {
		sub.nextDelay = sub.schedule.Interval
	}
}

// RecoverFromProjection restores only persisted refresh intents. RetryAt is
// honoured, so restart cannot turn a backoff window into an immediate storm.
func (m *BlockRefreshManager) RecoverFromProjection(view *WorkView, resolve BlockSourceResolver) {
	if m == nil || view == nil || view.Work == nil || resolve == nil || view.Work.ArchiveState != ArchiveActive {
		return
	}
	for i := range view.Work.Blocks {
		block := view.Work.Blocks[i]
		if block.Tombstone || block.Freshness == nil {
			continue
		}
		adapter, source, schedule, ok := resolve(block)
		if !ok || adapter == nil {
			continue
		}
		delay := time.Duration(0)
		if block.Freshness.RetryAt != nil && block.Freshness.RetryAt.After(m.clock.Now()) {
			delay = block.Freshness.RetryAt.Sub(m.clock.Now())
		}
		m.SubscribeAfter(view.Work.ID, block.ID, adapter, source, schedule, delay)
	}
}

func normalizeRefreshSchedule(schedule RefreshSchedule) RefreshSchedule {
	if schedule.Interval <= 0 {
		schedule.Interval = DefaultRefreshSchedule().Interval
	}
	if schedule.Backoff == nil {
		schedule.Backoff = NewExponentialBackoff()
	}
	return schedule
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
