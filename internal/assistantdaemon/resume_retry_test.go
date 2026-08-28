package assistantdaemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/control"
	"workground2/internal/provider"
)

// fakeResumer is a scripted turnResumer: it records whether ContinueTurn was
// invoked (the checkpoint-resume path) and never actually drives a model.
type fakeResumer struct {
	path       string
	running    bool
	pending    bool
	continued  bool
	continueFn func() error
}

func (f *fakeResumer) SessionPath() string { return f.path }
func (f *fakeResumer) Running() bool       { return f.running }
func (f *fakeResumer) PendingInteraction() (control.PendingInteraction, bool) {
	return control.PendingInteraction{}, f.pending
}
func (f *fakeResumer) ContinueTurn(ctx context.Context) error {
	f.continued = true
	if f.continueFn != nil {
		return f.continueFn()
	}
	return nil
}

// makeResumeSession writes a durable session with one user turn plus an
// InFlightTurnMeta (an unfinished round after a mid-turn crash), so resume can
// prove it continues from the checkpoint instead of re-submitting the turn.
func makeResumeSession(t *testing.T, dir, name string, inFlight bool) string {
	t.Helper()
	path := filepath.Join(dir, name+".jsonl")
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "do the thing"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = "assistant-1"
	meta.Purpose = agent.PurposeManaged
	if inFlight {
		meta.InFlightTurn = &agent.InFlightTurnMeta{
			StartMessageIndex: 1, PreserveUser: true, StartedAt: time.Now().UTC(),
		}
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDaemonResumeContinuesFromCheckpointNotResubmit proves acceptance #1: a
// session with an unfinished round resumes via ContinueTurn (the checkpoint
// path), and the last user turn is never re-submitted as a new message.
func TestDaemonResumeContinuesFromCheckpointNotResubmit(t *testing.T) {
	dir := t.TempDir()
	path := makeResumeSession(t, dir, "s1", true)
	f := &fakeResumer{path: path}

	c := newDaemonSessionControl("model", nil, nil, nil)
	if err := c.resumeFromCheckpoint(f); err != nil {
		t.Fatalf("resumeFromCheckpoint: %v", err)
	}
	if !f.continued {
		t.Fatal("resume did not continue the interrupted round via ContinueTurn")
	}
	// The user turn count in the durable file must not have grown: resume
	// continues from the checkpoint, it does not append a second user message.
	sess, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, m := range sess.Snapshot() {
		if m.Role == provider.RoleUser {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("user turns after resume = %d, want 1 (last user turn must not be re-submitted)", users)
	}
}

// TestDaemonResumeWithoutInFlightRoundIsExplicit proves that resume of a
// session with no unfinished round (a completed session ending on an assistant
// answer) returns a typed error instead of guessing a redo of the last user
// turn.
func TestDaemonResumeWithoutInFlightRoundIsExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s2.jsonl")
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
	meta.SessionKind = agent.SessionKindAssistant
	meta.Purpose = agent.PurposeManaged
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	f := &fakeResumer{path: path}

	c := newDaemonSessionControl("model", nil, nil, nil)
	err = c.resumeFromCheckpoint(f)
	if err == nil {
		t.Fatal("resume of a completed session must return an explicit error, got nil")
	}
	if f.continued {
		t.Fatal("resume invented a round for a completed session")
	}
}

// TestDaemonResumePendingInteractionLeavesWaiting proves that a session
// waiting on the user (pending interaction after restart) is left waiting, not
// resumed into a duplicate round.
func TestDaemonResumePendingInteractionLeavesWaiting(t *testing.T) {
	dir := t.TempDir()
	path := makeResumeSession(t, dir, "s3", true)
	f := &fakeResumer{path: path, pending: true}

	c := newDaemonSessionControl("model", nil, nil, nil)
	if err := c.resumeFromCheckpoint(f); err != nil {
		t.Fatalf("resume with pending interaction: %v", err)
	}
	if f.continued {
		t.Fatal("resume must not continue a round while the session waits on the user")
	}
}

// TestDaemonRetryOnlyRetryableKnown proves acceptance #2: session_retry allows
// only retryable_known; outcome_unknown, blocked_policy and the absence of a
// failure checkpoint are explicit typed refusals — never a guessed redo.
func TestDaemonRetryOnlyRetryableKnown(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC()

	t.Run("no failure checkpoint is an explicit refusal", func(t *testing.T) {
		path := makeResumeSession(t, dir, "r-no-fail", false)
		f := &fakeResumer{path: path}
		c := newDaemonSessionControl("model", nil, nil, nil)
		err := c.retryFromFailure(f)
		if !errors.Is(err, agent.ErrNoFailureCheckpoint) {
			t.Fatalf("err = %v, want ErrNoFailureCheckpoint", err)
		}
		if f.continued {
			t.Fatal("retry ran a round without a failure checkpoint")
		}
	})

	t.Run("outcome_unknown is refused", func(t *testing.T) {
		path := makeResumeSession(t, dir, "r-unknown", true)
		if err := agent.RecordSessionFailure(path, agent.SessionFailure{
			Class: agent.FailOutcomeUnknown, Message: "external result lost", At: at,
		}); err != nil {
			t.Fatal(err)
		}
		f := &fakeResumer{path: path}
		c := newDaemonSessionControl("model", nil, nil, nil)
		err := c.retryFromFailure(f)
		if !errors.Is(err, agent.ErrOutcomeUnknown) {
			t.Fatalf("err = %v, want ErrOutcomeUnknown", err)
		}
		if f.continued {
			t.Fatal("retry re-ran a round with unknown external outcome")
		}
	})

	t.Run("blocked_policy is refused", func(t *testing.T) {
		path := makeResumeSession(t, dir, "r-policy", true)
		if err := agent.RecordSessionFailure(path, agent.SessionFailure{
			Class: agent.FailBlockedPolicy, Message: "policy refused", At: at,
		}); err != nil {
			t.Fatal(err)
		}
		f := &fakeResumer{path: path}
		c := newDaemonSessionControl("model", nil, nil, nil)
		err := c.retryFromFailure(f)
		if !errors.Is(err, agent.ErrBlockedPolicy) {
			t.Fatalf("err = %v, want ErrBlockedPolicy", err)
		}
		if f.continued {
			t.Fatal("retry re-ran a policy-blocked round")
		}
	})

	t.Run("failed_known is refused", func(t *testing.T) {
		path := makeResumeSession(t, dir, "r-known", true)
		if err := agent.RecordSessionFailure(path, agent.SessionFailure{
			Class: agent.FailFailedKnown, Message: "approach is wrong", At: at,
		}); err != nil {
			t.Fatal(err)
		}
		f := &fakeResumer{path: path}
		c := newDaemonSessionControl("model", nil, nil, nil)
		err := c.retryFromFailure(f)
		if !errors.Is(err, agent.ErrNotRetryable) {
			t.Fatalf("err = %v, want ErrNotRetryable", err)
		}
	})

	t.Run("retryable_known resumes from checkpoint", func(t *testing.T) {
		path := makeResumeSession(t, dir, "r-retryable", true)
		if err := agent.RecordSessionFailure(path, agent.SessionFailure{
			Class: agent.FailRetryableKnown, Message: "network blip before side effect", At: at,
		}); err != nil {
			t.Fatal(err)
		}
		f := &fakeResumer{path: path}
		c := newDaemonSessionControl("model", nil, nil, nil)
		if err := c.retryFromFailure(f); err != nil {
			t.Fatalf("retryable_known retry: %v", err)
		}
		if !f.continued {
			t.Fatal("retryable_known retry did not continue from the checkpoint")
		}
	})
}
