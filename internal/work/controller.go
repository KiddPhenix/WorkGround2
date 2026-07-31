package work

import "context"

// WorkController 是所有前端共享的意图级 Work 入口。
// 每个方法对应一个用户意图，内部由 Work Service 实现业务细节。
// 所有非只读操作均接受 RequestID 以保证幂等。
//
// 此接口由 internal/control 消费；internal/work 不导入 control。
type WorkController interface {
	// CreateWork 从 Blueprint 创建新 Work。RequestID 用于幂等。
	CreateWork(ctx context.Context, input CreateWorkInput) (*Work, error)

	// GetWork 获取 Work 的当前视图投影。
	GetWork(ctx context.Context, workID string) (*WorkView, error)

	// ListWorks 按筛选条件分页列出 Work。
	ListWorks(ctx context.Context, filter WorkFilter) (WorkPage, error)

	// ListWorkBlueprints 列出当前可创建的 Blueprint。
	ListWorkBlueprints(ctx context.Context) ([]WorkBlueprint, error)

	// CopyWork 从现有 Work 创建独立草稿副本。
	CopyWork(ctx context.Context, input CopyWorkInput) (*Work, error)

	// UpdateDraft 更新草稿 Work 的可编辑字段。
	UpdateDraft(ctx context.Context, input UpdateDraftInput) (*WorkView, error)

	// UpsertWorkBlock 更新用户可编辑 Block。
	UpsertWorkBlock(ctx context.Context, input BlockUpsertInput) (*WorkView, error)

	// RunWork 启动 Work 执行，创建 WorkflowRun。
	RunWork(ctx context.Context, workID, requestID string) (*WorkflowRun, error)

	// RetryTask 在失败 Task 下新增 Attempt 重试。
	RetryTask(ctx context.Context, input RetryTaskInput) (*Attempt, error)

	// CancelRun 取消正在运行的 WorkflowRun。
	CancelRun(ctx context.Context, workID, runID, requestID string) error

	// PauseRun 暂停正在运行的 WorkflowRun。
	PauseRun(ctx context.Context, workID, runID, requestID string) error

	// ResumeRun 恢复暂停/等待用户的 WorkflowRun，可选附带 gate resolution 上下文。
	ResumeRun(ctx context.Context, input ResumeRunInput) (*WorkflowRun, error)

	// RestartRun 安全终止指定 Run 后重新启动，重复请求复用同一新 Run。
	RestartRun(ctx context.Context, workID, runID, requestID string) (*WorkflowRun, error)

	// ArchiveWork 归档 Work，生成不可变 WorkRecord。
	ArchiveWork(ctx context.Context, workID, requestID string) (*WorkRecord, error)

	// RestoreWork 从归档恢复 Work 到 active 状态。
	RestoreWork(ctx context.Context, workID, requestID string) (*WorkView, error)

	// DeleteWork 将 Work 移入回收站。
	DeleteWork(ctx context.Context, workID, requestID string) error

	// PinCornerstone 将类型化长期基石绑定到 Work。RequestID 和 ExpectedRevision
	// 用于幂等和乐观并发控制。返回 CornerstoneResult 包含基石、WorkView 和评估。
	PinCornerstone(ctx context.Context, workID string, input PinCornerstoneInput) (*CornerstoneResult, error)

	// RefreshCornerstone 重新解析 live_ref 的源状态或校验 snapshot blob 完整性。
	// 对于 live_ref，解析源并与保存的 digest 比较；状态可变为 stale/missing/denied。
	RefreshCornerstone(ctx context.Context, workID string, input RefreshCornerstoneInput) (*CornerstoneResult, error)

	// RemoveCornerstone 以 tombstone 方式移除基石，可通过 UndoCornerstone 恢复。
	RemoveCornerstone(ctx context.Context, workID string, input RemoveCornerstoneInput) (*CornerstoneResult, error)

	// UndoCornerstone 恢复已 tombstone 的基石。对于 snapshot 基石会校验 blob 完整性。
	UndoCornerstone(ctx context.Context, workID string, input UndoCornerstoneInput) (*CornerstoneResult, error)

	// AcceptCornerstone 接受 stale live_ref 基石的新解析内容，将状态切回 active。
	// 仅在精确 candidate digest 匹配时接受，防止 TOCTOU。
	AcceptCornerstone(ctx context.Context, workID string, input AcceptCornerstoneInput) (*CornerstoneResult, error)

	// FreezeCornerstone 将 live_ref 基石冻结为 snapshot，使其内容不再随源变化。
	// UseLastKnown 模式可在源不可达时冻结最后已知内容。
	FreezeCornerstone(ctx context.Context, workID string, input FreezeCornerstoneInput) (*CornerstoneResult, error)

	// RepairCornerstone 修复处于 missing/denied/invalid/stale 状态的基石。
	// live_ref 可替换 Ref 重新解析；snapshot 可 rematerialize blob。
	RepairCornerstone(ctx context.Context, workID string, input RepairCornerstoneInput) (*RepairResult, error)

	// RefreshBlock 刷新指定 Block 的数据。
	RefreshBlock(ctx context.Context, workID, blockID, requestID string) (*BlockInstance, error)

	// ExecuteBlockAction 执行 Block 上的 action intent。
	ExecuteBlockAction(ctx context.Context, input BlockActionRequest) (*ActionReceipt, error)

	// PrepareRerun 为重执行做预检，返回 RerunPlan 供用户审阅。
	PrepareRerun(ctx context.Context, input PrepareRerunInput) (*RerunPlan, error)

	// ExecuteRerun 根据已审阅的 RerunPlan 执行重执行。
	ExecuteRerun(ctx context.Context, planToken, requestID string) (*Work, error)

	// ── V2 Collaboration Controller ──────────────────────────────────────

	// BeginWorkPlanning starts a conversation-based definition flow.
	// Creates a new Work with a draft WorkDefinitionRevision.
	// requestID ensures idempotent creation.
	BeginWorkPlanning(ctx context.Context, input BeginWorkPlanningInput) (*WorkView, error)

	// BeginWorkPlanningWithResult exposes duplicate and committed-recovery
	// metadata to transport callers without changing the internal view API.
	BeginWorkPlanningWithResult(ctx context.Context, input BeginWorkPlanningInput) (*BeginWorkPlanningResult, error)

	// BeginBlueprintPlanning selects, defines, and applies a built-in V2
	// Blueprint through the authoritative planning and scheduling pipeline.
	BeginBlueprintPlanning(ctx context.Context, input BeginBlueprintPlanningInput) (*BeginBlueprintPlanningResult, error)

	// ApplyDefinition atomically activates a new definition revision.
	// expectedRevision guards against lost updates.
	ApplyDefinition(ctx context.Context, input ApplyDefinitionInput) (*ApplyDefinitionResult, error)

	// CreateCandidateRevision creates a copy-on-write draft and impact preview.
	// It never switches the active definition.
	CreateCandidateRevision(ctx context.Context, input CreateCandidateRevisionInput) (*CreateCandidateRevisionResult, error)

	// SubmitWorkInput commits a typed input value through InputService.
	// requestID guarantees idempotent submission; expectedRevision prevents
	// overwriting a concurrent update. On success the affected task subgraph
	// is automatically resumed.
	SubmitWorkInput(ctx context.Context, input SubmitInputRequest) (*SubmitInputResult, error)

	// AddCustomWorkInput adds user-owned text/file information without
	// changing the active WorkDefinitionRevision.
	AddCustomWorkInput(ctx context.Context, input AddCustomWorkInputRequest) (*SubmitInputResult, error)

	// SetInputCornerstone pins or unpins a submitted input as a Cornerstone.
	// Pin and input submission are independent operations; pin failure does
	// not roll back the input.
	SetInputCornerstone(ctx context.Context, input SetInputCornerstoneRequest) (*CornerstonePinResult, error)

	// PreviewWorkPatch generates a structured WorkPatchPreview from a
	// discussion instruction. This is read-only — no Work state is mutated.
	PreviewWorkPatch(ctx context.Context, input PreviewWorkPatchInput) (*PreviewWorkPatchResult, error)

	// ApplyWorkPatch applies a previously previewed patch. requestID makes
	// repeated applies idempotent; expectedRevision rejects stale mutations.
	// On success the affected task subgraph is automatically resumed.
	ApplyWorkPatch(ctx context.Context, input ApplyWorkPatchInput) (*ApplyWorkPatchResult, error)

	// RetryWorkNode retries a failed or invalidated V2 task node.
	// expectedRevision guards against lost updates.
	RetryWorkNode(ctx context.Context, input RetryWorkNodeRequest) (*RetryWorkNodeResult, error)

	// RetryArtifactSlot resets a failed/partial/stale active slot and wakes its
	// authoritative producer task.
	RetryArtifactSlot(ctx context.Context, input RetryArtifactSlotRequest) (*RetryArtifactSlotResult, error)

	// PreviewArtifact produces a graded ArtifactPreview for the given artifact
	// reference. This is read-only — no Work state is mutated. The preview is
	// cached by content digest and converter version; repeated calls for the
	// same content return the cached result. The original artifact is never
	// modified. When no converter is available, the result degrades safely to
	// filecard with CanOpen=true.
	PreviewArtifact(ctx context.Context, input PreviewArtifactRequest) (*PreviewArtifactResult, error)

	// RequestArtifactConversion executes or resumes an async conversion with
	// external-approval gating. requestID makes repeated requests idempotent.
	RequestArtifactConversion(ctx context.Context, input RequestArtifactConversionInput) (*RequestArtifactConversionResult, error)

	// RecoverArtifactConversions resumes pending and expired-running durable
	// conversion receipts for one Work. It is safe across process instances.
	RecoverArtifactConversions(ctx context.Context, workID string) (int, error)
}
