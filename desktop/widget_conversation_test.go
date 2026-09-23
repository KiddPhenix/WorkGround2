package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"workground2/internal/autoresearch"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/provider"
)

func TestRetryWidgetConversationRetriesFiveTimesWithSameInput(t *testing.T) {
	input := WidgetConversationInput{Prompt: "fix it", RequestID: "req-1", Workspace: "global"}
	var calls []WidgetConversationInput
	var delays []time.Duration

	result := retryWidgetConversation(input, func(got WidgetConversationInput) WidgetConversationResult {
		calls = append(calls, got)
		return WidgetConversationResult{Status: "retryable_error", Error: "timeout"}
	}, func(delay time.Duration) {
		delays = append(delays, delay)
	})

	if result.Status != "retryable_error" || result.Error != "timeout" {
		t.Fatalf("result = %+v, want final retryable error", result)
	}
	if len(calls) != 6 {
		t.Fatalf("calls = %d, want 6 (initial + 5 retries)", len(calls))
	}
	for i, got := range calls {
		if !reflect.DeepEqual(got, input) {
			t.Fatalf("call %d input = %+v, want %+v", i, got, input)
		}
	}
	wantDelays := []time.Duration{
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
		3200 * time.Millisecond,
	}
	if !reflect.DeepEqual(delays, wantDelays) {
		t.Fatalf("delays = %v, want %v", delays, wantDelays)
	}
}

func TestRetryWidgetConversationStopsAfterSuccess(t *testing.T) {
	calls := 0
	result := retryWidgetConversation(WidgetConversationInput{RequestID: "req-2"}, func(WidgetConversationInput) WidgetConversationResult {
		calls++
		if calls < 3 {
			return WidgetConversationResult{Status: "retryable_error"}
		}
		return WidgetConversationResult{Status: "accepted"}
	}, func(time.Duration) {})

	if result.Status != "accepted" || calls != 3 {
		t.Fatalf("result = %+v, calls = %d; want accepted after 3 calls", result, calls)
	}
}

func TestRetryWidgetConversationDoesNotRetryTerminalResult(t *testing.T) {
	for _, status := range []string{"accepted", "already_applied", "invalid", "unknown"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			result := retryWidgetConversation(WidgetConversationInput{}, func(WidgetConversationInput) WidgetConversationResult {
				calls++
				return WidgetConversationResult{Status: status}
			}, func(time.Duration) { t.Fatal("terminal result must not sleep") })
			if result.Status != status || calls != 1 {
				t.Fatalf("result = %+v, calls = %d; want one terminal call", result, calls)
			}
		})
	}
}

func TestApplyWidgetConversationDefaultsRefreshesReusableBlankTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.Default()
	cfg.DefaultModel = "new/new-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:               "blank",
		Scope:            "global",
		WorkspaceRoot:    globalWorkspaceRoot(),
		model:            "old/old-model",
		toolApprovalMode: control.ToolApprovalAsk,
		disabledMCP:      map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.applyWidgetConversationDefaults(tab.ID, "new/new-model", control.ToolApprovalAuto); err != nil {
		t.Fatalf("applyWidgetConversationDefaults: %v", err)
	}
	if tab.pendingModel != "new/new-model" {
		t.Fatalf("pending model = %q, want user default", tab.pendingModel)
	}
	if tab.toolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("approval mode = %q, want user default auto", tab.toolApprovalMode)
	}
}

func TestWidgetConversationRejectsUnknownModelSelection(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()

	result := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-model", Workspace: "global",
		Model: "ghost/ghost-model",
	})
	if result.Status != "invalid" {
		t.Fatalf("status = %q, want invalid for an unconfigured model", result.Status)
	}
	if result.Error == "" {
		t.Fatal("missing model must surface an explicit error")
	}
}

func TestWidgetConversationModelApprovalGateIsIdempotentAndStrict(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()

	seed := widgetConversationReceipt{
		RequestID: "req-gate", PromptHash: fmt.Sprintf("%x", sha256.Sum256([]byte("fix it"))),
		WorkspaceSelection: "global", Model: "deepseek/deepseek-v4", ToolApprovalMode: control.ToolApprovalAuto,
		Scope: "global", WorkspaceName: "Global", Status: "submitted",
	}
	if err := app.saveWidgetConversationReceipt(seed); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	// Same intent retry stays idempotent (the gate accepts the exact retry).
	same := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-gate", Workspace: "global",
		Model: "deepseek/deepseek-v4", ApprovalMode: control.ToolApprovalAuto,
	})
	if same.Status != "already_applied" {
		t.Fatalf("same-intent retry = %+v, want already_applied", same)
	}

	// A model change on the same requestId is an explicit error, never a silent
	// reuse of the old selection.
	changedModel := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-gate", Workspace: "global",
		Model: "deepseek/deepseek-v5", ApprovalMode: control.ToolApprovalAuto,
	})
	if changedModel.Status != "invalid" {
		t.Fatalf("changed model = %+v, want invalid", changedModel)
	}

	// An approval change on the same requestId is rejected too.
	changedApproval := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-gate", Workspace: "global",
		Model: "deepseek/deepseek-v4", ApprovalMode: control.ToolApprovalYolo,
	})
	if changedApproval.Status != "invalid" {
		t.Fatalf("changed approval = %+v, want invalid", changedApproval)
	}
}

func TestWidgetConversationEmptyModelRetryDoesNotDeadlock(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()

	// First attempt had no usable model (empty selection), so the receipt was
	// filled with the user defaults. The retry still sends model:"" — the gate
	// must treat an empty selection as "use defaults", never as a change.
	seed := widgetConversationReceipt{
		RequestID: "req-empty-model", PromptHash: fmt.Sprintf("%x", sha256.Sum256([]byte("fix it"))),
		WorkspaceSelection: "global", Model: "default/default-model", ToolApprovalMode: control.ToolApprovalAuto,
		Scope: "global", WorkspaceName: "Global", Status: "submitted",
	}
	if err := app.saveWidgetConversationReceipt(seed); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	retry := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: "req-empty-model", Workspace: "global",
		ApprovalMode: control.ToolApprovalAuto,
	})
	if retry.Status != "already_applied" {
		t.Fatalf("empty-model retry = %+v, want already_applied (no deadlock)", retry)
	}
}

func TestWidgetModelRefExists(t *testing.T) {
	models := []ModelInfo{
		{Ref: "deepseek/deepseek-v4", Provider: "deepseek", Model: "deepseek-v4"},
		{Ref: "openai/gpt-5", Provider: "openai", Model: "gpt-5"},
	}
	for _, ref := range []string{"deepseek/deepseek-v4", "openai/gpt-5"} {
		if !widgetModelRefExists(models, ref) {
			t.Fatalf("ref %q should resolve", ref)
		}
	}
	for _, ref := range []string{"", "  ", "ghost/ghost", "deepseek/ deepseek-v4"} {
		if widgetModelRefExists(models, ref) {
			t.Fatalf("ref %q must not resolve", ref)
		}
	}
}

func TestWidgetApprovalModePreservesOptionalDefault(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"", ""},
		{"  ", ""},
		{control.ToolApprovalAsk, control.ToolApprovalAsk},
		{control.ToolApprovalAuto, control.ToolApprovalAuto},
		{control.ToolApprovalYolo, control.ToolApprovalYolo},
	} {
		got, err := widgetApprovalMode(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("widgetApprovalMode(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	if _, err := widgetApprovalMode("sometimes"); err == nil {
		t.Fatal("unknown approval mode must fail explicitly")
	}
}

// fakeWidgetConversationCtrl is a minimal control.SessionAPI double for the
// widget send path: the composer send, the tab identity resolution around it and
// the widget snapshot projection read exactly these members, so a send can be
// observed end to end without starting a real Controller or model turn.
type fakeWidgetConversationCtrl struct {
	control.SessionAPI
	root    string
	dir     string
	path    string
	history []provider.Message
	submits []string
}

func (f *fakeWidgetConversationCtrl) SessionPath() string { return f.path }
func (f *fakeWidgetConversationCtrl) SessionDir() string  { return f.dir }
func (f *fakeWidgetConversationCtrl) WorkspaceRoot() string {
	return f.root
}

func (f *fakeWidgetConversationCtrl) PlanMode() bool           { return false }
func (f *fakeWidgetConversationCtrl) AutoApproveTools() bool   { return false }
func (f *fakeWidgetConversationCtrl) ToolApprovalMode() string { return control.ToolApprovalAuto }
func (f *fakeWidgetConversationCtrl) Goal() string             { return "" }
func (f *fakeWidgetConversationCtrl) GoalStatus() string       { return control.GoalStatusStopped }
func (f *fakeWidgetConversationCtrl) Label() string            { return "fake-model" }

func (f *fakeWidgetConversationCtrl) SetToolApprovalMode(string) {}
func (f *fakeWidgetConversationCtrl) SetPlanMode(bool)           {}

func (f *fakeWidgetConversationCtrl) AutoResearchSummary() (*autoresearch.Summary, bool) {
	return nil, false
}

func (f *fakeWidgetConversationCtrl) PendingInteraction() (control.PendingInteraction, bool) {
	return control.PendingInteraction{}, false
}
func (f *fakeWidgetConversationCtrl) RuntimeStatus() control.RuntimeStatus {
	return control.RuntimeStatus{}
}

func (f *fakeWidgetConversationCtrl) History() []provider.Message {
	return append([]provider.Message(nil), f.history...)
}

func (f *fakeWidgetConversationCtrl) SubmitDisplay(_, input string) {
	f.submits = append(f.submits, input)
	f.history = append(f.history, provider.Message{Role: provider.RoleUser, Content: input})
}

// A receipt whose persisted tab is gone must never be resubmitted blindly: an
// uncertain submit stays visible instead of turning into a duplicate turn.
func TestWidgetConversationRefusesUncertainSubmitWithStaleTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()

	requestID := "req-stale-submitting"
	seed := widgetConversationReceipt{
		RequestID: requestID, PromptHash: fmt.Sprintf("%x", sha256.Sum256([]byte("fix it"))),
		WorkspaceSelection: widgetWorkspaceProject + ":", Model: "deepseek/deepseek-v4",
		ToolApprovalMode: control.ToolApprovalAuto, Scope: "project", WorkspaceName: "测试区",
		SessionName: "已有名称", TabID: "tab_gone", Status: "submitting",
	}
	if err := app.saveWidgetConversationReceipt(seed); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	result := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: requestID, Workspace: widgetWorkspaceProject + ":",
		Model: "deepseek/deepseek-v4", ApprovalMode: control.ToolApprovalAuto,
	})
	if result.Status != "invalid" {
		t.Fatalf("stale submitting result = %+v, want terminal invalid (no automatic resend)", result)
	}
	if !strings.Contains(result.Error, "状态未知") {
		t.Fatalf("error = %q, want an explicit uncertain-submit report", result.Error)
	}
	if len(app.tabs) != 0 {
		t.Fatalf("tabs = %d, want no tab created for an uncertain submit", len(app.tabs))
	}
	stored, found, err := app.widgetConversationReceipt(requestID)
	if err != nil || !found {
		t.Fatalf("receipt lookup: found=%v err=%v", found, err)
	}
	if stored.TabID != "tab_gone" || stored.Status != "submitting" {
		t.Fatalf("receipt = %+v, want the uncertain identity kept intact", stored)
	}
}

// Nothing was submitted for a receipt in the routing/naming/created states, so a
// dead tab identity may be dropped: the flow must move on to creating a fresh
// tab for the same route instead of retrying "新会话不存在" forever.
func TestWidgetConversationRecoversStaleReceiptTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()

	requestID := "req-stale-created"
	seed := widgetConversationReceipt{
		RequestID: requestID, PromptHash: fmt.Sprintf("%x", sha256.Sum256([]byte("fix it"))),
		WorkspaceSelection: widgetWorkspaceProject + ":", Model: "deepseek/deepseek-v4",
		ToolApprovalMode: control.ToolApprovalAuto, Scope: "project", WorkspaceName: "测试区",
		SessionName: "已有名称", TabID: "tab_gone", Status: "created",
	}
	if err := app.saveWidgetConversationReceipt(seed); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	// An empty project root fails the create step immediately: that is the
	// observable proof the flow moved past the stale identity without starting a
	// Controller, submitting a turn, or touching any user Session.
	result := app.startWidgetConversationOnce(WidgetConversationInput{
		Prompt: "fix it", RequestID: requestID, Workspace: widgetWorkspaceProject + ":",
		Model: "deepseek/deepseek-v4", ApprovalMode: control.ToolApprovalAuto,
	})
	if strings.Contains(result.Error, "新会话不存在") {
		t.Fatalf("stale created result = %+v, want the dead tab identity dropped", result)
	}
	if !strings.Contains(result.Error, "创建新对话") {
		t.Fatalf("error = %q, want the flow to reach the create step again", result.Error)
	}
	stored, found, err := app.widgetConversationReceipt(requestID)
	if err != nil || !found {
		t.Fatalf("receipt lookup: found=%v err=%v", found, err)
	}
	if stored.TabID != "" || stored.Status != "created" {
		t.Fatalf("receipt = %+v, want TabID cleared with the name/model/approval kept", stored)
	}
	if stored.SessionName != "已有名称" || stored.Model != "deepseek/deepseek-v4" || stored.ToolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("receipt = %+v, want name/model/approval preserved", stored)
	}
}

func TestWidgetConversationKeepsUncertainSubmitAfterNamingFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{ID: "tab-uncertain", Scope: "global"}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	seed := widgetConversationReceipt{
		RequestID: "req-uncertain", PromptHash: fmt.Sprintf("%x", sha256.Sum256([]byte("fix it"))),
		WorkspaceSelection: "global", Model: "fake/fake-model", ToolApprovalMode: control.ToolApprovalAuto,
		Scope: "global", SessionName: "已有名称", TabID: tab.ID, Status: "submitting",
	}
	if err := app.saveWidgetConversationReceipt(seed); err != nil {
		t.Fatal(err)
	}
	input := WidgetConversationInput{Prompt: "fix it", RequestID: seed.RequestID, Workspace: "global"}
	result := app.startWidgetConversationOnce(input)
	if result.Status != "retryable_error" || !strings.Contains(result.Error, "应用会话名称") {
		t.Fatalf("naming failure = %+v", result)
	}
	stored, _, err := app.widgetConversationReceipt(seed.RequestID)
	if err != nil || stored.Status != "submitting" {
		t.Fatalf("uncertain submit was downgraded: %+v, %v", stored, err)
	}
	delete(app.tabs, tab.ID)
	result = app.startWidgetConversationOnce(input)
	if result.Status != "invalid" || !strings.Contains(result.Error, "状态未知") || len(app.tabs) != 0 {
		t.Fatalf("retry must not recreate an uncertain submission: %+v", result)
	}
	// An incomplete persisted identity also cannot prove that no turn ran.
	stored.TabID = ""
	if err := app.saveWidgetConversationReceipt(stored); err != nil {
		t.Fatal(err)
	}
	result = app.startWidgetConversationOnce(input)
	if result.Status != "invalid" || len(app.tabs) != 0 {
		t.Fatalf("missing identity must not recreate an uncertain submission: %+v", result)
	}
}

// The first send of one requestId must succeed once, keep the generated name
// and the selected model/approval, and never submit the same requestId twice.
func TestWidgetConversationSendIsIdempotentWithFakeController(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.Default()
	cfg.DefaultModel = "fake/fake-model"
	cfg.Desktop.ProviderAccess = []string{"fake"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "fake", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "fake-model"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	root := t.TempDir()
	if err := addProject(root, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	topicID := "topic-send"
	if err := setTopicTitleWithSource(root, topicID, defaultTopicTitle, topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}
	path, err := createEmptySessionFile(desktopSessionDir(root), "fake/fake-model")
	if err != nil {
		t.Fatal(err)
	}
	ctrl := &fakeWidgetConversationCtrl{root: root, dir: desktopSessionDir(root), path: path}
	tab := &WorkspaceTab{
		ID: "tab-send", Scope: "project", WorkspaceRoot: root, TopicID: topicID,
		TopicTitle: defaultTopicTitle, SessionPath: path, Ctrl: ctrl,
		model: "fake/fake-model", toolApprovalMode: control.ToolApprovalAuto,
		disabledMCP: map[string]ServerView{}, sink: &tabEventSink{tabID: "tab-send"},
	}
	app := NewApp()
	app.projectTreeChangedHook = func() {}
	app.ctx = context.Background()
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	requestID := "req-send-once"
	seed := widgetConversationReceipt{
		RequestID: requestID, PromptHash: fmt.Sprintf("%x", sha256.Sum256([]byte("fix it"))),
		WorkspaceSelection: widgetWorkspaceProject + ":" + root, Model: "fake/fake-model",
		ToolApprovalMode: control.ToolApprovalAuto, Scope: "project", WorkspaceRoot: root,
		WorkspaceName: "测试区", SessionName: "首次发送", TabID: tab.ID, Status: "created",
	}
	if err := app.saveWidgetConversationReceipt(seed); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	input := WidgetConversationInput{
		Prompt: "fix it", RequestID: requestID, Workspace: widgetWorkspaceProject + ":" + root,
		Model: "fake/fake-model", ApprovalMode: control.ToolApprovalAuto,
	}

	result := app.startWidgetConversationOnce(input)
	if result.Status != "accepted" {
		t.Fatalf("first send = %+v (%s), want accepted", result, result.Error)
	}
	if result.TabID != tab.ID || result.SessionName != "首次发送" {
		t.Fatalf("result = %+v, want the named conversation on its existing tab", result)
	}
	if len(ctrl.submits) != 1 || ctrl.submits[0] != "fix it" {
		t.Fatalf("submits = %v, want exactly one turn", ctrl.submits)
	}
	if got := loadTopicTitle(root, topicID); got != "首次发送" {
		t.Fatalf("topic title = %q, want the generated name kept", got)
	}
	if got := loadSessionTitles(filepath.Dir(path))[filepath.Base(path)]; got != "首次发送" {
		t.Fatalf("session title = %q, want the generated name kept", got)
	}
	if tab.model != "fake/fake-model" || tab.toolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("tab model/approval = %q/%q, want the selected settings kept", tab.model, tab.toolApprovalMode)
	}

	retry := app.startWidgetConversationOnce(input)
	if retry.Status != "already_applied" {
		t.Fatalf("same-requestId retry = %+v, want already_applied", retry)
	}
	if len(ctrl.submits) != 1 {
		t.Fatalf("submits after retry = %v, want the same requestId submitted once", ctrl.submits)
	}
}

func TestWidgetWorkspaceCandidatesMarksPinnedTransient(t *testing.T) {
	isolateDesktopUserDirs(t)
	pinnedTransient := transientProjectRoot(t, "pinned-transient")
	unpinnedTransient := transientProjectRoot(t, "unpinned-transient")
	normal := normalProjectRoot(t, "normal")

	if err := saveProjectsFile(desktopProjectFile{
		PinnedProjects: []string{pinnedTransient},
		Projects: []desktopProject{
			{Root: pinnedTransient, Title: "Pinned shell"},
			{Root: unpinnedTransient, Title: "Unpinned shell"},
			{Root: normal, Title: "Normal"},
		},
	}); err != nil {
		t.Fatalf("save projects: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	candidates := app.widgetWorkspaceCandidates()

	byRoot := map[string]widgetWorkspaceCandidate{}
	for _, c := range candidates {
		byRoot[c.Root] = c
	}
	if c := byRoot[pinnedTransient]; !c.Transient || !c.Pinned {
		t.Fatalf("pinned transient candidate = %+v, want Transient && Pinned", c)
	}
	if c := byRoot[unpinnedTransient]; !c.Transient || c.Pinned {
		t.Fatalf("unpinned transient candidate = %+v, want Transient && !Pinned", c)
	}
	if c := byRoot[normal]; c.Transient || c.Pinned {
		t.Fatalf("normal candidate = %+v, want !Transient && !Pinned", c)
	}
}

func TestListWidgetWorkspacesIncludesOnlyPinnedTransient(t *testing.T) {
	isolateDesktopUserDirs(t)
	pinnedTransient := transientProjectRoot(t, "pinned-transient")
	unpinnedTransient := transientProjectRoot(t, "unpinned-transient")
	normal := normalProjectRoot(t, "normal")

	if err := saveProjectsFile(desktopProjectFile{
		PinnedProjects: []string{pinnedTransient},
		Projects: []desktopProject{
			{Root: pinnedTransient, Title: "Pinned shell"},
			{Root: unpinnedTransient, Title: "Unpinned shell"},
			{Root: normal, Title: "Normal"},
		},
	}); err != nil {
		t.Fatalf("save projects: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()

	// Repeating the call must stay stable (idempotent projection).
	for i := 0; i < 2; i++ {
		options := app.ListWidgetWorkspaces()
		roots := map[string]WidgetWorkspaceOption{}
		for _, opt := range options {
			roots[opt.Root] = opt
		}
		if opt, ok := roots[pinnedTransient]; !ok || !opt.Pinned {
			t.Fatalf("iteration %d: pinned transient must be listed with Pinned set: %+v", i, opt)
		}
		if _, ok := roots[unpinnedTransient]; ok {
			t.Fatalf("iteration %d: unpinned transient must stay hidden", i)
		}
		if opt, ok := roots[normal]; !ok || opt.Pinned {
			t.Fatalf("iteration %d: normal project must be listed unpinned: %+v", i, opt)
		}
	}
}

func transientProjectRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, ".WorkGround2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !widgetIsTransientRoot(root, name) {
		t.Fatalf("fixture root %s is not transient", root)
	}
	return root
}

func normalProjectRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("project"), 0o644); err != nil {
		t.Fatal(err)
	}
	if widgetIsTransientRoot(root, name) {
		t.Fatalf("fixture root %s should not be transient", root)
	}
	return root
}
