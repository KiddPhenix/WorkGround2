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

func TestServiceInferWorkInputsSuggestsReplacementForExplicitCompletedInput(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	committed, err := inputSvc.SubmitInput(context.Background(), SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"已保存的范围"`),
		DefinitionRev: defRev, InputRevision: inputRev,
		ExpectedRevision: workRev, RequestID: "submit-before-suggestion",
	})
	if err != nil || committed.Error != "" || committed.Input == nil {
		t.Fatalf("SubmitInput: result=%#v err=%v", committed, err)
	}
	before, beforeState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetInputInferrer(inputInferrerFunc(func(_ context.Context, input InputInferencePlanInput) (*InferWorkInputsResult, error) {
		if len(input.Targets) != 1 || input.Targets[0].InputID != inputID {
			t.Fatalf("unexpected explicit targets: %#v", input.Targets)
		}
		return &InferWorkInputsResult{Items: []InferredWorkInput{{
			InputID: inputID, Value: json.RawMessage(`"替代建议"`), Reason: "基于最新目标",
		}}}, nil
	}))

	legacy, err := svc.InferWorkInputs(context.Background(), InferWorkInputsRequest{
		WorkID: workID, RunID: "run-1", DefinitionRevision: defRev,
	})
	if err != nil || len(legacy.Items) != 0 || len(legacy.Skipped) != 0 {
		t.Fatalf("legacy pending-only inference = %#v, %v", legacy, err)
	}

	result, err := svc.InferWorkInputs(context.Background(), InferWorkInputsRequest{
		WorkID: workID, RunID: "run-1", InputIDs: []string{inputID}, DefinitionRevision: defRev,
	})
	if err != nil || len(result.Items) != 1 || string(result.Items[0].Value) != `"替代建议"` {
		t.Fatalf("explicit completed suggestion = %#v, %v", result, err)
	}
	after, afterState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Revision != beforeState.Revision || len(after.V2Inputs) != len(before.V2Inputs) ||
		after.V2Inputs[0].Revision != before.V2Inputs[0].Revision ||
		string(after.V2Inputs[0].Value) != string(before.V2Inputs[0].Value) {
		t.Fatalf("replacement suggestion mutated state: before=%#v/%#v after=%#v/%#v", before.V2Inputs, beforeState, after.V2Inputs, afterState)
	}
}

func TestServiceInferWorkInputsSuggestsForExplicitCustomTextInput(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	workRev, _, defRev := inputGuards(t, store, workID, inputID)
	customID := "custom-location"
	added, err := inputSvc.AddCustomInput(context.Background(), AddCustomWorkInputRequest{
		WorkID: workID, RunID: "run-1", InputID: customID,
		Name: "地点", Kind: InputText, Value: json.RawMessage(`"北京朝阳区"`),
		DefinitionRevision: defRev, ExpectedRevision: workRev, RequestID: "add-custom-location",
	})
	if err != nil || added.Error != "" || added.Input == nil {
		t.Fatalf("AddCustomInput: result=%#v err=%v", added, err)
	}
	before, beforeState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetInputInferrer(inputInferrerFunc(func(_ context.Context, input InputInferencePlanInput) (*InferWorkInputsResult, error) {
		if len(input.Targets) != 1 || input.Targets[0].InputID != customID || input.Targets[0].Spec.Label != "地点" {
			t.Fatalf("unexpected custom target: %#v", input.Targets)
		}
		return &InferWorkInputsResult{Items: []InferredWorkInput{{
			InputID: customID, Value: json.RawMessage(`"北京奥林匹克森林公园"`), Reason: "适合团队活动",
		}}}, nil
	}))
	result, err := svc.InferWorkInputs(context.Background(), InferWorkInputsRequest{
		WorkID: workID, RunID: "run-1", InputIDs: []string{customID}, DefinitionRevision: defRev,
	})
	if err != nil || len(result.Items) != 1 || result.Items[0].InputID != customID {
		t.Fatalf("custom suggestion = %#v, %v", result, err)
	}
	after, afterState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Revision != beforeState.Revision || len(after.V2Inputs) != len(before.V2Inputs) {
		t.Fatalf("custom suggestion mutated state: before=%#v/%#v after=%#v/%#v", before.V2Inputs, beforeState, after.V2Inputs, afterState)
	}
	for _, input := range after.V2Inputs {
		if input.ID == customID && string(input.Value) != `"北京朝阳区"` {
			t.Fatalf("custom value changed without submit: %#v", input)
		}
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
