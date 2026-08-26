package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/control"
	"workground2/internal/decision"
	"workground2/internal/event"
	"workground2/internal/permission"
)

type assistantControllerStub struct {
	mu     sync.Mutex
	mode   string
	policy permission.Policy
}

func (c *assistantControllerStub) SetPermissionPolicy(policy permission.Policy) {
	c.mu.Lock()
	c.policy = policy
	c.mu.Unlock()
}

func (c *assistantControllerStub) SetToolApprovalMode(mode string) {
	c.mu.Lock()
	c.mode = mode
	c.mu.Unlock()
}

type assistantHostStub struct {
	mu           sync.Mutex
	runtime      *AssistantRuntime
	store        *assistant.Store
	prepareErr   error
	submitErr    error
	busy         bool
	approval     *event.Approval
	ask          *event.Ask
	toolResults  []event.Tool
	block        <-chan struct{}
	cancelDone   bool
	prepared     assistant.Run
	session      assistantSession
	submitted    int
	prompts      []string
	grants       [][]control.ToolGrant
	boundAtSend  bool
	activeBefore string
	activeAfter  string
	controller   assistantControllerStub
	cancelled    int
}

func (h *assistantHostStub) PrepareSession(run assistant.Run) (assistantSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prepared = run
	h.activeAfter = h.activeBefore
	if h.prepareErr != nil {
		return assistantSession{}, h.prepareErr
	}
	if h.session.TabID == "" {
		h.session = assistantSession{TabID: "assistant-tab", SessionPath: "C:/assistant/session.jsonl"}
	}
	return h.session, nil
}

func (h *assistantHostStub) WaitReady(context.Context, string, time.Duration) (assistantController, error) {
	return &h.controller, nil
}

func (h *assistantHostStub) TrySubmit(tabID, prompt string, policy assistant.Policy, grants []control.ToolGrant, claim func() bool, release func()) (bool, error) {
	if !claim() {
		return false, nil
	}
	h.mu.Lock()
	h.submitted++
	h.prompts = append(h.prompts, prompt)
	h.grants = append(h.grants, append([]control.ToolGrant(nil), grants...))
	if h.store != nil {
		if snap, err := h.store.Get(h.prepared.AssistantID); err == nil {
			for _, run := range snap.Runs {
				if run.ID == h.prepared.ID && strings.TrimSpace(run.SessionPath) != "" {
					h.boundAtSend = true
				}
			}
		}
	}
	err := h.submitErr
	busy := h.busy
	runtime := h.runtime
	approval := h.approval
	ask := h.ask
	toolResults := append([]event.Tool(nil), h.toolResults...)
	block := h.block
	h.mu.Unlock()
	if err != nil || busy {
		release()
		return false, err
	}
	h.controller.SetToolApprovalMode(control.ToolApprovalAuto)
	h.controller.SetPermissionPolicy(buildAssistantPermissionPolicy(policy))
	if runtime != nil {
		go func() {
			if block != nil {
				<-block
			}
			if approval != nil {
				runtime.ObserveEvent(tabID, event.Event{Kind: event.ApprovalRequest, Approval: *approval})
				return
			}
			if ask != nil {
				runtime.ObserveEvent(tabID, event.Event{Kind: event.AskRequest, Ask: *ask})
				return
			}
			for _, result := range toolResults {
				runtime.ObserveEvent(tabID, event.Event{Kind: event.ToolResult, Tool: result})
			}
			runtime.ObserveEvent(tabID, event.Event{Kind: event.Message, Text: "Scan complete"})
			runtime.ObserveEvent(tabID, event.Event{Kind: event.TurnDone})
		}()
	}
	return true, nil
}

func (h *assistantHostStub) Cancel(string) {
	h.mu.Lock()
	h.cancelled++
	runtime := h.runtime
	tabID := h.session.TabID
	cancelDone := h.cancelDone
	h.mu.Unlock()
	if cancelDone && runtime != nil {
		runtime.ObserveEvent(tabID, event.Event{Kind: event.TurnDone})
	}
}

func newAssistantTestRuntime(t *testing.T, host *assistantHostStub) (*AssistantRuntime, *assistant.Store) {
	t.Helper()
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	scheduler, err := assistant.NewScheduler(store)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	runner, err := assistant.NewRunner(store, "desktop-test", 2*time.Second)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	service := &AssistantRuntime{
		store: store, scheduler: scheduler, runner: runner, host: host,
		inflight: map[string]*assistantInFlight{}, byRun: map[string]*assistantInFlight{}, tick: 10 * time.Millisecond,
	}
	host.runtime, host.store = service, store
	return service, store
}

func createAssistantRun(t *testing.T, store *assistant.Store, requestID string) (assistant.Snapshot, assistant.Run) {
	return createAssistantRunWithTask(t, store, requestID, "Keep the project healthy", "Scan now")
}

func createAssistantRunWithTask(t *testing.T, store *assistant.Store, requestID, mission, prompt string) (assistant.Snapshot, assistant.Run) {
	t.Helper()
	now := time.Now()
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-" + requestID,
		Assistant: assistant.Assistant{
			ID: "helper-" + requestID, Name: "Helper", Mission: mission,
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Routines: []assistant.Routine{{
			ID: "routine-" + requestID, Title: "Scan", Prompt: prompt, Enabled: true,
			CatchUp: assistant.CatchUpCoalesceLatest, Schedule: assistant.Schedule{Kind: assistant.ScheduleManual},
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	run, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RoutineID: snapshot.Routines[0].ID,
		RequestID: "run-" + requestID, Trigger: assistant.TriggerManual, Now: now,
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	return snapshot, run
}

func TestAssistantRunTopicTitleUsesFrozenUserIntent(t *testing.T) {
	tests := []struct {
		name string
		run  assistant.Run
		want string
	}{
		{name: "prompt", run: assistant.Run{Prompt: "调查最近的构建失败", Mission: "维护项目"}, want: "调查最近的构建失败"},
		{name: "mission fallback", run: assistant.Run{Mission: "持续整理发布反馈"}, want: "持续整理发布反馈"},
		{name: "empty", run: assistant.Run{}, want: defaultTopicTitle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assistantRunTopicTitle(tt.run); got != tt.want {
				t.Fatalf("assistantRunTopicTitle() = %q, want %q", got, tt.want)
			}
		})
	}
	if !isLegacyAssistantTopicTitle("你正在执行一个长期助手…") || isLegacyAssistantTopicTitle("调查最近的构建失败") {
		t.Fatal("legacy Assistant title classification drifted")
	}
}

func TestReconcileAssistantSessionTitleRepairsOnlyAutoLegacyTitle(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "assistant-session.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	meta.Scope = "project"
	meta.WorkspaceRoot = root
	meta.TopicID = "assistant-topic"
	meta.TopicTitle = "你正在执行一个长期助手…"
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = "assistant-readable"
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, meta); err != nil {
		t.Fatal(err)
	}
	if err := setTopicTitleWithSource(root, meta.TopicID, meta.TopicTitle, topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	run := assistant.Run{
		ID: "run-readable", AssistantID: meta.AssistantID, SessionPath: sessionPath,
		Scope: assistant.ScopeWorkspace, WorkspaceRoot: root, Prompt: "整理论坛热门帖子并附原文链接",
	}
	updated, err := app.reconcileAssistantSessionTitle(run)
	if err != nil || !updated {
		t.Fatalf("reconcileAssistantSessionTitle() updated=%v err=%v", updated, err)
	}
	if got := loadTopicTitle(root, meta.TopicID); got != "整理论坛热门帖子并附原文链接" {
		t.Fatalf("reconciled title = %q", got)
	}
	updated, err = app.reconcileAssistantSessionTitle(run)
	if err != nil || updated {
		t.Fatalf("idempotent reconcile updated=%v err=%v", updated, err)
	}

	if err := setTopicTitleWithSource(root, meta.TopicID, "用户手动标题", topicTitleSourceManual); err != nil {
		t.Fatal(err)
	}
	if updated, err := app.reconcileAssistantSessionTitle(run); err != nil || updated {
		t.Fatalf("manual title was replaced: updated=%v err=%v", updated, err)
	}
}

func TestAssistantRuntimeAutoResumesPersistedMemoryApproval(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRun(t, store, "memory-recovery")
	now := time.Now()
	claimed, ok, err := store.Claim("desktop-test", now, time.Minute)
	if err != nil || !ok || claimed.ID != run.ID {
		t.Fatalf("Claim: run=%+v ok=%v err=%v", claimed, ok, err)
	}
	waiting, err := store.RequestApproval(assistant.ApprovalInput{
		RequestID: "memory-approval", RunID: run.ID, LeaseOwner: "desktop-test", LeaseFence: claimed.LeaseFence,
		Action: "approve_tool:remember", Summary: "save writing principles", Tool: "remember", Subject: "writing-principles",
		SessionPath: "sessions/memory", ResumeToken: "memory-token", Now: now.Add(time.Second),
	})
	if err != nil || waiting.State != assistant.RunWaitingApproval {
		t.Fatalf("RequestApproval: run=%+v err=%v", waiting, err)
	}

	if err := service.resumeAutoMemoryApprovals(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("resumeAutoMemoryApprovals: %v", err)
	}
	if err := service.resumeAutoMemoryApprovals(now.Add(3 * time.Second)); err != nil {
		t.Fatalf("idempotent recovery: %v", err)
	}
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 1 || got.Runs[0].State != assistant.RunQueued {
		t.Fatalf("recovered run = %+v, want queued", got.Runs)
	}
	if len(got.Attention) != 1 || got.Attention[0].State != assistant.AttentionApproved || !strings.Contains(got.Attention[0].Resolution, "自动允许") {
		t.Fatalf("recovered attention = %+v", got.Attention)
	}
}

func TestAssistantRuntimeKeepsSensitiveApprovalOpen(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRun(t, store, "publish-recovery")
	now := time.Now()
	claimed, ok, err := store.Claim("desktop-test", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim: run=%+v ok=%v err=%v", claimed, ok, err)
	}
	if _, err := store.RequestApproval(assistant.ApprovalInput{
		RequestID: "publish-approval", RunID: run.ID, LeaseOwner: "desktop-test", LeaseFence: claimed.LeaseFence,
		Action: "approve_tool:publish", Summary: "publish result", Tool: "publish", Subject: "public post",
		SessionPath: "sessions/publish", ResumeToken: "publish-token", Now: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.resumeAutoMemoryApprovals(now.Add(2 * time.Second)); err != nil {
		t.Fatalf("resumeAutoMemoryApprovals: %v", err)
	}
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs[0].State != assistant.RunWaitingApproval || got.Attention[0].State != assistant.AttentionOpen {
		t.Fatalf("sensitive approval changed: run=%+v attention=%+v", got.Runs[0], got.Attention[0])
	}
}

func TestAssistantRuntimeRejectsMissingLiveWebEvidence(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRunWithTask(t, store, "web-missing", "观察 NGA", "查看 bbs.nga.cn 最新内容")
	service.Start()
	defer service.Stop()

	failed := waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunRetryWait)
	if failed.Error == nil || failed.Error.Code != "evidence_missing" || !failed.Error.Retryable {
		t.Fatalf("missing live_web evidence was not explicit and retryable: %+v", failed.Error)
	}
}

func TestAssistantRuntimeAcceptsSuccessfulLiveWebResult(t *testing.T) {
	host := &assistantHostStub{toolResults: []event.Tool{{Name: "browser_state", Output: "NGA live page"}}}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRunWithTask(t, store, "web-success", "观察 NGA", "查看 bbs.nga.cn 最新内容")
	service.Start()
	defer service.Stop()

	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunSucceeded)
}

func TestAssistantRuntimeRejectsFailedLiveWebResult(t *testing.T) {
	host := &assistantHostStub{toolResults: []event.Tool{{Name: "web_fetch", Err: "network denied"}}}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRunWithTask(t, store, "web-failed", "观察 NGA", "查看 bbs.nga.cn 最新内容")
	service.Start()
	defer service.Stop()

	failed := waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunRetryWait)
	if failed.Error == nil || failed.Error.Code != "evidence_missing" {
		t.Fatalf("failed live tool result incorrectly satisfied evidence: %+v", failed.Error)
	}
}

func TestAssistantRuntimeBindsBeforeSubmitAndDoesNotChangeActiveTab(t *testing.T) {
	host := &assistantHostStub{activeBefore: "user-tab"}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRun(t, store, "success")
	service.Start()
	defer service.Stop()

	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunSucceeded)
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.submitted != 1 || !host.boundAtSend {
		t.Fatalf("submit=%d boundAtSend=%v", host.submitted, host.boundAtSend)
	}
	if host.activeAfter != host.activeBefore {
		t.Fatalf("background run changed active tab: before=%q after=%q", host.activeBefore, host.activeAfter)
	}
}

func TestAssistantRuntimeSetupFailureIsPersistedAndRetryable(t *testing.T) {
	host := &assistantHostStub{prepareErr: errors.New("workspace missing")}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRun(t, store, "failure")
	service.Start()
	defer service.Stop()

	failed := waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunRetryWait)
	if failed.Error == nil || failed.Error.Code != "session_prepare" || !failed.Error.OutcomeKnown || !failed.Error.Retryable {
		t.Fatalf("failure was not explicit/retryable: %+v", failed.Error)
	}
}

func TestAssistantRejectedSubmitDoesNotRegisterOrMisfinishRun(t *testing.T) {
	host := &assistantHostStub{busy: true}
	host.controller.mode = control.ToolApprovalYolo
	host.controller.policy = permission.New("allow", []string{"write_file"}, nil, nil)
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRun(t, store, "busy")
	service.Start()
	defer service.Stop()
	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunRetryWait)
	if consumed := service.ObserveEvent("assistant-tab", event.Event{Kind: event.TurnDone}); consumed {
		t.Fatal("unowned TurnDone was consumed")
	}
	result, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Runs[0].State != assistant.RunRetryWait || result.Runs[0].Error == nil || result.Runs[0].Error.Code != "session_busy" {
		t.Fatalf("rejected submit was mis-settled: %+v", result.Runs[0])
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.inflight) != 0 || len(service.byRun) != 0 {
		t.Fatalf("rejected submit leaked ownership: inflight=%d byRun=%d", len(service.inflight), len(service.byRun))
	}
	host.controller.mu.Lock()
	defer host.controller.mu.Unlock()
	if host.controller.mode != control.ToolApprovalYolo || host.controller.policy.Decide("write_file", false, nil) != permission.Allow {
		t.Fatalf("busy submit mutated controller policy: mode=%q policy=%+v", host.controller.mode, host.controller.policy)
	}
}

func TestAssistantTrySubmitAppliesPolicyToReconciledController(t *testing.T) {
	fixture := newStaleWorkspaceBindingFixture(t, "assistant_policy")
	fixture.oldCtrl.SetToolApprovalMode(control.ToolApprovalYolo)
	claimed := false
	host := appAssistantSessionHost{app: fixture.app}
	accepted, err := host.TrySubmit(fixture.tab.ID, "assistant policy probe", assistant.DefaultPolicy(), nil, func() bool {
		claimed = true
		return true
	}, func() { claimed = false })
	if err != nil {
		t.Fatalf("TrySubmit: %v", err)
	}
	if !accepted || !claimed {
		t.Fatalf("TrySubmit accepted=%v claimed=%v", accepted, claimed)
	}
	assertTabRebuiltToPinnedWorkspace(t, fixture)
	if got := fixture.tab.Ctrl.ToolApprovalMode(); got != control.ToolApprovalAuto {
		t.Fatalf("reconciled controller mode=%q, want auto", got)
	}
	fixture.tab.Ctrl.Cancel()
	waitNotRunning(t, fixture.tab.Ctrl)
}

func TestAssistantMissingWorkspaceCreatesAttentionWithoutPreparingOrSubmitting(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	missing := filepath.Join(t.TempDir(), "missing-workspace")
	now := time.Now()
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-missing-workspace",
		Assistant: assistant.Assistant{
			ID: "helper-missing-workspace", Name: "Helper", Mission: "Inspect project",
			Scope: assistant.ScopeWorkspace, WorkspaceRoot: missing,
			Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Routines: []assistant.Routine{{
			ID: "routine-missing-workspace", Title: "Scan", Prompt: "Scan", Enabled: true,
			CatchUp: assistant.CatchUpCoalesceLatest, Schedule: assistant.Schedule{Kind: assistant.ScheduleManual},
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	run, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RoutineID: snapshot.Routines[0].ID,
		RequestID: "run-missing-workspace", Trigger: assistant.TriggerManual, Now: now,
	})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	service.Start()
	defer service.Stop()
	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunWaitingAttention)
	result, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Attention) != 1 || result.Attention[0].Action != "cancel_recreate" {
		t.Fatalf("workspace attention = %+v", result.Attention)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.prepared.ID != "" || host.submitted != 0 {
		t.Fatalf("missing workspace reached session host: prepared=%q submitted=%d", host.prepared.ID, host.submitted)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing workspace was recreated: %v", err)
	}
}

func TestAssistantRuntimeStartStopAreIdempotent(t *testing.T) {
	service, _ := newAssistantTestRuntime(t, &assistantHostStub{})
	service.Start()
	service.Start()
	service.Stop()
	service.Stop()
	if service.running.Load() {
		t.Fatal("runtime still marked running after Stop")
	}
}

func TestAssistantRunNowWakesRuntimeWithoutWaitingForTicker(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	service.tick = time.Hour
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-wake",
		Assistant: assistant.Assistant{
			ID: "helper-wake", Name: "Helper", Mission: "Wake immediately",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Routines: []assistant.Routine{{
			ID: "routine-wake", Title: "Wake", Prompt: "Run now", Enabled: true,
			CatchUp: assistant.CatchUpCoalesceLatest, Schedule: assistant.Schedule{Kind: assistant.ScheduleManual},
		}},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	app := &App{assistant: service}
	service.Start()
	defer service.Stop()
	// Let the initial empty pass finish. The next regular pass is one hour away.
	time.Sleep(50 * time.Millisecond)
	run, err := app.AssistantRunNow(AssistantRunNowRequest{
		AssistantID: snapshot.Assistant.ID, RoutineID: snapshot.Routines[0].ID, RequestID: "wake-run",
	})
	if err != nil {
		t.Fatalf("AssistantRunNow: %v", err)
	}
	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunSucceeded)
}

func TestAssistantApprovalPersistsToolAndSubject(t *testing.T) {
	host := &assistantHostStub{approval: &event.Approval{
		ID: "approval-1", Tool: "bash", Subject: "publish release artifacts", Summary: "requires owner approval",
	}}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRun(t, store, "approval")
	service.Start()
	defer service.Stop()

	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunWaitingApproval)
	result, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(result.Attention) != 1 {
		t.Fatalf("attention count = %d, want 1", len(result.Attention))
	}
	item := result.Attention[0]
	if item.Action != "approve_tool:bash" {
		t.Fatalf("attention action = %q", item.Action)
	}
	if item.Tool != "bash" || item.Subject != "publish release artifacts" {
		t.Fatalf("attention grant identity = (%q, %q)", item.Tool, item.Subject)
	}
	if !strings.Contains(item.Summary, "publish release artifacts") {
		t.Fatalf("attention summary lost subject: %q", item.Summary)
	}
}

func TestAssistantApprovedToolAttentionOnlyGrantsCurrentResumeToken(t *testing.T) {
	host := &assistantHostStub{approval: &event.Approval{
		ID: "approval-empty-subject", Tool: "bash", Summary: "approve exact tool call",
	}}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRun(t, store, "approval-grant")
	service.Start()
	defer service.Stop()

	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunWaitingApproval)
	current, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	item := current.Attention[0]
	if item.Tool != "bash" || item.Subject != "" {
		t.Fatalf("persisted exact grant = (%q, %q)", item.Tool, item.Subject)
	}
	if _, err := store.ResolveAttention(assistant.ResolveAttentionInput{
		RequestID: "resolve-approval-grant-a", AssistantID: snapshot.Assistant.ID,
		AttentionID: item.ID, ExpectedRevision: item.Revision,
		State: assistant.AttentionApproved, Resolution: "approved once", Now: time.Now(),
	}); err != nil {
		t.Fatalf("ResolveAttention: %v", err)
	}
	host.mu.Lock()
	host.approval = &event.Approval{
		ID: "approval-b", Tool: "browser_click", Subject: "publish release button", Summary: "approve second exact call",
	}
	host.mu.Unlock()
	if _, err := store.Resume(assistant.ResumeInput{RequestID: "resume-approval-grant-a", RunID: run.ID, Now: time.Now()}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	service.Wake()
	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunWaitingApproval)

	host.mu.Lock()
	submittedGrants := append([][]control.ToolGrant(nil), host.grants...)
	host.mu.Unlock()
	if len(submittedGrants) != 2 || len(submittedGrants[1]) != 1 {
		t.Fatalf("submit grants = %+v", submittedGrants)
	}
	if got := submittedGrants[1][0]; got.Tool != "bash" || got.Subject != "" {
		t.Fatalf("resumed exact grant = %+v", got)
	}

	current, err = store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Attention) != 2 {
		t.Fatalf("attention count = %d, want 2", len(current.Attention))
	}
	item = current.Attention[1]
	if item.Tool != "browser_click" || item.Subject != "publish release button" {
		t.Fatalf("second exact grant = (%q, %q)", item.Tool, item.Subject)
	}
	if _, err := store.ResolveAttention(assistant.ResolveAttentionInput{
		RequestID: "resolve-approval-grant-b", AssistantID: snapshot.Assistant.ID,
		AttentionID: item.ID, ExpectedRevision: item.Revision,
		State: assistant.AttentionApproved, Resolution: "approved second", Now: time.Now(),
	}); err != nil {
		t.Fatalf("Resolve second attention: %v", err)
	}
	host.mu.Lock()
	host.approval = nil
	host.mu.Unlock()
	if _, err := store.Resume(assistant.ResumeInput{RequestID: "resume-approval-grant-b", RunID: run.ID, Now: time.Now()}); err != nil {
		t.Fatalf("Resume second attention: %v", err)
	}
	service.Wake()
	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunSucceeded)

	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.grants) != 3 || len(host.grants[2]) != 1 {
		t.Fatalf("all submit grants = %+v", host.grants)
	}
	if got := host.grants[2][0]; got.Tool != "browser_click" || got.Subject != "publish release button" {
		t.Fatalf("stale grant revived on second resume: %+v", host.grants[2])
	}
}

func TestAssistantOwnedAskBypassesDecisionBrokerAndCancelDoneCannotSucceed(t *testing.T) {
	ask := &event.Ask{ID: "ask-1", Questions: []event.AskQuestion{{ID: "q1", Prompt: "Choose release channel"}}}
	host := &assistantHostStub{ask: ask, cancelDone: true}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, run := createAssistantRun(t, store, "ask")
	service.Start()
	defer service.Stop()
	waitAssistantRunState(t, store, snapshot.Assistant.ID, run.ID, assistant.RunWaitingApproval)
	time.Sleep(30 * time.Millisecond)
	result, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Runs[0].State != assistant.RunWaitingApproval || len(result.Attention) != 1 || result.Attention[0].Action != "answer_required" {
		t.Fatalf("ask/cancel TurnDone corrupted run: run=%+v attention=%+v", result.Runs[0], result.Attention)
	}
	item := result.Attention[0]
	if _, err := store.ResolveAttention(assistant.ResolveAttentionInput{
		RequestID: "resolve-ask-answer", AssistantID: snapshot.Assistant.ID,
		AttentionID: item.ID, ExpectedRevision: item.Revision,
		State: assistant.AttentionApproved, Resolution: "stable", Now: time.Now(),
	}); err != nil {
		t.Fatalf("Resolve ask: %v", err)
	}
	prompt, grants, _, _, err := service.promptFor(result.Runs[0])
	if err != nil {
		t.Fatalf("promptFor: %v", err)
	}
	if !strings.Contains(prompt, "明确回答：stable") || len(grants) != 0 {
		t.Fatalf("ask answer was not injected exactly: prompt=%q grants=%+v", prompt, grants)
	}

	broker, err := decision.Open("")
	if err != nil {
		t.Fatal(err)
	}
	in := &assistantInFlight{runID: "owned", tabID: "owned-tab", done: make(chan assistantTurnResult, 1)}
	service.mu.Lock()
	service.inflight[in.tabID] = in
	service.mu.Unlock()
	sink := &tabEventSink{tabID: in.tabID, app: &App{assistant: service, decisionBroker: broker}}
	sink.Emit(event.Event{Kind: event.AskRequest, Ask: *ask})
	if got := len(broker.Snapshot().Decisions); got != 0 {
		t.Fatalf("assistant-owned Ask leaked to DecisionBroker: %d decisions", got)
	}
}

func TestAssistantRunCompletionWakesNextQueuedRun(t *testing.T) {
	release := make(chan struct{})
	host := &assistantHostStub{block: release}
	service, store := newAssistantTestRuntime(t, host)
	service.tick = time.Hour
	snapshot, first := createAssistantRun(t, store, "serial")
	service.Start()
	defer service.Stop()
	waitAssistantRunState(t, store, snapshot.Assistant.ID, first.ID, assistant.RunRunning)
	second, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RoutineID: snapshot.Routines[0].ID,
		RequestID: "run-serial-second", Trigger: assistant.TriggerManual, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Trigger second: %v", err)
	}
	service.Wake()
	close(release)
	waitAssistantRunState(t, store, snapshot.Assistant.ID, first.ID, assistant.RunSucceeded)
	waitAssistantRunState(t, store, snapshot.Assistant.ID, second.ID, assistant.RunSucceeded)
}

func TestApplyAssistantPolicyKeepsApprovalForSensitiveActions(t *testing.T) {
	policy := buildAssistantPermissionPolicy(assistant.DefaultPolicy())
	if policy.Mode != permission.Ask {
		t.Fatalf("fallback mode = %s, want ask", policy.Mode)
	}
}

type permissionApproverStub struct{ calls int }

func (a *permissionApproverStub) Approve(context.Context, string, string, json.RawMessage) (bool, bool, error) {
	a.calls++
	return false, false, nil
}

func TestAssistantPermissionPolicyDeniesWithoutApprovalAndKeepsSensitiveAsk(t *testing.T) {
	policy := assistant.DefaultPolicy()
	policy.Publish = assistant.AccessAllow
	policy.Delete = assistant.AccessAllow
	policy.Payment = assistant.AccessAllow
	policy.Secrets = assistant.AccessAllow
	policy.Private = assistant.AccessAllow
	approver := &permissionApproverStub{}
	gate := permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
	allowed, _, err := gate.Check(context.Background(), "write_file", json.RawMessage(`{"path":"a.txt"}`), false)
	if err != nil || allowed {
		t.Fatalf("denied writer result: allowed=%v err=%v", allowed, err)
	}
	if approver.calls != 0 {
		t.Fatalf("deny unexpectedly requested approval %d times", approver.calls)
	}
	allowed, _, err = gate.Check(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf build"}`), false)
	if err != nil || allowed || approver.calls != 0 {
		t.Fatalf("denied bash result: allowed=%v calls=%d err=%v", allowed, approver.calls, err)
	}
	allowed, _, err = gate.Check(context.Background(), "delete_range", json.RawMessage(`{"path":"a.txt"}`), false)
	if err != nil || allowed || approver.calls != 0 {
		t.Fatalf("denied delete result: allowed=%v calls=%d err=%v", allowed, approver.calls, err)
	}
	policy.LocalWrite = assistant.AccessAllow
	approver = &permissionApproverStub{}
	gate = permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
	allowed, _, err = gate.Check(context.Background(), "bash", json.RawMessage(`{"command":"go build ./..."}`), false)
	if err != nil || !allowed || approver.calls != 0 {
		t.Fatalf("allowed bash result: allowed=%v calls=%d err=%v", allowed, approver.calls, err)
	}
	allowed, _, err = gate.Check(context.Background(), "delete_range", json.RawMessage(`{"path":"a.txt"}`), false)
	if err != nil || allowed || approver.calls != 1 {
		t.Fatalf("sensitive delete result: allowed=%v calls=%d err=%v", allowed, approver.calls, err)
	}
}

func TestAssistantPermissionPolicyBashThreeStates(t *testing.T) {
	for _, tc := range []struct {
		access        assistant.Access
		wantAllowed   bool
		wantApprovals int
	}{
		{assistant.AccessDeny, false, 0},
		{assistant.AccessApprove, false, 1},
		{assistant.AccessAllow, true, 0},
	} {
		policy := assistant.DefaultPolicy()
		policy.LocalWrite = tc.access
		approver := &permissionApproverStub{}
		gate := permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
		// A normal build/test command exercises the no-whitelist path; read-only
		// commands share the same decision because bash is gated by LocalWrite,
		// not command content.
		allowed, _, err := gate.Check(context.Background(), "bash", json.RawMessage(`{"command":"go test ./..."}`), false)
		if err != nil || allowed != tc.wantAllowed || approver.calls != tc.wantApprovals {
			t.Fatalf("local=%s allowed=%v approvals=%d err=%v", tc.access, allowed, approver.calls, err)
		}
	}
}

func TestAssistantPermissionPolicyLocalAndNetworkThreeStates(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		for _, tc := range []struct {
			access        assistant.Access
			wantAllowed   bool
			wantApprovals int
		}{
			{assistant.AccessDeny, false, 0},
			{assistant.AccessApprove, false, 1},
			{assistant.AccessAllow, true, 0},
		} {
			policy := assistant.DefaultPolicy()
			policy.LocalWrite = tc.access
			approver := &permissionApproverStub{}
			gate := permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
			allowed, _, err := gate.Check(context.Background(), "write_file", json.RawMessage(`{"path":"a.txt"}`), false)
			if err != nil || allowed != tc.wantAllowed || approver.calls != tc.wantApprovals {
				t.Fatalf("local=%s allowed=%v approvals=%d err=%v", tc.access, allowed, approver.calls, err)
			}
		}
	})
	t.Run("network", func(t *testing.T) {
		for _, tc := range []struct {
			access        assistant.Access
			wantAllowed   bool
			wantApprovals int
		}{
			{assistant.AccessDeny, false, 0},
			{assistant.AccessApprove, false, 1},
			{assistant.AccessAllow, true, 0},
		} {
			policy := assistant.DefaultPolicy()
			policy.Network = tc.access
			approver := &permissionApproverStub{}
			gate := permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
			allowed, _, err := gate.Check(context.Background(), "web_fetch", json.RawMessage(`{"url":"https://example.com"}`), true)
			if err != nil || allowed != tc.wantAllowed || approver.calls != tc.wantApprovals {
				t.Fatalf("network=%s allowed=%v approvals=%d err=%v", tc.access, allowed, approver.calls, err)
			}
		}
	})
}

func TestAssistantPermissionSkillInstallRequiresLocalAndNetwork(t *testing.T) {
	for _, tc := range []struct {
		local, network assistant.Access
		wantAllowed    bool
		wantApprovals  int
	}{
		{assistant.AccessAllow, assistant.AccessAllow, true, 0},
		{assistant.AccessAllow, assistant.AccessApprove, false, 1},
		{assistant.AccessApprove, assistant.AccessAllow, false, 1},
		{assistant.AccessDeny, assistant.AccessAllow, false, 0},
		{assistant.AccessAllow, assistant.AccessDeny, false, 0},
	} {
		policy := assistant.DefaultPolicy()
		policy.LocalWrite, policy.Network = tc.local, tc.network
		approver := &permissionApproverStub{}
		gate := permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
		allowed, _, err := gate.Check(context.Background(), "install_source", json.RawMessage(`{"source":"https://example.com/SKILL.md","kind":"skill","scope":"project"}`), false)
		if err != nil || allowed != tc.wantAllowed || approver.calls != tc.wantApprovals {
			t.Fatalf("local=%s network=%s allowed=%v approvals=%d err=%v", tc.local, tc.network, allowed, approver.calls, err)
		}
	}
}

func TestAssistantPermissionPolicyKeepsMoveDeleteAndAllMCPAsk(t *testing.T) {
	policy := assistant.DefaultPolicy()
	policy.LocalWrite = assistant.AccessAllow
	policy.Network = assistant.AccessAllow
	approver := &permissionApproverStub{}
	gate := permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
	for _, toolName := range []string{"move_file", "delete_range", "mcp__forum__publish"} {
		allowed, _, err := gate.Check(context.Background(), toolName, json.RawMessage(`{"path":"a.txt"}`), false)
		if err != nil || allowed {
			t.Fatalf("%s allowed=%v err=%v, want declined Ask", toolName, allowed, err)
		}
	}
	if approver.calls != 3 {
		t.Fatalf("writer approval calls=%d, want 3", approver.calls)
	}
	allowed, _, err := gate.Check(context.Background(), "mcp__docs__read", nil, true)
	if err != nil || allowed || approver.calls != 4 {
		t.Fatalf("read-only MCP allowed=%v approvals=%d err=%v, want Ask", allowed, approver.calls, err)
	}
	policy.Network = assistant.AccessDeny
	approver = &permissionApproverStub{}
	gate = permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
	allowed, _, err = gate.Check(context.Background(), "mcp__docs__read", nil, true)
	if err != nil || allowed || approver.calls != 0 {
		t.Fatalf("network-denied MCP allowed=%v approvals=%d err=%v", allowed, approver.calls, err)
	}
}

func TestAssistantPermissionMemoryToolsAutoExecute(t *testing.T) {
	// memory/remember/forget auto-execute regardless of LocalWrite and never
	// request an approver, because they write to the assistant's bound project
	// memory store rather than arbitrary files.
	for _, lw := range []assistant.Access{assistant.AccessDeny, assistant.AccessApprove, assistant.AccessAllow} {
		policy := assistant.DefaultPolicy()
		policy.LocalWrite = lw
		approver := &permissionApproverStub{}
		gate := permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
		for _, toolName := range []string{"memory", "remember", "forget"} {
			allowed, _, err := gate.Check(context.Background(), toolName, json.RawMessage(`{"name":"x"}`), toolName == "memory")
			if err != nil || !allowed {
				t.Fatalf("local=%s %s allowed=%v err=%v, want auto-allow", lw, toolName, allowed, err)
			}
		}
		if approver.calls != 0 {
			t.Fatalf("local=%s memory tools requested approval %d times, want 0", lw, approver.calls)
		}
	}

	// Sensitive boundaries still require approval even with memory tools
	// auto-allowed and LocalWrite/Network set to Allow. bash is no longer in
	// this set: LocalWrite=Allow auto-executes it, so only destructive file
	// ops and MCP publishing remain Ask here.
	policy := assistant.DefaultPolicy()
	policy.LocalWrite = assistant.AccessAllow
	policy.Network = assistant.AccessAllow
	approver := &permissionApproverStub{}
	gate := permission.NewGate(buildAssistantPermissionPolicy(policy), approver)
	for _, toolName := range []string{"delete_range", "mcp__forum__publish"} {
		allowed, _, err := gate.Check(context.Background(), toolName, json.RawMessage(`{"command":"rm -rf build"}`), false)
		if err != nil || allowed {
			t.Fatalf("%s allowed=%v err=%v, want declined", toolName, allowed, err)
		}
	}
	if approver.calls != 2 {
		t.Fatalf("sensitive tool approvals=%d, want 2", approver.calls)
	}
}

func TestAssistantPromptForInjectsResponsibilityGraph(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "graph")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	if _, err := store.CompleteRunWithProgress(assistant.CompleteRunInput{
		RequestID: "seed-graph", RunID: claimed.ID, LeaseOwner: "desktop-test", LeaseFence: claimed.LeaseFence,
		Progress: assistant.ProgressBlock{
			PlanRevision: 1,
			Responsibilities: []assistant.RespDecl{
				{Alias: "up", Objective: "do up"},
				{Alias: "down", Objective: "do down", DoneCriteria: "shipped", NextAction: "ship it", DependsOn: []string{"up"}},
			},
			Complete: []string{"up"},
		},
		Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RoutineID: snapshot.Routines[0].ID,
		RequestID: "run-graph-second", Trigger: assistant.TriggerManual, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, _, selected, planRevision, err := service.promptFor(second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "down") || !strings.Contains(prompt, "本次负责：down") {
		t.Fatalf("graph not injected: %q", prompt)
	}
	if selected == nil || selected.Alias != "down" || planRevision != 2 {
		t.Fatalf("selected=%+v planRevision=%d", selected, planRevision)
	}
}

func TestAssistantPromptForInjectsTypedProposalTargetsAndPendingDedupContext(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "proposal-context")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	newPrompt := "Inspect tests, release notes, and publish readiness"
	if _, err := store.CompleteRunWithProgress(assistant.CompleteRunInput{
		RequestID: "seed-proposal", RunID: claimed.ID, LeaseOwner: "desktop-test", LeaseFence: claimed.LeaseFence,
		Progress: assistant.ProgressBlock{PlanRevision: 1, Proposals: []assistant.ProposalDecl{{
			TargetKind: assistant.ProposalTargetRoutine, TargetID: snapshot.Routines[0].ID,
			Routine: &assistant.RoutineProposalPatch{Prompt: &newPrompt},
			Summary: "Expand release checks", Reason: "The last run missed release notes", Evidence: []string{"run summary omitted release notes"},
		}}}, Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RoutineID: snapshot.Routines[0].ID,
		RequestID: "run-proposal-context-second", Trigger: assistant.TriggerManual, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, _, _, _, err := service.promptFor(second)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{snapshot.Routines[0].ID, "可提出改进建议的 Routine", "已有待用户处理的改进建议", "Expand release checks", `"proposals"`, "不能声称配置已经修改", "禁止通过提案修改使命、权限、Workspace、渠道地址或凭据"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("proposal prompt missing %q: %s", want, prompt)
		}
	}
}

func TestAssistantPromptForDirectInputUsesUserInputSemantics(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	now := time.Now()
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-direct-semantics",
		Assistant: assistant.Assistant{
			ID: "helper-direct-semantics", Name: "Helper", Mission: "Keep the project healthy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Routines: []assistant.Routine{{
			ID: "routine-direct-semantics", Title: "Scan", Prompt: "Inspect changes", Enabled: true,
			CatchUp: assistant.CatchUpCoalesceLatest, Schedule: assistant.Schedule{Kind: assistant.ScheduleManual},
		}},
		Now: now,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RequestID: "direct-prior", Prompt: "以后不要静默吞错",
		Trigger: assistant.TriggerManual, Now: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	run, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RequestID: "direct-current", Prompt: "这是一个批评：不要改我的原文",
		Trigger: assistant.TriggerManual, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, _, _, _, err := service.promptFor(run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "本次用户输入（原文）") {
		t.Fatalf("direct input lost its original-input semantics: %q", prompt)
	}
	if strings.Contains(prompt, "本次任务") {
		t.Fatalf("direct input was framed as a routine task: %q", prompt)
	}
	if !strings.Contains(prompt, "这是一个批评：不要改我的原文") {
		t.Fatalf("direct input original text not preserved: %q", prompt)
	}
	if !strings.Contains(prompt, "不要要求用户把输入改写成任务") {
		t.Fatalf("direct input rewrite-forbidding guidance missing: %q", prompt)
	}
	if !strings.Contains(prompt, "以后不要静默吞错") {
		t.Fatalf("recent direct-input history not injected: %q", prompt)
	}
	if strings.Contains(prompt, "Inspect changes") {
		t.Fatalf("routine prompt leaked into a direct-input run: %q", prompt)
	}
}

func TestDirectInputHistoryExcludesCurrentOrdersAndBounds(t *testing.T) {
	base := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	mk := func(id, prompt string, startedAt time.Time, state assistant.RunState, summary string) assistant.Run {
		return assistant.Run{
			ID: id, RoutineID: "", Trigger: assistant.TriggerManual, Prompt: prompt,
			State: state, StartedAt: startedAt, Summary: summary, CreatedAt: startedAt,
		}
	}
	runs := []assistant.Run{
		mk("run-1", "第一个输入", base, assistant.RunSucceeded, "完成了"),
		mk("run-routine", "routine prompt", base.Add(time.Minute), assistant.RunSucceeded, ""), // ignored: routine id
		mk("run-2", "第二个输入", base.Add(3*time.Minute), assistant.RunFailed, "失败"),               // same timestamp as current: queue order wins
		mk("run-current", "当前输入", base.Add(3*time.Minute), assistant.RunQueued, ""),
		mk("run-future", "稍后才提交的输入", base.Add(4*time.Minute), assistant.RunQueued, ""),
	}
	runs[1].RoutineID = "routine-1"
	runs[1].Trigger = assistant.TriggerManual // keep manual but routine id makes it non-direct

	got := directInputHistory(runs, "run-current", 2, 1000)
	if len(got) != 2 || got[0].ID != "run-2" || got[1].ID != "run-1" {
		t.Fatalf("history = %+v, want newest-first excluding current and routine runs", got)
	}
	if got[0].Prompt != "第二个输入" || got[1].Summary != "完成了" {
		t.Fatalf("history lost original text or summary: %+v", got)
	}

	// Byte bound: an oversized newest item is UTF-8 safely truncated in the
	// injected copy; the durable Run remains unchanged.
	huge := strings.Repeat("长", 200)
	runs = []assistant.Run{
		mk("run-small", "小输入", base, assistant.RunSucceeded, "ok"),
		mk("run-huge", huge, base.Add(time.Minute), assistant.RunSucceeded, ""),
	}
	got = directInputHistory(runs, "", 8, 100)
	if len(got) != 1 || got[0].ID != "run-huge" || len(got[0].Prompt)+len(got[0].Summary) > 100 || !strings.HasSuffix(got[0].Prompt, "…") {
		t.Fatalf("byte-bounded history = %+v, want a safely truncated newest item", got)
	}
	if runs[1].Prompt != huge {
		t.Fatalf("history selection mutated durable input: got %d bytes want %d", len(runs[1].Prompt), len(huge))
	}
}

func TestAssistantCompleteRunAppliesProgressAndStripsSummary(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "progress")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	block := `<assistant-progress>{"responsibility":"scan","responsibilities":[{"alias":"scan","objective":"scan changes"}],"complete":["scan"]}</assistant-progress>`
	service.completeRun(*claimed, nil, snapshot.Plan.Revision, assistantTurnResult{
		Summary:      "Scan complete\n" + block,
		ProgressText: block,
	}, "C:/assistant/session.jsonl")
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs[0].State != assistant.RunSucceeded {
		t.Fatalf("run state = %s", got.Runs[0].State)
	}
	if strings.Contains(got.Runs[0].Summary, "<assistant-progress>") {
		t.Fatalf("raw block leaked into summary: %q", got.Runs[0].Summary)
	}
	if got.Runs[0].ResponsibilityID == "" {
		t.Fatalf("run did not reference its responsibility: %+v", got.Runs[0])
	}
	if got.Plan.Revision != 2 || len(got.Plan.Responsibilities) != 1 || got.Plan.Responsibilities[0].Status != assistant.RespDone {
		t.Fatalf("plan = %+v", got.Plan)
	}
}

func TestAssistantCompleteRunSucceedsDiscardingBlockedProgress(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "blocked")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	block := `<assistant-progress>{"responsibilities":[{"alias":"a","objective":"a"},{"alias":"b","objective":"b","depends_on":["a"]}],"complete":["b"]}</assistant-progress>`
	service.completeRun(*claimed, nil, snapshot.Plan.Revision, assistantTurnResult{Summary: "tried", ProgressText: block}, "C:/assistant/session.jsonl")
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs[0].State != assistant.RunSucceeded {
		t.Fatalf("run state = %s, want succeeded", got.Runs[0].State)
	}
	if got.Runs[0].Summary != "tried" || got.Runs[0].SessionPath != "C:/assistant/session.jsonl" {
		t.Fatalf("summary/session not preserved on discarded patch: %+v", got.Runs[0])
	}
	if got.Plan.Revision != 1 || len(got.Plan.Responsibilities) != 0 {
		t.Fatalf("plan mutated on blocked patch: %+v", got.Plan)
	}
	if !hasAssistantDiagnostic(service, "progress_apply") {
		t.Fatalf("blocked patch did not record a progress_apply diagnostic: %+v", service.Diagnostics())
	}
}

func TestAssistantCompleteRunAcceptsCompleteWithStaleActiveMarker(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "complete-active")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	block := `<assistant-progress>{"responsibility":"learn-skills","responsibilities":[{"alias":"learn-skills","objective":"learn skills"},{"alias":"promote","objective":"promote product","depends_on":["learn-skills"]}],"complete":["learn-skills"],"active":["learn-skills"]}</assistant-progress>`
	service.completeRun(*claimed, nil, snapshot.Plan.Revision, assistantTurnResult{Summary: "learned", ProgressText: block}, "C:/assistant/session.jsonl")

	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs[0].State != assistant.RunSucceeded {
		t.Fatalf("run state = %s, want succeeded", got.Runs[0].State)
	}
	if resp := assistantRespByAlias(got, "learn-skills"); resp == nil || resp.Status != assistant.RespDone {
		t.Fatalf("learn-skills = %+v, want done", resp)
	}
	if resp := assistantRespByAlias(got, "promote"); resp == nil || resp.Status != assistant.RespReady {
		t.Fatalf("promote = %+v, want ready", resp)
	}
	if hasAssistantDiagnostic(service, "progress_apply") {
		t.Fatalf("valid complete+active patch recorded a diagnostic: %+v", service.Diagnostics())
	}
}

func TestAssistantCompleteRunSucceedsDiscardingMalformedProgress(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "malformed")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	// The trailing valid block must NOT be applied: a malformed block discards
	// the whole progress result while the run still succeeds.
	service.completeRun(*claimed, nil, snapshot.Plan.Revision, assistantTurnResult{
		Summary:      "tried",
		ProgressText: `<assistant-progress>not-json</assistant-progress><assistant-progress>{"complete":["scan"]}</assistant-progress>`,
	}, "C:/assistant/session.jsonl")
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs[0].State != assistant.RunSucceeded {
		t.Fatalf("run state = %s, want succeeded", got.Runs[0].State)
	}
	if got.Runs[0].Summary != "tried" || got.Runs[0].SessionPath != "C:/assistant/session.jsonl" {
		t.Fatalf("summary/session not preserved on malformed patch: %+v", got.Runs[0])
	}
	if got.Plan.Revision != 1 || len(got.Plan.Responsibilities) != 0 {
		t.Fatalf("plan mutated on malformed patch: %+v", got.Plan)
	}
	if !hasAssistantDiagnostic(service, "progress_parse") {
		t.Fatalf("malformed patch did not record a progress_parse diagnostic: %+v", service.Diagnostics())
	}
	if hasAssistantDiagnostic(service, "progress_apply") {
		t.Fatalf("malformed patch unexpectedly tried to apply: %+v", service.Diagnostics())
	}
}

func TestAssistantCompleteRunSucceedsDiscardingInvalidProgressPatch(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "invalid-patch")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	block := `<assistant-progress>{"complete":["missing-alias"]}</assistant-progress>`
	service.completeRun(*claimed, nil, snapshot.Plan.Revision, assistantTurnResult{Summary: "tried", ProgressText: block}, "C:/assistant/session.jsonl")
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs[0].State != assistant.RunSucceeded {
		t.Fatalf("run state = %s, want succeeded", got.Runs[0].State)
	}
	if got.Runs[0].Summary != "tried" || got.Runs[0].SessionPath != "C:/assistant/session.jsonl" {
		t.Fatalf("summary/session not preserved on invalid patch: %+v", got.Runs[0])
	}
	if got.Plan.Revision != 1 || len(got.Plan.Responsibilities) != 0 {
		t.Fatalf("plan mutated on invalid patch: %+v", got.Plan)
	}
	if !hasAssistantDiagnostic(service, "progress_apply") {
		t.Fatalf("invalid patch did not record a progress_apply diagnostic: %+v", service.Diagnostics())
	}
}

func TestAssistantCompleteRunSucceedsDiscardingCycleProgress(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "cycle")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	block := `<assistant-progress>{"responsibilities":[{"alias":"a","objective":"a","depends_on":["b"]},{"alias":"b","objective":"b","depends_on":["a"]}]}</assistant-progress>`
	service.completeRun(*claimed, nil, snapshot.Plan.Revision, assistantTurnResult{Summary: "tried", ProgressText: block}, "C:/assistant/session.jsonl")
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs[0].State != assistant.RunSucceeded {
		t.Fatalf("run state = %s, want succeeded", got.Runs[0].State)
	}
	if got.Plan.Revision != 1 || len(got.Plan.Responsibilities) != 0 {
		t.Fatalf("plan mutated on cyclic patch: %+v", got.Plan)
	}
	if !hasAssistantDiagnostic(service, "progress_apply") {
		t.Fatalf("cyclic patch did not record a progress_apply diagnostic: %+v", service.Diagnostics())
	}
}

func TestAssistantCompleteRunRebasesStalePlanRevision(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "stale")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	// Advance the plan revision so a later patch against revision 1 is stale.
	if _, err := store.CompleteRunWithProgress(assistant.CompleteRunInput{
		RequestID: "seed", RunID: claimed.ID, LeaseOwner: "desktop-test", LeaseFence: claimed.LeaseFence,
		Progress: assistant.ProgressBlock{PlanRevision: 1, Responsibilities: []assistant.RespDecl{{Alias: "a", Objective: "a"}}},
		Now:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RoutineID: snapshot.Routines[0].ID,
		RequestID: "run-stale-second", Trigger: assistant.TriggerManual, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	run2, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok || run2.ID != second.ID {
		t.Fatalf("Claim second: run=%+v ok=%v err=%v", run2, ok, err)
	}
	block := `<assistant-progress>{"responsibilities":[{"alias":"b","objective":"b"}]}</assistant-progress>`
	service.completeRun(*run2, nil, 1 /* stale */, assistantTurnResult{Summary: "tried", ProgressText: block}, "C:/assistant/session.jsonl")
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	var target assistant.Run
	for _, r := range got.Runs {
		if r.ID == run2.ID {
			target = r
		}
	}
	if target.State != assistant.RunSucceeded {
		t.Fatalf("stale revision was not rebased into success: %+v", target)
	}
	if len(got.Plan.Responsibilities) != 2 || assistantRespByAlias(got, "b") == nil {
		t.Fatalf("new alias was not applied after rebase: %+v", got.Plan)
	}
	if hasAssistantDiagnostic(service, "progress_apply") {
		t.Fatalf("rebase recorded a spurious progress_apply diagnostic: %+v", service.Diagnostics())
	}
}

func TestAssistantCompleteRunRebasesExistingAliasObjective(t *testing.T) {
	host := &assistantHostStub{}
	service, store := newAssistantTestRuntime(t, host)
	snapshot, _ := createAssistantRun(t, store, "alias-objective")
	claimed, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("Claim: %v ok=%v", err, ok)
	}
	// Seed an authoritative objective for alias "consolidate" and advance the
	// plan revision so the next patch is both stale and re-declares the alias
	// with a different objective.
	if _, err := store.CompleteRunWithProgress(assistant.CompleteRunInput{
		RequestID: "seed", RunID: claimed.ID, LeaseOwner: "desktop-test", LeaseFence: claimed.LeaseFence,
		Progress: assistant.ProgressBlock{PlanRevision: 1, Responsibilities: []assistant.RespDecl{{Alias: "consolidate", Objective: "merge duplicates"}}},
		Now:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Trigger(assistant.TriggerInput{
		AssistantID: snapshot.Assistant.ID, RoutineID: snapshot.Routines[0].ID,
		RequestID: "run-alias-objective-second", Trigger: assistant.TriggerManual, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	run2, ok, err := store.Claim("desktop-test", time.Now(), 2*time.Second)
	if err != nil || !ok || run2.ID != second.ID {
		t.Fatalf("Claim second: run=%+v ok=%v err=%v", run2, ok, err)
	}
	block := `<assistant-progress>{"responsibilities":[{"alias":"consolidate","objective":"rewrite everything"}],"complete":["consolidate"]}</assistant-progress>`
	service.completeRun(*run2, nil, 1 /* stale */, assistantTurnResult{Summary: "consolidated", ProgressText: block}, "C:/assistant/session.jsonl")
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	var target assistant.Run
	for _, r := range got.Runs {
		if r.ID == run2.ID {
			target = r
		}
	}
	if target.State != assistant.RunSucceeded {
		t.Fatalf("alias-objective conflict not rebased into success: %+v", target)
	}
	resp := assistantRespByAlias(got, "consolidate")
	if resp == nil {
		t.Fatalf("consolidate responsibility missing: %+v", got.Plan)
	}
	if resp.Objective != "merge duplicates" {
		t.Fatalf("existing objective was rewritten by the stale declaration: %+v", resp)
	}
	if resp.Status != assistant.RespDone {
		t.Fatalf("legal complete was not applied after rebase: %+v", resp)
	}
	if hasAssistantDiagnostic(service, "progress_apply") || hasAssistantDiagnostic(service, "complete_failed") {
		t.Fatalf("rebase recorded a spurious failure diagnostic: %+v", service.Diagnostics())
	}
}

func TestAssistantObserveEventCollectsRawProgressFromTextDeltas(t *testing.T) {
	service, _ := newAssistantTestRuntime(t, &assistantHostStub{})
	service.mu.Lock()
	in := &assistantInFlight{runID: "run-1", tabID: "tab-1", done: make(chan assistantTurnResult, 1)}
	service.inflight[in.tabID] = in
	service.mu.Unlock()

	// Raw answer deltas carry the protocol block; the final Message is clean.
	service.ObserveEvent("tab-1", event.Event{Kind: event.Text, Text: "Scan complete\n"})
	service.ObserveEvent("tab-1", event.Event{Kind: event.Text, Text: `<assistant-progress>{"complete":["scan"]}</assistant-progress>`})
	service.ObserveEvent("tab-1", event.Event{Kind: event.Message, Text: "Scan complete"})
	service.ObserveEvent("tab-1", event.Event{Kind: event.TurnDone})

	result := <-in.done
	if !strings.Contains(result.ProgressText, "<assistant-progress>") {
		t.Fatalf("raw protocol was not collected from text deltas: %q", result.ProgressText)
	}
	if strings.Contains(result.Summary, "<assistant-progress>") {
		t.Fatalf("stripped message leaked raw protocol into summary: %q", result.Summary)
	}
}

func hasAssistantDiagnostic(service *AssistantRuntime, operation string) bool {
	for _, d := range service.Diagnostics() {
		if d.Operation == operation && d.Category == assistantDiagnosticRuntime {
			return true
		}
	}
	return false
}

func assistantRespByAlias(snapshot assistant.Snapshot, alias string) *assistant.Responsibility {
	for i := range snapshot.Plan.Responsibilities {
		if snapshot.Plan.Responsibilities[i].Alias == alias {
			return &snapshot.Plan.Responsibilities[i]
		}
	}
	return nil
}

func waitAssistantRunState(t *testing.T, store *assistant.Store, assistantID, runID string, want assistant.RunState) assistant.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := store.Get(assistantID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		for _, run := range snapshot.Runs {
			if run.ID == runID && run.State == want {
				return run
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach %s", runID, want)
	return assistant.Run{}
}
