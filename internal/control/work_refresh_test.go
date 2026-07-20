package control

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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
	deadline := time.Now().Add(2 * time.Second)
	for source.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
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
}
