package work

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the V1 Work schema version. When a serialised Work,
// WorkRecord or WorkEvent carries a higher version the current binary must
// refuse writes and offer read-only access.
const SchemaVersion = 1

// ── State enums ────────────────────────────────────────────────────────────

// WorkState tracks where a Work is in its execution lifecycle.
type WorkState string

const (
	WorkDraft       WorkState = "draft"
	WorkReady       WorkState = "ready"
	WorkRunning     WorkState = "running"
	WorkWaitingUser WorkState = "waiting_user"
	WorkPaused      WorkState = "paused"
	WorkCompleted   WorkState = "completed"
	WorkFailed      WorkState = "failed"
	WorkCancelled   WorkState = "cancelled"
)

// WorkArchiveState is an independent lifecycle for archival — it never
// overwrites the last real execution result.
type WorkArchiveState string

const (
	ArchiveActive   WorkArchiveState = "active"
	ArchiveArchived WorkArchiveState = "archived"
	ArchiveDeleted  WorkArchiveState = "deleted"
)

// RunState is the execution state of a WorkflowRun, Stage, Task, or Attempt.
type RunState string

const (
	RunPending   RunState = "pending"
	RunRunning   RunState = "running"
	RunWaiting   RunState = "waiting"
	RunCompleted RunState = "completed"
	RunFailed    RunState = "failed"
	RunCancelled RunState = "cancelled"
)

// ── Blueprint / source ─────────────────────────────────────────────────────

// BlueprintSource names who authored a WorkBlueprint.
type BlueprintSource string

const (
	BlueprintSystem BlueprintSource = "system"
	BlueprintUser   BlueprintSource = "user"
	// BlueprintAddOn is "addon:<name>".
)

// ── References ─────────────────────────────────────────────────────────────

// BlueprintRef is a lightweight pointer to a specific version of a Blueprint.
type BlueprintRef struct {
	ID            string `json:"id"`
	SchemaVersion int    `json:"schemaVersion"`
	Version       int    `json:"version"`
}

// ToolContractRef records the contract a Blueprint expects from a tool.
type ToolContractRef struct {
	Name            string `json:"name"`
	ContractVersion int    `json:"contractVersion"`
	Provider        string `json:"provider,omitempty"`
	SideEffectClass string `json:"sideEffectClass"` // read | workspace_write | external_write | destructive
	Required        bool   `json:"required"`
}

// RuntimeFingerprint captures the environment a Work was created with.
type RuntimeFingerprint struct {
	WorkSchemaVersion  int               `json:"workSchemaVersion"`
	EventSchemaVersion int               `json:"eventSchemaVersion"`
	RendererSetVersion int               `json:"rendererSetVersion"`
	ToolContracts      []ToolContractRef `json:"toolContracts,omitempty"`
	Provider           string            `json:"provider,omitempty"`
	Model              string            `json:"model,omitempty"`
}

// ── WorkBlueprint ──────────────────────────────────────────────────────────

// WorkBlueprint defines the default layout and behaviour for a class of Work.
// SchemaVersion is the serialisation contract version; Version is the business
// version of this particular Blueprint definition.
type WorkBlueprint struct {
	SchemaVersion   int               `json:"schemaVersion"`
	ID              string            `json:"id"` // "blueprint:code-review"
	Version         int               `json:"version"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Source          BlueprintSource   `json:"source"`
	InputSchema     json.RawMessage   `json:"inputSchema,omitempty"`
	PromptTemplate  string            `json:"promptTemplate"`
	Workflow        WorkflowDef       `json:"workflow"`
	BlockSpecs      []BlockSpec       `json:"blockSpecs"`
	CornerstoneReqs []CornerstoneReq  `json:"cornerstoneRequirements,omitempty"`
	ConclusionKinds []ConclusionKind  `json:"conclusionKinds,omitempty"`
	ArtifactKinds   []string          `json:"artifactKinds,omitempty"`
	ToolContracts   []ToolContractRef `json:"toolContracts,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
}

// WorkflowDef is the V1 workflow definition — ordered stages with explicit
// gates. No general-purpose conditional branching DSL.
type WorkflowDef struct {
	Stages []StageSpec `json:"stages"`
}

// StageSpec is one stage inside a WorkflowDef.
type StageSpec struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	Tasks []TaskSpec `json:"tasks"`
	Gate  string     `json:"gate,omitempty"` // "input" | "approval" | ""
}

// TaskSpec is one task inside a StageSpec.
type TaskSpec struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// ── Work ───────────────────────────────────────────────────────────────────

// Work is the user root object — a saveable, runnable, flippable structured
// work unit.
type Work struct {
	SchemaVersion   int                    `json:"schemaVersion"`
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	State           WorkState              `json:"state"`
	ArchiveState    WorkArchiveState       `json:"archiveState"`
	BlueprintRef    BlueprintRef           `json:"blueprintRef"`
	Definition      WorkDefinitionSnapshot `json:"definitionSnapshot"`
	Inputs          map[string]any         `json:"inputs,omitempty"`
	Blocks          []BlockInstance        `json:"blocks"`
	Placements      []BlockPlacement       `json:"placements"`
	Prompt          string                 `json:"prompt"`
	Cornerstones    []Cornerstone          `json:"cornerstones"`
	Runs            []WorkflowRun          `json:"runs"`
	ActionReceipts  []ActionReceiptRecord  `json:"actionReceipts,omitempty"`
	Conclusions     []Conclusion           `json:"conclusions,omitempty"`
	RerunOf         string                 `json:"rerunOf,omitempty"`
	CopiedFrom      string                 `json:"copiedFrom,omitempty"`
	ReferencedWorks []string               `json:"referencedWorks,omitempty"`
	RerunUpgraded   bool                   `json:"rerunUpgraded,omitempty"`
	MigrationPath   []int                  `json:"migrationPath,omitempty"`
	CreatedWith     RuntimeFingerprint     `json:"createdWith"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
	ArchivedAt      *time.Time             `json:"archivedAt,omitempty"`
}

// WorkRecord is an immutable snapshot of an archived Work.
type WorkRecord struct {
	ArchiveSchemaVersion int             `json:"archiveSchemaVersion"`
	WorkID               string          `json:"workId"`
	Snapshot             Work            `json:"snapshot"`
	RendererSetVersion   int             `json:"rendererSetVersion"`
	FallbackBlocks       []BlockFallback `json:"fallbackBlocks"`
	ArchivedAt           time.Time       `json:"archivedAt"`
}
