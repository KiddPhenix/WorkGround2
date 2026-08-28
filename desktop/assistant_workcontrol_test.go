package main

import (
	"context"
	"testing"

	"workground2/internal/assistant"
	"workground2/internal/control"
)

func TestAssistantRuntimeWorkControlPauseResume(t *testing.T) {
	service, _ := newAssistantTestRuntime(t)

	wc, err := service.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != assistant.WorkRunning || wc.Epoch != 1 {
		t.Fatalf("default work control = %+v", wc)
	}
	if wc.Revision != 1 {
		t.Fatalf("default revision = %d, want 1", wc.Revision)
	}

	paused, err := service.PauseAll("pause-1")
	if err != nil {
		t.Fatal(err)
	}
	if paused.State != assistant.WorkPaused {
		t.Fatalf("PauseAll state = %s, want paused", paused.State)
	}

	// Replay is idempotent: no epoch churn, no error.
	again, err := service.PauseAll("pause-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Epoch != paused.Epoch || again.State != assistant.WorkPaused {
		t.Fatalf("replay = %+v", again)
	}

	resumed, err := service.ResumeAll("resume-1")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != assistant.WorkRunning {
		t.Fatalf("ResumeAll state = %s, want running", resumed.State)
	}

	// Resume replay while already RUNNING is a no-op returning the gate.
	again, err = service.ResumeAll("resume-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != assistant.WorkRunning || again.Epoch != resumed.Epoch {
		t.Fatalf("resume replay = %+v", again)
	}
}

func TestAssistantRuntimePauseForRestartRecordsIntent(t *testing.T) {
	service, _ := newAssistantTestRuntime(t)

	wc, err := service.PauseForRestart("restart-1")
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != assistant.WorkPaused {
		t.Fatalf("PauseForRestart = %+v", wc)
	}

	// The view is authoritative: verify the durable intent through the store.
	store, err := service.requireStore()
	if err != nil {
		t.Fatal(err)
	}
	durable, err := store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if durable.RestartIntent != assistant.RestartIntentRestart {
		t.Fatalf("restart intent not recorded: %+v", durable)
	}

	// An explicit pause clears the restart intent so it never auto-resumes.
	cleared, err := service.PauseAll("pause-after-restart")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.State != assistant.WorkPaused {
		t.Fatalf("explicit pause = %+v", cleared)
	}
	durable, err = store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if durable.RestartIntent != assistant.RestartIntentNone {
		t.Fatalf("explicit pause kept restart intent: %+v", durable)
	}
}

// TestAssistantRuntimeSafeRestartRecoversOnce covers the desktop restart
// semantics: a safe-restart intent left by pause_for_restart is consumed by the
// first tick (PAUSED -> RECOVERING -> RUNNING), an explicit PAUSED stays
// paused, and a plain RUNNING restart does not fabricate a recovery.
func TestAssistantRuntimeSafeRestartRecoversOnce(t *testing.T) {
	service, store := newAssistantTestRuntime(t)

	// Safe restart: intent armed + paused, then a fresh process tick recovers.
	if _, err := service.PauseForRestart("restart-1"); err != nil {
		t.Fatal(err)
	}
	service.tickOnce(context.Background())
	wc, err := store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != assistant.WorkRunning || wc.RestartIntent != assistant.RestartIntentNone {
		t.Fatalf("safe restart left gate %+v, want running with intent consumed", wc)
	}

	// Explicit pause: tick must not resume it.
	if _, err := service.PauseAll("pause-1"); err != nil {
		t.Fatal(err)
	}
	service.tickOnce(context.Background())
	wc, err = store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != assistant.WorkPaused {
		t.Fatalf("explicit pause auto-resumed to %s", wc.State)
	}

	// Resume, then a plain running tick: no fake recovery, no extra epoch bump
	// beyond the resume itself.
	if _, err := service.ResumeAll("resume-1"); err != nil {
		t.Fatal(err)
	}
	wc, err = store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	epoch := wc.Epoch
	service.tickOnce(context.Background())
	wc, err = store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != assistant.WorkRunning || wc.Epoch != epoch {
		t.Fatalf("plain running tick changed gate: %+v", wc)
	}
}

// TestAssistantRuntimePauseAllReturnsActiveOnTimeout proves QUIESCING keeps the
// gate from claiming PAUSED while a live controller is still running: the view
// carries the still-active object and an explicit error, and the persisted
// state stays QUIESCING for a later CompletePause.
func TestAssistantRuntimePauseAllReturnsActiveOnTimeout(t *testing.T) {
	service, store := newAssistantTestRuntime(t)

	// Simulate a host whose active work never quiesces: a registry with one
	// tab whose controller stays running across Cancel.
	ctrl := &stuckController{}
	app := &App{}
	app.sessions.add(&WorkspaceTab{SessionID: "tab-1", Ctrl: ctrl})
	app.assistant = service
	service.app = app

	wc, err := service.PauseAll("pause-1")
	if err == nil {
		t.Fatal("PauseAll with stuck session = nil error, want a quiesce-timeout error")
	}
	if wc.State != assistant.WorkQuiescing {
		t.Fatalf("PauseAll timeout state = %s, want quiescing (never claim PAUSED)", wc.State)
	}
	if len(wc.Active) != 1 || wc.Active[0].State != "running" || wc.Active[0].ID == "" {
		t.Fatalf("PauseAll timeout active = %+v, want the stuck session listed as running", wc.Active)
	}
	durable, err := store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if durable.State != assistant.WorkQuiescing {
		t.Fatalf("persisted state after timeout = %s, want quiescing", durable.State)
	}
}

// stuckController is a control.SessionAPI whose Running never becomes false, so
// a quiesce wait times out deterministically without real controllers. It
// embeds the interface so only Running/Cancel/SessionPath are overridden.
type stuckController struct {
	control.SessionAPI
}

func (c *stuckController) Running() bool       { return true }
func (c *stuckController) Cancel()             {}
func (c *stuckController) SessionPath() string { return "stuck.jsonl" }
