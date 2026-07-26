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

func resolveDiscussionBlock(
	current *Work,
	def *WorkDefinitionRevision,
	runID, taskID, blockID string,
	now time.Time,
) (*WorkflowRun, *Task, *BlockInstance, string, bool, error) {
	run, task, err := resolvePatchRunTask(current, runID, taskID)
	if err != nil {
		return nil, nil, nil, "", false, err
	}
	nodeID := task.ID
	if runtime := current.V2TaskRuntimes[taskID]; runtime != nil && runtime.RunID == runID {
		nodeID = runtime.NodeID
	}
	var node *NodeDef
	for i := range def.Nodes {
		if def.Nodes[i].ID == nodeID {
			node = &def.Nodes[i]
			break
		}
	}
	if node == nil {
		return nil, nil, nil, "", false, fmt.Errorf(
			"task %q does not resolve to a node in definition revision %d",
			taskID,
			def.Revision,
		)
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
