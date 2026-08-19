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
	"workground2/internal/decision"
)

// remoteAPI is a minimal HTTP server on 127.0.0.1 that lets the CLI send
// commands to a running Desktop instance: open a session, create a new one, or
// focus the window. The port is written to ~/.WorkGround2/desktop-port so the
// CLI can discover it.
type remoteAPI struct {
	app        *App
	srv        *http.Server
	port       int
	closed     chan struct{}
	newSession func(scope, workspace, name string) (*WorkspaceTab, string, bool, error) // test seam

	mu        sync.Mutex
	submitted map[string]remoteSubmittedSession
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
	api.registerRoutes(mux)

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

// registerRoutes 注册远程 API 路由。主人决策的 /api/v1/decisions/* 仅在
// ownerDecisionEnabled 打开时注册；关闭时这些路径返回 404（fail closed）。
func (api *remoteAPI) registerRoutes(mux *http.ServeMux) {
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
	if api.app.ownerDecisionActive() {
		mux.HandleFunc("/api/v1/decisions/create", api.handleDecisionCreate)
		mux.HandleFunc("/api/v1/decisions/get", api.handleDecisionGet)
		mux.HandleFunc("/api/v1/decisions/list", api.handleDecisionList)
		mux.HandleFunc("/api/v1/decisions/wait", api.handleDecisionWait)
		mux.HandleFunc("/api/v1/decisions/cancel", api.handleDecisionCancel)
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
	api.app.emitSessionActivated("remote-open")
	api.app.mu.RLock()
	tab := api.app.activeTabLocked()
	api.app.mu.RUnlock()
	if tab == nil {
		http.Error(w, "opened session is unavailable", http.StatusInternalServerError)
		return
	}
	api.writeJSON(w, api.sessionResponseForTab(tab, "ok"))
}

func (api *remoteAPI) handleSessionNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
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

	// Every external create call creates one new Session. Missing workspace
	// means Global and never reads or changes the UI's current workspace.
	scope := "global"
	if workspace != "" {
		scope = "project"
	}
	create := api.newSession
	if create == nil {
		create = api.app.newBackgroundSession
	}
	tab, _, created, err := create(scope, workspace, sessionName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if body.ToolApprovalMode != "" {
		api.app.SetToolApprovalModeForTab(tab.ID, body.ToolApprovalMode)
	}
	out := api.sessionResponseForTab(tab, "ok")
	if sessionName != "" {
		out["sessionName"] = sessionName
	}
	out["created"] = created
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

func (api *remoteAPI) handleDecisionCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body DecisionCreateInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid decision request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.AgentID) == "" || strings.TrimSpace(body.ThreadID) == "" || strings.TrimSpace(body.IdempotencyKey) == "" {
		http.Error(w, "invalid decision request: agentId, threadId and idempotencyKey are required", http.StatusBadRequest)
		return
	}
	body.IdempotencyKey = "remote:" + strings.TrimSpace(body.AgentID) + ":" + strings.TrimSpace(body.ThreadID) + ":" + strings.TrimSpace(body.IdempotencyKey)
	value, err := api.app.CreateDecision(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	api.writeJSON(w, value)
}

func (api *remoteAPI) handleDecisionGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if api.app.decisionBroker == nil {
		http.Error(w, "decision broker unavailable", http.StatusServiceUnavailable)
		return
	}
	value, ok := api.app.decisionBroker.Get(strings.TrimSpace(r.URL.Query().Get("id")))
	if !ok {
		http.Error(w, decision.ErrNotFound.Error(), http.StatusNotFound)
		return
	}
	if !remoteDecisionOwned(value, r.URL.Query().Get("agentId"), r.URL.Query().Get("threadId")) {
		http.Error(w, "decision is not owned by this caller", http.StatusForbidden)
		return
	}
	api.writeJSON(w, value)
}

func (api *remoteAPI) handleDecisionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agentID, threadID := strings.TrimSpace(r.URL.Query().Get("agentId")), strings.TrimSpace(r.URL.Query().Get("threadId"))
	if agentID == "" || threadID == "" || api.app.decisionBroker == nil {
		http.Error(w, "agentId and threadId are required", http.StatusBadRequest)
		return
	}
	values := api.app.decisionBroker.List(decision.ListFilter{Origin: &decision.Origin{Kind: "agent", AgentID: agentID, ThreadID: threadID}})
	api.writeJSON(w, map[string]any{"revision": api.app.decisionBroker.Snapshot().Revision, "decisions": values})
}

func (api *remoteAPI) handleDecisionWait(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if api.app.decisionBroker == nil {
		http.Error(w, "decision broker unavailable", http.StatusServiceUnavailable)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	agentID, threadID := strings.TrimSpace(r.URL.Query().Get("agentId")), strings.TrimSpace(r.URL.Query().Get("threadId"))
	if agentID == "" || threadID == "" {
		http.Error(w, "agentId and threadId are required", http.StatusBadRequest)
		return
	}
	timeoutSec, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	if timeoutSec <= 0 || timeoutSec > 25 {
		timeoutSec = 25
	}
	if id != "" {
		value, ok := api.app.decisionBroker.Get(id)
		if !ok {
			http.Error(w, decision.ErrNotFound.Error(), http.StatusNotFound)
			return
		}
		if !remoteDecisionOwned(value, agentID, threadID) {
			http.Error(w, "decision is not owned by this caller", http.StatusForbidden)
			return
		}
		if decisionWaitTerminal(value.Status) {
			api.writeRemoteDecisionList(w, agentID, threadID)
			return
		}
	} else if current := api.app.decisionBroker.Snapshot(); current.Revision > after {
		api.writeRemoteDecisionList(w, agentID, threadID)
		return
	}
	changes, unsubscribe := api.app.decisionBroker.Subscribe(64)
	defer unsubscribe()
	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case change := <-changes:
			if id == "" || change.Decision.ID == id {
				api.writeRemoteDecisionList(w, agentID, threadID)
				return
			}
		case <-timer.C:
			api.writeRemoteDecisionList(w, agentID, threadID)
			return
		}
	}
}

func decisionWaitTerminal(status decision.Status) bool {
	switch status {
	case decision.StatusDecided, decision.StatusApplied, decision.StatusCancelled, decision.StatusOrphaned, decision.StatusApplyFailed:
		return true
	default:
		return false
	}
}

func (api *remoteAPI) handleDecisionCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID       string `json:"id"`
		AgentID  string `json:"agentId"`
		ThreadID string `json:"threadId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		http.Error(w, "invalid request: id is required", http.StatusBadRequest)
		return
	}
	value, ok := api.app.decisionBroker.Get(strings.TrimSpace(body.ID))
	if !ok {
		http.Error(w, decision.ErrNotFound.Error(), http.StatusNotFound)
		return
	}
	if !remoteDecisionOwned(value, body.AgentID, body.ThreadID) {
		http.Error(w, "decision is not owned by this caller", http.StatusForbidden)
		return
	}
	transition, err := api.app.decisionBroker.Cancel(body.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	api.writeJSON(w, transition.Decision)
}

func remoteDecisionOwned(value decision.Decision, agentID, threadID string) bool {
	agentID = strings.TrimSpace(agentID)
	threadID = strings.TrimSpace(threadID)
	return agentID != "" && threadID != "" && value.Origin.Kind == "agent" && value.Origin.AgentID == agentID && value.Origin.ThreadID == threadID
}

func (api *remoteAPI) writeRemoteDecisionList(w http.ResponseWriter, agentID, threadID string) {
	values := api.app.decisionBroker.List(decision.ListFilter{Origin: &decision.Origin{Kind: "agent", AgentID: agentID, ThreadID: threadID}})
	api.writeJSON(w, map[string]any{"revision": api.app.decisionBroker.Snapshot().Revision, "decisions": values})
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

func (api *remoteAPI) remoteSession(id string) (*WorkspaceTab, int, string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, http.StatusBadRequest, "sessionId is required"
	}
	tab := api.app.sessionByID(id)
	if tab == nil {
		return nil, http.StatusNotFound, fmt.Sprintf("session %q was not found", id)
	}
	return tab, http.StatusOK, ""
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
	out["sessionId"] = tab.SessionID
	if path != "" {
		out["path"] = path
	}
	api.app.mu.RLock()
	ctrl := tab.Ctrl
	ready := tab.Ready && ctrl != nil
	buildDone := tab.Ready
	startupErr := strings.TrimSpace(tab.StartupErr)
	queued := strings.TrimSpace(tab.pendingRemoteInput) != ""
	submitErr := strings.TrimSpace(tab.remoteSubmitErr)
	api.app.mu.RUnlock()
	out["ready"] = ready
	if ctrl != nil {
		rs := ctrl.RuntimeStatus()
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
	if queued {
		out["queued"] = true
		out["submitted"] = true
	}
	switch {
	case startupErr != "" && ctrl == nil:
		delete(out, "starting")
		out["mode"] = "startup_failed"
		out["startupError"] = startupErr
		out["foregroundActive"] = false
		out["activeRuntimeWork"] = false
	case buildDone && ctrl == nil:
		delete(out, "starting")
		out["mode"] = "startup_failed"
		out["startupError"] = "controller unavailable after startup"
		out["foregroundActive"] = false
		out["activeRuntimeWork"] = false
	case submitErr != "":
		delete(out, "starting")
		out["mode"] = "submit_failed"
		out["submitError"] = submitErr
		out["foregroundActive"] = false
		out["activeRuntimeWork"] = false
	case ctrl == nil || (queued && !tabHasActiveRuntimeWork(tab)):
		applyRemoteStarting(out)
	}
	_, starting := out["starting"]
	if !starting && !tabHasActiveRuntimeWork(tab) && !tabHasPendingPrompt(tab) {
		if report := api.app.lastAssistantReport(tab.ID, 2000); report != "" {
			out["report"] = report
		}
	}
	return out
}

func applyRemoteStarting(out map[string]any) {
	out["running"] = false
	out["pendingPrompt"] = false
	out["foregroundActive"] = true
	out["backgroundOnly"] = false
	out["activeRuntimeWork"] = true
	out["cancelRequested"] = false
	out["starting"] = true
	out["mode"] = "starting"
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

type remoteSubmitError struct {
	status  int
	message string
}

func (e *remoteSubmitError) Error() string { return e.message }

// submitRemote accepts one durable initial input while a session controller is
// still starting. Repeating the same input is idempotent; a different queued
// input is rejected so callers cannot silently replace accepted work.
func (a *App) submitRemote(tab *WorkspaceTab, input string) (bool, error) {
	if tab == nil || strings.TrimSpace(input) == "" {
		return false, &remoteSubmitError{status: http.StatusBadRequest, message: "session and prompt are required"}
	}

	a.mu.Lock()
	if tab.ReadOnly {
		a.mu.Unlock()
		return false, &remoteSubmitError{status: http.StatusConflict, message: readOnlyChannelErr().Error()}
	}
	if pending := strings.TrimSpace(tab.pendingRemoteInput); pending != "" && pending != input {
		a.mu.Unlock()
		return false, &remoteSubmitError{status: http.StatusConflict, message: "session already has a different queued submission"}
	}
	if tab.Ctrl == nil {
		if startupErr := strings.TrimSpace(tab.StartupErr); startupErr != "" {
			a.mu.Unlock()
			return false, &remoteSubmitError{status: http.StatusServiceUnavailable, message: "workspace failed to start: " + startupErr}
		}
		if tab.Ready {
			a.mu.Unlock()
			return false, &remoteSubmitError{status: http.StatusServiceUnavailable, message: "session controller is unavailable after startup"}
		}
		tab.pendingRemoteInput = input
		tab.remoteSubmitErr = ""
		a.saveTabsLocked()
		a.mu.Unlock()
		return true, nil
	}
	if tab.remoteSubmitting {
		a.mu.Unlock()
		return true, nil
	}
	tab.pendingRemoteInput = input
	tab.remoteSubmitErr = ""
	tab.remoteSubmitting = true
	a.saveTabsLocked()
	a.mu.Unlock()

	err := a.submitToTab(tab.ID, input, false)
	a.finishRemoteSubmit(tab, input, err)
	return false, err
}

// drainRemoteSubmit resumes a persisted starting-phase submission after the
// controller becomes ready. Failures keep the input and remain retryable.
func (a *App) drainRemoteSubmit(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	a.mu.Lock()
	input := tab.pendingRemoteInput
	if input == "" || tab.Ctrl == nil || tab.remoteSubmitting {
		a.mu.Unlock()
		return
	}
	tab.remoteSubmitting = true
	tab.remoteSubmitErr = ""
	ctrl := tab.Ctrl
	a.mu.Unlock()

	if remoteInputSubmitted(ctrl, input) {
		a.finishRemoteSubmit(tab, input, nil)
		return
	}
	err := a.submitToTab(tab.ID, input, false)
	a.finishRemoteSubmit(tab, input, err)
}

func remoteInputSubmitted(ctrl control.SessionAPI, input string) bool {
	if ctrl == nil {
		return false
	}
	for _, message := range ctrl.History() {
		if string(message.Role) == "user" && message.Content == input {
			return true
		}
	}
	return false
}

func (a *App) finishRemoteSubmit(tab *WorkspaceTab, input string, submitErr error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if tab.pendingRemoteInput != input {
		return
	}
	tab.remoteSubmitting = false
	if submitErr != nil {
		tab.remoteSubmitErr = submitErr.Error()
	} else {
		tab.pendingRemoteInput = ""
		tab.remoteSubmitErr = ""
	}
	a.saveTabsLocked()
}

func (api *remoteAPI) handleSessionSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Prompt           string `json:"prompt"`
		SessionID        string `json:"sessionId"`
		ToolApprovalMode string `json:"toolApprovalMode,omitempty"` // optional: ask, auto, yolo
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		http.Error(w, "invalid request: prompt is required", http.StatusBadRequest)
		return
	}
	targetTab, status, message := api.remoteSession(body.SessionID)
	if targetTab == nil {
		http.Error(w, message, status)
		return
	}
	if body.ToolApprovalMode != "" {
		api.app.SetToolApprovalModeForTab(targetTab.ID, body.ToolApprovalMode)
	}
	submittedAt := time.Now()
	queued, err := api.app.submitRemote(targetTab, body.Prompt)
	if err != nil {
		status := http.StatusInternalServerError
		if remoteErr, ok := err.(*remoteSubmitError); ok {
			status = remoteErr.status
		}
		http.Error(w, err.Error(), status)
		return
	}
	api.markSubmitted(targetTab.currentSessionPath(), submittedAt)
	out := api.sessionResponseForTab(targetTab, "ok")
	if queued {
		out["queued"] = true
	}
	api.writeJSON(w, out)
}

func (api *remoteAPI) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	tab, status, message := api.remoteSession(r.URL.Query().Get("sessionId"))
	if tab == nil {
		http.Error(w, message, status)
		return
	}
	api.writeJSON(w, api.sessionResponseForTab(tab, "ok"))
}

func (api *remoteAPI) handleSessionApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		SessionID string `json:"sessionId"`
		ID        string `json:"id,omitempty"`
		Allow     bool   `json:"allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	tab, status, message := api.remoteSession(body.SessionID)
	if tab == nil {
		http.Error(w, message, status)
		return
	}
	if err := api.app.approvePendingIDForTab(tab.ID, body.ID, body.Allow); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (api *remoteAPI) handleSessionAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		SessionID string           `json:"sessionId"`
		ID        string           `json:"id"`
		Answers   []QuestionAnswer `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	tab, status, message := api.remoteSession(body.SessionID)
	if tab == nil {
		http.Error(w, message, status)
		return
	}
	if err := api.app.answerPendingQuestionForTab(tab.ID, body.ID, body.Answers); err != nil {
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
		tab := api.app.tabByIDLocked(tabID)
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
