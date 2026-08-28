package main

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/tool"
)

// fakeTool is a minimal tool.Tool for the read-only filter test.
type fakeTool struct {
	name string
	ro   bool
}

func (t fakeTool) Name() string        { return t.name }
func (t fakeTool) Description() string { return t.name }
func (t fakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t fakeTool) ReadOnly() bool { return t.ro }
func (t fakeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "ok", nil
}

// TestProductionSupervisorHasNoOutOfSessionReason is the regression guard for
// design gap E: the production supervisor loop must never construct the legacy
// out-of-session Supervisor or call Supervisor.Reason (a headless role-model
// completion outside any Session). All supervisor reasoning now runs through
// the assistant's durable Purpose=supervisor Session Controller.
func TestProductionSupervisorHasNoOutOfSessionReason(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{
		filepath.Join(root),
		filepath.Join(root, "..", "internal", "assistantdaemon"),
	}
	bad := []string{
		"NewSupervisor(",
		"supervisor.Reason(",
		".supervisor.Reason",
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			// Only production (non-test) Go files, parsed as real Go so the
			// guard cannot be fooled by comments or strings.
			fset := token.NewFileSet()
			if _, err := parser.ParseFile(fset, e.Name(), src, parser.SkipObjectResolution); err != nil {
				t.Fatalf("parse %s: %v", e.Name(), err)
			}
			text := string(src)
			for _, pattern := range bad {
				if strings.Contains(text, pattern) {
					t.Fatalf("%s: production file contains forbidden out-of-session supervisor pattern %q", filepath.Join(dir, e.Name()), pattern)
				}
			}
		}
	}
}

// TestSupervisorToolFilterKeepsOnlyReadOnly proves the supervisor Session's
// tool surface is bounded to read-only observation: write tools (session
// control, schedule/memory/policy mutations) stay off the supervisor Controller
// so the loop alone routes the acting phase.
func TestSupervisorToolFilterKeepsOnlyReadOnly(t *testing.T) {
	tools := []tool.Tool{
		fakeTool{name: "session_list", ro: true},
		fakeTool{name: "session_steer", ro: false},
		fakeTool{name: "schedule_get", ro: true},
		fakeTool{name: "schedule_create", ro: false},
		fakeTool{name: "memory_search", ro: true},
		fakeTool{name: "memory_remember", ro: false},
	}
	got := filterReadOnlyTools(tools)
	if len(got) != 3 {
		t.Fatalf("filtered tools = %d, want 3", len(got))
	}
	for _, g := range got {
		if !g.ReadOnly() {
			t.Fatalf("write tool %q survived the supervisor filter", g.Name())
		}
	}
}

// TestSupervisorTabDetectsDurablePurpose proves supervisorTab reads the
// Session subsystem's durable meta (SessionKind=assistant + Purpose=supervisor)
// rather than a runtime flag, so a restarted session stays a supervisor. The
// negative cases are asserted unconditionally: a managed-purpose session, an
// arbitrary third purpose, and a supervisor purpose on a non-assistant
// SessionKind must all be rejected — so a "not managed" style inverted
// implementation or a missing SessionKind conjunct can never pass.
func TestSupervisorTabDetectsDurablePurpose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supervisor-session.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = "helper-guard"
	meta.Purpose = agent.PurposeSupervisor
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	if !supervisorTab(&WorkspaceTab{SessionPath: path}) {
		t.Fatal("supervisorTab did not detect the durable supervisor session")
	}
	// A managed-purpose session is not a supervisor tab.
	meta.Purpose = agent.PurposeManaged
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	if supervisorTab(&WorkspaceTab{SessionPath: path}) {
		t.Fatal("supervisorTab matched a managed session")
	}
	// An arbitrary third purpose (neither supervisor nor managed) must also be
	// rejected: this guards against a "Purpose != managed" inverted check.
	meta.Purpose = agent.SessionPurpose("experiment")
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	if supervisorTab(&WorkspaceTab{SessionPath: path}) {
		t.Fatal("supervisorTab matched a non-supervisor purpose")
	}
	// Purpose=supervisor on a non-assistant SessionKind is not a supervisor
	// tab either: the SessionKind conjunct must be required, not optional.
	meta.SessionKind = agent.SessionKindNormal
	meta.Purpose = agent.PurposeSupervisor
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	if supervisorTab(&WorkspaceTab{SessionPath: path}) {
		t.Fatal("supervisorTab matched a supervisor purpose on a non-assistant session kind")
	}
}
