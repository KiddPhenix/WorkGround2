package assistantdaemon

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/boot"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// TestDaemonRestorePreservesWorkspacePolicyAndToolParity proves acceptance #4:
// a daemon restore rebuilds the Controller with the durable WorkspaceRoot, the
// Assistant's tool surface, the shared WorkGate, and the Assistant session
// kind — the same parity the creator used. The options the restore passes to
// boot.Build are observed via the onRestoreOpts seam.
func TestDaemonRestorePreservesWorkspacePolicyAndToolParity(t *testing.T) {
	bootstrapDaemonSupervisorConfig(t)
	store, err := assistant.NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(assistant.CreateInput{
		RequestID: "create-parity",
		Assistant: assistant.Assistant{
			ID: "assistant-parity", Name: "Parity", Mission: "x",
			Scope: assistant.ScopeWorkspace, WorkspaceRoot: "/ws/parity",
			Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Durable session with Assistant identity + workspace recorded in meta.
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "parity-session.jsonl")
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "work"})
	if err := sess.Save(sessionPath); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = "assistant-parity"
	meta.WorkspaceRoot = "/ws/parity"
	meta.Purpose = agent.PurposeManaged
	meta.Status = agent.SessionStatusFailed
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, meta); err != nil {
		t.Fatal(err)
	}

	c := newDaemonSessionControl("test-model", io.Discard, store, func(assistantID, executionID string) []tool.Tool {
		return []tool.Tool{&toolSurfaceProbe{}}
	})
	observed := make(chan boot.Options, 1)
	c.onRestoreOpts = func(opts boot.Options) { observed <- opts }

	opts := c.restoreOptions(sessionPath, "/ws/parity", "assistant-parity", "parity-session", []tool.Tool{&toolSurfaceProbe{}})
	if opts.WorkspaceRoot != "/ws/parity" {
		t.Fatalf("restore workspace = %q, want /ws/parity", opts.WorkspaceRoot)
	}
	if opts.SessionKind != agent.SessionKindAssistant {
		t.Fatalf("restore session kind = %q, want assistant", opts.SessionKind)
	}
	if opts.SessionDir != dir {
		t.Fatalf("restore session dir = %q, want %q", opts.SessionDir, dir)
	}
	if opts.WorkGate == nil {
		t.Fatal("restore dropped the shared WorkGate (policy fence)")
	}
	if len(opts.ExtraTools) == 0 {
		t.Fatal("restore dropped the Assistant tool surface")
	}
	select {
	case got := <-observed:
		if got.WorkspaceRoot != "/ws/parity" || got.SessionKind != agent.SessionKindAssistant {
			t.Fatalf("observed restore options differ: %+v", got)
		}
	default:
		t.Fatal("restore options hook was not invoked")
	}
}

// toolSurfaceProbe is a minimal tool.Tool used to prove the Assistant tool
// surface is carried through a restore.
type toolSurfaceProbe struct{}

func (*toolSurfaceProbe) Name() string        { return "parity_probe" }
func (*toolSurfaceProbe) ReadOnly() bool      { return true }
func (*toolSurfaceProbe) Description() string { return "restore parity probe" }
func (*toolSurfaceProbe) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (*toolSurfaceProbe) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return `{"status":"ok"}`, nil
}
