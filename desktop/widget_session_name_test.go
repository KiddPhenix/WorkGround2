package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"workground2/internal/agent"
)

type fakeWidgetSessionNameGenerator struct {
	name string
	err  error
	reqs []widgetSessionNameRequest
}

func (f *fakeWidgetSessionNameGenerator) Generate(_ context.Context, req widgetSessionNameRequest) (string, error) {
	f.reqs = append(f.reqs, req)
	return f.name, f.err
}

func TestCleanWidgetSessionNameBoundsChineseAndEnglish(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"名称：修复登录界面", "修复登录界"},
		{"Name: LoginRepairLong", "LoginRepai"},
		{"`\n  搜索异常  \n`", "搜索异常"},
	} {
		got, err := cleanWidgetSessionName(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("cleanWidgetSessionName(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
	if _, err := cleanWidgetSessionName("```\n```"); err == nil {
		t.Fatal("empty generated name must fail explicitly")
	}
}

func TestUniqueWidgetSessionNameStaysBounded(t *testing.T) {
	name, err := uniqueWidgetSessionName("修复登录界", []string{"修复登录界", "修复登录2"})
	if err != nil || name != "修复登录3" || len([]rune(name)) > widgetSessionNameChineseRunes {
		t.Fatalf("Chinese unique name = %q, %v", name, err)
	}
	name, err = uniqueWidgetSessionName("LoginRepai", []string{"loginrepai"})
	if err != nil || name != "LoginRepa2" || len([]rune(name)) > widgetSessionNameEnglishRunes {
		t.Fatalf("English unique name = %q, %v", name, err)
	}
}

func TestGenerateUniqueWidgetSessionNameUsesVisibleIconTitles(t *testing.T) {
	isolateDesktopUserDirs(t)
	gen := &fakeWidgetSessionNameGenerator{name: "新建"}
	app := NewApp()
	app.widgetSessionNameGen = gen

	name, err := app.generateUniqueWidgetSessionName("帮我检查新建会话流程", "", "provider/model", []string{"其他准备中"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "新建2" {
		t.Fatalf("unique name = %q, want 新建2", name)
	}
	if len(gen.reqs) != 1 || !slices.Contains(gen.reqs[0].ExistingTitles, "新建") || !slices.Contains(gen.reqs[0].ExistingTitles, "其他准备中") || gen.reqs[0].Prompt == "" {
		t.Fatalf("name request = %+v, want prompt and visible fixed titles", gen.reqs)
	}
}

func TestGenerateUniqueWidgetSessionNameSurfacesGeneratorFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.widgetSessionNameGen = &fakeWidgetSessionNameGenerator{err: errors.New("provider unavailable")}
	if _, err := app.generateUniqueWidgetSessionName("fix it", "", "provider/model", nil); err == nil {
		t.Fatal("provider failure must remain visible and retryable")
	}
}

func TestApplyWidgetSessionNameConvergesTopicSessionAndRuntime(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	path, err := createEmptySessionFile(desktopSessionDir(root), "model")
	if err != nil {
		t.Fatal(err)
	}
	topicID := "topic-name"
	if err := setTopicTitleWithSource(root, topicID, defaultTopicTitle, topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{
		ID: "tab-name", Scope: "project", WorkspaceRoot: root, TopicID: topicID,
		TopicTitle: defaultTopicTitle, SessionPath: path,
	}
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}

	if err := app.applyWidgetSessionName(tab.ID, "命名会话"); err != nil {
		t.Fatal(err)
	}
	if tab.TopicTitle != "命名会话" || loadTopicTitle(root, topicID) != "命名会话" || loadTopicTitleSource(root, topicID) != topicTitleSourceAuto {
		t.Fatalf("topic naming did not converge: runtime=%q stored=%q source=%q", tab.TopicTitle, loadTopicTitle(root, topicID), loadTopicTitleSource(root, topicID))
	}
	if got := loadSessionTitles(filepath.Dir(path))[filepath.Base(path)]; got != "命名会话" {
		t.Fatalf("session title = %q, want 命名会话", got)
	}
	// Repeating the same application is a safe no-op from the caller's view.
	if err := app.applyWidgetSessionName(tab.ID, "命名会话"); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}

	// A pending model can rebuild the controller onto a continuation path before
	// the first submit. Reapplying converges the new sidecar as well.
	nextPath, err := createEmptySessionFile(desktopSessionDir(root), "next-model")
	if err != nil {
		t.Fatal(err)
	}
	tab.SessionPath = nextPath
	if err := app.applyWidgetSessionName(tab.ID, "命名会话"); err != nil {
		t.Fatalf("reapply after path rotation: %v", err)
	}
	if got := loadSessionTitles(filepath.Dir(nextPath))[filepath.Base(nextPath)]; got != "命名会话" {
		t.Fatalf("rotated session title = %q, want 命名会话", got)
	}
}

func TestApplyWidgetSessionNameIndexesTransientBlank(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	path, err := createEmptySessionFile(desktopSessionDir(root), "model")
	if err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{
		ID: "transient-blank", Scope: "project", WorkspaceRoot: root,
		SessionPath: path,
	}
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}

	if err := app.applyWidgetSessionName(tab.ID, "小组件任务"); err != nil {
		t.Fatalf("applyWidgetSessionName: %v", err)
	}
	if strings.TrimSpace(tab.TopicID) == "" {
		t.Fatal("widget naming did not index the transient blank")
	}
	projects := loadProjectsFile()
	if len(projects.Projects) != 1 || !containsDesktopString(projects.Projects[0].Topics, tab.TopicID) {
		t.Fatalf("indexed topic %q missing from project: %#v", tab.TopicID, projects)
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta(%q): ok=%v err=%v", path, ok, err)
	}
	if meta.TopicID != tab.TopicID || meta.TopicTitle != "小组件任务" {
		t.Fatalf("session metadata = %+v, want topic %q title 小组件任务", meta, tab.TopicID)
	}
	if err := app.applyWidgetSessionName(tab.ID, "小组件任务"); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if got := len(loadProjectsFile().Projects[0].Topics); got != 1 {
		t.Fatalf("idempotent retry indexed %d topics, want 1", got)
	}
}
