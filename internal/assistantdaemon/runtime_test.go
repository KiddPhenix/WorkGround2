package assistantdaemon

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/assistant"
	"workground2/internal/provider"
)

func TestDaemonAssistantTextKeepsOnlyAssistantMessages(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "ignore"},
		{Role: provider.RoleAssistant, Content: "first"},
		{Role: provider.RoleTool, Content: "ignore tool"},
		{Role: provider.RoleAssistant, Content: "second"},
	}
	if got := daemonAssistantText(messages); got != "first\nsecond" {
		t.Fatalf("daemonAssistantText = %q", got)
	}
}

func TestRuntimeLeaderHandoffWithEmptyStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	one, err := New(Options{StoreRoot: root, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := New(Options{StoreRoot: root, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()

	if err := one.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := one.currentLease()
	if first.Fence == "" {
		t.Fatal("first runtime did not acquire leadership")
	}
	if err := two.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := two.currentLease(); got.Fence != "" {
		t.Fatalf("follower retained lease %+v", got)
	}
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	if err := two.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := two.currentLease(); got.Fence == "" || got.Fence == first.Fence {
		t.Fatalf("second runtime did not take over: %+v", got)
	}
}

// TestRuntimeRestartSemantics covers the three restart cases through RunOnce:
// an explicit PAUSED stays paused, a safe-restart intent recovers exactly once
// (PAUSED -> RECOVERING -> RUNNING), and a plain RUNNING keeps running without
// fabricating a recovery.
func TestRuntimeRestartSemantics(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")

	// Explicit pause: RunOnce must not resume it.
	r := mustRuntime(t, root)
	defer r.Close()
	if _, err := r.store.PauseAll("pause-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.CompletePause("complete-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	wc, err := r.store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != assistant.WorkPaused {
		t.Fatalf("explicit pause auto-resumed to %s", wc.State)
	}

	// Safe restart: one RECOVERING pass then RUNNING.
	if _, err := r.store.PauseForRestart("restart-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.CompletePause("complete-2", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	wc, err = r.store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != assistant.WorkRunning {
		t.Fatalf("safe restart left state %s, want running after one recovery pass", wc.State)
	}
	if wc.RestartIntent != assistant.RestartIntentNone {
		t.Fatalf("restart intent not consumed: %+v", wc)
	}
	epochAfterRestart := wc.Epoch

	// Plain running restart: stays running, no fake recovery, no extra bump.
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	wc, err = r.store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != assistant.WorkRunning || wc.Epoch != epochAfterRestart {
		t.Fatalf("plain restart changed gate: %+v", wc)
	}
}

func mustRuntime(t *testing.T, root string) *Runtime {
	t.Helper()
	r, err := New(Options{StoreRoot: root, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
