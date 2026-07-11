package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/provider"
)

func forkCoveredRecoveryBranch(t *testing.T, dir, name string) (parentPath, branchPath string) {
	t.Helper()
	parentPath = filepath.Join(dir, name+".jsonl")
	disk := agent.NewSession("sys")
	disk.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	disk.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	disk.Add(provider.Message{Role: provider.RoleUser, Content: "disk " + name})
	if err := disk.Save(parentPath); err != nil {
		t.Fatalf("Save parent: %v", err)
	}
	stale := agent.NewSession("sys")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	stale.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "local " + name})
	info, err := stale.SaveRecoveryBranch(agent.RecoveryBranchOptions{OriginalPath: parentPath})
	if err != nil {
		t.Fatalf("SaveRecoveryBranch: %v", err)
	}
	covering := agent.NewSession("")
	covering.Messages = append([]provider.Message(nil), stale.Snapshot()...)
	covering.Add(provider.Message{Role: provider.RoleAssistant, Content: "answered after recovery"})
	if err := covering.Save(parentPath); err != nil {
		t.Fatalf("Save covering parent: %v", err)
	}
	return parentPath, info.Path
}

func TestRecoveryGCTrashesCoveredForkAndKeepsParent(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	parentPath, branchPath := forkCoveredRecoveryBranch(t, dir, "session")

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	if got := app.reclaimRecoveryBranchesIn([]string{dir}, time.Now().Add(48*time.Hour)); got != 1 {
		t.Fatalf("reclaimed = %d, want 1", got)
	}

	if _, err := os.Stat(branchPath); !os.IsNotExist(err) {
		t.Fatalf("reclaimed branch still present at %s (err=%v)", branchPath, err)
	}
	key := filepath.Base(branchPath)
	trashPath := filepath.Join(dir, sessionTrashDir, key, key)
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("reclaimed branch should be in trash: %v", err)
	}
	if _, err := os.Stat(parentPath); err != nil {
		t.Fatalf("parent session must be untouched: %v", err)
	}

	if got := app.reclaimRecoveryBranchesIn([]string{dir}, time.Now().Add(48*time.Hour)); got != 0 {
		t.Fatalf("second sweep reclaimed = %d, want 0", got)
	}
}

func TestRecoveryGCSkipsBranchOpenInTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	_, branchPath := forkCoveredRecoveryBranch(t, dir, "open")

	tab := &WorkspaceTab{ID: "tab", Scope: "global", SessionPath: branchPath, Ready: true}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, detachedSessions: map[string]*WorkspaceTab{}}
	if got := app.reclaimRecoveryBranchesIn([]string{dir}, time.Now().Add(48*time.Hour)); got != 0 {
		t.Fatalf("reclaimed = %d, want 0 while the branch is open in a tab", got)
	}
	if _, err := os.Stat(branchPath); err != nil {
		t.Fatalf("open branch must be untouched: %v", err)
	}
}
