package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/runhub"
	"workground2/internal/runhub/dsh"
)

type fakeDesktopRunner struct {
	starts  atomic.Int32
	cancels atomic.Int32
	started chan struct{}

	mu   sync.Mutex
	sink runhub.EventSink
	id   runhub.RunID
}

func newFakeDesktopRunner() *fakeDesktopRunner {
	return &fakeDesktopRunner{started: make(chan struct{})}
}

func (r *fakeDesktopRunner) Probe(context.Context, runhub.Profile) (runhub.Capabilities, error) {
	return runhub.Capabilities{Cancel: true}, nil
}

func (r *fakeDesktopRunner) Start(_ context.Context, req runhub.LaunchRequest, sink runhub.EventSink) (runhub.RunnerBinding, error) {
	r.starts.Add(1)
	id := runhub.DeriveRunID(req.RequestID)
	r.mu.Lock()
	r.sink, r.id = sink, id
	r.mu.Unlock()
	_, _ = sink.Report(runhub.RunEvent{EventID: runhub.EventID(string(id) + ":fake-start"), RunID: id, Source: runhub.SourceDSH, Type: runhub.EventStarting})
	_, _ = sink.Report(runhub.RunEvent{EventID: runhub.EventID(string(id) + ":fake-running"), RunID: id, Source: runhub.SourceDSH, Type: runhub.EventRunning})
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	return runhub.RunnerBinding{RunID: id, NativeSessionID: "fake-session", ProtocolVersion: "2.0", ProcessRef: "fake", Attempt: 1}, nil
}

func (r *fakeDesktopRunner) Cancel(context.Context, runhub.RunnerBinding) error {
	r.cancels.Add(1)
	r.mu.Lock()
	sink, id := r.sink, r.id
	r.mu.Unlock()
	_, _ = sink.Report(runhub.RunEvent{EventID: runhub.EventID(string(id) + ":fake-cancel"), RunID: id, Source: runhub.SourceDSH, Type: runhub.EventCancelled})
	return nil
}

func (*fakeDesktopRunner) Open(context.Context, runhub.RunnerBinding) error {
	return nil
}

func (*fakeDesktopRunner) Recover(context.Context, runhub.RunnerBinding) (runhub.Observation, error) {
	return runhub.Observation{}, nil
}

func testDesktopRunHub(t *testing.T, runner *fakeDesktopRunner) *desktopRunHub {
	t.Helper()
	service, err := newDesktopRunHub(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service.resolveProfile = func(string) (ExternalRunProfileView, dsh.Config, dsh.RunnerConfig) {
		return ExternalRunProfileView{
			ID: desktopRunHubProfileID, Ready: true, Version: "0.1.0-rc.8",
			Capabilities: runhub.Capabilities{Cancel: true},
		}, dsh.Config{}, dsh.RunnerConfig{Provider: "fake", Model: "fake"}
	}
	service.newRunner = func(dsh.RunnerConfig, *runhub.Store) runhub.Runner { return runner }
	return service
}

func TestDesktopRunHubLaunchIsIdempotentAndCapabilityBound(t *testing.T) {
	runner := newFakeDesktopRunner()
	service := testDesktopRunHub(t, runner)
	workspace := t.TempDir()

	first, err := service.launch(context.Background(), "req-one", workspace, "first prompt")
	if err != nil || first.Receipt.Status != runhub.ReceiptAccepted {
		t.Fatalf("first launch = (%s, %v)", first.Receipt.Status, err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	waitForExternalRun(t, service, first.Run.ID, runhub.StateRunning)

	replay, err := service.launch(context.Background(), "req-one", workspace, "first prompt")
	if err != nil || replay.Receipt.Status != runhub.ReceiptAlreadyApplied {
		t.Fatalf("replay = (%s, %v)", replay.Receipt.Status, err)
	}
	conflict, err := service.launch(context.Background(), "req-one", workspace, "changed prompt")
	if err != nil || conflict.Receipt.Status != runhub.ReceiptInvalid {
		t.Fatalf("conflict = (%s, %v)", conflict.Receipt.Status, err)
	}
	if got := runner.starts.Load(); got != 1 {
		t.Fatalf("runner starts = %d, want 1", got)
	}

	snapshot := service.snapshot(workspace)
	if len(snapshot.Runs) != 1 || !snapshot.Runs[0].Capabilities.Cancel {
		t.Fatalf("snapshot runs = %+v", snapshot.Runs)
	}
	if snapshot.Runs[0].Capabilities.Open || snapshot.Runs[0].Capabilities.Resume || snapshot.Runs[0].Capabilities.Approve {
		t.Fatalf("unsupported capabilities leaked: %+v", snapshot.Runs[0].Capabilities)
	}

	action, err := service.cancel(context.Background(), first.Run.ID, "cancel-one", workspace)
	if err != nil || action.Run.State != runhub.StateCancelled {
		t.Fatalf("cancel = (%s, %v)", action.Run.State, err)
	}
	if runner.cancels.Load() != 1 {
		t.Fatalf("cancel calls = %d, want 1", runner.cancels.Load())
	}
	replayCancel, err := service.cancel(context.Background(), first.Run.ID, "cancel-one", workspace)
	if err != nil || replayCancel.Receipt.Status != runhub.ReceiptAlreadyApplied {
		t.Fatalf("cancel replay = (%s, %v)", replayCancel.Receipt.Status, err)
	}
}

func TestDesktopRunHubRestartSettlesUnknownOutcomeWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	hub, err := runhub.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runhub.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, run := hub.Launch(runhub.LaunchIntent{RequestID: "req-recover", Source: runhub.SourceDSH, Capabilities: runhub.Capabilities{Cancel: true}})
	_, _ = hub.Report(runhub.RunEvent{EventID: "start", RunID: run.ID, Source: runhub.SourceDSH, Type: runhub.EventStarting})
	_, running := hub.Report(runhub.RunEvent{EventID: "running", RunID: run.ID, Source: runhub.SourceDSH, Type: runhub.EventRunning})
	if err := store.SaveBinding(runhub.BindingRecord{
		RunID: run.ID, Binding: runhub.RunnerBinding{RunID: run.ID, NativeSessionID: "session", ProtocolVersion: "2.0", ProcessRef: "42", Attempt: 1},
		State: running.State, SavedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	restarted, err := newDesktopRunHub(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := restarted.hub.Get(run.ID)
	if !ok || got.State != runhub.StateInterrupted {
		t.Fatalf("recovered state = %s, want interrupted", got.State)
	}
	if len(restarted.active) != 0 || len(restarted.launching) != 0 {
		t.Fatalf("restart manufactured live runtime: active=%d launching=%d", len(restarted.active), len(restarted.launching))
	}

	again, err := newDesktopRunHub(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := again.hub.Get(run.ID); after.State != runhub.StateInterrupted || after.Revision != got.Revision {
		t.Fatalf("second recovery changed run: %+v", after)
	}
}

func TestResolveExternalRunWorkspaceRejectsMissingPath(t *testing.T) {
	app := &App{}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := app.resolveExternalRunWorkspace(missing); err == nil {
		t.Fatal("missing workspace was accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing workspace was created: %v", err)
	}
}

func TestExternalRunSnapshotCarriesResolvedWorkspace(t *testing.T) {
	runner := newFakeDesktopRunner()
	service := testDesktopRunHub(t, runner)
	workspace := t.TempDir()
	snapshot := service.snapshot(workspace)
	if snapshot.Workspace != workspace {
		t.Fatalf("snapshot workspace = %q, want %q", snapshot.Workspace, workspace)
	}
}

func TestResolveDesktopDSHRootUsesExplicitAbsoluteRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DSH_RUNHUB_ROOT", root)
	got, err := resolveDesktopDSHRoot("")
	if err != nil || got != filepath.Clean(root) {
		t.Fatalf("resolve root = (%q, %v)", got, err)
	}
	t.Setenv("DSH_RUNHUB_ROOT", "relative")
	if _, err := resolveDesktopDSHRoot(""); err == nil {
		t.Fatal("relative explicit root was accepted")
	}
}

func waitForExternalRun(t *testing.T, service *desktopRunHub, id runhub.RunID, state runhub.RunState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, ok := service.hub.Get(id); ok && got.State == state {
			service.mu.Lock()
			_, active := service.active[id]
			service.mu.Unlock()
			if active {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	got, _ := service.hub.Get(id)
	t.Fatalf("run did not reach %s with active binding: %+v", state, got)
}

var _ runhub.Runner = (*fakeDesktopRunner)(nil)
