package work

import (
	"encoding/json"
	"time"
)

// CreateWorkInput 是创建 Work 的输入参数。
// RequestID 用于幂等创建：相同 RequestID 的重复调用返回同一结果。
type CreateWorkInput struct {
	BlueprintRef BlueprintRef   `json:"blueprintRef"`
	Name         string         `json:"name"`
	Inputs       map[string]any `json:"inputs,omitempty"`
	RequestID    string         `json:"requestId"`
}

// UpdateDraftInput 是更新草稿 Work 的输入参数。
// ExpectedRevision 用于乐观并发控制。
type UpdateDraftInput struct {
	WorkID           string         `json:"workId"`
	Name             *string        `json:"name,omitempty"`
	Prompt           *string        `json:"prompt,omitempty"`
	Inputs           map[string]any `json:"inputs,omitempty"`
	ExpectedRevision int64          `json:"expectedRevision"`
	RequestID        string         `json:"requestId"`
}

// RetryTaskInput 是重试 Task 的输入参数。
type RetryTaskInput struct {
	WorkID    string `json:"workId"`
	RunID     string `json:"runId"`
	StageID   string `json:"stageId"`
	TaskID    string `json:"taskId"`
	RequestID string `json:"requestId"`
}

// ResumeRunInput 是恢复暂停/等待 Run 的输入参数。
// GateResolutions 可附带 gate 已解决上下文（approval accepted / input provided）。
type ResumeRunInput struct {
	WorkID    string `json:"workId"`
	RunID     string `json:"runId"`
	RequestID string `json:"requestId"`
	// GateResolutions maps stage ID → resolution outcome.
	GateResolutions map[string]GateResolution `json:"gateResolutions,omitempty"`
}

// GateResolution records the outcome of an approval or input gate.
type GateResolution struct {
	StageID string         `json:"stageId"`
	Outcome string         `json:"outcome"` // "approved" | "input_provided"
	Input   map[string]any `json:"input,omitempty"`
	Note    string         `json:"note,omitempty"`
}

// BlockActionRequest 是执行 Block action 的输入。
type BlockActionRequest struct {
	WorkID           string         `json:"workId"`
	BlockID          string         `json:"blockId"`
	ActionID         string         `json:"actionId"`
	Input            map[string]any `json:"input,omitempty"`
	RequestID        string         `json:"requestId"`
	ExpectedRevision int64          `json:"expectedRevision"`
}

// WorkFilter 定义 Work 列表的筛选条件。
type WorkFilter struct {
	State        *WorkState        `json:"state,omitempty"`
	ArchiveState *WorkArchiveState `json:"archiveState,omitempty"`
	Blueprint    string            `json:"blueprint,omitempty"`
	Search       string            `json:"search,omitempty"`
	Cursor       string            `json:"cursor,omitempty"`
	Limit        int               `json:"limit"`
}

// WorkPage 是 Work 列表的分页结果。
type WorkPage struct {
	Items      []WorkSummary `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
	Total      int           `json:"total"`
}

// WorkView 是 Work 的前端视图投影，由 WorkViewEvent snapshot 携带。
type WorkView struct {
	SchemaVersion int   `json:"schemaVersion"`
	Work          *Work `json:"work"`
	Revision      int64 `json:"revision"`
}

// WorkSummary 是 Work 列表中的摘要条目。
type WorkSummary struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	State        WorkState        `json:"state"`
	ArchiveState WorkArchiveState `json:"archiveState"`
	BlueprintRef BlueprintRef     `json:"blueprintRef"`
	CreatedAt    time.Time        `json:"createdAt"`
	UpdatedAt    time.Time        `json:"updatedAt"`
}

// CornerstoneInput captures the user's pin intent. RequestID makes repeated
// pin requests safe and idempotent.
type CornerstoneInput struct {
	Type      CornerstoneType `json:"type"`
	Title     string          `json:"title"`
	Content   string          `json:"content"`
	Ref       CornerstoneRef  `json:"ref"`
	Mode      CornerstoneMode `json:"mode"`
	Required  bool            `json:"required"`
	Tags      []string        `json:"tags,omitempty"`
	RequestID string          `json:"requestId"`
}

// PrepareRerunInput 是重执行预检的输入。
type PrepareRerunInput struct {
	RecordID string    `json:"recordId"`
	Mode     RerunMode `json:"mode"`
}

// RerunMode 枚举重执行模式。
type RerunMode string

const (
	RerunOriginalDefinition RerunMode = "original_definition"
	RerunLatestDefinition   RerunMode = "latest_definition"
)

// RerunPlan 是重执行预检结果。PrepareRerun 返回此结果供用户审阅后再执行。
type RerunPlan struct {
	PlanToken         string             `json:"planToken"`
	SourceDefinition  BlueprintRef       `json:"sourceDefinition"`
	TargetDefinition  BlueprintRef       `json:"targetDefinition"`
	DefinitionDiff    []ChangeSummary    `json:"definitionDiff,omitempty"`
	MissingTools      []ToolContractRef  `json:"missingTools,omitempty"`
	MissingFiles      []SourceRef        `json:"missingFiles,omitempty"`
	MissingSecrets    []string           `json:"missingSecrets,omitempty"`
	PermissionIssues  []PermissionIssue  `json:"permissionIssues,omitempty"`
	CornerstoneIssues []CornerstoneIssue `json:"cornerstoneIssues,omitempty"`
	BlockIssues       []BlockCompatIssue `json:"blockIssues,omitempty"`
	Blocking          bool               `json:"blocking"`
	Warnings          []string           `json:"warnings,omitempty"`
	ExpiresAt         time.Time          `json:"expiresAt"`
}

// ChangeSummary 描述 Blueprint 版本间的差异。
type ChangeSummary struct {
	Field    string `json:"field"`
	Previous string `json:"previous,omitempty"`
	Current  string `json:"current,omitempty"`
	Breaking bool   `json:"breaking"`
}

// PermissionIssue 描述权限检查发现的问题。
type PermissionIssue struct {
	Tool        string `json:"tool"`
	Description string `json:"description"`
	Blocking    bool   `json:"blocking"`
}

// CornerstoneIssue 描述 Cornerstone 相关问题。
type CornerstoneIssue struct {
	CornerstoneID string `json:"cornerstoneId"`
	Title         string `json:"title"`
	Problem       string `json:"problem"`
	Blocking      bool   `json:"blocking"`
}

// BlockCompatIssue 描述 Block 兼容性问题。
type BlockCompatIssue struct {
	BlockID  string `json:"blockId"`
	Kind     string `json:"kind"`
	Problem  string `json:"problem"`
	Blocking bool   `json:"blocking"`
}

// ActionReceipt 是 Block action 执行的回执。
type ActionReceipt struct {
	WorkID                 string          `json:"workId"`
	BlockID                string          `json:"blockId"`
	ActionID               string          `json:"actionId"`
	HandlerIdentityVersion int             `json:"handlerIdentityVersion,omitempty"`
	HandlerID              string          `json:"handlerId,omitempty"`
	HandlerVersion         string          `json:"handlerVersion,omitempty"`
	Status                 string          `json:"status"`
	Message                string          `json:"message,omitempty"`
	RequestID              string          `json:"requestId"`
	Fingerprint            string          `json:"fingerprint"`
	Result                 json.RawMessage `json:"result,omitempty"`
	Retryable              bool            `json:"retryable"`
	OutcomeKnown           bool            `json:"outcomeKnown"`
	Revision               int64           `json:"revision"`
}

// BlockUpsertInput is a request to upsert a BlockInstance with revision-based
// merge semantics. ExpectedRevision is the Work-level optimistic lock.
type BlockUpsertInput struct {
	WorkID           string            `json:"workId"`
	BlockID          string            `json:"blockId"`
	Kind             string            `json:"kind"`
	SchemaVersion    int               `json:"schemaVersion"`
	Revision         int64             `json:"revision"`
	Title            string            `json:"title,omitempty"`
	Status           BlockStatus       `json:"status"`
	Data             json.RawMessage   `json:"data"`
	Actions          []BlockActionSpec `json:"actions,omitempty"`
	Source           BlockSource       `json:"source"`
	Freshness        *BlockFreshness   `json:"freshness,omitempty"`
	Fallback         BlockFallback     `json:"fallback"`
	Tombstone        bool              `json:"tombstone,omitempty"`
	ExpectedRevision int64             `json:"expectedRevision"`
	RequestID        string            `json:"requestId"`
}

// BlockRemoveInput is a tombstone marker for a block.
type BlockRemoveInput struct {
	WorkID           string `json:"workId"`
	BlockID          string `json:"blockId"`
	Revision         int64  `json:"revision"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// BlockPlacementInput carries a complete replacement set of placements.
type BlockPlacementInput struct {
	WorkID           string           `json:"workId"`
	Placements       []BlockPlacement `json:"placements"`
	ExpectedRevision int64            `json:"expectedRevision"`
	RequestID        string           `json:"requestId"`
}
