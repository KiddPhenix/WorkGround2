package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"workground2/internal/config"
	"workground2/internal/control"
)

// remoteAPI is a minimal HTTP server on 127.0.0.1 that lets the CLI send
// commands to a running Desktop instance: open a session, create a new one, or
// focus the window. The port is written to ~/.WorkGround2/desktop-port so the
// CLI can discover it.
type remoteAPI struct {
	app    *App
	srv    *http.Server
	port   int
	closed chan struct{}

	mu        sync.Mutex
	submitted map[string]remoteSubmittedSession

	// remoteTargetKey stores the session runtime key of the last externally
	// targeted session. Status, submit, approve, and answer operations read
	// from this target instead of implicitly reading the active tab.
	remoteTargetKey string
}

type remoteSubmittedSession struct {
	submittedAt    time.Time
	observedActive bool
}

const (
	remoteAPIPortFile               = "desktop-port"
	remoteWorkspaceReadyTimeout     = 30 * time.Second
	remoteWorkspaceReadyPoll        = 50 * time.Millisecond
	remoteWorkspaceReadyWriteMargin = 5 * time.Second
	remoteSubmitStartingTTL         = 2 * time.Minute
)

// startRemoteAPI picks a random free port on 127.0.0.1, starts an HTTP server,
// and writes the port file. Safe to call from any goroutine.
func (a *App) startRemoteAPI() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("[remote-api] listen: %v", err)
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port

	api := &remoteAPI{
		app:    a,
		port:   port,
		closed: make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/session/open", api.handleSessionOpen)
	mux.HandleFunc("/api/v1/session/new", api.handleSessionNew)
	mux.HandleFunc("/api/v1/session/submit", api.handleSessionSubmit)
	mux.HandleFunc("/api/v1/session/status", api.handleSessionStatus)
	mux.HandleFunc("/api/v1/session/approve", api.handleSessionApprove)
	mux.HandleFunc("/api/v1/session/answer", api.handleSessionAnswer)
	mux.HandleFunc("/api/v1/workspaces", api.handleWorkspaces)
	mux.HandleFunc("/api/v1/workspace/switch", api.handleWorkspaceSwitch)
	mux.HandleFunc("/api/v1/window/focus", api.handleWindowFocus)
	mux.HandleFunc("/api/v1/status", api.handleStatus)

	api.srv = &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: remoteWorkspaceReadyTimeout + remoteWorkspaceReadyWriteMargin,
		IdleTimeout:  30 * time.Second,
	}

	// Write port file.
	portPath := filepath.Join(config.MemoryUserDir(), remoteAPIPortFile)
	if err := os.WriteFile(portPath, []byte(strconv.Itoa(port)+"\n"), 0o644); err != nil {
		log.Printf("[remote-api] write port file: %v", err)
	}

	a.remoteAPI = api
	log.Printf("[remote-api] listening on 127.0.0.1:%d", port)

	// Serve until the app context is cancelled.
	go func() {
		<-a.ctx.Done()
		api.shutdown()
	}()

	if err := api.srv.Serve(ln); err != http.ErrServerClosed {
		log.Printf("[remote-api] serve: %v", err)
	}
}

func (api *remoteAPI) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = api.srv.Shutdown(ctx)

	// Remove port file.
	portPath := filepath.Join(config.MemoryUserDir(), remoteAPIPortFile)
	_ = os.Remove(portPath)

	close(api.closed)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (api *remoteAPI) handleSessionOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		http.Error(w, "invalid request: path is required", http.StatusBadRequest)
		return
	}

	_, err := api.app.ResumeSession(body.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	api.setRemoteTarget(sessionRuntimeKey(api.app.CurrentSessionPath()))
	api.app.emitSessionActivated("remote-open")
	api.writeSessionResponse(w, "ok")
}

func (api *remoteAPI) handleSessionNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Background       bool   `json:"background,omitempty"`
		Workspace        string `json:"workspace,omitempty"`
		ToolApprovalMode string `json:"toolApprovalMode,omitempty"`
		SessionName      string `json:"sessionName,omitempty"`
	}
	if err := decodeRemoteOptionalJSON(r, &body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	workspace := strings.TrimSpace(body.Workspace)
	sessionName := strings.TrimSpace(body.SessionName)

	if body.Background || workspace != "" {
		// The desktop CLI always requests background creation. If it omits a
		// workspace, inherit the active tab's scope without replacing that tab.
		scope := "project"
		if workspace == "" {
			api.app.mu.RLock()
			active := api.app.activeTabLocked()
			if active == nil || active.Scope != "project" {
				scope = "global"
			} else {
				workspace = active.WorkspaceRoot
			}
			api.app.mu.RUnlock()
		}
		tab, sessionPath, created, err := api.app.newBackgroundSession(scope, workspace, sessionName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if body.ToolApprovalMode != "" {
			api.app.SetToolApprovalModeForTab(tab.ID, body.ToolApprovalMode)
		}
		api.setRemoteTarget(sessionRuntimeKey(sessionPath))
		if err := api.waitForTabReady(r.Context(), tab.ID, remoteWorkspaceReadyTimeout); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		out := api.sessionResponseForTab(tab, "ok")
		if sessionName != "" {
			out["sessionName"] = sessionName
			out["created"] = created
		}
		api.writeJSON(w, out)
		return
	}

	// Keep the HTTP endpoint's legacy foreground behavior for callers that do
	// not opt into background creation.
	created := true
	var err error
	if sessionName == "" {
		err = api.app.forceNewSession()
	} else {
		created, err = api.app.openOrCreateNamedSession(sessionName)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if created {
		if err := api.app.setActiveSessionSource("cli"); err != nil {
			http.Error(w, fmt.Sprintf("stamp CLI session source: %v", err), http.StatusInternalServerError)
			return
		}
	}
	api.app.mu.RLock()
	active := api.app.activeTabLocked()
	workspaceRoot := ""
	if active != nil {
		workspaceRoot = active.WorkspaceRoot
	}
	api.app.mu.RUnlock()
	if err := api.waitForActiveWorkspaceReady(r.Context(), workspaceRoot, remoteWorkspaceReadyTimeout); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if body.ToolApprovalMode != "" {
		api.app.SetToolApprovalMode(body.ToolApprovalMode)
	}
	api.app.emitSessionActivated("remote-new")
	out := api.activeSessionResponse("ok")
	if sessionName != "" {
		out["sessionName"] = sessionName
		out["created"] = created
	}
	api.writeJSON(w, out)
}

func (api *remoteAPI) handleWindowFocus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := api.app.ctx
	if ctx == nil {
		http.Error(w, "app not ready", http.StatusServiceUnavailable)
		return
	}
	runtime.WindowShow(ctx)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (api *remoteAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "running",
		"port":   api.port,
	})
}

func decodeRemoteOptionalJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(r.Body).Decode(out)
	if err == io.EOF {
		return nil
	}
	return err
}

func (api *remoteAPI) setRemoteTarget(key string) {
	api.mu.Lock()
	api.remoteTargetKey = key
	api.mu.Unlock()
}

func (api *remoteAPI) getRemoteTargetKey() string {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.remoteTargetKey
}

// remoteTargetTab returns the tab that matches the stored remote target key,
// or nil when no remote target is set or the tab is no longer available.
func (api *remoteAPI) remoteTargetTab() *WorkspaceTab {
	key := api.getRemoteTargetKey()
	if key == "" {
		return nil
	}
	return api.app.findTabBySessionRuntimeKey(key)
}

func (api *remoteAPI) writeSessionResponse(w http.ResponseWriter, status string) {
	api.writeJSON(w, api.targetSessionResponse(status))
}

// targetSessionResponse returns status for the remote target tab when one is
// set; otherwise it falls back to the active tab (backward compatibility).
func (api *remoteAPI) targetSessionResponse(status string) map[string]any {
	if tab := api.remoteTargetTab(); tab != nil {
		return api.sessionResponseForTab(tab, status)
	}
	return api.activeSessionResponse(status)
}

func newSessionResponse(status, path string) map[string]any {
	return map[string]any{
		"status":            status,
		"path":              path,
		"running":           false,
		"pendingPrompt":     false,
		"mode":              string(control.RuntimeModeIdle),
		"foregroundActive":  false,
		"backgroundOnly":    false,
		"activeRuntimeWork": false,
		"cancelRequested":   false,
	}
}

// sessionResponseForTab builds a status JSON response for a specific tab,
// mirroring the shape of activeSessionResponse.
func (api *remoteAPI) sessionResponseForTab(tab *WorkspaceTab, status string) map[string]any {
	path := tab.currentSessionPath()
	out := newSessionResponse(status, path)
	if path != "" {
		out["path"] = path
	}
	if tab.Ctrl != nil {
		rs := tab.Ctrl.RuntimeStatus()
		out["running"] = rs.ActiveRuntimeWork || rs.ForegroundActive || rs.PendingPrompt
		out["pendingPrompt"] = rs.PendingPrompt
		if rs.Mode != "" {
			out["mode"] = string(rs.Mode)
		}
		out["foregroundActive"] = rs.ForegroundActive
		out["backgroundOnly"] = rs.BackgroundOnly
		out["activeRuntimeWork"] = rs.ActiveRuntimeWork
		out["cancelRequested"] = rs.CancelRequested
		out["toolApprovalMode"] = currentTabToolApprovalMode(tab)
	}
	api.applyPendingInteractionForTab(tab, out)
	api.applySubmittedState(out, path, tabHasActiveRuntimeWork(tab))
	_, starting := out["starting"]
	if !starting && !tabHasActiveRuntimeWork(tab) && !tabHasPendingPrompt(tab) {
		if report := api.app.lastAssistantReport(tab.ID, 2000); report != "" {
			out["report"] = report
		}
	}
	return out
}

func tabHasActiveRuntimeWork(tab *WorkspaceTab) bool {
	if tab == nil || tab.Ctrl == nil {
		return false
	}
	rs := tab.Ctrl.RuntimeStatus()
	return rs.ActiveRuntimeWork || rs.ForegroundActive || rs.PendingPrompt
}

func tabHasPendingPrompt(tab *WorkspaceTab) bool {
	if tab == nil || tab.Ctrl == nil {
		return false
	}
	return tab.Ctrl.RuntimeStatus().PendingPrompt
}

func (api *remoteAPI) applyPendingInteractionForTab(tab *WorkspaceTab, out map[string]any) {
	if tab == nil {
		return
	}
	pending, ok := api.app.pendingInteractionForTab(tab.ID)
	if !ok {
		return
	}
	switch pending.Kind {
	case control.PendingInteractionApproval:
		out["pendingInteraction"] = map[string]any{
			"kind":    pending.Kind,
			"id":      pending.Approval.ID,
			"tool":    pending.Approval.Tool,
			"subject": pending.Approval.Subject,
			"reason":  pending.Approval.Reason,
		}
	case control.PendingInteractionAsk:
		questions := make([]map[string]any, 0, len(pending.Ask.Questions))
		for _, question := range pending.Ask.Questions {
			options := make([]map[string]string, 0, len(question.Options))
			for _, option := range question.Options {
				options = append(options, map[string]string{
					"label":       option.Label,
					"description": option.Description,
				})
			}
			questions = append(questions, map[string]any{
				"id":          question.ID,
				"header":      question.Header,
				"question":    question.Prompt,
				"options":     options,
				"multiSelect": question.Multi,
			})
		}
		out["pendingInteraction"] = map[string]any{
			"kind":      pending.Kind,
			"id":        pending.Ask.ID,
			"questions": questions,
		}
	}
}

func (api *remoteAPI) writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(value)
}

func (api *remoteAPI) activeSessionResponse(status string) map[string]any {
	path := api.app.CurrentSessionPath()
	out := newSessionResponse(status, path)
	for _, tab := range api.app.ListTabs() {
		if !tab.Active {
			continue
		}
		if tab.SessionPath != "" {
			path = tab.SessionPath
			out["path"] = path
		}
		out["running"] = tab.Running
		out["pendingPrompt"] = tab.PendingPrompt
		if tab.RuntimeMode != "" {
			out["mode"] = tab.RuntimeMode
		}
		out["foregroundActive"] = tab.ForegroundActive
		out["backgroundOnly"] = tab.BackgroundOnly
		out["activeRuntimeWork"] = tab.ActiveRuntimeWork
		out["cancelRequested"] = tab.CancelRequested
		out["toolApprovalMode"] = tab.ToolApprovalMode
		api.applyPendingInteraction(out)
		api.applySubmittedState(out, path, tab.ActiveRuntimeWork || tab.ForegroundActive || tab.PendingPrompt || tab.Running)
		_, starting := out["starting"]
		if !starting && !tab.ForegroundActive && !tab.PendingPrompt {
			if report := api.app.lastAssistantReport(tab.ID, 2000); report != "" {
				out["report"] = report
			}
		}
		return out
	}
	api.applySubmittedState(out, path, false)
	return out
}

func (a *App) lastAssistantReport(tabID string, limit int) string {
	a.mu.RLock()
	tab := a.tabByIDLocked(tabID)
	var ctrl control.SessionAPI
	if tab != nil {
		ctrl = tab.Ctrl
	}
	a.mu.RUnlock()
	if ctrl == nil {
		return ""
	}
	messages := ctrl.History()
	for i := len(messages) - 1; i >= 0; i-- {
		if string(messages[i].Role) != "assistant" {
			continue
		}
		text := strings.TrimSpace(messages[i].Content)
		runes := []rune(text)
		if limit > 0 && len(runes) > limit {
			text = string(runes[:limit]) + "..."
		}
		return text
	}
	return ""
}

func (api *remoteAPI) applyPendingInteraction(out map[string]any) {
	pending, ok := api.app.pendingInteraction()
	if !ok {
		return
	}
	switch pending.Kind {
	case control.PendingInteractionApproval:
		out["pendingInteraction"] = map[string]any{
			"kind":    pending.Kind,
			"id":      pending.Approval.ID,
			"tool":    pending.Approval.Tool,
			"subject": pending.Approval.Subject,
			"reason":  pending.Approval.Reason,
		}
	case control.PendingInteractionAsk:
		questions := make([]map[string]any, 0, len(pending.Ask.Questions))
		for _, question := range pending.Ask.Questions {
			options := make([]map[string]string, 0, len(question.Options))
			for _, option := range question.Options {
				options = append(options, map[string]string{
					"label":       option.Label,
					"description": option.Description,
				})
			}
			questions = append(questions, map[string]any{
				"id":          question.ID,
				"header":      question.Header,
				"question":    question.Prompt,
				"options":     options,
				"multiSelect": question.Multi,
			})
		}
		out["pendingInteraction"] = map[string]any{
			"kind":      pending.Kind,
			"id":        pending.Ask.ID,
			"questions": questions,
		}
	}
}

func (api *remoteAPI) markSubmitted(path string, submittedAt time.Time) {
	key := sessionRuntimeKey(path)
	if key == "" {
		return
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.submitted == nil {
		api.submitted = map[string]remoteSubmittedSession{}
	}
	api.submitted[key] = remoteSubmittedSession{submittedAt: submittedAt}
}

func (api *remoteAPI) applySubmittedState(out map[string]any, path string, runtimeActive bool) {
	starting, submitted := api.submittedState(path, runtimeActive)
	if submitted {
		out["submitted"] = true
	}
	if !starting {
		return
	}
	// Accepted/submitted is observable, but it is deliberately outside the
	// user-facing Running set until the controller reports active runtime work.
	out["running"] = false
	out["pendingPrompt"] = false
	out["foregroundActive"] = true
	out["backgroundOnly"] = false
	out["activeRuntimeWork"] = true
	out["cancelRequested"] = false
	out["starting"] = true
	out["mode"] = "starting"
}

func (api *remoteAPI) submittedState(path string, runtimeActive bool) (starting, submitted bool) {
	key := sessionRuntimeKey(path)
	if key == "" {
		return false, false
	}
	now := time.Now()
	api.mu.Lock()
	defer api.mu.Unlock()
	state, ok := api.submitted[key]
	if !ok {
		return false, false
	}
	if runtimeActive {
		state.observedActive = true
		api.submitted[key] = state
		return false, true
	}
	if state.observedActive || remoteSessionFileChangedSince(path, state.submittedAt) || now.Sub(state.submittedAt) > remoteSubmitStartingTTL {
		delete(api.submitted, key)
		return false, false
	}
	return true, true
}

func remoteSessionFileChangedSince(path string, since time.Time) bool {
	if strings.TrimSpace(path) == "" || since.IsZero() {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.ModTime().Before(since)
}

func (api *remoteAPI) handleSessionSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Prompt           string `json:"prompt"`
		Session          string `json:"session,omitempty"`          // optional: target a specific session
		ToolApprovalMode string `json:"toolApprovalMode,omitempty"` // optional: ask, auto, yolo
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		http.Error(w, "invalid request: prompt is required", http.StatusBadRequest)
		return
	}

	// When a specific session path is given, try to route to it without
	// activating the tab. First check if the session is already loaded in
	// any tab; if not, fall back to the old behaviour (ResumeSession on
	// the active tab) for backward compatibility.
	var targetTab *WorkspaceTab
	if body.Session != "" {
		targetKey := sessionRuntimeKey(body.Session)
		if tab := api.app.findTabBySessionRuntimeKey(targetKey); tab != nil {
			// Session is already loaded — route directly without activating.
			targetTab = tab
		} else if tab, err := api.app.ensureTabForSessionPath(body.Session); err == nil {
			// Session can be loaded into a background tab.
			targetTab = tab
		}
	} else if targetKey := api.getRemoteTargetKey(); targetKey != "" {
		// Use the last remote target if set.
		if tab := api.remoteTargetTab(); tab != nil {
			targetTab = tab
		}
	}

	if targetTab != nil {
		// Route directly to the target tab's controller without affecting
		// the active tab.
		if body.ToolApprovalMode != "" {
			api.app.SetToolApprovalModeForTab(targetTab.ID, body.ToolApprovalMode)
		}
		submittedAt := time.Now()
		if err := api.app.submitToTab(targetTab.ID, body.Prompt, false); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		api.setRemoteTarget(sessionRuntimeKey(targetTab.currentSessionPath()))
		api.markSubmitted(targetTab.currentSessionPath(), submittedAt)
		api.writeJSON(w, api.sessionResponseForTab(targetTab, "ok"))
		return
	}

	// No explicit target — fall back to the active tab (backward compatible).
	sessionActivated := false
	if body.Session != "" && sessionRuntimeKey(api.app.CurrentSessionPath()) != sessionRuntimeKey(body.Session) {
		if _, err := api.app.ResumeSession(body.Session); err != nil {
			http.Error(w, "open session: "+err.Error(), http.StatusBadRequest)
			return
		}
		sessionActivated = true
	}

	if body.ToolApprovalMode != "" {
		api.app.SetToolApprovalMode(body.ToolApprovalMode)
	}
	if sessionActivated {
		api.app.emitSessionActivated("remote-submit")
	}
	submittedAt := time.Now()
	if err := api.app.submitToTab("", body.Prompt, false); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	api.markSubmitted(api.app.CurrentSessionPath(), submittedAt)
	api.writeSessionResponse(w, "ok")
}

func (api *remoteAPI) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(api.targetSessionResponse("ok"))
}

func (api *remoteAPI) handleSessionApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID    string `json:"id,omitempty"`
		Allow bool   `json:"allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := api.app.approvePendingIDForTab(api.remoteTargetTabID(), body.ID, body.Allow); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (api *remoteAPI) remoteTargetTabID() string {
	if tab := api.remoteTargetTab(); tab != nil {
		return tab.ID
	}
	return ""
}

func (api *remoteAPI) handleSessionAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID      string           `json:"id"`
		Answers []QuestionAnswer `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := api.app.answerPendingQuestionForTab(api.remoteTargetTabID(), body.ID, body.Answers); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	api.writeJSON(w, map[string]string{"status": "ok"})
}

func (api *remoteAPI) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(api.app.ListWorkspaces())
}

func (api *remoteAPI) handleWorkspaceSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Dir == "" {
		http.Error(w, "invalid request: dir is required", http.StatusBadRequest)
		return
	}
	root, err := api.app.SwitchWorkspace(body.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := api.waitForActiveWorkspaceReady(r.Context(), root, remoteWorkspaceReadyTimeout); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (api *remoteAPI) waitForActiveWorkspaceReady(ctx context.Context, workspaceRoot string, timeout time.Duration) error {
	return api.waitUntilReady(ctx, timeout, func() (bool, error) {
		return api.activeWorkspaceReady(workspaceRoot)
	})
}

func (api *remoteAPI) waitForTabReady(ctx context.Context, tabID string, timeout time.Duration) error {
	return api.waitUntilReady(ctx, timeout, func() (bool, error) {
		if api == nil || api.app == nil {
			return false, fmt.Errorf("app not ready")
		}
		api.app.mu.RLock()
		tab := api.app.tabs[tabID]
		if tab == nil {
			api.app.mu.RUnlock()
			return false, fmt.Errorf("session tab is no longer available")
		}
		startupErr := strings.TrimSpace(tab.StartupErr)
		ready := tab.Ready && tab.Ctrl != nil
		api.app.mu.RUnlock()
		if startupErr != "" {
			return false, fmt.Errorf("workspace failed to start: %s", startupErr)
		}
		return ready, nil
	})
}

func (api *remoteAPI) waitUntilReady(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	if timeout <= 0 {
		timeout = remoteWorkspaceReadyTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(remoteWorkspaceReadyPoll)
	defer ticker.Stop()

	for {
		ready, err := check()
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("workspace did not become ready within %s", timeout)
		case <-ticker.C:
		}
	}
}

func (api *remoteAPI) activeWorkspaceReady(workspaceRoot string) (bool, error) {
	if api == nil || api.app == nil {
		return false, fmt.Errorf("app not ready")
	}
	targetRoot := normalizeProjectRoot(workspaceRoot)
	api.app.mu.RLock()
	tab := api.app.activeTabLocked()
	if tab == nil {
		api.app.mu.RUnlock()
		return false, fmt.Errorf("workspace switch did not activate a tab")
	}
	activeRoot := normalizeProjectRoot(tab.WorkspaceRoot)
	startupErr := strings.TrimSpace(tab.StartupErr)
	ready := tab.Ready && tab.Ctrl != nil
	api.app.mu.RUnlock()

	if targetRoot != "" && activeRoot != targetRoot {
		return false, fmt.Errorf("workspace switch activated %q, want %q", activeRoot, targetRoot)
	}
	if startupErr != "" {
		return false, fmt.Errorf("workspace failed to start: %s", startupErr)
	}
	return ready, nil
}
