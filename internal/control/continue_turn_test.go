package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/provider"
)

// continueRunnerFake is a scripted runner implementing both Run and
// ContinueRunner. It records whether Continue was invoked and how many user
// messages were appended by the host (it appends none itself).
type continueRunnerFake struct {
	runCalled      bool
	continueCalled bool
}

func (r *continueRunnerFake) Run(ctx context.Context, input string) error {
	r.runCalled = true
	return nil
}

func (r *continueRunnerFake) Continue(ctx context.Context) error {
	r.continueCalled = true
	return nil
}

var _ agent.Runner = (*continueRunnerFake)(nil)
var _ agent.ContinueRunner = (*continueRunnerFake)(nil)

// TestContinueTurnResumesFromCheckpointWithoutNewUserTurn proves acceptance #1
// at the controller level: ContinueTurn drives the runner's Continue (the
// checkpoint path) instead of Run (which appends a new user message), and the
// durable user-turn count is unchanged afterwards.
func TestContinueTurnResumesFromCheckpointWithoutNewUserTurn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.jsonl")
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "continue me"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.InFlightTurn = &agent.InFlightTurnMeta{
		StartMessageIndex: 1, PreserveUser: true,
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}

	runner := &continueRunnerFake{}
	c := New(Options{Runner: runner})
	c.SetSessionPath(path)
	c.Resume(sess, path)

	if err := c.ContinueTurn(context.Background()); err != nil {
		t.Fatalf("ContinueTurn: %v", err)
	}
	if !runner.continueCalled {
		t.Fatal("ContinueTurn did not drive the runner's Continue")
	}
	if runner.runCalled {
		t.Fatal("ContinueTurn invoked Run (would append a new user turn)")
	}

	// The durable session still has exactly one user turn: continue resumes the
	// interrupted round from the checkpoint, it never re-submits the user turn.
	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, m := range loaded.Snapshot() {
		if m.Role == provider.RoleUser {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("user turns after continue = %d, want 1", users)
	}
}

// TestContinueTurnRefusesWithoutInFlightRound proves ContinueTurn returns a
// typed error when the session has no unfinished round (it ends on an
// assistant answer), instead of inventing work.
func TestContinueTurnRefusesWithoutInFlightRound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c2.jsonl")
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "task"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "done"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.InFlightTurn = nil
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}

	runner := &continueRunnerFake{}
	c := New(Options{Runner: runner})
	c.SetSessionPath(path)
	c.Resume(sess, path)

	err = c.ContinueTurn(context.Background())
	if err == nil {
		t.Fatal("ContinueTurn on a completed session must fail, got nil")
	}
	if runner.continueCalled {
		t.Fatal("ContinueTurn invented a round for a completed session")
	}
}

// TestContinueTurnNonContinueRunnerRefuses proves a runner without Continue
// support yields an explicit error instead of silently falling back to Run.
func TestContinueTurnNonContinueRunnerRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c3.jsonl")
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "x"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.InFlightTurn = &agent.InFlightTurnMeta{StartMessageIndex: 1, PreserveUser: true}
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}

	c := New(Options{Runner: runOnlyRunner{}})
	c.SetSessionPath(path)
	c.Resume(sess, path)

	err = c.ContinueTurn(context.Background())
	if err == nil {
		t.Fatal("ContinueTurn with a non-continue runner must fail")
	}
	if !strings.Contains(err.Error(), "does not support checkpoint continue") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// runOnlyRunner implements only agent.Runner (no Continue).
type runOnlyRunner struct{}

func (runOnlyRunner) Run(ctx context.Context, input string) error { return nil }
