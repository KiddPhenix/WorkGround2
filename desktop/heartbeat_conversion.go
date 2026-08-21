// Heartbeat → Assistant conversion.
//
// Old HeartbeatTask entries remain readable, and each one can be converted into
// a durable Assistant + one Routine through a resumable, cross-store state
// machine. The conversion journal (heartbeat-conversions.json) is the single
// source of truth for progress; every step is idempotent and replayable so a
// crash at any point converges on retry without leaving the old heartbeat task
// and the new assistant running at the same time.

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/assistant"
	"workground2/internal/config"
	"workground2/internal/fileutil"
)

// ── Result / status types ──────────────────────────────────────────────────

// Conversion result states surfaced to the frontend. These are intentionally
// narrow and typed so the panel never has to guess from an error string.
const (
	HeartbeatConvConvertible = "convertible" // no receipt yet, mapping is lossless
	HeartbeatConvConverted   = "converted"   // receipt committed and assistant activated
	HeartbeatConvConflict    = "conflict"    // task changed after conversion; no new object
	HeartbeatConvUnmappable  = "unmappable"  // schedule cannot be mapped losslessly
	HeartbeatConvInProgress  = "in_progress" // receipt exists but not yet activated
)

// HeartbeatConversionState is the durable progress marker stored in the
// conversion journal.
type HeartbeatConversionState string

const (
	HeartbeatConversionCreated   HeartbeatConversionState = "created"
	HeartbeatConversionDisabled  HeartbeatConversionState = "disabled"
	HeartbeatConversionActivated HeartbeatConversionState = "activated"
)

// HeartbeatConversionStatus describes one heartbeat task's conversion state
// without mutating anything.
type HeartbeatConversionStatus struct {
	TaskID        string `json:"taskId"`
	State         string `json:"state"`
	AssistantID   string `json:"assistantId,omitempty"`
	AssistantName string `json:"assistantName,omitempty"`
	Reason        string `json:"reason,omitempty"`
	ApprovalMode  string `json:"approvalMode,omitempty"` // original mode, for risk display
}

// HeartbeatConversionResult is the outcome of a single conversion request.
type HeartbeatConversionResult struct {
	TaskID        string              `json:"taskId"`
	State         string              `json:"state"`
	AssistantID   string              `json:"assistantId,omitempty"`
	AssistantName string              `json:"assistantName,omitempty"`
	Assistant     assistant.Assistant `json:"assistant,omitempty"`
	Reason        string              `json:"reason,omitempty"`
	ApprovalMode  string              `json:"approvalMode,omitempty"`
}

// ── Conversion journal ─────────────────────────────────────────────────────

type heartbeatConversionReceipt struct {
	TaskID       string                   `json:"taskId"`
	Fingerprint  string                   `json:"fingerprint"`
	AssistantID  string                   `json:"assistantId"`
	RoutineID    string                   `json:"routineId"`
	State        HeartbeatConversionState `json:"state"`
	Enabled      bool                     `json:"enabled"`
	ApprovalMode string                   `json:"approvalMode"`
	UpdatedAt    int64                    `json:"updatedAt"`
}

func (a *App) heartbeatConversionPath() string {
	dir := config.MemoryUserDir()
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "heartbeat-conversions.json")
}

func loadHeartbeatConversions(path string) (map[string]heartbeatConversionReceipt, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]heartbeatConversionReceipt{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("heartbeat: read conversions: %w", err)
	}
	var items map[string]heartbeatConversionReceipt
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("heartbeat: parse conversions: %w", err)
	}
	if items == nil {
		items = map[string]heartbeatConversionReceipt{}
	}
	// A corrupt journal must fail closed: never downgrade a broken receipt to
	// "convertible", which could create a duplicate assistant.
	for key, receipt := range items {
		if key != receipt.TaskID {
			return nil, fmt.Errorf("heartbeat: corrupt conversion receipt: map key %q does not match task %q", key, receipt.TaskID)
		}
		if receipt.Fingerprint == "" {
			return nil, fmt.Errorf("heartbeat: corrupt conversion receipt for %q: missing fingerprint", receipt.TaskID)
		}
		if receipt.AssistantID != assistant.StableID("assistant", "heartbeat:"+receipt.TaskID) {
			return nil, fmt.Errorf("heartbeat: corrupt conversion receipt for %q: non-deterministic assistant id", receipt.TaskID)
		}
		if receipt.RoutineID != assistant.StableID("routine", "heartbeat:"+receipt.TaskID+":routine") {
			return nil, fmt.Errorf("heartbeat: corrupt conversion receipt for %q: non-deterministic routine id", receipt.TaskID)
		}
		switch receipt.State {
		case HeartbeatConversionCreated, HeartbeatConversionDisabled, HeartbeatConversionActivated:
		default:
			return nil, fmt.Errorf("heartbeat: corrupt conversion receipt for %q: invalid state %q", receipt.TaskID, receipt.State)
		}
	}
	return items, nil
}

func saveHeartbeatConversions(path string, items map[string]heartbeatConversionReceipt) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("heartbeat: encode conversions: %w", err)
	}
	data = append(data, '\n')
	if err := fileutil.AtomicWriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("heartbeat: commit conversions: %w", err)
	}
	return nil
}

// ── Mapping ────────────────────────────────────────────────────────────────

var errHeartbeatScheduleUnmappable = errors.New("heartbeat schedule cannot be mapped losslessly")

func heartbeatConvertRequestID(taskID string) string {
	return "heartbeat-convert:" + taskID
}

func heartbeatActivateRequestID(taskID string) string {
	return "heartbeat-convert-activate:" + taskID
}

func heartbeatLocalTimezone() string {
	if name := time.Local.String(); name != "" {
		return name
	}
	return "Local"
}

func heartbeatTaskFingerprint(task HeartbeatTask, now time.Time) (string, error) {
	schedule, err := mapHeartbeatSchedule(task, now)
	if err != nil {
		return "", err
	}
	return heartbeatFingerprint(task, schedule, normalizeHeartbeatApprovalMode(task.ApprovalMode))
}

// mapHeartbeatTask derives the deterministic Assistant + Routine from a task.
// The returned fingerprint covers the source payload so any later edit to the
// task produces a typed conflict instead of a duplicate assistant.
func mapHeartbeatTask(task HeartbeatTask, now time.Time) (assistant.Assistant, assistant.Routine, string, error) {
	schedule, err := mapHeartbeatSchedule(task, now)
	if err != nil {
		return assistant.Assistant{}, assistant.Routine{}, "", err
	}

	mode := normalizeHeartbeatApprovalMode(task.ApprovalMode)
	scope := assistant.ScopeGlobal
	if strings.TrimSpace(task.Scope) == "project" {
		scope = assistant.ScopeWorkspace
	}
	workspaceRoot := strings.TrimSpace(task.WorkspaceRoot)
	if scope == assistant.ScopeWorkspace && workspaceRoot == "" {
		return assistant.Assistant{}, assistant.Routine{}, "", fmt.Errorf("%w: project scope requires a workspace root", errHeartbeatScheduleUnmappable)
	}

	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = "Heartbeat 定时任务"
	}
	prompt := strings.TrimSpace(task.Prompt)
	if prompt == "" {
		prompt = "继续执行该定时任务并汇报结果。"
	}

	assistantID := assistant.StableID("assistant", "heartbeat:"+task.ID)
	routineID := assistant.StableID("routine", "heartbeat:"+task.ID+":routine")

	a := assistant.Assistant{
		ID:            assistantID,
		Name:          title,
		Description:   "从 Heartbeat 任务转换而来",
		Mission:       "按计划执行定时任务：" + title,
		Scope:         scope,
		WorkspaceRoot: workspaceRoot,
		Lifecycle:     assistant.LifecyclePaused,
		Policy:        heartbeatPolicyForMode(mode),
	}
	// The Routine stays enabled regardless of the source task's enabled flag:
	// a disabled source only pauses the Assistant, so resuming the Assistant
	// makes the Routine schedulable again.
	r := assistant.Routine{
		ID:          routineID,
		AssistantID: assistantID,
		Title:       title,
		Prompt:      prompt,
		Schedule:    schedule,
		Enabled:     true,
		CatchUp:     assistant.CatchUpCoalesceLatest,
	}

	fingerprint, err := heartbeatFingerprint(task, schedule, mode)
	if err != nil {
		return assistant.Assistant{}, assistant.Routine{}, "", err
	}
	return a, r, fingerprint, nil
}

func heartbeatPolicyForMode(mode string) assistant.Policy {
	policy := assistant.DefaultPolicy()
	switch mode {
	case "ask":
		policy.LocalWrite = assistant.AccessApprove
		policy.Network = assistant.AccessApprove
	case "auto", "yolo":
		policy.LocalWrite = assistant.AccessAllow
		policy.Network = assistant.AccessAllow
	}
	// Publish/Delete/Payment/Secrets/Private stay AccessApprove from
	// DefaultPolicy: external publishing always keeps per-action approval even
	// for yolo/auto heartbeats.
	return policy
}

func heartbeatFingerprint(task HeartbeatTask, schedule assistant.Schedule, mode string) (string, error) {
	// Enabled is deliberately excluded: the conversion itself disables the old
	// task, so the flag cannot be an identity signal. Re-enabling after a
	// completed conversion is detected explicitly as a conflict instead.
	payload := struct {
		Title         string             `json:"title"`
		Prompt        string             `json:"prompt"`
		Scope         string             `json:"scope"`
		WorkspaceRoot string             `json:"workspace_root"`
		Schedule      assistant.Schedule `json:"schedule"`
		ApprovalMode  string             `json:"approval_mode"`
	}{
		Title: task.Title, Prompt: task.Prompt,
		Scope: task.Scope, WorkspaceRoot: task.WorkspaceRoot,
		Schedule: schedule, ApprovalMode: mode,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("heartbeat: fingerprint payload: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func mapHeartbeatSchedule(task HeartbeatTask, now time.Time) (assistant.Schedule, error) {
	window, err := heartbeatScheduleWindow(task)
	if err != nil {
		return assistant.Schedule{}, err
	}

	if idx := strings.Index(task.Interval, "|"); idx >= 0 {
		s, ok := parseHeartbeatSchedule(task.Interval)
		if !ok {
			return assistant.Schedule{}, fmt.Errorf("%w: interval %q", errHeartbeatScheduleUnmappable, task.Interval)
		}
		return mapHeartbeatCycleSchedule(s, task, now, window)
	}

	d, err := parseInterval(task.Interval)
	if err != nil || d <= 0 {
		return assistant.Schedule{}, fmt.Errorf("%w: interval %q", errHeartbeatScheduleUnmappable, task.Interval)
	}
	schedule := assistant.Schedule{Kind: assistant.ScheduleInterval, IntervalSeconds: int64(d.Seconds())}
	if window.start != "" {
		schedule.Timezone = heartbeatLocalTimezone()
		schedule.Window = assistant.TimeWindow{Start: window.start, End: window.end}
	}
	return schedule, nil
}

type heartbeatWindow struct {
	start string
	end   string
}

func heartbeatScheduleWindow(task HeartbeatTask) (heartbeatWindow, error) {
	start := strings.TrimSpace(task.TimeWindowStart)
	end := strings.TrimSpace(task.TimeWindowEnd)
	if start == "" && end == "" {
		return heartbeatWindow{}, nil
	}
	if start == "" || end == "" {
		return heartbeatWindow{}, fmt.Errorf("%w: time window requires both start and end", errHeartbeatScheduleUnmappable)
	}
	if _, _, ok := parseHeartbeatClock(start); !ok {
		return heartbeatWindow{}, fmt.Errorf("%w: invalid time window start %q", errHeartbeatScheduleUnmappable, start)
	}
	if _, _, ok := parseHeartbeatClock(end); !ok {
		return heartbeatWindow{}, fmt.Errorf("%w: invalid time window end %q", errHeartbeatScheduleUnmappable, end)
	}
	return heartbeatWindow{start: start, end: end}, nil
}

func mapHeartbeatCycleSchedule(s heartbeatSchedule, task HeartbeatTask, now time.Time, window heartbeatWindow) (assistant.Schedule, error) {
	tz := heartbeatLocalTimezone()
	at := fmt.Sprintf("%02d:%02d", s.hour, s.minute)
	// Heartbeat ignores a time window for cycle schedules; assistant validation
	// would also reject a window whose clock time is outside it, so we drop it
	// exactly like the legacy engine does.
	_ = window
	switch s.kind {
	case "daily":
		return assistant.Schedule{Kind: assistant.ScheduleDaily, Timezone: tz, At: at}, nil
	case "weekly", "biweekly":
		if len(s.days) != 1 {
			return assistant.Schedule{}, fmt.Errorf("%w: %s needs exactly one weekday", errHeartbeatScheduleUnmappable, s.kind)
		}
		schedule := assistant.Schedule{Kind: assistant.ScheduleWeekly, Timezone: tz, At: at, Weekday: s.days[0]}
		if s.kind == "biweekly" {
			schedule.Kind = assistant.ScheduleBiweekly
			schedule.StartAt = heartbeatScheduleAnchorTime(task, now)
		}
		return schedule, nil
	case "monthly":
		if s.day < 1 || s.day > 31 {
			return assistant.Schedule{}, fmt.Errorf("%w: monthly day %d", errHeartbeatScheduleUnmappable, s.day)
		}
		return assistant.Schedule{Kind: assistant.ScheduleMonthly, Timezone: tz, At: at, Day: s.day}, nil
	case "yearly":
		if s.month < 1 || s.month > 12 || s.day < 1 || s.day > 31 {
			return assistant.Schedule{}, fmt.Errorf("%w: yearly %d-%d", errHeartbeatScheduleUnmappable, s.month, s.day)
		}
		return assistant.Schedule{Kind: assistant.ScheduleYearly, Timezone: tz, At: at, Month: time.Month(s.month), Day: s.day}, nil
	default:
		return assistant.Schedule{}, fmt.Errorf("%w: %q", errHeartbeatScheduleUnmappable, s.kind)
	}
}

func heartbeatScheduleAnchorTime(task HeartbeatTask, now time.Time) time.Time {
	if task.CreatedAt != 0 {
		return time.UnixMilli(task.CreatedAt)
	}
	if task.LastRunAt != 0 {
		return time.UnixMilli(task.LastRunAt)
	}
	// No timestamp at all: derive a stable anchor from the task ID so the
	// biweekly parity (and therefore the fingerprint) is identical across
	// consecutive conversions and restarts.
	sum := sha256.Sum256([]byte("heartbeat-biweekly-anchor:" + task.ID))
	offset := binary.BigEndian.Uint64(sum[:8]) % (10 * 365 * 24 * 3600)
	return time.Unix(int64(offset), 0).UTC()
}

// ── Conversion ─────────────────────────────────────────────────────────────

// HeartbeatListConversions returns the conversion status of every heartbeat
// task without mutating either store. A corrupt conversion journal is reported
// as an error instead of being silently downgraded to "convertible".
func (a *App) HeartbeatListConversions() ([]HeartbeatConversionStatus, error) {
	a.conversionMu.Lock()
	defer a.conversionMu.Unlock()
	statuses := []HeartbeatConversionStatus{}
	if a.heartbeat == nil {
		return statuses, nil
	}
	tasks := a.heartbeat.ListTasks()
	receipts, err := loadHeartbeatConversions(a.heartbeatConversionPath())
	if err != nil {
		return nil, err
	}
	service, serviceErr := a.assistantRuntime()
	now := time.Now()
	for _, task := range tasks {
		status := HeartbeatConversionStatus{
			TaskID:       task.ID,
			ApprovalMode: normalizeHeartbeatApprovalMode(task.ApprovalMode),
		}
		_, _, fingerprint, err := mapHeartbeatTask(task, now)
		if err != nil {
			status.State = HeartbeatConvUnmappable
			status.Reason = err.Error()
			statuses = append(statuses, status)
			continue
		}
		receipt, exists := receipts[task.ID]
		switch {
		case !exists:
			status.State = HeartbeatConvConvertible
		case receipt.Fingerprint != fingerprint:
			status.State = HeartbeatConvConflict
			status.AssistantID = receipt.AssistantID
			status.Reason = "Heartbeat 任务内容已变更，无法复用已转换的助理"
		case receipt.State == HeartbeatConversionActivated && task.Enabled:
			status.State = HeartbeatConvConflict
			status.AssistantID = receipt.AssistantID
			status.Reason = "旧 Heartbeat 任务已被重新启用"
		case receipt.State == HeartbeatConversionActivated:
			status.State = HeartbeatConvConverted
			status.AssistantID = receipt.AssistantID
			if serviceErr == nil {
				if snap, gerr := service.store.Get(receipt.AssistantID); gerr == nil {
					status.AssistantName = snap.Assistant.Name
				}
			}
		default:
			status.State = HeartbeatConvInProgress
			status.AssistantID = receipt.AssistantID
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// HeartbeatConvertToAssistant converts one task (or resumes a partially
// completed conversion) and returns its typed result.
func (a *App) HeartbeatConvertToAssistant(taskID string) (HeartbeatConversionResult, error) {
	a.conversionMu.Lock()
	defer a.conversionMu.Unlock()
	return a.convertHeartbeatTask(taskID)
}

func (a *App) convertHeartbeatTask(taskID string) (HeartbeatConversionResult, error) {
	if a.heartbeat == nil {
		return HeartbeatConversionResult{}, errors.New("heartbeat runtime is not started")
	}
	service, err := a.assistantRuntime()
	if err != nil {
		return HeartbeatConversionResult{}, err
	}

	task, ok := a.heartbeat.TaskByID(taskID)
	if !ok {
		return HeartbeatConversionResult{}, fmt.Errorf("heartbeat task %q not found", taskID)
	}

	now := time.Now()
	mappedAssistant, mappedRoutine, fingerprint, err := mapHeartbeatTask(task, now)
	if err != nil {
		if errors.Is(err, errHeartbeatScheduleUnmappable) {
			return HeartbeatConversionResult{
				TaskID:       task.ID,
				State:        HeartbeatConvUnmappable,
				Reason:       err.Error(),
				ApprovalMode: normalizeHeartbeatApprovalMode(task.ApprovalMode),
			}, nil
		}
		return HeartbeatConversionResult{}, err
	}

	path := a.heartbeatConversionPath()
	receipts, err := loadHeartbeatConversions(path)
	if err != nil {
		return HeartbeatConversionResult{}, err
	}
	// The conversion disables the old task, so on resume the original enabled
	// flag must come from the journal, not the (already mutated) task.
	enabled := task.Enabled
	receipt, exists := receipts[task.ID]
	if exists {
		if receipt.Fingerprint != fingerprint {
			return HeartbeatConversionResult{
				TaskID:       task.ID,
				State:        HeartbeatConvConflict,
				AssistantID:  receipt.AssistantID,
				Reason:       "Heartbeat 任务内容已变更，无法复用已转换的助理；请先处理旧任务再重新转换。",
				ApprovalMode: normalizeHeartbeatApprovalMode(task.ApprovalMode),
			}, nil
		}
		if receipt.State == HeartbeatConversionActivated {
			if task.Enabled {
				return HeartbeatConversionResult{
					TaskID:       task.ID,
					State:        HeartbeatConvConflict,
					AssistantID:  receipt.AssistantID,
					Reason:       "旧 Heartbeat 任务已被重新启用，与新助理同时运行会产生重复执行；请先停用旧任务。",
					ApprovalMode: normalizeHeartbeatApprovalMode(task.ApprovalMode),
				}, nil
			}
			return a.heartbeatConversionResultFor(receipt)
		}
		enabled = receipt.Enabled
	}

	if !exists {
		// Record the conversion intent first, before creating anything. A crash
		// between this write and the Create below is recovered by replaying the
		// deterministic Create on the next call.
		receipt = heartbeatConversionReceipt{
			TaskID:       task.ID,
			Fingerprint:  fingerprint,
			AssistantID:  mappedAssistant.ID,
			RoutineID:    mappedRoutine.ID,
			State:        HeartbeatConversionCreated,
			Enabled:      enabled,
			ApprovalMode: normalizeHeartbeatApprovalMode(task.ApprovalMode),
			UpdatedAt:    now.UnixMilli(),
		}
		receipts[task.ID] = receipt
		if err := saveHeartbeatConversions(path, receipts); err != nil {
			return HeartbeatConversionResult{}, err
		}
	}

	// Create the paused assistant. The deterministic request ID makes this a
	// safe replay: a second call with the same task returns the same aggregate
	// via its own receipt. Any other Create conflict means a different aggregate
	// or payload already owns the deterministic ID — surface it, do NOT disable
	// the old task.
	if _, err := service.store.Create(assistant.CreateInput{
		RequestID: heartbeatConvertRequestID(task.ID),
		Assistant: mappedAssistant,
		Routines:  []assistant.Routine{mappedRoutine},
		Now:       now,
	}); err != nil {
		if errors.Is(err, assistant.ErrConflict) || errors.Is(err, assistant.ErrIdempotency) {
			return HeartbeatConversionResult{
				TaskID:       task.ID,
				State:        HeartbeatConvConflict,
				AssistantID:  mappedAssistant.ID,
				Reason:       fmt.Sprintf("无法创建转换后的助理：%v", err),
				ApprovalMode: normalizeHeartbeatApprovalMode(task.ApprovalMode),
			}, nil
		}
		return HeartbeatConversionResult{}, err
	}

	// Disable the old task before the assistant can run. Until this point the
	// assistant is paused, so old heartbeat and new assistant never overlap.
	// Always persist the disable (idempotent) so a stale in-memory enabled flag
	// can never let the old task keep running next to the new assistant.
	if err := a.heartbeat.SetTaskEnabled(task.ID, false); err != nil {
		return HeartbeatConversionResult{}, err
	}
	receipt.State = HeartbeatConversionDisabled
	receipt.UpdatedAt = time.Now().UnixMilli()
	receipts[task.ID] = receipt
	if err := saveHeartbeatConversions(path, receipts); err != nil {
		return HeartbeatConversionResult{}, err
	}

	// Finally mirror the old enabled state onto the assistant lifecycle.
	desired := assistant.LifecyclePaused
	if receipt.Enabled {
		desired = assistant.LifecycleActive
	}
	current, err := service.store.Get(receipt.AssistantID)
	if err != nil {
		return HeartbeatConversionResult{}, err
	}
	if current.Assistant.Lifecycle != desired {
		updated := current.Assistant
		updated.Lifecycle = desired
		if _, err := service.store.UpdateAssistant(heartbeatActivateRequestID(task.ID), updated, current.Assistant.Revision, time.Now()); err != nil {
			return HeartbeatConversionResult{}, err
		}
	}
	receipt.State = HeartbeatConversionActivated
	receipt.UpdatedAt = time.Now().UnixMilli()
	receipts[task.ID] = receipt
	if err := saveHeartbeatConversions(path, receipts); err != nil {
		return HeartbeatConversionResult{}, err
	}

	return a.heartbeatConversionResultFor(receipt)
}

func (a *App) heartbeatConversionResultFor(receipt heartbeatConversionReceipt) (HeartbeatConversionResult, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return HeartbeatConversionResult{}, err
	}
	snapshot, err := service.store.Get(receipt.AssistantID)
	if err != nil {
		return HeartbeatConversionResult{}, err
	}
	return HeartbeatConversionResult{
		TaskID:        receipt.TaskID,
		State:         HeartbeatConvConverted,
		AssistantID:   receipt.AssistantID,
		AssistantName: snapshot.Assistant.Name,
		Assistant:     snapshot.Assistant,
		ApprovalMode:  receipt.ApprovalMode,
	}, nil
}
