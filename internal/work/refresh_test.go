package work

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── FakeClock ───────────────────────────────────────────────────────────────

type fakeClock struct {
	mu       sync.Mutex
	now      time.Time
	triggers map[time.Time][]chan time.Time
	epoch    time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		triggers: make(map[time.Time][]chan time.Time),
		epoch:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (c *fakeClock) Now() time.Time {
	if c == nil {
		return time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	if c == nil {
		return time.After(d)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	target := c.now.Add(d)
	ch := make(chan time.Time, 1)
	c.triggers[target] = append(c.triggers[target], ch)
	return ch
}

func (c *fakeClock) advance(d time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	// Fire all triggers at or before the new now.
	for t, chs := range c.triggers {
		if !t.After(c.now) {
			for _, ch := range chs {
				select {
				case ch <- t:
				default:
				}
			}
			delete(c.triggers, t)
		}
	}
	c.mu.Unlock()
}

func (c *fakeClock) advanceTo(t time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.now = t
	for tt, chs := range c.triggers {
		if !tt.After(c.now) {
			for _, ch := range chs {
				select {
				case ch <- tt:
				default:
				}
			}
			delete(c.triggers, tt)
		}
	}
	c.mu.Unlock()
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, triggers := range c.triggers {
		total += len(triggers)
	}
	return total
}

func waitForTimer(t *testing.T, clock *fakeClock) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if clock.pending() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("refresh loop did not register a timer")
}

// ── FakeBlockSource ─────────────────────────────────────────────────────────

type fakeBlockSource struct {
	mu       sync.Mutex
	results  []BlockRefreshResult // pre-programmed results (FIFO)
	errs     []error              // pre-programmed errors (FIFO)
	calls    int32
	lastWork string
	lastKind string

	// Dynamic control.
	onFetch func(workID string, block BlockInstance) (BlockRefreshResult, error)
}

func newFakeBlockSource(results ...BlockRefreshResult) *fakeBlockSource {
	return &fakeBlockSource{results: results}
}

func (s *fakeBlockSource) FetchBlock(ctx context.Context, workID string, block BlockInstance) (BlockRefreshResult, error) {
	atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	s.lastWork = workID
	s.lastKind = block.Kind
	s.mu.Unlock()

	if s.onFetch != nil {
		return s.onFetch(workID, block)
	}

	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return BlockRefreshResult{}, err
		}
	}

	if len(s.results) > 0 {
		r := s.results[0]
		s.results = s.results[1:]
		return r, nil
	}

	return BlockRefreshResult{}, errors.New("fake: no more results")
}

func (s *fakeBlockSource) Calls() int32 {
	return atomic.LoadInt32(&s.calls)
}

func (s *fakeBlockSource) LastWork() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastWork
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func checklistResult(kind string, schemaVersion int) BlockRefreshResult {
	items := []map[string]any{
		{"id": "1", "text": "item one", "checked": true},
		{"id": "2", "text": "item two", "checked": false},
	}
	data, _ := json.Marshal(map[string]any{"items": items})
	return BlockRefreshResult{
		Kind:          kind,
		SchemaVersion: schemaVersion,
		Data:          data,
		Status:        BlockReady,
		Freshness:     &BlockFreshness{},
		Fallback:      BlockFallback{Summary: "2 items"},
	}
}

func createWorkWithBlock(t *testing.T, f *serviceFixture, requestID string) *Work {
	t.Helper()
	input := CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: "blueprint:blank", SchemaVersion: SchemaVersion, Version: 1},
		Name:         "Refresh Test",
		Inputs:       map[string]any{},
		RequestID:    requestID,
	}
	value, err := f.svc.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The blank blueprint creates a "markdown" block with ID "bp-blank-notes".
	// We use that as our refresh target.
	return value
}

func blockKind() string { return "markdown" }

func blockResult() BlockRefreshResult {
	return BlockRefreshResult{
		Kind:          blockKind(),
		SchemaVersion: 1,
		Data:          json.RawMessage(`{"content":"refreshed content"}`),
		Status:        BlockReady,
		Freshness:     &BlockFreshness{},
		Fallback:      BlockFallback{Summary: "updated"},
	}
}

func refreshInput(workID, blockID, requestID string) RefreshBlockInput {
	return RefreshBlockInput{
		WorkID: workID, BlockID: blockID, RequestID: requestID,
		Source:    BlockSource{Provider: "controller", Mode: "snapshot", Verified: true},
		CheckedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// waitForCalls blocks until the fake source has been called at least n times
// or the timeout expires. It fails the test on timeout.
func waitForCalls(t *testing.T, s *fakeBlockSource, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&s.calls) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("adapter calls = %d, want >= %d after %v", atomic.LoadInt32(&s.calls), want, timeout)
}

// ── Service.RefreshBlock tests ──────────────────────────────────────────────

func TestRefreshBlockSuccess(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-success")
	blockID := value.Blocks[0].ID

	adapter := newFakeBlockSource(blockResult())
	block, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-success-req"), adapter)
	if err != nil {
		t.Fatalf("RefreshBlock: %v", err)
	}
	if block.Status != BlockReady {
		t.Fatalf("block status = %s, want ready", block.Status)
	}
	if block.Source.Provider != "controller" || !block.Source.Verified {
		t.Fatalf("block source = %+v, want controller/verified", block.Source)
	}
	if block.Freshness == nil || block.Freshness.CheckedAt == nil {
		t.Fatal("block freshness missing CheckedAt")
	}
	if block.Fallback.Summary != "updated" {
		t.Fatalf("block fallback summary = %q, want updated", block.Fallback.Summary)
	}
	if adapter.Calls() != 1 {
		t.Fatalf("adapter calls = %d, want 1", adapter.Calls())
	}
}

func TestRefreshBlockIdempotent(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-idem")
	blockID := value.Blocks[0].ID

	adapter := newFakeBlockSource(blockResult())
	first, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-idem-req"), adapter)
	if err != nil {
		t.Fatalf("first RefreshBlock: %v", err)
	}

	// Same requestID with fresh adapter — the requestID is already committed,
	// so the adapter should not be called and the same result returned.
	secondAdapter := newFakeBlockSource(blockResult())
	second, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-idem-req"), secondAdapter)
	if err != nil {
		t.Fatalf("idempotent RefreshBlock: %v", err)
	}
	if second.Revision != first.Revision {
		t.Fatalf("idempotent refresh changed revision from %d to %d", first.Revision, second.Revision)
	}
	// Second adapter should not have been called because the requestID was idempotent.
	if secondAdapter.Calls() != 0 {
		t.Fatalf("adapter called %d times on idempotent retry, want 0", secondAdapter.Calls())
	}
}

func TestRefreshBlockOutOfOrder(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-ooo")
	blockID := value.Blocks[0].ID

	// First refresh: revision 2 → 3
	adapter1 := newFakeBlockSource(blockResult())
	_, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-ooo-1"), adapter1)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// A late refresh with an older revision should be silently skipped (no error,
	// no state change). We simulate this with a stale BlockUpsertInput.
	lateInput := BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          blockID,
		Kind:             blockKind(),
		SchemaVersion:    1,
		Revision:         1, // older than current block revision (3)
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"stale data"}`),
		Source:           BlockSource{Provider: "tool:git", Mode: "snapshot"},
		ExpectedRevision: 3,
		RequestID:        "refresh-ooo-late",
	}
	view, err := f.svc.UpsertBlock(context.Background(), lateInput)
	if err != nil {
		t.Fatalf("late upsert should be silently skipped, got: %v", err)
	}
	// Block should not have changed. After the first refresh, the block revision
	// is 2. The late input has revision 1 < 2, so it's silently skipped.
	for _, b := range view.Work.Blocks {
		if b.ID == blockID && b.Revision != 2 {
			t.Fatalf("block revision = %d after late delivery, want 2 (unchanged)", b.Revision)
		}
	}
}

func TestRefreshBlockWrongKind(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-wrongkind")
	blockID := value.Blocks[0].ID

	// Adapter returns "code" but the block is "markdown" (from blank blueprint).
	adapter := newFakeBlockSource()
	adapter.onFetch = func(workID string, block BlockInstance) (BlockRefreshResult, error) {
		return BlockRefreshResult{
			Kind:          "code",
			SchemaVersion: 1,
			Data:          json.RawMessage(`{"language":"go","content":"package main"}`),
			Status:        BlockReady,
			Fallback:      BlockFallback{Summary: "x"},
		}, nil
	}
	_, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-wrongkind-req"), adapter)
	if err == nil {
		t.Fatal("expected error for kind mismatch")
	}
	if !strings.Contains(err.Error(), "kind") && !strings.Contains(err.Error(), "expects") {
		t.Fatalf("error = %v, want kind mismatch", err)
	}
}

func TestRefreshBlockSourceUnavailable(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-unavail")
	blockID := value.Blocks[0].ID

	adapter := newFakeBlockSource()
	adapter.onFetch = func(workID string, block BlockInstance) (BlockRefreshResult, error) {
		return BlockRefreshResult{}, ErrSourceUnavailable
	}
	_, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-unavail-req"), adapter)
	if err == nil {
		t.Fatal("expected error for unavailable source")
	}
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("error = %v, want ErrSourceUnavailable", err)
	}
}

func TestRefreshBlockFailurePersistsAndReplays(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-failure-persist")
	blockID := value.Blocks[0].ID
	checkedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	retryAt := checkedAt.Add(7 * time.Second)
	adapter := newFakeBlockSource()
	adapter.onFetch = func(string, BlockInstance) (BlockRefreshResult, error) {
		return BlockRefreshResult{}, ErrSourceUnavailable
	}
	input := refreshInput(value.ID, blockID, "refresh-failure-persist-req")
	input.CheckedAt, input.RetryAt = checkedAt, &retryAt

	failed, err := f.svc.RefreshBlock(context.Background(), input, adapter)
	if !errors.Is(err, ErrSourceUnavailable) || failed == nil {
		t.Fatalf("RefreshBlock failure = (%+v, %v), want persisted source error", failed, err)
	}
	if failed.Status != BlockStale && failed.Status != BlockFailed {
		t.Fatalf("status = %s, want stale/failed", failed.Status)
	}
	if failed.Freshness == nil || failed.Freshness.CheckedAt == nil || !failed.Freshness.CheckedAt.Equal(checkedAt) ||
		failed.Freshness.RetryAt == nil || !failed.Freshness.RetryAt.Equal(retryAt) || failed.Freshness.StaleReason == "" {
		t.Fatalf("failure freshness = %+v", failed.Freshness)
	}
	view, getErr := f.svc.Get(context.Background(), value.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	persisted := findBlock(view.Work, blockID)
	if persisted == nil || persisted.Revision != failed.Revision || persisted.Freshness == nil {
		t.Fatalf("persisted failure = %+v, returned = %+v", persisted, failed)
	}

	replayed, replayErr := f.svc.RefreshBlock(context.Background(), input, newFakeBlockSource(blockResult()))
	if !errors.Is(replayErr, ErrBlockRefreshFailed) || replayed == nil || adapter.Calls() != 1 {
		t.Fatalf("failure replay = (%+v, %v), calls=%d", replayed, replayErr, adapter.Calls())
	}
}

func TestRefreshBlockLateResultCannotOverwriteNewerResult(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-late-source")
	blockID := value.Blocks[0].ID
	started := make(chan struct{})
	release := make(chan struct{})
	late := newFakeBlockSource()
	late.onFetch = func(string, BlockInstance) (BlockRefreshResult, error) {
		close(started)
		<-release
		result := blockResult()
		result.Data = json.RawMessage(`{"content":"late"}`)
		return result, nil
	}
	lateDone := make(chan error, 1)
	go func() {
		_, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-late-1"), late)
		lateDone <- err
	}()
	<-started
	fresh := newFakeBlockSource()
	fresh.onFetch = func(string, BlockInstance) (BlockRefreshResult, error) {
		result := blockResult()
		result.Data = json.RawMessage(`{"content":"newer"}`)
		return result, nil
	}
	if _, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-late-2"), fresh); err != nil {
		t.Fatalf("newer refresh: %v", err)
	}
	close(release)
	if err := <-lateDone; err == nil {
		t.Fatal("late concurrent refresh should report a retryable conflict")
	}
	view, _ := f.svc.Get(context.Background(), value.ID)
	if got := string(findBlock(view.Work, blockID).Data); !strings.Contains(got, "newer") {
		t.Fatalf("late result overwrote newer data: %s", got)
	}
}

func TestRefreshBlockUpdatesNonEditableSourceBlock(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-noneditable")
	current, state, err := f.store.LoadState(value.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	current.Definition.BlockSpecs[0].Editable = false
	if err := f.store.WriteProjection(value.ID, current, state.Revision); err != nil {
		t.Fatal(err)
	}
	blockID := current.Blocks[0].ID
	if _, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-noneditable-req"), newFakeBlockSource(blockResult())); err != nil {
		t.Fatalf("controlled source refresh rejected non-editable block: %v", err)
	}
}

func TestRefreshBlockRejectsAddonInjectionAndOversize(t *testing.T) {
	for _, tc := range []struct {
		name string
		data json.RawMessage
	}{
		{name: "renderer", data: json.RawMessage(`{"content":"ok","renderer":{"component":"Injected"}}`)},
		{name: "schema", data: json.RawMessage(`{"unexpected":true}`)},
		{name: "oversize", data: json.RawMessage(`{"content":"` + strings.Repeat("x", BlockInlineMaxBytes) + `"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServiceFixture(t)
			value := createWorkWithBlock(t, f, "refresh-addon-"+tc.name)
			result := blockResult()
			result.Data = tc.data
			block, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, value.Blocks[0].ID, "refresh-addon-req-"+tc.name), newFakeBlockSource(result))
			if err == nil || block == nil || block.Status != BlockFailed {
				t.Fatalf("invalid addon result = (%+v, %v), want persisted failed", block, err)
			}
		})
	}
}

func TestRefreshBlockCancellationDoesNotPersistFailure(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-cancel-no-event")
	ctx, cancel := context.WithCancel(context.Background())
	adapter := newFakeBlockSource()
	adapter.onFetch = func(string, BlockInstance) (BlockRefreshResult, error) {
		cancel()
		return BlockRefreshResult{}, context.Canceled
	}
	before, _ := f.svc.Get(context.Background(), value.ID)
	if _, err := f.svc.RefreshBlock(ctx, refreshInput(value.ID, value.Blocks[0].ID, "refresh-cancel-no-event-req"), adapter); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	after, _ := f.svc.Get(context.Background(), value.ID)
	if after.Revision != before.Revision {
		t.Fatalf("cancel persisted revision %d -> %d", before.Revision, after.Revision)
	}
}

func TestRefreshBlockArchiveStops(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-archive")
	blockID := value.Blocks[0].ID

	// Archive the work.
	_, err := f.svc.Archive(context.Background(), value.ID, "refresh-archive-req")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}

	adapter := newFakeBlockSource(blockResult())
	_, err = f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-archive-req2"), adapter)
	if err == nil {
		t.Fatal("expected error for archived work")
	}
	if !strings.Contains(err.Error(), "archived") {
		t.Fatalf("error = %v, want archive-related message", err)
	}
}

func TestRefreshBlockNilAdapter(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "refresh-niladapter")
	blockID := value.Blocks[0].ID

	_, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "refresh-niladapter-req"), nil)
	if err == nil {
		t.Fatal("expected error for nil adapter")
	}
	if !strings.Contains(err.Error(), "adapter") && !strings.Contains(err.Error(), "Adapter") {
		t.Fatalf("error = %v, want adapter-related message", err)
	}
}

// ── BlockRefreshManager tests ───────────────────────────────────────────────

func TestRefreshManagerSubscribeRefresh(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-sub")
	blockID := value.Blocks[0].ID

	clock := newFakeClock()
	adapter := newFakeBlockSource(
		blockResult(),
		blockResult(),
		blockResult(),
	)

	mgr := NewBlockRefreshManager(f.svc, clock)
	defer mgr.Close()

	mgr.Subscribe(value.ID, blockID, adapter, RefreshSchedule{
		Interval: 10 * time.Second,
		Backoff:  NewExponentialBackoff(),
	})

	// First refresh runs immediately.
	clock.advance(0)                   // trigger immediate tick
	time.Sleep(100 * time.Millisecond) // let goroutine run

	if adapter.Calls() == 0 {
		t.Fatal("expected at least one adapter call after subscribe")
	}

	// Verify block was updated in projection.
	view, err := f.svc.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var updated BlockInstance
	for _, b := range view.Work.Blocks {
		if b.ID == blockID {
			updated = b
			break
		}
	}
	if updated.Status != BlockReady {
		t.Fatalf("block status = %s, want ready after refresh", updated.Status)
	}
	if updated.Source.Provider != "controller" {
		t.Fatalf("source = %s, want controller", updated.Source.Provider)
	}
}

func TestRefreshManagerSingleFlight(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-single")
	blockID := value.Blocks[0].ID

	clock := newFakeClock()
	adapter := newFakeBlockSource()
	fetched := make(chan struct{}, 3)
	adapter.onFetch = func(workID string, block BlockInstance) (BlockRefreshResult, error) {
		fetched <- struct{}{}
		// Slow fetch.
		time.Sleep(200 * time.Millisecond)
		return blockResult(), nil
	}

	mgr := NewBlockRefreshManager(f.svc, clock)
	defer mgr.Close()

	mgr.Subscribe(value.ID, blockID, adapter, RefreshSchedule{
		Interval: 10 * time.Second,
		Backoff:  NewExponentialBackoff(),
	})

	// After subscribe, the first refresh starts immediately.
	clock.advance(0)
	time.Sleep(50 * time.Millisecond)

	// Concurrent callers while the first refresh is in-flight join that flight.
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.RefreshNow(context.Background(), value.ID, blockID)
		}()
	}
	wg.Wait()

	// Count actual fetches (not calls — calls counts all queued results).
	close(fetched)
	count := 0
	for range fetched {
		count++
	}
	if count != 1 {
		t.Fatalf("fetch count = %d, want 1 (single-flight)", count)
	}
}

func TestRefreshManagerUnsubscribe(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-unsub")
	blockID := value.Blocks[0].ID

	clock := newFakeClock()
	adapter := newFakeBlockSource(blockResult(), blockResult())

	mgr := NewBlockRefreshManager(f.svc, clock)
	mgr.Subscribe(value.ID, blockID, adapter, RefreshSchedule{
		Interval: 1 * time.Second,
		Backoff:  NewExponentialBackoff(),
	})

	if mgr.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d, want 1", mgr.SubscriberCount())
	}

	clock.advance(0)
	time.Sleep(100 * time.Millisecond)

	mgr.Unsubscribe(value.ID, blockID)
	if mgr.SubscriberCount() != 0 {
		t.Fatalf("subscriber count = %d, want 0 after unsubscribe", mgr.SubscriberCount())
	}

	// Idempotent unsubscribe.
	mgr.Unsubscribe(value.ID, blockID)
	mgr.Unsubscribe("nonexistent", "block")
}

func TestRefreshManagerCancelStopsLoop(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-cancel")
	blockID := value.Blocks[0].ID

	clock := newFakeClock()
	callsBefore := int32(0)
	adapter := newFakeBlockSource()
	adapter.onFetch = func(workID string, block BlockInstance) (BlockRefreshResult, error) {
		atomic.AddInt32(&callsBefore, 1)
		return blockResult(), nil
	}

	mgr := NewBlockRefreshManager(f.svc, clock)
	mgr.Subscribe(value.ID, blockID, adapter, RefreshSchedule{
		Interval: 100 * time.Millisecond,
		Backoff:  NewExponentialBackoff(),
	})

	// Let a couple ticks run.
	clock.advance(0)
	time.Sleep(50 * time.Millisecond)
	clock.advance(200 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	ca := atomic.LoadInt32(&callsBefore)
	if ca < 1 {
		t.Fatalf("expected at least 1 call before cancel, got %d", ca)
	}

	mgr.Unsubscribe(value.ID, blockID)
	after1 := atomic.LoadInt32(&callsBefore)

	// Advance clock to trigger more ticks.
	clock.advance(5 * time.Second)
	time.Sleep(200 * time.Millisecond)

	after2 := atomic.LoadInt32(&callsBefore)
	if after2 != after1 {
		t.Fatalf("calls increased from %d to %d after unsubscribe", after1, after2)
	}
}

func TestRefreshManagerOfflineReconnect(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-offline")
	blockID := value.Blocks[0].ID

	clock := newFakeClock()
	state := &struct {
		mu      sync.Mutex
		offline bool
		calls   int
	}{offline: true}

	adapter := newFakeBlockSource()
	adapter.onFetch = func(workID string, block BlockInstance) (BlockRefreshResult, error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.calls++
		if state.offline {
			return BlockRefreshResult{}, ErrSourceUnavailable
		}
		return blockResult(), nil
	}

	mgr := NewBlockRefreshManager(f.svc, clock)
	defer mgr.Close()

	mgr.Subscribe(value.ID, blockID, adapter, RefreshSchedule{
		Interval: 10 * time.Second,
		Backoff:  NewExponentialBackoff(),
	})

	// Wait for first (failing) attempt.
	waitForCalls(t, adapter, 1, 3*time.Second)

	// Reconnect.
	state.mu.Lock()
	state.offline = false
	state.mu.Unlock()

	// Trigger immediate refresh via RefreshNow.
	if err := mgr.RefreshNow(context.Background(), value.ID, blockID); err != nil {
		t.Fatalf("RefreshNow after reconnect: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Verify block is now ready.
	view, err := f.svc.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("Get after reconnect: %v", err)
	}
	for _, b := range view.Work.Blocks {
		if b.ID == blockID && b.Status == BlockReady {
			return
		}
	}
	t.Fatal("block did not reach ready status after reconnect")
}

func TestRefreshManagerClose(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-close")
	blockID := value.Blocks[0].ID

	clock := newFakeClock()
	adapter := newFakeBlockSource(blockResult())

	mgr := NewBlockRefreshManager(f.svc, clock)
	mgr.Subscribe(value.ID, blockID, adapter, DefaultRefreshSchedule())

	clock.advance(0)
	time.Sleep(50 * time.Millisecond)

	err := mgr.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Double close must be safe.
	err = mgr.Close()
	if err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// Subscribe after close is a no-op.
	mgr.Subscribe(value.ID, blockID, adapter, DefaultRefreshSchedule())
	if mgr.SubscriberCount() != 0 {
		t.Fatal("Subscribe after Close should be no-op")
	}
}

func TestRefreshManagerRecoverFromProjection(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-recover-create")
	blockID := value.Blocks[0].ID

	// First, do a successful refresh so the block has freshness metadata.
	adapter1 := newFakeBlockSource(blockResult())
	_, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "mgr-recover-refresh"), adapter1)
	if err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	// Reload the projection.
	view, err := f.svc.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Now create a new manager and recover from projection.
	clock := newFakeClock()
	adapter2 := newFakeBlockSource(blockResult())
	mgr := NewBlockRefreshManager(f.svc, clock)
	defer mgr.Close()

	mgr.RecoverFromProjection(view, func(block BlockInstance) (BlockSourceAdapter, BlockSource, RefreshSchedule, bool) {
		return adapter2, BlockSource{Provider: "controller", Mode: "snapshot"}, DefaultRefreshSchedule(), block.Kind == blockKind()
	})

	if mgr.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d after recover, want 1", mgr.SubscriberCount())
	}

	// Let the recovered subscription run once.
	clock.advance(0)
	time.Sleep(100 * time.Millisecond)

	if adapter2.Calls() < 1 {
		t.Fatal("recovered subscription did not trigger refresh")
	}
}

func TestRefreshManagerReopen(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-reopen")
	blockID := value.Blocks[0].ID

	// Phase 1: direct refresh to create freshness.
	adapter1 := newFakeBlockSource(blockResult())
	_, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "mgr-reopen-refresh"), adapter1)
	if err != nil {
		t.Fatalf("phase 1 refresh: %v", err)
	}
	if adapter1.Calls() != 1 {
		t.Fatal("phase 1 adapter not called")
	}

	// Restart service and manager.
	restarted := f.restart(t)
	view, err := restarted.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}

	clock2 := newFakeClock()
	adapter2 := newFakeBlockSource(blockResult())
	mgr2 := NewBlockRefreshManager(restarted, clock2)
	defer mgr2.Close()

	mgr2.RecoverFromProjection(view, func(block BlockInstance) (BlockSourceAdapter, BlockSource, RefreshSchedule, bool) {
		return adapter2, BlockSource{Provider: "controller", Mode: "snapshot"}, DefaultRefreshSchedule(), block.Kind == blockKind()
	})

	if mgr2.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d after reopen, want 1", mgr2.SubscriberCount())
	}

	// The subscription triggers an immediate refresh (delay=0).
	// Wait for the goroutine to complete the async refresh.
	time.Sleep(500 * time.Millisecond)

	if adapter2.Calls() < 1 {
		t.Fatalf("reopened subscription did not trigger refresh (calls=%d)", adapter2.Calls())
	}
}

func TestRefreshManagerAddonInvalid(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-addon-invalid")
	blockID := value.Blocks[0].ID

	// An AddOn-like adapter tries to inject a non-core kind (rejected at type level
	// + blocked by coreBlockKinds validation).
	adapter := newFakeBlockSource()
	adapter.onFetch = func(workID string, block BlockInstance) (BlockRefreshResult, error) {
		return BlockRefreshResult{
			Kind:          "html_injection",
			SchemaVersion: 999,
			Data:          json.RawMessage(`{"malicious":true}`),
			Status:        BlockReady,
			Fallback:      BlockFallback{Summary: "bad"},
		}, nil
	}

	_, err := f.svc.RefreshBlock(context.Background(), refreshInput(value.ID, blockID, "mgr-addon-invalid-req"), adapter)
	if err == nil {
		t.Fatal("expected error for unknown/invalid kind from addon")
	}
	msg := err.Error()
	if !strings.Contains(msg, "kind") && !strings.Contains(msg, "unknown") && !strings.Contains(msg, "unsupported") {
		t.Fatalf("error = %v, want kind/unknown/unsupported rejection", err)
	}
}

func TestRefreshManagerBackoffOnFailure(t *testing.T) {
	f := newServiceFixture(t)
	value := createWorkWithBlock(t, f, "mgr-backoff")
	blockID := value.Blocks[0].ID

	clock := newFakeClock()
	failCount := int32(0)
	adapter := newFakeBlockSource()
	adapter.onFetch = func(workID string, block BlockInstance) (BlockRefreshResult, error) {
		n := atomic.AddInt32(&failCount, 1)
		if n <= 2 {
			return BlockRefreshResult{}, errors.New("transient error")
		}
		return blockResult(), nil
	}

	mgr := NewBlockRefreshManager(f.svc, clock)
	defer mgr.Close()

	mgr.Subscribe(value.ID, blockID, adapter, RefreshSchedule{
		Interval: 10 * time.Second,
		Backoff:  controlBackoffForWorkTest(time.Second),
	})

	waitForCalls(t, adapter, 1, 3*time.Second)
	waitForTimer(t, clock)
	clock.advance(time.Second)
	waitForCalls(t, adapter, 2, 3*time.Second)
	waitForTimer(t, clock)
	clock.advance(time.Second)
	waitForCalls(t, adapter, 3, 3*time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, _ := f.svc.Get(context.Background(), value.ID)
		if block := findBlock(view.Work, blockID); block != nil && block.Status == BlockReady {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("block did not reach ready status after backoff")
}

type controlBackoffForWorkTest time.Duration

func (b controlBackoffForWorkTest) Delay(int) time.Duration { return time.Duration(b) }

func TestExponentialBackoffDelay(t *testing.T) {
	b := NewExponentialBackoff()
	b.Base = 1 * time.Second
	b.Max = 10 * time.Second
	b.Jitter = func(limit time.Duration) time.Duration { return limit }

	d1 := b.Delay(1)
	if d1 <= 0 || d1 > b.Base {
		t.Fatalf("delay(1) = %v, want 0 < d <= Base", d1)
	}

	d5 := b.Delay(5)
	if d5 <= b.Base || d5 > b.Max {
		t.Fatalf("delay(5) = %v, want Base < d <= Max", d5)
	}

	// Very high failure count caps at Max.
	d100 := b.Delay(100)
	if d100 > b.Max {
		t.Fatalf("delay(100) = %v, want <= Max", d100)
	}
}
