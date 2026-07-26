package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const missingV2ArtifactReason = "required artifact output is missing"

// reconcileV2Artifacts repairs the historical state produced before artifact
// completion was enforced. A completed producer without all declared outputs
// is invalidated, and its missing slots become explicit retryable failures.
func (c *V2Coordinator) reconcileV2Artifacts(
	ctx context.Context,
	workID, runID string,
	definition *WorkDefinitionRevision,
) (bool, error) {
	if definition == nil || strings.TrimSpace(runID) == "" {
		return false, nil
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return false, err
	}
	runtimes := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, runID)
	repaired := false
	var repairErr error
	for _, node := range definition.Nodes {
		if len(node.ProducesSlotIDs) == 0 {
			continue
		}
		runtime := runtimes[node.ID]
		if runtime == nil {
			continue
		}
		switch runtime.State {
		case TaskCompleted, TaskFailedRetryable, TaskFailedTerminal, TaskInvalidated:
		default:
			continue
		}
		var missing []string
		for _, slotID := range node.ProducesSlotIDs {
			slot, _ := FindArtifactSlotRevision(projection, definition.Revision, slotID)
			if v2ArtifactDelivered(slot) {
				continue
			}
			missing = append(missing, slotID)
			if err := c.failV2ArtifactSlot(
				ctx,
				workID,
				definition.Revision,
				runID,
				runtime.TaskID,
				slotID,
			); err != nil {
				repairErr = errors.Join(repairErr, err)
			} else {
				repaired = true
			}
		}
		if runtime.State == TaskCompleted && len(missing) > 0 {
			reason := missingV2ArtifactReason + ": " + strings.Join(missing, ", ")
			if err := c.invalidateV2CompletedTask(ctx, workID, runID, runtime.TaskID, definition.Revision, reason); err != nil {
				repairErr = errors.Join(repairErr, err)
			} else {
				repaired = true
			}
		}
	}
	return repaired, repairErr
}

func v2ArtifactDelivered(slot *ArtifactSlot) bool {
	return slot != nil &&
		slot.State == SlotReady &&
		slot.ExpectedCount > 0 &&
		usableArtifactRefCount(slot.ArtifactRefs) >= slot.ExpectedCount
}

func (c *V2Coordinator) failV2ArtifactSlot(
	ctx context.Context,
	workID string,
	definitionRev int64,
	runID, taskID, slotID string,
) error {
	requestID := fmt.Sprintf("%s/v2/artifact-missing/%s/%s", runID, taskID, slotID)
	for tries := 0; tries < 4; tries++ {
		projection, state, err := c.store.LoadState(workID, requestID)
		if err != nil {
			return err
		}
		if state.RequestFound {
			return nil
		}
		slot, _ := FindArtifactSlotRevision(projection, definitionRev, slotID)
		if slot == nil {
			return fmt.Errorf("work: reconcile V2 artifact: slot %q is unavailable", slotID)
		}
		if v2ArtifactDelivered(slot) || slot.State == SlotFailed || slot.State == SlotStale {
			return nil
		}
		result, err := (&Service{store: c.store}).UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
			WorkID:           workID,
			SlotID:           slotID,
			RequestID:        requestID,
			State:            SlotFailed,
			Refs:             append([]ArtifactRef(nil), slot.ArtifactRefs...),
			UpstreamDigest:   slot.UpstreamDigest,
			Summary:          slot.Summary,
			Error:            &ArtifactError{Code: "missing_output", Message: missingV2ArtifactReason, Retryable: true},
			Revision:         slot.Revision + 1,
			ExpectedRevision: state.Revision,
			DefinitionRev:    definitionRev,
		})
		if err == nil && result != nil {
			return nil
		}
		var conflict *ErrWorkEventConflict
		if !errors.As(err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
			return err
		}
	}
	return fmt.Errorf("work: reconcile V2 artifact slot %q exceeded conflict retries", slotID)
}

func (c *V2Coordinator) invalidateV2CompletedTask(
	ctx context.Context,
	workID, runID, taskID string,
	definitionRev int64,
	reason string,
) error {
	requestID := fmt.Sprintf("%s/v2/artifact-missing/%s/invalidate", runID, taskID)
	for tries := 0; tries < 4; tries++ {
		if err := checkServiceContext(ctx); err != nil {
			return err
		}
		projection, state, err := c.store.LoadState(workID, requestID)
		if err != nil {
			return err
		}
		if state.RequestFound {
			return nil
		}
		runtime := projection.V2TaskRuntimes[taskID]
		if runtime == nil || runtime.RunID != runID || runtime.State != TaskCompleted {
			return nil
		}
		payload, _ := json.Marshal(TaskInvalidatedPayload{
			TaskID: taskID,
			WorkID: workID,
			RunID:  runID,
			Reason: reason,
		})
		event := newServiceEventV2(workID, requestID, EventTaskInvalidated, payload, time.Now().UTC())
		event.BaseRevision, event.Revision = state.Revision, state.Revision+1
		event.Object = ObjectContext{
			Kind:               ObjectTask,
			ID:                 taskID,
			WorkID:             workID,
			RunID:              runID,
			TaskID:             taskID,
			DefinitionRevision: int64Ptr(definitionRev),
		}
		if _, err := c.store.CommitEvent(workID, event); err == nil {
			return nil
		} else {
			var conflict *ErrWorkEventConflict
			if !errors.As(err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
				return err
			}
		}
	}
	return fmt.Errorf("work: invalidate V2 task %q exceeded conflict retries", taskID)
}

func isMissingV2ArtifactRuntime(runtime *V2TaskRuntime) bool {
	return runtime != nil &&
		runtime.State == TaskInvalidated &&
		strings.HasPrefix(runtime.Error, missingV2ArtifactReason)
}

func (c *V2Coordinator) prepareV2NodeArtifacts(
	ctx context.Context,
	workID string,
	definitionRev int64,
	nodeID, requestID string,
) error {
	definition, err := c.loadDefinition(workID, definitionRev)
	if err != nil {
		return err
	}
	node := findNodeDef(definition.Nodes, nodeID)
	if node == nil {
		return fmt.Errorf("work: prepare V2 artifact retry: node %q not found", nodeID)
	}
	for _, slotID := range node.ProducesSlotIDs {
		slotRequestID := fmt.Sprintf("%s/artifact/%s/generating", requestID, slotID)
		for tries := 0; tries < 4; tries++ {
			projection, state, err := c.store.LoadState(workID, slotRequestID)
			if err != nil {
				return err
			}
			if state.RequestFound {
				break
			}
			slot, _ := FindArtifactSlotRevision(projection, definitionRev, slotID)
			if slot == nil {
				return fmt.Errorf("work: prepare V2 artifact retry: slot %q not found", slotID)
			}
			if slot.State == SlotReady || slot.State == SlotGenerating {
				break
			}
			_, err = (&Service{store: c.store}).UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
				WorkID:           workID,
				SlotID:           slotID,
				RequestID:        slotRequestID,
				State:            SlotGenerating,
				Refs:             append([]ArtifactRef(nil), slot.ArtifactRefs...),
				UpstreamDigest:   slot.UpstreamDigest,
				Summary:          slot.Summary,
				Revision:         slot.Revision + 1,
				ExpectedRevision: state.Revision,
				DefinitionRev:    definitionRev,
			})
			if err == nil {
				break
			}
			var conflict *ErrWorkEventConflict
			if !errors.As(err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
				return err
			}
			if tries == 3 {
				return fmt.Errorf("work: prepare V2 artifact slot %q exceeded conflict retries", slotID)
			}
		}
	}
	return nil
}

func (c *V2Coordinator) settleV2Run(
	ctx context.Context,
	workID, runID string,
	definition *WorkDefinitionRevision,
) error {
	if definition == nil || strings.TrimSpace(runID) == "" {
		return nil
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	target := desiredV2RunState(projection, runID, definition)
	run := findV2WorkflowRun(projection, runID)
	if run == nil {
		return fmt.Errorf("work: settle V2 run %q: run not found", runID)
	}
	if run.State == RunPending && target != RunPending {
		if err := c.commitV2RunState(ctx, workID, runID, definition, RunRunning); err != nil {
			return err
		}
	}
	return c.commitV2RunState(ctx, workID, runID, definition, target)
}

func desiredV2RunState(projection *Work, runID string, definition *WorkDefinitionRevision) RunState {
	if projection == nil || definition == nil || len(definition.Nodes) == 0 {
		return RunRunning
	}
	runtimes := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, runID)
	allCompleted := true
	hasWaiting := false
	for _, node := range definition.Nodes {
		runtime := runtimes[node.ID]
		if runtime == nil {
			allCompleted = false
			continue
		}
		switch runtime.State {
		case TaskCompleted:
		case TaskFailedRetryable, TaskFailedTerminal, TaskInvalidated, TaskCanceled:
			return RunFailed
		case TaskWaitingInput, TaskWaitingApproval:
			allCompleted = false
			hasWaiting = true
		default:
			allCompleted = false
		}
	}
	if allCompleted {
		for _, slot := range definition.ArtifactSlots {
			if !slot.Required {
				continue
			}
			current, _ := FindArtifactSlotRevision(projection, definition.Revision, slot.ID)
			if !v2ArtifactDelivered(current) {
				return RunFailed
			}
		}
		return RunCompleted
	}
	if hasWaiting {
		return RunWaiting
	}
	return RunRunning
}

func (c *V2Coordinator) commitV2RunState(
	ctx context.Context,
	workID, runID string,
	definition *WorkDefinitionRevision,
	target RunState,
) error {
	for tries := 0; tries < 4; tries++ {
		if err := checkServiceContext(ctx); err != nil {
			return err
		}
		projection, state, err := c.store.LoadState(workID, "")
		if err != nil {
			return err
		}
		current := findV2WorkflowRun(projection, runID)
		if current == nil {
			return fmt.Errorf("work: update V2 run %q: run not found", runID)
		}
		if current.State == RunCompleted || current.State == RunCancelled {
			return nil
		}
		if err := ValidateRunTransition(current.State, target); err != nil {
			return err
		}
		next := projectV2LegacyRun(*current, projection, definition, target)
		workState := workStateForRun(target)
		if reflect.DeepEqual(*current, next) && projection.State == workState {
			return nil
		}
		payload, err := json.Marshal(runEventPayload{Run: next, WorkState: workState})
		if err != nil {
			return err
		}
		requestID := fmt.Sprintf("%s/v2/aggregate/%s/%s", runID, target, payloadRequestPart(payload))
		event := newServiceEvent(workID, requestID, EventRunChanged, payload, time.Now().UTC())
		event.BaseRevision, event.Revision = state.Revision, state.Revision+1
		if _, err := c.store.CommitEvent(workID, event); err == nil {
			return nil
		} else {
			var conflict *ErrWorkEventConflict
			if !errors.As(err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
				return err
			}
		}
	}
	return fmt.Errorf("work: update V2 run %q exceeded conflict retries", runID)
}

func projectV2LegacyRun(
	current WorkflowRun,
	projection *Work,
	definition *WorkDefinitionRevision,
	target RunState,
) WorkflowRun {
	next := current
	next.State = target
	if target == RunRunning {
		next.FinishedAt = nil
	} else if IsTerminalRunState(target) && (current.State != target || current.FinishedAt == nil) {
		now := time.Now().UTC()
		next.FinishedAt = &now
	}
	runtimes := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, current.ID)
	stages := make([]Stage, 0, len(definition.Nodes))
	for _, node := range definition.Nodes {
		stage := Stage{ID: node.ID, Name: node.Title, State: RunPending, StartedAt: current.StartedAt}
		task := Task{ID: node.ID, Name: node.Title, State: RunPending}
		if runtime := runtimes[node.ID]; runtime != nil {
			state := legacyRunStateForV2Task(runtime.State)
			stage.State, task.State = state, state
			if IsTerminalRunState(state) {
				finished := runtime.UpdatedAt
				stage.FinishedAt = &finished
				task.FinishedAt = &finished
			}
		}
		stage.Tasks = []Task{task}
		stages = append(stages, stage)
	}
	next.Stages = stages
	return next
}

func legacyRunStateForV2Task(state TaskStateV2) RunState {
	switch state {
	case TaskCompleted:
		return RunCompleted
	case TaskWaitingInput, TaskWaitingApproval:
		return RunWaiting
	case TaskFailedRetryable, TaskFailedTerminal, TaskInvalidated, TaskCanceled:
		return RunFailed
	case TaskRunning:
		return RunRunning
	default:
		return RunPending
	}
}

func findV2WorkflowRun(projection *Work, runID string) *WorkflowRun {
	if projection == nil {
		return nil
	}
	for index := range projection.Runs {
		if projection.Runs[index].ID == runID {
			return &projection.Runs[index]
		}
	}
	return nil
}
