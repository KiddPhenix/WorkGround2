package assistant

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func ValidateAssistant(value Assistant) error { return validateAssistant(value) }
func ValidateRoutine(value Routine) error     { return validateRoutine(value) }
func ValidateSchedule(value Schedule) error   { return validateSchedule(value) }

func validateID(kind, id string) error {
	if id != strings.TrimSpace(id) {
		return fmt.Errorf("assistant: %s id must not have surrounding whitespace", kind)
	}
	if id == "" {
		return fmt.Errorf("assistant: %s id is required", kind)
	}
	if !safeID.MatchString(id) || strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) || !filepath.IsLocal(id) {
		return fmt.Errorf("assistant: unsafe %s id %q", kind, id)
	}
	return nil
}

// maxDirectPromptBytes bounds a single direct user input ("对助手说") by its
// UTF-8 byte length. The normalized prompt is stored in the frozen Run, so the
// limit keeps aggregates bounded without silently truncating user text.
const maxDirectPromptBytes = 64 * 1024

func validateDirectPrompt(prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return errors.New("assistant: direct prompt must not be empty")
	}
	if len(prompt) > maxDirectPromptBytes {
		return fmt.Errorf("assistant: direct prompt exceeds %d bytes", maxDirectPromptBytes)
	}
	return nil
}

func validateRequestID(id string) error {
	if id != strings.TrimSpace(id) {
		return errors.New("assistant: request id must not have surrounding whitespace")
	}
	if id == "" {
		return errors.New("assistant: request id is required")
	}
	if len(id) > 512 {
		return errors.New("assistant: request id is too long")
	}
	return nil
}

func validateAssistant(a Assistant) error {
	if err := validateID("assistant", a.ID); err != nil {
		return err
	}
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("assistant: name is required")
	}
	if strings.TrimSpace(a.Mission) == "" {
		return errors.New("assistant: mission is required")
	}
	switch a.Scope {
	case ScopeGlobal:
		if strings.TrimSpace(a.WorkspaceRoot) != "" {
			return errors.New("assistant: global scope cannot have a workspace root")
		}
	case ScopeWorkspace:
		if strings.TrimSpace(a.WorkspaceRoot) == "" {
			return errors.New("assistant: workspace scope requires a workspace root")
		}
	default:
		return fmt.Errorf("assistant: invalid scope %q", a.Scope)
	}
	switch a.Lifecycle {
	case LifecycleActive, LifecyclePaused, LifecycleArchived:
	default:
		return fmt.Errorf("assistant: invalid lifecycle %q", a.Lifecycle)
	}
	if err := validatePolicy(a.Policy); err != nil {
		return err
	}
	if a.Revision < 1 {
		return errors.New("assistant: revision must be positive")
	}
	if a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		return errors.New("assistant: timestamps are required")
	}
	return nil
}

func validatePolicy(p Policy) error {
	for name, access := range map[string]Access{
		"local_write": p.LocalWrite, "network": p.Network, "publish": p.Publish,
		"delete": p.Delete, "payment": p.Payment, "secrets": p.Secrets, "private_data": p.Private,
	} {
		switch access {
		case AccessDeny, AccessAllow, AccessApprove:
		default:
			return fmt.Errorf("assistant: invalid %s access %q", name, access)
		}
	}
	return nil
}

func validateRoutine(r Routine) error {
	if err := validateID("routine", r.ID); err != nil {
		return err
	}
	if err := validateID("assistant", r.AssistantID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Prompt) == "" {
		return errors.New("assistant: routine title and prompt are required")
	}
	if r.CatchUp != CatchUpCoalesceLatest && r.CatchUp != CatchUpSkip {
		return fmt.Errorf("assistant: invalid catch-up policy %q", r.CatchUp)
	}
	if err := validateSchedule(r.Schedule); err != nil {
		return err
	}
	if r.Revision < 1 || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return errors.New("assistant: routine revision and timestamps are required")
	}
	return nil
}

func validateSchedule(s Schedule) error {
	switch s.Kind {
	case ScheduleManual:
		return nil
	case ScheduleInterval:
		if s.IntervalSeconds <= 0 {
			return errors.New("assistant: interval seconds must be positive")
		}
		if s.IntervalSeconds > int64((time.Duration(1<<63-1))/time.Second) {
			return errors.New("assistant: interval seconds exceed supported duration")
		}
		if s.Window.Start != "" {
			if strings.TrimSpace(s.Timezone) == "" {
				return errors.New("assistant: interval schedule with a window requires a timezone")
			}
			if _, err := scheduleLocation(s); err != nil {
				return err
			}
		}
	case ScheduleDaily, ScheduleWeekly, ScheduleBiweekly, ScheduleMonthly, ScheduleYearly:
		if _, err := scheduleLocation(s); err != nil {
			return err
		}
		if _, _, err := parseClock(s.At); err != nil {
			return err
		}
		if s.Kind == ScheduleWeekly || s.Kind == ScheduleBiweekly {
			if s.Weekday < time.Sunday || s.Weekday > time.Saturday {
				return errors.New("assistant: weekday is invalid")
			}
		}
		if s.Kind == ScheduleBiweekly && s.StartAt.IsZero() {
			return errors.New("assistant: biweekly schedule requires start_at as its parity anchor")
		}
		if s.Kind == ScheduleMonthly || s.Kind == ScheduleYearly {
			if s.Day < 1 || s.Day > 31 {
				return errors.New("assistant: day must be between 1 and 31")
			}
		}
		if s.Kind == ScheduleYearly && (s.Month < time.January || s.Month > time.December) {
			return errors.New("assistant: month is invalid")
		}
	default:
		return fmt.Errorf("assistant: invalid schedule kind %q", s.Kind)
	}
	if (s.Window.Start == "") != (s.Window.End == "") {
		return errors.New("assistant: schedule window requires both start and end")
	}
	if s.Window.Start != "" {
		startHour, startMinute, err := parseClock(s.Window.Start)
		if err != nil {
			return fmt.Errorf("assistant: invalid window start: %w", err)
		}
		endHour, endMinute, err := parseClock(s.Window.End)
		if err != nil {
			return fmt.Errorf("assistant: invalid window end: %w", err)
		}
		if startHour == endHour && startMinute == endMinute {
			return errors.New("assistant: schedule window start and end must differ")
		}
		if s.Kind != ScheduleInterval {
			hour, minute, _ := parseClock(s.At)
			if !clockMinuteInWindow(hour*60+minute, startHour*60+startMinute, endHour*60+endMinute) {
				return errors.New("assistant: scheduled time must be inside its run window")
			}
		}
	}
	return nil
}

func clockMinuteInWindow(value, start, end int) bool {
	if start < end {
		return value >= start && value < end
	}
	return value >= start || value < end
}

func validateRun(r Run) error {
	if err := validateID("run", r.ID); err != nil {
		return err
	}
	if err := validateID("assistant", r.AssistantID); err != nil {
		return err
	}
	if err := validateRequestID(r.RequestID); err != nil {
		return err
	}
	switch r.Trigger {
	case TriggerManual, TriggerScheduled:
	default:
		return fmt.Errorf("assistant: invalid run trigger %q", r.Trigger)
	}
	switch r.State {
	case RunQueued, RunRunning, RunSucceeded, RunWaitingApproval, RunRetryWait,
		RunWaitingAttention, RunFailed, RunCancelled:
	default:
		return fmt.Errorf("assistant: invalid run state %q", r.State)
	}
	if r.State == RunRunning && (r.LeaseOwner == "" || r.LeaseFence < 1 || r.LeaseUntil.IsZero()) {
		return errors.New("assistant: running run requires a fenced lease")
	}
	if r.Attempt < 0 || r.MaxAttempts < 1 || r.Revision < 1 {
		return errors.New("assistant: invalid run counters")
	}
	if r.AssistantRevision < 1 || r.Scope == "" {
		return errors.New("assistant: legacy run missing frozen assistant context; migrate or recreate the run")
	}
	switch r.Scope {
	case ScopeGlobal:
		if strings.TrimSpace(r.WorkspaceRoot) != "" {
			return errors.New("assistant: frozen global run cannot have a workspace root")
		}
	case ScopeWorkspace:
		if strings.TrimSpace(r.WorkspaceRoot) == "" {
			return errors.New("assistant: frozen workspace run requires a workspace root")
		}
	default:
		return fmt.Errorf("assistant: invalid frozen run scope %q", r.Scope)
	}
	if strings.TrimSpace(r.Mission) == "" {
		return errors.New("assistant: run requires a frozen mission")
	}
	if err := validatePolicy(r.Policy); err != nil {
		return err
	}
	if r.RoutineID != "" && (r.RoutineRevision < 1 || strings.TrimSpace(r.Prompt) == "") {
		return errors.New("assistant: routine run requires frozen routine revision and prompt")
	}
	if r.ResponsibilityID != "" {
		if err := validateID("responsibility", r.ResponsibilityID); err != nil {
			return err
		}
	}
	return nil
}

func validateMemoryItem(item MemoryItem) error {
	if err := validateID("memory", item.ID); err != nil {
		return err
	}
	switch item.Kind {
	case MemoryCharter, MemoryFact, MemoryStrategy, MemoryOpenLoop, MemoryMetric:
	default:
		return fmt.Errorf("assistant: invalid memory kind %q", item.Kind)
	}
	if strings.TrimSpace(item.Body) == "" {
		return errors.New("assistant: memory body is required")
	}
	return nil
}
