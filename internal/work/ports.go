package work

import "context"

// WorkStore 是 Work 持久化的窄端口接口。
// 实现由 control/boot 侧提供适配器并注入。
type WorkStore interface {
	// CreateWorkDir 原子创建完整 Work 目录；重复 RequestID 必须校验创建意图。
	CreateWorkDir(input CreateWorkDirInput) error
	// LoadProjection 加载 Work 的当前投影。
	LoadProjection(workID string) (*Work, error)
	// LoadState 在同一 Work 写锁内加载投影、当前 revision 和可选 requestID 状态。
	LoadState(workID, requestID string) (*Work, WorkEventState, error)
	// LoadArchive 加载已归档的 WorkRecord。
	LoadArchive(workID string) (*WorkRecord, error)
	// Append 追加一条持久化 WorkEvent 到事件日志，返回分配 revision。
	// 调用方必须已持有该 Work 的 writer lease；底层维护和测试使用此接口。
	// 仅接受 WorkEvent，不接受 WorkViewEvent。
	Append(workID string, event WorkEvent) (int64, error)
	// CommitEvent 为 Service 获取 writer lease，并原子串行化单条事件提交。
	CommitEvent(workID string, event WorkEvent) (int64, error)
	// WriteProjection 以原子方式写入投影快照。
	WriteProjection(workID string, work *Work, revision int64) error
	// WriteArchive 以原子方式写入归档 WorkRecord。
	WriteArchive(workID string, record *WorkRecord) error
	// List 按筛选条件列出 Work 摘要。
	List(filter WorkFilter) ([]WorkSummary, error)
	// MoveToTrash 将 Work 移入回收站。
	MoveToTrash(workID, requestID string) error
	// RestoreFromTrash 从回收站恢复 Work。
	RestoreFromTrash(workID, requestID string) error
}

// WorkEventState 是 Service 执行幂等和乐观并发校验所需的持久化状态。
type WorkEventState struct {
	Revision        int64 `json:"revision"`
	RequestRevision int64 `json:"requestRevision,omitempty"`
	RequestFound    bool  `json:"requestFound"`
}

// TaskExecutor 是 WorkRunner 用来执行单个 Task 的窄端口。
// 实现侧（control）负责创建 Session、运行 Agent 并报告结果。
type TaskExecutor interface {
	// ExecuteTask 执行一个 Task 并返回 Attempt 结果。
	ExecuteTask(ctx context.Context, input TaskExecuteInput) (*Attempt, error)
	// CancelTask 请求取消关联 Session。重复 request ID 必须安全。
	CancelTask(ctx context.Context, session SessionRef, requestID string) error
}

// TaskExecuteInput carries stable object and request context into the session
// executor without exposing Controller internals.
type TaskExecuteInput struct {
	WorkID           string `json:"workId"`
	RunID            string `json:"runId"`
	StageID          string `json:"stageId"`
	TaskID           string `json:"taskId"`
	AttemptIndex     int    `json:"attemptIndex"`
	RequestID        string `json:"requestId"`
	DefinitionDigest string `json:"definitionDigest"`
	Prompt           string `json:"prompt"`
}

// SessionLookup 是 Work 查找关联 Session 的窄端口。
type SessionLookup interface {
	// LookupSession resolves the current metadata for a lightweight reference.
	LookupSession(ctx context.Context, sessionPath string) (SessionRef, bool, error)
}

// ToolCatalog 是 Work 查询可用工具的窄端口。
type ToolCatalog interface {
	// ResolveTool checks availability and contract compatibility without running
	// the tool.
	ResolveTool(ctx context.Context, ref ToolContractRef) (ToolCapability, error)
}

// ToolCapability is the preflight view of a registered tool.
type ToolCapability struct {
	Available  bool   `json:"available"`
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason,omitempty"`
}

// PermissionChecker 是 Work 执行权限检查的窄端口。
type PermissionChecker interface {
	// CheckPermission returns an explicit decision; callers must not infer
	// approval from an empty error or missing policy.
	CheckPermission(ctx context.Context, input PermissionRequest) (PermissionDecision, error)
}

// PermissionRequest contains only the action context relevant to policy.
type PermissionRequest struct {
	WorkID    string         `json:"workId"`
	RequestID string         `json:"requestId"`
	Object    ObjectContext  `json:"object"`
	ToolName  string         `json:"toolName"`
	Risk      string         `json:"risk"`
	Input     map[string]any `json:"input,omitempty"`
}

// PermissionDecision distinguishes direct allow, required approval, and deny.
type PermissionDecision struct {
	Allowed          bool   `json:"allowed"`
	ApprovalRequired bool   `json:"approvalRequired"`
	Reason           string `json:"reason,omitempty"`
}
