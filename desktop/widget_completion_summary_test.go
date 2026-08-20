package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/config"
)

type fakeCompletionSummaryGenerator struct {
	fn func(ctx context.Context, req desktopIconCompletionSummaryRequest) (string, error)
}

func (f fakeCompletionSummaryGenerator) Generate(ctx context.Context, req desktopIconCompletionSummaryRequest) (string, error) {
	if f.fn != nil {
		return f.fn(ctx, req)
	}
	return "任务已完成。", nil
}

func newSummaryTestApp(t *testing.T, tab *WorkspaceTab, gen completionSummaryGenerator) *App {
	t.Helper()
	t.Setenv("WorkGround2_STATE_HOME", t.TempDir())
	tabs := map[string]*WorkspaceTab{}
	active := ""
	if tab != nil {
		tabs[tab.ID] = tab
		active = tab.ID
	}
	return &App{
		tabs:                      tabs,
		activeTabID:               active,
		iconWidgetStateLoaded:     true,
		iconWidgetState:           desktopIconPersistedState{Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, CompletionSummaries: map[string]desktopIconCompletionSummary{}},
		completionSummaryGen:      gen,
		completionSummaryInFlight: map[string]*completionSummaryCall{},
	}
}

func completionTestTab(t *testing.T, attentionAt int64) (*WorkspaceTab, string) {
	t.Helper()
	sp := agent.NewSessionPath(t.TempDir(), "task")
	if _, err := agent.EnsureBranchMeta(sp); err != nil {
		t.Fatalf("EnsureBranchMeta: %v", err)
	}
	if attentionAt > 0 {
		if err := setNeedsAttention(sp, attentionAt); err != nil {
			t.Fatalf("setNeedsAttention: %v", err)
		}
	}
	return &WorkspaceTab{ID: "task-1", SessionPath: sp, disabledMCP: map[string]ServerView{}}, sp
}

func TestCompletionSummaryKeyStableAndDistinct(t *testing.T) {
	key := desktopIconCompletionKey("task-1", "completed", 1000)
	if desktopIconCompletionKey("task-1", "completed", 1000) != key {
		t.Fatal("same task+kind+revision must produce the same key")
	}
	if desktopIconCompletionKey("task-1", "completed", 2000) == key {
		t.Fatal("a newer completion revision must produce a different key")
	}
	if desktopIconCompletionKey("task-1", "failed", 1000) == key {
		t.Fatal("completed and failed must produce different keys")
	}
	if desktopIconCompletionKey("task-2", "completed", 1000) == key {
		t.Fatal("a different task must produce a different key")
	}
}

func TestCompletionFailureIdentityChangesWithError(t *testing.T) {
	first := desktopIconFailureKey("task-1", "network timeout")
	if desktopIconFailureKey("task-1", "network timeout") != first {
		t.Fatal("same failure must keep a stable key")
	}
	if desktopIconFailureKey("task-1", "permission denied") == first {
		t.Fatal("a changed failure must not reuse the prior summary")
	}
	if desktopIconFailureFingerprint("network timeout") == desktopIconFailureFingerprint("permission denied") {
		t.Fatal("late-result guard must distinguish changed failures")
	}
}

func TestCompletionSummaryProviderPrefersDefaultModel(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultModel = "preferred/model-b"
	cfg.Providers = []config.ProviderEntry{
		{Name: "first", Kind: "openai", BaseURL: "http://127.0.0.1:11434/v1", Model: "model-a"},
		{Name: "preferred", Kind: "openai", BaseURL: "http://127.0.0.1:11434/v1", Model: "model-b"},
	}
	entry := completionSummaryProvider(cfg)
	if entry == nil || entry.Name != "preferred" || entry.Model != "model-b" {
		t.Fatalf("summary provider = %+v, want preferred/model-b", entry)
	}
}

func TestBuildCompletionSummaryPromptBounded(t *testing.T) {
	req := desktopIconCompletionSummaryRequest{
		Title:   strings.Repeat("长标题", 60),
		Request: strings.Repeat("长请求", 400),
		Result:  strings.Repeat("长结果", 600),
	}
	prompt := buildCompletionSummaryPrompt(req)
	if !strings.Contains(prompt, "任务标题：") || !strings.Contains(prompt, "用户请求：") || !strings.Contains(prompt, "任务结果：") {
		t.Fatalf("prompt misses material sections: %s", prompt)
	}
	if len([]rune(prompt)) > 2000 {
		t.Fatalf("prompt is not bounded: %d runes", len([]rune(prompt)))
	}
}

func TestCleanCompletionSummaryStripsMarkdown(t *testing.T) {
	raw := "“# 标题\n- 第一步\n- 第二步\n\n```go\nfmt.Println(\"x\")\n```\n\n结论是 `pkg` 已修复，路径 D:\\work\\x.go 不再使用。"
	got, err := cleanCompletionSummary(raw)
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	for _, banned := range []string{"#", "```", "`", "- 第一步", "- 第二步"} {
		if strings.Contains(got, banned) {
			t.Fatalf("cleaned summary still contains %q: %q", banned, got)
		}
	}
	if !strings.Contains(got, "结论是") || !strings.Contains(got, "已修复") {
		t.Fatalf("cleaned summary lost the core facts: %q", got)
	}
	if got != strings.Trim(got, "\"'“”‘’") {
		t.Fatal("cleaned summary still carries wrapping quotes")
	}
}

func TestCleanCompletionSummaryBoundedAndEmpty(t *testing.T) {
	got, err := cleanCompletionSummary(strings.Repeat("字", 500))
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if len([]rune(got)) > completionSummaryMaxRunes {
		t.Fatalf("cleaned summary exceeds the popup bound: %d", len([]rune(got)))
	}
	if !strings.Contains(completionSummarySystemPrompt, "约 100 字") || !strings.Contains(completionSummarySystemPrompt, "最多 100 字") {
		t.Fatal("summary prompt must state the same popup maximum")
	}
	if _, err := cleanCompletionSummary("   "); err == nil {
		t.Fatal("blank input must be an explicit error")
	}
	if _, err := cleanCompletionSummary("```\n代码\n```\n`#`"); err == nil {
		t.Fatal("markdown-only input must be an explicit error")
	}
}

func TestCompletionSummaryFallbackUsesHundredRuneThreshold(t *testing.T) {
	short := strings.Repeat("字", completionSummaryMaxRunes-2) + "\n结"
	if got, needsSummary := completionSummaryFallback(short); got != short || needsSummary {
		t.Fatalf("short reply = %q/%v, want verbatim without generation", got, needsSummary)
	}
	long := strings.Repeat("字", completionSummaryMaxRunes+1)
	got, needsSummary := completionSummaryFallback(long)
	if !needsSummary || len([]rune(got)) != completionSummaryMaxRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("long reply fallback = %q/%v (%d runes)", got, needsSummary, len([]rune(got)))
	}
}

func TestDesktopIconCompletionSummaryForFallback(t *testing.T) {
	key := desktopIconCompletionKey("task-1", "completed", 10)
	mechanical := "机械摘要"
	summaries := map[string]desktopIconCompletionSummary{
		key: {Status: "ready", Text: "新闻体摘要"},
	}
	if body, status := desktopIconCompletionSummaryFor(summaries, key, mechanical); body != "新闻体摘要" || status != "ready" {
		t.Fatalf("ready projection = %q/%q", body, status)
	}
	summaries[key] = desktopIconCompletionSummary{Status: "failed", Error: "boom"}
	if body, status := desktopIconCompletionSummaryFor(summaries, key, mechanical); body != mechanical || status != "failed" {
		t.Fatalf("failed projection = %q/%q", body, status)
	}
	if body, status := desktopIconCompletionSummaryFor(nil, key, mechanical); body != mechanical || status != "" {
		t.Fatalf("missing projection = %q/%q", body, status)
	}
	summaries[key] = desktopIconCompletionSummary{Status: "ready", Text: strings.Repeat("旧摘要", completionSummaryMaxRunes)}
	if body, status := desktopIconCompletionSummaryFor(summaries, key, mechanical); len([]rune(body)) != completionSummaryMaxRunes || status != "ready" {
		t.Fatalf("legacy cached summary was not bounded = %q/%q", body, status)
	}
}

func TestSummaryGenerationDue(t *testing.T) {
	now := int64(1000)
	if summaryGenerationDue(desktopIconCompletionSummary{}, now) != true {
		t.Fatal("a never-generated summary must be due")
	}
	if summaryGenerationDue(desktopIconCompletionSummary{Status: "ready"}, now) {
		t.Fatal("a ready summary must never regenerate")
	}
	failed := desktopIconCompletionSummary{Status: "failed", RetryAfter: now + 100}
	if summaryGenerationDue(failed, now) {
		t.Fatal("a fresh failure must wait for the backoff")
	}
	failed.RetryAfter = now
	if !summaryGenerationDue(failed, now) {
		t.Fatal("a failed summary must retry after the backoff")
	}
}

func TestCompletionSummaryRequestsCollectsAndSkips(t *testing.T) {
	app := newSummaryTestApp(t, nil, fakeCompletionSummaryGenerator{})
	longA := strings.Repeat("a", completionSummaryMaxRunes+1)
	longB := strings.Repeat("b", completionSummaryMaxRunes+1)
	longFailure := strings.Repeat("boom", completionSummaryMaxRunes)
	sources := []widgetSource{
		{meta: TabMeta{ID: "a", NeedsAttention: true, NeedsAttentionAt: 10}, requestText: "ask-a", resultText: longA},
		{meta: TabMeta{ID: "b", NeedsAttention: true, NeedsAttentionAt: 20}, requestText: "ask-b", resultText: longB},
		{meta: TabMeta{ID: "c"}, resultText: "no-attention"},
		{meta: TabMeta{ID: "d", NeedsAttention: true, NeedsAttentionAt: 30}},
		{meta: TabMeta{ID: "e", StartupErr: longFailure}, requestText: "ask-e"},
		{meta: TabMeta{ID: "short", NeedsAttention: true, NeedsAttentionAt: 40}, resultText: "直接显示"},
	}
	app.iconWidgetState.CompletionSummaries[desktopIconCompletionKey("b", "completed", 20)] = desktopIconCompletionSummary{Status: "ready", Text: "x"}
	app.completionSummaryInFlight[desktopIconCompletionKey("a", "completed", 10)] = &completionSummaryCall{done: make(chan struct{})}

	requests := app.completionSummaryRequestsLocked(sources)
	if len(requests) != 1 {
		t.Fatalf("requests = %+v, want only the failed task", requests)
	}
	req := requests[0]
	if req.TaskID != "e" || req.Kind != "failed" || req.Revision != desktopIconFailureFingerprint(longFailure) || req.Key != desktopIconFailureKey("e", longFailure) || req.Request != "ask-e" || req.Result != longFailure {
		t.Fatalf("failed request = %+v", req)
	}
}

func TestCompletionSummaryReadyWritten(t *testing.T) {
	tab, _ := completionTestTab(t, 1000)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	key := desktopIconCompletionKey("task-1", "completed", 1000)
	req := desktopIconCompletionSummaryRequest{
		Key: key, TaskID: "task-1", Kind: "completed", Revision: "completed:1000",
		Title: "标题", Request: "请求", Result: "结果",
	}
	done := make(chan struct{})
	app.runCompletionSummary(req, &completionSummaryCall{done: done})
	<-done

	entry := app.iconWidgetState.CompletionSummaries[key]
	if entry.Status != "ready" || entry.Text != "任务已完成。" || entry.Error != "" {
		t.Fatalf("summary entry = %+v", entry)
	}
	if len(app.completionSummaryInFlight) != 0 {
		t.Fatalf("in-flight slot was not released: %+v", app.completionSummaryInFlight)
	}
}

func TestCompletionSummaryLateResultDiscarded(t *testing.T) {
	tab, sp := completionTestTab(t, 1000)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	key := desktopIconCompletionKey("task-1", "completed", 1000)
	req := desktopIconCompletionSummaryRequest{
		Key: key, TaskID: "task-1", Kind: "completed", Revision: "completed:1000",
		Title: "标题", Request: "请求", Result: "结果",
	}
	// The task starts a new completion round while generation is in flight:
	// attention is dismissed first, then a newer completion stamps a new time.
	if err := clearNeedsAttention(sp); err != nil {
		t.Fatalf("clear attention: %v", err)
	}
	if err := setNeedsAttention(sp, 2000); err != nil {
		t.Fatalf("advance completion: %v", err)
	}
	done := make(chan struct{})
	app.runCompletionSummary(req, &completionSummaryCall{done: done})
	<-done

	if len(app.iconWidgetState.CompletionSummaries) != 0 {
		t.Fatalf("late result leaked into the next round: %+v", app.iconWidgetState.CompletionSummaries)
	}
}

func TestCompletionSummaryLateResultSurvivesSessionOpen(t *testing.T) {
	tab, sp := completionTestTab(t, 1000)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	key := desktopIconCompletionKey("task-1", "completed", 1000)
	app.iconWidgetState.Kept["task:task-1"] = desktopIconKept{
		ItemID: "task:task-1", SourceID: "task-1", Summary: "机械摘要", CompletionKey: key, CompletedAt: 1000,
	}
	if err := clearNeedsAttention(sp); err != nil {
		t.Fatalf("simulate session open: %v", err)
	}
	req := desktopIconCompletionSummaryRequest{
		Key: key, TaskID: "task-1", Kind: "completed", Revision: "completed:1000",
		Title: "标题", Request: "请求", Result: strings.Repeat("长结果", completionSummaryMaxRunes),
	}
	done := make(chan struct{})
	app.runCompletionSummary(req, &completionSummaryCall{done: done})
	<-done
	if entry := app.iconWidgetState.CompletionSummaries[key]; entry.Status != completionSummaryReady || entry.Text != "任务已完成。" {
		t.Fatalf("retained completion did not accept late summary: %+v", entry)
	}
}

func TestCompletionSummaryDiscardedAfterDismiss(t *testing.T) {
	tab, _ := completionTestTab(t, 1000)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	key := desktopIconCompletionKey("task-1", "completed", 1000)
	req := desktopIconCompletionSummaryRequest{
		Key: key, TaskID: "task-1", Kind: "completed", Revision: "completed:1000",
		Title: "标题", Request: "请求", Result: "结果",
	}
	// The task loses its completion state (dismissed) while generating.
	if err := clearNeedsAttention(tab.currentSessionPath()); err != nil {
		t.Fatalf("clear attention: %v", err)
	}
	done := make(chan struct{})
	app.runCompletionSummary(req, &completionSummaryCall{done: done})
	<-done

	if len(app.iconWidgetState.CompletionSummaries) != 0 {
		t.Fatalf("result survived a dismissed task: %+v", app.iconWidgetState.CompletionSummaries)
	}
}

func TestCompletionSummaryFailureDegradesAndRetries(t *testing.T) {
	tab, _ := completionTestTab(t, 1000)
	gen := fakeCompletionSummaryGenerator{fn: func(context.Context, desktopIconCompletionSummaryRequest) (string, error) {
		return "", context.DeadlineExceeded
	}}
	app := newSummaryTestApp(t, tab, gen)
	key := desktopIconCompletionKey("task-1", "completed", 1000)
	longResult := strings.Repeat("机械结果", completionSummaryMaxRunes)
	req := desktopIconCompletionSummaryRequest{
		Key: key, TaskID: "task-1", Kind: "completed", Revision: "completed:1000",
		Title: "标题", Request: "请求", Result: longResult,
	}
	done := make(chan struct{})
	app.runCompletionSummary(req, &completionSummaryCall{done: done})
	<-done

	entry := app.iconWidgetState.CompletionSummaries[key]
	if entry.Status != "failed" || entry.Error == "" || entry.RetryAfter <= entry.GeneratedAt {
		t.Fatalf("failed entry = %+v", entry)
	}
	// The failed entry keeps the mechanical body on the snapshot and waits.
	sources := []widgetSource{{meta: TabMeta{ID: "task-1", NeedsAttention: true, NeedsAttentionAt: 1000}, resultText: longResult}}
	if requests := app.completionSummaryRequestsLocked(sources); len(requests) != 0 {
		t.Fatalf("retry fired before the backoff: %+v", requests)
	}
	// After the backoff the same snapshot retries the generation safely.
	entry.RetryAfter = time.Now().UnixMilli() - 1
	app.iconWidgetState.CompletionSummaries[key] = entry
	requests := app.completionSummaryRequestsLocked(sources)
	if len(requests) != 1 || requests[0].Key != key {
		t.Fatalf("retry did not fire after the backoff: %+v", requests)
	}
}

func TestCompletionSummaryGuardUsesEffectiveTimestamp(t *testing.T) {
	tab, _ := completionTestTab(t, 1000)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	// A newer completion is pending in memory while the BranchMeta keeps the
	// first completion timestamp: this is still the same attention round, so
	// the guard must report the effective min and match the cache key.
	tab.saveMu.Lock()
	tab.pendingAttentionAt = 2000
	tab.saveMu.Unlock()
	got, ok := app.completionRevisionForTask("task-1")
	if !ok || got != "completed:1000" {
		t.Fatalf("guard = %q/%v, want effective min completed:1000", got, ok)
	}
}

func TestCompletionSummaryCacheBounded(t *testing.T) {
	app := newSummaryTestApp(t, nil, fakeCompletionSummaryGenerator{})
	for i := 0; i < completionSummaryCacheLimit+16; i++ {
		app.iconWidgetState.CompletionSummaries[fmt.Sprintf("k-%d", i)] = desktopIconCompletionSummary{GeneratedAt: int64(i)}
	}
	app.trimCompletionSummariesLocked()
	if len(app.iconWidgetState.CompletionSummaries) != completionSummaryCacheLimit {
		t.Fatalf("cache size = %d, want %d", len(app.iconWidgetState.CompletionSummaries), completionSummaryCacheLimit)
	}
	if _, ok := app.iconWidgetState.CompletionSummaries["k-0"]; ok {
		t.Fatal("oldest entry survived trimming")
	}
}

func TestDesktopTaskItemProjectsCachedSummary(t *testing.T) {
	longResult := strings.Repeat("机械结果", completionSummaryMaxRunes)
	source := widgetSource{meta: TabMeta{ID: "task-1", SessionID: "session-1", TopicTitle: "实现图标模式", NeedsAttention: true, NeedsAttentionAt: 1000}, resultText: longResult}
	summaries := map[string]desktopIconCompletionSummary{
		desktopIconCompletionKey("task-1", "completed", 1000): {Status: "ready", Text: "新闻体摘要"},
	}
	item := desktopTaskItem(source, 0, summaries)
	if len(item.Notifications) != 1 {
		t.Fatalf("notifications = %+v", item.Notifications)
	}
	notice := item.Notifications[0]
	if notice.Kind != "completed" || notice.Body != "新闻体摘要" || notice.SummaryStatus != "ready" {
		t.Fatalf("completed notice = %+v", notice)
	}
	// The generation itself never touched the session history: the snapshot
	// only reads the pre-captured request/result material.
	mechanical := desktopTaskItem(source, 0, nil)
	if len([]rune(mechanical.Notifications[0].Body)) != completionSummaryMaxRunes || mechanical.Notifications[0].SummaryStatus != "" {
		t.Fatalf("mechanical fallback notice = %+v", mechanical.Notifications[0])
	}
	short := "短回复\n保持原文"
	direct := desktopTaskItem(widgetSource{meta: TabMeta{ID: "short", NeedsAttention: true, NeedsAttentionAt: 2000}, resultText: short}, 0, summaries)
	if direct.Notifications[0].Body != short || direct.Notifications[0].SummaryStatus != "" {
		t.Fatalf("short reply was summarized: %+v", direct.Notifications[0])
	}
}
