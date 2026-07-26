package work

import (
	"sort"
)

// ── Ready-set evaluation ────────────────────────────────────────────────────
//
// EvaluateReadySet is a pure function: given the definition DAG and the current
// V2TaskRuntime states, it returns every node that is ready to execute.
//
// Rules:
//   - A node is ready when all its DependsOn are completed.
//   - waiting_input, waiting_approval, and failed states only block the
//     descendants of the blocked node — unrelated branches proceed.
//   - A node whose definition declares a GlobalGate blocks the entire
//     scheduling loop (no other node becomes ready) while the gate is active.
//   - Pending nodes with all dependencies satisfied are ready.
//   - Nodes that are already running, completed, canceled, or invalidated
//     are not returned.

// ReadySetResult is the output of EvaluateReadySet.
type ReadySetResult struct {
	// Ready lists node IDs whose dependencies are satisfied and that are in
	// a state that allows execution (pending, ready, failed_retryable).
	Ready []string
	// Blocked lists node IDs that are not ready because at least one
	// dependency is not completed.
	Blocked []string
	// Waiting lists node IDs that are in waiting_input or waiting_approval.
	Waiting []string
	// Terminal lists node IDs that are in a terminal non-retryable state
	// (completed, failed_terminal, canceled).
	Terminal []string
	// GlobalGate is set when a node with an active global gate is blocking
	// all scheduling. The value is the node ID of the gate holder.
	GlobalGate string
	// HasGlobalBlock is true when any global gate is active.
	HasGlobalBlock bool
}

// EvaluateReadySet computes which nodes are ready given the definition DAG
// and current runtime states.
//
// nodes: the NodeDef list from the active WorkDefinitionRevision.
// runtimes: map of nodeID → current V2TaskRuntime (may be nil for pending).
func EvaluateReadySet(nodes []NodeDef, runtimes map[string]*V2TaskRuntime) ReadySetResult {
	result := ReadySetResult{}
	if len(nodes) == 0 {
		return result
	}

	// Index nodes and compute completion status.
	completed := make(map[string]bool, len(nodes))
	stateMap := make(map[string]TaskStateV2, len(nodes))

	for _, n := range nodes {
		rt := runtimes[n.ID]
		if rt == nil {
			stateMap[n.ID] = TaskPending
		} else {
			stateMap[n.ID] = rt.State
		}
	}

	// First pass: classify every node.
	for _, n := range nodes {
		state := stateMap[n.ID]
		switch state {
		case TaskCompleted:
			completed[n.ID] = true
			result.Terminal = append(result.Terminal, n.ID)
		case TaskFailedTerminal, TaskCanceled:
			result.Terminal = append(result.Terminal, n.ID)
		case TaskWaitingInput, TaskWaitingApproval:
			result.Waiting = append(result.Waiting, n.ID)
		case TaskRunning:
			// Running is not ready, not blocked, not waiting.
		}
		// A dependent global gate becomes global only after its own
		// prerequisites are satisfied (ready) or after it has entered an
		// active/waiting state. Blocking earlier would deadlock its upstream.
		if !completed[n.ID] && n.GlobalGate != "" &&
			isGlobalGateActive(n, stateMap, runtimes) &&
			(result.GlobalGate == "" || n.ID < result.GlobalGate) {
			result.HasGlobalBlock = true
			result.GlobalGate = n.ID
		}
	}

	// Second pass: determine ready nodes.
	for _, n := range nodes {
		// An active global gate blocks every node except the gate itself. The
		// gate must remain schedulable or a pending approval gate would
		// deadlock the whole DAG.
		if result.HasGlobalBlock && n.ID != result.GlobalGate {
			if stateMap[n.ID] == TaskPending || stateMap[n.ID] == TaskReady ||
				stateMap[n.ID] == TaskFailedRetryable || stateMap[n.ID] == TaskInvalidated {
				result.Blocked = append(result.Blocked, n.ID)
			}
			continue
		}
		state := stateMap[n.ID]
		// Candidates: pending, ready, failed_retryable.
		switch state {
		case TaskPending, TaskReady, TaskFailedRetryable, TaskInvalidated:
			// eligible
		default:
			continue
		}

		// Check all dependencies.
		depsSatisfied := true
		hasBlockingDep := false
		for _, depID := range n.DependsOn {
			depState, exists := stateMap[depID]
			if !exists {
				if rt := runtimes[depID]; rt != nil {
					depState = rt.State
				} else {
					depState = TaskPending
				}
			}
			switch depState {
			case TaskCompleted:
				// OK — dependency is done.
			case TaskFailedTerminal:
				// A terminal failure blocks this node.
				depsSatisfied = false
				hasBlockingDep = true
			case TaskWaitingInput, TaskWaitingApproval, TaskFailedRetryable:
				// Not done yet, but retryable — blocks this node.
				depsSatisfied = false
				hasBlockingDep = true
			case TaskPending, TaskReady, TaskRunning:
				// Still running or not started — blocks this node.
				depsSatisfied = false
				hasBlockingDep = true
			case TaskCanceled:
				// Canceled is terminal but not a success — blocks.
				depsSatisfied = false
				hasBlockingDep = true
			case TaskInvalidated:
				// Invalidated is pending retry — blocks.
				depsSatisfied = false
				hasBlockingDep = true
			default:
				depsSatisfied = false
				hasBlockingDep = true
			}
		}

		if depsSatisfied {
			result.Ready = append(result.Ready, n.ID)
		} else if hasBlockingDep {
			result.Blocked = append(result.Blocked, n.ID)
		}
	}

	sort.Strings(result.Ready)
	sort.Strings(result.Blocked)
	sort.Strings(result.Waiting)
	sort.Strings(result.Terminal)
	return result
}

// EvaluateAffectedReadySet evaluates the complete DAG and then returns only
// the changed seeds and their descendants. Global gates are intentionally
// preserved because their scope is explicitly global.
func EvaluateAffectedReadySet(nodes []NodeDef, runtimes map[string]*V2TaskRuntime, seeds []string) ReadySetResult {
	result := EvaluateReadySet(nodes, runtimes)
	if len(seeds) == 0 {
		result.Ready = nil
		result.Blocked = nil
		result.Waiting = nil
		result.Terminal = nil
		result.GlobalGate = ""
		result.HasGlobalBlock = false
		return result
	}
	if result.HasGlobalBlock {
		return result
	}
	affected := make(map[string]bool)
	for _, id := range AffectedNodes(nodes, seeds) {
		affected[id] = true
	}
	result.Ready = filterNodeIDs(result.Ready, affected)
	result.Blocked = filterNodeIDs(result.Blocked, affected)
	result.Waiting = filterNodeIDs(result.Waiting, affected)
	result.Terminal = filterNodeIDs(result.Terminal, affected)
	return result
}

func filterNodeIDs(ids []string, allowed map[string]bool) []string {
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		if allowed[id] {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

// ── Affected subgraph ───────────────────────────────────────────────────────

// AffectedNodes returns all nodes that could be affected by a change to the
// given seed nodes. It follows DependsOn forward (downstream): if A depends on
// B, and B changed, then A is affected.
//
// This is used to incrementally recompute ready-set after an input, patch,
// or definition change without re-evaluating the entire DAG.
func AffectedNodes(nodes []NodeDef, seeds []string) []string {
	if len(seeds) == 0 {
		return nil
	}
	// Build reverse index: nodeID → downstream nodes (those that depend on it).
	downstream := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			downstream[dep] = append(downstream[dep], n.ID)
		}
	}
	affected := make(map[string]bool)
	queue := make([]string, 0, len(seeds))
	for _, s := range seeds {
		if !affected[s] {
			affected[s] = true
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, ds := range downstream[id] {
			if affected[ds] {
				continue
			}
			affected[ds] = true
			queue = append(queue, ds)
		}
	}
	result := make([]string, 0, len(affected))
	for id := range affected {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// ── DAG helpers ─────────────────────────────────────────────────────────────

// DirectDependenciesOf returns the direct DependsOn for a given node.
func DirectDependenciesOf(nodes []NodeDef, nodeID string) []string {
	for _, n := range nodes {
		if n.ID == nodeID {
			return append([]string(nil), n.DependsOn...)
		}
	}
	return nil
}

// DirectDependentsOf returns the node IDs that directly depend on the given node.
func DirectDependentsOf(nodes []NodeDef, nodeID string) []string {
	var result []string
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if dep == nodeID {
				result = append(result, n.ID)
			}
		}
	}
	sort.Strings(result)
	return result
}

// HasGlobalGate reports whether any node in the DAG has an active global gate
// that is not yet completed.
func HasGlobalGate(nodes []NodeDef, runtimes map[string]*V2TaskRuntime) (bool, string) {
	stateMap := make(map[string]TaskStateV2, len(nodes))
	for _, n := range nodes {
		if rt := runtimes[n.ID]; rt != nil {
			stateMap[n.ID] = rt.State
		} else {
			stateMap[n.ID] = TaskPending
		}
	}
	for _, n := range nodes {
		if n.GlobalGate == "" {
			continue
		}
		rt := runtimes[n.ID]
		if (rt == nil || rt.State != TaskCompleted) && isGlobalGateActive(n, stateMap, runtimes) {
			return true, n.ID
		}
	}
	return false, ""
}

func isGlobalGateActive(
	node NodeDef,
	states map[string]TaskStateV2,
	runtimes map[string]*V2TaskRuntime,
) bool {
	state := states[node.ID]
	switch state {
	case TaskRunning, TaskWaitingInput, TaskWaitingApproval, TaskReady,
		TaskFailedTerminal, TaskCanceled:
		return true
	case TaskCompleted:
		return false
	}
	for _, depID := range node.DependsOn {
		depState, ok := states[depID]
		if !ok {
			if rt := runtimes[depID]; rt != nil {
				depState = rt.State
			}
		}
		if depState != TaskCompleted {
			return false
		}
	}
	return true
}

// IsNodeTerminal reports whether a runtime state is terminal (won't change
// without explicit retry or invalidation).
func IsNodeTerminal(state TaskStateV2) bool {
	switch state {
	case TaskCompleted, TaskFailedTerminal, TaskCanceled:
		return true
	default:
		return false
	}
}

// IsNodeBlocking reports whether a node's state blocks its dependents.
// Completed is the only non-blocking terminal state.
func IsNodeBlocking(state TaskStateV2) bool {
	return state != TaskCompleted
}
