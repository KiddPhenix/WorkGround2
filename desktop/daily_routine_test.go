package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"workground2/internal/agent"
	"workground2/internal/control"
	"workground2/internal/fileutil"
	"workground2/internal/provider"
)

type fakeDailyRoutineGenerator struct {
	output string
	err    error
	calls  int
	mutate func()
}

func (f *fakeDailyRoutineGenerator) Generate(_ context.Context, _ dailyRoutineGenerateRequest) (string, error) {
	f.calls++
	if f.mutate != nil {
		f.mutate()
	}
	return f.output, f.err
}

func withDailyRoutineStorePath(t *testing.T) string {
	t.Helper()
	old := dailyRoutineStorePath
	path := filepath.Join(t.TempDir(), "daily-routines.json")
	dailyRoutineStorePath = func() string { return path }
	t.Cleanup(func() { dailyRoutineStorePath = old })
	return path
}

func writeDailyRoutineSession(t *testing.T, root string) string {
	t.Helper()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := agent.NewSessionPath(dir, "fake/model")
	session := agent.NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "把现有改动 commit 并合并到 main"})
	session.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "bash", Arguments: `{"cmd":"git status"}`}}})
	session.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "1", Name: "bash", Content: "clean"})
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: "已完成提交和合并。"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.Scope, meta.WorkspaceRoot = "project", normalizeProjectRoot(root)
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	return path
}

type dailyRoutineSubmitController struct {
	control.SessionAPI
	mu          sync.Mutex
	history     []provider.Message
	running     bool
	accept      bool
	transient   string
	workspace   string
	sessionDir  string
	skipHistory bool
	submissions []string
}

func (c *dailyRoutineSubmitController) History() []provider.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Message(nil), c.history...)
}

func (c *dailyRoutineSubmitController) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (c *dailyRoutineSubmitController) WorkspaceRoot() string { return c.workspace }
func (c *dailyRoutineSubmitController) SessionDir() string    { return c.sessionDir }

func (c *dailyRoutineSubmitController) TrySubmitUserTurn(input, _ string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.submissions = append(c.submissions, input)
	if !c.accept {
		return false
	}
	if !c.skipHistory {
		c.history = append(c.history, provider.Message{Role: provider.RoleUser, Content: c.transient + input})
	}
	return true
}

func setupDailyRoutineRunTab(t *testing.T, app *App, root, tabID string, ctrl *dailyRoutineSubmitController) string {
	t.Helper()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := agent.NewSessionPath(dir, "fake/model")
	session := agent.NewSession("system")
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.Scope, meta.WorkspaceRoot, meta.TopicID = "project", normalizeProjectRoot(root), "topic-"+tabID
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	ctrl.SessionAPI = carryingController(ctrl.history, path)
	ctrl.workspace, ctrl.sessionDir = normalizeProjectRoot(root), dir
	app.tabs = map[string]*WorkspaceTab{tabID: {ID: tabID, Scope: "project", WorkspaceRoot: root, TopicID: meta.TopicID, SessionPath: path, Ctrl: ctrl, Ready: true}}
	app.tabOrder = []string{tabID}
	return path
}

func dailyRoutineRef(root, path string) *DesktopIconTaskRef {
	return &DesktopIconTaskRef{Scope: "project", WorkspaceRoot: root, SessionPath: path}
}

func TestCreateDailyRoutinePersistsCompleteStructuredTemplateIdempotently(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	path := writeDailyRoutineSession(t, root)
	gen := &fakeDailyRoutineGenerator{output: `{"name":"提交并合并","goal":"安全提交并合并当前改动。","prompt":"检查当前改动，运行相关测试，提交后合并到 main；若遇到冲突则停止并显式报告。","successSteps":["检查状态","运行测试","提交并合并"],"failureLessons":["不要覆盖无关改动"]}`}
	app := NewApp()
	app.dailyRoutineGen = gen
	input := DailyRoutineSourceInput{SessionRef: dailyRoutineRef(root, path), RequestID: "extract-1"}

	first := app.CreateDailyRoutine(input)
	if first.Status != "accepted" || first.Routine == nil || first.Routine.WorkspaceRoot != normalizeProjectRoot(root) {
		t.Fatalf("first result = %+v", first)
	}
	second := app.CreateDailyRoutine(input)
	if second.Status != "already_applied" || second.Routine == nil || second.Routine.ID != first.Routine.ID || gen.calls != 1 {
		t.Fatalf("retry = %+v, calls=%d", second, gen.calls)
	}
	list, err := app.ListDailyRoutines(strings.ToUpper(root[:1]) + root[1:])
	if err != nil || len(list) != 1 || list[0].Prompt == "" || len(list[0].SuccessSteps) != 3 {
		t.Fatalf("list = %+v, err=%v", list, err)
	}
}

func TestCreateDailyRoutineRepairsCompactedCompletedReceiptWithoutGenerator(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	path := writeDailyRoutineSession(t, root)
	gen := &fakeDailyRoutineGenerator{output: `{"name":"恢复回执","goal":"完成任务","prompt":"执行已验证流程。","successSteps":[],"failureLessons":[]}`}
	app := NewApp()
	app.dailyRoutineGen = gen
	input := DailyRoutineSourceInput{SessionRef: dailyRoutineRef(root, path), RequestID: "compacted"}
	first := app.CreateDailyRoutine(input)
	if first.Status != "accepted" || gen.calls != 1 {
		t.Fatalf("first=%+v calls=%d", first, gen.calls)
	}
	store, err := loadDailyRoutineStoreLocked()
	if err != nil {
		t.Fatal(err)
	}
	store.CreateReceipts = nil // simulate bounded terminal compaction
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	retried := app.CreateDailyRoutine(input)
	if retried.Status != "already_applied" || retried.Routine == nil || retried.Routine.ID != first.Routine.ID || gen.calls != 1 {
		t.Fatalf("retry=%+v calls=%d", retried, gen.calls)
	}
	repaired, err := loadDailyRoutineStoreLocked()
	receipt := findDailyRoutineCreateReceipt(repaired.CreateReceipts, input.RequestID)
	if err != nil || receipt == nil || receipt.Status != "completed" || receipt.RoutineID != first.Routine.ID {
		t.Fatalf("repaired receipt=%+v err=%v", receipt, err)
	}
}

func TestCreateDailyRoutineParseFailureLeavesNoRoutineAndCanRetry(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	path := writeDailyRoutineSession(t, root)
	gen := &fakeDailyRoutineGenerator{output: "not-json"}
	app := NewApp()
	app.dailyRoutineGen = gen
	input := DailyRoutineSourceInput{SessionRef: dailyRoutineRef(root, path), RequestID: "extract-retry"}

	failed := app.CreateDailyRoutine(input)
	if failed.Status != "retryable_error" || !strings.Contains(failed.Error, "structured output") {
		t.Fatalf("failure = %+v", failed)
	}
	list, err := app.ListDailyRoutines(root)
	if err != nil || len(list) != 0 {
		t.Fatalf("partial routine leaked: %+v, %v", list, err)
	}
	gen.output = `{"name":"重试成功","goal":"完成任务","prompt":"重新执行经过验证的流程。","successSteps":[],"failureLessons":["解析失败要重试"]}`
	retried := app.CreateDailyRoutine(input)
	if retried.Status != "accepted" || retried.Routine == nil || gen.calls != 2 {
		t.Fatalf("retry = %+v, calls=%d", retried, gen.calls)
	}
}

func TestCreateDailyRoutineDropsLateResultAfterSourceChanges(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	path := writeDailyRoutineSession(t, root)
	gen := &fakeDailyRoutineGenerator{output: `{"name":"迟到","goal":"目标","prompt":"执行。","successSteps":[],"failureLessons":[]}`}
	gen.mutate = func() {
		loaded, err := agent.LoadSession(path)
		if err != nil {
			t.Fatal(err)
		}
		loaded.Add(provider.Message{Role: provider.RoleUser, Content: "新的迟到输入"})
		if err := loaded.SaveSnapshot(path); err != nil {
			t.Fatal(err)
		}
	}
	app := NewApp()
	app.dailyRoutineGen = gen
	result := app.CreateDailyRoutine(DailyRoutineSourceInput{SessionRef: dailyRoutineRef(root, path), RequestID: "late"})
	if result.Status != "retryable_error" || !strings.Contains(result.Error, "changed") {
		t.Fatalf("late result = %+v", result)
	}
	list, _ := app.ListDailyRoutines(root)
	if len(list) != 0 {
		t.Fatalf("late routine leaked: %+v", list)
	}
}

func TestDailyRoutineRenameConflictAndDeleteAreExplicitAndIdempotent(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	key, normalized := canonicalDailyRoutineWorkspace(root)
	store := newDailyRoutineStore()
	store.Routines = []DailyRoutine{
		{ID: "one", WorkspaceRoot: normalized, Name: "测试", Prompt: "one", UpdatedAt: 1},
		{ID: "two", WorkspaceRoot: normalized, Name: "发布", Prompt: "two", UpdatedAt: 2},
	}
	if key == "" {
		t.Fatal("missing workspace key")
	}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	conflict := app.RenameDailyRoutine(DailyRoutineRenameInput{WorkspaceRoot: root, RoutineID: "one", Name: "发布"})
	if conflict.Status != "invalid" || !strings.Contains(conflict.Error, "already exists") {
		t.Fatalf("rename conflict = %+v", conflict)
	}
	renamed := app.RenameDailyRoutine(DailyRoutineRenameInput{WorkspaceRoot: root, RoutineID: "one", Name: "启动测试"})
	if renamed.Status != "accepted" || renamed.Routine == nil || renamed.Routine.Name != "启动测试" {
		t.Fatalf("rename = %+v", renamed)
	}
	for i := 0; i < 2; i++ {
		deleted := app.DeleteDailyRoutine(DailyRoutineDeleteInput{WorkspaceRoot: root, RoutineID: "one"})
		want := "accepted"
		if i == 1 {
			want = "already_applied"
		}
		if deleted.Status != want {
			t.Fatalf("delete %d = %+v", i, deleted)
		}
	}
}

func TestDailyRoutineStoreRecoversCorruptPrimaryFromBackup(t *testing.T) {
	path := withDailyRoutineStorePath(t)
	store := newDailyRoutineStore()
	store.Routines = []DailyRoutine{{ID: "safe", Name: "Recovered", Prompt: "run", UpdatedAt: 1}}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	list, err := app.ListDailyRoutines("")
	if err != nil || len(list) != 1 || list[0].ID != "safe" {
		t.Fatalf("recovered list = %+v, err=%v", list, err)
	}
	if _, err := decodeDailyRoutineStore(mustReadDailyRoutineFile(t, path)); err != nil {
		t.Fatalf("primary was not repaired: %v", err)
	}
}

func TestRunDailyRoutineSubmittingReceiptReconcilesWithoutNewSession(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	prompt := "启动一轮测试"
	routine := DailyRoutine{ID: "run", WorkspaceRoot: normalizeProjectRoot(root), Name: "启动测试", Prompt: prompt, UpdatedAt: 1}
	requestID := "run-request"
	store := newDailyRoutineStore()
	store.Routines = []DailyRoutine{routine}
	key, _ := canonicalDailyRoutineWorkspace(root)
	store.RunReceipts = []dailyRoutineRunReceipt{{RequestID: requestID, RoutineID: routine.ID, RoutineVersion: dailyRoutineVersion(routine), WorkspaceKey: key, Status: "submitting", TabID: "existing", BaseUserTurns: 0, Delivery: string(desktopIconReplyAccepted)}}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	ctrl := &dailyRoutineSubmitController{history: []provider.Message{{Role: provider.RoleUser, Content: "<response-language>zh</response-language>\n" + prompt}}}
	setupDailyRoutineRunTab(t, app, root, "existing", ctrl)

	result := app.RunDailyRoutine(DailyRoutineRunInput{WorkspaceRoot: root, RoutineID: routine.ID, RequestID: requestID})
	if result.Status != "already_applied" || result.TabID != "existing" || len(app.tabs) != 1 || len(ctrl.submissions) != 0 {
		t.Fatalf("reconciled run = %+v, tabs=%d", result, len(app.tabs))
	}
	loaded, err := loadDailyRoutineStoreLocked()
	if err != nil || loaded.RunReceipts[0].Status != "submitted" {
		t.Fatalf("receipt = %+v, err=%v", loaded.RunReceipts, err)
	}
}

func TestRunDailyRoutineUsesAcknowledgedUserTurnForCommandLikePrompts(t *testing.T) {
	for _, prompt := range []string{"!danger", "/clear"} {
		t.Run(prompt, func(t *testing.T) {
			withDailyRoutineStorePath(t)
			root := t.TempDir()
			routine := DailyRoutine{ID: "run", WorkspaceRoot: normalizeProjectRoot(root), Name: "安全执行", Prompt: prompt, UpdatedAt: 1}
			key, _ := canonicalDailyRoutineWorkspace(root)
			store := newDailyRoutineStore()
			store.Routines = []DailyRoutine{routine}
			store.RunReceipts = []dailyRoutineRunReceipt{{RequestID: "request", RoutineID: routine.ID, RoutineVersion: dailyRoutineVersion(routine), WorkspaceKey: key, Status: "created", TabID: "existing"}}
			if err := saveDailyRoutineStoreLocked(store); err != nil {
				t.Fatal(err)
			}
			app := NewApp()
			ctrl := &dailyRoutineSubmitController{accept: true}
			setupDailyRoutineRunTab(t, app, root, "existing", ctrl)
			result := app.RunDailyRoutine(DailyRoutineRunInput{WorkspaceRoot: root, RoutineID: routine.ID, RequestID: "request"})
			if result.Status != "accepted" || len(ctrl.submissions) != 1 || ctrl.submissions[0] != prompt {
				t.Fatalf("result=%+v submissions=%q", result, ctrl.submissions)
			}
			loaded, err := loadDailyRoutineStoreLocked()
			if err != nil || loaded.RunReceipts[0].Status != "submitted" {
				t.Fatalf("receipt=%+v err=%v", loaded.RunReceipts, err)
			}
		})
	}
}

func TestRunDailyRoutineBusyStaysRetryableAndPending(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	routine := DailyRoutine{ID: "run", WorkspaceRoot: normalizeProjectRoot(root), Name: "忙态", Prompt: "continue", UpdatedAt: 1}
	key, _ := canonicalDailyRoutineWorkspace(root)
	store := newDailyRoutineStore()
	store.Routines = []DailyRoutine{routine}
	store.RunReceipts = []dailyRoutineRunReceipt{{RequestID: "request", RoutineID: routine.ID, RoutineVersion: dailyRoutineVersion(routine), WorkspaceKey: key, Status: "created", TabID: "existing"}}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	ctrl := &dailyRoutineSubmitController{running: true, accept: false}
	setupDailyRoutineRunTab(t, app, root, "existing", ctrl)
	result := app.RunDailyRoutine(DailyRoutineRunInput{WorkspaceRoot: root, RoutineID: routine.ID, RequestID: "request"})
	if result.Status != "retryable_error" || !strings.Contains(result.Error, "busy") {
		t.Fatalf("result=%+v", result)
	}
	loaded, err := loadDailyRoutineStoreLocked()
	if err != nil || loaded.RunReceipts[0].Status == "submitted" || loaded.RunReceipts[0].Delivery != "" {
		t.Fatalf("receipt=%+v err=%v", loaded.RunReceipts, err)
	}
}

func TestRunDailyRoutineAcceptedButUnconfirmedStaysPendingAndResumesWithoutDuplicate(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	routine := DailyRoutine{ID: "run", WorkspaceRoot: normalizeProjectRoot(root), Name: "异步确认", Prompt: "continue", UpdatedAt: 1}
	key, _ := canonicalDailyRoutineWorkspace(root)
	store := newDailyRoutineStore()
	store.Routines = []DailyRoutine{routine}
	store.RunReceipts = []dailyRoutineRunReceipt{{RequestID: "request", RoutineID: routine.ID, RoutineVersion: dailyRoutineVersion(routine), WorkspaceKey: key, Status: "created", TabID: "existing"}}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	ctrl := &dailyRoutineSubmitController{accept: true, skipHistory: true}
	setupDailyRoutineRunTab(t, app, root, "existing", ctrl)
	first := app.RunDailyRoutine(DailyRoutineRunInput{WorkspaceRoot: root, RoutineID: routine.ID, RequestID: "request"})
	if first.Status != "pending" || first.TabID != "existing" || len(ctrl.submissions) != 1 {
		t.Fatalf("pending result=%+v submissions=%q", first, ctrl.submissions)
	}
	loaded, err := loadDailyRoutineStoreLocked()
	if err != nil || loaded.RunReceipts[0].Status != "submitting" || loaded.RunReceipts[0].Delivery != string(desktopIconReplyAccepted) {
		t.Fatalf("pending receipt=%+v err=%v", loaded.RunReceipts, err)
	}
	stillPending := app.RunDailyRoutine(DailyRoutineRunInput{WorkspaceRoot: root, RoutineID: routine.ID, RequestID: "request"})
	if stillPending.Status != "pending" || len(ctrl.submissions) != 1 {
		t.Fatalf("accepted delivery was resubmitted: result=%+v submissions=%q", stillPending, ctrl.submissions)
	}
	ctrl.mu.Lock()
	ctrl.history = append(ctrl.history, provider.Message{Role: provider.RoleUser, Content: "<response-language>zh</response-language>\n" + routine.Prompt})
	ctrl.skipHistory = false
	ctrl.mu.Unlock()
	second := app.RunDailyRoutine(DailyRoutineRunInput{WorkspaceRoot: root, RoutineID: routine.ID, RequestID: "request"})
	if second.Status != "already_applied" || len(ctrl.submissions) != 1 {
		t.Fatalf("confirmed retry=%+v submissions=%q", second, ctrl.submissions)
	}
}

func TestRunDailyRoutineSubmittedReceiptReopensExactMissingTab(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	routine := DailyRoutine{ID: "run", WorkspaceRoot: normalizeProjectRoot(root), Name: "恢复会话", Prompt: "continue", UpdatedAt: 1}
	app := NewApp()
	ctrl := &dailyRoutineSubmitController{}
	path := setupDailyRoutineRunTab(t, app, root, "old", ctrl)
	key, _ := canonicalDailyRoutineWorkspace(root)
	store := newDailyRoutineStore()
	store.Routines = []DailyRoutine{routine}
	store.RunReceipts = []dailyRoutineRunReceipt{{
		RequestID: "request", RoutineID: routine.ID, RoutineVersion: dailyRoutineVersion(routine), WorkspaceKey: key,
		Status: "submitted", TabID: "old", Scope: "project", WorkspaceRoot: normalizeProjectRoot(root), TopicID: "topic-old", SessionPath: path,
	}}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	delete(app.tabs, "old")
	app.tabOrder = nil
	app.mu.Unlock()
	result := app.RunDailyRoutine(DailyRoutineRunInput{WorkspaceRoot: root, RoutineID: routine.ID, RequestID: "request"})
	if result.Status != "already_applied" || result.TabID == "" || result.TabID == "old" {
		t.Fatalf("recovered result=%+v", result)
	}
	app.mu.RLock()
	reopened := app.tabs[result.TabID]
	app.mu.RUnlock()
	if reopened == nil || sessionRuntimeKey(reopened.currentSessionPath()) != sessionRuntimeKey(path) {
		t.Fatalf("reopened tab=%+v want path=%s", reopened, path)
	}
}

func TestRunDailyRoutineLegacySubmittedMissingTabIsExplicit(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	routine := DailyRoutine{ID: "run", WorkspaceRoot: normalizeProjectRoot(root), Name: "旧回执", Prompt: "continue", UpdatedAt: 1}
	key, _ := canonicalDailyRoutineWorkspace(root)
	store := newDailyRoutineStore()
	store.Routines = []DailyRoutine{routine}
	store.RunReceipts = []dailyRoutineRunReceipt{{RequestID: "request", RoutineID: routine.ID, RoutineVersion: dailyRoutineVersion(routine), WorkspaceKey: key, Status: "submitted", TabID: "missing"}}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	result := NewApp().RunDailyRoutine(DailyRoutineRunInput{WorkspaceRoot: root, RoutineID: routine.ID, RequestID: "request"})
	if result.Status != "retryable_error" || !strings.Contains(result.Error, "legacy receipt") {
		t.Fatalf("legacy result=%+v", result)
	}
}

func TestDailyRoutineGeneratorFailureIsVisible(t *testing.T) {
	withDailyRoutineStorePath(t)
	root := t.TempDir()
	path := writeDailyRoutineSession(t, root)
	app := NewApp()
	app.dailyRoutineGen = &fakeDailyRoutineGenerator{err: errors.New("network down")}
	result := app.CreateDailyRoutine(DailyRoutineSourceInput{SessionRef: dailyRoutineRef(root, path), RequestID: "network"})
	if result.Status != "retryable_error" || !strings.Contains(result.Error, "network down") {
		t.Fatalf("result = %+v", result)
	}
}

func TestDailyRoutineLongHistoryKeepsTailAndRevisionCoversTruncatedMiddle(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "最初目标：整理 downloads"}}
	for i := 0; i < 20; i++ {
		messages = append(messages, provider.Message{Role: provider.RoleTool, Name: "read_file", Content: strings.Repeat(fmt.Sprintf("middle-%02d ", i), 400)})
	}
	messages = append(messages,
		provider.Message{Role: provider.RoleTool, Name: "bash", Content: "FINAL_TOOL_RESULT: 归档成功，重复文件已隔离"},
		provider.Message{Role: provider.RoleAssistant, Content: "FINAL_CONCLUSION: 下载目录整理完成；下次先做 dry-run。"},
	)
	material := dailyRoutineHistoryMaterial(messages)
	if !strings.Contains(material, "最初目标") || !strings.Contains(material, "FINAL_TOOL_RESULT") || !strings.Contains(material, "FINAL_CONCLUSION") {
		t.Fatalf("bounded material lost goal or tail: %s", material)
	}
	before := dailyRoutineHistoryRevision("session.jsonl", messages)
	// This turn is beyond the bounded prompt budget but must still invalidate a
	// late extraction through the complete-history revision.
	messages = append(messages, provider.Message{Role: provider.RoleUser, Content: strings.Repeat("late-change", 5000)})
	after := dailyRoutineHistoryRevision("session.jsonl", messages)
	if before == after {
		t.Fatal("complete-history revision ignored a late turn")
	}
}

func TestDailyRoutineHistorySplitsAssistantCallsAndTruncatesNewestRecord(t *testing.T) {
	tail := "LATEST_RESULT_END"
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "目标：启动测试"},
		{Role: provider.RoleAssistant, Content: "assistant conclusion", ToolCalls: []provider.ToolCall{{Name: "bash", Arguments: "tool arguments"}}},
		{Role: provider.RoleTool, Name: "bash", Content: strings.Repeat("x", 8<<20) + tail},
	}
	material := dailyRoutineHistoryMaterial(messages)
	splitMaterial := dailyRoutineHistoryMaterial(messages[:2])
	if !strings.Contains(splitMaterial, "[ASSISTANT]\nassistant conclusion") || !strings.Contains(splitMaterial, "[TOOL CALL bash]\ntool arguments") {
		t.Fatalf("assistant records were not split: %s", splitMaterial)
	}
	if !strings.Contains(material, "[TOOL RESULT bash]") || !strings.Contains(material, tail) {
		t.Fatalf("oversized newest record was skipped or lost its tail: %s", material[len(material)-min(len(material), 300):])
	}
	if utf8.RuneCountInString(material) > dailyRoutineSourceRunes || strings.Count(material, "x") > dailyRoutineRecordRunes {
		t.Fatalf("oversized tool result was retained before global bounding: runes=%d", utf8.RuneCountInString(material))
	}
}

func TestDailyRoutineWorkspaceLimitRejectsWithoutEvictingOtherWorkspace(t *testing.T) {
	withDailyRoutineStorePath(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	_, normalizedA := canonicalDailyRoutineWorkspace(rootA)
	_, normalizedB := canonicalDailyRoutineWorkspace(rootB)
	store := newDailyRoutineStore()
	store.Routines = append(store.Routines, DailyRoutine{ID: "keep-a", WorkspaceRoot: normalizedA, Name: "A", Prompt: "a"})
	for i := 0; i < dailyRoutineMaxCount; i++ {
		store.Routines = append(store.Routines, DailyRoutine{ID: fmt.Sprintf("b-%03d", i), WorkspaceRoot: normalizedB, Name: fmt.Sprintf("B %d", i), Prompt: "b"})
	}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatal(err)
	}
	path := writeDailyRoutineSession(t, rootB)
	gen := &fakeDailyRoutineGenerator{output: `{"name":"overflow","goal":"g","prompt":"p","successSteps":[],"failureLessons":[]}`}
	app := NewApp()
	app.dailyRoutineGen = gen
	result := app.CreateDailyRoutine(DailyRoutineSourceInput{SessionRef: dailyRoutineRef(rootB, path), RequestID: "overflow"})
	if result.Status != "invalid" || !strings.Contains(result.Error, "limit") || gen.calls != 0 {
		t.Fatalf("result=%+v calls=%d", result, gen.calls)
	}
	loaded, err := loadDailyRoutineStoreLocked()
	if err != nil || findDailyRoutine(loaded.Routines, "keep-a") == nil || len(loaded.Routines) != dailyRoutineMaxCount+1 {
		t.Fatalf("store changed: count=%d err=%v", len(loaded.Routines), err)
	}
}

func TestDailyRoutineVersionIgnoresRename(t *testing.T) {
	routine := DailyRoutine{ID: "id", Name: "before", Prompt: "execute", UpdatedAt: 1}
	before := dailyRoutineVersion(routine)
	routine.Name, routine.UpdatedAt = "after", 999
	if after := dailyRoutineVersion(routine); after != before {
		t.Fatalf("rename changed executable version: %s != %s", before, after)
	}
}

func TestDailyRoutineOfflineRefRejectsCrossWorkspacePath(t *testing.T) {
	withDailyRoutineStorePath(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	path := writeDailyRoutineSession(t, rootA)
	app := NewApp()
	app.dailyRoutineGen = &fakeDailyRoutineGenerator{}
	result := app.CreateDailyRoutine(DailyRoutineSourceInput{SessionRef: dailyRoutineRef(rootB, path), RequestID: "cross"})
	if result.Status != "retryable_error" || !strings.Contains(result.Error, "outside") {
		t.Fatalf("cross-workspace result=%+v", result)
	}
}

func TestDailyRoutineBackupFailureDoesNotReverseCommittedPrimary(t *testing.T) {
	path := withDailyRoutineStorePath(t)
	oldWrite := dailyRoutineAtomicWrite
	dailyRoutineAtomicWrite = func(name string, data []byte, mode os.FileMode) error {
		if strings.HasSuffix(name, ".bak") {
			return errors.New("backup disk full")
		}
		return fileutil.AtomicWriteFile(name, data, mode)
	}
	t.Cleanup(func() { dailyRoutineAtomicWrite = oldWrite })
	store := newDailyRoutineStore()
	store.Routines = []DailyRoutine{{ID: "committed", Name: "kept", Prompt: "run"}}
	if err := saveDailyRoutineStoreLocked(store); err != nil {
		t.Fatalf("post-commit backup failure leaked as operation failure: %v", err)
	}
	loaded, err := decodeDailyRoutineStore(mustReadDailyRoutineFile(t, path))
	if err != nil || findDailyRoutine(loaded.Routines, "committed") == nil {
		t.Fatalf("primary not committed: %+v err=%v", loaded, err)
	}
}

func TestDailyRoutineOperationLocksOnlySameRequest(t *testing.T) {
	app := NewApp()
	releaseFirst := app.lockDailyRoutineOp("create:same")
	sameAcquired := make(chan struct{})
	go func() {
		release := app.lockDailyRoutineOp("create:same")
		close(sameAcquired)
		release()
	}()
	differentAcquired := make(chan struct{})
	go func() {
		release := app.lockDailyRoutineOp("run:other")
		close(differentAcquired)
		release()
	}()
	select {
	case <-differentAcquired:
	case <-time.After(time.Second):
		t.Fatal("unrelated operation was globally blocked")
	}
	select {
	case <-sameAcquired:
		t.Fatal("same request was not serialized")
	default:
	}
	releaseFirst()
	select {
	case <-sameAcquired:
	case <-time.After(time.Second):
		t.Fatal("same request did not resume")
	}
}

func TestTrimDailyRoutineRunReceiptsEvictsOldSubmittedAndKeepsRecoverable(t *testing.T) {
	store := newDailyRoutineStore()
	store.RunReceipts = append(store.RunReceipts,
		dailyRoutineRunReceipt{RequestID: "created", Status: "created", UpdatedAt: 1},
		dailyRoutineRunReceipt{RequestID: "submitting", Status: "submitting", UpdatedAt: 2},
	)
	for i := 0; i < dailyRoutineReceiptLimit+20; i++ {
		store.RunReceipts = append(store.RunReceipts, dailyRoutineRunReceipt{RequestID: fmt.Sprintf("done-%03d", i), Status: "submitted", UpdatedAt: int64(100 + i)})
	}
	trimDailyRoutineRunReceipts(&store)
	if len(store.RunReceipts) != dailyRoutineReceiptLimit+2 {
		t.Fatalf("receipt count = %d, want %d", len(store.RunReceipts), dailyRoutineReceiptLimit+2)
	}
	if findDailyRoutineRunReceipt(store.RunReceipts, "created") == nil || findDailyRoutineRunReceipt(store.RunReceipts, "submitting") == nil {
		t.Fatal("trim removed a recoverable nonterminal receipt")
	}
	if findDailyRoutineRunReceipt(store.RunReceipts, "done-000") != nil || findDailyRoutineRunReceipt(store.RunReceipts, fmt.Sprintf("done-%03d", dailyRoutineReceiptLimit+19)) == nil {
		t.Fatal("trim did not evict the oldest submitted receipts first")
	}
}

func mustReadDailyRoutineFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
