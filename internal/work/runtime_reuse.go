package work

import (
	"sort"
	"strings"
	"time"
)

// projectKeptRuntimes deterministically carries compatible completed evidence
// into a new definition run. It is deliberately conservative: stale, failed,
// context-mismatched, missing-result, or dependency-incompatible runtimes are
// left pending and will execute normally in the new run.
func projectKeptRuntimes(
	current *Work,
	parent, next *WorkDefinitionRevision,
	newRunID string,
	impact *RunImpact,
	now time.Time,
) []*V2TaskRuntime {
	if current == nil || parent == nil || next == nil || impact == nil ||
		strings.TrimSpace(newRunID) == "" || len(impact.KeptNodeIDs) == 0 {
		return nil
	}
	if current.V2CurrentRevision != parent.Revision {
		return nil
	}
	oldRunID := latestRunIDForDigest(current, parent.Digest)
	if oldRunID == "" {
		return nil
	}
	kept := make(map[string]bool, len(impact.KeptNodeIDs))
	for _, nodeID := range impact.KeptNodeIDs {
		kept[nodeID] = true
	}
	oldByNode := normalizeV2RuntimesForRun(current.V2TaskRuntimes, oldRunID)
	nextByNode := make(map[string]*V2TaskRuntime)
	nodes := append([]NodeDef(nil), next.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	projected := make([]*V2TaskRuntime, 0, len(kept))
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
			runtime := projectKeptRuntime(
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
				projected = append(projected, runtime)
			}
			progress = true
		}
		if !progress {
			break
		}
	}
	sort.Slice(projected, func(i, j int) bool {
		return projected[i].NodeID < projected[j].NodeID
	})
	return projected
}

func projectKeptRuntime(
	current *Work,
	parent, next *WorkDefinitionRevision,
	oldRunID, newRunID string,
	node NodeDef,
	oldByNode, nextByNode map[string]*V2TaskRuntime,
	now time.Time,
) *V2TaskRuntime {
	old := oldByNode[node.ID]
	if old == nil || old.State != TaskCompleted || old.RunID != oldRunID ||
		old.WorkID != current.ID || old.DefinitionRev != parent.Revision ||
		strings.TrimSpace(old.Error) != "" {
		return nil
	}
	attempt := latestReusableAttempt(old)
	if attempt == nil {
		return nil
	}
	oldInputDigest := ComputeInputDigest(
		current.V2Inputs, current.ID, oldRunID, old.TaskID, node.InputSpecIDs,
	)
	if old.InputDigest != oldInputDigest {
		return nil
	}
	oldDependencyDigest := ComputeDependencyDigest(oldByNode, node.DependsOn)
	if old.DependencyDigest != oldDependencyDigest {
		return nil
	}
	if ValidateStaleCompletion(attempt, DefTokenSet{
		DefinitionRev:    old.DefinitionRev,
		InputDigest:      old.InputDigest,
		DependencyDigest: old.DependencyDigest,
		ExecutionToken:   old.ExecutionToken,
	}) {
		return nil
	}
	newTaskID, err := DeriveTaskID(newRunID, node.ID)
	if err != nil {
		return nil
	}
	newInputDigest := ComputeInputDigest(
		current.V2Inputs, current.ID, newRunID, newTaskID, node.InputSpecIDs,
	)
	if newInputDigest != oldInputDigest {
		return nil
	}
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
	return runtime
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

func buildKeptRuntimeEvents(
	current *Work,
	parent, next *WorkDefinitionRevision,
	newRunID string,
	impact *RunImpact,
	now time.Time,
) ([]WorkEvent, error) {
	runtimes := projectKeptRuntimes(current, parent, next, newRunID, impact, now)
	events := make([]WorkEvent, 0, len(runtimes))
	for _, runtime := range runtimes {
		_, event, err := newRuntimeCreatedEvent(runtime, now)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
