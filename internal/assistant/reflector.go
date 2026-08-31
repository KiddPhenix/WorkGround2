package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Reflector turns one fully-terminal Dispatch into exactly one bounded
// ContextPack using a real model role call. It never runs early and never
// duplicates: the Store enforces the all-jobs-terminal precondition and
// idempotent replay. A model or parse failure is persisted as a retryable
// reflection state with bounded backoff.
type Reflector struct {
	store *Store
	model RoleModel
}

func NewReflector(store *Store, model RoleModel) (*Reflector, error) {
	if store == nil {
		return nil, errors.New("assistant: reflector store is required")
	}
	if model == nil {
		return nil, errors.New("assistant: reflector requires a role model")
	}
	return &Reflector{store: store, model: model}, nil
}

// Reflect synthesizes and persists one ContextPack for a terminal Dispatch. If a
// pack already exists (for example after a lost response replay) it returns that
// pack unchanged. Model or parse failures are recorded as retryable reflection
// failures and returned as errors.
func (r *Reflector) Reflect(ctx context.Context, assistantID, dispatchID, requestID string, now time.Time) (ContextPack, error) {
	snapshot, err := r.store.Get(assistantID)
	if err != nil {
		return ContextPack{}, err
	}
	dispatch, ok := findDispatch(snapshot.Dispatches, dispatchID)
	if !ok {
		return ContextPack{}, ErrNotFound
	}
	jobs := jobsForDispatch(snapshot.Jobs, dispatchID)
	prompt := ReflectorPrompt(snapshot, dispatch, jobs)
	text, modelErr := r.model.Complete(WithRoleContext(ctx, snapshot.Assistant), prompt)
	if modelErr != nil {
		return ContextPack{}, r.fail(assistantID, dispatchID, snapshot, "reflection_model_unavailable", modelErr, now)
	}
	content, parseErr := ParseReflectorOutput(text)
	if parseErr != nil {
		return ContextPack{}, r.fail(assistantID, dispatchID, snapshot, "reflection_invalid", parseErr, now)
	}
	return r.store.ReflectDispatch(ReflectInput{
		AssistantID: assistantID, DispatchID: dispatchID, RequestID: requestID,
		Content: content, Now: now,
	})
}

func (r *Reflector) fail(assistantID, dispatchID string, snapshot Snapshot, code string, cause error, now time.Time) error {
	attempt := 0
	for _, d := range snapshot.Dispatches {
		if d.ID == dispatchID {
			attempt = d.ReflectionAttempt
			break
		}
	}
	requestID := fmt.Sprintf("reflect-fail:%s:%d", dispatchID, attempt+1)
	_, persistErr := r.store.FailReflection(FailReflectionInput{
		AssistantID: assistantID, DispatchID: dispatchID, RequestID: requestID,
		Failure: Failure{Code: code, Message: cause.Error(), Retryable: true, OutcomeKnown: true, Now: now},
	})
	if persistErr != nil {
		return errors.Join(cause, persistErr)
	}
	return cause
}

func findDispatch(dispatches []Dispatch, id string) (Dispatch, bool) {
	for _, d := range dispatches {
		if d.ID == id {
			return d, true
		}
	}
	return Dispatch{}, false
}

func jobsForDispatch(jobs []RunnerJob, dispatchID string) []RunnerJob {
	out := make([]RunnerJob, 0)
	for _, j := range jobs {
		if j.DispatchID == dispatchID {
			out = append(out, j)
		}
	}
	return out
}

// DispatchesReadyForReflection returns Dispatches whose managed Session reached
// a terminal state (DispatchExecuted) and which have no ContextPack yet, plus
// previously failed reflections whose bounded backoff has elapsed. Legacy
// Dispatches with frozen Jobs remain supported read-only: their jobs being all
// terminal still counts as ready.
func DispatchesReadyForReflection(snapshot Snapshot, now time.Time) []Dispatch {
	hasPack := func(dispatchID string) bool {
		for i := range snapshot.ContextPacks {
			if snapshot.ContextPacks[i].DispatchID == dispatchID {
				return true
			}
		}
		return false
	}
	jobsTerminal := func(dispatchID string) bool {
		found := false
		for i := range snapshot.Jobs {
			if snapshot.Jobs[i].DispatchID != dispatchID {
				continue
			}
			found = true
			if !jobTerminal(snapshot.Jobs[i].State) {
				return false
			}
		}
		return found
	}
	out := make([]Dispatch, 0)
	for _, d := range snapshot.Dispatches {
		if hasPack(d.ID) {
			continue
		}
		if d.State != DispatchExecuted && !jobsTerminal(d.ID) {
			continue
		}
		switch d.State {
		case DispatchClassified, DispatchExecuted:
			out = append(out, d)
		case DispatchReflectionFailed:
			if d.RetryAt.IsZero() || !d.RetryAt.After(now) {
				out = append(out, d)
			}
		}
	}
	return out
}
