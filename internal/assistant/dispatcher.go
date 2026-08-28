package assistant

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Dispatcher classifies a raw direct input into zero or more frozen Runner Jobs
// using a real model role call. The input is persisted first; classification is
// applied at most once; and a model or parse failure leaves an explicit,
// retryable state without losing the input. There is no heuristic fallback and
// no fabricated reply.
type Dispatcher struct {
	store    *Store
	model    RoleModel
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	observer ReplyObserver
}

func NewDispatcher(store *Store, model RoleModel) (*Dispatcher, error) {
	if store == nil {
		return nil, errors.New("assistant: dispatcher store is required")
	}
	if model == nil {
		return nil, errors.New("assistant: dispatcher requires a role model")
	}
	return &Dispatcher{store: store, model: model, locks: map[string]*sync.Mutex{}}, nil
}

// SetReplyObserver installs an optional observer that receives incremental,
// escape-decoded previews of the Dispatcher reply while a streaming model runs.
// It must be set before classification starts; previews are provisional and
// never replace the validated Dispatch.Reply.
func (d *Dispatcher) SetReplyObserver(observer ReplyObserver) {
	d.mu.Lock()
	d.observer = observer
	d.mu.Unlock()
}

func (d *Dispatcher) currentObserver() ReplyObserver {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.observer
}

// Dispatch opens (or replays) a Dispatch and, if it is not yet classified, runs
// the model and applies the result. The classify request ID is derived from the
// dispatch ID so retries are idempotent.
func (d *Dispatcher) Dispatch(ctx context.Context, in OpenDispatchInput) (Dispatch, error) {
	dispatch, err := d.store.OpenDispatch(in)
	if err != nil {
		return Dispatch{}, err
	}
	unlock := d.lock(in.AssistantID)
	defer unlock()
	snapshot, err := d.store.Get(in.AssistantID)
	if err != nil {
		return Dispatch{}, err
	}
	if current, ok := findDispatch(snapshot.Dispatches, dispatch.ID); ok {
		dispatch = current
	}
	if dispatch.State == DispatchClassified || dispatch.State == DispatchReflected {
		return dispatch, nil
	}
	return d.classify(ctx, in.AssistantID, dispatch, in.Now)
}

// RetryDispatch re-runs classification for a Dispatch stuck in
// pending_classification or classification_failed. Classified and reflected
// Dispatches are returned unchanged.
func (d *Dispatcher) RetryDispatch(ctx context.Context, assistantID, dispatchID string, now time.Time) (Dispatch, error) {
	unlock := d.lock(assistantID)
	defer unlock()
	snapshot, err := d.store.Get(assistantID)
	if err != nil {
		return Dispatch{}, err
	}
	dispatch, ok := findDispatch(snapshot.Dispatches, dispatchID)
	if !ok {
		return Dispatch{}, ErrNotFound
	}
	if dispatch.State == DispatchClassified || dispatch.State == DispatchReflected {
		return dispatch, nil
	}
	return d.classify(ctx, assistantID, dispatch, now)
}

func (d *Dispatcher) lock(assistantID string) func() {
	d.mu.Lock()
	lock := d.locks[assistantID]
	if lock == nil {
		lock = &sync.Mutex{}
		d.locks[assistantID] = lock
	}
	d.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (d *Dispatcher) classify(ctx context.Context, assistantID string, dispatch Dispatch, now time.Time) (Dispatch, error) {
	snapshot, err := d.store.Get(assistantID)
	if err != nil {
		return Dispatch{}, err
	}
	prompt := DispatcherPrompt(snapshot, dispatch.Input)
	text, modelErr := d.complete(ctx, assistantID, dispatch, prompt)
	if modelErr != nil {
		return d.fail(assistantID, dispatch, "classification_model_unavailable", modelErr, now)
	}
	classification, parseErr := ParseDispatcherOutput(text)
	if parseErr != nil {
		return d.fail(assistantID, dispatch, "classification_invalid", parseErr, now)
	}
	return d.store.ClassifyDispatch(ClassifyDispatchInput{
		AssistantID: assistantID, DispatchID: dispatch.ID,
		RequestID: classifyRequestID(dispatch.ID),
		Kind:      classification.Kind, Reply: classification.Reply,
		// The Dispatcher keeps the user-intent entry and streaming reply but no
		// longer freezes Jobs; the supervisor creates a managed Session for a
		// task Dispatch (17.9 convergence).
		Jobs: nil, Now: now,
	})
}

// complete runs the model once, optionally streaming incremental "reply"
// previews to the installed observer. The final text is authoritative; previews
// are best-effort and never fabricated by the Dispatcher itself.
func (d *Dispatcher) complete(ctx context.Context, assistantID string, dispatch Dispatch, prompt string) (string, error) {
	streamer, ok := d.model.(StreamRoleModel)
	observer := d.currentObserver()
	if !ok || observer == nil {
		return d.model.Complete(ctx, prompt)
	}
	decoder := &replyStreamDecoder{}
	var last string
	text, err := streamer.CompleteStream(ctx, prompt, func(delta string) {
		if delta == "" {
			return
		}
		reply, changed := decoder.Feed(delta)
		if changed && reply != last {
			last = reply
			observer(ReplyPreview{
				AssistantID: assistantID, DispatchID: dispatch.ID,
				RequestID: dispatch.RequestID, Reply: reply,
			})
		}
	})
	if err != nil {
		return "", err
	}
	return text, nil
}

// fail persists a retryable classification failure. If the failure cannot be
// persisted, the original error is joined so the host still sees it.
func (d *Dispatcher) fail(assistantID string, dispatch Dispatch, code string, cause error, now time.Time) (Dispatch, error) {
	failed, persistErr := d.store.FailDispatch(FailDispatchInput{
		AssistantID: assistantID, DispatchID: dispatch.ID,
		RequestID: failRequestID(dispatch.ID, dispatch.ClassificationAttempt+1),
		Failure: Failure{
			Code: code, Message: cause.Error(),
			Retryable: true, OutcomeKnown: true, Now: now,
		},
	})
	if persistErr != nil {
		return Dispatch{}, errors.Join(cause, persistErr)
	}
	return failed, nil
}

func classifyRequestID(dispatchID string) string {
	return fmt.Sprintf("classify:%s", dispatchID)
}

func failRequestID(dispatchID string, attempt int) string {
	return fmt.Sprintf("classify-fail:%s:%d", dispatchID, attempt)
}
