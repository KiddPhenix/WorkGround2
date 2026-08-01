package work

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type inputInferrerFunc func(context.Context, InputInferencePlanInput) (*InferWorkInputsResult, error)

func (f inputInferrerFunc) InferInputs(ctx context.Context, input InputInferencePlanInput) (*InferWorkInputsResult, error) {
	return f(ctx, input)
}

func TestServiceInferWorkInputsReturnsValidatedDraftWithoutWrite(t *testing.T) {
	_, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	before, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetInputInferrer(inputInferrerFunc(func(_ context.Context, input InputInferencePlanInput) (*InferWorkInputsResult, error) {
		if len(input.Targets) != 1 || input.Targets[0].InputID != inputID || input.Work.ID != workID {
			t.Fatalf("unexpected inference context: %#v", input)
		}
		return &InferWorkInputsResult{Items: []InferredWorkInput{{
			InputID: inputID, Value: json.RawMessage(`"采用稳妥的默认范围"`), Reason: "依据任务目标",
		}}, Skipped: []SkippedWorkInput{}}, nil
	}))

	result, err := svc.InferWorkInputs(context.Background(), InferWorkInputsRequest{
		WorkID: workID, RunID: "run-1", InputIDs: []string{inputID}, DefinitionRevision: before.V2CurrentRevision,
	})
	if err != nil {
		t.Fatalf("InferWorkInputs: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].InputID != inputID || string(result.Items[0].Value) != `"采用稳妥的默认范围"` {
		t.Fatalf("result = %#v", result)
	}
	after, afterState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Revision != state.Revision || after.V2Inputs[0].State != InputRequested || after.V2Inputs[0].Revision != before.V2Inputs[0].Revision {
		t.Fatalf("read-only inference mutated state: before=%#v/%#v after=%#v/%#v", before.V2Inputs, state, after.V2Inputs, afterState)
	}
}

func TestServiceInferWorkInputsRejectsInvalidModelValue(t *testing.T) {
	_, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	current, _, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetInputInferrer(inputInferrerFunc(func(context.Context, InputInferencePlanInput) (*InferWorkInputsResult, error) {
		return &InferWorkInputsResult{Items: []InferredWorkInput{{InputID: inputID, Value: json.RawMessage(`null`)}}}, nil
	}))
	_, err = svc.InferWorkInputs(context.Background(), InferWorkInputsRequest{
		WorkID: workID, RunID: "run-1", DefinitionRevision: current.V2CurrentRevision,
	})
	if !errors.Is(err, ErrInputInferenceFailed) {
		t.Fatalf("err = %v, want ErrInputInferenceFailed", err)
	}
}
