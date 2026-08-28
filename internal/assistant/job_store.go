package assistant

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

func nextQueuedJobIndex(agg *aggregate) int {
	for i := range agg.Jobs {
		if agg.Jobs[i].State == JobQueued {
			return i
		}
	}
	return -1
}

func runningJobCount(agg *aggregate) int {
	count := 0
	for i := range agg.Jobs {
		if agg.Jobs[i].State == JobRunning {
			count++
		}
	}
	return count
}

// ClaimJob atomically claims one queued Job under a lease, honoring the
// per-assistant concurrency cap. It scans assistants in stable order so claims
// are deterministic across processes.
func (s *Store) ClaimJob(owner string, now time.Time, lease time.Duration) (*RunnerJob, bool, error) {
	if strings.TrimSpace(owner) == "" || lease <= 0 {
		return nil, false, errors.New("assistant: job claim requires owner and positive lease")
	}
	now = storeNow(now)
	s.gate.root.Lock()
	defer s.gate.root.Unlock()
	wc, err := s.readWorkControlLocked()
	if err != nil {
		return nil, false, err
	}
	if wc.State != WorkRunning {
		return nil, false, nil
	}
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("assistant: list job claim candidates: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var issues []error
	for _, entry := range entries {
		assistantID := entry.Name()
		if !entry.IsDir() || validateID("assistant", assistantID) != nil {
			continue
		}
		unlock, lockErr := s.lockAssistant(assistantID)
		if lockErr != nil {
			issues = append(issues, lockErr)
			continue
		}
		agg, readErr := s.read(assistantID)
		if readErr != nil {
			unlock()
			issues = append(issues, readErr)
			continue
		}
		if agg.Assistant.Lifecycle != LifecycleActive {
			unlock()
			continue
		}
		if runningJobCount(agg) >= maxConcurrentJobs {
			unlock()
			continue
		}
		idx := nextQueuedJobIndex(agg)
		if idx < 0 {
			unlock()
			continue
		}
		job := &agg.Jobs[idx]
		if err := moveJob(job, JobRunning); err != nil {
			unlock()
			return nil, false, err
		}
		job.Attempt++
		job.LeaseOwner = owner
		job.LeaseFence++
		job.LeaseUntil = now.Add(lease)
		job.WorkEpoch = wc.Epoch
		job.StartedAt = now
		job.UpdatedAt = now
		job.Revision++
		job.Error, job.RetryAt, job.FinishedAt = nil, time.Time{}, time.Time{}
		touch(agg, now)
		if writeErr := s.write(agg); writeErr != nil {
			unlock()
			return nil, false, writeErr
		}
		result := clone(*job)
		unlock()
		return &result, true, nil
	}
	if len(issues) > 0 {
		return nil, false, fmt.Errorf("%w: %w", ErrCorrupt, errors.Join(issues...))
	}
	return nil, false, nil
}

// RenewJob extends a running Job lease under its fence.
func (s *Store) RenewJob(jobID, owner string, fence int64, now time.Time, lease time.Duration) (*RunnerJob, error) {
	if lease <= 0 {
		return nil, errors.New("assistant: job renew requires positive lease")
	}
	return s.withJobLease(jobID, owner, fence, storeNow(now), func(job *RunnerJob, at time.Time) error {
		job.LeaseUntil = at.Add(lease)
		return nil
	})
}

// BindJobSession durably records the execution session path on a running Job
// before the host submits the model turn. It is fence-guarded and replay-safe:
// a crash after this commit leaves an auditable session reference on the job
// recovered into attention, and a stale/late fence can never overwrite the
// session path of a newer retried execution.
func (s *Store) BindJobSession(in BindJobSessionInput) (*RunnerJob, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	in.SessionPath = strings.TrimSpace(in.SessionPath)
	if in.SessionPath == "" {
		return nil, errors.New("assistant: session path is required")
	}
	fp, err := inputFingerprint(struct {
		JobID, Owner, SessionPath string
		Fence                     int64
	}{in.JobID, in.LeaseOwner, in.SessionPath, in.LeaseFence})
	if err != nil {
		return nil, err
	}
	return s.withJobLeaseRequest(in.JobID, in.LeaseOwner, in.LeaseFence, in.RequestID, "bind_job_session", fp, storeNow(in.Now), func(job *RunnerJob, _ time.Time) error {
		job.SessionPath = in.SessionPath
		return nil
	}, nil)
}

// FinishJob completes a running Job under its lease fence.
func (s *Store) FinishJob(in FinishJobInput) (*RunnerJob, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	fp, err := inputFingerprint(struct {
		JobID, Owner, Summary string
		Fence                 int64
	}{in.JobID, in.LeaseOwner, strings.TrimSpace(in.Summary), in.LeaseFence})
	if err != nil {
		return nil, err
	}
	return s.withJobLeaseRequest(in.JobID, in.LeaseOwner, in.LeaseFence, in.RequestID, "finish_job", fp, storeNow(in.Now), func(job *RunnerJob, at time.Time) error {
		if err := moveJob(job, JobSucceeded); err != nil {
			return err
		}
		job.Summary = strings.TrimSpace(in.Summary)
		job.FinishedAt = at
		clearJobLease(job)
		return nil
	}, nil)
}

// FailJob records a Job failure under its lease fence.
func (s *Store) FailJob(in FailJobInput) (*RunnerJob, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	failure := in.Failure
	now := storeNow(failure.Now)
	intent := failure
	intent.Now = time.Time{}
	fp, err := inputFingerprint(struct {
		JobID, Owner string
		Fence        int64
		Failure      Failure
	}{in.JobID, in.LeaseOwner, in.LeaseFence, intent})
	if err != nil {
		return nil, err
	}
	return s.withJobLeaseRequest(in.JobID, in.LeaseOwner, in.LeaseFence, in.RequestID, "fail_job", fp, now, func(job *RunnerJob, at time.Time) error {
		failure.Code, failure.Message, failure.Provider = strings.TrimSpace(failure.Code), strings.TrimSpace(failure.Message), strings.TrimSpace(failure.Provider)
		if failure.Code == "" || failure.Message == "" {
			return errors.New("assistant: failure code and message are required")
		}
		job.Error = &RunError{
			Code: failure.Code, Message: failure.Message, Provider: failure.Provider,
			Retryable: failure.Retryable, OutcomeKnown: failure.OutcomeKnown, At: at,
		}
		clearJobLease(job)
		if !failure.OutcomeKnown {
			if err := moveJob(job, JobWaitingAttention); err != nil {
				return err
			}
			job.Error.Retryable = false
		} else if failure.Retryable && job.Attempt < job.MaxAttempts {
			if err := moveJob(job, JobRetryWait); err != nil {
				return err
			}
			if failure.RetryAfter < 0 {
				failure.RetryAfter = 0
			}
			job.RetryAt = at.Add(failure.RetryAfter)
		} else {
			if err := moveJob(job, JobFailed); err != nil {
				return err
			}
			job.FinishedAt = at
		}
		return nil
	}, nil)
}

// CancelJob cancels a Job by stable request ID. It requires no lease, matching
// user-driven cancellation of a queued or in-flight Job.
func (s *Store) CancelJob(in CancelJobInput) (*RunnerJob, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	fp, err := inputFingerprint(struct{ JobID, Reason string }{in.JobID, in.Reason})
	if err != nil {
		return nil, err
	}
	return s.mutateJob(in.JobID, in.RequestID, "cancel_job", fp, storeNow(in.Now), func(_ *aggregate, job *RunnerJob, now time.Time) error {
		if jobTerminal(job.State) {
			return nil
		}
		if err := moveJob(job, JobCancelled); err != nil {
			return err
		}
		clearJobLease(job)
		job.Summary = in.Reason
		job.FinishedAt = now
		return nil
	})
}

// RetryJob re-queues a failed, cancelled, or waiting-attention Job under an
// explicit user retry. It is idempotent by request ID and never runs a job that
// is already active.
func (s *Store) RetryJob(in RetryJobInput) (*RunnerJob, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	fp, err := inputFingerprint(struct{ JobID string }{in.JobID})
	if err != nil {
		return nil, err
	}
	return s.mutateJob(in.JobID, in.RequestID, "retry_job", fp, storeNow(in.Now), func(_ *aggregate, job *RunnerJob, now time.Time) error {
		switch job.State {
		case JobFailed, JobCancelled, JobWaitingAttention:
		default:
			return fmt.Errorf("%w: cannot retry job in %s", ErrTransition, job.State)
		}
		job.State = JobQueued
		job.Error = nil
		job.RetryAt = time.Time{}
		job.FinishedAt = time.Time{}
		clearJobLease(job)
		return nil
	})
}

func (s *Store) withJobLease(jobID, owner string, fence int64, now time.Time, mutate func(*RunnerJob, time.Time) error) (*RunnerJob, error) {
	if err := validateID("job", jobID); err != nil {
		return nil, err
	}
	assistantID, err := s.jobOwner(jobID)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lockAssistant(assistantID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return nil, err
	}
	idx := jobIndex(agg, jobID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	job := &agg.Jobs[idx]
	if job.State != JobRunning || job.LeaseOwner != owner || job.LeaseFence != fence || !now.Before(job.LeaseUntil) {
		return nil, fmt.Errorf("assistant: job %s fence %d is stale: %w", jobID, fence, ErrLeaseLost)
	}
	if err := mutate(job, now); err != nil {
		return nil, err
	}
	job.Revision++
	job.UpdatedAt = now
	touch(agg, now)
	if err := s.write(agg); err != nil {
		return nil, err
	}
	return clonePtr(*job), nil
}

func (s *Store) withJobLeaseRequest(jobID, owner string, fence int64, requestID, operation, fingerprint string, now time.Time, mutate func(*RunnerJob, time.Time) error, after func(*aggregate, RunnerJob, time.Time)) (*RunnerJob, error) {
	if err := validateID("job", jobID); err != nil {
		return nil, err
	}
	assistantID, err := s.jobOwner(jobID)
	if err != nil {
		return nil, err
	}
	wc, err := s.WorkControl()
	if err != nil {
		return nil, err
	}
	unlock, err := s.lockAssistant(assistantID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return nil, err
	}
	if result, ok, receiptErr := receiptResult[RunnerJob](agg, requestID, operation, fingerprint); ok || receiptErr != nil {
		return &result, receiptErr
	}
	idx := jobIndex(agg, jobID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	job := &agg.Jobs[idx]
	if job.State != JobRunning || job.LeaseOwner != owner || job.LeaseFence != fence || !now.Before(job.LeaseUntil) {
		return nil, fmt.Errorf("assistant: job %s fence %d is stale: %w", jobID, fence, ErrLeaseLost)
	}
	if err := checkWorkEpoch(job.WorkEpoch, wc.Epoch); err != nil {
		return nil, err
	}
	if err := mutate(job, now); err != nil {
		return nil, err
	}
	job.Revision++
	job.UpdatedAt = now
	if after != nil {
		after(agg, *job, now)
	}
	result := clone(*job)
	touch(agg, now)
	if err := putReceipt(agg, requestID, operation, fingerprint, result, now); err != nil {
		return nil, err
	}
	if err := s.write(agg); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) mutateJob(jobID, requestID, operation, fingerprint string, now time.Time, mutate func(*aggregate, *RunnerJob, time.Time) error) (*RunnerJob, error) {
	if err := validateID("job", jobID); err != nil {
		return nil, err
	}
	assistantID, err := s.jobOwner(jobID)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lockAssistant(assistantID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return nil, err
	}
	if result, ok, receiptErr := receiptResult[RunnerJob](agg, requestID, operation, fingerprint); ok || receiptErr != nil {
		return &result, receiptErr
	}
	idx := jobIndex(agg, jobID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	job := &agg.Jobs[idx]
	if err := mutate(agg, job, now); err != nil {
		return nil, err
	}
	job.UpdatedAt, job.Revision = now, job.Revision+1
	result := clone(*job)
	touch(agg, now)
	if err := putReceipt(agg, requestID, operation, fingerprint, result, now); err != nil {
		return nil, err
	}
	if err := s.write(agg); err != nil {
		return nil, err
	}
	return &result, nil
}

// RecoverJobs converts expired running Job leases to waiting_attention. The
// process cannot know whether an external side effect completed before a crash,
// so automatic replay is unsafe.
func (s *Store) RecoverJobs(now time.Time) ([]RunnerJob, error) {
	now = storeNow(now)
	return s.scanJobs(now, func(job *RunnerJob, at time.Time) (bool, error) {
		if job.State != JobRunning || job.LeaseUntil.After(at) {
			return false, nil
		}
		if err := moveJob(job, JobWaitingAttention); err != nil {
			return false, err
		}
		job.Error = &RunError{Code: "outcome_unknown", Message: "execution lease expired; external outcome is unknown", Retryable: false, OutcomeKnown: false, At: at}
		job.FinishedAt = time.Time{}
		clearJobLease(job)
		return true, nil
	})
}

// RetryJobsDue promotes due retry_wait Jobs back to queued.
func (s *Store) RetryJobsDue(now time.Time) ([]RunnerJob, error) {
	now = storeNow(now)
	return s.scanJobs(now, func(job *RunnerJob, at time.Time) (bool, error) {
		if job.State != JobRetryWait || job.RetryAt.After(at) {
			return false, nil
		}
		if err := moveJob(job, JobQueued); err != nil {
			return false, err
		}
		job.RetryAt = time.Time{}
		return true, nil
	})
}

func (s *Store) scanJobs(now time.Time, mutate func(*RunnerJob, time.Time) (bool, error)) ([]RunnerJob, error) {
	assistants, listErr := s.List()
	changed := make([]RunnerJob, 0)
	for _, a := range assistants {
		unlock, lockErr := s.lockAssistant(a.ID)
		if lockErr != nil {
			if listErr == nil {
				listErr = lockErr
			} else {
				listErr = errors.Join(listErr, lockErr)
			}
			continue
		}
		agg, readErr := s.read(a.ID)
		if readErr != nil {
			unlock()
			return nil, readErr
		}
		dirty := false
		for i := range agg.Jobs {
			changedJob, mutateErr := mutate(&agg.Jobs[i], now)
			if mutateErr != nil {
				unlock()
				return nil, mutateErr
			}
			if changedJob {
				agg.Jobs[i].Revision++
				agg.Jobs[i].UpdatedAt = now
				changed = append(changed, clone(agg.Jobs[i]))
				dirty = true
			}
		}
		if dirty {
			touch(agg, now)
			if writeErr := s.write(agg); writeErr != nil {
				unlock()
				return nil, writeErr
			}
		}
		unlock()
	}
	return changed, listErr
}

func clonePtr[T any](v T) *T { return &v }
