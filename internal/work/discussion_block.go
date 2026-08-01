package work

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const v2DiscussionBlockPrefix = "v2-node-"

// V2DiscussionBlockID returns the stable runtime Block identity owned by a V2
// definition node when the definition does not declare a concrete block.
func V2DiscussionBlockID(nodeID string) string {
	return v2DiscussionBlockPrefix + hex.EncodeToString([]byte(nodeID))
}

func resolveDiscussionTask(
	current *Work,
	def *WorkDefinitionRevision,
	runID, taskID string,
) (*WorkflowRun, *Task, *NodeDef, error) {
	run, task, resolveErr := resolvePatchRunTask(current, runID, taskID)
	if resolveErr == nil {
		nodeID := task.ID
		if runtime := current.V2TaskRuntimes[taskID]; runtime != nil && runtime.RunID == runID {
			nodeID = runtime.NodeID
		}
		for i := range def.Nodes {
			if def.Nodes[i].ID == nodeID {
				return run, task, &def.Nodes[i], nil
			}
		}
		return nil, nil, nil, fmt.Errorf(
			"task %q does not resolve to a node in definition revision %d",
			taskID,
			def.Revision,
		)
	}

	// The complete DAG is visible before every runtime is materialized. Accept
	// only the exact stable ID of a node in this run's authoritative definition.
	var pendingRun *WorkflowRun
	for i := range current.Runs {
		if current.Runs[i].ID == runID {
			runCopy := current.Runs[i]
			pendingRun = &runCopy
			break
		}
	}
	if pendingRun == nil {
		return nil, nil, nil, resolveErr
	}
	for i := range def.Nodes {
		candidateID, err := DeriveTaskID(runID, def.Nodes[i].ID)
		if err == nil && candidateID == taskID {
			return pendingRun, &Task{
				ID: taskID, Name: def.Nodes[i].ID, State: RunPending,
			}, &def.Nodes[i], nil
		}
	}
	return nil, nil, nil, fmt.Errorf(
		"task %q does not match any node in definition revision %d for run %q",
		taskID, def.Revision, runID,
	)
}

func resolveDiscussionBlock(
	current *Work,
	def *WorkDefinitionRevision,
	runID, taskID, blockID string,
	now time.Time,
) (*WorkflowRun, *Task, *BlockInstance, string, bool, error) {
	run, task, node, err := resolveDiscussionTask(current, def, runID, taskID)
	if err != nil {
		return nil, nil, nil, "", false, err
	}
	if !discussionBlockBound(current, runID, taskID, node, blockID) {
		return nil, nil, nil, "", false, fmt.Errorf(
			"block %q is not bound to task %q node %q",
			blockID,
			taskID,
			node.ID,
		)
	}
	for i := range current.Blocks {
		if current.Blocks[i].ID != blockID {
			continue
		}
		if current.Blocks[i].Tombstone {
			return nil, nil, nil, "", false, fmt.Errorf("block %q was removed", blockID)
		}
		block := cloneDiscussionBlock(current.Blocks[i])
		return run, task, &block, node.ID, false, nil
	}
	content := strings.TrimSpace(node.Description)
	if content == "" {
		content = node.Title
	}
	data, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return nil, nil, nil, "", false, fmt.Errorf("encode discussion block: %w", err)
	}
	block := &BlockInstance{
		ID:            blockID,
		Kind:          "markdown",
		SchemaVersion: 1,
		Revision:      1,
		Title:         node.Title,
		Status:        BlockReady,
		Data:          data,
		Source:        BlockSource{Provider: "controller", Mode: "snapshot", Verified: true},
		Fallback:      BlockFallback{Summary: content},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return run, task, block, node.ID, true, nil
}

func discussionBlockBound(current *Work, runID, taskID string, node *NodeDef, blockID string) bool {
	if node == nil || blockID == "" {
		return false
	}
	if containsID(node.BlockIDs, blockID) {
		return true
	}
	hasInputBlock := false
	for _, input := range current.V2Inputs {
		if input.RunID != runID || input.TaskID != taskID {
			continue
		}
		hasInputBlock = true
		if input.BlockID == blockID {
			return true
		}
	}
	return len(node.BlockIDs) == 0 && !hasInputBlock && blockID == V2DiscussionBlockID(node.ID)
}

func cloneDiscussionBlock(block BlockInstance) BlockInstance {
	block.Data = append(json.RawMessage(nil), block.Data...)
	block.Actions = append([]BlockActionSpec(nil), block.Actions...)
	return block
}
