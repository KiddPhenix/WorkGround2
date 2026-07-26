package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// materializeV2WaitingInputs turns a scheduler input gate into authoritative
// WorkInput records. The scheduler owns whether a task is waiting; InputService
// owns the editable/submittable input lifecycle consumed by every frontend.
func (c *V2Coordinator) materializeV2WaitingInputs(
	ctx context.Context,
	workID, runID string,
	definition *WorkDefinitionRevision,
) error {
	if c == nil || c.inputs == nil || definition == nil || strings.TrimSpace(runID) == "" {
		return nil
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	runtimes := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, runID)
	var materializeErr error
	for _, node := range definition.Nodes {
		runtime := runtimes[node.ID]
		if runtime == nil || runtime.State != TaskWaitingInput {
			continue
		}
		ok, missing := HasAllRequiredInputs(
			projection.V2Inputs,
			definition.InputSpecs,
			workID,
			runID,
			runtime.TaskID,
			node.InputSpecIDs,
		)
		if ok {
			continue
		}
		blockID := v2InputBlockID(node)
		for _, specID := range missing {
			input, requestErr := c.requestV2Input(
				ctx,
				workID,
				runID,
				runtime.TaskID,
				blockID,
				specID,
				definition.Revision,
			)
			if requestErr != nil {
				materializeErr = errors.Join(materializeErr, requestErr)
				continue
			}
			if input != nil {
				projection.V2Inputs = append(projection.V2Inputs, *input)
			}
		}
	}
	return materializeErr
}

func (c *V2Coordinator) requestV2Input(
	ctx context.Context,
	workID, runID, taskID, blockID, specID string,
	definitionRev int64,
) (*WorkInput, error) {
	inputID, requestID := v2InputIdentity(runID, taskID, specID)
	for tries := 0; tries < 4; tries++ {
		projection, state, err := c.store.LoadState(workID, "")
		if err != nil {
			return nil, err
		}
		if existing := findV2TaskInput(projection.V2Inputs, runID, taskID, specID); existing != nil {
			return existing, nil
		}
		input, err := c.inputs.RequestInput(ctx, RequestInputRequest{
			WorkID:           workID,
			RunID:            runID,
			TaskID:           taskID,
			BlockID:          blockID,
			InputID:          inputID,
			SpecID:           specID,
			DefinitionRev:    definitionRev,
			ExpectedRevision: state.Revision,
			RequestID:        requestID,
		})
		if err == nil {
			return input, nil
		}
		var conflict *ErrWorkEventConflict
		if !errors.As(err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
			return nil, err
		}
	}
	return nil, fmt.Errorf("work: materialize V2 input %q exceeded conflict retries", specID)
}

func v2InputBlockID(node NodeDef) string {
	for _, blockID := range node.BlockIDs {
		if blockID = strings.TrimSpace(blockID); blockID != "" {
			return blockID
		}
	}
	return V2DiscussionBlockID(node.ID)
}

func v2InputIdentity(runID, taskID, specID string) (inputID, requestID string) {
	identity := fmt.Sprintf(
		"%d:%s/%d:%s/%d:%s",
		len(runID), runID,
		len(taskID), taskID,
		len(specID), specID,
	)
	digest := strings.TrimPrefix(ContentDigest([]byte(identity)), "sha256:")
	short := digest[:24]
	return "v2-input-" + short, runID + "/v2/input/" + short
}

func findV2TaskInput(inputs []WorkInput, runID, taskID, specID string) *WorkInput {
	var found *WorkInput
	for i := range inputs {
		input := &inputs[i]
		if input.RunID != runID || input.TaskID != taskID || input.SpecID != specID {
			continue
		}
		if found == nil || input.Revision > found.Revision {
			copy := *input
			found = &copy
		}
	}
	return found
}
