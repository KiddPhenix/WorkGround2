package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/assistant"
	"workground2/internal/config"
)

func newHeartbeatConversionFixture(t *testing.T, tasks []HeartbeatTask) *App {
	t.Helper()
	isolateDesktopUserDirs(t)
	app := NewApp()
	engine := newHeartbeatEngine(app)
	app.heartbeat = engine
	if err := engine.ReplaceTasks(tasks); err != nil {
		t.Fatalf("ReplaceTasks: %v", err)
	}
	root := filepath.Join(config.MemoryUserDir(), "assistants")
	store, err := assistant.NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	app.assistant = &AssistantRuntime{store: store, inflight: map[string]*assistantInFlight{}, byRun: map[string]*assistantInFlight{}}
	return app
}

func heartbeatConversionFingerprintForTest(t *testing.T, task HeartbeatTask) string {
	t.Helper()
	_, _, fingerprint, err := mapHeartbeatTask(task, time.Now())
	if err != nil {
		t.Fatalf("mapHeartbeatTask: %v", err)
	}
	return fingerprint
}

func seedHeartbeatReceipt(t *testing.T, app *App, task HeartbeatTask, state HeartbeatConversionState) heartbeatConversionReceipt {
	t.Helper()
	assistantID := assistant.StableID("assistant", "heartbeat:"+task.ID)
	receipt := heartbeatConversionReceipt{
		TaskID:       task.ID,
		Fingerprint:  heartbeatConversionFingerprintForTest(t, task),
		AssistantID:  assistantID,
		RoutineID:    assistant.StableID("routine", "heartbeat:"+task.ID+":routine"),
		State:        state,
		Enabled:      task.Enabled,
		ApprovalMode: normalizeHeartbeatApprovalMode(task.ApprovalMode),
		UpdatedAt:    time.Now().UnixMilli(),
	}
	receipts, err := loadHeartbeatConversions(app.heartbeatConversionPath())
	if err != nil {
		t.Fatalf("load receipts: %v", err)
	}
	receipts[task.ID] = receipt
	if err := saveHeartbeatConversions(app.heartbeatConversionPath(), receipts); err != nil {
		t.Fatalf("save receipts: %v", err)
	}
	return receipt
}

func TestHeartbeatConvertIsIdempotentAndDisablesOldTask(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "每日检查", Prompt: "总结状态", Interval: "1h", Enabled: true, Scope: "global", ApprovalMode: "auto",
	}})

	first, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if first.State != HeartbeatConvConverted || first.AssistantID == "" {
		t.Fatalf("first = %+v", first)
	}
	if first.Assistant.Lifecycle != assistant.LifecycleActive {
		t.Fatalf("assistant lifecycle = %q, want active", first.Assistant.Lifecycle)
	}
	if first.Assistant.Scope != assistant.ScopeGlobal {
		t.Fatalf("assistant scope = %q, want global", first.Assistant.Scope)
	}

	second, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.State != HeartbeatConvConverted || second.AssistantID != first.AssistantID {
		t.Fatalf("replay drifted: %+v vs %+v", second, first)
	}

	task, ok := app.heartbeat.TaskByID("task-1")
	if !ok || task.Enabled {
		t.Fatalf("old task was not disabled: %+v", task)
	}

	snapshot, err := app.assistant.store.Get(first.AssistantID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(snapshot.Routines) != 1 || snapshot.Routines[0].Prompt != "总结状态" {
		t.Fatalf("routine mapping lost: %+v", snapshot.Routines)
	}
	if snapshot.Assistant.Policy.LocalWrite != assistant.AccessAllow || snapshot.Assistant.Policy.Publish != assistant.AccessApprove {
		t.Fatalf("auto policy = %+v, want local allow + publish approve", snapshot.Assistant.Policy)
	}

	// The journal is the source of truth; it should now be activated.
	receipts, err := loadHeartbeatConversions(app.heartbeatConversionPath())
	if err != nil {
		t.Fatalf("load receipts: %v", err)
	}
	if receipts["task-1"].State != HeartbeatConversionActivated {
		t.Fatalf("receipt state = %q, want activated", receipts["task-1"].State)
	}
}

func TestHeartbeatConvertChangedTaskReturnsConflictWithoutDuplicate(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "每日检查", Prompt: "总结状态", Interval: "1h", Enabled: true,
	}})

	first, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if err := app.heartbeat.ReplaceTasks([]HeartbeatTask{{
		ID: "task-1", Title: "每日检查", Prompt: "改了 prompt", Interval: "1h", Enabled: true,
	}}); err != nil {
		t.Fatalf("edit task: %v", err)
	}

	conflict, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("conflict convert: %v", err)
	}
	if conflict.State != HeartbeatConvConflict || conflict.AssistantID != first.AssistantID {
		t.Fatalf("conflict = %+v", conflict)
	}

	list, err := app.assistant.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("assistant count = %d, want no duplicate", len(list))
	}

	statuses, err := app.HeartbeatListConversions()
	if err != nil {
		t.Fatalf("HeartbeatListConversions: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != HeartbeatConvConflict {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestHeartbeatConvertReadsReceiptAfterRestart(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "检查", Prompt: "跑", Interval: "2h", Enabled: true,
	}})
	first, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	// Simulate a restart: a fresh engine + fresh assistant runtime over the same dirs.
	restarted := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "检查", Prompt: "跑", Interval: "2h", Enabled: false,
	}})
	second, err := restarted.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if second.State != HeartbeatConvConverted || second.AssistantID != first.AssistantID {
		t.Fatalf("restart replay drifted: %+v vs %+v", second, first)
	}
	list, err := restarted.assistant.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("restart created a duplicate: %d assistants", len(list))
	}
}

func TestHeartbeatConvertResumesFromCreatedReceiptBeforeAssistant(t *testing.T) {
	task := HeartbeatTask{ID: "task-1", Title: "检查", Prompt: "跑", Interval: "2h", Enabled: true}
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{task})
	// Crash window 1: receipt written, assistant not yet created.
	seedHeartbeatReceipt(t, app, task, HeartbeatConversionCreated)

	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != HeartbeatConvConverted {
		t.Fatalf("resume state = %q", result.State)
	}
	if _, err := app.assistant.store.Get(result.AssistantID); err != nil {
		t.Fatalf("assistant was not created on resume: %v", err)
	}
	taskNow, ok := app.heartbeat.TaskByID("task-1")
	if !ok || taskNow.Enabled {
		t.Fatalf("old task not disabled on resume: %+v", taskNow)
	}
}

func TestHeartbeatConvertResumesFromDisabledReceiptBeforeActivation(t *testing.T) {
	task := HeartbeatTask{ID: "task-1", Title: "检查", Prompt: "跑", Interval: "2h", Enabled: true}
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{task})
	// Create the paused assistant and disable the old task to reach the
	// disabled-but-not-activated crash window.
	a, r, _, err := mapHeartbeatTask(task, time.Now())
	if err != nil {
		t.Fatalf("mapHeartbeatTask: %v", err)
	}
	if _, err := app.assistant.store.Create(assistant.CreateInput{
		RequestID: heartbeatConvertRequestID(task.ID), Assistant: a, Routines: []assistant.Routine{r}, Now: time.Now(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := app.heartbeat.SetTaskEnabled(task.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	seedHeartbeatReceipt(t, app, task, HeartbeatConversionDisabled)

	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != HeartbeatConvConverted || result.Assistant.Lifecycle != assistant.LifecycleActive {
		t.Fatalf("resume did not activate: %+v", result)
	}
}

func TestHeartbeatConvertDisabledTaskStaysPaused(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "停用任务", Prompt: "跑", Interval: "1h", Enabled: false,
	}})
	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if result.State != HeartbeatConvConverted || result.Assistant.Lifecycle != assistant.LifecyclePaused {
		t.Fatalf("disabled task assistant should stay paused: %+v", result)
	}
}

func TestHeartbeatConvertUnmappableScheduleReturnsExplicitState(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "每周多天", Prompt: "跑", Interval: "168h|weekly:mon,fri@09:00", Enabled: true,
	}})
	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("convert should return typed result, not error: %v", err)
	}
	if result.State != HeartbeatConvUnmappable || result.Reason == "" {
		t.Fatalf("unmappable = %+v", result)
	}
	if list, _ := app.assistant.store.List(); len(list) != 0 {
		t.Fatalf("unmappable task should not create an assistant")
	}
	statuses, err := app.HeartbeatListConversions()
	if err != nil {
		t.Fatalf("HeartbeatListConversions: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != HeartbeatConvUnmappable {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestHeartbeatConvertRejectsStartOnlyWindow(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "窗口", Prompt: "跑", Interval: "30m", Enabled: true, TimeWindowStart: "09:00",
	}})
	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if result.State != HeartbeatConvUnmappable {
		t.Fatalf("start-only window should be unmappable: %+v", result)
	}
}

func TestHeartbeatScheduleMapping(t *testing.T) {
	now := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		task     HeartbeatTask
		wantKind assistant.ScheduleKind
	}{
		{"interval", HeartbeatTask{Interval: "45m", Enabled: true}, assistant.ScheduleInterval},
		{"daily", HeartbeatTask{Interval: "24h|daily@09:30", Enabled: true}, assistant.ScheduleDaily},
		{"weekly", HeartbeatTask{Interval: "168h|weekly:fri@09:00", Enabled: true}, assistant.ScheduleWeekly},
		{"biweekly", HeartbeatTask{Interval: "336h|biweekly:mon@09:00", Enabled: true, CreatedAt: now.UnixMilli()}, assistant.ScheduleBiweekly},
		{"monthly", HeartbeatTask{Interval: "720h|monthly:15@09:00", Enabled: true}, assistant.ScheduleMonthly},
		{"yearly", HeartbeatTask{Interval: "8760h|yearly:2-28@09:00", Enabled: true}, assistant.ScheduleYearly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schedule, err := mapHeartbeatSchedule(tc.task, now)
			if err != nil {
				t.Fatalf("mapHeartbeatSchedule: %v", err)
			}
			if schedule.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", schedule.Kind, tc.wantKind)
			}
		})
	}
}

func TestHeartbeatApprovalModePolicyMapping(t *testing.T) {
	cases := []struct {
		mode        string
		wantLocal   assistant.Access
		wantNetwork assistant.Access
	}{
		{"ask", assistant.AccessApprove, assistant.AccessApprove},
		{"auto", assistant.AccessAllow, assistant.AccessAllow},
		{"yolo", assistant.AccessAllow, assistant.AccessAllow},
		{"", assistant.AccessAllow, assistant.AccessAllow}, // empty defaults to yolo
	}
	for _, tc := range cases {
		policy := heartbeatPolicyForMode(normalizeHeartbeatApprovalMode(tc.mode))
		if policy.LocalWrite != tc.wantLocal || policy.Network != tc.wantNetwork {
			t.Fatalf("mode %q: policy = %+v", tc.mode, policy)
		}
		if policy.Publish != assistant.AccessApprove || policy.Delete != assistant.AccessApprove {
			t.Fatalf("mode %q: high-risk boundary must stay approve: %+v", tc.mode, policy)
		}
	}
}

func TestHeartbeatConvertProjectScopeRequiresWorkspace(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "项目任务", Prompt: "跑", Interval: "1h", Enabled: true, Scope: "project",
	}})
	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if result.State != HeartbeatConvUnmappable {
		t.Fatalf("project scope without workspace should be unmappable: %+v", result)
	}
}

func TestHeartbeatConvertDisabledTaskKeepsRoutineEnabled(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "停用任务", Prompt: "跑", Interval: "1h", Enabled: false,
	}})
	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if result.Assistant.Lifecycle != assistant.LifecyclePaused {
		t.Fatalf("disabled source assistant should be paused: %+v", result.Assistant)
	}
	snapshot, err := app.assistant.store.Get(result.AssistantID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(snapshot.Routines) != 1 || !snapshot.Routines[0].Enabled {
		t.Fatalf("routine should stay enabled so resuming the assistant makes it schedulable: %+v", snapshot.Routines)
	}
}

func TestHeartbeatBiweeklyZeroAnchorIsDeterministic(t *testing.T) {
	task := HeartbeatTask{ID: "task-1", Title: "双周", Prompt: "跑", Interval: "336h|biweekly:mon@09:00", Enabled: true}
	_, _, first, err := mapHeartbeatTask(task, time.Now())
	if err != nil {
		t.Fatalf("mapHeartbeatTask: %v", err)
	}
	_, _, second, err := mapHeartbeatTask(task, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("mapHeartbeatTask replay: %v", err)
	}
	if first != second {
		t.Fatalf("zero-anchor biweekly fingerprint drifted: %s vs %s", first, second)
	}
}

func TestHeartbeatListConversionsRejectsCorruptJournal(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "检查", Prompt: "跑", Interval: "1h", Enabled: true,
	}})
	if err := os.WriteFile(app.heartbeatConversionPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt journal: %v", err)
	}
	statuses, err := app.HeartbeatListConversions()
	if err == nil {
		t.Fatalf("corrupt journal should return an error, got %+v", statuses)
	}
}

func TestHeartbeatListConversionsRejectsBadReceipt(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "检查", Prompt: "跑", Interval: "1h", Enabled: true,
	}})
	bad := map[string]heartbeatConversionReceipt{
		"task-1": {
			TaskID: "task-1", Fingerprint: "abc",
			AssistantID: "wrong-assistant", RoutineID: "wrong-routine",
			State: HeartbeatConversionActivated, Enabled: true,
		},
	}
	if err := saveHeartbeatConversions(app.heartbeatConversionPath(), bad); err != nil {
		t.Fatalf("write bad receipt: %v", err)
	}
	statuses, err := app.HeartbeatListConversions()
	if err == nil {
		t.Fatalf("bad receipt should return an error, got %+v", statuses)
	}
}

func TestHeartbeatSaveTasksRejectsConvertedTaskEdits(t *testing.T) {
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{{
		ID: "task-1", Title: "检查", Prompt: "跑", Interval: "1h", Enabled: true,
	}})
	if _, err := app.HeartbeatConvertToAssistant("task-1"); err != nil {
		t.Fatalf("convert: %v", err)
	}

	// Content edit must be rejected.
	if err := app.HeartbeatSaveTasks([]HeartbeatTask{{ID: "task-1", Title: "检查", Prompt: "改了", Interval: "1h", Enabled: false}}); err == nil {
		t.Fatal("editing a converted task's fingerprint must be rejected")
	}
	// Re-enabling must be rejected.
	if err := app.HeartbeatSaveTasks([]HeartbeatTask{{ID: "task-1", Title: "检查", Prompt: "跑", Interval: "1h", Enabled: true}}); err == nil {
		t.Fatal("re-enabling a converted task must be rejected")
	}
	// Unchanged + disabled must be accepted.
	if err := app.HeartbeatSaveTasks([]HeartbeatTask{{ID: "task-1", Title: "检查", Prompt: "跑", Interval: "1h", Enabled: false}}); err != nil {
		t.Fatalf("unchanged converted task should save: %v", err)
	}
}

func TestHeartbeatSaveFailureKeepsInMemoryState(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	engine := newHeartbeatEngine(app)
	app.heartbeat = engine
	if err := engine.ReplaceTasks([]HeartbeatTask{{ID: "t1", Title: "检查", Prompt: "跑", Interval: "1h", Enabled: true}}); err != nil {
		t.Fatalf("ReplaceTasks: %v", err)
	}
	// Replace the config file with a directory so the atomic rename fails.
	if err := os.Remove(engine.configPath()); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	if err := os.MkdirAll(engine.configPath(), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := engine.SetTaskEnabled("t1", false); err == nil {
		t.Fatal("expected save failure when config path is a directory")
	}
	task, ok := engine.TaskByID("t1")
	if !ok || !task.Enabled {
		t.Fatalf("failed save must keep the previous in-memory enabled state: %+v", task)
	}
}

func TestHeartbeatConvertResumesFromDisableWindow(t *testing.T) {
	task := HeartbeatTask{ID: "task-1", Title: "检查", Prompt: "跑", Interval: "2h", Enabled: true}
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{task})
	// Crash window: assistant created, task disabled, receipt still "created".
	a, r, _, err := mapHeartbeatTask(task, time.Now())
	if err != nil {
		t.Fatalf("mapHeartbeatTask: %v", err)
	}
	if _, err := app.assistant.store.Create(assistant.CreateInput{
		RequestID: heartbeatConvertRequestID(task.ID), Assistant: a, Routines: []assistant.Routine{r}, Now: time.Now(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := app.heartbeat.SetTaskEnabled(task.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	seedHeartbeatReceipt(t, app, task, HeartbeatConversionCreated)

	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != HeartbeatConvConverted || result.Assistant.Lifecycle != assistant.LifecycleActive {
		t.Fatalf("resume from disable window: %+v", result)
	}
}

func TestHeartbeatConvertResumesFromFinalReceiptWindow(t *testing.T) {
	task := HeartbeatTask{ID: "task-1", Title: "检查", Prompt: "跑", Interval: "2h", Enabled: true}
	app := newHeartbeatConversionFixture(t, []HeartbeatTask{task})
	// Crash window: assistant already activated, receipt still "disabled".
	if _, err := app.HeartbeatConvertToAssistant("task-1"); err != nil {
		t.Fatalf("convert: %v", err)
	}
	receipts, err := loadHeartbeatConversions(app.heartbeatConversionPath())
	if err != nil {
		t.Fatalf("load receipts: %v", err)
	}
	receipt := receipts["task-1"]
	receipt.State = HeartbeatConversionDisabled
	receipts["task-1"] = receipt
	if err := saveHeartbeatConversions(app.heartbeatConversionPath(), receipts); err != nil {
		t.Fatalf("rewind receipt: %v", err)
	}

	result, err := app.HeartbeatConvertToAssistant("task-1")
	if err != nil {
		t.Fatalf("resume final window: %v", err)
	}
	if result.State != HeartbeatConvConverted || result.Assistant.Lifecycle != assistant.LifecycleActive {
		t.Fatalf("resume from final receipt window: %+v", result)
	}
}
