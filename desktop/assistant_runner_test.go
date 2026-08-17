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
	block := h.block
	h.mu.Unlock()
	if err != nil || busy {
		release()
		return false, err
	}
	h.controller.SetToolApprovalMode(control.ToolApprovalAsk)
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
	t.Helper()
	now := time.Now()
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-" + requestID,
		Assistant: assistant.Assistant{
			ID: "helper-" + requestID, Name: "Helper", Mission: "Keep the project healthy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Routines: []assistant.Routine{{
			ID: "routine-" + requestID, Title: "Scan", Prompt: "Scan now", Enabled: true,
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
	if got := fixture.tab.Ctrl.ToolApprovalMode(); got != control.ToolApprovalAsk {
		t.Fatalf("reconciled controller mode=%q, want ask", got)
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
	prompt, grants, err := service.promptFor(result.Runs[0])
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
	allowed, _, err = gate.Check(context.Background(), "bash", json.RawMessage(`{"command":"echo publish"}`), false)
	if err != nil || allowed {
		t.Fatalf("sensitive bash result: allowed=%v err=%v", allowed, err)
	}
	if approver.calls != 1 {
		t.Fatalf("sensitive action approval calls = %d, want 1", approver.calls)
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
