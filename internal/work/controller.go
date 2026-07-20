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

	// UpdateDraft 更新草稿 Work 的可编辑字段。
	UpdateDraft(ctx context.Context, input UpdateDraftInput) (*WorkView, error)

	// RunWork 启动 Work 执行，创建 WorkflowRun。
	RunWork(ctx context.Context, workID, requestID string) (*WorkflowRun, error)

	// RetryTask 在失败 Task 下新增 Attempt 重试。
	RetryTask(ctx context.Context, input RetryTaskInput) (*Attempt, error)

	// CancelRun 取消正在运行的 WorkflowRun。
	CancelRun(ctx context.Context, workID, runID, requestID string) error

	// ArchiveWork 归档 Work，生成不可变 WorkRecord。
	ArchiveWork(ctx context.Context, workID, requestID string) (*WorkRecord, error)

	// RestoreWork 从归档恢复 Work 到 active 状态。
	RestoreWork(ctx context.Context, workID, requestID string) (*WorkView, error)

	// DeleteWork 将 Work 移入回收站。
	DeleteWork(ctx context.Context, workID, requestID string) error

	// PinCornerstone 将类型化长期基石绑定到 Work。
	PinCornerstone(ctx context.Context, workID string, input CornerstoneInput) (*Cornerstone, error)

	// RefreshCornerstone 重新解析 live_ref 并更新其可观察状态。
	RefreshCornerstone(ctx context.Context, workID, cornerstoneID, requestID string) (*Cornerstone, error)

	// RemoveCornerstone 以 tombstone 方式移除基石。
	RemoveCornerstone(ctx context.Context, workID, cornerstoneID, requestID string) error

	// RefreshBlock 刷新指定 Block 的数据。
	RefreshBlock(ctx context.Context, workID, blockID, requestID string) (*BlockInstance, error)

	// ExecuteBlockAction 执行 Block 上的 action intent。
	ExecuteBlockAction(ctx context.Context, input BlockActionRequest) (*ActionReceipt, error)

	// PrepareRerun 为重执行做预检，返回 RerunPlan 供用户审阅。
	PrepareRerun(ctx context.Context, input PrepareRerunInput) (*RerunPlan, error)

	// ExecuteRerun 根据已审阅的 RerunPlan 执行重执行。
	ExecuteRerun(ctx context.Context, planToken, requestID string) (*Work, error)
}
