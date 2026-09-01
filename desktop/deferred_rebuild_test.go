package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/jobs"
)

// waitConfigRebuildFlag polls the pending flag until it equals want or times out.
func waitConfigRebuildFlag(t *testing.T, app *App, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if app.configRebuildNeeded.Load() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("configRebuildNeeded never became %v", want)
}

func TestIsBackgroundJobTerminalNotice(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"finished", "background bash finished: bash-1", true},
		{"failed", "background task failed: task-2 — boom", true},
		{"killed", "background bash killed: bash-3", true},
		{"started", "background bash started: bash-1", false},
		{"stalled", "background bash may be stalled: bash-1 — still running", false},
		{"unrelated", "something else finished: nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isBackgroundJobTerminalNotice(event.Event{Kind: event.Notice, Text: tc.text})
			if got != tc.want {
				t.Fatalf("isBackgroundJobTerminalNotice(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestTryDeferredConfigRebuildKeepsPendingWhileJobRunning(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	ctrl := newBackgroundJobController(t, "deferred-job")
	app.setTestCtrl(ctrl, "")

	app.configRebuildNeeded.Store(true)
	app.TryDeferredConfigRebuild()

	if !app.configRebuildNeeded.Load() {
		t.Fatal("pending flag cleared while a background job was still running")
	}
	if app.activeCtrl() != ctrl {
		t.Fatal("active controller changed while a background job was still running")
	}
}

func TestTryDeferredConfigRebuildRestoresPendingOnRebuildError(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.ctx = context.Background() // make rebuild() attempt real work and fail on "no active tab"

	app.configRebuildNeeded.Store(true)
	app.TryDeferredConfigRebuild()

	if !app.configRebuildNeeded.Load() {
		t.Fatal("pending flag cleared despite a failed rebuild")
	}
}

func TestTerminalJobNoticeConsumesPendingWhenIdle(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp() // a.ctx == nil: rebuild is a no-op that still consumes the flag
	app.tabs["job"] = &WorkspaceTab{
		ID:          "job",
		Scope:       "global",
		Ready:       true,
		disabledMCP: map[string]ServerView{},
	}
	app.activeTabID = "job"
	app.configRebuildNeeded.Store(true)

	sink := &tabEventSink{tabID: "job", app: app}
	sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: "background bash finished: bash-1"})

	waitConfigRebuildFlag(t, app, false)
}

func TestStartedAndStalledNoticesDoNotConsumePending(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.tabs["job"] = &WorkspaceTab{
		ID:          "job",
		Scope:       "global",
		Ready:       true,
		disabledMCP: map[string]ServerView{},
	}
	app.activeTabID = "job"
	app.configRebuildNeeded.Store(true)

	sink := &tabEventSink{tabID: "job", app: app}
	sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: "background bash started: bash-1"})
	sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "background bash may be stalled: bash-1 — still running"})

	time.Sleep(50 * time.Millisecond)
	if !app.configRebuildNeeded.Load() {
		t.Fatal("pending flag consumed by a non-terminal job notice")
	}
}

func TestTerminalJobNoticeWaitsForLastJob(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := desktopSessionDir(globalTabWorkspaceRoot())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, "deferred-multi-job.jsonl")

	app := NewApp() // a.ctx == nil: rebuild is a no-op that still consumes the flag
	app.tabs["job"] = &WorkspaceTab{
		ID:            "job",
		Scope:         "global",
		WorkspaceRoot: globalTabWorkspaceRoot(),
		SessionPath:   path,
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}
	app.tabOrder = []string{"job"}
	app.activeTabID = "job"

	sink := &tabEventSink{tabID: "job", app: app}
	app.tabs["job"].sink = sink

	jm := jobs.NewManager(sink)
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	block := func(release <-chan struct{}) func(context.Context, io.Writer) (string, error) {
		return func(ctx context.Context, _ io.Writer) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
				return "", nil
			}
		}
	}
	jm.StartForSession(agent.BranchID(path), "bash", "job-a", block(releaseA))
	jm.StartForSession(agent.BranchID(path), "bash", "job-b", block(releaseB))

	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "job", Jobs: jm})
	app.tabs["job"].Ctrl = ctrl
	defer ctrl.Close()

	app.configRebuildNeeded.Store(true)

	// First job finishes while the second is still running: keep pending.
	close(releaseA)
	waitJobCount(t, ctrl, 1)
	if !app.configRebuildNeeded.Load() {
		t.Fatal("pending flag consumed while a second job was still running")
	}
	if app.activeCtrl() != ctrl {
		t.Fatal("active controller replaced while a second job was still running")
	}

	// Last job finishes: consume pending and attempt the rebuild.
	close(releaseB)
	waitConfigRebuildFlag(t, app, false)
	waitJobCount(t, ctrl, 0)
}

func waitJobCount(t *testing.T, ctrl control.SessionAPI, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(ctrl.Jobs()) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("controller job count never became %d (now %d)", want, len(ctrl.Jobs()))
}
