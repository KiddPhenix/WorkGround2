package assistant

import (
	"reflect"
	"testing"
	"time"
)

func TestRunnerRecoversAndRetriesBeforeClaim(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	store := &runnerFake{
		recovered: []Run{{ID: "run-old", State: RunWaitingAttention, Error: &RunError{Code: "outcome_unknown", OutcomeKnown: false}}},
		retried:   []Run{{ID: "run-retry", State: RunQueued}},
		claimed:   &Run{ID: "run-new", State: RunRunning, LeaseFence: 4},
	}
	runner, err := NewRunner(store, "desktop-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Acquire(now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run == nil || result.Run.ID != "run-new" || len(result.Recovered) != 1 || len(result.Retried) != 1 {
		t.Fatalf("unexpected acquire result: %#v", result)
	}
	if !reflect.DeepEqual(store.calls, []string{"recover", "retry_due", "claim"}) {
		t.Fatalf("call order=%v", store.calls)
	}
}

func TestRunnerForwardsLeaseFence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	store := &runnerFake{}
	runner, _ := NewRunner(store, "desktop-1", time.Minute)
	run := Run{ID: "run-1", LeaseFence: 9}
	if _, err := runner.Renew(run, now); err != nil {
		t.Fatal(err)
	}
	if store.fence != 9 || store.owner != "desktop-1" {
		t.Fatalf("renew forwarded owner=%q fence=%d", store.owner, store.fence)
	}
	if _, err := runner.Finish(run, "done", "session.json", now); err != nil {
		t.Fatal(err)
	}
	if store.finish.LeaseFence != 9 || store.finish.Summary != "done" {
		t.Fatalf("finish input=%#v", store.finish)
	}
}

type runnerFake struct {
	calls     []string
	recovered []Run
	retried   []Run
	claimed   *Run
	owner     string
	fence     int64
	finish    FinishInput
	fail      FailInput
}

func (f *runnerFake) Recover(time.Time) ([]Run, error) {
	f.calls = append(f.calls, "recover")
	return f.recovered, nil
}
func (f *runnerFake) RetryDue(time.Time) ([]Run, error) {
	f.calls = append(f.calls, "retry_due")
	return f.retried, nil
}
func (f *runnerFake) Claim(string, time.Time, time.Duration) (*Run, bool, error) {
	f.calls = append(f.calls, "claim")
	return f.claimed, f.claimed != nil, nil
}
func (f *runnerFake) Renew(_ string, owner string, fence int64, _ time.Time, _ time.Duration) (*Run, error) {
	f.owner, f.fence = owner, fence
	return &Run{LeaseOwner: owner, LeaseFence: fence}, nil
}
func (f *runnerFake) BindSession(input BindSessionInput) (*Run, error) {
	return &Run{ID: input.RunID, State: RunRunning, SessionPath: input.SessionPath, LeaseFence: input.LeaseFence}, nil
}
func (f *runnerFake) Finish(input FinishInput) (*Run, error) {
	f.finish = input
	return &Run{ID: input.RunID, State: RunSucceeded}, nil
}
func (f *runnerFake) Fail(input FailInput) (*Run, error) {
	f.fail = input
	return &Run{ID: input.RunID, State: RunFailed}, nil
}

var _ RunnerStore = (*runnerFake)(nil)
