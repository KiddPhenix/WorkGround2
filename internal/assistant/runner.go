package assistant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type RunnerStore interface {
	Recover(time.Time) ([]Run, error)
	RetryDue(time.Time) ([]Run, error)
	Claim(owner string, now time.Time, ttl time.Duration) (*Run, bool, error)
	Renew(runID, owner string, fence int64, now time.Time, ttl time.Duration) (*Run, error)
	BindSession(BindSessionInput) (*Run, error)
	Finish(FinishInput) (*Run, error)
	Fail(FailInput) (*Run, error)
}

// Runner owns lease lifecycle only. Session creation and agent execution are
// supplied by the Desktop adapter in phase 2.
type Runner struct {
	store RunnerStore
	owner string
	ttl   time.Duration
}

var _ RunnerStore = (*Store)(nil)

func NewRunner(store RunnerStore, owner string, ttl time.Duration) (*Runner, error) {
	if store == nil {
		return nil, errors.New("assistant: runner store is required")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || ttl <= 0 {
		return nil, errors.New("assistant: runner requires owner and positive ttl")
	}
	return &Runner{store: store, owner: owner, ttl: ttl}, nil
}

type AcquireResult struct {
	Run         *Run     `json:"run,omitempty"`
	Recovered   []Run    `json:"recovered,omitempty"`
	Retried     []Run    `json:"retried,omitempty"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// Acquire performs recovery and retry promotion before atomically claiming one
// run. Unknown expired outcomes remain in attention and are never promoted.
func (r *Runner) Acquire(now time.Time) (AcquireResult, error) {
	now = utcNow(now)
	recovered, err := r.store.Recover(now)
	var diagnostics []string
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return AcquireResult{}, err
	}
	appendDiagnostic(&diagnostics, err)
	retried, err := r.store.RetryDue(now)
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return AcquireResult{Recovered: recovered, Diagnostics: diagnostics}, err
	}
	appendDiagnostic(&diagnostics, err)
	run, ok, err := r.store.Claim(r.owner, now, r.ttl)
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return AcquireResult{Recovered: recovered, Retried: retried, Diagnostics: diagnostics}, err
	}
	appendDiagnostic(&diagnostics, err)
	if !ok {
		run = nil
	}
	return AcquireResult{Run: run, Recovered: recovered, Retried: retried, Diagnostics: diagnostics}, nil
}

func appendDiagnostic(diagnostics *[]string, err error) {
	if err == nil {
		return
	}
	message := err.Error()
	for _, existing := range *diagnostics {
		if existing == message {
			return
		}
	}
	*diagnostics = append(*diagnostics, message)
}

func (r *Runner) Renew(run Run, now time.Time) (*Run, error) {
	return r.store.Renew(run.ID, r.owner, run.LeaseFence, utcNow(now), r.ttl)
}

func (r *Runner) BindSession(run Run, requestID, sessionPath string, now time.Time) (*Run, error) {
	return r.store.BindSession(BindSessionInput{
		RequestID: requestID, RunID: run.ID, LeaseOwner: r.owner,
		LeaseFence: run.LeaseFence, SessionPath: sessionPath, Now: utcNow(now),
	})
}

func (r *Runner) Finish(run Run, summary, sessionPath string, now time.Time) (*Run, error) {
	return r.store.Finish(FinishInput{
		RequestID: fmt.Sprintf("finish:%s:%d", run.ID, run.LeaseFence),
		RunID:     run.ID, LeaseOwner: r.owner, LeaseFence: run.LeaseFence,
		Summary: summary, SessionPath: sessionPath, Now: utcNow(now),
	})
}

func (r *Runner) Fail(run Run, failure Failure) (*Run, error) {
	failure.Now = utcNow(failure.Now)
	return r.store.Fail(FailInput{
		RequestID: fmt.Sprintf("fail:%s:%d", run.ID, run.LeaseFence),
		RunID:     run.ID, LeaseOwner: r.owner, LeaseFence: run.LeaseFence, Failure: failure,
	})
}
