package assistant

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// dispatchIndex returns the aggregate index of a Dispatch, or -1.
func dispatchIndex(agg *aggregate, id string) int {
	for i := range agg.Dispatches {
		if agg.Dispatches[i].ID == id {
			return i
		}
	}
	return -1
}

// jobIndex returns the aggregate index of a RunnerJob, or -1.
func jobIndex(agg *aggregate, id string) int {
	for i := range agg.Jobs {
		if agg.Jobs[i].ID == id {
			return i
		}
	}
	return -1
}

func contextPackIndex(agg *aggregate, id string) int {
	for i := range agg.ContextPacks {
		if agg.ContextPacks[i].ID == id {
			return i
		}
	}
	return -1
}

func ideaIndex(agg *aggregate, id string) int {
	for i := range agg.Ideas {
		if agg.Ideas[i].ID == id {
			return i
		}
	}
	return -1
}

func jobTerminal(state JobState) bool {
	switch state {
	case JobSucceeded, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

func dispatchJobsTerminal(agg *aggregate, dispatchID string) bool {
	found := false
	for i := range agg.Jobs {
		if agg.Jobs[i].DispatchID != dispatchID {
			continue
		}
		found = true
		if !jobTerminal(agg.Jobs[i].State) {
			return false
		}
	}
	return found
}

func dispatchSucceeded(agg *aggregate, dispatchID string) bool {
	found := false
	for i := range agg.Jobs {
		if agg.Jobs[i].DispatchID != dispatchID {
			continue
		}
		found = true
		if agg.Jobs[i].State != JobSucceeded {
			return false
		}
	}
	return found
}

func moveJob(job *RunnerJob, next JobState) error {
	switch next {
	case JobQueued:
		if job.State != JobQueued && job.State != JobRetryWait {
			return fmt.Errorf("%w: cannot move job to queued from %s", ErrTransition, job.State)
		}
	case JobRunning:
		if job.State != JobQueued {
			return fmt.Errorf("%w: cannot run job in %s", ErrTransition, job.State)
		}
	case JobSucceeded, JobFailed, JobCancelled:
		if job.State != JobRunning && job.State != JobWaitingAttention {
			return fmt.Errorf("%w: cannot finish job in %s", ErrTransition, job.State)
		}
	case JobRetryWait:
		if job.State != JobRunning {
			return fmt.Errorf("%w: cannot retry-wait job in %s", ErrTransition, job.State)
		}
	case JobWaitingAttention:
		if job.State != JobRunning {
			return fmt.Errorf("%w: cannot attention job in %s", ErrTransition, job.State)
		}
	default:
		return fmt.Errorf("assistant: unknown job state %q", next)
	}
	job.State = next
	return nil
}

func clearJobLease(job *RunnerJob) {
	// LeaseFence is deliberately preserved: it is monotonic across claims, so a
	// stale/late completion from an earlier lease can never match the fence of
	// a newer retried execution (numeric fence reuse would let it overwrite the
	// retry state).
	job.LeaseOwner = ""
	job.LeaseUntil = time.Time{}
}

func (s *Store) jobOwner(jobID string) (string, error) {
	assistants, listErr := s.List()
	for _, assistant := range assistants {
		unlock, lockErr := s.lockAssistant(assistant.ID)
		if lockErr != nil {
			if listErr == nil {
				listErr = lockErr
			} else {
				listErr = errors.Join(listErr, lockErr)
			}
			continue
		}
		agg, readErr := s.read(assistant.ID)
		unlock()
		if readErr != nil {
			return "", readErr
		}
		if jobIndex(agg, jobID) >= 0 {
			return assistant.ID, nil
		}
	}
	if listErr != nil {
		return "", errors.Join(ErrNotFound, listErr)
	}
	return "", ErrNotFound
}

// OpenDispatch persists a raw direct input as a pending Dispatch. Reusing the
// same request ID with the same input returns the same Dispatch; a different
// input under the same request ID is an explicit conflict. The input is never
// dropped: even if classification never runs, it stays recoverable.
func (s *Store) OpenDispatch(in OpenDispatchInput) (Dispatch, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Dispatch{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return Dispatch{}, err
	}
	in.Input = strings.TrimSpace(in.Input)
	if err := validateDirectPrompt(in.Input); err != nil {
		return Dispatch{}, err
	}
	fingerprint, err := inputFingerprint(struct {
		AssistantID string
		Input       string
	}{in.AssistantID, in.Input})
	if err != nil {
		return Dispatch{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	if result, ok, receiptErr := receiptResult[Dispatch](agg, in.RequestID, "open_dispatch", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	if agg.Assistant.Lifecycle != LifecycleActive {
		return Dispatch{}, fmt.Errorf("assistant: %s is %s: %w", in.AssistantID, agg.Assistant.Lifecycle, ErrTransition)
	}
	// New dispatches (new work) are refused while the gate is not RUNNING;
	// replay of an already-recorded dispatch returns the receipt result above.
	if err := s.requireRunning(); err != nil {
		return Dispatch{}, err
	}
	now := storeNow(in.Now)
	wc, err := s.WorkControl()
	if err != nil {
		return Dispatch{}, err
	}
	dispatch := Dispatch{
		ID: StableID("dispatch", in.AssistantID+"/"+in.RequestID), AssistantID: in.AssistantID,
		RequestID: in.RequestID, Input: in.Input, State: DispatchPendingClassification,
		WorkEpoch: wc.Epoch, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	agg.Dispatches = append(agg.Dispatches, dispatch)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "open_dispatch", fingerprint, dispatch, now); err != nil {
		return Dispatch{}, err
	}
	if err := s.write(agg); err != nil {
		return Dispatch{}, err
	}
	return clone(dispatch), nil
}

// ClassifyDispatch applies a completed classification and freezes its Runner
// Jobs. Classification is at-most-once per Dispatch: an already classified or
// reflected Dispatch is returned unchanged instead of spawning duplicate jobs.
// Job permission, scope, workspace and ContextPack revision are frozen from the
// assistant snapshot so the classifier cannot escalate access.
func (s *Store) ClassifyDispatch(in ClassifyDispatchInput) (Dispatch, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Dispatch{}, err
	}
	if err := validateID("dispatch", in.DispatchID); err != nil {
		return Dispatch{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return Dispatch{}, err
	}
	if err := validateDispatchKind(in.Kind); err != nil {
		return Dispatch{}, err
	}
	in.Reply = strings.TrimSpace(in.Reply)
	if in.Reply == "" {
		return Dispatch{}, errors.New("assistant: classification reply is required")
	}
	specs := normalizeJobSpecs(in.Jobs)
	if err := validateJobSpecs(specs); err != nil {
		return Dispatch{}, err
	}
	fingerprint, err := inputFingerprint(struct {
		DispatchID string
		Kind       DispatchKind
		Reply      string
		Jobs       []JobSpec
	}{in.DispatchID, in.Kind, in.Reply, specs})
	if err != nil {
		return Dispatch{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	if result, ok, receiptErr := receiptResult[Dispatch](agg, in.RequestID, "classify_dispatch", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := dispatchIndex(agg, in.DispatchID)
	if idx < 0 {
		return Dispatch{}, ErrNotFound
	}
	dispatch := &agg.Dispatches[idx]
	if dispatch.AssistantID != in.AssistantID {
		return Dispatch{}, fmt.Errorf("assistant: dispatch %s belongs to %s: %w", in.DispatchID, dispatch.AssistantID, ErrNotFound)
	}
	if dispatch.State == DispatchClassified || dispatch.State == DispatchReflected || dispatch.State == DispatchExecuted {
		return clone(*dispatch), nil
	}
	if dispatch.State != DispatchPendingClassification && dispatch.State != DispatchClassificationFailed {
		return Dispatch{}, fmt.Errorf("%w: cannot classify dispatch in %s", ErrTransition, dispatch.State)
	}
	// The dispatch was opened under an older work generation: its late
	// classification is a stale-fence write and must not spawn jobs.
	if wc, err := s.WorkControl(); err != nil {
		return Dispatch{}, err
	} else if err := checkWorkEpoch(dispatch.WorkEpoch, wc.Epoch); err != nil {
		return Dispatch{}, err
	}
	if err := s.requireResumeRunning(); err != nil {
		return Dispatch{}, err
	}
	now := storeNow(in.Now)
	watermark := agg.Revision
	for i, spec := range specs {
		job := RunnerJob{
			ID:          StableID("job", fmt.Sprintf("%s/%s/%d/%s", in.AssistantID, in.DispatchID, i, spec.Name)),
			AssistantID: in.AssistantID, DispatchID: in.DispatchID, Name: spec.Name,
			Kind: spec.Kind, Target: spec.Target, Prompt: spec.Prompt,
			Scope: agg.Assistant.Scope, WorkspaceRoot: agg.Assistant.WorkspaceRoot,
			Policy: agg.Assistant.Policy, ContextPackRevision: watermark,
			State: JobQueued, MaxAttempts: spec.MaxAttempts, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		agg.Jobs = append(agg.Jobs, job)
	}
	dispatch.Kind = in.Kind
	dispatch.Reply = in.Reply
	dispatch.State = DispatchClassified
	dispatch.ClassifiedAt = now
	dispatch.Error = nil
	dispatch.RetryAt = time.Time{}
	dispatch.Revision++
	dispatch.UpdatedAt = now
	result := clone(*dispatch)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "classify_dispatch", fingerprint, result, now); err != nil {
		return Dispatch{}, err
	}
	if err := s.write(agg); err != nil {
		return Dispatch{}, err
	}
	return result, nil
}

// BindDispatchSessionInput records the managed Session the supervisor created to
// execute a classified Dispatch.
type BindDispatchSessionInput struct {
	RequestID   string
	AssistantID string
	DispatchID  string
	SessionID   string
	Now         time.Time
}

// BindDispatchSession durably links a classified Dispatch to its execution
// Session and is idempotent by request ID. It is the new-flow execution target
// bookkeeping that replaces the frozen RunnerJob.
func (s *Store) BindDispatchSession(in BindDispatchSessionInput) (Dispatch, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Dispatch{}, err
	}
	if err := validateID("dispatch", in.DispatchID); err != nil {
		return Dispatch{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return Dispatch{}, err
	}
	in.SessionID = strings.TrimSpace(in.SessionID)
	if in.SessionID == "" {
		return Dispatch{}, errors.New("assistant: session id is required")
	}
	fp, err := inputFingerprint(struct {
		DispatchID string
		SessionID  string
	}{in.DispatchID, in.SessionID})
	if err != nil {
		return Dispatch{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	if result, ok, receiptErr := receiptResult[Dispatch](agg, in.RequestID, "bind_dispatch_session", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := dispatchIndex(agg, in.DispatchID)
	if idx < 0 {
		return Dispatch{}, ErrNotFound
	}
	dispatch := &agg.Dispatches[idx]
	now := storeNow(in.Now)
	dispatch.SessionID = in.SessionID
	dispatch.Revision++
	dispatch.UpdatedAt = now
	result := clone(*dispatch)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "bind_dispatch_session", fp, result, now); err != nil {
		return Dispatch{}, err
	}
	if err := s.write(agg); err != nil {
		return Dispatch{}, err
	}
	return result, nil
}

// MarkDispatchExecutedInput marks a Dispatch as executed when its managed
// Session reached a terminal state, so it becomes reflection-ready.
type MarkDispatchExecutedInput struct {
	RequestID   string
	AssistantID string
	DispatchID  string
	Now         time.Time
}

// MarkDispatchExecuted transitions a classified (or reflection-failed) Dispatch
// to the executed state, the converged precondition for reflection. It is
// idempotent and never applies to an already-reflected Dispatch.
func (s *Store) MarkDispatchExecuted(in MarkDispatchExecutedInput) (Dispatch, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Dispatch{}, err
	}
	if err := validateID("dispatch", in.DispatchID); err != nil {
		return Dispatch{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return Dispatch{}, err
	}
	fp, err := inputFingerprint(struct{ DispatchID string }{in.DispatchID})
	if err != nil {
		return Dispatch{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	if result, ok, receiptErr := receiptResult[Dispatch](agg, in.RequestID, "mark_dispatch_executed", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := dispatchIndex(agg, in.DispatchID)
	if idx < 0 {
		return Dispatch{}, ErrNotFound
	}
	dispatch := &agg.Dispatches[idx]
	if dispatch.State == DispatchReflected || dispatch.State == DispatchExecuted {
		return clone(*dispatch), nil
	}
	if dispatch.State != DispatchClassified && dispatch.State != DispatchReflectionFailed {
		return Dispatch{}, fmt.Errorf("%w: cannot mark dispatch in %s as executed", ErrTransition, dispatch.State)
	}
	now := storeNow(in.Now)
	dispatch.State = DispatchExecuted
	dispatch.Revision++
	dispatch.UpdatedAt = now
	result := clone(*dispatch)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "mark_dispatch_executed", fp, result, now); err != nil {
		return Dispatch{}, err
	}
	if err := s.write(agg); err != nil {
		return Dispatch{}, err
	}
	return result, nil
}

// FailDispatch records an explicit, retryable classification failure while
// keeping the raw input. It never pretends an unclassified input was executed.
func (s *Store) FailDispatch(in FailDispatchInput) (Dispatch, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Dispatch{}, err
	}
	if err := validateID("dispatch", in.DispatchID); err != nil {
		return Dispatch{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return Dispatch{}, err
	}
	failure := in.Failure
	now := storeNow(failure.Now)
	fingerprint, err := inputFingerprint(struct {
		DispatchID string
	}{in.DispatchID})
	if err != nil {
		return Dispatch{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	if result, ok, receiptErr := receiptResult[Dispatch](agg, in.RequestID, "fail_dispatch", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := dispatchIndex(agg, in.DispatchID)
	if idx < 0 {
		return Dispatch{}, ErrNotFound
	}
	dispatch := &agg.Dispatches[idx]
	if dispatch.State == DispatchClassified || dispatch.State == DispatchReflected || dispatch.State == DispatchExecuted {
		return clone(*dispatch), nil
	}
	failure.Code = strings.TrimSpace(failure.Code)
	failure.Message = strings.TrimSpace(failure.Message)
	if failure.Code == "" || failure.Message == "" {
		return Dispatch{}, errors.New("assistant: failure code and message are required")
	}
	dispatch.State = DispatchClassificationFailed
	dispatch.ClassificationAttempt++
	dispatch.RetryAt = now.Add(roleBackoff(dispatch.ClassificationAttempt))
	dispatch.Error = &RunError{
		Code: failure.Code, Message: failure.Message, Provider: failure.Provider,
		Retryable: failure.Retryable, OutcomeKnown: true, At: now,
	}
	dispatch.Revision++
	dispatch.UpdatedAt = now
	result := clone(*dispatch)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "fail_dispatch", fingerprint, result, now); err != nil {
		return Dispatch{}, err
	}
	if err := s.write(agg); err != nil {
		return Dispatch{}, err
	}
	return result, nil
}

// ReflectDispatch persists exactly one bounded ContextPack for a Dispatch whose
// jobs are all terminal. Replay returns the same pack; a Dispatch with pending
// jobs is rejected so reflection can never run early.
func (s *Store) ReflectDispatch(in ReflectInput) (ContextPack, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return ContextPack{}, err
	}
	if err := validateID("dispatch", in.DispatchID); err != nil {
		return ContextPack{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return ContextPack{}, err
	}
	content := normalizeContextPack(in.Content)
	if err := validateContextPackContent(content); err != nil {
		return ContextPack{}, err
	}
	// Reflection is exactly-once per Dispatch, not per model payload: the model
	// may emit a different content blob on a re-entrant/duplicate call, so the
	// idempotency fingerprint keys on the Dispatch alone. The same request ID
	// reused for a different Dispatch still yields an idempotency conflict.
	fingerprint, err := inputFingerprint(struct {
		DispatchID string
	}{in.DispatchID})
	if err != nil {
		return ContextPack{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return ContextPack{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return ContextPack{}, err
	}
	if result, ok, receiptErr := receiptResult[ContextPack](agg, in.RequestID, "reflect_dispatch", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := dispatchIndex(agg, in.DispatchID)
	if idx < 0 {
		return ContextPack{}, ErrNotFound
	}
	dispatch := &agg.Dispatches[idx]
	if existing := contextPackForDispatch(agg, in.DispatchID); existing != nil {
		return clone(*existing), nil
	}
	if dispatch.State != DispatchExecuted && !dispatchJobsTerminal(agg, in.DispatchID) {
		return ContextPack{}, fmt.Errorf("%w: dispatch %s has not reached a terminal execution state", ErrTransition, in.DispatchID)
	}
	now := storeNow(in.Now)
	pack := ContextPack{
		ID: StableID("pack", in.AssistantID+"/"+in.DispatchID), AssistantID: in.AssistantID,
		DispatchID: in.DispatchID, Conclusion: content.Conclusion, Evidence: content.Evidence,
		Failures: content.Failures, Strategies: content.Strategies, OpenLoops: content.OpenLoops,
		RunnerContext: content.RunnerContext, BoundJobIDs: jobIDsForDispatch(agg, in.DispatchID),
		CreatedAt: now,
	}
	dispatch.State = DispatchReflected
	dispatch.Revision++
	dispatch.UpdatedAt = now
	touch(agg, now)
	pack.Revision = agg.Revision
	agg.ContextPacks = append(agg.ContextPacks, pack)
	result := clone(pack)
	if err := putReceipt(agg, in.RequestID, "reflect_dispatch", fingerprint, result, now); err != nil {
		return ContextPack{}, err
	}
	if err := s.write(agg); err != nil {
		return ContextPack{}, err
	}
	return result, nil
}

// FailReflection records a retryable reflection failure for a terminal Dispatch
// with bounded backoff. It is idempotent and never applies when a ContextPack
// already exists or jobs are still non-terminal.
func (s *Store) FailReflection(in FailReflectionInput) (Dispatch, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Dispatch{}, err
	}
	if err := validateID("dispatch", in.DispatchID); err != nil {
		return Dispatch{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return Dispatch{}, err
	}
	fingerprint, err := inputFingerprint(struct{ DispatchID string }{in.DispatchID})
	if err != nil {
		return Dispatch{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	if result, ok, receiptErr := receiptResult[Dispatch](agg, in.RequestID, "fail_reflection", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := dispatchIndex(agg, in.DispatchID)
	if idx < 0 {
		return Dispatch{}, ErrNotFound
	}
	dispatch := &agg.Dispatches[idx]
	if contextPackForDispatch(agg, in.DispatchID) != nil {
		return clone(*dispatch), nil
	}
	if dispatch.State != DispatchExecuted && !dispatchJobsTerminal(agg, in.DispatchID) {
		return Dispatch{}, fmt.Errorf("%w: dispatch %s has not reached a terminal execution state", ErrTransition, in.DispatchID)
	}
	now := storeNow(in.Failure.Now)
	failure := in.Failure
	failure.Code = strings.TrimSpace(failure.Code)
	failure.Message = strings.TrimSpace(failure.Message)
	if failure.Code == "" || failure.Message == "" {
		return Dispatch{}, errors.New("assistant: failure code and message are required")
	}
	dispatch.State = DispatchReflectionFailed
	dispatch.Error = &RunError{Code: failure.Code, Message: failure.Message, Provider: failure.Provider, Retryable: true, OutcomeKnown: true, At: now}
	dispatch.ReflectionAttempt++
	dispatch.RetryAt = now.Add(roleBackoff(dispatch.ReflectionAttempt))
	dispatch.Revision++
	dispatch.UpdatedAt = now
	result := clone(*dispatch)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "fail_reflection", fingerprint, result, now); err != nil {
		return Dispatch{}, err
	}
	if err := s.write(agg); err != nil {
		return Dispatch{}, err
	}
	return result, nil
}

// FailIdeation records a retryable cadence-ideation failure with bounded
// backoff so the host does not retry on every tick. Manual ideation failures are
// returned to the caller instead and never reach this path.
func (s *Store) FailIdeation(in FailIdeationInput) error {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return err
	}
	fingerprint, err := inputFingerprint(struct{ AssistantID string }{in.AssistantID})
	if err != nil {
		return err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return err
	}
	now := storeNow(in.Now)
	agg.Ideation.Attempt++
	agg.Ideation.LastAttemptAt = now
	agg.Ideation.RetryAt = now.Add(roleBackoff(agg.Ideation.Attempt))
	agg.Ideation.Error = strings.TrimSpace(in.Message)
	touch(agg, now)
	receipt := agg.Ideation
	if err := putReceipt(agg, in.RequestID, "fail_ideation", fingerprint, receipt, now); err != nil {
		return err
	}
	return s.write(agg)
}

// ShouldIdeate reports whether the ideation cadence is due: at least
// ideaCadenceSuccessfulTasks successful task Dispatches since the last idea, or
// ideaCadenceInterval elapsed since the last idea (or since creation).
func (s *Store) ShouldIdeate(assistantID string, now time.Time) (bool, string, int, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return false, "", 0, err
	}
	agg, err := s.read(assistantID)
	if err != nil {
		return false, "", 0, err
	}
	due, reason, count := ideationDue(agg, storeNow(now))
	return due, reason, count, nil
}

// OpenIdea creates an IdeaProposal. Manual trigger is always allowed; cadence
// trigger is rejected unless ShouldIdeate is true, so the Ideator stays
// low-frequency. Reusing the same request ID returns the same proposal.
func (s *Store) OpenIdea(in OpenIdeaInput) (IdeaProposal, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return IdeaProposal{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return IdeaProposal{}, err
	}
	in.Summary = strings.TrimSpace(in.Summary)
	in.Rationale = strings.TrimSpace(in.Rationale)
	in.StrategyMemory = strings.TrimSpace(in.StrategyMemory)
	in.Responsibility = strings.TrimSpace(in.Responsibility)
	in.Objective = strings.TrimSpace(in.Objective)
	in.DoneCriteria = strings.TrimSpace(in.DoneCriteria)
	in.NextAction = strings.TrimSpace(in.NextAction)
	switch in.Trigger {
	case IdeaTriggerManual, IdeaTriggerCadence:
	default:
		return IdeaProposal{}, fmt.Errorf("assistant: invalid idea trigger %q", in.Trigger)
	}
	if in.Summary == "" {
		return IdeaProposal{}, errors.New("assistant: idea summary is required")
	}
	if in.Responsibility != "" {
		if !validAlias(in.Responsibility) {
			return IdeaProposal{}, errors.New("assistant: idea responsibility must be a valid alias")
		}
		if in.Objective == "" {
			return IdeaProposal{}, errors.New("assistant: idea responsibility requires an objective")
		}
	}
	fingerprint, err := inputFingerprint(struct {
		AssistantID, Trigger, Summary, Rationale, StrategyMemory string
		Responsibility, Objective, DoneCriteria, NextAction      string
	}{in.AssistantID, string(in.Trigger), in.Summary, in.Rationale, in.StrategyMemory, in.Responsibility, in.Objective, in.DoneCriteria, in.NextAction})
	if err != nil {
		return IdeaProposal{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return IdeaProposal{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return IdeaProposal{}, err
	}
	if result, ok, receiptErr := receiptResult[IdeaProposal](agg, in.RequestID, "open_idea", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	now := storeNow(in.Now)
	if in.Trigger == IdeaTriggerCadence {
		due, _, _ := ideationDue(agg, now)
		if !due {
			return IdeaProposal{}, fmt.Errorf("%w: ideation cadence is not due", ErrTransition)
		}
	}
	idea := IdeaProposal{
		ID: StableID("idea", in.AssistantID+"/"+in.RequestID), AssistantID: in.AssistantID,
		RequestID: in.RequestID, Trigger: in.Trigger, Summary: in.Summary, Rationale: in.Rationale,
		StrategyMemory: in.StrategyMemory, Responsibility: in.Responsibility, Objective: in.Objective,
		DoneCriteria: in.DoneCriteria, NextAction: in.NextAction, State: IdeaPending,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	agg.Ideas = append(agg.Ideas, idea)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "open_idea", fingerprint, idea, now); err != nil {
		return IdeaProposal{}, err
	}
	if err := s.write(agg); err != nil {
		return IdeaProposal{}, err
	}
	return clone(idea), nil
}

// ResolveIdea applies an accept/reject decision under revision CAS. Acceptance
// only writes a strategy memory item and/or a new responsibility candidate; it
// never mutates the mission, policy, workspace, credentials, or publish config,
// and never executes external work. A superseded target converges idempotently.
func (s *Store) ResolveIdea(in ResolveIdeaInput) (IdeaProposal, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return IdeaProposal{}, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return IdeaProposal{}, err
	}
	if err := validateID("idea", in.IdeaID); err != nil {
		return IdeaProposal{}, err
	}
	switch in.Decision {
	case IdeaAccept, IdeaReject:
	default:
		return IdeaProposal{}, errors.New("assistant: idea decision must be accept or reject")
	}
	in.Resolution = strings.TrimSpace(in.Resolution)
	fingerprint, err := inputFingerprint(struct {
		IdeaID     string
		Expected   int64
		Decision   IdeaDecision
		Resolution string
	}{in.IdeaID, in.ExpectedRevision, in.Decision, in.Resolution})
	if err != nil {
		return IdeaProposal{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return IdeaProposal{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return IdeaProposal{}, err
	}
	if result, ok, receiptErr := receiptResult[IdeaProposal](agg, in.RequestID, "resolve_idea", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := ideaIndex(agg, in.IdeaID)
	if idx < 0 {
		return IdeaProposal{}, ErrNotFound
	}
	idea := &agg.Ideas[idx]
	if idea.Revision != in.ExpectedRevision {
		return IdeaProposal{}, conflict("idea", idea.ID, in.ExpectedRevision, idea.Revision)
	}
	if idea.State != IdeaPending {
		return IdeaProposal{}, fmt.Errorf("%w: idea %s is %s", ErrTransition, idea.ID, idea.State)
	}
	now := storeNow(in.Now)
	superseded := false
	if in.Decision == IdeaAccept && idea.Responsibility != "" {
		for _, responsibility := range agg.Plan.Responsibilities {
			if responsibility.Alias != idea.Responsibility {
				continue
			}
			idea.State = IdeaSuperseded
			idea.Resolution = fmt.Sprintf("责任 %s 已存在，提案未应用", idea.Responsibility)
			superseded = true
			break
		}
	}
	if !superseded && in.Decision == IdeaAccept {
		if idea.StrategyMemory != "" {
			patch := MemoryPatch{Upsert: []MemoryItem{{
				ID: StableID("memory", "idea:"+idea.ID), Kind: MemoryStrategy,
				Body: idea.StrategyMemory, SourceRun: "", Evidence: "idea:" + idea.ID,
			}}}
			if err := applyMemoryPatch(&agg.Memory, patch, now); err != nil {
				return IdeaProposal{}, err
			}
			agg.Assistant.MemoryRev = agg.Memory.Revision
		}
		if idea.Responsibility != "" {
			agg.Plan.Responsibilities = append(agg.Plan.Responsibilities, Responsibility{
				ID: StableID("resp", idea.AssistantID+"/"+idea.Responsibility), AssistantID: idea.AssistantID,
				Alias: idea.Responsibility, Objective: idea.Objective, DoneCriteria: idea.DoneCriteria,
				NextAction: idea.NextAction, Disposition: DispositionPlanned,
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
			agg.Plan.Revision++
			// The plan persists decision states only; refresh the derived Status
			// projection so the written JSON stays consistent.
			deriveResponsibilityStatuses(&agg.Plan)
		}
		idea.State = IdeaAccepted
	} else if !superseded {
		idea.State = IdeaRejected
	}
	if !superseded {
		idea.Resolution = in.Resolution
	}
	idea.ResolvedAt = now
	idea.Revision++
	idea.UpdatedAt = now
	result := clone(*idea)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "resolve_idea", fingerprint, result, now); err != nil {
		return IdeaProposal{}, err
	}
	if err := s.write(agg); err != nil {
		return IdeaProposal{}, err
	}
	return result, nil
}

// normalizeJobSpecs fills defaults and trims fields without rewriting user text.
func normalizeJobSpecs(in []JobSpec) []JobSpec {
	out := make([]JobSpec, len(in))
	for i, spec := range in {
		spec.Name = strings.TrimSpace(spec.Name)
		spec.Target = strings.TrimSpace(spec.Target)
		spec.Prompt = strings.TrimSpace(spec.Prompt)
		if spec.MaxAttempts < 1 {
			spec.MaxAttempts = 3
		}
		out[i] = spec
	}
	return out
}

func validateJobSpecs(specs []JobSpec) error {
	seen := map[string]bool{}
	for i, spec := range specs {
		if spec.Name == "" {
			return fmt.Errorf("assistant: job %d requires a name", i)
		}
		if spec.Prompt == "" {
			return fmt.Errorf("assistant: job %q requires a prompt", spec.Name)
		}
		if err := validateDispatchKind(spec.Kind); err != nil {
			return err
		}
		if seen[spec.Name] {
			return fmt.Errorf("assistant: duplicate job name %q", spec.Name)
		}
		seen[spec.Name] = true
	}
	return nil
}

func normalizeContextPack(in ContextPackContent) ContextPackContent {
	in.Conclusion = strings.TrimSpace(in.Conclusion)
	in.RunnerContext = strings.TrimSpace(in.RunnerContext)
	in.Evidence = trimList(in.Evidence)
	in.Failures = trimList(in.Failures)
	in.Strategies = trimList(in.Strategies)
	in.OpenLoops = trimList(in.OpenLoops)
	return in
}

func trimList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func validateContextPackContent(content ContextPackContent) error {
	if content.Conclusion == "" {
		return errors.New("assistant: context pack requires a conclusion")
	}
	if len(content.Conclusion) > contextPackMaxBytes || len(content.RunnerContext) > contextPackMaxBytes {
		return errors.New("assistant: context pack field exceeds byte limit")
	}
	for name, list := range map[string][]string{
		"evidence": content.Evidence, "failures": content.Failures,
		"strategies": content.Strategies, "open_loops": content.OpenLoops,
	} {
		if len(list) > maxContextPackItems {
			return fmt.Errorf("assistant: context pack %s exceeds %d items", name, maxContextPackItems)
		}
		for _, item := range list {
			if len(item) > contextPackMaxBytes {
				return fmt.Errorf("assistant: context pack %s item exceeds byte limit", name)
			}
		}
	}
	if contextPackContentBytes(content) > contextPackTotalMaxBytes {
		return fmt.Errorf("assistant: context pack exceeds %d total bytes", contextPackTotalMaxBytes)
	}
	return nil
}

func contextPackContentBytes(content ContextPackContent) int {
	n := len(content.Conclusion) + len(content.RunnerContext)
	for _, list := range [][]string{content.Evidence, content.Failures, content.Strategies, content.OpenLoops} {
		for _, item := range list {
			n += len(item)
		}
	}
	return n
}

func contextPackForDispatch(agg *aggregate, dispatchID string) *ContextPack {
	for i := range agg.ContextPacks {
		if agg.ContextPacks[i].DispatchID == dispatchID {
			return &agg.ContextPacks[i]
		}
	}
	return nil
}

func jobIDsForDispatch(agg *aggregate, dispatchID string) []string {
	out := make([]string, 0)
	for i := range agg.Jobs {
		if agg.Jobs[i].DispatchID == dispatchID {
			out = append(out, agg.Jobs[i].ID)
		}
	}
	sort.Strings(out)
	return out
}

func ideationDue(agg *aggregate, now time.Time) (bool, string, int) {
	if !agg.Ideation.RetryAt.IsZero() && agg.Ideation.RetryAt.After(now) {
		return false, "", 0
	}
	last := lastIdeaAt(agg)
	elapsed := now.Sub(last)
	if elapsed >= ideaCadenceInterval {
		return true, "interval", 0
	}
	count := successfulTaskDispatchesSince(agg, last)
	if count >= ideaCadenceSuccessfulTasks {
		return true, "tasks", count
	}
	return false, "", count
}

func lastIdeaAt(agg *aggregate) time.Time {
	last := agg.Assistant.CreatedAt
	for i := range agg.Ideas {
		if agg.Ideas[i].CreatedAt.After(last) {
			last = agg.Ideas[i].CreatedAt
		}
	}
	return last
}

func successfulTaskDispatchesSince(agg *aggregate, since time.Time) int {
	count := 0
	for i := range agg.Dispatches {
		d := agg.Dispatches[i]
		if d.Kind != DispatchTask || d.State != DispatchReflected {
			continue
		}
		if d.ClassifiedAt.Before(since) {
			continue
		}
		// Reflection implies the managed Session reached a terminal state and its
		// results were written back, which is the converged "successful task"
		// signal (no longer coupled to frozen Runner Jobs).
		count++
	}
	return count
}

// ApplicableContextPacks selects bounded ContextPacks that a Runner Job may
// inject: same assistant, no newer than the frozen revision, and excluding the
// job's own source dispatch. Ordering is newest-first and both item count and
// total UTF-8 bytes are bounded.
func ApplicableContextPacks(packs []ContextPack, assistantID string, excludeDispatchID string, maxRevision int64, maxItems, maxBytes int) []ContextPack {
	type candidate struct {
		pack  ContextPack
		order int
	}
	selected := make([]candidate, 0, len(packs))
	for i, pack := range packs {
		if pack.AssistantID != assistantID {
			continue
		}
		if pack.DispatchID == excludeDispatchID {
			continue
		}
		if maxRevision > 0 && pack.Revision > maxRevision {
			continue
		}
		selected = append(selected, candidate{pack: pack, order: i})
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].pack.CreatedAt.Equal(selected[j].pack.CreatedAt) {
			return selected[i].order > selected[j].order
		}
		return selected[i].pack.CreatedAt.After(selected[j].pack.CreatedAt)
	})
	out := make([]ContextPack, 0, len(selected))
	total := 0
	for _, item := range selected {
		if len(out) >= maxItems || total >= maxBytes {
			break
		}
		pack := item.pack
		cost := packBytes(pack)
		if cost == 0 {
			continue
		}
		if cost > maxBytes-total {
			continue
		}
		out = append(out, pack)
		total += cost
	}
	return out
}

func packBytes(pack ContextPack) int {
	n := len(pack.Conclusion) + len(pack.RunnerContext)
	for _, v := range pack.Evidence {
		n += len(v)
	}
	for _, v := range pack.Failures {
		n += len(v)
	}
	for _, v := range pack.Strategies {
		n += len(v)
	}
	for _, v := range pack.OpenLoops {
		n += len(v)
	}
	return n
}
