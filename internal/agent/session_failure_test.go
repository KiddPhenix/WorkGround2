package agent

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/provider"
)

// TestSessionFailureRecordAndRetryClassification proves the failure
// classification persistence (BranchMeta.Failure) and the typed retry errors:
// retryable_known is the only in-place retryable class; outcome_unknown and
// blocked_policy refuse with explicit typed errors.
func TestSessionFailureRecordAndRetryClassification(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.jsonl")
	sess := NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "task"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC()
	if err := RecordSessionFailure(path, SessionFailure{
		Class: FailOutcomeUnknown, Message: "external result lost", At: at,
	}); err != nil {
		t.Fatal(err)
	}
	m, ok, err := LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("meta: %v ok=%v", err, ok)
	}
	if m.Failure == nil || m.Failure.Class != FailOutcomeUnknown {
		t.Fatalf("failure = %+v, want outcome_unknown", m.Failure)
	}
	if m.Status != SessionStatusFailed {
		t.Fatalf("status = %q, want failed", m.Status)
	}
	if err := RetryErrFromClass(m.Failure.Class); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("retry class err = %v, want ErrOutcomeUnknown", err)
	}

	// Overwrite with a blocked_policy record.
	if err := RecordSessionFailure(path, SessionFailure{
		Class: FailBlockedPolicy, Message: "policy refused", At: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := RetryErrFromClass(LoadSessionFailure(path).Class); !errors.Is(err, ErrBlockedPolicy) {
		t.Fatalf("retry class err = %v, want ErrBlockedPolicy", err)
	}

	// retryable_known allows retry.
	if err := RecordSessionFailure(path, SessionFailure{
		Class: FailRetryableKnown, Message: "network blip", At: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := RetryErrFromClass(LoadSessionFailure(path).Class); err != nil {
		t.Fatalf("retryable_known must allow retry, got %v", err)
	}

	// Clear removes the failure record; the durable Status stays as a history
	// marker (preserveBranchMetaPersistence keeps non-empty Status across
	// rewrites, so a retry/state change must set a new Status explicitly).
	if err := ClearSessionFailure(path); err != nil {
		t.Fatal(err)
	}
	m, ok, _ = LoadBranchMeta(path)
	if m.Failure != nil {
		t.Fatalf("after clear: failure=%+v, want nil", m.Failure)
	}
}

// TestClassifySessionErrorConservative proves ClassifySessionError maps raw
// errors to conservative classes: unknown-outcome and policy signals refuse
// retry, ordinary errors stay retryable_known.
func TestClassifySessionErrorConservative(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want FailureClass
	}{
		{"nil", nil, ""},
		{"unknown outcome", ErrOutcomeUnknown, FailOutcomeUnknown},
		{"policy", ErrBlockedPolicy, FailBlockedPolicy},
		{"dependency", ErrBlockedDependency, FailBlockedDependency},
		{"work paused text", errors.New("work paused (epoch 3)"), FailBlockedPolicy},
		{"lease expired text", errors.New("execution lease expired; external outcome is unknown"), FailOutcomeUnknown},
		{"plain error", errors.New("provider returned 500"), FailRetryableKnown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifySessionError(tc.err); got != tc.want {
				t.Fatalf("classify(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestForkStableIDDeterministic proves the fork idempotency key is stable for
// the same (parent, request) and distinct for different inputs.
func TestForkStableIDDeterministic(t *testing.T) {
	a1 := ForkStableID("parent-1", "req-1")
	a2 := ForkStableID("parent-1", "req-1")
	if a1 != a2 {
		t.Fatalf("fork stable id not deterministic: %s vs %s", a1, a2)
	}
	if !strings.HasPrefix(a1, "fork-") {
		t.Fatalf("fork stable id %q lacks fork- prefix", a1)
	}
	if ForkStableID("parent-1", "req-2") == a1 {
		t.Fatal("fork stable id collides across request ids")
	}
	if ForkStableID("parent-2", "req-1") == a1 {
		t.Fatal("fork stable id collides across parents")
	}
}
