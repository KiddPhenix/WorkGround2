package main

// Regression tests for fork sessions keeping an independent title that can be
// renamed, deleted, and re-derived from the fork's own new content (issue:
// Global list kept showing the source session's old title after a fork asked a
// new question). Fork topics get an auto placeholder title derived from the
// source; once the forked session produces its own first user message, the
// ordinary auto-title promotion path updates the topic, keeping the Global row
// in sync with the header/widget previews. Manual renames still lock the title.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
)

func writeForkSessionLines(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write fork session: %v", err)
	}
	return path
}

func TestForkAutoTitleUsesOwnFirstUserMessageNotInheritedHistory(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The fork inherited one source message (index 0: the Beijing question),
	// then the user asked a brand-new question on the fork (index 1+). The
	// fork title must come from the fork's own first user message, not from
	// the inherited source question.
	forkPath := writeForkSessionLines(t, dir, "fork.jsonl", []string{
		`{"role":"system","content":"system"}`,
		`{"role":"user","content":"查一下北京接下来的天气. 列出来给我"}`,
		`{"role":"assistant","content":"北京未来七天以晴为主"}`,
		`{"role":"user","content":"天津接下来一周天气如何?"}`,
		`{"role":"assistant","content":"天津以多云为主"}`,
	})
	if err := agent.SaveBranchMeta(forkPath, agent.BranchMeta{
		CreatedAt:        time.Now().Add(-time.Minute),
		UpdatedAt:        time.Now(),
		Scope:            "global",
		ParentID:         "source-session",
		ForkTurn:         1,
		ForkMessageIndex: 3, // first three lines are inherited source history
		TopicID:          "topic_fork_auto",
		TopicTitle:       forkTopicTitle("查一下北京接下来的天气"),
	}); err != nil {
		t.Fatalf("save fork branch meta: %v", err)
	}

	// Initial fork title: auto placeholder derived from the source title.
	topicID := "topic_fork_auto"
	if err := setTopicTitleWithSource("", topicID, forkTopicTitle("查一下北京接下来的天气"), topicTitleSourceAuto); err != nil {
		t.Fatalf("seed fork title: %v", err)
	}

	updated, ok := autoTitleTopicFromSession("", topicID, forkPath)
	if !ok {
		t.Fatalf("autoTitleTopicFromSession did not update the fork topic after it gained its own content")
	}
	if !strings.HasPrefix(updated, "天津接下来一周天气如何") {
		t.Fatalf("fork topic title = %q, want its own first question (天津...), not inherited history", updated)
	}
	if got := loadTopicTitle("", topicID); got != updated {
		t.Fatalf("stored title = %q, want %q", got, updated)
	}
	if got := loadTopicTitleSource("", topicID); got != topicTitleSourceAuto {
		t.Fatalf("title source = %q, want auto", got)
	}
}

func TestForkAutoTitleKeepsPlaceholderWhenForkHasNoOwnContent(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Fork that inherited source history but has not typed anything of its
	// own yet: the inherited history must not be promoted to the fork title
	// (that would re-title the branch with the parent's question).
	forkPath := writeForkSessionLines(t, dir, "fork-empty.jsonl", []string{
		`{"role":"system","content":"system"}`,
		`{"role":"user","content":"查一下北京接下来的天气. 列出来给我"}`,
	})
	if err := agent.SaveBranchMeta(forkPath, agent.BranchMeta{
		CreatedAt:        time.Now().Add(-time.Minute),
		UpdatedAt:        time.Now(),
		Scope:            "global",
		ParentID:         "source-session",
		ForkTurn:         1,
		ForkMessageIndex: 2,
		TopicID:          "topic_fork_empty",
		TopicTitle:       forkTopicTitle("查一下北京接下来的天气"),
	}); err != nil {
		t.Fatalf("save fork branch meta: %v", err)
	}

	topicID := "topic_fork_empty"
	placeholder := forkTopicTitle("查一下北京接下来的天气")
	if err := setTopicTitleWithSource("", topicID, placeholder, topicTitleSourceAuto); err != nil {
		t.Fatalf("seed fork title: %v", err)
	}

	if updated, ok := autoTitleTopicFromSession("", topicID, forkPath); ok {
		t.Fatalf("fork with no own content should keep placeholder, but title changed to %q", updated)
	}
	if got := loadTopicTitle("", topicID); got != placeholder {
		t.Fatalf("stored title = %q, want placeholder %q", got, placeholder)
	}
}

func TestForkDerivedPlaceholderIsOverridableByFirstForkTurn(t *testing.T) {
	isolateDesktopUserDirs(t)
	// A fork title is derived ("查一下北京接下来的天气 · 分叉"), not a manual
	// rename. The fork's first submitted message is allowed to take over the
	// title (like a new session's first message), while ordinary auto titles
	// that are not fork placeholders remain untouched.
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(dir, "fork-turn.jsonl")
	if err := os.WriteFile(sessionPath, []byte(`{"role":"user","content":"天津接下来一周天气如何?"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(sessionPath, agent.BranchMeta{
		CreatedAt:        time.Now().Add(-time.Minute),
		UpdatedAt:        time.Now(),
		Scope:            "global",
		ParentID:         "source-session",
		ForkTurn:         1,
		ForkMessageIndex: 1,
		TopicID:          "topic_fork_turn",
		TopicTitle:       forkTopicTitle("查一下北京接下来的天气"),
	}); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.mu.Lock()
	app.tabs = map[string]*WorkspaceTab{
		"fork": {
			ID:          "fork",
			Scope:       "global",
			TopicID:     "topic_fork_turn",
			TopicTitle:  forkTopicTitle("查一下北京接下来的天气"),
			SessionPath: sessionPath,
		},
	}
	app.activeTabID = "fork"
	app.mu.Unlock()

	placeholder := forkTopicTitle("查一下北京接下来的天气")
	if err := setTopicTitleWithSource("", "topic_fork_turn", placeholder, topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}

	if !app.maybeAutoTitleTopicFromText(app.tabs["fork"], "天津接下来一周天气如何?") {
		t.Fatalf("fork placeholder should be promotable by the fork's first turn text")
	}
	if got := loadTopicTitle("", "topic_fork_turn"); got != "天津接下来一周天气如何" {
		t.Fatalf("stored fork title = %q, want 天津接下来一周天气如何 (trimmed)", got)
	}
}

func TestForkTopicTitleTextSkipsManualLockedTitles(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(dir, "fork-manual.jsonl")
	if err := os.WriteFile(sessionPath, []byte(`{"role":"user","content":"天津接下来一周天气如何?"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	topicID := "topic_fork_manual"
	if err := setTopicTitleWithSource("", topicID, "用户手动标题", topicTitleSourceManual); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.mu.Lock()
	app.tabs = map[string]*WorkspaceTab{
		"fork": {
			ID:          "fork",
			Scope:       "global",
			TopicID:     topicID,
			TopicTitle:  "用户手动标题",
			SessionPath: sessionPath,
		},
	}
	app.activeTabID = "fork"
	app.mu.Unlock()

	if app.maybeAutoTitleTopicFromText(app.tabs["fork"], "天津接下来一周天气如何?") {
		t.Fatalf("manual title must stay locked from auto promotion")
	}
	if got := loadTopicTitle("", topicID); got != "用户手动标题" {
		t.Fatalf("stored title = %q, want manual 用户手动标题", got)
	}
}

// TestForkForSessionTitleIsAutoAndPromotable drives the real desktop fork
// flow: a completed source session is forked; the fork topic title is stored
// with an auto source (so Global rows can re-derive it from the fork's own
// later content instead of freezing the source's old title), and the fork's
// first new question promotes the title exactly like a normal session.
func TestForkForSessionTitleIsAutoAndPromotable(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := agent.NewSessionPath(dir, "source")
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	runner := &appendingDesktopRunner{session: sess, started: make(chan string, 4)}
	ctrl := control.New(control.Options{
		Runner:      runner,
		Executor:    exec,
		Sink:        event.Discard,
		SessionDir:  dir,
		SessionPath: path,
		Label:       "test",
	})
	defer ctrl.Close()

	app := NewApp()
	app.projectTreeChangedHook = func() {}
	app.setTestCtrl(ctrl, "deepseek/test")
	app.tabs["test"].Scope = "global"
	app.tabs["test"].WorkspaceRoot = ""
	app.tabs["test"].TopicID = "topic_source"
	app.tabs["test"].TopicTitle = "查一下北京接下来的天气"
	if err := setTopicTitleWithSource("", "topic_source", "查一下北京接下来的天气", topicTitleSourceAuto); err != nil {
		t.Fatalf("seed source topic title: %v", err)
	}

	ctrl.Submit("查一下北京接下来的天气. 列出来给我")
	<-runner.started
	waitNotRunning(t, ctrl)
	ctrl.Submit("北京今天天气怎么样")
	<-runner.started
	waitNotRunning(t, ctrl)

	meta, err := app.Fork(1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	forkTopicID := meta.TopicID
	if forkTopicID == "" || forkTopicID == "topic_source" {
		t.Fatalf("fork topic id = %q, want an independent topic", forkTopicID)
	}

	// The fork title is a derived placeholder stored as auto (not manual), so
	// the topic stays promotable from the fork's own content.
	placeholder := forkTopicTitle("查一下北京接下来的天气")
	if got := loadTopicTitle("", forkTopicID); got != placeholder {
		t.Fatalf("fork stored title = %q, want placeholder %q", got, placeholder)
	}
	if got := loadTopicTitleSource("", forkTopicID); got != topicTitleSourceAuto {
		t.Fatalf("fork title source = %q, want auto", got)
	}

	// The source topic title stays untouched by the fork.
	if got := loadTopicTitle("", "topic_source"); got != "查一下北京接下来的天气" {
		t.Fatalf("source title changed to %q", got)
	}
}

func TestForkRenameAndTrashKeepSource(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, title := range map[string]string{"source": "北京", "fork": "天津"} {
		if err := setTopicTitle(root, id, title); err != nil {
			t.Fatal(err)
		}
	}
	source := writeTopicSession(t, dir, "source.jsonl", "source", "北京", root)
	fork := writeTopicSession(t, dir, "fork.jsonl", "fork", "天津", root)
	meta, _, err := agent.LoadBranchMeta(fork)
	if err != nil {
		t.Fatal(err)
	}
	meta.ParentID = agent.BranchID(source)
	meta.ForkTurn = 0
	if err := agent.SaveBranchMeta(fork, meta); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.sessionDirsOverride = []string{dir}
	for range 2 {
		if err := app.RenameTopic("fork", "天津预报"); err != nil {
			t.Fatal(err)
		}
	}
	if loadTopicTitle(root, "source") != "北京" || loadTopicTitle(root, "fork") != "天津预报" {
		t.Fatal("rename changed the wrong topic")
	}
	for range 2 {
		if err := app.TrashTopic("fork"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source must survive fork deletion: %v", err)
	}
	if _, err := os.Stat(fork); !os.IsNotExist(err) {
		t.Fatalf("fork must be removed from active sessions: %v", err)
	}
	if loadTopicTitle(root, "source") != "北京" {
		t.Fatal("source title must survive fork deletion")
	}
}

func TestForkTitleRecoveryKeepsConversationOrigin(t *testing.T) {
	path := writeForkSessionLines(t, t.TempDir(), "recovery.jsonl", []string{
		`{"role":"user","content":"北京天气"}`,
		`{"role":"assistant","content":"晴"}`,
		`{"role":"user","content":"明天呢"}`,
	})
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{ParentID: "source", Recovered: true, ForkMessageIndex: 2}); err != nil {
		t.Fatal(err)
	}
	if got := topicTitleFromForkAwareSession(path); got != "北京天气" {
		t.Fatalf("recovery title = %q, want original conversation title", got)
	}
}
