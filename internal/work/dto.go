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
// Assessment 和 RunBlock 是权威阻断投影：UI 不得据此猜测 status，
// 必须将 WorkView 作为单一可信源派生 Attention、RunEntry 和 blocked 指示。
type WorkView struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Work          *Work                 `json:"work"`
	Revision      int64                 `json:"revision"`
	Assessment    CornerstoneAssessment `json:"assessment"`
	RunBlock      *RunBlockReason       `json:"runBlock,omitempty"`
}

// RunBlockCode 是权威阻断原因的稳定机器可读编码。UI 据此映射图标和文案，
// 不得解析 Detail 英文文本做逻辑分支。
type RunBlockCode string

const (
	RunBlockBlobMissing         RunBlockCode = "blob_missing"
	RunBlockBudgetExhausted     RunBlockCode = "budget_exhausted"
	RunBlockResolverUnavailable RunBlockCode = "resolver_unavailable"
	RunBlockCornerstoneStale    RunBlockCode = "cornerstone_stale"
	RunBlockCornerstoneMissing  RunBlockCode = "cornerstone_missing"
	RunBlockCornerstoneDenied   RunBlockCode = "cornerstone_denied"
	RunBlockCornerstoneInvalid  RunBlockCode = "cornerstone_invalid"
	RunBlockWaitingUser         RunBlockCode = "waiting_user"
	RunBlockFailed              RunBlockCode = "failed"
	RunBlockArchived            RunBlockCode = "archived"
)

// RunBlockItem 携带单个阻断原因的稳定编码与关联上下文。
type RunBlockItem struct {
	Code          RunBlockCode      `json:"code"`
	CornerstoneID string            `json:"cornerstoneId,omitempty"`
	Status        CornerstoneStatus `json:"status,omitempty"`
	Detail        string            `json:"detail,omitempty"`
}

// RunBlockReason 解释为何当前 Work 无法运行。nil 或空 Items 表示可运行。
type RunBlockReason struct {
	Blocked bool           `json:"blocked"`
	Items   []RunBlockItem `json:"items,omitempty"`
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

// PinCornerstoneInput is a typed mutation DTO for pinning a Cornerstone.
// ExpectedRevision is the Work-level optimistic lock. Repeated calls with the
// same RequestID are idempotent and return the same stable Cornerstone ID.
type PinCornerstoneInput struct {
	Type             CornerstoneType `json:"type"`
	Title            string          `json:"title"`
	Content          string          `json:"content"`
	Ref              CornerstoneRef  `json:"ref"`
	Mode             CornerstoneMode `json:"mode"`
	Required         bool            `json:"required"`
	Tags             []string        `json:"tags,omitempty"`
	ExpectedRevision int64           `json:"expectedRevision"`
	RequestID        string          `json:"requestId"`
}

// RefreshCornerstoneInput is a typed mutation DTO for refreshing a live_ref
// Cornerstone's source status. For snapshot cornerstones, this verifies blob
// integrity.
type RefreshCornerstoneInput struct {
	CornerstoneID    string `json:"cornerstoneId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// RemoveCornerstoneInput is a typed mutation DTO for removing a Cornerstone.
// The operation writes a tombstone event; the cornerstone is not physically
// deleted from the projection.
type RemoveCornerstoneInput struct {
	CornerstoneID    string `json:"cornerstoneId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// UndoCornerstoneInput is a typed mutation DTO for restoring a tombstoned
// Cornerstone.
type UndoCornerstoneInput struct {
	CornerstoneID    string `json:"cornerstoneId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// CornerstoneResult is the unified result of a Cornerstone mutation.
type CornerstoneResult struct {
	Cornerstone *Cornerstone           `json:"cornerstone"`
	WorkView    *WorkView              `json:"workView,omitempty"`
	Duplicate   bool                   `json:"duplicate"`
	Revision    int64                  `json:"revision"`
	Resolution  *CornerstoneResolution `json:"resolution,omitempty"`
	Assessment  CornerstoneAssessment  `json:"assessment"`
}

// CornerstoneResolution is the transient, reviewable result of resolving a
// source. CandidateContent and Diff are never written to Work events.
type CornerstoneResolution struct {
	CandidateContent string           `json:"candidateContent,omitempty"`
	CandidateDigest  string           `json:"candidateDigest,omitempty"`
	Diff             string           `json:"diff,omitempty"`
	ErrorKind        ResolveErrorKind `json:"errorKind,omitempty"`
	Retryable        bool             `json:"retryable"`
}

// CornerstoneUseState describes whether a Work may consume its Cornerstones.
type CornerstoneUseState string

const (
	CornerstoneUseReady    CornerstoneUseState = "ready"
	CornerstoneUseDegraded CornerstoneUseState = "degraded"
	CornerstoneUseBlocked  CornerstoneUseState = "blocked"
)

// CornerstoneAssessment is shared by open/run/preflight callers. Required
// failures block; optional failures remain explicit degraded warnings.
type CornerstoneAssessment struct {
	State    CornerstoneUseState `json:"state"`
	Blocking bool                `json:"blocking"`
	Degraded bool                `json:"degraded"`
	Issues   []CornerstoneIssue  `json:"issues,omitempty"`
}

// AcceptCornerstoneInput carries the intent to accept a stale live_ref
// Cornerstone's newly resolved content. The Content and Digest fields are
// replaced with the candidate values; status transitions back to active.
type AcceptCornerstoneInput struct {
	CornerstoneID    string `json:"cornerstoneId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// FreezeCornerstoneInput carries the intent to freeze a live_ref Cornerstone
// into a snapshot. The current content is resolved, written as a snapshot
// (possibly to blob store), and the Mode is changed to snapshot.
type FreezeCornerstoneInput struct {
	CornerstoneID string `json:"cornerstoneId"`
	// UseLastKnown explicitly freezes the last accepted content when the source
	// is currently unreachable. Without it, Freeze re-resolves and verifies the
	// reviewed candidate/current digest before writing a snapshot.
	UseLastKnown     bool   `json:"useLastKnown,omitempty"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// RepairCornerstoneInput carries the intent to repair a Cornerstone that is
// in missing, denied, invalid, or stale status. For live_ref cornerstones,
// this re-resolves the source. For snapshot cornerstones with blob references,
// this attempts to recover the blob.
type RepairCornerstoneInput struct {
	CornerstoneID string `json:"cornerstoneId"`
	// Ref replaces a broken live reference. A nil Ref retries the current one.
	Ref *CornerstoneRef `json:"ref,omitempty"`
	// Content rematerializes a missing snapshot blob and must match the already
	// accepted snapshot digest.
	Content          *string `json:"content,omitempty"`
	ExpectedRevision int64   `json:"expectedRevision"`
	RequestID        string  `json:"requestId"`
}

// RepairResult reports the outcome of a Cornerstone repair attempt.
type RepairResult struct {
	Cornerstone *Cornerstone `json:"cornerstone"`
	WorkView    *WorkView    `json:"workView,omitempty"`
	// Repaired is true when the cornerstone status is now active.
	Repaired  bool  `json:"repaired"`
	Duplicate bool  `json:"duplicate"`
	Revision  int64 `json:"revision"`
	// FailedRefs lists any refs within a compound repair that could not be fixed.
	FailedRefs []string               `json:"failedRefs,omitempty"`
	Resolution *CornerstoneResolution `json:"resolution,omitempty"`
	Assessment CornerstoneAssessment  `json:"assessment"`
}

// GCInput carries the intent to garbage-collect unreferenced blobs.
type GCInput struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// GCResult reports the outcome of a blob GC pass.
type GCResult struct {
	WorkID        string   `json:"workId"`
	TotalBlobs    int      `json:"totalBlobs"`
	Referenced    int      `json:"referenced"`
	Reclaimed     int      `json:"reclaimed"`
	ReclaimedKeys []string `json:"reclaimedKeys,omitempty"`
	Errors        []string `json:"errors,omitempty"`
	Duplicate     bool     `json:"duplicate"`
	Revision      int64    `json:"revision"`
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
