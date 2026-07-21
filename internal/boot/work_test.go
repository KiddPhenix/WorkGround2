package boot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/config"
	"workground2/internal/event"
	"workground2/internal/work"
)

// TestWorkDisabledDoesNotTouchWorkDir verifies that when Work is disabled
// (default), boot does not create any Work data directories and the Controller
// has no Work service.
func TestWorkDisabledDoesNotTouchWorkDir(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	// Write a minimal config with no [work] section (default disabled).
	cfgContent := `
config_version = 3
default_model = "deepseek-flash"
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build with the test dir as root.
	sink := &recordSink{}
	ctrl, err := Build(context.Background(), Options{
		Sink:          sink,
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build with default config: %v", err)
	}
	defer ctrl.Close()

	// WorkControl should return an error (disabled).
	wc := ctrl.WorkControl()
	if wc == nil {
		t.Fatal("WorkControl returned nil interface")
	}
	_, err = wc.GetWork(context.Background(), "w-1")
	if err == nil {
		t.Fatal("GetWork should return error when Work is disabled")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error should mention disabled, got: %v", err)
	}

	// WorkViews should be nil.
	if v := ctrl.WorkViews(); v != nil {
		t.Fatal("WorkViews should be nil when Work is disabled")
	}
	if executor := ctrl.TaskExecutor(); executor != nil {
		t.Fatal("TaskExecutor should be nil when Work is disabled")
	}

	// No project Work dir should have been created.
	workDir := config.ProjectWorkDir(dir)
	if workDir != "" {
		if _, err := os.Stat(workDir); !os.IsNotExist(err) {
			t.Fatalf("Work dir %q should not exist when Work is disabled", workDir)
		}
	}

	before := systemMessage(ctrl.History())
	oldPath := filepath.Join(ctrl.SessionDir(), "existing.jsonl")
	ctrl.SetSessionPath(oldPath)
	if err := ctrl.NewSession(); err != nil {
		t.Fatalf("NewSession with Work disabled: %v", err)
	}
	if ctrl.SessionPath() == "" || ctrl.SessionPath() == oldPath {
		t.Fatalf("NewSession path = %q, want a fresh path", ctrl.SessionPath())
	}
	if after := systemMessage(ctrl.History()); after != before {
		t.Fatalf("Work-disabled NewSession changed cache-stable prefix\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestWorkEnabledWiresService verifies that enabling [work] in config assembles
// the Service and injects it into the Controller.
func TestWorkEnabledWiresService(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "works")

	// Write config with [work] enabled.
	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &recordSink{}
	ctrl, err := Build(context.Background(), Options{
		Sink:          sink,
		WorkspaceRoot: dir,
		WorkDir:       workDir,
		SessionRefs:   work.NewMemorySessionRefStore(work.WithRetention(0)),
	})
	if err != nil {
		t.Fatalf("Build with work enabled: %v", err)
	}
	defer ctrl.Close()

	// WorkControl should be functional.
	wc := ctrl.WorkControl()
	if wc == nil {
		t.Fatal("WorkControl returned nil interface")
	}
	// GetWork on non-existent should return a "not found" error, not "disabled".
	_, err = wc.GetWork(context.Background(), "w-nonexistent")
	if err == nil {
		t.Fatal("GetWork should return error for non-existent Work")
	}
	if strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error should be 'not found', not 'disabled': %v", err)
	}

	// WorkViews should be non-nil.
	v := ctrl.WorkViews()
	if v == nil {
		t.Fatal("WorkViews should be non-nil when Work is enabled")
	}
	if executor := ctrl.TaskExecutor(); executor == nil {
		t.Fatal("TaskExecutor should be non-nil when Work is enabled")
	}

	// Verify the info notice was emitted.
	found := false
	for _, ev := range sink.events {
		if ev.Kind == event.Notice && ev.Level == event.LevelInfo && strings.Contains(ev.Text, "work: feature enabled") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'work: feature enabled' info notice")
	}
}

func TestWorkEnabledSessionRefInitFailureIsExplicit(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte("config_version = 3\ndefault_model = \"deepseek-flash\"\n\n[work]\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctrl, err := Build(context.Background(), Options{
		WorkspaceRoot:  dir,
		WorkDir:        filepath.Join(t.TempDir(), "works"),
		SessionRefsErr: errors.New("injected Session ref failure"),
		Sink:           event.Discard,
	})
	if err == nil {
		if ctrl != nil {
			ctrl.Close()
		}
		t.Fatal("Build succeeded without its Session cleanup guard")
	}
	if !strings.Contains(err.Error(), "initialize Work Session refs") {
		t.Fatalf("Build error = %v", err)
	}
}

func TestWorkEnabledUnavailableStoreFailsBuild(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte("config_version = 3\ndefault_model = \"deepseek-flash\"\n\n[work]\nenabled = true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badPath, []byte("occupied"), 0o644); err != nil {
		t.Fatalf("write unavailable store fixture: %v", err)
	}
	ctrl, err := Build(context.Background(), Options{
		WorkspaceRoot: dir,
		WorkDir:       badPath,
		Sink:          event.Discard,
	})
	if err == nil {
		if ctrl != nil {
			ctrl.Close()
		}
		t.Fatal("Build succeeded with an unavailable non-empty WorkDir")
	}
	if !strings.Contains(err.Error(), "initialize Work store") || !strings.Contains(err.Error(), badPath) {
		t.Fatalf("Build error = %v, want explicit Work store path", err)
	}
}

func TestWorkDisabledIgnoresUnavailableStoreOverride(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte("config_version = 3\ndefault_model = \"deepseek-flash\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badPath, []byte("occupied"), 0o644); err != nil {
		t.Fatalf("write unavailable store fixture: %v", err)
	}
	ctrl, err := Build(context.Background(), Options{
		WorkspaceRoot: dir,
		WorkDir:       badPath,
		Sink:          event.Discard,
	})
	if err != nil {
		t.Fatalf("Work-disabled Build inspected WorkDir: %v", err)
	}
	defer ctrl.Close()
	if ctrl.WorkViews() != nil {
		t.Fatal("Work-disabled Build created Work transport")
	}
}

func TestWorkFlagKeepsSystemPromptPrefix(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "WorkGround2.toml")
	writeConfig := func(enabled bool) {
		body := "config_version = 3\ndefault_model = \"deepseek-flash\"\n"
		if enabled {
			body += "\n[work]\nenabled = true\n"
		}
		if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	writeConfig(false)
	disabled, err := Build(context.Background(), Options{WorkspaceRoot: dir, Sink: event.Discard})
	if err != nil {
		t.Fatalf("Build disabled: %v", err)
	}
	disabledPrefix := systemMessage(disabled.History())
	disabled.Close()

	writeConfig(true)
	enabled, err := Build(context.Background(), Options{WorkspaceRoot: dir, Sink: event.Discard})
	if err != nil {
		t.Fatalf("Build enabled: %v", err)
	}
	enabledPrefix := systemMessage(enabled.History())
	enabled.Close()

	if enabledPrefix != disabledPrefix {
		t.Fatalf("work.enabled changed cache-stable system prompt prefix\n--- disabled ---\n%s\n--- enabled ---\n%s", disabledPrefix, enabledPrefix)
	}
	for _, dynamic := range []string{"work.events.jsonl", config.ProjectWorkDir(dir), "WorkViewEvent"} {
		if dynamic != "" && strings.Contains(enabledPrefix, dynamic) {
			t.Fatalf("dynamic Work data %q leaked into system prompt", dynamic)
		}
	}
}

type recordSink struct {
	events []event.Event
}

func (s *recordSink) Emit(e event.Event) {
	s.events = append(s.events, e)
}

// TestWorkBootBlobStoreWired verifies that FileWorkStore is wired as BlobStore
// during boot, enabling >4096-byte cornerstone content persistence.
func TestWorkBootBlobStoreWired(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "works")

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &recordSink{}
	ctrl, err := Build(context.Background(), Options{
		Sink:          sink,
		WorkspaceRoot: dir,
		WorkDir:       workDir,
		SessionRefs:   work.NewMemorySessionRefStore(work.WithRetention(0)),
	})
	if err != nil {
		t.Fatalf("Build with work enabled: %v", err)
	}
	defer func() {
		if ctrl != nil {
			ctrl.Close()
		}
	}()

	wc := ctrl.WorkControl()
	ctx := context.Background()

	// Create a Work first.
	w, err := wc.CreateWork(ctx, work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		Name:         "blob-test",
		RequestID:    "create-blob-test",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}

	// Get the current revision after creation.
	view, err := wc.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWork after create: %v", err)
	}
	currentRev := view.Revision

	// Build >4096-byte content — must be stored as blob, not inline.
	largeContent := strings.Repeat("a", 5000)

	// Pin a cornerstone with large content.
	result, err := wc.PinCornerstone(ctx, w.ID, work.PinCornerstoneInput{
		Type:             work.CornerstoneInstruction,
		Title:            "large blob test",
		Content:          largeContent,
		Ref:              work.CornerstoneRef{Kind: "inline"},
		Mode:             work.CornerstoneSnapshot,
		Required:         false,
		ExpectedRevision: currentRev,
		RequestID:        "pin-blob-test-1",
	})
	if err != nil {
		t.Fatalf("PinCornerstone with >4096 content: %v", err)
	}
	if result.Cornerstone == nil {
		t.Fatal("PinCornerstone returned nil cornerstone")
	}
	// Content >4096 should be in blob store, not inline.
	if result.Cornerstone.Ref.BlobDigest == "" {
		t.Fatal("large cornerstone content should have a blob digest")
	}
	if len(result.Cornerstone.Content) > 500 {
		t.Fatalf("large cornerstone content should be truncated to preview, got %d bytes", len(result.Cornerstone.Content))
	}

	// Close the first Controller and rebuild from the same WorkDir. This proves
	// both the event projection and the content-addressed blob survive restart.
	ctrl.Close()
	ctrl = nil
	restarted, err := Build(context.Background(), Options{
		Sink:          event.Discard,
		WorkspaceRoot: dir,
		WorkDir:       workDir,
		SessionRefs:   work.NewMemorySessionRefStore(work.WithRetention(0)),
	})
	if err != nil {
		t.Fatalf("Build after restart: %v", err)
	}
	defer restarted.Close()
	restartedWork := restarted.WorkControl()
	reloaded, err := restartedWork.GetWork(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWork after restart: %v", err)
	}
	var reloadedCornerstone *work.Cornerstone
	for _, cs := range reloaded.Work.Cornerstones {
		if cs.Title == "large blob test" {
			copy := cs
			reloadedCornerstone = &copy
			if cs.Ref.BlobDigest == "" {
				t.Fatal("reloaded cornerstone missing blob digest")
			}
			if cs.Status != work.CornerstoneActive {
				t.Fatalf("reloaded cornerstone status = %s, want active", cs.Status)
			}
			break
		}
	}
	if reloadedCornerstone == nil {
		t.Fatal("large cornerstone not found in projection after restart")
	}

	// Snapshot validation reads and verifies the blob through the manager wired
	// by the second boot. A nil/missing BlobStore fails this operation explicitly.
	verified, err := restartedWork.RefreshCornerstone(ctx, w.ID, work.RefreshCornerstoneInput{
		CornerstoneID:    reloadedCornerstone.ID,
		ExpectedRevision: reloaded.Revision,
		RequestID:        "verify-blob-after-restart-1",
	})
	if err != nil {
		t.Fatalf("RefreshCornerstone after restart: %v", err)
	}
	if verified.Cornerstone == nil || verified.Cornerstone.Status != work.CornerstoneActive {
		t.Fatalf("verified cornerstone after restart = %#v, want active", verified.Cornerstone)
	}
}

func TestWorkBootURLResolverUsesGuardedWebFetch(t *testing.T) {
	isolateConfigHome(t)
	root := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "works")
	if err := os.WriteFile(filepath.Join(root, "WorkGround2.toml"), []byte(`
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("resolved URL source"))
	}))
	defer server.Close()

	ctrl, err := Build(t.Context(), Options{
		Sink:          event.Discard,
		WorkspaceRoot: root,
		WorkDir:       workDir,
		SessionRefs:   work.NewMemorySessionRefStore(work.WithRetention(0)),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	wc := ctrl.WorkControl()
	created, err := wc.CreateWork(t.Context(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		Name:         "url-resolver-test",
		RequestID:    "create-url-resolver-test",
	})
	if err != nil {
		t.Fatalf("CreateWork: %v", err)
	}
	view, err := wc.GetWork(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	pinned, err := wc.PinCornerstone(t.Context(), created.ID, work.PinCornerstoneInput{
		Type:             work.CornerstoneSource,
		Title:            "guarded URL",
		Content:          "previous URL source",
		Ref:              work.CornerstoneRef{Kind: "url", URL: server.URL},
		Mode:             work.CornerstoneLiveRef,
		ExpectedRevision: view.Revision,
		RequestID:        "pin-url-resolver-test",
	})
	if err != nil {
		t.Fatalf("PinCornerstone: %v", err)
	}
	refreshed, err := wc.RefreshCornerstone(t.Context(), created.ID, work.RefreshCornerstoneInput{
		CornerstoneID:    pinned.Cornerstone.ID,
		ExpectedRevision: pinned.Revision,
		RequestID:        "refresh-url-resolver-test",
	})
	if err != nil {
		t.Fatalf("RefreshCornerstone: %v", err)
	}
	if refreshed.Cornerstone == nil || refreshed.Cornerstone.Status != work.CornerstoneStale {
		t.Fatalf("refreshed Cornerstone = %#v, want stale", refreshed.Cornerstone)
	}
	if refreshed.Resolution == nil || !strings.Contains(refreshed.Resolution.CandidateContent, "resolved URL source") {
		t.Fatalf("URL resolution = %#v", refreshed.Resolution)
	}
}
