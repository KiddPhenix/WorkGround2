package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInputInferrerUnavailable is a retryable boot/configuration state.
	ErrInputInferrerUnavailable = errors.New("work: input inferrer is unavailable")
	// ErrInputInferenceFailed reports invalid or incomplete model output.
	ErrInputInferenceFailed = errors.New("work: input inference failed")
)

// InferWorkInputs proposes typed drafts for pending inputs from one coherent
// Work projection. It never commits values or changes runtime readiness.
func (s *Service) InferWorkInputs(ctx context.Context, req InferWorkInputsRequest) (*InferWorkInputsResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	req.WorkID = strings.TrimSpace(req.WorkID)
	req.RunID = strings.TrimSpace(req.RunID)
	if req.WorkID == "" || req.RunID == "" || req.DefinitionRevision <= 0 {
		return nil, errors.New("work: InferWorkInputs: workID/runID/definitionRevision are required")
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	planner := s.inputInferencePlanner()
	if planner == nil {
		return nil, ErrInputInferrerUnavailable
	}
	projection, _, err := s.store.LoadState(req.WorkID, "")
	if err != nil {
		return nil, err
	}
	if projection.V2CurrentRevision != req.DefinitionRevision {
		return nil, fmt.Errorf("work: InferWorkInputs: definition revision conflict: expected %d, current %d", req.DefinitionRevision, projection.V2CurrentRevision)
	}
	definition, err := s.definitionStore().LoadRevision(req.WorkID, projection.V2CurrentRevision)
	if err != nil {
		return nil, fmt.Errorf("work: InferWorkInputs: load definition: %w", err)
	}

	requested := make(map[string]struct{}, len(req.InputIDs))
	for _, id := range req.InputIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			requested[id] = struct{}{}
		}
	}
	specs := make(map[string]InputSpec, len(definition.InputSpecs))
	for _, spec := range definition.InputSpecs {
		specs[spec.ID] = spec
	}
	result := &InferWorkInputsResult{Items: []InferredWorkInput{}}
	targets := make([]InputInferenceTarget, 0)
	seen := make(map[string]struct{})
	for _, input := range projection.V2Inputs {
		if input.RunID != req.RunID || input.CustomSpec != nil ||
			(input.State != InputRequested && input.State != InputDraft && input.State != InputRejected) {
			continue
		}
		if len(requested) > 0 {
			if _, ok := requested[input.ID]; !ok {
				continue
			}
		}
		spec, ok := specs[input.SpecID]
		if !ok {
			return nil, fmt.Errorf("work: InferWorkInputs: input %q references unknown spec %q", input.ID, input.SpecID)
		}
		seen[input.ID] = struct{}{}
		switch spec.Kind {
		case InputFile:
			result.Skipped = append(result.Skipped, SkippedWorkInput{InputID: input.ID, Reason: "需要用户提供真实文件"})
		case InputApproval:
			result.Skipped = append(result.Skipped, SkippedWorkInput{InputID: input.ID, Reason: "确认或授权必须由用户决定"})
		default:
			targets = append(targets, InputInferenceTarget{InputID: input.ID, Spec: spec})
		}
	}
	for id := range requested {
		if _, ok := seen[id]; !ok {
			return nil, fmt.Errorf("work: InferWorkInputs: pending input %q not found in run %q", id, req.RunID)
		}
	}
	if len(targets) == 0 {
		return result, nil
	}
	contextInputs := make([]WorkInput, 0, len(projection.V2Inputs))
	for _, input := range projection.V2Inputs {
		if input.RunID == req.RunID || input.CustomSpec != nil {
			contextInputs = append(contextInputs, input)
		}
	}

	plan, err := planner.InferInputs(ctx, InputInferencePlanInput{
		Work: projection, Definition: definition, Inputs: contextInputs, Targets: targets,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInputInferenceFailed, err)
	}
	if plan == nil {
		return nil, fmt.Errorf("%w: model returned no result", ErrInputInferenceFailed)
	}
	targetByID := make(map[string]InputSpec, len(targets))
	for _, target := range targets {
		targetByID[target.InputID] = target.Spec
	}
	covered := make(map[string]struct{}, len(targets))
	for _, item := range plan.Items {
		spec, ok := targetByID[item.InputID]
		if !ok {
			return nil, fmt.Errorf("%w: unknown input %q", ErrInputInferenceFailed, item.InputID)
		}
		if _, duplicate := covered[item.InputID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate input %q", ErrInputInferenceFailed, item.InputID)
		}
		if err := ValidateInputValue(spec, item.Value); err != nil {
			return nil, fmt.Errorf("%w: input %q: %v", ErrInputInferenceFailed, item.InputID, err)
		}
		item.Reason = strings.TrimSpace(item.Reason)
		result.Items = append(result.Items, item)
		covered[item.InputID] = struct{}{}
	}
	for _, item := range plan.Skipped {
		if _, ok := targetByID[item.InputID]; !ok {
			return nil, fmt.Errorf("%w: unknown skipped input %q", ErrInputInferenceFailed, item.InputID)
		}
		if _, duplicate := covered[item.InputID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate input %q", ErrInputInferenceFailed, item.InputID)
		}
		item.Reason = strings.TrimSpace(item.Reason)
		if item.Reason == "" {
			item.Reason = "缺少可靠依据"
		}
		result.Skipped = append(result.Skipped, item)
		covered[item.InputID] = struct{}{}
	}
	for _, target := range targets {
		if _, ok := covered[target.InputID]; !ok {
			result.Skipped = append(result.Skipped, SkippedWorkInput{
				InputID: target.InputID, Reason: "模型未给出可验证的推断",
			})
		}
	}
	return result, nil
}
