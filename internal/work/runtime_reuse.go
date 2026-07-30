package work

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// projectKeptContexts deterministically carries compatible submitted input data
// and completed evidence into a new definition run. Inputs receive new
// run/task/input identities. Stale, failed, context-mismatched, missing-result,
// or dependency-incompatible contexts are left pending and execute normally.
func projectKeptContexts(
	current *Work,
	parent, next *WorkDefinitionRevision,
	newRunID string,
	impact *RunImpact,
	now time.Time,
) ([]WorkInput, []*V2TaskRuntime) {
	if current == nil || parent == nil || next == nil || impact == nil ||
		strings.TrimSpace(newRunID) == "" {
		return nil, nil
	}
	if current.V2CurrentRevision != parent.Revision {
		return nil, nil
	}
	oldRunID := latestRunIDForDigest(current, parent.Digest)
	if oldRunID == "" {
		return nil, nil
	}
	kept := make(map[string]bool, len(impact.KeptNodeIDs))
	for _, nodeID := range impact.KeptNodeIDs {
		kept[nodeID] = true
	}
	oldByNode := normalizeV2RuntimesForRun(current.V2TaskRuntimes, oldRunID)
	nextByNode := make(map[string]*V2TaskRuntime)
	nodes := append([]NodeDef(nil), next.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	projectedInputs := make([]WorkInput, 0)
	projectedRuntimes := make([]*V2TaskRuntime, 0, len(kept))
	remaining := len(nodes)
	for remaining > 0 {
		progress := false
		for index := range nodes {
			node := nodes[index]
			if node.ID == "" {
				continue
			}
			nodes[index].ID = ""
			remaining--
			if !kept[node.ID] {
				progress = true
				continue
			}
			dependenciesReady := true
			for _, dependency := range node.DependsOn {
				if !kept[dependency] || nextByNode[dependency] == nil {
					dependenciesReady = false
					break
				}
			}
			if !dependenciesReady {
				nodes[index].ID = node.ID
				remaining++
				continue
			}
			runtime, inputs := projectKeptRuntime(
				current,
				parent,
				next,
				oldRunID,
				newRunID,
				node,
				oldByNode,
				nextByNode,
				now,
			)
			if runtime != nil {
				nextByNode[node.ID] = runtime
				projectedInputs = append(projectedInputs, inputs...)
				projectedRuntimes = append(projectedRuntimes, runtime)
			}
			progress = true
		}
		if !progress {
			break
		}
	}
	// User-owned values belong to the Work, not to one immutable Definition
	// revision. Carry every unambiguous value whose stable spec ID and schema
	// still accept it, even when its node must rerun or moved in the new DAG.
	// Completed runtime reuse stays stricter and is handled above.
	carried := projectCompatibleInputs(current, parent, next, oldRunID, newRunID, impact, now)
	existing := make(map[string]bool, len(projectedInputs))
	for _, input := range projectedInputs {
		existing[input.TaskID+"\x00"+input.SpecID] = true
	}
	for _, input := range carried {
		key := input.TaskID + "\x00" + input.SpecID
		if existing[key] {
			continue
		}
		existing[key] = true
		projectedInputs = append(projectedInputs, input)
	}
	sort.Slice(projectedInputs, func(i, j int) bool {
		if projectedInputs[i].TaskID != projectedInputs[j].TaskID {
			return projectedInputs[i].TaskID < projectedInputs[j].TaskID
		}
		return projectedInputs[i].SpecID < projectedInputs[j].SpecID
	})
	sort.Slice(projectedRuntimes, func(i, j int) bool {
		return projectedRuntimes[i].NodeID < projectedRuntimes[j].NodeID
	})
	return projectedInputs, projectedRuntimes
}

func projectCompatibleInputs(
	current *Work,
	parent, next *WorkDefinitionRevision,
	oldRunID, newRunID string,
	impact *RunImpact,
	now time.Time,
) []WorkInput {
	if current == nil || parent == nil || next == nil || impact == nil {
		return nil
	}
	parentSpecs := indexSpecs(parent.InputSpecs)
	nextSpecs := indexSpecs(next.InputSpecs)
	kept := make(map[string]bool, len(impact.KeptNodeIDs))
	for _, nodeID := range impact.KeptNodeIDs {
		kept[nodeID] = true
	}

	// A spec may be bound to more than one old task. Reuse it only when every
	// submitted value agrees; conflicting user facts require a fresh question.
	bySpec := make(map[string][]WorkInput)
	for _, input := range current.V2Inputs {
		if input.WorkID != current.ID || input.RunID != oldRunID ||
			(input.State != InputSubmitted && input.State != InputAccepted) {
			continue
		}
		bySpec[input.SpecID] = append(bySpec[input.SpecID], input)
	}

	nodes := append([]NodeDef(nil), next.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	projected := make([]WorkInput, 0)
	for _, node := range nodes {
		taskID, err := DeriveTaskID(newRunID, node.ID)
		if err != nil {
			continue
		}
		specIDs := append([]string(nil), node.InputSpecIDs...)
		sort.Strings(specIDs)
		for _, specID := range specIDs {
			oldSpec, oldOK := parentSpecs[specID]
			newSpec, newOK := nextSpecs[specID]
			if !oldOK || !newOK || oldSpec.Kind != newSpec.Kind {
				continue
			}
			// Approval is a decision about a concrete execution context. It may
			// cross revisions only when the owning node is semantically kept.
			if newSpec.Kind == InputApproval && !kept[node.ID] {
				continue
			}
			source, ok := unambiguousInput(bySpec[specID])
			if !ok || ValidateInputValue(newSpec, source.Value) != nil {
				continue
			}
			inputID, _ := v2InputIdentity(newRunID, taskID, specID)
			revision := int64(1)
			if source.CornerstoneID != "" {
				revision++
			}
			projected = append(projected, WorkInput{
				ID:            inputID,
				WorkID:        current.ID,
				RunID:         newRunID,
				TaskID:        taskID,
				BlockID:       v2InputBlockID(node),
				SpecID:        specID,
				Value:         append(json.RawMessage(nil), source.Value...),
				State:         InputSubmitted,
				CornerstoneID: source.CornerstoneID,
				Source:        source.Source,
				UpdatedBy:     source.UpdatedBy,
				Revision:      revision,
				UpdatedAt:     now,
			})
		}
	}
	return projected
}

func unambiguousInput(candidates []WorkInput) (WorkInput, bool) {
	if len(candidates) == 0 {
		return WorkInput{}, false
	}
	latest := candidates[0]
	for _, candidate := range candidates[1:] {
		if !rawMessageEq(latest.Value, candidate.Value) {
			return WorkInput{}, false
		}
		if candidate.Revision > latest.Revision ||
			(candidate.Revision == latest.Revision && candidate.UpdatedAt.After(latest.UpdatedAt)) ||
			(candidate.Revision == latest.Revision && candidate.UpdatedAt.Equal(latest.UpdatedAt) &&
				candidate.ID > latest.ID) {
			latest = candidate
		}
	}
	return latest, true
}

func projectKeptRuntime(
	current *Work,
	parent, next *WorkDefinitionRevision,
	oldRunID, newRunID string,
	node NodeDef,
	oldByNode, nextByNode map[string]*V2TaskRuntime,
	now time.Time,
) (*V2TaskRuntime, []WorkInput) {
	old := oldByNode[node.ID]
	if old == nil || old.State != TaskCompleted || old.RunID != oldRunID ||
		old.WorkID != current.ID || old.DefinitionRev != parent.Revision ||
		strings.TrimSpace(old.Error) != "" {
		return nil, nil
	}
	attempt := latestReusableAttempt(old)
	if attempt == nil {
		return nil, nil
	}
	oldInputDigest := ComputeInputDigest(
		current.V2Inputs, current.ID, oldRunID, old.TaskID, node.InputSpecIDs,
	)
	if old.InputDigest != oldInputDigest {
		return nil, nil
	}
	oldDependencyDigest := ComputeDependencyDigest(oldByNode, node.DependsOn)
	if old.DependencyDigest != oldDependencyDigest {
		return nil, nil
	}
	if ValidateStaleCompletion(attempt, DefTokenSet{
		DefinitionRev:    old.DefinitionRev,
		InputDigest:      old.InputDigest,
		DependencyDigest: old.DependencyDigest,
		ExecutionToken:   old.ExecutionToken,
	}) {
		return nil, nil
	}
	newTaskID, err := DeriveTaskID(newRunID, node.ID)
	if err != nil {
		return nil, nil
	}
	inputs := projectKeptInputs(current, old, node, newRunID, newTaskID, now)
	if complete, _ := HasAllRequiredInputs(
		inputs,
		next.InputSpecs,
		current.ID,
		newRunID,
		newTaskID,
		node.InputSpecIDs,
	); !complete {
		return nil, nil
	}
	newInputDigest := ComputeInputDigest(
		inputs, current.ID, newRunID, newTaskID, node.InputSpecIDs,
	)
	newDependencyDigest := ComputeDependencyDigest(nextByNode, node.DependsOn)
	token := GenerateExecutionToken(newTaskID, next.Revision, newInputDigest, newDependencyDigest)
	reusedAttempt := *attempt
	reusedAttempt.ID = V2RunAttemptID(newTaskID, 0)
	reusedAttempt.Index = 0
	reusedAttempt.DefinitionRev = next.Revision
	reusedAttempt.InputDigest = newInputDigest
	reusedAttempt.DependencyDigest = newDependencyDigest
	reusedAttempt.ExecutionToken = token
	reusedAttempt.StaleResult = false

	runtime := V2NewTaskRuntime(
		current.ID,
		newRunID,
		node.ID,
		next.Revision,
		old.SideEffectClass,
		now,
	)
	runtime.State = TaskCompleted
	runtime.InputDigest = newInputDigest
	runtime.DependencyDigest = newDependencyDigest
	runtime.ExecutionToken = token
	runtime.Attempts = []V2Attempt{reusedAttempt}
	runtime.Revision = 1
	runtime.UpdatedAt = now
	return runtime, inputs
}

func projectKeptInputs(
	current *Work,
	oldRuntime *V2TaskRuntime,
	node NodeDef,
	newRunID, newTaskID string,
	now time.Time,
) []WorkInput {
	if current == nil || oldRuntime == nil || len(node.InputSpecIDs) == 0 {
		return nil
	}
	specs := make(map[string]bool, len(node.InputSpecIDs))
	for _, specID := range node.InputSpecIDs {
		if specID = strings.TrimSpace(specID); specID != "" {
			specs[specID] = true
		}
	}
	latest := make(map[string]WorkInput, len(specs))
	for _, input := range current.V2Inputs {
		if input.WorkID != current.ID || input.RunID != oldRuntime.RunID ||
			input.TaskID != oldRuntime.TaskID || !specs[input.SpecID] ||
			(input.State != InputSubmitted && input.State != InputAccepted) {
			continue
		}
		previous, found := latest[input.SpecID]
		if found && (previous.Revision > input.Revision ||
			(previous.Revision == input.Revision && previous.ID <= input.ID)) {
			continue
		}
		latest[input.SpecID] = input
	}
	specIDs := make([]string, 0, len(latest))
	for specID := range latest {
		specIDs = append(specIDs, specID)
	}
	sort.Strings(specIDs)
	projected := make([]WorkInput, 0, len(specIDs))
	for _, specID := range specIDs {
		source := latest[specID]
		inputID, _ := v2InputIdentity(newRunID, newTaskID, specID)
		revision := int64(1)
		if source.CornerstoneID != "" {
			revision++
		}
		projected = append(projected, WorkInput{
			ID:            inputID,
			WorkID:        current.ID,
			RunID:         newRunID,
			TaskID:        newTaskID,
			BlockID:       source.BlockID,
			SpecID:        specID,
			Value:         append(json.RawMessage(nil), source.Value...),
			State:         InputSubmitted,
			CornerstoneID: source.CornerstoneID,
			Source:        source.Source,
			UpdatedBy:     source.UpdatedBy,
			Revision:      revision,
			UpdatedAt:     now,
		})
	}
	return projected
}

func latestReusableAttempt(runtime *V2TaskRuntime) *V2Attempt {
	if runtime == nil || len(runtime.Attempts) == 0 {
		return nil
	}
	attempt := &runtime.Attempts[len(runtime.Attempts)-1]
	if attempt.State != TaskCompleted || attempt.StaleResult ||
		strings.TrimSpace(attempt.ResultRef) == "" {
		return nil
	}
	effectiveClass := UpgradeV2SideEffectClass(runtime.SideEffectClass, attempt.SideEffectClass)
	if V2ReceiptRequired(effectiveClass) && !reusableReceipt(attempt, effectiveClass) {
		return nil
	}
	return attempt
}

func reusableReceipt(attempt *V2Attempt, sideEffectClass string) bool {
	if attempt == nil || attempt.Receipt == nil {
		return false
	}
	receipt := attempt.Receipt
	requestID := strings.TrimSpace(attempt.RequestID)
	if requestID == "" || strings.TrimSpace(receipt.RequestID) != requestID ||
		receipt.ConfirmedAt.IsZero() ||
		strings.TrimSpace(receipt.SideEffectClass) != strings.TrimSpace(sideEffectClass) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(receipt.Outcome)) {
	case "succeeded", "accept":
		return true
	default:
		return false
	}
}

func latestRunIDForDigest(current *Work, digest string) string {
	if current == nil || strings.TrimSpace(digest) == "" {
		return ""
	}
	for index := len(current.Runs) - 1; index >= 0; index-- {
		if current.Runs[index].DefinitionDigest == digest {
			return current.Runs[index].ID
		}
	}
	return ""
}

func buildKeptContextEvents(
	current *Work,
	parent, next *WorkDefinitionRevision,
	newRunID string,
	impact *RunImpact,
	now time.Time,
) ([]WorkEvent, error) {
	inputs, runtimes := projectKeptContexts(current, parent, next, newRunID, impact, now)
	events := make([]WorkEvent, 0, len(inputs)*2+len(runtimes))
	for _, input := range inputs {
		inputEvents, err := buildKeptInputEvents(input, next.Revision, now)
		if err != nil {
			return nil, err
		}
		events = append(events, inputEvents...)
	}
	for _, runtime := range runtimes {
		_, event, err := newRuntimeCreatedEvent(runtime, now)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func buildKeptInputEvents(input WorkInput, definitionRev int64, now time.Time) ([]WorkEvent, error) {
	requestPayload, err := json.Marshal(InputRequestedPayload{
		InputID: input.ID,
		WorkID:  input.WorkID,
		RunID:   input.RunID,
		TaskID:  input.TaskID,
		BlockID: input.BlockID,
		SpecID:  input.SpecID,
	})
	if err != nil {
		return nil, err
	}
	request := newServiceEventV2(
		input.WorkID,
		input.RunID+"/reuse/input/"+input.ID+"/request",
		EventInputRequested,
		requestPayload,
		now,
	)
	request.Object = ObjectContext{
		Kind: ObjectInput, ID: input.ID, WorkID: input.WorkID,
		RunID: input.RunID, TaskID: input.TaskID, BlockID: input.BlockID,
		InputID: input.ID, SpecID: input.SpecID,
		DefinitionRevision: int64Ptr(definitionRev),
	}

	submitPayload, err := json.Marshal(InputSubmittedPayload{
		InputID:          input.ID,
		WorkID:           input.WorkID,
		RunID:            input.RunID,
		TaskID:           input.TaskID,
		BlockID:          input.BlockID,
		SpecID:           input.SpecID,
		Value:            append(json.RawMessage(nil), input.Value...),
		Source:           input.Source,
		UpdatedBy:        input.UpdatedBy,
		Revision:         1,
		ExpectedRevision: 0,
	})
	if err != nil {
		return nil, err
	}
	submit := newServiceEventV2(
		input.WorkID,
		input.RunID+"/reuse/input/"+input.ID+"/submit",
		EventInputSubmitted,
		submitPayload,
		now,
	)
	submit.Object = ObjectContext{
		Kind: ObjectInput, ID: input.ID, WorkID: input.WorkID,
		RunID: input.RunID, TaskID: input.TaskID, BlockID: input.BlockID,
		InputID: input.ID, SpecID: input.SpecID,
		ExpectedRevision: int64Ptr(0), DefinitionRevision: int64Ptr(definitionRev),
	}
	events := []WorkEvent{request, submit}
	if input.CornerstoneID == "" {
		return events, nil
	}
	cornerstonePayload, err := json.Marshal(InputCornerstoneChangedPayload{
		InputID:          input.ID,
		WorkID:           input.WorkID,
		RunID:            input.RunID,
		TaskID:           input.TaskID,
		BlockID:          input.BlockID,
		SpecID:           input.SpecID,
		CornerstoneID:    input.CornerstoneID,
		Pinned:           true,
		Revision:         2,
		ExpectedRevision: 1,
	})
	if err != nil {
		return nil, err
	}
	cornerstone := newServiceEventV2(
		input.WorkID,
		input.RunID+"/reuse/input/"+input.ID+"/cornerstone",
		EventInputCornerstoneChanged,
		cornerstonePayload,
		now,
	)
	cornerstone.Object = ObjectContext{
		Kind: ObjectInput, ID: input.ID, WorkID: input.WorkID,
		RunID: input.RunID, TaskID: input.TaskID, BlockID: input.BlockID,
		InputID: input.ID, SpecID: input.SpecID,
		ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(definitionRev),
	}
	return append(events, cornerstone), nil
}
