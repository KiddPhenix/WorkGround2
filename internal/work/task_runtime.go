package work

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// V2TaskRuntime is the durable execution record for one V2 task node within a
// single WorkflowRun. It is the authoritative source for scheduling decisions:
// ready-set evaluation reads only runtimes + the definition DAG. Every mutation
// is persisted as a WorkEvent before the in-memory projection is updated.
//
// TaskID is stable (DeriveTaskID(runID, nodeID)) and survives state transitions,
// retries, and restarts. The UI row identity never changes.
type V2TaskRuntime struct {
	TaskID           string      `json:"taskId"`
	WorkID           string      `json:"workId"`
	RunID            string      `json:"runId"`
	NodeID           string      `json:"nodeId"`
	DefinitionRev    int64       `json:"definitionRev"`
	State            TaskStateV2 `json:"state"`
	InputDigest      string      `json:"inputDigest,omitempty"`
	DependencyDigest string      `json:"dependencyDigest,omitempty"`
	ExecutionToken   string      `json:"executionToken,omitempty"`
	SideEffectClass  string      `json:"sideEffectClass,omitempty"`
	Attempts         []V2Attempt `json:"attempts,omitempty"`
	Progress         string      `json:"progress,omitempty"`
	SessionRef       *SessionRef `json:"sessionRef,omitempty"`
	Error            string      `json:"error,omitempty"`
	WaitingInputIDs  []string    `json:"waitingInputIds,omitempty"`
	ApprovalToken    string      `json:"approvalToken,omitempty"`
	Revision         int64       `json:"revision"`
	UpdatedAt        time.Time   `json:"updatedAt" ts_type:"string"`
}

// V2Attempt records one execution try of a V2 task node. External/destructive
// attempts without a matching receipt enter waiting_approval; they are never
// replayed automatically after restart.
type V2Attempt struct {
	ID               string          `json:"id"`
	RequestID        string          `json:"requestId,omitempty"`
	Index            int             `json:"index"`
	State            TaskStateV2     `json:"state"`
	StartedAt        time.Time       `json:"startedAt" ts_type:"string"`
	FinishedAt       *time.Time      `json:"finishedAt,omitempty" ts_type:"string"`
	DefinitionRev    int64           `json:"definitionRev"`
	InputDigest      string          `json:"inputDigest,omitempty"`
	DependencyDigest string          `json:"dependencyDigest,omitempty"`
	ExecutionToken   string          `json:"executionToken,omitempty"`
	SideEffectClass  string          `json:"sideEffectClass,omitempty"`
	Error            string          `json:"error,omitempty"`
	Receipt          *AttemptReceipt `json:"receipt,omitempty"`
	ResultRef        string          `json:"resultRef,omitempty"`
	StaleResult      bool            `json:"staleResult,omitempty"`
}

// ComputeInputDigest returns a deterministic digest of submitted WorkInput
// values owned by exactly one Work/Run/Task. Inputs with the same SpecID from
// another run or task are distinct objects and must never affect this digest.
func ComputeInputDigest(
	inputs []WorkInput,
	workID, runID, taskID string,
	specIDs []string,
) string {
	if len(specIDs) == 0 {
		return "inputs:none"
	}
	type inputEntry struct {
		InputID string          `json:"i"`
		SpecID  string          `json:"s"`
		Value   json.RawMessage `json:"v"`
		Rev     int64           `json:"r"`
	}
	entries := make([]inputEntry, 0, len(specIDs))
	specSet := make(map[string]bool, len(specIDs))
	for _, id := range specIDs {
		specSet[id] = true
	}
	for _, in := range inputs {
		if in.WorkID != workID || in.RunID != runID || in.TaskID != taskID ||
			!specSet[in.SpecID] {
			continue
		}
		if in.State != InputSubmitted && in.State != InputAccepted {
			continue
		}
		entries = append(entries, inputEntry{
			InputID: in.ID,
			SpecID:  in.SpecID,
			Value:   in.Value,
			Rev:     in.Revision,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SpecID != entries[j].SpecID {
			return entries[i].SpecID < entries[j].SpecID
		}
		return entries[i].InputID < entries[j].InputID
	})
	raw, _ := json.Marshal(entries)
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", digest)
}

// HasAllRequiredInputs returns true when every required InputSpec for this
// exact Work/Run/Task has a submitted or accepted WorkInput.
func HasAllRequiredInputs(
	inputs []WorkInput,
	specs []InputSpec,
	workID, runID, taskID string,
	specIDs []string,
) (bool, []string) {
	specMap := make(map[string]InputSpec, len(specs))
	for _, s := range specs {
		specMap[s.ID] = s
	}
	submitted := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		if in.WorkID != workID || in.RunID != runID || in.TaskID != taskID {
			continue
		}
		spec, ok := specMap[in.SpecID]
		if ok && inputSatisfiesSpec(in, spec) {
			submitted[in.SpecID] = true
		}
	}
	var missing []string
	for _, id := range specIDs {
		spec, ok := specMap[id]
		if !ok || !spec.Required {
			continue
		}
		if !submitted[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return len(missing) == 0, missing
}

func inputSatisfiesSpec(input WorkInput, spec InputSpec) bool {
	if input.State != InputSubmitted && input.State != InputAccepted {
		return false
	}
	if spec.Kind != InputApproval {
		return true
	}
	var decision string
	if err := json.Unmarshal(input.Value, &decision); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(decision), "approved")
}

// ComputeDependencyDigest returns a deterministic digest of all upstream
// dependency states. It includes the state, completion digest, and
// revision of each dependency.
func ComputeDependencyDigest(runtimes map[string]*V2TaskRuntime, depNodeIDs []string) string {
	if len(depNodeIDs) == 0 {
		return "deps:none"
	}
	type depEntry struct {
		NodeID string      `json:"n"`
		State  TaskStateV2 `json:"s"`
		Rev    int64       `json:"r"`
	}
	entries := make([]depEntry, 0, len(depNodeIDs))
	for _, nid := range depNodeIDs {
		rt, ok := runtimes[nid]
		if !ok {
			entries = append(entries, depEntry{NodeID: nid, State: TaskPending, Rev: 0})
			continue
		}
		entries = append(entries, depEntry{NodeID: nid, State: rt.State, Rev: rt.Revision})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].NodeID < entries[j].NodeID })
	raw, _ := json.Marshal(entries)
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", digest)
}

// GenerateExecutionToken produces a unique token for single-flight execution
// guarding. It binds the task identity, definition revision, input digest, and
// dependency digest together so any change produces a new token.
func GenerateExecutionToken(taskID string, definitionRev int64, inputDigest, dependencyDigest string) string {
	raw := fmt.Sprintf("%s\x00%d\x00%s\x00%s", taskID, definitionRev, inputDigest, dependencyDigest)
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("sha256:%x", digest[:20])
}

// ValidateStaleCompletion checks whether a completion result is still valid
// given the current execution token, definition revision, and digests. A result
// is stale when the token or digests have changed since the attempt started.
func ValidateStaleCompletion(attempt *V2Attempt, current DefTokenSet) bool {
	if attempt == nil {
		return true
	}
	return attempt.DefinitionRev != current.DefinitionRev ||
		attempt.InputDigest != current.InputDigest ||
		attempt.DependencyDigest != current.DependencyDigest ||
		attempt.ExecutionToken != current.ExecutionToken
}

// DefTokenSet is the set of tokens/digests that define a task's expected
// execution context at a point in time.
type DefTokenSet struct {
	DefinitionRev    int64  `json:"definitionRev"`
	InputDigest      string `json:"inputDigest"`
	DependencyDigest string `json:"dependencyDigest"`
	ExecutionToken   string `json:"executionToken"`
}

// V2ReceiptRequired reports whether the given side-effect class requires
// a human-confirmed receipt before the attempt outcome is accepted.
func V2ReceiptRequired(class string) bool {
	switch strings.TrimSpace(class) {
	case "external_write", "destructive":
		return true
	default:
		return false
	}
}

// DeriveV2SideEffectClass infers the side-effect class from a node's tool hints.
// The highest-risk class wins (read < workspace_write < external_write < destructive).
func DeriveV2SideEffectClass(toolHints []string) string {
	class, maxRank := "read", 1
	for _, hint := range toolHints {
		hint = strings.TrimSpace(hint)
		// Tool hints may carry a side-effect annotation: "tool:side_effect=external_write"
		if idx := strings.Index(hint, "side_effect="); idx >= 0 {
			candidate := hint[idx+len("side_effect="):]
			if end := strings.IndexAny(candidate, ",; \t"); end >= 0 {
				candidate = candidate[:end]
			}
			if v2SideEffectRank(candidate) > maxRank {
				class, maxRank = candidate, v2SideEffectRank(candidate)
			}
		}
	}
	return class
}

// UpgradeV2SideEffectClass freezes a task's declared risk floor. Runtime
// observations may upgrade that risk but can never lower it and bypass receipt
// handling.
func UpgradeV2SideEffectClass(declared, observed string) string {
	declared = strings.TrimSpace(declared)
	if v2SideEffectRank(declared) == 0 {
		declared = "read"
	}
	observed = strings.TrimSpace(observed)
	if v2SideEffectRank(observed) > v2SideEffectRank(declared) {
		return observed
	}
	return declared
}

func v2SideEffectRank(class string) int {
	switch strings.TrimSpace(class) {
	case "read":
		return 1
	case "workspace_write":
		return 2
	case "external_write":
		return 3
	case "destructive":
		return 4
	default:
		return 0
	}
}

// V2NewTaskRuntime creates a fresh V2TaskRuntime for a node.
func V2NewTaskRuntime(workID, runID, nodeID string, definitionRev int64, sideEffectClass string, now time.Time) *V2TaskRuntime {
	taskID, _ := DeriveTaskID(runID, nodeID)
	return &V2TaskRuntime{
		TaskID:          taskID,
		WorkID:          workID,
		RunID:           runID,
		NodeID:          nodeID,
		DefinitionRev:   definitionRev,
		State:           TaskPending,
		SideEffectClass: sideEffectClass,
		Revision:        1,
		UpdatedAt:       now,
	}
}

// V2RunAttemptID generates a stable, deterministic attempt ID.
func V2RunAttemptID(taskID string, index int) string {
	return fmt.Sprintf("%s-attempt-%d", taskID, index)
}
