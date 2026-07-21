package work

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"time"
)

// WorkStore 是 Work 持久化的窄端口接口。
// 实现由 control/boot 侧提供适配器并注入。
type WorkStore interface {
	// CreateWorkDir 原子创建完整 Work 目录；重复 RequestID 必须校验创建意图。
	CreateWorkDir(input CreateWorkDirInput) error
	// LoadProjection 加载 Work 的当前投影。
	LoadProjection(workID string) (*Work, error)
	// LoadState 在同一 Work 写锁内加载投影、当前 revision 和可选 requestID 状态。
	LoadState(workID, requestID string) (*Work, WorkEventState, error)
	// LoadTrashState 从回收站事件日志加载同样的权威状态，不修改或恢复目录。
	LoadTrashState(workID, requestID string) (*Work, WorkEventState, error)
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
	Revision          int64         `json:"revision"`
	LifecycleRevision int64         `json:"lifecycleRevision,omitempty"`
	RequestRevision   int64         `json:"requestRevision,omitempty"`
	RequestType       WorkEventType `json:"requestType,omitempty"`
	// RequestEventID binds an idempotent replay to the original operation,
	// object, and caller intent encoded by the event producer. It is sourced
	// from the authoritative request index and survives log compaction.
	RequestEventID string `json:"requestEventId,omitempty"`
	RequestFound   bool   `json:"requestFound"`
}

// TaskExecutor 是 WorkRunner 用来执行单个 Task 的窄端口。
// 实现侧（control）负责创建 Session、运行 Agent 并报告结果。
type TaskExecutor interface {
	// ExecuteTask 执行一个 Task 并返回 Attempt 结果。
	ExecuteTask(ctx context.Context, input TaskExecuteInput) (*Attempt, error)
	// CancelTask 按稳定 Attempt 上下文取消关联 Session。SessionRef 允许为空，
	// 因为 cancel intent 可能早于 Session 创建完成；重复 request ID 必须安全。
	CancelTask(ctx context.Context, input TaskCancelInput) error
}

// TaskExecuteInput carries stable object and request context into the session
// executor without exposing Controller internals.
type TaskExecuteInput struct {
	WorkID           string `json:"workId"`
	RunID            string `json:"runId"`
	StageID          string `json:"stageId"`
	TaskID           string `json:"taskId"`
	AttemptID        string `json:"-"`
	AttemptIndex     int    `json:"attemptIndex"`
	RequestID        string `json:"requestId"`
	DefinitionDigest string `json:"definitionDigest"`
	SideEffectClass  string `json:"-"`
	Prompt           string `json:"prompt"`
}

// TaskCancelInput identifies one attempt independently of Session creation.
// Stable object IDs are the owner key; SessionRef is supplemental evidence.
type TaskCancelInput struct {
	WorkID    string     `json:"workId"`
	RunID     string     `json:"runId"`
	StageID   string     `json:"stageId"`
	TaskID    string     `json:"taskId"`
	AttemptID string     `json:"attemptId"`
	Session   SessionRef `json:"sessionRef,omitempty"`
	RequestID string     `json:"requestId"`
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
	WorkID          string         `json:"workId"`
	BlockID         string         `json:"blockId"`
	ActionID        string         `json:"actionId"`
	HandlerID       string         `json:"handlerId"`
	HandlerVersion  string         `json:"handlerVersion"`
	RequestID       string         `json:"requestId"`
	Object          ObjectContext  `json:"object"`
	ToolName        string         `json:"toolName"`
	Risk            string         `json:"risk"`
	Summary         string         `json:"summary"`
	ConfirmRequired bool           `json:"confirmRequired"`
	Input           map[string]any `json:"input,omitempty"`
}

// PermissionDecision distinguishes direct allow, required approval, and deny.
type PermissionDecision struct {
	Allowed          bool   `json:"allowed"`
	ApprovalRequired bool   `json:"approvalRequired"`
	Reason           string `json:"reason,omitempty"`
}

// ── Block source adapter ─────────────────────────────────────────────────

// BlockSourceAdapter fetches fresh data for a BlockInstance from an external
// source. Implementations must be safe for concurrent use and honour context
// cancellation.
//
// When the source is temporarily unreachable the adapter should return
// ErrSourceUnavailable. The caller uses this to trigger backoff without
// marking the block permanently failed.
type BlockSourceAdapter interface {
	FetchBlock(ctx context.Context, workID string, block BlockInstance) (BlockRefreshResult, error)
}

// BlockRefreshResult is the validated output of a BlockSourceAdapter.FetchBlock
// call. Only Kind, SchemaVersion, Data, Status, Freshness, and Fallback are
// accepted; Actions, Source, Title, and Tombstone are rejected for refresh
// because they carry execution/UI intent.
type BlockRefreshResult struct {
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schemaVersion"`
	Data          json.RawMessage `json:"data"`
	Status        BlockStatus     `json:"status"`
	Freshness     *BlockFreshness `json:"freshness,omitempty"`
	Fallback      BlockFallback   `json:"fallback"`
}

// RefreshBlockInput carries controller-owned request and scheduling context.
// Source is trusted registration metadata; adapters cannot replace it.
type RefreshBlockInput struct {
	WorkID    string
	BlockID   string
	RequestID string
	Source    BlockSource
	CheckedAt time.Time
	RetryAt   *time.Time
}

// ── Clock ─────────────────────────────────────────────────────────────────

// Clock abstracts time for deterministic scheduling tests.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// RealClock delegates to the time package.
type RealClock struct{}

func (RealClock) Now() time.Time                         { return time.Now() }
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// JitterFunc returns a retry delay in [0, limit]. It is injectable so retry
// schedules remain deterministic in tests and observable in production.
type JitterFunc func(limit time.Duration) time.Duration

// FullJitter returns a process-safe random delay in [0, limit].
func FullJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	if limit == time.Duration(1<<63-1) {
		return time.Duration(rand.Int64N(int64(limit)))
	}
	return time.Duration(rand.Int64N(int64(limit) + 1))
}

// ── Backoff ───────────────────────────────────────────────────────────────

// BackoffStrategy computes the delay before the next retry after n consecutive
// failures. Implementations must be safe for concurrent use.
type BackoffStrategy interface {
	// Delay returns the wait duration after n consecutive failures, where n≥1.
	Delay(n int) time.Duration
}

// ExponentialBackoff implements capped exponential backoff with full jitter.
type ExponentialBackoff struct {
	Base   time.Duration
	Max    time.Duration
	Jitter JitterFunc
}

// NewExponentialBackoff returns a backoff with sensible defaults.
func NewExponentialBackoff() *ExponentialBackoff {
	return &ExponentialBackoff{Base: 200 * time.Millisecond, Max: 30 * time.Second, Jitter: FullJitter}
}

func (b *ExponentialBackoff) Delay(n int) time.Duration {
	if b == nil || n < 1 {
		return 0
	}
	base := b.Base
	if base <= 0 {
		base = 200 * time.Millisecond
	}
	maxDelay := b.Max
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	factor := time.Duration(int64(1) << min(n-1, 10))
	d := maxDelay
	if base <= maxDelay/factor {
		d = base * factor
	}
	if b.Jitter == nil {
		return d
	}
	jitter := b.Jitter(d)
	if jitter < 0 {
		return 0
	}
	if jitter > d {
		return d
	}
	return jitter
}

// ── Refresh schedule ─────────────────────────────────────────────────────

// RefreshSchedule controls how often a block source is polled.
type RefreshSchedule struct {
	// Interval is the base polling interval. Zero retains the intent for manual
	// refreshes without creating an automatic timer. Negative values are invalid.
	Interval    time.Duration
	Backoff     BackoffStrategy
	MaxFailures int // 0 = unlimited retries
}

// DefaultRefreshSchedule returns a schedule with a 90-second interval and
// exponential backoff capped at 30 seconds.
func DefaultRefreshSchedule() RefreshSchedule {
	return RefreshSchedule{
		Interval:    90 * time.Second,
		Backoff:     NewExponentialBackoff(),
		MaxFailures: 0,
	}
}

// ── Sentinel errors ──────────────────────────────────────────────────────

// ErrSourceUnavailable signals a transient source failure. Callers should
// back off and retry rather than marking the block permanently failed.
var ErrSourceUnavailable = errors.New("work: block source is temporarily unavailable")

// ErrBlockNotRefreshable signals that the given block cannot be refreshed
// (e.g. user-editable blocks that have no source adapter).
var ErrBlockNotRefreshable = errors.New("work: block is not refreshable")

// ErrBlockRefreshStopped means the Work lifecycle no longer permits refresh.
var ErrBlockRefreshStopped = errors.New("work: block refresh stopped")

// ErrBlockNotFound identifies a terminal refresh target that no longer exists.
var ErrBlockNotFound = errors.New("work: block not found")

// ErrBlockSourcePanic identifies an adapter panic converted at the source
// boundary into an observable, retryable refresh failure.
var ErrBlockSourcePanic = errors.New("work: block source adapter panic")

// ErrBlockRefreshFailed identifies an idempotently replayed failed attempt
// when the original adapter error type is no longer available in memory.
var ErrBlockRefreshFailed = errors.New("work: block refresh failed")

// ErrTaskNotRunning means a cancellation target has already stopped. Callers
// may treat it as successful delivery because no active side effect remains.
var ErrTaskNotRunning = errors.New("work: task attempt is not running")
