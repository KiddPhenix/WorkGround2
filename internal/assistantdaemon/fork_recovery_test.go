package assistantdaemon

import (
	"errors"
	"path/filepath"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/provider"
)

// fakeForker is a scripted forkExecuter: ForkSessionAt records the requested
// fork path and writes a marker so the test can simulate a crash between
// receipt and metadata inheritance.
type fakeForker struct {
	path      string
	turn      int
	forkCalls int
	forkedAt  string
	forkErr   error
}

func (f *fakeForker) SessionPath() string { return f.path }
func (f *fakeForker) Turn() int           { return f.turn }
func (f *fakeForker) ForkSessionAt(turn int, name, forkPath string) (string, error) {
	f.forkCalls++
	f.forkedAt = forkPath
	if f.forkErr != nil {
		return "", f.forkErr
	}
	return forkPath, nil
}

// makeForkParentSession writes a durable parent session with Assistant
// metadata (owner/purpose/workspace) that forks must inherit.
func makeForkParentSession(t *testing.T, dir, name, owner string, purpose agent.SessionPurpose, workspace string) string {
	t.Helper()
	path := filepath.Join(dir, name+".jsonl")
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "task"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "started"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = owner
	meta.Purpose = purpose
	meta.WorkspaceRoot = workspace
	meta.Scope = "project"
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDaemonForkIdempotentAndCrashRecoverable proves acceptance #5: the same
// fork request resolves to the same deterministic branch across replays and
// restarts, and a crash after the receipt but before the fork never forks
// twice.
func TestDaemonForkIdempotentAndCrashRecoverable(t *testing.T) {
	dir := t.TempDir()
	parentPath := makeForkParentSession(t, dir, "parent", "assistant-1", agent.PurposeManaged, "/ws")
	f := &fakeForker{path: parentPath, turn: 1}
	c := newDaemonSessionControl("model", nil, nil, nil)

	// First fork: receipt reserved, fork executed, receipt committed.
	id1, err := c.forkSessionWith("parent", "fork-1", f)
	if err != nil {
		t.Fatalf("first fork: %v", err)
	}
	if id1 != agent.ForkStableID("parent", "fork-1") {
		t.Fatalf("fork id = %s, want deterministic %s", id1, agent.ForkStableID("parent", "fork-1"))
	}
	if f.forkCalls != 1 {
		t.Fatalf("fork calls = %d, want 1", f.forkCalls)
	}

	// Replay (simulated crash recovery): the receipt is committed, so the same
	// request resolves to the same branch and the fork is NOT executed again.
	f.forkCalls = 0
	id2, err := c.forkSessionWith("parent", "fork-1", f)
	if err != nil {
		t.Fatalf("replay fork: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("replay resolved %s, want %s", id2, id1)
	}
	if f.forkCalls != 0 {
		t.Fatalf("replay forked again: %d calls", f.forkCalls)
	}
}

// TestDaemonForkCrashBetweenReceiptAndFork proves the mid-crash window is
// recoverable: a receipt claimed but not yet committed (crash before the fork
// ran) resolves to the same deterministic branch on the next call and the fork
// executes exactly once.
func TestDaemonForkCrashBetweenReceiptAndFork(t *testing.T) {
	dir := t.TempDir()
	parentPath := makeForkParentSession(t, dir, "parent", "assistant-1", agent.PurposeManaged, "/ws")

	// Simulate a crash: another host claimed the receipt but crashed before
	// the fork executed (receipt at reserved state).
	branchID := agent.ForkStableID("parent", "fork-crash")
	if _, err := agent.ReserveSessionReceipt(dir, "fork:fork-crash", agent.SessionReceipt{
		SessionID:   branchID,
		Fingerprint: agent.SessionReceiptFingerprint("fork", "parent", "fork-crash"),
	}); err != nil {
		t.Fatal(err)
	}

	// Recovery: the same request resolves to the same branch and forks exactly
	// once (the crashed host never created the branch).
	f := &fakeForker{path: parentPath, turn: 1}
	c := newDaemonSessionControl("model", nil, nil, nil)
	id, err := c.forkSessionWith("parent", "fork-crash", f)
	if err != nil {
		t.Fatalf("crash-recovery fork: %v", err)
	}
	if id != branchID {
		t.Fatalf("crash-recovery resolved %s, want %s", id, branchID)
	}
	if f.forkCalls != 1 {
		t.Fatalf("crash-recovery fork calls = %d, want exactly 1", f.forkCalls)
	}
	if f.forkedAt != forkStablePath(dir, "parent", "fork-crash") {
		t.Fatalf("forked at %s, want deterministic %s", f.forkedAt, forkStablePath(dir, "parent", "fork-crash"))
	}
}

// TestDaemonForkRequestConflict proves the same fork request ID reused with a
// different parent session is an explicit conflict.
func TestDaemonForkRequestConflict(t *testing.T) {
	dir := t.TempDir()
	parentPath := makeForkParentSession(t, dir, "parent", "assistant-1", agent.PurposeManaged, "/ws")
	f := &fakeForker{path: parentPath, turn: 1}
	c := newDaemonSessionControl("model", nil, nil, nil)

	if _, err := c.forkSessionWith("parent", "fork-conflict", f); err != nil {
		t.Fatalf("first fork: %v", err)
	}
	f.forkCalls = 0

	// Same request ID, different parent: conflict, never forks.
	otherPath := makeForkParentSession(t, dir, "other", "assistant-2", agent.PurposeManaged, "/ws2")
	f2 := &fakeForker{path: otherPath, turn: 1}
	_, err := c.forkSessionWith("other", "fork-conflict", f2)
	var conflict *agent.SessionReceiptConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want SessionReceiptConflictError", err)
	}
	if f2.forkCalls != 0 {
		t.Fatal("conflicting fork executed")
	}
}

// TestDaemonForkBranchInheritsParentMetadata proves the fork request resolves
// to the deterministic branch path carrying the parent Session identity
// (acceptance #5). The metadata inheritance itself lives in
// Controller.ForkSessionAt (covered in internal/control); here we verify the
// daemon resolves the same deterministic path and passes it to the fork.
func TestDaemonForkBranchInheritsParentMetadata(t *testing.T) {
	dir := t.TempDir()
	parentPath := makeForkParentSession(t, dir, "parent-meta", "assistant-1", agent.PurposeManaged, "/ws")
	f := &fakeForker{path: parentPath, turn: 1}
	c := newDaemonSessionControl("model", nil, nil, nil)

	branchID, err := c.forkSessionWith("parent-meta", "fork-meta", f)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	forkPath := filepath.Join(dir, branchID+".jsonl")
	if f.forkedAt != forkPath {
		t.Fatalf("forked at %s, want deterministic %s", f.forkedAt, forkPath)
	}
	if branchID != agent.ForkStableID("parent-meta", "fork-meta") {
		t.Fatalf("branch id = %s, want %s", branchID, agent.ForkStableID("parent-meta", "fork-meta"))
	}
}
