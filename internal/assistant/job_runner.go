package assistant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// JobRunnerStore is the subset of Store the JobRunner drives. It mirrors
// RunnerStore so Job lease lifecycle uses the same recovery rules as Runs.
type JobRunnerStore interface {
	RecoverJobs(time.Time) ([]RunnerJob, error)
	RetryJobsDue(time.Time) ([]RunnerJob, error)
	ClaimJob(owner string, now time.Time, ttl time.Duration) (*RunnerJob, bool, error)
	RenewJob(jobID, owner string, fence int64, now time.Time, ttl time.Duration) (*RunnerJob, error)
	BindJobSession(BindJobSessionInput) (*RunnerJob, error)
	FinishJob(FinishJobInput) (*RunnerJob, error)
	FailJob(FailJobInput) (*RunnerJob, error)
}

var _ JobRunnerStore = (*Store)(nil)

// JobRunner owns Job lease lifecycle only. Session creation and agent execution
// are supplied by the host adapter, exactly like Runner.
type JobRunner struct {
	store JobRunnerStore
	owner string
	ttl   time.Duration
}

func NewJobRunner(store JobRunnerStore, owner string, ttl time.Duration) (*JobRunner, error) {
	if store == nil {
		return nil, errors.New("assistant: job runner store is required")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || ttl <= 0 {
		return nil, errors.New("assistant: job runner requires owner and positive ttl")
	}
	return &JobRunner{store: store, owner: owner, ttl: ttl}, nil
}

type JobAcquireResult struct {
	Job         *RunnerJob  `json:"job,omitempty"`
	Recovered   []RunnerJob `json:"recovered,omitempty"`
	Retried     []RunnerJob `json:"retried,omitempty"`
	Diagnostics []string    `json:"diagnostics,omitempty"`
}

// Acquire performs recovery and retry promotion before atomically claiming one
// Job, honoring the per-assistant concurrency cap.
func (r *JobRunner) Acquire(now time.Time) (JobAcquireResult, error) {
	now = utcNow(now)
	recovered, err := r.store.RecoverJobs(now)
	var diagnostics []string
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return JobAcquireResult{}, err
	}
	appendDiagnostic(&diagnostics, err)
	retried, err := r.store.RetryJobsDue(now)
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return JobAcquireResult{Recovered: recovered, Diagnostics: diagnostics}, err
	}
	appendDiagnostic(&diagnostics, err)
	job, ok, err := r.store.ClaimJob(r.owner, now, r.ttl)
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return JobAcquireResult{Recovered: recovered, Retried: retried, Diagnostics: diagnostics}, err
	}
	appendDiagnostic(&diagnostics, err)
	if !ok {
		job = nil
	}
	return JobAcquireResult{Job: job, Recovered: recovered, Retried: retried, Diagnostics: diagnostics}, nil
}

func (r *JobRunner) Renew(job RunnerJob, now time.Time) (*RunnerJob, error) {
	return r.store.RenewJob(job.ID, r.owner, job.LeaseFence, utcNow(now), r.ttl)
}

// BindSession durably records the execution session path on a running Job
// before the host submits the model turn, mirroring Runner.BindSession.
func (r *JobRunner) BindSession(job RunnerJob, requestID, sessionPath string, now time.Time) (*RunnerJob, error) {
	return r.store.BindJobSession(BindJobSessionInput{
		RequestID: requestID, JobID: job.ID, LeaseOwner: r.owner,
		LeaseFence: job.LeaseFence, SessionPath: sessionPath, Now: utcNow(now),
	})
}

func (r *JobRunner) Finish(job RunnerJob, summary string, now time.Time) (*RunnerJob, error) {
	return r.store.FinishJob(FinishJobInput{
		RequestID: fmt.Sprintf("finish:%s:%d", job.ID, job.LeaseFence),
		JobID:     job.ID, LeaseOwner: r.owner, LeaseFence: job.LeaseFence,
		Summary: summary, Now: utcNow(now),
	})
}

func (r *JobRunner) Fail(job RunnerJob, failure Failure) (*RunnerJob, error) {
	failure.Now = utcNow(failure.Now)
	return r.store.FailJob(FailJobInput{
		RequestID: fmt.Sprintf("fail:%s:%d", job.ID, job.LeaseFence),
		JobID:     job.ID, LeaseOwner: r.owner, LeaseFence: job.LeaseFence, Failure: failure,
	})
}
