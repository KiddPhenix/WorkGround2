package control

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/work"
)

type controlBlockSource struct {
	calls atomic.Int32
}

func (s *controlBlockSource) FetchBlock(context.Context, string, work.BlockInstance) (work.BlockRefreshResult, error) {
	s.calls.Add(1)
	return work.BlockRefreshResult{
		Kind: "markdown", SchemaVersion: 1, Status: work.BlockReady,
		Data:     json.RawMessage(`{"content":"controller refreshed"}`),
		Fallback: work.BlockFallback{Summary: "controller refreshed"},
	}, nil
}

type gatedWorkService struct {
	*work.Service
	mu          sync.Mutex
	getStarted  chan struct{}
	getRelease  chan struct{}
	gateNextGet bool
	deleteDone  chan struct{}
}

func (s *gatedWorkService) armGet() (<-chan struct{}, chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getStarted = make(chan struct{})
	s.getRelease = make(chan struct{})
	s.gateNextGet = true
	return s.getStarted, s.getRelease
}

func (s *gatedWorkService) Get(ctx context.Context, workID string) (*work.WorkView, error) {
	s.mu.Lock()
	if !s.gateNextGet {
		s.mu.Unlock()
		return s.Service.Get(ctx, workID)
	}
	s.gateNextGet = false
	started, release := s.getStarted, s.getRelease
	s.mu.Unlock()
	view, err := s.Service.Get(ctx, workID)
	close(started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-release:
		return view, err
	}
}

func (s *gatedWorkService) Delete(ctx context.Context, workID, requestID string) error {
	err := s.Service.Delete(ctx, workID, requestID)
	if err == nil && s.deleteDone != nil {
		close(s.deleteDone)
	}
	return err
}

type blockingControlSource struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *blockingControlSource) FetchBlock(context.Context, string, work.BlockInstance) (work.BlockRefreshResult, error) {
	s.calls.Add(1)
	close(s.started)
	<-s.release // deliberately ignores cancellation to exercise the late response path
	return work.BlockRefreshResult{
		Kind: "markdown", SchemaVersion: 1, Status: work.BlockReady,
		Data: json.RawMessage(`{"content":"late controller result"}`),
	}, nil
}

type controlBackoff time.Duration

func (b controlBackoff) Delay(int) time.Duration { return time.Duration(b) }

func newRefreshController(t *testing.T, root string, source *controlBlockSource, views *WorkViewBroadcaster) (*Controller, *work.Service) {
	t.Helper()
	store, err := work.NewFileWorkStore(filepath.Join(root, "works"), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	c := New(Options{
		Work: svc, WorkViews: views,
		WorkBlockSources: []WorkBlockSource{{
			Source: work.BlockSource{Provider: "addon:test", Ref: "fixture", Mode: "query"},
			Kind:   "markdown", Adapter: source,
			Schedule: work.RefreshSchedule{Interval: time.Hour, Backoff: controlBackoff(5 * time.Second)},
		}},
	})
	return c, svc
}

func TestControllerRefreshBlockEventProjectionOfflineReconnectArchive(t *testing.T) {
	root := t.TempDir()
	views := NewWorkViewBroadcaster()
	source := &controlBlockSource{}
	c, svc := newRefreshController(t, root, source, views)
	defer c.Close()
	id, events := views.Subscribe(16)
	defer views.Unsubscribe(id)

	value, err := c.WorkControl().CreateWork(context.Background(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		Name:         "controller refresh", RequestID: "control-refresh-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	blockID := value.Blocks[0].ID
	refreshed, err := c.WorkControl().RefreshBlock(context.Background(), value.ID, blockID, "control-refresh-1")
	if err != nil {
		t.Fatalf("RefreshBlock: %v", err)
	}
	if refreshed.Source.Provider != "addon:test" || !refreshed.Source.Verified || source.calls.Load() != 1 {
		t.Fatalf("refresh result/source/calls = %+v / %d", refreshed.Source, source.calls.Load())
	}
	if _, err := c.WorkControl().RefreshBlock(context.Background(), value.ID, blockID, "control-refresh-1"); err != nil {
		t.Fatalf("idempotent RefreshBlock: %v", err)
	}
	if source.calls.Load() != 1 {
		t.Fatalf("duplicate request called source %d times", source.calls.Load())
	}
	foundEvent := false
	for !foundEvent {
		select {
		case event := <-events:
			foundEvent = event.RequestID == "control-refresh-1" && event.WorkID == value.ID && event.Object.ID == blockID
		case <-time.After(time.Second):
			t.Fatal("missing RefreshBlock projection event")
		}
	}

	c.SetWorkOnline(false)
	failed, err := c.WorkControl().RefreshBlock(context.Background(), value.ID, blockID, "control-refresh-offline")
	if !errors.Is(err, work.ErrSourceUnavailable) || failed == nil || failed.Freshness == nil || failed.Freshness.RetryAt == nil {
		t.Fatalf("offline refresh = (%+v, %v)", failed, err)
	}
	if source.calls.Load() != 1 {
		t.Fatalf("offline refresh reached adapter; calls=%d", source.calls.Load())
	}
	if state := c.WorkRefreshState(value.ID, blockID); state.Failures != 1 || state.Online {
		t.Fatalf("offline scheduler state = %+v", state)
	}

	c.SetWorkOnline(true)
	for source.calls.Load() < 2 {
		select {
		case event := <-events:
			if event.WorkID != value.ID {
				continue
			}
		case <-time.After(2 * time.Second):
			t.Fatal("reconnect did not emit a refresh event")
		}
	}
	view, err := svc.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if source.calls.Load() < 2 || view.Work.Blocks[0].Status != work.BlockReady {
		t.Fatalf("reconnect did not recover: calls=%d block=%+v", source.calls.Load(), view.Work.Blocks[0])
	}

	if _, err := c.WorkControl().ArchiveWork(context.Background(), value.ID, "control-refresh-archive"); err != nil {
		t.Fatal(err)
	}
	if state := c.WorkRefreshState(value.ID, blockID); state.Subscribed {
		t.Fatalf("archive retained subscription: %+v", state)
	}
}

func TestControllerRefreshBlockReopenRestoresIntent(t *testing.T) {
	root := t.TempDir()
	source := &controlBlockSource{}
	c1, _ := newRefreshController(t, root, source, NewWorkViewBroadcaster())
	value, err := c1.WorkControl().CreateWork(context.Background(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		Name:         "reopen", RequestID: "control-reopen-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	blockID := value.Blocks[0].ID
	if _, err := c1.WorkControl().RefreshBlock(context.Background(), value.ID, blockID, "control-reopen-refresh"); err != nil {
		t.Fatal(err)
	}
	c1.Close()

	c2, _ := newRefreshController(t, root, source, NewWorkViewBroadcaster())
	if state := c2.WorkRefreshState(value.ID, blockID); !state.Subscribed {
		t.Fatalf("reopen did not restore refresh intent: %+v, recovery=%q", state, c2.WorkRefreshRecoveryError())
	}
	if err := c2.CancelBlockRefresh(context.Background(), value.ID, blockID, "control-reopen-cancel"); err != nil {
		t.Fatal(err)
	}
	if state := c2.WorkRefreshState(value.ID, blockID); state.Subscribed {
		t.Fatalf("cancel retained refresh intent: %+v", state)
	}
	c2.Close()

	c3, _ := newRefreshController(t, root, source, NewWorkViewBroadcaster())
	defer c3.Close()
	if state := c3.WorkRefreshState(value.ID, blockID); state.Subscribed {
		t.Fatalf("reopen resurrected canceled intent: %+v", state)
	}
}

func TestRegisterWorkBlockSourceRejectsUnsafeMetadata(t *testing.T) {
	c := New(Options{})
	if err := c.RegisterWorkBlockSource(WorkBlockSource{}); !errors.Is(err, errWorkDisabled) {
		t.Fatalf("disabled register error = %v", err)
	}
	enabled, _ := newRefreshController(t, t.TempDir(), &controlBlockSource{}, NewWorkViewBroadcaster())
	defer enabled.Close()
	for _, binding := range []WorkBlockSource{
		{Source: work.BlockSource{Provider: "addon:", Mode: "query"}, Kind: "markdown", Adapter: &controlBlockSource{}},
		{Source: work.BlockSource{Provider: "addon:test", Mode: "html"}, Kind: "markdown", Adapter: &controlBlockSource{}},
		{Source: work.BlockSource{Provider: "addon:test", Mode: "query"}, Kind: "markdown"},
	} {
		if err := enabled.RegisterWorkBlockSource(binding); err == nil {
			t.Fatalf("unsafe source registration accepted: %+v", binding)
		}
	}
	var typedNil *controlBlockSource
	if err := enabled.RegisterWorkBlockSource(WorkBlockSource{
		Source: work.BlockSource{Provider: "addon:typed-nil", Mode: "query"},
		Kind:   "markdown", Adapter: typedNil,
	}); err == nil {
		t.Fatal("typed-nil source adapter was accepted")
	}
}

func TestControllerDeleteInvalidatesCapturedRecoveryProjection(t *testing.T) {
	root := t.TempDir()
	views := NewWorkViewBroadcaster()
	store, err := work.NewFileWorkStore(filepath.Join(root, "works"), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	base := work.NewService(store, work.NewBlueprintRegistry(), views)
	value, err := base.Create(context.Background(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		Name:         "generation race", RequestID: "generation-race-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	blockID := value.Blocks[0].ID
	source := &controlBlockSource{}
	if _, err := base.RefreshBlock(context.Background(), work.RefreshBlockInput{
		WorkID: value.ID, BlockID: blockID, RequestID: "generation-race-intent",
		Source: work.BlockSource{Provider: "addon:test", Mode: "query", Verified: true}, CheckedAt: time.Now().UTC(),
	}, source); err != nil {
		t.Fatal(err)
	}
	gated := &gatedWorkService{Service: base}
	c := New(Options{
		Work: gated, WorkViews: views,
		WorkBlockSources: []WorkBlockSource{{
			Source: work.BlockSource{Provider: "addon:test", Mode: "query"}, Kind: "markdown", Adapter: source,
			Schedule: work.RefreshSchedule{Interval: 0},
		}},
	})
	defer c.Close()
	c.workRefresh.UnsubscribeWork(value.ID)
	started, release := gated.armGet()
	recovered := make(chan struct{})
	go func() {
		c.recoverWorkRefreshes(context.Background())
		close(recovered)
	}()
	<-started
	if err := c.WorkControl().DeleteWork(context.Background(), value.ID, "generation-race-delete"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-recovered:
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not finish after delete")
	}
	if state := c.WorkRefreshState(value.ID, blockID); state.Subscribed {
		t.Fatalf("captured pre-delete projection resurrected subscription: %+v", state)
	}
}

func TestControllerDeleteCancelsLateRefreshWithoutResurrection(t *testing.T) {
	root := t.TempDir()
	views := NewWorkViewBroadcaster()
	store, err := work.NewFileWorkStore(filepath.Join(root, "works"), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	base := work.NewService(store, work.NewBlueprintRegistry(), views)
	value, err := base.Create(context.Background(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		Name:         "late delete", RequestID: "late-delete-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	gated := &gatedWorkService{Service: base, deleteDone: make(chan struct{})}
	source := &blockingControlSource{started: make(chan struct{}), release: make(chan struct{})}
	c := New(Options{
		Work: gated, WorkViews: views,
		WorkBlockSources: []WorkBlockSource{{
			Source: work.BlockSource{Provider: "addon:late", Mode: "query"}, Kind: "markdown", Adapter: source,
			Schedule: work.RefreshSchedule{Interval: 0},
		}},
	})
	defer c.Close()
	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := c.WorkControl().RefreshBlock(context.Background(), value.ID, value.Blocks[0].ID, "late-delete-refresh")
		refreshDone <- refreshErr
	}()
	<-source.started
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- c.WorkControl().DeleteWork(context.Background(), value.ID, "late-delete-delete")
	}()
	select {
	case <-gated.deleteDone:
	case <-time.After(3 * time.Second):
		t.Fatal("delete did not persist while refresh was in flight")
	}
	close(source.release)
	if err := <-deleteDone; err != nil {
		t.Fatal(err)
	}
	if err := <-refreshDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("late refresh error = %v, want context canceled", err)
	}
	if _, err := base.Get(context.Background(), value.ID); !errors.Is(err, work.ErrWorkNotFound) {
		t.Fatalf("late refresh resurrected deleted Work: %v", err)
	}
	if state := c.WorkRefreshState(value.ID, value.Blocks[0].ID); state.Subscribed || state.Inflight {
		t.Fatalf("delete retained refresh state: %+v", state)
	}
}

func TestControllerRestoreUsesCurrentPersistedIntentOnly(t *testing.T) {
	c, _ := newRefreshController(t, t.TempDir(), &controlBlockSource{}, NewWorkViewBroadcaster())
	defer c.Close()
	value, err := c.WorkControl().CreateWork(context.Background(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		Name:         "restore intent", RequestID: "restore-intent-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	blockID := value.Blocks[0].ID
	if _, err := c.WorkControl().RefreshBlock(context.Background(), value.ID, blockID, "restore-intent-refresh"); err != nil {
		t.Fatal(err)
	}
	if err := c.CancelBlockRefresh(context.Background(), value.ID, blockID, "restore-intent-cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WorkControl().ArchiveWork(context.Background(), value.ID, "restore-intent-archive"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WorkControl().RestoreWork(context.Background(), value.ID, "restore-intent-restore"); err != nil {
		t.Fatal(err)
	}
	if state := c.WorkRefreshState(value.ID, blockID); state.Subscribed {
		t.Fatalf("restore resurrected canceled persisted intent: %+v", state)
	}
}
