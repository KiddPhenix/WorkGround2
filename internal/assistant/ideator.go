package assistant

import (
	"context"
	"errors"
	"fmt"
)

// Ideator produces a pending IdeaProposal using a real model role call. It runs
// on a low-frequency cadence or on manual request; acceptance is always a
// separate human CAS decision, so the Ideator never applies the idea itself.
// Manual failures surface directly to the caller; cadence failures are persisted
// with bounded backoff by the Store.
type Ideator struct {
	store *Store
	model RoleModel
}

func NewIdeator(store *Store, model RoleModel) (*Ideator, error) {
	if store == nil {
		return nil, errors.New("assistant: ideator store is required")
	}
	if model == nil {
		return nil, errors.New("assistant: ideator requires a role model")
	}
	return &Ideator{store: store, model: model}, nil
}

// Ideate generates and persists a pending IdeaProposal. Cadence-triggered ideas
// are rejected by the Store when not due; manual ideas always proceed. A model
// or parse failure returns an error; cadence failures are also persisted for
// bounded-backoff retry.
func (id *Ideator) Ideate(ctx context.Context, in OpenIdeaInput) (IdeaProposal, error) {
	snapshot, err := id.store.Get(in.AssistantID)
	if err != nil {
		return IdeaProposal{}, err
	}
	prompt := IdeatorPrompt(snapshot, in.Trigger)
	text, modelErr := id.model.Complete(ctx, prompt)
	if modelErr != nil {
		id.failCadence(in, "ideation_model_unavailable", modelErr)
		return IdeaProposal{}, modelErr
	}
	content, parseErr := ParseIdeatorOutput(text)
	if parseErr != nil {
		id.failCadence(in, "ideation_invalid", parseErr)
		return IdeaProposal{}, parseErr
	}
	in.Summary = content.Summary
	in.Rationale = content.Rationale
	in.StrategyMemory = content.StrategyMemory
	in.Responsibility = content.Responsibility
	in.Objective = content.Objective
	in.DoneCriteria = content.DoneCriteria
	in.NextAction = content.NextAction
	return id.store.OpenIdea(in)
}

func (id *Ideator) failCadence(in OpenIdeaInput, code string, cause error) {
	if in.Trigger != IdeaTriggerCadence {
		return
	}
	snapshot, err := id.store.Get(in.AssistantID)
	if err != nil {
		return
	}
	requestID := fmt.Sprintf("ideate-fail:%s:%d", in.AssistantID, snapshot.Ideation.Attempt+1)
	_ = id.store.FailIdeation(FailIdeationInput{
		AssistantID: in.AssistantID, RequestID: requestID,
		Message: code + ": " + cause.Error(), Now: in.Now,
	})
}
