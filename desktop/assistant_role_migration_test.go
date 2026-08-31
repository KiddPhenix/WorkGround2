package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/provider"
	"workground2/internal/tool/sessiontool"
)

func TestRehomeLegacyRoleSessionsMovesUniqueWorkspaceOwner(t *testing.T) {
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-role-migration",
		Assistant: assistant.Assistant{
			ID: "helper-role-migration", Name: "Helper", Mission: "Keep releases healthy",
			Scope: assistant.ScopeWorkspace, WorkspaceRoot: workspace,
			Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	globalDir := t.TempDir()
	path := filepath.Join(globalDir, "legacy-role.jsonl")
	writeRoleTranscript(t, path, assistant.DispatcherPrompt(snapshot, "continue"))
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.Scope = "global"
	meta.SessionSource = "external"
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * legacyRoleMoveAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	projectDir := filepath.Join(t.TempDir(), "project-sessions")
	moved, err := rehomeLegacyRoleSessions(store, globalDir, func(string) string { return projectDir }, nil, time.Now())
	if err != nil || moved != 1 {
		t.Fatalf("rehome moved=%d err=%v", moved, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy transcript still exists: %v", err)
	}
	target := filepath.Join(projectDir, filepath.Base(path))
	meta, ok, err := agent.LoadBranchMeta(target)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta ok=%v err=%v", ok, err)
	}
	if meta.Scope != "project" || meta.WorkspaceRoot != normalizeProjectRoot(workspace) || meta.AssistantID != snapshot.Assistant.ID || meta.SessionSource != agent.SessionSourceAssist {
		t.Fatalf("migrated meta = %+v", meta)
	}
	if moved, err := rehomeLegacyRoleSessions(store, globalDir, func(string) string { return projectDir }, nil, time.Now()); err != nil || moved != 0 {
		t.Fatalf("idempotent replay moved=%d err=%v", moved, err)
	}
}

func TestLegacyRoleOwnerRejectsAmbiguousMission(t *testing.T) {
	prompt := "你是长期助手的 Dispatcher。\n\n助手使命：\nShared mission\n"
	snapshots := []assistant.Snapshot{
		{Assistant: assistant.Assistant{ID: "a", Mission: "Shared mission", Scope: assistant.ScopeWorkspace, WorkspaceRoot: "A"}},
		{Assistant: assistant.Assistant{ID: "b", Mission: "Shared mission", Scope: assistant.ScopeWorkspace, WorkspaceRoot: "B"}},
	}
	if owner, ok := legacyRoleOwner(prompt, snapshots); ok {
		t.Fatalf("ambiguous role owner resolved to %+v", owner)
	}
}

func TestAssistantSessionWorkspaceUsesOwnerAsAuthority(t *testing.T) {
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-session-workspace",
		Assistant: assistant.Assistant{
			ID: "helper-session-workspace", Name: "Helper", Mission: "Work here",
			Scope: assistant.ScopeWorkspace, WorkspaceRoot: workspace,
			Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	control := &appAssistantSessionControl{store: store}
	got, err := control.createWorkspace(sessiontool.SessionCreateRequest{OwnerID: snapshot.Assistant.ID})
	if err != nil || got != normalizeProjectRoot(workspace) {
		t.Fatalf("createWorkspace = %q err=%v", got, err)
	}
	if _, err := control.createWorkspace(sessiontool.SessionCreateRequest{
		OwnerID: snapshot.Assistant.ID, Workspace: t.TempDir(),
	}); err == nil {
		t.Fatal("Assistant Session accepted a conflicting workspace")
	}
}

func writeRoleTranscript(t *testing.T, path, prompt string) {
	t.Helper()
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: prompt},
		{Role: provider.RoleAssistant, Content: `{"kind":"task","reply":"ok","jobs":[]}`},
	}
	var data []byte
	for _, message := range messages {
		line, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
