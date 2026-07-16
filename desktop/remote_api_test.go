package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/autoresearch"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

func TestRemoteAPIActiveWorkspaceReadyStates(t *testing.T) {
	root := t.TempDir()
	ctrl := control.New(control.Options{Label: "ready"})
	defer ctrl.Close()

	tests := []struct {
		name      string
		tab       *WorkspaceTab
		wantReady bool
		wantErr   string
	}{
		{
			name:      "ready controller",
			tab:       &WorkspaceTab{ID: "tab", Scope: "project", WorkspaceRoot: root, Ready: true, Ctrl: ctrl},
			wantReady: true,
		},
		{
			name:      "still building",
			tab:       &WorkspaceTab{ID: "tab", Scope: "project", WorkspaceRoot: root},
			wantReady: false,
		},
		{
			name:    "startup error",
			tab:     &WorkspaceTab{ID: "tab", Scope: "project", WorkspaceRoot: root, Ready: true, StartupErr: "boot failed"},
			wantErr: "workspace failed to start: boot failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{tabs: map[string]*WorkspaceTab{"tab": tt.tab}, activeTabID: "tab"}
			api := &remoteAPI{app: app}
			ready, err := api.activeWorkspaceReady(root)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("activeWorkspaceReady: %v", err)
			}
			if ready != tt.wantReady {
				t.Fatalf("ready = %v, want %v", ready, tt.wantReady)
			}
		})
	}
}

func TestDesktopEmbeddedSessionOptions(t *testing.T) {
	got := desktopEmbeddedSessionOptions("worker", control.ToolApprovalYolo)
	if _, ok := got["background"]; ok || got["sessionName"] != "worker" || got["toolApprovalMode"] != control.ToolApprovalYolo {
		t.Fatalf("session options = %+v", got)
	}
}

func TestDesktopEmbeddedSubmitBodyUsesSessionID(t *testing.T) {
	got := desktopEmbeddedSubmitBody("hello", "session-123", control.ToolApprovalYolo)
	if got["sessionId"] != "session-123" || got["prompt"] != "hello" || got["toolApprovalMode"] != control.ToolApprovalYolo {
		t.Fatalf("submit body = %#v", got)
	}
	if _, exists := got["session"]; exists {
		t.Fatalf("legacy path target leaked into submit body: %#v", got)
	}
}

func TestDesktopEmbeddedStatusRequiresSessionID(t *testing.T) {
	if code := desktopEmbeddedStatus(nil); code != 2 {
		t.Fatalf("desktopEmbeddedStatus without SessionID = %d, want 2", code)
	}
}

func TestRemoteAPIWaitForActiveWorkspaceReadyObservesAsyncBuild(t *testing.T) {
	root := t.TempDir()
	tab := &WorkspaceTab{ID: "tab", Scope: "project", WorkspaceRoot: root}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	api := &remoteAPI{app: app}

	ctrl := control.New(control.Options{Label: "ready"})
	defer ctrl.Close()
	go func() {
		time.Sleep(20 * time.Millisecond)
		app.mu.Lock()
		tab.Ctrl = ctrl
		tab.Ready = true
		app.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.waitForActiveWorkspaceReady(ctx, root, time.Second); err != nil {
		t.Fatalf("waitForActiveWorkspaceReady: %v", err)
	}
}

func TestSingleSurfacePruneKeepsExternalSessionAddressable(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	uiPath := filepath.Join(root, "ui.jsonl")
	externalPath := filepath.Join(root, "external.jsonl")
	ui := &WorkspaceTab{ID: "ui-tab", SessionID: "session-ui", Scope: "project", WorkspaceRoot: root, SessionPath: uiPath, Ready: true}
	external := &WorkspaceTab{ID: "external-tab", SessionID: "session-external", Scope: "project", WorkspaceRoot: root, SessionPath: externalPath}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			ui.ID:       ui,
			external.ID: external,
		},
		tabOrder:         []string{ui.ID, external.ID},
		activeTabID:      ui.ID,
		detachedSessions: map[string]*WorkspaceTab{},
	}
	app.trackSession(ui)
	app.trackSession(external)

	if _, err := app.keepOnlyVisibleTab(ui.ID); err != nil {
		t.Fatalf("keepOnlyVisibleTab: %v", err)
	}
	if got := app.sessionByID(external.SessionID); got != external {
		t.Fatalf("external SessionID no longer resolves: got %p want %p", got, external)
	}
	app.mu.RLock()
	_, stillVisible := app.tabs[external.ID]
	detached := app.detachedSessions[sessionRuntimeKey(externalPath)]
	removed := external.removed
	app.mu.RUnlock()
	if stillVisible || detached != external || removed {
		t.Fatalf("pruned external state: visible=%t detached=%p removed=%t", stillVisible, detached, removed)
	}

	ctrl := &remoteStatusCtrlStub{path: externalPath, status: control.RuntimeStatus{Mode: control.RuntimeModeIdle}}
	app.mu.Lock()
	external.Ctrl = ctrl
	external.Ready = true
	app.mu.Unlock()
	api := &remoteAPI{app: app}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.waitForTabReady(ctx, external.ID, time.Second); err != nil {
		t.Fatalf("waitForTabReady after UI prune: %v", err)
	}
	status := api.sessionResponseForTab(app.sessionByID(external.SessionID), "ok")
	if status["sessionId"] != external.SessionID || status["path"] != externalPath {
		t.Fatalf("external status after UI prune = %+v", status)
	}
}

func TestRemoteAPISessionNewAppliesToolApprovalMode(t *testing.T) {
	root := t.TempDir()
	ctrl := control.New(control.Options{Label: "ready", WorkspaceRoot: root, SessionDir: t.TempDir()})
	defer ctrl.Close()
	ctrl.SetToolApprovalMode(control.ToolApprovalAsk)

	tab := &WorkspaceTab{ID: "tab", SessionID: "session-ask", Scope: "project", WorkspaceRoot: root, Ready: true, Ctrl: ctrl}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	api := &remoteAPI{app: app}
	app.trackSession(tab)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/new", bytes.NewBufferString(`{"toolApprovalMode":"yolo"}`))
	rec := httptest.NewRecorder()
	api.handleSessionNew(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["toolApprovalMode"] != control.ToolApprovalYolo {
		t.Fatalf("toolApprovalMode response = %v, want yolo; body=%s", got["toolApprovalMode"], rec.Body.String())
	}
	created := app.sessionByID(desktopEmbeddedStringField(got, "sessionId"))
	if created == nil || created.Ctrl == nil {
		t.Fatalf("created session is unavailable: %+v", got)
	}
	defer app.closeTabRuntime(created)
	if mode := created.Ctrl.ToolApprovalMode(); mode != control.ToolApprovalYolo {
		t.Fatalf("created controller tool approval mode = %q, want yolo", mode)
	}
	if mode := ctrl.ToolApprovalMode(); mode != control.ToolApprovalAsk {
		t.Fatalf("active UI controller was mutated: %q", mode)
	}
}

func TestRemoteAPISessionNewForcesFreshBlankSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	root := t.TempDir()
	dir := t.TempDir()
	oldPath := agent.NewSessionPath(dir, "ready")
	sess := &agent.Session{}
	sess.Replace([]provider.Message{{Role: provider.RoleSystem, Content: "sys"}})
	ag := agent.New(stubProvider{}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Executor:      ag,
		Label:         "ready",
		WorkspaceRoot: root,
		SessionDir:    dir,
		SessionPath:   oldPath,
		Sink:          event.Discard,
	})
	defer ctrl.Close()

	oldTopicID := "topic_old"
	tab := &WorkspaceTab{
		ID:            "tab",
		SessionID:     "session-worker",
		Scope:         "project",
		WorkspaceRoot: root,
		Ready:         true,
		Ctrl:          ctrl,
		SessionPath:   oldPath,
		TopicID:       oldTopicID,
		TopicTitle:    "Old topic",
		disabledMCP:   map[string]ServerView{},
	}
	if err := tab.ensureSessionLease(oldPath); err != nil {
		t.Fatalf("ensure old lease: %v", err)
	}
	defer tab.releaseSessionLease()
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	api := &remoteAPI{app: app}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/new", nil)
	rec := httptest.NewRecorder()
	api.handleSessionNew(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	created := app.sessionByID(desktopEmbeddedStringField(got, "sessionId"))
	if created == nil {
		t.Fatalf("created session is unavailable: %+v", got)
	}
	defer app.closeTabRuntime(created)
	newPath := created.currentSessionPath()
	if newPath == "" || filepath.Clean(newPath) == filepath.Clean(oldPath) {
		t.Fatalf("session path = %q, want fresh path distinct from %q", newPath, oldPath)
	}
	if created.TopicID == "" || created.TopicID == oldTopicID {
		t.Fatalf("topic ID = %q, want fresh ID distinct from %q", created.TopicID, oldTopicID)
	}
	if leaseKey := created.sessionLeaseRuntimeKey(); leaseKey != sessionRuntimeKey(newPath) {
		t.Fatalf("lease key = %q, want %q", leaseKey, sessionRuntimeKey(newPath))
	}
	if source := app.tabMeta(created, false).SessionSource; source != "cli" {
		t.Fatalf("CLI-created runtime session source = %q, want cli", source)
	}
	if ctrl.SessionPath() != oldPath || tab.TopicID != oldTopicID {
		t.Fatal("external creation mutated the active UI session")
	}
	if responsePath, _ := got["path"].(string); filepath.Clean(responsePath) != filepath.Clean(newPath) {
		t.Fatalf("response path = %q, want %q; body=%s", responsePath, newPath, rec.Body.String())
	}
}

func TestRemoteAPISessionNewDoesNotEmitSessionActivated(t *testing.T) {
	isolateDesktopUserDirs(t)

	root := t.TempDir()
	dir := t.TempDir()
	oldPath := agent.NewSessionPath(dir, "ready")
	ctrl := control.New(control.Options{Label: "ready", WorkspaceRoot: root, SessionDir: dir, SessionPath: oldPath})
	defer ctrl.Close()
	tab := &WorkspaceTab{
		ID:            "tab",
		Scope:         "project",
		WorkspaceRoot: root,
		Ready:         true,
		Ctrl:          ctrl,
		SessionPath:   oldPath,
		disabledMCP:   map[string]ServerView{},
	}
	events := make(chan sessionActivatedEvent, 1)
	app := &App{
		tabs:        map[string]*WorkspaceTab{"tab": tab},
		activeTabID: "tab",
	}
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != "session:activated" {
			return
		}
		if len(payload) != 1 {
			t.Errorf("session:activated payload count = %d, want 1", len(payload))
			return
		}
		event, ok := payload[0].(sessionActivatedEvent)
		if !ok {
			t.Errorf("session:activated payload type = %T, want sessionActivatedEvent", payload[0])
			return
		}
		events <- event
	}
	api := &remoteAPI{app: app}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/new", nil)
	rec := httptest.NewRecorder()
	api.handleSessionNew(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	created := app.sessionByID(desktopEmbeddedStringField(got, "sessionId"))
	if created == nil {
		t.Fatalf("created session is unavailable: %+v", got)
	}
	defer app.closeTabRuntime(created)

	select {
	case event := <-events:
		t.Fatalf("external create emitted UI activation: %+v", event)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRemoteAPISessionNewBackgroundKeepsActiveTab(t *testing.T) {
	isolateDesktopUserDirs(t)

	root := t.TempDir()
	sessionDir := desktopSessionDir(root)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	activePath := filepath.Join(sessionDir, "active.jsonl")
	backgroundPath := filepath.Join(sessionDir, "background.jsonl")
	if err := os.WriteFile(backgroundPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setSessionTitle(sessionDir, backgroundPath, "worker"); err != nil {
		t.Fatal(err)
	}
	activeCtrl := &remoteStatusCtrlStub{path: activePath, status: control.RuntimeStatus{Mode: control.RuntimeModeIdle}}
	backgroundCtrl := &remoteStatusCtrlStub{path: backgroundPath, status: control.RuntimeStatus{Mode: control.RuntimeModeIdle}}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active": {
				ID: "active", Scope: "project", WorkspaceRoot: root,
				Ready: true, Ctrl: activeCtrl, SessionPath: activePath,
			},
			"background": {
				ID: "background", Scope: "project", WorkspaceRoot: root,
				Ready: true, Ctrl: backgroundCtrl, SessionPath: backgroundPath,
			},
		},
		activeTabID: "active",
	}
	api := &remoteAPI{app: app}

	payload, _ := json.Marshal(map[string]any{
		"background": true, "workspace": root, "sessionName": "worker",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/new", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	api.handleSessionNew(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	path, _ := got["path"].(string)
	if path == "" || sessionRuntimeKey(path) == sessionRuntimeKey(backgroundPath) {
		t.Fatalf("background path = %q, want a newly created session distinct from %q", path, backgroundPath)
	}
	if sessionID, _ := got["sessionId"].(string); sessionID == "" {
		t.Fatalf("new response missing sessionId: %s", rec.Body.String())
	} else if created := app.sessionByID(sessionID); created != nil {
		defer app.closeTabRuntime(created)
	}
	app.mu.RLock()
	activeID := app.activeTabID
	app.mu.RUnlock()
	if activeID != "active" {
		t.Fatalf("active tab changed to %q", activeID)
	}
}

func TestRemoteAPIStatusKeepsSubmittedSessionStartingUntilObservedActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.jsonl")
	ctrl := &remoteStatusCtrlStub{
		path:         path,
		approvalMode: control.ToolApprovalYolo,
		status:       control.RuntimeStatus{Mode: control.RuntimeModeIdle},
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"tab": {
				ID:            "tab",
				Scope:         "project",
				WorkspaceRoot: filepath.Dir(path),
				Ready:         true,
				Ctrl:          ctrl,
				SessionPath:   path,
			},
		},
		activeTabID: "tab",
	}
	api := &remoteAPI{app: app}
	api.markSubmitted(path, time.Now())

	got := api.activeSessionResponse("ok")
	if got["running"] == true || got["starting"] != true || got["submitted"] != true || got["mode"] != "starting" {
		t.Fatalf("starting status = %+v, want submitted/start but not running", got)
	}
	if got["foregroundActive"] != true || got["activeRuntimeWork"] != true ||
		got["pendingPrompt"] != false || got["backgroundOnly"] != false || got["cancelRequested"] != false {
		t.Fatalf("starting status has incomplete runtime state: %+v", got)
	}

	ctrl.status = control.RuntimeStatus{
		Mode:              control.RuntimeModeForeground,
		Running:           true,
		Cancellable:       true,
		ForegroundActive:  true,
		ActiveRuntimeWork: true,
		RunningWork:       true,
	}
	got = api.activeSessionResponse("ok")
	if got["running"] != true || got["submitted"] != true {
		t.Fatalf("observed active status = %+v, want running/submitted", got)
	}
	if _, ok := got["starting"]; ok {
		t.Fatalf("observed active status should not remain starting: %+v", got)
	}

	ctrl.status = control.RuntimeStatus{Mode: control.RuntimeModeIdle}
	got = api.activeSessionResponse("ok")
	if got["running"] == true || got["submitted"] == true || got["starting"] == true {
		t.Fatalf("completed status = %+v, want idle without submitted/start", got)
	}
}

func TestRemoteAPIStatusClearsSubmittedStartingAfterSessionFileUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.jsonl")
	ctrl := &remoteStatusCtrlStub{
		path:   path,
		status: control.RuntimeStatus{Mode: control.RuntimeModeIdle},
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"tab": {
				ID:            "tab",
				Scope:         "project",
				WorkspaceRoot: filepath.Dir(path),
				Ready:         true,
				Ctrl:          ctrl,
				SessionPath:   path,
			},
		},
		activeTabID: "tab",
	}
	api := &remoteAPI{app: app}
	submittedAt := time.Now()
	api.markSubmitted(path, submittedAt)
	for time.Now().Sub(submittedAt) <= time.Millisecond {
		time.Sleep(time.Millisecond)
	}
	if err := os.WriteFile(path, []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := api.activeSessionResponse("ok")
	if got["running"] == true || got["submitted"] == true || got["starting"] == true {
		t.Fatalf("updated-file status = %+v, want idle without submitted/start", got)
	}
}

func TestRemoteAPISessionNewDoesNotReuseNamedSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	root := t.TempDir()
	dir := t.TempDir()
	sessionPath := writeTopicSessionWithPrompt(t, dir, "worker.jsonl", "topic_worker", "Worker", root, "existing worker prompt", time.Now())
	if err := setSessionTitle(dir, sessionPath, "codex-worker"); err != nil {
		t.Fatalf("set session title: %v", err)
	}
	ctrl := control.New(control.Options{Label: "ready", WorkspaceRoot: root, SessionDir: dir, SessionPath: sessionPath})
	defer ctrl.Close()
	tab := &WorkspaceTab{
		ID:            "tab",
		Scope:         "project",
		WorkspaceRoot: root,
		Ready:         true,
		Ctrl:          ctrl,
		SessionPath:   sessionPath,
		disabledMCP:   map[string]ServerView{},
	}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	api := &remoteAPI{app: app}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/new", bytes.NewBufferString(`{"sessionName":"codex-worker"}`))
	rec := httptest.NewRecorder()
	api.handleSessionNew(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["created"] != true {
		t.Fatalf("created = %v, want true", got["created"])
	}
	if responsePath, _ := got["path"].(string); responsePath == "" || filepath.Clean(responsePath) == filepath.Clean(sessionPath) {
		t.Fatalf("response path = %q, want a new session distinct from %q", responsePath, sessionPath)
	}
	if got["sessionName"] != "codex-worker" {
		t.Fatalf("sessionName = %v, want codex-worker", got["sessionName"])
	}
	created := app.sessionByID(desktopEmbeddedStringField(got, "sessionId"))
	if created == nil {
		t.Fatalf("created session is unavailable: %+v", got)
	}
	defer app.closeTabRuntime(created)
	meta, _, err := agent.LoadBranchMeta(sessionPath)
	if err != nil {
		t.Fatalf("LoadBranchMeta reused session: %v", err)
	}
	if meta.SessionSource == "cli" {
		t.Fatal("existing same-name desktop session was reclassified")
	}
	if source := app.tabMeta(created, false).SessionSource; source != "cli" {
		t.Fatalf("new session source = %q, want cli", source)
	}
}

func TestRemoteAPISessionNewCreatesMissingNamedSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	root := t.TempDir()
	dir := t.TempDir()
	oldPath := agent.NewSessionPath(dir, "ready")
	sess := &agent.Session{}
	sess.Replace([]provider.Message{{Role: provider.RoleSystem, Content: "sys"}})
	ag := agent.New(stubProvider{}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Executor:      ag,
		Label:         "ready",
		WorkspaceRoot: root,
		SessionDir:    dir,
		SessionPath:   oldPath,
		Sink:          event.Discard,
	})
	defer ctrl.Close()

	tab := &WorkspaceTab{
		ID:            "tab",
		Scope:         "project",
		WorkspaceRoot: root,
		Ready:         true,
		Ctrl:          ctrl,
		SessionPath:   oldPath,
		disabledMCP:   map[string]ServerView{},
	}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	api := &remoteAPI{app: app}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/new", bytes.NewBufferString(`{"sessionName":"codex-worker"}`))
	rec := httptest.NewRecorder()
	api.handleSessionNew(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	firstID := desktopEmbeddedStringField(got, "sessionId")
	created := app.sessionByID(firstID)
	if created == nil {
		t.Fatalf("created session is unavailable: %+v", got)
	}
	defer app.closeTabRuntime(created)
	newPath := created.currentSessionPath()
	if newPath == "" || filepath.Clean(newPath) == filepath.Clean(oldPath) {
		t.Fatalf("session path = %q, want fresh path distinct from %q", newPath, oldPath)
	}
	if title := loadSessionTitles(filepath.Dir(newPath))[filepath.Base(newPath)]; title != "codex-worker" {
		t.Fatalf("new session title = %q, want codex-worker", title)
	}
	if got := created.TopicTitle; got != "codex-worker" {
		t.Fatalf("new session topic title = %q, want codex-worker", got)
	}
	if got := loadTopicTitle("", created.TopicID); got != "codex-worker" {
		t.Fatalf("stored topic title = %q, want codex-worker", got)
	}
	if got["created"] != true {
		t.Fatalf("created = %v, want true", got["created"])
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/session/new", bytes.NewBufferString(`{"sessionName":"codex-worker"}`))
	rec = httptest.NewRecorder()
	api.handleSessionNew(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", rec.Code, rec.Body.String())
	}
	second := map[string]any{}
	if err := json.NewDecoder(rec.Body).Decode(&second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	secondID := desktopEmbeddedStringField(second, "sessionId")
	secondTab := app.sessionByID(secondID)
	if secondTab == nil {
		t.Fatalf("second session is unavailable: %+v", second)
	}
	defer app.closeTabRuntime(secondTab)
	if second["created"] != true || secondID == "" || secondID == firstID {
		t.Fatalf("second create did not produce a distinct SessionID: first=%q second=%+v", firstID, second)
	}
	if sessionRuntimeKey(secondTab.currentSessionPath()) == sessionRuntimeKey(newPath) {
		t.Fatalf("second create reused path %q", newPath)
	}
	if ctrl.SessionPath() != oldPath {
		t.Fatalf("external creates changed active UI session path to %q", ctrl.SessionPath())
	}
}

func TestRemoteAPISessionSubmitUsesCurrentBlankNamedSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	root := t.TempDir()
	dir := t.TempDir()
	oldPath := agent.NewSessionPath(dir, "ready")
	sess := &agent.Session{}
	sess.Replace([]provider.Message{{Role: provider.RoleSystem, Content: "sys"}})
	ag := agent.New(remoteReplyProvider{}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Runner:        ag,
		Executor:      ag,
		SystemPrompt:  "sys",
		Label:         "ready",
		WorkspaceRoot: root,
		SessionDir:    dir,
		SessionPath:   oldPath,
		Sink:          event.Discard,
	})
	defer ctrl.Close()

	tab := &WorkspaceTab{
		ID:            "tab",
		SessionID:     "session-worker",
		Scope:         "project",
		WorkspaceRoot: root,
		Ready:         true,
		Ctrl:          ctrl,
		SessionPath:   oldPath,
		disabledMCP:   map[string]ServerView{},
	}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	api := &remoteAPI{app: app}
	app.trackSession(tab)
	if err := setSessionTitle(dir, oldPath, "codex-worker"); err != nil {
		t.Fatal(err)
	}
	tab.TopicTitle = "codex-worker"
	if err := app.setTabSessionSource(tab.ID, "cli"); err != nil {
		t.Fatal(err)
	}
	sessionPath := oldPath

	body := map[string]string{
		"sessionId":        tab.SessionID,
		"prompt":           "run the worker packet",
		"toolApprovalMode": "yolo",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/submit", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	api.handleSessionSubmit(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	waitForControllerIdle(t, ctrl)
	waitForFile(t, sessionPath, "run the worker packet")
	status := api.sessionResponseForTab(tab, "ok")
	if status["foregroundActive"] != false || status["activeRuntimeWork"] != false {
		t.Fatalf("completed structured status = %+v", status)
	}
	if report, _ := status["report"].(string); report != "ack" {
		t.Fatalf("completion report = %q, want ack; status=%+v", report, status)
	}
	if meta := app.tabMeta(tab, true); meta.SessionSource != "cli" || meta.NeedsAttention {
		t.Fatalf("remote submit changed CLI ownership: %+v", meta)
	}
	if got := loadSessionTitles(dir)[filepath.Base(sessionPath)]; got != "codex-worker" {
		t.Fatalf("session title = %q, want codex-worker", got)
	}
}

func TestRemoteAPISessionSubmitReportsRunningImmediately(t *testing.T) {
	isolateDesktopUserDirs(t)

	root := t.TempDir()
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "immediate-running")
	started := make(chan struct{})
	release := make(chan struct{})
	sess := &agent.Session{}
	sess.Replace([]provider.Message{{Role: provider.RoleSystem, Content: "sys"}})
	ag := agent.New(remoteBlockingProvider{started: started, release: release}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Runner:        ag,
		Executor:      ag,
		SystemPrompt:  "sys",
		Label:         "immediate-running",
		WorkspaceRoot: root,
		SessionDir:    dir,
		SessionPath:   path,
		Sink:          event.Discard,
	})
	defer ctrl.Close()

	tab := &WorkspaceTab{
		ID:            "tab",
		SessionID:     "session-immediate",
		Scope:         "project",
		WorkspaceRoot: root,
		Ready:         true,
		Ctrl:          ctrl,
		SessionPath:   path,
		disabledMCP:   map[string]ServerView{},
	}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	api := &remoteAPI{app: app}
	app.trackSession(tab)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/submit", bytes.NewBufferString(`{"sessionId":"session-immediate","prompt":"run now"}`))
	rec := httptest.NewRecorder()
	api.handleSessionSubmit(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var submitted map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&submitted); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if submitted["running"] != true || submitted["mode"] != string(control.RuntimeModeForeground) {
		t.Fatalf("immediate submit response = %+v, want foreground running", submitted)
	}
	status := api.sessionResponseForTab(tab, "ok")
	if status["running"] != true || status["mode"] != string(control.RuntimeModeForeground) {
		t.Fatalf("immediate status response = %+v, want foreground running", status)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	close(release)
	waitForControllerIdle(t, ctrl)
}

func TestRemoteAPIStatusExposesAskAndAnswerResolvesIt(t *testing.T) {
	root := t.TempDir()
	askEvents := make(chan event.Ask, 1)
	ctrl := control.New(control.Options{
		Label:         "ready",
		WorkspaceRoot: root,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.AskRequest {
				askEvents <- e.Ask
			}
		}),
	})
	defer ctrl.Close()
	tab := &WorkspaceTab{ID: "tab", SessionID: "session-ask", Scope: "project", WorkspaceRoot: root, Ready: true, Ctrl: ctrl}
	app := &App{tabs: map[string]*WorkspaceTab{"tab": tab}, activeTabID: "tab"}
	api := &remoteAPI{app: app}
	app.trackSession(tab)

	type askResult struct {
		answers []event.AskAnswer
		err     error
	}
	done := make(chan askResult, 1)
	go func() {
		answers, err := ctrl.Ask(context.Background(), []event.AskQuestion{{
			ID:     "q1",
			Header: "Approach",
			Prompt: "Which implementation should be used?",
			Options: []event.AskOption{
				{Label: "Use existing path", Description: "Smallest compatible change"},
				{Label: "Add abstraction", Description: "Broader refactor"},
			},
		}})
		done <- askResult{answers: answers, err: err}
	}()
	ask := <-askEvents

	status := api.sessionResponseForTab(tab, "ok")
	if status["pendingPrompt"] != true || status["mode"] != string(control.RuntimeModeWaitingUser) {
		t.Fatalf("status = %+v, want waiting pending prompt", status)
	}
	pending, ok := status["pendingInteraction"].(map[string]any)
	if !ok {
		t.Fatalf("pendingInteraction = %#v, want object", status["pendingInteraction"])
	}
	if pending["kind"] != control.PendingInteractionAsk || pending["id"] != ask.ID {
		t.Fatalf("pendingInteraction = %+v, want ask %q", pending, ask.ID)
	}
	questions, ok := pending["questions"].([]map[string]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("questions = %#v, want one question", pending["questions"])
	}
	if questions[0]["id"] != "q1" || questions[0]["question"] != "Which implementation should be used?" {
		t.Fatalf("question = %+v", questions[0])
	}
	options, ok := questions[0]["options"].([]map[string]string)
	if !ok || len(options) != 2 || options[0]["label"] != "Use existing path" {
		t.Fatalf("options = %#v", questions[0]["options"])
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/v1/session/answer", bytes.NewBufferString(`{"sessionId":"session-ask","id":"stale","answers":[{"questionId":"q1","selected":["Use existing path"]}]}`))
	badRec := httptest.NewRecorder()
	api.handleSessionAnswer(badRec, badReq)
	if badRec.Code != http.StatusBadRequest || !strings.Contains(badRec.Body.String(), "pending ask changed") {
		t.Fatalf("stale answer status = %d, body = %s", badRec.Code, badRec.Body.String())
	}

	payload, err := json.Marshal(map[string]any{
		"sessionId": "session-ask",
		"id":        ask.ID,
		"answers": []QuestionAnswer{{
			QuestionID: "q1",
			Selected:   []string{"Use existing path"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/answer", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	api.handleSessionAnswer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("answer status = %d, body = %s", rec.Code, rec.Body.String())
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Ask: %v", result.err)
		}
		if len(result.answers) != 1 || result.answers[0].QuestionID != "q1" || len(result.answers[0].Selected) != 1 || result.answers[0].Selected[0] != "Use existing path" {
			t.Fatalf("answers = %+v", result.answers)
		}
	case <-time.After(time.Second):
		t.Fatal("answer did not release pending ask")
	}
	if _, ok := ctrl.PendingInteraction(); ok {
		t.Fatal("pending interaction should be cleared after answer")
	}
}

func TestDesktopEmbeddedParseAnswersGroupsMultiSelect(t *testing.T) {
	got, err := desktopEmbeddedParseAnswers([]string{"q1=First", "q1=Second", "q2=Only"})
	if err != nil {
		t.Fatalf("desktopEmbeddedParseAnswers: %v", err)
	}
	if len(got) != 2 || got[0].QuestionID != "q1" || len(got[0].Selected) != 2 || got[1].QuestionID != "q2" {
		t.Fatalf("answers = %+v", got)
	}
}

func waitForControllerIdle(t *testing.T, ctrl *control.Controller) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !ctrl.Running() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("controller did not become idle")
}

type remoteReplyProvider struct{}

func (remoteReplyProvider) Name() string { return "remote-reply" }

func (remoteReplyProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "ack"}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

type remoteBlockingProvider struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (remoteBlockingProvider) Name() string { return "remote-blocking" }

func (p remoteBlockingProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 2)
	go func() {
		defer close(ch)
		select {
		case p.started <- struct{}{}:
		case <-ctx.Done():
			return
		}
		select {
		case <-p.release:
			ch <- provider.Chunk{Type: provider.ChunkText, Text: "ack"}
			ch <- provider.Chunk{Type: provider.ChunkDone}
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// --- Background session tests ------------------------------------------------

func TestRemoteAPIStatusUsesExplicitSessionID(t *testing.T) {
	root := t.TempDir()
	activeCtrl := &remoteStatusCtrlStub{
		path:         filepath.Join(root, "active.jsonl"),
		approvalMode: control.ToolApprovalAsk,
		status:       control.RuntimeStatus{Mode: control.RuntimeModeIdle},
	}
	bgCtrl := &remoteStatusCtrlStub{
		path:         filepath.Join(root, "bg.jsonl"),
		approvalMode: control.ToolApprovalYolo,
		status: control.RuntimeStatus{
			Mode:              control.RuntimeModeForeground,
			Running:           true,
			ForegroundActive:  true,
			ActiveRuntimeWork: true,
		},
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active": {
				ID: "active", SessionID: "session-active", Scope: "project", WorkspaceRoot: root,
				Ready: true, Ctrl: activeCtrl,
				SessionPath: activeCtrl.path,
			},
			"bg": {
				ID: "bg", SessionID: "session-bg", Scope: "project", WorkspaceRoot: root,
				Ready: true, Ctrl: bgCtrl,
				SessionPath: bgCtrl.path,
			},
		},
		activeTabID: "active",
	}
	api := &remoteAPI{app: app}
	app.trackSession(app.tabs["active"])
	app.trackSession(app.tabs["bg"])
	req := httptest.NewRequest(http.MethodGet, "/api/v1/session/status?sessionId=session-bg", nil)
	rec := httptest.NewRecorder()
	api.handleSessionStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["running"] != true {
		t.Fatalf("bg running = %v, want true; body=%+v", got["running"], got)
	}
	if got["sessionId"] != "session-bg" {
		t.Fatalf("sessionId = %v, want session-bg", got["sessionId"])
	}
	if got["toolApprovalMode"] != control.ToolApprovalYolo {
		t.Fatalf("bg toolApprovalMode = %v, want yolo; body=%+v", got["toolApprovalMode"], got)
	}
}

func TestRemoteAPITargetStartingStatusHasStableSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "starting.jsonl")
	tab := &WorkspaceTab{
		ID:          "starting",
		SessionID:   "session-starting",
		Scope:       "project",
		SessionPath: path,
	}
	app := &App{tabs: map[string]*WorkspaceTab{"starting": tab}}
	api := &remoteAPI{app: app}
	app.trackSession(tab)
	api.markSubmitted(path, time.Now())

	got := api.sessionResponseForTab(tab, "ok")
	if got["running"] != false || got["pendingPrompt"] != false ||
		got["foregroundActive"] != true || got["backgroundOnly"] != false ||
		got["activeRuntimeWork"] != true || got["cancelRequested"] != false ||
		got["starting"] != true || got["mode"] != "starting" {
		t.Fatalf("starting status = %+v, want stable starting schema", got)
	}
}

func TestRemoteAPIStatusRequiresKnownSessionID(t *testing.T) {
	api := &remoteAPI{app: &App{tabs: map[string]*WorkspaceTab{}}}
	for _, target := range []struct {
		url  string
		code int
	}{
		{url: "/api/v1/session/status", code: http.StatusBadRequest},
		{url: "/api/v1/session/status?sessionId=missing", code: http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, target.url, nil)
		rec := httptest.NewRecorder()
		api.handleSessionStatus(rec, req)
		if rec.Code != target.code {
			t.Fatalf("%s status = %d, want %d; body=%s", target.url, rec.Code, target.code, rec.Body.String())
		}
	}
}

func TestRemoteAPISessionSubmitUsesExplicitSessionID(t *testing.T) {
	root := t.TempDir()

	// Active tab A.
	activeCtrl := &remoteStatusCtrlStub{
		path:   filepath.Join(root, "active.jsonl"),
		status: control.RuntimeStatus{Mode: control.RuntimeModeIdle},
	}
	// Background tab B.
	bgCtrl := &remoteStatusCtrlStub{
		path:   filepath.Join(root, "bg.jsonl"),
		status: control.RuntimeStatus{Mode: control.RuntimeModeIdle},
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active": {
				ID: "active", SessionID: "session-active", Scope: "project", WorkspaceRoot: root,
				Ready: true, Ctrl: activeCtrl,
				SessionPath: activeCtrl.path,
			},
			"bg": {
				ID: "bg", SessionID: "session-bg", Scope: "project", WorkspaceRoot: root,
				Ready: true, Ctrl: bgCtrl,
				SessionPath: bgCtrl.path,
			},
		},
		activeTabID: "active",
	}
	api := &remoteAPI{app: app}
	app.trackSession(app.tabs["active"])
	app.trackSession(app.tabs["bg"])

	submitBody := map[string]string{"prompt": "hello bg", "sessionId": "session-bg"}
	payload, _ := json.Marshal(submitBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/submit", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	api.handleSessionSubmit(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["sessionId"] != "session-bg" {
		t.Fatalf("response sessionId = %v, want session-bg", got["sessionId"])
	}

	// Active tab should still be "active".
	app.mu.RLock()
	stillActive := app.activeTabID
	app.mu.RUnlock()
	if stillActive != "active" {
		t.Fatalf("active tab changed to %q, want active", stillActive)
	}
}

func TestRemoteAPISubmitRejectsMissingSessionID(t *testing.T) {
	api := &remoteAPI{app: &App{tabs: map[string]*WorkspaceTab{}}}
	payload, _ := json.Marshal(map[string]string{"prompt": "hello"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/submit", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	api.handleSessionSubmit(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("submit status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRemoteAPIApproveUsesExplicitSessionID(t *testing.T) {
	root := t.TempDir()
	bgCtrl := &remoteStatusCtrlStub{
		path:         filepath.Join(root, "bg.jsonl"),
		approvalMode: control.ToolApprovalAsk,
		status:       control.RuntimeStatus{Mode: control.RuntimeModeIdle},
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"bg": {
				ID: "bg", SessionID: "session-bg", Scope: "project", WorkspaceRoot: root,
				Ready: true, Ctrl: bgCtrl,
				SessionPath: bgCtrl.path,
			},
		},
		activeTabID: "bg",
	}
	api := &remoteAPI{app: app}
	app.trackSession(app.tabs["bg"])

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/approve", bytes.NewBufferString(`{"sessionId":"session-bg","allow":true}`))
	rec := httptest.NewRecorder()
	api.handleSessionApprove(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for no pending interaction, got %d", rec.Code)
	}
}

func TestRemoteAPIAnswerUsesExplicitSessionID(t *testing.T) {
	root := t.TempDir()
	bgCtrl := &remoteStatusCtrlStub{
		path:   filepath.Join(root, "bg.jsonl"),
		status: control.RuntimeStatus{Mode: control.RuntimeModeIdle},
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"bg": {
				ID: "bg", SessionID: "session-bg", Scope: "project", WorkspaceRoot: root,
				Ready: true, Ctrl: bgCtrl,
				SessionPath: bgCtrl.path,
			},
		},
		activeTabID: "bg",
	}
	api := &remoteAPI{app: app}
	app.trackSession(app.tabs["bg"])

	req := httptest.NewRequest(http.MethodPost, "/api/v1/session/answer", bytes.NewBufferString(`{"sessionId":"session-bg","id":"ask-1","answers":[]}`))
	rec := httptest.NewRecorder()
	api.handleSessionAnswer(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for no pending interaction, got %d", rec.Code)
	}
}

type remoteStatusCtrlStub struct {
	control.SessionAPI
	path         string
	approvalMode string
	status       control.RuntimeStatus
}

func (s *remoteStatusCtrlStub) History() []provider.Message {
	return nil
}

func (s *remoteStatusCtrlStub) SessionPath() string {
	return s.path
}

func (s *remoteStatusCtrlStub) RuntimeStatus() control.RuntimeStatus {
	return s.status
}

func (s *remoteStatusCtrlStub) PendingInteraction() (control.PendingInteraction, bool) {
	return control.PendingInteraction{}, false
}

func (s *remoteStatusCtrlStub) PlanMode() bool {
	return false
}

func (s *remoteStatusCtrlStub) AutoApproveTools() bool {
	return false
}

func (s *remoteStatusCtrlStub) SubmitDisplay(display, raw string) {}

func (s *remoteStatusCtrlStub) Goal() string {
	return ""
}

func (s *remoteStatusCtrlStub) GoalStatus() string {
	return control.GoalStatusStopped
}

func (s *remoteStatusCtrlStub) ToolApprovalMode() string {
	if s.approvalMode != "" {
		return s.approvalMode
	}
	return control.ToolApprovalAsk
}

func (s *remoteStatusCtrlStub) AutoResearchSummary() (*autoresearch.Summary, bool) {
	return nil, false
}
