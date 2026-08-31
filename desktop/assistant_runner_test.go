package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/permission"
	"workground2/internal/tool/sessiontool"
)

// recordingSessionControl is a headless SessionControl fake that records Create
// intents without touching the desktop tab/session registry.
type recordingSessionControl struct {
	mu      sync.Mutex
	creates []sessiontool.SessionCreateRequest
}

func (c *recordingSessionControl) Create(req sessiontool.SessionCreateRequest) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creates = append(c.creates, req)
	return "session-" + strconv.Itoa(len(c.creates)), nil
}
func (c *recordingSessionControl) Steer(string, string, string) error { return nil }
func (c *recordingSessionControl) AnswerQuestion(string, string, []event.AskAnswer, string) error {
	return nil
}
func (c *recordingSessionControl) Cancel(string, string) error { return nil }
func (c *recordingSessionControl) Resume(string, string) error { return nil }
func (c *recordingSessionControl) Retry(string, string) error  { return nil }
func (c *recordingSessionControl) Fork(string, string) (string, error) {
	return "", nil
}
func (c *recordingSessionControl) PendingInteractions(string) ([]sessiontool.SessionInteraction, error) {
	return nil, nil
}
func (c *recordingSessionControl) created() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.creates)
}

func (c *recordingSessionControl) createdRequest(index int) sessiontool.SessionCreateRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates[index]
}

var _ sessiontool.SessionControl = (*recordingSessionControl)(nil)

// testSupervisorHost is the minimal host fake the test runtime wires into the
// shared supervisor executor: it never creates real tabs/controllers, so tests
// exercise the acting-phase delegates against injected SessionControl fakes.
type testSupervisorHost struct{}

func (testSupervisorHost) FindSupervisorSession(string) (assistant.SupervisorSessionRef, bool) {
	return assistant.SupervisorSessionRef{}, false
}
func (testSupervisorHost) EnsureSupervisorSession(a assistant.Assistant) (assistant.SupervisorSessionRef, error) {
	return assistant.SupervisorSessionRef{ID: "supervisor-" + a.ID}, nil
}
func (testSupervisorHost) ManagedSessions(string) []assistant.ManagedSessionSummary { return nil }
func (testSupervisorHost) SupervisorHistoryLen(assistant.SupervisorSessionRef) (int, error) {
	return 0, nil
}
func (testSupervisorHost) RunSupervisorTurn(ref assistant.SupervisorSessionRef, prompt string, budget time.Duration) assistant.SupervisorTurnOutcome {
	return assistant.SupervisorTurnOutcome{}
}
func (testSupervisorHost) SettleSupervisorTurn(ref assistant.SupervisorSessionRef) assistant.SupervisorTurnOutcome {
	return assistant.SupervisorTurnOutcome{}
}

func newAssistantTestRuntime(t *testing.T) (*AssistantRuntime, *assistant.Store) {
	t.Helper()
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	scheduler, err := assistant.NewScheduler(store)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	roleModel := assistant.RoleModelFunc(func(_ context.Context, prompt string) (string, error) {
		switch {
		case strings.Contains(prompt, "Reflector"):
			return `{"conclusion":"done","evidence":["tests passed"]}`, nil
		case strings.Contains(prompt, "Ideator"):
			return `{"summary":"换个发布策略"}`, nil
		default:
			return `{"kind":"task","reply":"收到，我来处理。","jobs":[{"name":"execute","kind":"task","prompt":"请扫描项目最近修改并跑测试"}]}`, nil
		}
	})
	dispatcher, err := assistant.NewDispatcher(store, roleModel)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	reflector, err := assistant.NewReflector(store, roleModel)
	if err != nil {
		t.Fatalf("NewReflector: %v", err)
	}
	ideator, err := assistant.NewIdeator(store, roleModel)
	if err != nil {
		t.Fatalf("NewIdeator: %v", err)
	}
	service := &AssistantRuntime{
		store: store, scheduler: scheduler, dispatcher: dispatcher,
		reflector: reflector, ideator: ideator, tick: 10 * time.Millisecond,
		wake: make(chan struct{}, 1),
	}
	// The shared supervisor executor is part of the runtime; its hooks resolve
	// sessionControl/autoAnswer/trialStatus lazily so tests can inject fakes.
	events, err := assistant.NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatalf("NewSupervisorEventQueue: %v", err)
	}
	executor, err := assistant.NewSupervisorExecutor(assistant.SupervisorExecutorOptions{
		Store:  store,
		Events: events,
		Host:   testSupervisorHost{},
		Control: func() assistant.SessionControl {
			return supervisorSessionControl{inner: service.sessionCreator()}
		},
		AutoAnswer: func() *assistant.AutoAnswer { return service.autoAnswer },
		TrialStatus: func() assistant.TrialStatusResolver {
			return service.trialStatus
		},
		Diagnostic: service.recordDiagnostic,
		Wake:       service.Wake,
	})
	if err != nil {
		t.Fatalf("NewSupervisorExecutor: %v", err)
	}
	service.executor = executor
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

func TestEnsureAssistantSessionMetaStampsAssistSource(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "assistant-session.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureAssistantSessionMeta(sessionPath, "assistant-source"); err != nil {
		t.Fatal(err)
	}
	if err := ensureAssistantSessionMeta(sessionPath, "assistant-source"); err != nil {
		t.Fatalf("idempotent restamp: %v", err)
	}
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta: ok=%v err=%v", ok, err)
	}
	if meta.SessionKind != agent.SessionKindAssistant || meta.SessionSource != agent.SessionSourceAssist || meta.AssistantID != "assistant-source" {
		t.Fatalf("assistant identity = kind %q source %q id %q", meta.SessionKind, meta.SessionSource, meta.AssistantID)
	}
	if meta.ToolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("assistant approval mode = %q, want auto", meta.ToolApprovalMode)
	}
}

func TestAssistantRuntimeStartStopAreIdempotent(t *testing.T) {
	service, _ := newAssistantTestRuntime(t)
	service.Start()
	service.Start()
	service.Stop()
	service.Stop()
	if service.running.Load() {
		t.Fatal("runtime still marked running after Stop")
	}
}

// TestAssistantConvergedPathsCreateNoRunsOrJobs proves the supervisor advance,
// classified task Dispatch and routine fire paths all create managed Sessions
// (observed by the injected SessionControl) and never append snapshot.Runs or
// snapshot.Jobs.
func TestAssistantConvergedPathsCreateNoRunsOrJobs(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	control := &recordingSessionControl{}
	service.sessionControl = control

	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	now := start.Add(3*time.Hour + time.Minute)
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-converged",
		Assistant: assistant.Assistant{
			ID: "helper-converged", Name: "Helper", Mission: "Keep scanning",
			Scope: assistant.ScopeWorkspace, WorkspaceRoot: t.TempDir(), Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Routines: []assistant.Routine{{
			ID: "routine-converged", Title: "Scan", Prompt: "Scan now", Enabled: true,
			CatchUp:  assistant.CatchUpCoalesceLatest,
			Schedule: assistant.Schedule{Kind: assistant.ScheduleInterval, IntervalSeconds: 3600},
		}},
		Now: start,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Routine fire -> managed Session.
	result, err := service.scheduler.Tick(now)
	if err != nil || len(result.Fires) != 1 {
		t.Fatalf("Tick fires=%#v err=%v", result.Fires, err)
	}
	service.fireRoutineSessions(result.Fires)
	if control.created() != 1 {
		t.Fatalf("routine fire did not create a managed Session: %d", control.created())
	}
	if req := control.createdRequest(0); req.Workspace != snapshot.Assistant.WorkspaceRoot || req.IntentPrompt != "Scan now" || req.Prompt == req.IntentPrompt || !strings.Contains(req.Prompt, "memory_search") {
		t.Fatalf("routine managed context = %#v", req)
	}

	// Supervisor advance -> managed Session.
	a := snapshot.Assistant
	if err := store.RecordProgress(assistant.RecordProgressInput{
		RequestID: "plan-converged", AssistantID: a.ID,
		Progress: assistant.ProgressBlock{
			PlanRevision:     snapshot.Plan.Revision,
			Responsibilities: []assistant.RespDecl{{Alias: "scan", Objective: "Scan", NextAction: "run the scan"}},
		},
		Now: time.Now(),
	}); err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}
	service.advanceResponsibility(a, "scan")
	if control.created() != 2 {
		t.Fatalf("supervisor advance did not create a managed Session: %d", control.created())
	}
	if req := control.createdRequest(1); req.Workspace != snapshot.Assistant.WorkspaceRoot || req.IntentPrompt != "run the scan" || req.Prompt == req.IntentPrompt || !strings.Contains(req.Prompt, "责任图") {
		t.Fatalf("responsibility managed context = %#v", req)
	}

	// Task Dispatch -> managed Session (classify then advance).
	if _, err := store.OpenDispatch(assistant.OpenDispatchInput{
		AssistantID: a.ID, RequestID: "dispatch-converged", Input: "请扫描项目最近修改并跑测试", Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	classifySnapshot, _ := store.Get(a.ID)
	if err := service.classifyPending(context.Background(), classifySnapshot); err != nil {
		t.Fatal(err)
	}
	service.advanceClassifiedDispatches(time.Now())
	if control.created() != 3 {
		t.Fatalf("task dispatch did not create a managed Session: %d", control.created())
	}
	if req := control.createdRequest(2); req.Workspace != snapshot.Assistant.WorkspaceRoot || req.IntentPrompt != "请扫描项目最近修改并跑测试" || req.Prompt == req.IntentPrompt || !strings.Contains(req.Prompt, "Assistant：Helper") {
		t.Fatalf("dispatch managed context = %#v", req)
	}

	final, err := store.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Runs) != 0 {
		t.Fatalf("converged paths created %d Runs", len(final.Runs))
	}
	if len(final.Jobs) != 0 {
		t.Fatalf("converged paths created %d Jobs", len(final.Jobs))
	}
	if final.Dispatches[0].SessionID == "" {
		t.Fatalf("task dispatch was not bound to a Session: %+v", final.Dispatches[0])
	}
}

// TestAssistantRetryAndCancelJobReturnExplicitError proves the legacy Job write
// APIs are rejected loudly rather than silently accepted.
func TestAssistantRetryAndCancelJobReturnExplicitError(t *testing.T) {
	service, _ := newAssistantTestRuntime(t)
	app := &App{assistant: service}
	_, err := app.AssistantRetryJob(AssistantRetryJobRequest{JobID: "job-1", RequestID: "retry-1"})
	if err == nil || !strings.Contains(err.Error(), "旧 Job 路径已停用") {
		t.Fatalf("AssistantRetryJob error = %v, want explicit decommission error", err)
	}
	_, err = app.AssistantCancelJob(AssistantCancelJobRequest{JobID: "job-1", RequestID: "cancel-1", Reason: "stale"})
	if err == nil || !strings.Contains(err.Error(), "旧 Job 路径已停用") {
		t.Fatalf("AssistantCancelJob error = %v, want explicit decommission error", err)
	}
}

// TestAssistantHistoricalRunsJobsRemainReadable proves historical Runs and Jobs
// are still readable through Store.Get even though no new path creates them.
func TestAssistantHistoricalRunsJobsRemainReadable(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	_ = service
	snapshot, run := createAssistantRun(t, store, "historical")
	dispatch, err := store.OpenDispatch(assistant.OpenDispatchInput{
		AssistantID: snapshot.Assistant.ID, RequestID: "open-historical", Input: "collect", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("OpenDispatch: %v", err)
	}
	if _, err := store.ClassifyDispatch(assistant.ClassifyDispatchInput{
		AssistantID: snapshot.Assistant.ID,
		DispatchID:  dispatch.ID, RequestID: "classify-historical",
		Kind: assistant.DispatchTask, Reply: "ok",
		Jobs: []assistant.JobSpec{{Name: "collect", Kind: assistant.DispatchTask, Prompt: "collect"}},
		Now:  time.Now(),
	}); err != nil {
		t.Fatalf("ClassifyDispatch: %v", err)
	}
	got, err := store.Get(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 1 || got.Runs[0].ID != run.ID {
		t.Fatalf("historical Run unreadable: %+v", got.Runs)
	}
	if len(got.Jobs) != 1 || got.Jobs[0].DispatchID != dispatch.ID {
		t.Fatalf("historical Job unreadable: %+v", got.Jobs)
	}
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

// experimentSessionControl is a headless SessionControl fake for the experiment
// trial path: Fork returns sequential fork IDs, AnswerQuestion and Cancel record
// their targets, and a status map simulates the Session subsystem's derived
// status per fork.
type experimentSessionControl struct {
	mu        sync.Mutex
	nextFork  int
	answered  map[string][]event.AskAnswer
	cancelled []string
	statuses  map[string]string
}

func newExperimentSessionControl() *experimentSessionControl {
	return &experimentSessionControl{answered: map[string][]event.AskAnswer{}, statuses: map[string]string{}}
}

func (c *experimentSessionControl) Fork(string, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextFork++
	return "fork-" + strconv.Itoa(c.nextFork), nil
}
func (c *experimentSessionControl) AnswerQuestion(sessionID, _ string, answers []event.AskAnswer, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answered[sessionID] = append(c.answered[sessionID], answers...)
	return nil
}
func (c *experimentSessionControl) Cancel(sessionID, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled = append(c.cancelled, sessionID)
	return nil
}
func (c *experimentSessionControl) Create(sessiontool.SessionCreateRequest) (string, error) {
	return "", nil
}
func (c *experimentSessionControl) Steer(string, string, string) error { return nil }
func (c *experimentSessionControl) Resume(string, string) error        { return nil }
func (c *experimentSessionControl) Retry(string, string) error         { return nil }
func (c *experimentSessionControl) PendingInteractions(string) ([]sessiontool.SessionInteraction, error) {
	return nil, nil
}

func (c *experimentSessionControl) setStatus(id, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[id] = status
}
func (c *experimentSessionControl) statusFor(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.statuses[id]
	return st, ok
}
func (c *experimentSessionControl) answerFor(id string) []event.AskAnswer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]event.AskAnswer(nil), c.answered[id]...)
}
func (c *experimentSessionControl) cancelledIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.cancelled...)
}

// forkedCount reports how many forks were created (0 for a direct answer or a
// hard gate).
func (c *experimentSessionControl) forkedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextFork
}

var _ sessiontool.SessionControl = (*experimentSessionControl)(nil)

func TestExperimentDecisionForksMultipleCandidateTrials(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	control := newExperimentSessionControl()
	service.sessionControl = control

	autoAnswer, err := assistant.NewAutoAnswer(assistant.RoleModelFunc(func(_ context.Context, _ string) (string, error) {
		return `{"selected":["Staging"],"confidence":0.5,"rationale":"uncertain"}`, nil
	}))
	if err != nil {
		t.Fatalf("NewAutoAnswer: %v", err)
	}
	service.autoAnswer = autoAnswer

	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-experiment",
		Assistant: assistant.Assistant{
			ID: "helper-experiment", Name: "Helper", Mission: "Deploy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now()
	item := sessiontool.SessionInteraction{
		Kind: "ask", ID: "ask-1", DueAt: now.Add(-time.Minute),
		Questions: []event.AskQuestion{{
			ID:      "q1",
			Prompt:  "Which environment?",
			Options: []event.AskOption{{Label: "Staging"}, {Label: "Production"}},
		}},
	}
	service.autoAnswerInteraction(snapshot.Assistant, "sess-1", item, now)

	rec, ok, err := store.LatestDecision("helper-experiment", "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if rec.Source != assistant.DecisionExperiment {
		t.Fatalf("source = %s, want experiment", rec.Source)
	}
	if len(rec.Trials) < 2 {
		t.Fatalf("trials = %d, want >= 2", len(rec.Trials))
	}
	if rec.Result != "running" {
		t.Fatalf("result = %s, want running", rec.Result)
	}
	for _, tr := range rec.Trials {
		if tr.Status != assistant.TrialStatusRunning {
			t.Fatalf("trial %s status = %s, want running", tr.SessionID, tr.Status)
		}
		if tr.SessionID == "" {
			t.Fatal("trial missing session id")
		}
		answers, err := assistant.DecodeTrialAnswer(tr.Answer)
		if err != nil || len(answers) != 1 || answers[0].QuestionID != "q1" {
			t.Fatalf("trial answer = %+v err=%v", answers, err)
		}
	}
}

func TestExperimentWinnerAnswersOriginalAndCancelsLosers(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	control := newExperimentSessionControl()
	service.sessionControl = control
	service.trialStatus = control.statusFor

	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-experiment-winner",
		Assistant: assistant.Assistant{
			ID: "helper-experiment-winner", Name: "Helper", Mission: "Deploy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = store.RecordInteractionDecision(assistant.InteractionDecisionRecord{
		ID:            assistant.StableID("decision", "helper-experiment-winner/sess-1/ask-1/experiment"),
		AssistantID:   snapshot.Assistant.ID,
		SessionID:     "sess-1",
		InteractionID: "ask-1",
		Source:        assistant.DecisionExperiment,
		Trials: []assistant.TrialState{
			{SessionID: "fork-1", Answer: assistant.EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Staging"}}}), Status: assistant.TrialStatusRunning},
			{SessionID: "fork-2", Answer: assistant.EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Production"}}}), Status: assistant.TrialStatusRunning},
		},
		Result:    "running",
		CreatedAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("RecordInteractionDecision: %v", err)
	}
	control.setStatus("fork-1", assistant.TrialStatusRunning)
	control.setStatus("fork-2", assistant.TrialStatusDone)

	service.resolveExperimentTrials()

	got := control.answerFor("sess-1")
	if len(got) != 1 || len(got[0].Selected) != 1 || got[0].Selected[0] != "Production" {
		t.Fatalf("original session answered with %+v, want Production", got)
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 1 || cancelled[0] != "fork-1" {
		t.Fatalf("cancelled = %v, want [fork-1]", cancelled)
	}

	latest, ok, err := store.LatestDecision(snapshot.Assistant.ID, "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if latest.Result != "answered:fork-2" {
		t.Fatalf("result = %s, want answered:fork-2", latest.Result)
	}
	if latest.Rollback != "fork-1" {
		t.Fatalf("rollback = %s, want fork-1", latest.Rollback)
	}
	for _, tr := range latest.Trials {
		if tr.SessionID == "fork-2" && tr.Status != assistant.TrialStatusDone {
			t.Fatalf("winner trial status = %s, want done", tr.Status)
		}
	}

	// Idempotency: a second sweep must not re-answer or re-cancel.
	service.resolveExperimentTrials()
	if got := control.answerFor("sess-1"); len(got) != 1 {
		t.Fatalf("re-sweep answered %d times, want 1", len(got))
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 1 {
		t.Fatalf("re-sweep cancelled %d times, want 1", len(cancelled))
	}
}

// TestExperimentAllFailedFallbackAnswersSafest proves the desktop host settles
// an experiment whose candidates all failed with the most rollback-safe
// inferred answer (never a permanently pending original session).
func TestExperimentAllFailedFallbackAnswersSafest(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	control := newExperimentSessionControl()
	service.sessionControl = control
	service.trialStatus = control.statusFor

	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-experiment-fallback",
		Assistant: assistant.Assistant{
			ID: "helper-experiment-fallback", Name: "Helper", Mission: "Deploy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = store.RecordInteractionDecision(assistant.InteractionDecisionRecord{
		ID:            assistant.StableID("decision", "helper-experiment-fallback/sess-1/ask-1/experiment"),
		AssistantID:   snapshot.Assistant.ID,
		SessionID:     "sess-1",
		InteractionID: "ask-1",
		Source:        assistant.DecisionExperiment,
		Trials: []assistant.TrialState{
			{SessionID: "fork-1", Answer: assistant.EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Staging"}}}), Status: assistant.TrialStatusRunning},
			{SessionID: "fork-2", Answer: assistant.EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Production"}}}), Status: assistant.TrialStatusRunning},
		},
		Result:    "running",
		CreatedAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordInteractionDecision: %v", err)
	}
	control.setStatus("fork-1", assistant.TrialStatusFailed)
	control.setStatus("fork-2", assistant.TrialStatusFailed)

	service.resolveExperimentTrials()

	got := control.answerFor("sess-1")
	if len(got) != 1 || len(got[0].Selected) != 1 || got[0].Selected[0] != "Staging" {
		t.Fatalf("fallback answered with %+v, want the inferred candidate Staging", got)
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 2 {
		t.Fatalf("cancelled = %v, want both failed forks", cancelled)
	}
	latest, ok, err := store.LatestDecision(snapshot.Assistant.ID, "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if latest.Result != "answered-fallback" || latest.Winner != "" {
		t.Fatalf("result = %s winner=%q, want answered-fallback with no winner", latest.Result, latest.Winner)
	}

	// A duplicate sweep must not fall back twice.
	service.resolveExperimentTrials()
	if got := control.answerFor("sess-1"); len(got) != 1 {
		t.Fatalf("re-sweep fallback answered %d times, want 1", len(got))
	}
}

// TestExperimentTimeoutFallsBackSafest proves forks still running past the
// experiment max age are timed out (terminal) and the original session is
// answered with the safest candidate instead of waiting forever.
func TestExperimentTimeoutFallsBackSafest(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	control := newExperimentSessionControl()
	service.sessionControl = control
	service.trialStatus = control.statusFor

	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-experiment-timeout",
		Assistant: assistant.Assistant{
			ID: "helper-experiment-timeout", Name: "Helper", Mission: "Deploy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The experiment started an hour ago; the default max age (15 minutes)
	// makes both still-running forks timed out on the first sweep.
	_, err = store.RecordInteractionDecision(assistant.InteractionDecisionRecord{
		ID:            assistant.StableID("decision", "helper-experiment-timeout/sess-1/ask-1/experiment"),
		AssistantID:   snapshot.Assistant.ID,
		SessionID:     "sess-1",
		InteractionID: "ask-1",
		Source:        assistant.DecisionExperiment,
		Trials: []assistant.TrialState{
			{SessionID: "fork-1", Answer: assistant.EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Staging"}}}), Status: assistant.TrialStatusRunning},
			{SessionID: "fork-2", Answer: assistant.EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Production"}}}), Status: assistant.TrialStatusRunning},
		},
		Result:    "running",
		CreatedAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("RecordInteractionDecision: %v", err)
	}
	control.setStatus("fork-1", assistant.TrialStatusRunning)
	control.setStatus("fork-2", assistant.TrialStatusRunning)

	service.resolveExperimentTrials()

	got := control.answerFor("sess-1")
	if len(got) != 1 || got[0].Selected[0] != "Staging" {
		t.Fatalf("timeout fallback answered with %+v, want the inferred candidate Staging", got)
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 2 {
		t.Fatalf("cancelled = %v, want both timed-out forks", cancelled)
	}
	latest, ok, _ := store.LatestDecision(snapshot.Assistant.ID, "sess-1", "ask-1")
	if !ok || latest.Result != "answered-fallback" {
		t.Fatalf("result = %q, want answered-fallback", latest.Result)
	}
	if !strings.Contains(latest.Cost, "timed_out=2") {
		t.Fatalf("cost = %q, want the timed-out count", latest.Cost)
	}
}

// TestExperimentHardGateWaitsForUser proves the desktop host never auto-answers
// a hard-gate interaction: no model turn, no fork, no answer on the original
// session; the decision waits for the user.
func TestExperimentHardGateWaitsForUser(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	control := newExperimentSessionControl()
	service.sessionControl = control
	service.autoAnswer, _ = assistant.NewAutoAnswer(assistant.RoleModelFunc(func(_ context.Context, _ string) (string, error) {
		t.Fatal("auto-answer model was consulted for a hard gate")
		return "", nil
	}))

	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-experiment-hardgate",
		Assistant: assistant.Assistant{
			ID: "helper-experiment-hardgate", Name: "Helper", Mission: "Deploy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now()
	item := sessiontool.SessionInteraction{
		Kind: "ask", ID: "ask-1", DueAt: now.Add(-time.Minute),
		Questions: []event.AskQuestion{{
			ID: "q1", Prompt: "请提供密码以继续", Multi: false,
			Options: []event.AskOption{{Label: "我提供"}, {Label: "稍后"}},
		}},
	}
	service.autoAnswerInteraction(snapshot.Assistant, "sess-1", item, now)

	if got := control.answerFor("sess-1"); len(got) != 0 {
		t.Fatalf("hard gate auto-answered: %+v", got)
	}
	if forked := control.forkedCount(); forked != 0 {
		t.Fatalf("hard gate forked %d candidates", forked)
	}
	latest, ok, err := store.LatestDecision(snapshot.Assistant.ID, "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if latest.Source != assistant.DecisionUser || latest.HardGate == "" || latest.Result != "wait_for_user" {
		t.Fatalf("decision = %s gate=%s result=%s, want user/wait_for_user", latest.Source, latest.HardGate, latest.Result)
	}
}
