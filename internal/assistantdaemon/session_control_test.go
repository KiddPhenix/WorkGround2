package assistantdaemon

import (
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/assistant"
	"workground2/internal/control"
	"workground2/internal/tool/sessiontool"
)

func TestDaemonSessionWorkspaceUsesOwnerAsAuthority(t *testing.T) {
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-daemon-workspace",
		Assistant: assistant.Assistant{
			ID: "helper-daemon-workspace", Name: "Helper", Mission: "Work here",
			Scope: assistant.ScopeWorkspace, WorkspaceRoot: workspace,
			Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	control := newDaemonSessionControl("model", io.Discard, store, nil)
	got, err := control.createWorkspace(sessiontool.SessionCreateRequest{OwnerID: snapshot.Assistant.ID})
	if err != nil || !sameDaemonWorkspace(got, workspace) {
		t.Fatalf("createWorkspace = %q err=%v", got, err)
	}
	if _, err := control.createWorkspace(sessiontool.SessionCreateRequest{
		OwnerID: snapshot.Assistant.ID, Workspace: t.TempDir(),
	}); err == nil {
		t.Fatal("Assistant Session accepted a conflicting workspace")
	}
}

// TestDaemonSessionControlConcurrentRestoreBuildsOneController verifies that
// many concurrent requireCtrl calls for the same unloaded Session build exactly
// one Controller (the restore path is serialized and double-checked), not one
// per caller.
func TestDaemonSessionControlConcurrentRestoreBuildsOneController(t *testing.T) {
	var builds atomic.Int64
	start := make(chan struct{})
	release := make(chan struct{})
	c := newDaemonSessionControl("model", io.Discard, nil, nil)
	c.build = func(sessionID string) (*control.Controller, error) {
		builds.Add(1)
		close(start) // only the first builder reaches here under restoreMu
		<-release    // hold the lock so every concurrent caller queues behind it
		return control.New(control.Options{Label: sessionID}), nil
	}
	defer c.Close()

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	ctrls := make([]*control.Controller, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctrl, err := c.requireCtrl("session-1")
			ctrls[i], errs[i] = ctrl, err
		}(i)
	}

	// Wait until the single builder is in-flight, then release it.
	<-start
	close(release)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("requireCtrl %d: %v", i, errs[i])
		}
		if ctrls[i] == nil {
			t.Fatalf("requireCtrl %d returned nil controller", i)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build calls = %d, want exactly 1", got)
	}
	// All callers observed the same controller instance.
	for i := 1; i < n; i++ {
		if ctrls[i] != ctrls[0] {
			t.Fatalf("concurrent restore returned divergent controllers: %p vs %p", ctrls[i], ctrls[0])
		}
	}
}

// TestDaemonSessionControlRepeatedRestoreReusesLiveController verifies that once
// a Session is loaded, a later requireCtrl returns the cached controller without
// re-building.
func TestDaemonSessionControlRepeatedRestoreReusesLiveController(t *testing.T) {
	var builds atomic.Int64
	c := newDaemonSessionControl("model", io.Discard, nil, nil)
	c.build = func(sessionID string) (*control.Controller, error) {
		builds.Add(1)
		return control.New(control.Options{Label: sessionID}), nil
	}
	defer c.Close()
	if _, err := c.requireCtrl("session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.requireCtrl("session-1"); err != nil {
		t.Fatal(err)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build calls = %d, want 1 (second require must reuse the live controller)", got)
	}
}

func TestDaemonSessionControlRestoreForcesAutoApproval(t *testing.T) {
	c := newDaemonSessionControl("model", io.Discard, nil, nil)
	c.build = func(sessionID string) (*control.Controller, error) {
		ctrl := control.New(control.Options{Label: sessionID})
		ctrl.SetToolApprovalMode(control.ToolApprovalAsk)
		return ctrl, nil
	}
	defer c.Close()

	ctrl, err := c.requireCtrl("assistant-session")
	if err != nil {
		t.Fatal(err)
	}
	if got := ctrl.ToolApprovalMode(); got != control.ToolApprovalAuto {
		t.Fatalf("restored assistant approval mode = %q, want auto", got)
	}
}

// TestDaemonSessionControlRequireCtrlForPathReusesRecoveredController proves a
// snapshot recovery that moves a live Controller onto a new physical path never
// builds a second Controller: the path lookup reuses the already-live instance
// instead of keying by the recovery branch ID.
func TestDaemonSessionControlRequireCtrlForPathReusesRecoveredController(t *testing.T) {
	var builds atomic.Int64
	c := newDaemonSessionControl("model", io.Discard, nil, nil)
	c.build = func(sessionID string) (*control.Controller, error) {
		builds.Add(1)
		return control.New(control.Options{Label: sessionID}), nil
	}
	defer c.Close()

	original, err := c.requireCtrl("supervisor-old")
	if err != nil {
		t.Fatal(err)
	}
	// Recovery re-keys the physical path without re-keying the live map.
	recoveryPath := filepath.Join(t.TempDir(), "supervisor-recovered.jsonl")
	original.SetSessionPath(recoveryPath)

	recovered, err := c.requireCtrlForPath(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != original {
		t.Fatalf("requireCtrlForPath returned a different controller (%p vs %p)", recovered, original)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build calls = %d, want 1 (recovery must reuse the live controller)", got)
	}
	c.mu.Lock()
	n := len(c.live)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("live controllers = %d, want 1", n)
	}
}
