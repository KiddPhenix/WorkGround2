package work

import (
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersionV2 is the V2 Work schema version. Work carries this version
// when it contains V2 definition revisions, artifact slots, typed inputs, or
// V2 discussion patches. V1 (SchemaVersion=1) history is immutable.
const SchemaVersionV2 = 2

// ── V2 definition model ────────────────────────────────────────────────────

// DefinitionStatus tracks the lifecycle of a WorkDefinitionRevision.
type DefinitionStatus string

const (
	DefDraft      DefinitionStatus = "draft"
	DefActive     DefinitionStatus = "active"
	DefSuperseded DefinitionStatus = "superseded"
)

// WorkDefinitionRevision is a versioned, immutable work definition produced
// via the conversation-based planning flow. Each revision is copy-on-write;
// the active revision atomically switches.
type WorkDefinitionRevision struct {
	WorkID         string            `json:"workId"`
	Revision       int64             `json:"revision"`
	ParentRevision int64             `json:"parentRevision"`
	Status         DefinitionStatus  `json:"status"`
	Goal           string            `json:"goal"`
	Nodes          []NodeDef         `json:"nodes"`
	ArtifactSlots  []ArtifactSlotDef `json:"artifactSlots"`
	InputSpecs     []InputSpec       `json:"inputSpecs"`
	CreatedBy      string            `json:"createdBy"`
	CreatedAt      time.Time         `json:"createdAt" ts_type:"string"`
	Digest         string            `json:"digest"`
}

// NodeDef is one node in a V2 definition DAG. Each node declares its inputs,
// dependencies, and expected artefact outputs.
type NodeDef struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Description     string   `json:"description,omitempty"`
	DependsOn       []string `json:"dependsOn,omitempty"`
	InputSpecIDs    []string `json:"inputSpecIds,omitempty"`
	ToolHints       []string `json:"toolHints,omitempty"`
	BlockIDs        []string `json:"blockIds,omitempty"`
	ProducesSlotIDs []string `json:"producesSlotIds,omitempty"`
	ConsumesSlotIDs []string `json:"consumesSlotIds,omitempty"`
	// AcceptanceCriteria lists concrete, observable delivery conditions that
	// must be satisfied before this node can be marked completed. Each criterion
	// should name a specific deliverable, observable outcome, or evidence
	// requirement — never vague phrases like "complete the task" or "ensure quality".
	// Empty remains valid only for backward compatibility with old definitions.
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	// GlobalGate, when non-empty, declares this node as a global scheduling gate.
	// While the node is not completed, no other node in the DAG may become ready.
	// Only used for genuinely global risks such as a release approval or final
	// delivery sign-off. Empty means local-only blocking (default).
	GlobalGate string `json:"globalGate,omitempty"`
}

// ArtifactSlotDef declares an expected output slot in a definition.
type ArtifactSlotDef struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	ExpectedCount int    `json:"expectedCount"`
	Required      bool   `json:"required"`
}

// InputSpec defines a typed input gate expected by one or more nodes.
type InputSpec struct {
	ID           string          `json:"id"`
	Label        string          `json:"label"`
	Description  string          `json:"description,omitempty"`
	Kind         InputKind       `json:"kind"`
	Required     bool            `json:"required"`
	ValueSchema  json.RawMessage `json:"valueSchema,omitempty"`
	DefaultValue json.RawMessage `json:"defaultValue,omitempty"`
	PinEligible  bool            `json:"pinEligible"`
}

// InputKind classifies what the user must provide.
type InputKind string

const (
	InputText        InputKind = "text"
	InputNumber      InputKind = "number"
	InputDate        InputKind = "date"
	InputChoice      InputKind = "choice"
	InputMultiChoice InputKind = "multi_choice"
	InputFile        InputKind = "file"
	InputRoster      InputKind = "roster"
	InputForm        InputKind = "form"
	InputApproval    InputKind = "approval"
)

// ── V2 artifact slots ──────────────────────────────────────────────────────

// ArtifactSlotState tracks the lifecycle of a declared output slot.
type ArtifactSlotState string

const (
	SlotReserved   ArtifactSlotState = "reserved"
	SlotGenerating ArtifactSlotState = "generating"
	SlotReady      ArtifactSlotState = "ready"
	SlotPartial    ArtifactSlotState = "partial"
	SlotFailed     ArtifactSlotState = "failed"
	SlotStale      ArtifactSlotState = "stale"
)

// ArtifactSlot is the runtime projection of a declared output. It is distinct
// from ArtifactRef (the actual file). A slot declares intent; the refs are the
// materialised outputs.
type ArtifactSlot struct {
	ID             string            `json:"id"`
	WorkID         string            `json:"workId"`
	DefinitionRev  int64             `json:"definitionRev"`
	UpstreamDigest string            `json:"upstreamDigest,omitempty"`
	Title          string            `json:"title"`
	Kind           string            `json:"kind"`
	ExpectedCount  int               `json:"expectedCount"`
	Required       bool              `json:"required"`
	State          ArtifactSlotState `json:"state"`
	ArtifactRefs   []ArtifactRef     `json:"artifactRefs"`
	Progress       *float64          `json:"progress,omitempty"`
	Summary        string            `json:"summary,omitempty"`
	Error          *ArtifactError    `json:"error,omitempty"`
	Revision       int64             `json:"revision"`
}

// ArtifactError captures a non-recoverable slot failure context.
type ArtifactError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// ── V2 typed inputs ────────────────────────────────────────────────────────

// InputState tracks the lifecycle of a WorkInput.
type InputState string

const (
	InputRequested InputState = "requested"
	InputDraft     InputState = "draft"
	InputSubmitted InputState = "submitted"
	InputRejected  InputState = "rejected"
	InputAccepted  InputState = "accepted"
)

// WorkInput is the typed, revision-tracked user input bound to a task and
// block. ID is stable across revisions. Value is validated against the
// corresponding InputSpec before submission.
type WorkInput struct {
	ID            string          `json:"id"`
	WorkID        string          `json:"workId"`
	RunID         string          `json:"runId"`
	TaskID        string          `json:"taskId"`
	BlockID       string          `json:"blockId"`
	SpecID        string          `json:"specId"`
	CustomSpec    *InputSpec      `json:"customSpec,omitempty"`
	Value         json.RawMessage `json:"value"`
	Extra         string          `json:"extra,omitempty"`
	State         InputState      `json:"state"`
	CornerstoneID string          `json:"cornerstoneId,omitempty"`
	Error         string          `json:"error,omitempty"`
	Source        string          `json:"source,omitempty"`
	UpdatedBy     string          `json:"updatedBy,omitempty"`
	ReadyForStart bool            `json:"readyForStart,omitempty"`
	Revision      int64           `json:"revision"`
	UpdatedAt     time.Time       `json:"updatedAt" ts_type:"string"`
}

// ── V2 discussion patches ──────────────────────────────────────────────────

// PatchScope defines how far a WorkPatchPreview's changes reach.
type PatchScope string

const (
	PatchBlock    PatchScope = "block"
	PatchWorkflow PatchScope = "workflow"
)

// PatchOp describes a single structured change within a WorkPatchPreview.
type PatchOp struct {
	Op       string          `json:"op"`
	Path     string          `json:"path"`
	OldValue json.RawMessage `json:"oldValue,omitempty"`
	NewValue json.RawMessage `json:"newValue,omitempty"`
}

// PatchActionKind is the semantic execution decision made by PatchPlanner.
// PatchService validates the decision against the authoritative definition and
// runtime state; V2Coordinator performs it idempotently.
type PatchActionKind string

const (
	PatchActionReuse    PatchActionKind = "reuse"
	PatchActionReformat PatchActionKind = "reformat"
	PatchActionRerun    PatchActionKind = "rerun"
	PatchActionAskUser  PatchActionKind = "ask_user"
)

// PatchAction separates semantic impact from JSON mutation paths. NodeID owns
// reuse/rerun decisions; ArtifactSlotID owns reformat decisions. Ask-user
// carries a concrete question and applies no mutation until the user replies.
type PatchAction struct {
	Action         PatchActionKind `json:"action"`
	NodeID         string          `json:"nodeId,omitempty"`
	ArtifactSlotID string          `json:"artifactSlotId,omitempty"`
	Question       string          `json:"question,omitempty"`
	Reason         string          `json:"reason,omitempty"`
}

// WorkPatchPreview is a read-only preview generated from a discussion message.
// The patch_previewed event persists only structured operations, impact summary,
// and references — it never saves the raw discussion text. The event does not
// mutate Work state; only a separate apply_patch event (with requestID and
// expectedRevision) applies the change.
type WorkPatchPreview struct {
	ID                      string        `json:"id"`
	WorkID                  string        `json:"workId"`
	RunID                   string        `json:"runId"`
	TaskID                  string        `json:"taskId"`
	BlockID                 string        `json:"blockId"`
	SessionID               string        `json:"sessionId"`
	BaseDefinitionRev       int64         `json:"baseDefinitionRev"`
	BaseBlockRev            int64         `json:"baseBlockRev"`
	Scope                   PatchScope    `json:"scope"`
	Operations              []PatchOp     `json:"operations"`
	Actions                 []PatchAction `json:"actions,omitempty"`
	AffectedNodeIDs         []string      `json:"affectedNodeIds"`
	AffectedBlockIDs        []string      `json:"affectedBlockIds"`
	AffectedArtifactSlotIDs []string      `json:"affectedArtifactSlotIds"`
	StaleArtifactSlotIDs    []string      `json:"staleArtifactSlotIds"`
	InvalidatedTaskIDs      []string      `json:"invalidatedTaskIds"`
	RequiresRerun           bool          `json:"requiresRerun"`
	Digest                  string        `json:"digest"`
	ExpiresAt               time.Time     `json:"expiresAt" ts_type:"string"`
}

// ── V2 Task states ─────────────────────────────────────────────────────────

// TaskStateV2 extends V1 RunState with V2-specific waiting and invalidation
// states. V1 states continue to apply to the underlying RunState field.
type TaskStateV2 string

const (
	TaskPending         TaskStateV2 = "pending"
	TaskReady           TaskStateV2 = "ready"
	TaskRunning         TaskStateV2 = "running"
	TaskWaitingInput    TaskStateV2 = "waiting_input"
	TaskWaitingApproval TaskStateV2 = "waiting_approval"
	TaskCompleted       TaskStateV2 = "completed"
	TaskFailedRetryable TaskStateV2 = "failed_retryable"
	TaskFailedTerminal  TaskStateV2 = "failed_terminal"
	TaskCanceled        TaskStateV2 = "canceled"
	TaskInvalidated     TaskStateV2 = "invalidated"
)

// maxV2AutomaticRecoveryAttempts bounds retries caused only by process
// restart. Explicit user retries remain available after this limit.
const maxV2AutomaticRecoveryAttempts = 3

// ── V2 Controller contract (frozen interface, no implementation) ───────────

// SubmitWorkInputRequest is the contract DTO for submitting a typed input.
// requestID guarantees idempotent submission; expectedRevision prevents lost
// updates.
type SubmitWorkInputRequest struct {
	WorkID             string          `json:"workId"`
	RunID              string          `json:"runId"`
	TaskID             string          `json:"taskId"`
	BlockID            string          `json:"blockId"`
	InputID            string          `json:"inputId"`
	Value              json.RawMessage `json:"value"`
	Extra              string          `json:"extra,omitempty"`
	DeferStart         bool            `json:"deferStart,omitempty"`
	DefinitionRevision int64           `json:"definitionRevision"`
	InputRevision      int64           `json:"inputRevision"`
	ExpectedRevision   int64           `json:"expectedRevision"`
	RequestID          string          `json:"requestId"`
}

// AddCustomWorkInputRequest adds user-owned Work information without mutating
// the immutable active definition. Kind is intentionally limited to text/file.
type AddCustomWorkInputRequest struct {
	WorkID             string          `json:"workId"`
	RunID              string          `json:"runId"`
	InputID            string          `json:"inputId"`
	Name               string          `json:"name"`
	Description        string          `json:"description,omitempty"`
	Kind               InputKind       `json:"kind"`
	Value              json.RawMessage `json:"value"`
	DefinitionRevision int64           `json:"definitionRevision"`
	ExpectedRevision   int64           `json:"expectedRevision"`
	RequestID          string          `json:"requestId"`
}

// InferWorkInputsRequest asks the configured model to propose draft values for
// selected inputs. When InputIDs is omitted, only pending inputs are targeted;
// explicit IDs may also request a replacement suggestion for an existing value.
// It is read-only: callers must review and submit returned values separately.
type InferWorkInputsRequest struct {
	WorkID             string   `json:"workId"`
	RunID              string   `json:"runId"`
	InputIDs           []string `json:"inputIds,omitempty"`
	DefinitionRevision int64    `json:"definitionRevision"`
}

// InferredWorkInput is one model-proposed draft value.
type InferredWorkInput struct {
	InputID string          `json:"inputId"`
	Value   json.RawMessage `json:"value"`
	Reason  string          `json:"reason,omitempty"`
}

// SkippedWorkInput explains why an input was not safe to infer.
type SkippedWorkInput struct {
	InputID string `json:"inputId"`
	Reason  string `json:"reason"`
}

// InferWorkInputsResult contains uncommitted drafts and explicit skips.
type InferWorkInputsResult struct {
	Items   []InferredWorkInput `json:"items"`
	Skipped []SkippedWorkInput  `json:"skipped,omitempty"`
}

// InputSubmissionResult reports the outcome of a SubmitWorkInput call.
type InputSubmissionResult struct {
	Input     *WorkInput `json:"input"`
	Revision  int64      `json:"revision"`
	Duplicate bool       `json:"duplicate"`
	Error     string     `json:"error,omitempty"`
}

// SetInputCornerstoneRequest pins or unpins a submitted input as a Cornerstone.
type SetInputCornerstoneRequest struct {
	WorkID             string `json:"workId"`
	InputID            string `json:"inputId"`
	Pin                bool   `json:"pin"`
	DefinitionRevision int64  `json:"definitionRevision"`
	InputRevision      int64  `json:"inputRevision"`
	ExpectedRevision   int64  `json:"expectedRevision"`
	RequestID          string `json:"requestId"`
}

// CornerstonePinResult reports the outcome of a pin/unpin operation.
type CornerstonePinResult struct {
	CornerstoneID  string              `json:"cornerstoneId,omitempty"`
	Receipt        *InputIntentReceipt `json:"receipt,omitempty"`
	Pinned         bool                `json:"pinned"`
	Revision       int64               `json:"revision"`
	Duplicate      bool                `json:"duplicate"`
	Error          string              `json:"error,omitempty"`
	Committed      bool                `json:"committed"`
	Recoverable    bool                `json:"recoverable"`
	TransportError *WorkTransportError `json:"transportError,omitempty"`
}

// PreviewWorkPatchRequest triggers generation of a structured patch preview
// from a discussion message. This is read-only — no state is mutated.
type PreviewWorkPatchRequest struct {
	WorkID             string     `json:"workId"`
	RunID              string     `json:"runId"`
	TaskID             string     `json:"taskId"`
	BlockID            string     `json:"blockId"`
	SessionID          string     `json:"sessionId"`
	Instruction        string     `json:"instruction"`
	DefinitionRevision int64      `json:"definitionRevision"`
	BlockRevision      int64      `json:"blockRevision"`
	Scope              PatchScope `json:"scope"`
	RequestID          string     `json:"requestId"`
}

// ApplyWorkPatchRequest applies a previously previewed patch. requestID makes
// repeated applies idempotent; expectedRevision rejects stale mutations.
type ApplyWorkPatchRequest struct {
	WorkID           string     `json:"workId"`
	PatchID          string     `json:"patchId"`
	PreviewDigest    string     `json:"previewDigest"`
	Scope            PatchScope `json:"scope"`
	ExpectedRevision int64      `json:"expectedRevision"`
	RequestID        string     `json:"requestId"`
}

// BeginWorkPlanningInput starts the conversation-based definition flow.
type BeginWorkPlanningInput struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	Locale    string `json:"locale,omitempty"`
}

// ApplyDefinitionInput atomically activates a new definition revision.
type ApplyDefinitionInput struct {
	WorkID           string `json:"workId"`
	Revision         int64  `json:"revision"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
	// deferWake is package-private so ordinary Controller/front-end callers
	// cannot accidentally commit a run without waking it. RunReusableFlow uses
	// the split phase to bind the durable Work identity before the DAG runs;
	// restart scheduling recovery closes the commit-to-wake crash window.
	deferWake bool
}

// RetryWorkNodeRequest retries a failed or invalidated task node.
type RetryWorkNodeRequest struct {
	WorkID           string `json:"workId"`
	RunID            string `json:"runId"`
	TaskID           string `json:"taskId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// SetNodeSkillRequest binds an optional Skill to a Work node for execution
// context augmentation. requestID makes repeated sets idempotent.
type SetNodeSkillRequest struct {
	WorkID           string `json:"workId"`
	NodeID           string `json:"nodeId"`
	SkillName        string `json:"skillName"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// ClearNodeSkillRequest removes a Skill binding from a Work node.
// requestID makes repeated clears idempotent.
type ClearNodeSkillRequest struct {
	WorkID           string `json:"workId"`
	NodeID           string `json:"nodeId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// ── V2 WorkView extension ──────────────────────────────────────────────────

// WorkViewV2 extends the V1 WorkView with V2-specific execution context.
// It is returned by V2-capable projections when the Work schema version ≥ 2.
type WorkViewV2 struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Work          *Work                   `json:"work"`
	Revision      int64                   `json:"revision"`
	Definition    *WorkDefinitionRevision `json:"definition,omitempty"`
	ArtifactSlots []ArtifactSlot          `json:"artifactSlots,omitempty"`
	Tasks         []TaskV2View            `json:"tasks,omitempty"`
	Inputs        []WorkInput             `json:"inputs,omitempty"`
	PatchPreviews []WorkPatchPreview      `json:"patchPreviews,omitempty"`
}

// TaskV2View is the stable execution-list row for a V2 task. Identity is
// stable across status changes so the UI row persists without remounting.
type TaskV2View struct {
	ID              string      `json:"id"`
	RunID           string      `json:"runId"`
	NodeID          string      `json:"nodeId"`
	Title           string      `json:"title"`
	State           TaskStateV2 `json:"state"`
	Progress        string      `json:"progress,omitempty"`
	SessionRef      *SessionRef `json:"sessionRef,omitempty"`
	WaitingInputIDs []string    `json:"waitingInputIds,omitempty"`
	SkillName       string      `json:"skillName,omitempty"`
	Error           string      `json:"error,omitempty"`
	Retryable       bool        `json:"retryable"`
	UpdatedAt       time.Time   `json:"updatedAt" ts_type:"string"`
}

// ── V2 state transition validators (production code) ───────────────────────

// validArtifactSlotTransitions is the canonical set of allowed transitions.
var validArtifactSlotTransitions = map[ArtifactSlotState]map[ArtifactSlotState]bool{
	SlotReserved:   {SlotGenerating: true, SlotReady: true, SlotPartial: true, SlotFailed: true, SlotStale: true},
	SlotGenerating: {SlotReady: true, SlotPartial: true, SlotFailed: true, SlotStale: true},
	SlotReady:      {SlotStale: true, SlotGenerating: true, SlotPartial: true, SlotReserved: true},
	SlotPartial:    {SlotGenerating: true, SlotReady: true, SlotFailed: true, SlotStale: true},
	SlotFailed:     {SlotGenerating: true, SlotReserved: true, SlotStale: true},
	SlotStale:      {SlotGenerating: true},
}

// ValidateArtifactSlotTransition returns nil if from→to is legal.
func ValidateArtifactSlotTransition(from, to ArtifactSlotState) error {
	if from == to {
		return nil
	}
	dests, ok := validArtifactSlotTransitions[from]
	if !ok || !dests[to] {
		return fmt.Errorf("work: invalid ArtifactSlotState transition %s → %s", from, to)
	}
	return nil
}

// validInputTransitions is the canonical set of allowed transitions.
var validInputTransitions = map[InputState]map[InputState]bool{
	InputRequested: {InputDraft: true, InputSubmitted: true, InputRejected: true},
	InputDraft:     {InputSubmitted: true, InputRejected: true},
	InputSubmitted: {InputDraft: true, InputAccepted: true, InputRejected: true},
	InputRejected:  {InputDraft: true, InputSubmitted: true},
	InputAccepted:  {InputDraft: true, InputSubmitted: true},
}

// ValidateInputTransition returns nil if from→to is legal.
func ValidateInputTransition(from, to InputState) error {
	if from == to {
		return nil
	}
	dests, ok := validInputTransitions[from]
	if !ok || !dests[to] {
		return fmt.Errorf("work: invalid InputState transition %s → %s", from, to)
	}
	return nil
}

// validTaskV2Transitions is the canonical set of allowed transitions.
var validTaskV2Transitions = map[TaskStateV2]map[TaskStateV2]bool{
	TaskPending:         {TaskReady: true, TaskWaitingInput: true, TaskWaitingApproval: true, TaskCanceled: true},
	TaskReady:           {TaskRunning: true, TaskWaitingInput: true, TaskWaitingApproval: true, TaskCanceled: true},
	TaskRunning:         {TaskCompleted: true, TaskFailedRetryable: true, TaskFailedTerminal: true, TaskWaitingInput: true, TaskWaitingApproval: true, TaskCanceled: true},
	TaskWaitingInput:    {TaskReady: true, TaskCanceled: true, TaskFailedTerminal: true},
	TaskWaitingApproval: {TaskReady: true, TaskCanceled: true, TaskFailedTerminal: true},
	TaskCompleted:       {TaskInvalidated: true},
	TaskFailedRetryable: {TaskReady: true, TaskWaitingInput: true, TaskWaitingApproval: true, TaskCanceled: true},
	TaskFailedTerminal:  {},
	TaskCanceled:        {TaskReady: true},
	TaskInvalidated:     {TaskReady: true, TaskWaitingInput: true, TaskWaitingApproval: true, TaskCanceled: true},
}

// ValidateTaskV2Transition returns nil if from→to is legal.
func ValidateTaskV2Transition(from, to TaskStateV2) error {
	if from == to {
		return nil
	}
	dests, ok := validTaskV2Transitions[from]
	if !ok || !dests[to] {
		return fmt.Errorf("work: invalid TaskStateV2 transition %s → %s", from, to)
	}
	return nil
}

// ── Task ID derivation ─────────────────────────────────────────────────────

// DeriveTaskID produces a stable, collision-resistant identifier for a task
// from its run and node IDs. Empty runID or nodeID returns an error. The ID
// is deterministic and survives state transitions, retries, and restarts.
func DeriveTaskID(runID, nodeID string) (string, error) {
	if runID == "" {
		return "", fmt.Errorf("work: DeriveTaskID: runID must be non-empty")
	}
	if nodeID == "" {
		return "", fmt.Errorf("work: DeriveTaskID: nodeID must be non-empty")
	}
	// Use length-prefixed join to avoid "ab:cd"/"a:bcd" collision.
	return fmt.Sprintf("%d:%s/%d:%s", len(runID), runID, len(nodeID), nodeID), nil
}

// ── Migration decision ─────────────────────────────────────────────────────

// MigrationDecision encodes the outcome of a schema migration check.
type MigrationDecision int

const (
	// MigrateCurrent — schema is within binary capability; no migration needed.
	MigrateCurrent MigrationDecision = iota
	// MigrateV1ToV2 — V1-to-V2 migration is allowed (copy-on-write, retryable).
	MigrateV1ToV2
	// MigrateFutureReadOnly — schema version exceeds binary capability;
	// read-only access only, no writes or migration.
	MigrateFutureReadOnly
	// MigrateInvalid — schema version is zero, negative, or otherwise invalid.
	MigrateInvalid
)

// DecideV2Migration determines the migration action for a given work schema
// version. V1→V2 is a retryable copy-on-write; future versions are read-only.
func DecideV2Migration(schemaVer int) MigrationDecision {
	switch {
	case schemaVer <= 0:
		return MigrateInvalid
	case schemaVer == SchemaVersion:
		return MigrateV1ToV2
	case schemaVer == SchemaVersionV2:
		return MigrateCurrent
	case schemaVer > SchemaVersionV2:
		return MigrateFutureReadOnly
	default:
		return MigrateInvalid
	}
}
