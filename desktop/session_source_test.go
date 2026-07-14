package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/control"
	"workground2/internal/event"
)

func TestRuntimeCLISourceDefersSidecarUntilTranscriptExists(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "runtime-cli")
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}

	if err := app.setActiveSessionSource("cli"); err != nil {
		t.Fatalf("setActiveSessionSource: %v", err)
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "cli" {
		t.Fatalf("runtime source = %q, want cli", source)
	}
	if _, err := os.Stat(agent.BranchMetaPath(sp)); !os.IsNotExist(err) {
		t.Fatalf("runtime-only source created an orphan sidecar: %v", err)
	}

	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	if err := app.persistTabSessionSource(tab.ID); err != nil {
		t.Fatalf("persistTabSessionSource: %v", err)
	}
	meta, ok, err := agent.LoadBranchMeta(sp)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta ok=%v err=%v", ok, err)
	}
	if meta.SessionSource != "cli" {
		t.Fatalf("persisted source = %q, want cli", meta.SessionSource)
	}
	app.queueNeedsAttention(tab.ID, 1000)
	if tab.pendingAttentionAt != 0 {
		t.Fatalf("CLI runtime queued attention at %d", tab.pendingAttentionAt)
	}
}

func TestQueuedAttentionPersistsAfterSnapshot(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "queued-attention")
	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}

	app.queueNeedsAttention(tab.ID, 1000)
	if _, err := os.Stat(agent.BranchMetaPath(sp)); !os.IsNotExist(err) {
		t.Fatalf("queued attention wrote before snapshot: %v", err)
	}
	if err := persistTabAttention(tab); err != nil {
		t.Fatalf("persistTabAttention: %v", err)
	}
	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("LoadBranchMeta: %v", err)
	}
	if !meta.NeedsAttention || meta.NeedsAttentionAt != 1000 {
		t.Fatalf("persisted attention = %+v", meta)
	}
}

func TestClearTabAttentionCancelsPending(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "clear-pending")
	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}

	app.queueNeedsAttention(tab.ID, 1000)
	if err := clearTabAttention(tab); err != nil {
		t.Fatalf("clearTabAttention: %v", err)
	}
	if err := persistTabAttention(tab); err != nil {
		t.Fatalf("persistTabAttention after clear: %v", err)
	}
	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("LoadBranchMeta: %v", err)
	}
	if meta.NeedsAttention || meta.NeedsAttentionAt != 0 {
		t.Fatalf("cleared pending attention resurfaced: %+v", meta)
	}
}

// ── sessionSource tests ─────────────────────────────────────────────────────

// TestCLISessionSourcePersisted verifies that a CLI-created session stamps
// sessionSource=cli on the BranchMeta sidecar.
func TestCLISessionSourcePersisted(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "cli-new")

	// Simulate what happens during a CLI-created session: first
	// the session file is created, then the BranchMeta is stamped.
	if _, err := agent.EnsureBranchMeta(sp); err != nil {
		t.Fatalf("EnsureBranchMeta: %v", err)
	}
	if err := stampSessionSource(sp, "cli"); err != nil {
		t.Fatalf("stampSessionSource: %v", err)
	}

	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("load BranchMeta: %v", err)
	}
	if meta.SessionSource != "cli" {
		t.Fatalf("SessionSource = %q, want cli", meta.SessionSource)
	}
}

// TestStampSessionSourceOverwrites verifies that stampSessionSource always
// overwrites the SessionSource field.
func TestStampSessionSourceOverwrites(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "test")

	if _, err := agent.EnsureBranchMeta(sp); err != nil {
		t.Fatalf("EnsureBranchMeta: %v", err)
	}
	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("load BranchMeta: %v", err)
	}
	meta.SessionSource = "external"
	if err := agent.SaveBranchMetaPreserveUpdated(sp, meta); err != nil {
		t.Fatalf("save BranchMeta: %v", err)
	}

	if err := stampSessionSource(sp, "cli"); err != nil {
		t.Fatalf("stampSessionSource: %v", err)
	}

	meta, _, err = agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("reload BranchMeta: %v", err)
	}
	if meta.SessionSource != "cli" {
		t.Fatalf("SessionSource = %q after stamp, want cli", meta.SessionSource)
	}
}

// ── needsAttention tests ─────────────────────────────────────────────────────

// TestNeedsAttentionIdempotent verifies that setNeedsAttention and
// clearNeedsAttention can be safely called multiple times.
func TestNeedsAttentionIdempotent(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "idem")

	if _, err := agent.EnsureBranchMeta(sp); err != nil {
		t.Fatalf("EnsureBranchMeta: %v", err)
	}

	// Set twice — idempotent.
	if err := setNeedsAttention(sp, 1000); err != nil {
		t.Fatalf("first setNeedsAttention: %v", err)
	}
	if err := setNeedsAttention(sp, 2000); err != nil {
		t.Fatalf("second setNeedsAttention: %v", err)
	}

	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("load BranchMeta: %v", err)
	}
	if !meta.NeedsAttention {
		t.Fatal("NeedsAttention should be true after set")
	}
	if meta.NeedsAttentionAt == 0 {
		t.Fatal("NeedsAttentionAt should be non-zero after set")
	}
	if meta.NeedsAttentionAt != 1000 {
		t.Fatalf("NeedsAttentionAt = %d, want first completion timestamp 1000", meta.NeedsAttentionAt)
	}

	// Clear twice — idempotent.
	if err := clearNeedsAttention(sp); err != nil {
		t.Fatalf("first clearNeedsAttention: %v", err)
	}
	meta, _, err = agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("reload after first clear: %v", err)
	}
	if meta.NeedsAttention {
		t.Fatal("NeedsAttention should be false after clear")
	}

	if err := clearNeedsAttention(sp); err != nil {
		t.Fatalf("second clearNeedsAttention: %v", err)
	}
	meta, _, err = agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("reload after second clear: %v", err)
	}
	if meta.NeedsAttention {
		t.Fatal("NeedsAttention should still be false after second clear")
	}
}

// TestCLISourceAndAttentionRace verifies the CLI exclusion invariant even
// when source stamping and turn completion arrive concurrently.
func TestCLISourceAndAttentionRace(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		sp := agent.NewSessionPath(dir, "race")
		if _, err := agent.EnsureBranchMeta(sp); err != nil {
			t.Fatalf("EnsureBranchMeta: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := stampSessionSource(sp, "cli"); err != nil {
				t.Errorf("stampSessionSource: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := setNeedsAttention(sp, 1000); err != nil {
				t.Errorf("setNeedsAttention: %v", err)
			}
		}()
		wg.Wait()

		meta, _, err := agent.LoadBranchMeta(sp)
		if err != nil {
			t.Fatalf("LoadBranchMeta: %v", err)
		}
		if meta.SessionSource != "cli" || meta.NeedsAttention || meta.NeedsAttentionAt != 0 {
			t.Fatalf("CLI attention invariant broken: %+v", meta)
		}
	}
}

// TestCLISessionsExcludedFromNeedsAttention verifies that CLI-originated
// sessions are never marked as needing attention.
func TestCLISessionsExcludedFromNeedsAttention(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "cli-session")

	if _, err := agent.EnsureBranchMeta(sp); err != nil {
		t.Fatalf("EnsureBranchMeta: %v", err)
	}

	if err := stampSessionSource(sp, "cli"); err != nil {
		t.Fatalf("stampSessionSource: %v", err)
	}

	// setNeedsAttention should silently refuse CLI sessions.
	if err := setNeedsAttention(sp, 1000); err != nil {
		t.Fatalf("setNeedsAttention: %v", err)
	}

	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("load BranchMeta: %v", err)
	}
	if meta.NeedsAttention {
		t.Fatal("CLI session should not have NeedsAttention set")
	}
}

// TestNeedsAttentionPersistence verifies that needsAttention survives a reload.
func TestNeedsAttentionPersistence(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "persist")

	if _, err := agent.EnsureBranchMeta(sp); err != nil {
		t.Fatalf("EnsureBranchMeta: %v", err)
	}

	if err := setNeedsAttention(sp, 1700000000000); err != nil {
		t.Fatalf("setNeedsAttention: %v", err)
	}

	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("reload BranchMeta: %v", err)
	}
	if !meta.NeedsAttention {
		t.Fatal("NeedsAttention should survive reload")
	}
	if meta.NeedsAttentionAt != 1700000000000 {
		t.Fatalf("NeedsAttentionAt = %d, want 1700000000000", meta.NeedsAttentionAt)
	}
}

// ── tabMeta transient attention tests ────────────────────────────────────────

// stubSessionForAttention is a SessionAPI wrapper that reports a fixed
// RuntimeStatus while delegating everything else to a real Controller.
type stubSessionForAttention struct {
	*control.Controller
	status control.RuntimeStatus
}

func (s *stubSessionForAttention) RuntimeStatus() control.RuntimeStatus {
	return s.status
}

func TestTabMetaShowsPendingAttentionBeforePersistence(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "pending-meta")
	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp, disabledMCP: map[string]ServerView{}}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}

	// Queue attention; sidecar should NOT exist yet.
	app.queueNeedsAttention(tab.ID, 2000)

	meta := app.tabMeta(tab, true)
	if !meta.NeedsAttention {
		t.Fatal("tabMeta should show NeedsAttention from pendingAttentionAt before sidecar exists")
	}
	if meta.NeedsAttentionAt != 2000 {
		t.Fatalf("NeedsAttentionAt = %d, want 2000", meta.NeedsAttentionAt)
	}

	// Persist and verify the sidecar now holds the same value.
	if err := persistTabAttention(tab); err != nil {
		t.Fatalf("persistTabAttention: %v", err)
	}
	meta2 := app.tabMeta(tab, true)
	if !meta2.NeedsAttention {
		t.Fatal("tabMeta should still show NeedsAttention after persistence")
	}
}

func TestTabMetaKeepsEarliestAttentionTimestamp(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "earliest-meta")
	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	if err := setNeedsAttention(sp, 1000); err != nil {
		t.Fatalf("setNeedsAttention: %v", err)
	}
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp, disabledMCP: map[string]ServerView{}}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}

	app.queueNeedsAttention(tab.ID, 2000)
	meta := app.tabMeta(tab, false)
	if !meta.NeedsAttention || meta.NeedsAttentionAt != 1000 {
		t.Fatalf("attention = %+v, want earliest timestamp 1000", meta)
	}
}

func TestTurnDoneDoesNotMarkActiveTabAttention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active-turn-done.jsonl")
	app, tab := appWithTab(t, path)

	tab.sink.Emit(event.Event{Kind: event.TurnDone})
	immediate := app.tabMeta(tab, true)
	if immediate.NeedsAttention || immediate.NeedsAttentionAt != 0 {
		t.Fatalf("immediate attention = %+v, want active completed tab to stay read", immediate)
	}

	waitForAutosaveIdle(t, tab)
	persisted := app.tabMeta(tab, true)
	if persisted.NeedsAttention || persisted.NeedsAttentionAt != 0 {
		t.Fatalf("persisted attention = %+v, want active completed tab to stay read", persisted)
	}
}

func TestTurnDoneMarksDetachedRuntimeAttention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "detached-turn-done.jsonl")
	app, tab := appWithTab(t, path)
	delete(app.tabs, tab.ID)
	app.activeTabID = ""
	app.detachedSessions = map[string]*WorkspaceTab{sessionRuntimeKey(path): tab}

	tab.sink.Emit(event.Event{Kind: event.TurnDone})
	meta := app.tabMeta(tab, false)
	if !meta.NeedsAttention || meta.NeedsAttentionAt == 0 {
		t.Fatalf("detached attention = %+v, want completed detached runtime to need attention", meta)
	}
	waitForAutosaveIdle(t, tab)
}

func TestCLITurnDoneDoesNotNeedAttention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli-turn-done.jsonl")
	app, tab := appWithTab(t, path)
	tab.runtimeSourcePath = path
	tab.runtimeSource = "cli"

	tab.sink.Emit(event.Event{Kind: event.TurnDone})
	meta := app.tabMeta(tab, true)
	if meta.NeedsAttention || meta.NeedsAttentionAt != 0 {
		t.Fatalf("CLI completion attention = %+v, want none", meta)
	}
	waitForAutosaveIdle(t, tab)
}

func TestTabMetaShowsWaitingUserAsNeedsAttention(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "waiting-meta")
	ctrl := controllerWithContent(t, sp)
	mockCtrl := &stubSessionForAttention{
		Controller: ctrl,
		status: control.RuntimeStatus{
			Mode:              control.RuntimeModeWaitingUser,
			Running:           true,
			RunningWork:       false,
			PendingPrompt:     true,
			ForegroundActive:  true,
			ActiveRuntimeWork: true,
		},
	}
	tab := &WorkspaceTab{
		ID:          "tab",
		Ctrl:        mockCtrl,
		Scope:       "global",
		SessionPath: sp,
		Ready:       true,
		disabledMCP: map[string]ServerView{},
	}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}

	meta := app.tabMeta(tab, false)
	if !meta.NeedsAttention {
		t.Fatal("waiting_user tab should show NeedsAttention=true in real time")
	}
	if meta.RunningWork {
		t.Fatal("waiting_user tab should have RunningWork=false")
	}
	if !meta.PendingPrompt {
		t.Fatal("waiting_user tab should have PendingPrompt=true")
	}
	mockCtrl.status = control.RuntimeStatus{Mode: control.RuntimeModeIdle}
	resolved := app.tabMeta(tab, false)
	if resolved.NeedsAttention {
		t.Fatalf("resolved waiting_user attention = %+v, want cleared runtime-only attention", resolved)
	}
}

func TestCLIWaitingUserDoesNotNeedAttention(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "cli-waiting")
	ctrl := controllerWithContent(t, sp)
	mockCtrl := &stubSessionForAttention{
		Controller: ctrl,
		status: control.RuntimeStatus{
			Mode:              control.RuntimeModeWaitingUser,
			Running:           true,
			RunningWork:       false,
			PendingPrompt:     true,
			ForegroundActive:  true,
			ActiveRuntimeWork: true,
		},
	}
	tab := &WorkspaceTab{
		ID:                "tab",
		Ctrl:              mockCtrl,
		Scope:             "global",
		SessionPath:       sp,
		Ready:             true,
		runtimeSourcePath: sp,
		runtimeSource:     "cli",
		disabledMCP:       map[string]ServerView{},
	}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}

	meta := app.tabMeta(tab, false)
	if meta.NeedsAttention {
		t.Fatal("CLI waiting_user tab should NOT show NeedsAttention")
	}
	if meta.SessionSource != "cli" {
		t.Fatalf("SessionSource = %q, want cli", meta.SessionSource)
	}
}

func TestClearTabAttentionRemovesFromMeta(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "clear-meta")
	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp, disabledMCP: map[string]ServerView{}}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}}

	app.queueNeedsAttention(tab.ID, 3000)
	if err := persistTabAttention(tab); err != nil {
		t.Fatalf("persistTabAttention: %v", err)
	}
	meta := app.tabMeta(tab, false)
	if !meta.NeedsAttention {
		t.Fatal("before clear, NeedsAttention should be true")
	}

	if err := clearTabAttention(tab); err != nil {
		t.Fatalf("clearTabAttention: %v", err)
	}
	meta2 := app.tabMeta(tab, false)
	if meta2.NeedsAttention {
		t.Fatal("after clear, NeedsAttention should be false — no rebound")
	}

	// Verify no pending attention lingers.
	if err := persistTabAttention(tab); err != nil {
		t.Fatalf("persistTabAttention after clear: %v", err)
	}
	meta3 := app.tabMeta(tab, false)
	if meta3.NeedsAttention {
		t.Fatal("persist after clear should not resurrect NeedsAttention")
	}
}

// ── takeoverFromCLI tests ────────────────────────────────────────────────────

func TestTakeoverFromCLIClearsRuntimeSource(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "takeover")
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}

	// Set CLI source first.
	if err := app.setActiveSessionSource("cli"); err != nil {
		t.Fatalf("setActiveSessionSource: %v", err)
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "cli" {
		t.Fatalf("SessionSource before takeover = %q, want cli", source)
	}

	// Takeover.
	if err := app.takeoverFromCLI(tab); err != nil {
		t.Fatalf("takeoverFromCLI: %v", err)
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "" {
		t.Fatalf("SessionSource after takeover = %q, want empty", source)
	}
}

func TestTakeoverFromCLIIdempotent(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "takeover-idem")
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}

	if err := app.setActiveSessionSource("cli"); err != nil {
		t.Fatalf("setActiveSessionSource: %v", err)
	}
	// First takeover.
	if err := app.takeoverFromCLI(tab); err != nil {
		t.Fatalf("first takeoverFromCLI: %v", err)
	}
	// Second takeover — idempotent.
	if err := app.takeoverFromCLI(tab); err != nil {
		t.Fatalf("second takeoverFromCLI: %v", err)
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "" {
		t.Fatalf("SessionSource after double takeover = %q, want empty", source)
	}
}

func TestTakeoverFromCLINonCLITabNoOp(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "takeover-noncli")
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}

	// No source set — desktop-local by default.
	if err := app.takeoverFromCLI(tab); err != nil {
		t.Fatalf("takeoverFromCLI on non-CLI tab: %v", err)
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "" {
		t.Fatalf("SessionSource = %q, want empty", source)
	}
}

func TestTakeoverFromCLINoTranscriptSafe(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "takeover-blank")
	// Don't create the file — blank session, no transcript.
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}

	// Set CLI source as runtime-only (no transcript file exists).
	if err := app.setActiveSessionSource("cli"); err != nil {
		t.Fatalf("setActiveSessionSource: %v", err)
	}

	// Takeover should succeed despite no transcript file.
	if err := app.takeoverFromCLI(tab); err != nil {
		t.Fatalf("takeoverFromCLI on blank session: %v", err)
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "" {
		t.Fatalf("SessionSource after blank takeover = %q, want empty", source)
	}
	if _, err := os.Stat(agent.BranchMetaPath(sp)); !os.IsNotExist(err) {
		t.Fatalf("blank takeover created orphan sidecar: %v", err)
	}
}

func TestTakeoverFromCLILoadsPersistedSource(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "takeover-persisted-only")
	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	if err := stampSessionSource(sp, "cli"); err != nil {
		t.Fatalf("stampSessionSource: %v", err)
	}
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}

	if err := app.takeoverFromCLI(tab); err != nil {
		t.Fatalf("takeoverFromCLI: %v", err)
	}
	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("LoadBranchMeta: %v", err)
	}
	if meta.SessionSource != "" {
		t.Fatalf("persisted SessionSource = %q, want empty", meta.SessionSource)
	}
}

func TestTakeoverFromCLIPersistsClearedSource(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "takeover-persist")
	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	if _, err := agent.EnsureBranchMeta(sp); err != nil {
		t.Fatalf("EnsureBranchMeta: %v", err)
	}
	if err := stampSessionSource(sp, "cli"); err != nil {
		t.Fatalf("stampSessionSource: %v", err)
	}

	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	tab.runtimeSourcePath = sp
	tab.runtimeSource = "cli"

	if err := app.takeoverFromCLI(tab); err != nil {
		t.Fatalf("takeoverFromCLI: %v", err)
	}

	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil {
		t.Fatalf("LoadBranchMeta: %v", err)
	}
	if meta.SessionSource != "" {
		t.Fatalf("persisted SessionSource = %q, want empty", meta.SessionSource)
	}
}

func TestTakeoverFromCLIAllowsAttentionAfter(t *testing.T) {
	dir := t.TempDir()
	sp := agent.NewSessionPath(dir, "takeover-attn")
	if err := os.WriteFile(sp, []byte(""), 0o644); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}

	// CLI session: attention blocked.
	if err := app.setActiveSessionSource("cli"); err != nil {
		t.Fatalf("setActiveSessionSource: %v", err)
	}
	app.queueNeedsAttention(tab.ID, 1000)
	if tab.pendingAttentionAt != 0 {
		t.Fatalf("CLI should block attention, got %d", tab.pendingAttentionAt)
	}

	// Takeover: attention now allowed.
	if err := app.takeoverFromCLI(tab); err != nil {
		t.Fatalf("takeoverFromCLI: %v", err)
	}
	app.activeTabID = ""
	app.queueNeedsAttention(tab.ID, 2000)
	if tab.pendingAttentionAt != 2000 {
		t.Fatalf("after takeover, attention should be queued, got %d", tab.pendingAttentionAt)
	}
}

func TestDesktopSubmitTakesOverCLIWithoutAttentionWhileActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-submit.jsonl")
	app, tab := appWithTab(t, path)
	ctrl := tab.Ctrl.(*control.Controller)
	t.Cleanup(ctrl.Close)
	if err := app.setActiveSessionSource("cli"); err != nil {
		t.Fatalf("setActiveSessionSource: %v", err)
	}

	if err := app.SubmitToTab(tab.ID, "desktop input"); err != nil {
		t.Fatalf("SubmitToTab: %v", err)
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "" {
		t.Fatalf("SessionSource after desktop submit = %q, want empty", source)
	}
	waitForControllerIdle(t, ctrl)
	app.queueNeedsAttention(tab.ID, 4000)
	if tab.pendingAttentionAt != 0 {
		t.Fatalf("active desktop-owned session attention = %d, want 0", tab.pendingAttentionAt)
	}
}

func TestAutomatedSubmitKeepsCLISource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automated-submit.jsonl")
	app, tab := appWithTab(t, path)
	ctrl := tab.Ctrl.(*control.Controller)
	t.Cleanup(ctrl.Close)
	if err := app.setActiveSessionSource("cli"); err != nil {
		t.Fatalf("setActiveSessionSource: %v", err)
	}

	if err := app.submitToTab(tab.ID, "remote input", false); err != nil {
		t.Fatalf("submitToTab: %v", err)
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "cli" {
		t.Fatalf("SessionSource after automated submit = %q, want cli", source)
	}
	waitForControllerIdle(t, ctrl)
}

func TestHeartbeatSubmitKeepsCLISource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat-submit.jsonl")
	app, tab := appWithTab(t, path)
	ctrl := tab.Ctrl.(*control.Controller)
	t.Cleanup(ctrl.Close)
	if err := app.setActiveSessionSource("cli"); err != nil {
		t.Fatalf("setActiveSessionSource: %v", err)
	}

	if ok := app.submitUserTurnToTab(tab.ID, "heartbeat input"); !ok {
		t.Fatal("submitUserTurnToTab returned false")
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "cli" {
		t.Fatalf("SessionSource after heartbeat submit = %q, want cli", source)
	}
	waitForControllerIdle(t, ctrl)
}

func TestRejectedDesktopSubmitKeepsCLISource(t *testing.T) {
	sp := agent.NewSessionPath(t.TempDir(), "rejected-submit")
	tab := &WorkspaceTab{ID: "tab", SessionPath: sp, runtimeSourcePath: sp, runtimeSource: "cli"}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}

	if err := app.SubmitToTab(tab.ID, "cannot send"); err == nil {
		t.Fatal("SubmitToTab should fail without a controller")
	}
	if source := app.tabMeta(tab, true).SessionSource; source != "cli" {
		t.Fatalf("SessionSource after rejected submit = %q, want cli", source)
	}
}
