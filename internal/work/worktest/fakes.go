package worktest

import (
	"context"
	"errors"
	"sync"

	"workground2/internal/work"
)

// ErrUnconfigured makes missing fake behavior explicit.
var ErrUnconfigured = errors.New("worktest: fake method is not configured")

// Sink is a thread-safe WorkViewEvent recorder.
type Sink struct {
	mu     sync.Mutex
	events []work.WorkViewEvent
}

func (s *Sink) EmitWorkView(event work.WorkViewEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

// Events returns a copy so callers cannot mutate recorder state.
func (s *Sink) Events() []work.WorkViewEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]work.WorkViewEvent(nil), s.events...)
}

// Store delegates every WorkStore method to an explicit function.
type Store struct {
	CreateWorkDirFunc   func(work.CreateWorkDirInput) error
	LoadProjectionFunc  func(string) (*work.Work, error)
	LoadStateFunc       func(string, string) (*work.Work, work.WorkEventState, error)
	LoadTrashStateFunc  func(string, string) (*work.Work, work.WorkEventState, error)
	LoadArchiveFunc     func(string) (*work.WorkRecord, error)
	AppendFunc          func(string, work.WorkEvent) (int64, error)
	CommitEventFunc     func(string, work.WorkEvent) (int64, error)
	WriteProjectionFunc func(string, *work.Work, int64) error
	WriteArchiveFunc    func(string, *work.WorkRecord) error
	ListFunc            func(work.WorkFilter) ([]work.WorkSummary, error)
	MoveToTrashFunc     func(string, string) error
	RestoreFunc         func(string, string) error
}

func (s *Store) CreateWorkDir(input work.CreateWorkDirInput) error {
	if s.CreateWorkDirFunc == nil {
		return ErrUnconfigured
	}
	return s.CreateWorkDirFunc(input)
}

func (s *Store) LoadProjection(id string) (*work.Work, error) {
	if s.LoadProjectionFunc == nil {
		return nil, ErrUnconfigured
	}
	return s.LoadProjectionFunc(id)
}

func (s *Store) LoadState(id, requestID string) (*work.Work, work.WorkEventState, error) {
	if s.LoadStateFunc == nil {
		return nil, work.WorkEventState{}, ErrUnconfigured
	}
	return s.LoadStateFunc(id, requestID)
}

func (s *Store) LoadTrashState(id, requestID string) (*work.Work, work.WorkEventState, error) {
	if s.LoadTrashStateFunc == nil {
		return nil, work.WorkEventState{}, ErrUnconfigured
	}
	return s.LoadTrashStateFunc(id, requestID)
}

func (s *Store) LoadArchive(id string) (*work.WorkRecord, error) {
	if s.LoadArchiveFunc == nil {
		return nil, ErrUnconfigured
	}
	return s.LoadArchiveFunc(id)
}

func (s *Store) Append(id string, event work.WorkEvent) (int64, error) {
	if s.AppendFunc == nil {
		return 0, ErrUnconfigured
	}
	return s.AppendFunc(id, event)
}

func (s *Store) CommitEvent(id string, event work.WorkEvent) (int64, error) {
	if s.CommitEventFunc == nil {
		return 0, ErrUnconfigured
	}
	return s.CommitEventFunc(id, event)
}

func (s *Store) WriteProjection(id string, value *work.Work, revision int64) error {
	if s.WriteProjectionFunc == nil {
		return ErrUnconfigured
	}
	return s.WriteProjectionFunc(id, value, revision)
}

func (s *Store) WriteArchive(id string, value *work.WorkRecord) error {
	if s.WriteArchiveFunc == nil {
		return ErrUnconfigured
	}
	return s.WriteArchiveFunc(id, value)
}

func (s *Store) List(filter work.WorkFilter) ([]work.WorkSummary, error) {
	if s.ListFunc == nil {
		return nil, ErrUnconfigured
	}
	return s.ListFunc(filter)
}

func (s *Store) MoveToTrash(id, requestID string) error {
	if s.MoveToTrashFunc == nil {
		return ErrUnconfigured
	}
	return s.MoveToTrashFunc(id, requestID)
}

func (s *Store) RestoreFromTrash(id, requestID string) error {
	if s.RestoreFunc == nil {
		return ErrUnconfigured
	}
	return s.RestoreFunc(id, requestID)
}

// TaskExecutor is a configurable TaskExecutor fake.
type TaskExecutor struct {
	ExecuteFunc func(context.Context, work.TaskExecuteInput) (*work.Attempt, error)
	CancelFunc  func(context.Context, work.TaskCancelInput) error
}

func (f *TaskExecutor) ExecuteTask(ctx context.Context, input work.TaskExecuteInput) (*work.Attempt, error) {
	if f.ExecuteFunc == nil {
		return nil, ErrUnconfigured
	}
	return f.ExecuteFunc(ctx, input)
}

func (f *TaskExecutor) CancelTask(ctx context.Context, input work.TaskCancelInput) error {
	if f.CancelFunc == nil {
		return ErrUnconfigured
	}
	return f.CancelFunc(ctx, input)
}

// SessionLookup is a configurable SessionLookup fake.
type SessionLookup struct {
	LookupFunc func(context.Context, string) (work.SessionRef, bool, error)
}

func (f *SessionLookup) LookupSession(ctx context.Context, path string) (work.SessionRef, bool, error) {
	if f.LookupFunc == nil {
		return work.SessionRef{}, false, ErrUnconfigured
	}
	return f.LookupFunc(ctx, path)
}

// ToolCatalog is a configurable ToolCatalog fake.
type ToolCatalog struct {
	ResolveFunc func(context.Context, work.ToolContractRef) (work.ToolCapability, error)
}

func (f *ToolCatalog) ResolveTool(ctx context.Context, ref work.ToolContractRef) (work.ToolCapability, error) {
	if f.ResolveFunc == nil {
		return work.ToolCapability{}, ErrUnconfigured
	}
	return f.ResolveFunc(ctx, ref)
}

// PermissionChecker is a configurable PermissionChecker fake.
type PermissionChecker struct {
	CheckFunc func(context.Context, work.PermissionRequest) (work.PermissionDecision, error)
}

func (f *PermissionChecker) CheckPermission(ctx context.Context, input work.PermissionRequest) (work.PermissionDecision, error) {
	if f.CheckFunc == nil {
		return work.PermissionDecision{}, ErrUnconfigured
	}
	return f.CheckFunc(ctx, input)
}

var (
	_ work.ViewSink          = (*Sink)(nil)
	_ work.WorkStore         = (*Store)(nil)
	_ work.TaskExecutor      = (*TaskExecutor)(nil)
	_ work.SessionLookup     = (*SessionLookup)(nil)
	_ work.ToolCatalog       = (*ToolCatalog)(nil)
	_ work.PermissionChecker = (*PermissionChecker)(nil)
)
