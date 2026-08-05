package work

import (
	"errors"
)

// WorkTransportError is the stable Wails-facing error projection. Frontends
// branch on Code/Committed/Recoverable and never parse Go error strings.
type WorkTransportError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Operation   string `json:"operation,omitempty"`
	WorkID      string `json:"workId,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
	Revision    int64  `json:"revision,omitempty"`
	Committed   bool   `json:"committed"`
	Recoverable bool   `json:"recoverable"`
}

// RetryWorkNodeResult preserves the durable retry outcome even when scheduling
// must be resumed later. Result is the stable task row; no runtime map leaks.
type RetryWorkNodeResult struct {
	Result      *Task               `json:"result,omitempty"`
	Revision    int64               `json:"revision"`
	Duplicate   bool                `json:"duplicate"`
	Committed   bool                `json:"committed"`
	Recoverable bool                `json:"recoverable"`
	Error       *WorkTransportError `json:"error,omitempty"`
}

// SetNodeSkillResult reports the outcome of binding a Skill to a node.
type SetNodeSkillResult struct {
	Revision    int64               `json:"revision"`
	Duplicate   bool                `json:"duplicate"`
	Committed   bool                `json:"committed"`
	Recoverable bool                `json:"recoverable"`
	Error       *WorkTransportError `json:"error,omitempty"`
}

// ClearNodeSkillResult reports the outcome of removing a Skill binding.
type ClearNodeSkillResult struct {
	Revision    int64               `json:"revision"`
	Duplicate   bool                `json:"duplicate"`
	Committed   bool                `json:"committed"`
	Recoverable bool                `json:"recoverable"`
	Error       *WorkTransportError `json:"error,omitempty"`
}

// SkillInfo is the narrow view of a Skill exposed to Work binding UIs.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`
	Enabled     bool   `json:"enabled"`
	RunAs       string `json:"runAs,omitempty"`
}

// CreateSkillRequest carries the fields needed to scaffold a new Skill file.
type CreateSkillRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Scope       string `json:"scope"`
	RequestID   string `json:"requestId"`
}

// CreateSkillResult reports the outcome of creating a Skill.
type CreateSkillResult struct {
	Skill       *SkillInfo          `json:"skill,omitempty"`
	Error       *WorkTransportError `json:"error,omitempty"`
	Committed   bool                `json:"committed"`
	Recoverable bool                `json:"recoverable"`
}

// TransportErrorFrom uses errors.As across wrapped/joined failures.
func TransportErrorFrom(err error) *WorkTransportError {
	if err == nil {
		return nil
	}
	result := &WorkTransportError{Code: "work_error", Message: err.Error()}
	var committed *ErrWorkCommittedRecovery
	if errors.As(err, &committed) {
		result.Code = "committed_recovery"
		result.Operation = committed.Operation
		result.WorkID = committed.WorkID
		result.RequestID = committed.RequestID
		result.Revision = committed.Revision
		result.Committed = committed.Committed
		result.Recoverable = committed.Recoverable
		return result
	}
	var conflict *ErrWorkEventConflict
	if errors.As(err, &conflict) {
		if conflict.Kind == WorkEventRequestConflict {
			result.Code = "request_conflict"
		} else {
			result.Code = "revision_conflict"
		}
		result.WorkID = conflict.WorkID
		result.RequestID = conflict.RequestID
		result.Recoverable = true
		return result
	}
	var future *ErrFutureSchema
	if errors.As(err, &future) {
		result.Code = "future_schema"
		result.Recoverable = false
		return result
	}
	switch {
	case errors.Is(err, ErrDefinitionPlannerUnavailable):
		result.Code, result.Recoverable = "planner_unavailable", true
	case errors.Is(err, ErrDefinitionPlannerNoChange):
		result.Code, result.Recoverable = "planner_no_change", true
	case errors.Is(err, ErrDefinitionPlannerFailed):
		result.Code, result.Recoverable = "planner_failed", true
	case errors.Is(err, ErrWorkRequestIDConflict):
		result.Code, result.Recoverable = "request_conflict", true
	case errors.Is(err, ErrWorkNotFound):
		result.Code = "not_found"
	case errors.Is(err, ErrWorkNeedsRepair):
		result.Code, result.Recoverable = "needs_repair", true
	}
	return result
}
