package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
	"workground2/internal/tool/sessiontool"
)

// appAssistantSessionControl adapts the desktop session registry to the
// sessiontool.SessionControl contract the Assistant's session-control tools
// consume. It resolves a stable session ID to the live control.SessionAPI
// (runtime registry ID first, then the durable session path/BranchID) and
// submits intent only — it never invents or stores execution state.
type appAssistantSessionControl struct {
	app *App
}

var _ sessiontool.SessionControl = (*appAssistantSessionControl)(nil)

// sessionCtrlByID resolves an explicit session ID to its live runtime holder.
// It accepts both the runtime registry ID and the durable session path/BranchID,
// so the session_list/session_status tools (which report BranchID) can target
// the same sessions the steer/answer/cancel/fork tools operate on.
func (a *App) sessionCtrlByID(id string) (*WorkspaceTab, control.SessionAPI) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	if tab, ctrl := a.sessionAndCtrl(id); tab != nil {
		return tab, ctrl
	}
	a.sessions.mu.RLock()
	defer a.sessions.mu.RUnlock()
	for _, tab := range a.sessions.items {
		if tab == nil {
			continue
		}
		path := tab.currentSessionPath()
		if agent.BranchID(path) == id || strings.TrimSpace(tab.SessionPath) == id {
			a.mu.RLock()
			ctrl := tab.Ctrl
			a.mu.RUnlock()
			return tab, ctrl
		}
	}
	return nil, nil
}

func (c *appAssistantSessionControl) requireCtrl(sessionID string) (control.SessionAPI, error) {
	_, ctrl := c.app.sessionCtrlByID(sessionID)
	if ctrl == nil {
		return nil, errors.New("session not found or not running: " + sessionID)
	}
	return ctrl, nil
}

func (c *appAssistantSessionControl) Steer(sessionID, text, requestID string) error {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return err
	}
	ctrl.Steer(text)
	return nil
}

func (c *appAssistantSessionControl) AnswerQuestion(sessionID, interactionID string, answers []event.AskAnswer, requestID string) error {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return err
	}
	ctrl.AnswerQuestion(interactionID, answers)
	return nil
}

func (c *appAssistantSessionControl) Cancel(sessionID, requestID string) error {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return err
	}
	ctrl.Cancel()
	return nil
}

// resumeCtrl resolves a stable Session ID to a live Controller, restoring the
// durable session into a tab when it is not already running in-process. It is
// the shared resolve step for Resume and Retry.
func (c *appAssistantSessionControl) resumeCtrl(sessionID string) (control.SessionAPI, error) {
	tab, ctrl := c.app.sessionCtrlByID(sessionID)
	if ctrl != nil {
		return ctrl, nil
	}
	path, err := c.sessionPathByID(sessionID)
	if err != nil {
		return nil, err
	}
	tab, err = c.app.ensureTabForSessionPath(path)
	if err != nil {
		return nil, err
	}
	// Wait for the restored controller to finish booting, mirroring Create.
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, ctrl := c.app.tabAndCtrlByID(tab.ID)
		if ctrl != nil {
			return ctrl, nil
		}
		if tab.Ready && strings.TrimSpace(tab.StartupErr) != "" {
			return nil, fmt.Errorf("assistant session startup: %s", tab.StartupErr)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("assistant session startup timed out")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// sessionPathByID finds the durable session file for a stable Session ID across
// the global session dir and every known project session dir.
func (c *appAssistantSessionControl) sessionPathByID(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session ID is required")
	}
	dirs := []string{config.SessionDir()}
	for _, p := range loadProjectsFile().Projects {
		dirs = append(dirs, config.ProjectSessionDir(p.Root))
	}
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		all, err := agent.ListSessions(dir)
		if err != nil {
			continue
		}
		for _, s := range all {
			if agent.BranchID(s.Path) == sessionID {
				return s.Path, nil
			}
		}
	}
	return "", fmt.Errorf("session not found: %s", sessionID)
}

// resumeFromCheckpoint continues an interrupted model/tool round of ctrl when
// the session has a durable unfinished round (hasUnfinishedRound). It never
// re-submits the last user turn: the transcript already carries it, and
// ContinueTurn picks the round up from the checkpoint. Sessions without an
// unfinished round return a typed error instead of inventing work.
func (c *appAssistantSessionControl) resumeFromCheckpoint(ctrl control.SessionAPI) error {
	if ctrl == nil {
		return errors.New("session controller unavailable")
	}
	if ctrl.Running() {
		return nil // already running; nothing to resume
	}
	if _, pending := ctrl.PendingInteraction(); pending {
		return nil // waiting on the user; resume leaves it waiting
	}
	path := strings.TrimSpace(ctrl.SessionPath())
	if !hasUnfinishedRound(path) {
		return errors.New("session has no unfinished round to resume")
	}
	cont, ok := ctrl.(interface {
		ContinueTurn(context.Context) error
	})
	if !ok {
		return errors.New("session controller does not support checkpoint continue")
	}
	return cont.ContinueTurn(context.Background())
}

// hasUnfinishedRound reports whether the durable session at path has an
// unfinished model/tool round: either the InFlightTurnMeta marker is still
// present, or the transcript ends on a user turn with no assistant reply after
// it (the post-strip shape). Resume strips the partial tail and clears the
// marker, so the history shape is the authoritative checkpoint; the marker is
// the pre-restore signal.
func hasUnfinishedRound(path string) bool {
	if path == "" {
		return false
	}
	if meta, ok, err := agent.LoadBranchMeta(path); err == nil && ok && meta.InFlightTurn != nil {
		return true
	}
	sess, err := agent.LoadSession(path)
	if err != nil {
		return false
	}
	msgs := sess.Snapshot()
	if len(msgs) == 0 {
		return false
	}
	last := msgs[len(msgs)-1]
	return last.Role == provider.RoleUser
}

func (c *appAssistantSessionControl) Resume(sessionID, requestID string) error {
	ctrl, err := c.resumeCtrl(sessionID)
	if err != nil {
		return err
	}
	return c.resumeFromCheckpoint(ctrl)
}

// retryFromFailure allows an in-place retry only when the session's durable
// failure record is classified retryable_known. outcome_unknown, blocked_policy,
// blocked_dependency, failed_known, and the absence of a failure checkpoint are
// all explicit typed refusals — the host never guesses a redo of external work.
func (c *appAssistantSessionControl) retryFromFailure(ctrl control.SessionAPI) error {
	if ctrl == nil {
		return errors.New("session controller unavailable")
	}
	if ctrl.Running() {
		return nil // already running; nothing to retry
	}
	path := strings.TrimSpace(ctrl.SessionPath())
	if path == "" {
		return errors.New("session path is unavailable")
	}
	failure := agent.LoadSessionFailure(path)
	if failure == nil {
		return agent.ErrNoFailureCheckpoint
	}
	if err := agent.RetryErrFromClass(failure.Class); err != nil {
		return err
	}
	// retryable_known: continue from the checkpoint (same as resume) — the
	// round restarts from the last durable boundary, never by re-submitting
	// the final user turn as a new message.
	return c.resumeFromCheckpoint(ctrl)
}

func (c *appAssistantSessionControl) Retry(sessionID, requestID string) error {
	ctrl, err := c.resumeCtrl(sessionID)
	if err != nil {
		return err
	}
	return c.retryFromFailure(ctrl)
}

// forkStablePath resolves the deterministic fork branch path for
// (parentSessionID, requestID) inside dir. It mirrors the daemon helper so
// desktop and daemon resolve the same branch for the same request.
func forkStablePath(dir, parentSessionID, requestID string) string {
	return filepath.Join(dir, agent.ForkStableID(parentSessionID, requestID)+".jsonl")
}

func (c *appAssistantSessionControl) Fork(sessionID, requestID string) (string, error) {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return "", err
	}
	parentPath := strings.TrimSpace(ctrl.SessionPath())
	if parentPath == "" {
		return "", errors.New("session path is unavailable")
	}
	dir := filepath.Dir(parentPath)
	branchID := agent.ForkStableID(sessionID, requestID)
	forkPath := forkStablePath(dir, sessionID, requestID)

	// Claim the (requestID -> fork branch) binding before any fork work. A
	// replayed or concurrent request resolves here and never forks twice; a
	// request ID reused with a different parent is an explicit conflict.
	if strings.TrimSpace(requestID) != "" {
		rec, err := agent.ReserveSessionReceipt(dir, "fork:"+requestID, agent.SessionReceipt{
			SessionID:   branchID,
			Fingerprint: agent.SessionReceiptFingerprint("fork", sessionID, requestID),
		})
		if err != nil {
			var conflict *agent.SessionReceiptConflictError
			if errors.As(err, &conflict) {
				return "", fmt.Errorf("session_fork request %q reused with a different parent session (already bound to %s)", requestID, conflict.SessionID)
			}
			return "", err
		}
		if rec.State.AtLeast(agent.ReceiptStarted) {
			return rec.SessionID, nil // already forked by a prior host/crash
		}
	}

	forker, ok := ctrl.(interface {
		ForkSessionAt(turn int, name, forkPath string) (string, error)
	})
	if !ok {
		return "", errors.New("session controller does not support deterministic fork")
	}
	forkedPath, err := forker.ForkSessionAt(ctrl.Turn(), "", forkPath)
	if err != nil {
		return "", err
	}
	_ = forkedPath // branch ID is deterministic (ForkStableID)
	// Stamp the fork's own create key onto the meta so the fork's provenance is
	// durable and consistent with desktop/daemon create paths.
	if strings.TrimSpace(requestID) != "" {
		if meta, err := agent.EnsureBranchMeta(forkPath); err == nil {
			meta.CreateRequestID = strings.TrimSpace(requestID)
			_ = agent.SaveBranchMetaPreserveUpdated(forkPath, meta)
		}
	}
	if strings.TrimSpace(requestID) != "" {
		if _, err := agent.AdvanceSessionReceipt(dir, "fork:"+requestID, agent.ReceiptMetaReady); err != nil {
			return "", err
		}
		if _, err := agent.AdvanceSessionReceipt(dir, "fork:"+requestID, agent.ReceiptCommitted); err != nil {
			return "", err
		}
	}
	return branchID, nil
}

func (c *appAssistantSessionControl) Create(req sessiontool.SessionCreateRequest) (string, error) {
	title := strings.TrimSpace(req.Title)
	prompt := strings.TrimSpace(req.Prompt)
	if title == "" || prompt == "" {
		return "", errors.New("session_create requires a title and prompt")
	}
	scope := assistant.ScopeGlobal
	workspaceRoot := ""
	if strings.TrimSpace(req.Workspace) != "" {
		scope, workspaceRoot = assistant.ScopeWorkspace, strings.TrimSpace(req.Workspace)
	}
	actualRoot := globalWorkspaceRoot()
	if scope == assistant.ScopeWorkspace {
		actualRoot = normalizeProjectRoot(workspaceRoot)
	}
	sessionDir := desktopSessionDir(actualRoot)

	hasRequest := req.RequestID != "" && req.OwnerID != ""
	// Full-input fingerprint: a request ID reused with any differing input
	// (owner, workspace, purpose, parent, title, prompt) is a conflict.
	fingerprint := agent.SessionReceiptInput{
		Owner: req.OwnerID, RequestID: req.RequestID, Workspace: workspaceRoot,
		Purpose: string(req.Purpose), Parent: req.ParentID, Title: title, Prompt: prompt,
	}.Fingerprint()

	// Reserve the (requestID -> SessionID) binding BEFORE any Session is created,
	// so a replayed or concurrently-racing request resolves here and never
	// submits a second Session.
	var reserved agent.SessionReceipt
	if hasRequest {
		stableID := agent.StableSessionID(req.OwnerID, req.RequestID)
		rec, err := agent.ReserveSessionReceipt(sessionDir, req.RequestID, agent.SessionReceipt{SessionID: stableID, Fingerprint: fingerprint})
		if err != nil {
			var conflict *agent.SessionReceiptConflictError
			if errors.As(err, &conflict) {
				return "", fmt.Errorf("session_create request %q reused with different input (already bound to %s)", req.RequestID, conflict.SessionID)
			}
			return "", err
		}
		if rec.State.AtLeast(agent.ReceiptStarted) {
			return rec.SessionID, nil
		}
		reserved = rec
	}

	// Deterministic Session file (atomic O_EXCL); a concurrent loser resolves to
	// the same path.
	stablePath := ""
	if hasRequest {
		var err error
		stablePath, _, err = agent.CreateStableSessionFile(sessionDir, req.OwnerID, req.RequestID)
		if err != nil {
			return "", err
		}
	}

	run := assistant.Run{
		AssistantID: req.OwnerID, Prompt: prompt, Mission: title,
		Scope: scope, WorkspaceRoot: workspaceRoot,
	}
	tab, err := c.app.ensureAssistantBackgroundTab(run, stablePath)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(tab.currentSessionPath())
	if path == "" {
		return "", errors.New("created session path is unavailable")
	}
	if meta, err := agent.EnsureBranchMeta(path); err == nil {
		meta.Purpose = req.Purpose
		if req.ParentID != "" {
			meta.ParentID = req.ParentID
		}
		if strings.TrimSpace(req.ResponsibilityID) != "" {
			meta.ResponsibilityID = strings.TrimSpace(req.ResponsibilityID)
		}
		if strings.TrimSpace(req.RequestID) != "" {
			meta.CreateRequestID = strings.TrimSpace(req.RequestID)
		}
		if strings.TrimSpace(req.OwnerID) != "" {
			meta.AssistantID = strings.TrimSpace(req.OwnerID)
		}
		if meta.WorkspaceRoot == "" && workspaceRoot != "" {
			meta.WorkspaceRoot = workspaceRoot
		}
		meta.SessionKind = agent.SessionKindAssistant
		meta.SessionSource = agent.SessionSourceAssist
		_ = agent.SaveBranchMetaPreserveUpdated(path, meta)
	}
	if hasRequest {
		if _, err := agent.AdvanceSessionReceipt(sessionDir, req.RequestID, agent.ReceiptMetaReady); err != nil {
			return "", err
		}
	}
	sessionID := agent.BranchID(path)

	// Submit the prompt only when the session file has no content yet AND the
	// receipt has not recorded the submit, so a crash between create and submit
	// (or a replay) never submits twice.
	if !agent.SessionFileHasContent(path) && !reserved.State.AtLeast(agent.ReceiptStarted) {
		deadline := time.Now().Add(20 * time.Second)
		for {
			_, ctrl := c.app.tabAndCtrlByID(tab.ID)
			if ctrl != nil {
				break
			}
			if tab != nil && tab.Ready && strings.TrimSpace(tab.StartupErr) != "" {
				return "", fmt.Errorf("assistant session startup: %s", tab.StartupErr)
			}
			if time.Now().After(deadline) {
				return "", errors.New("assistant session startup timed out")
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err := c.app.SubmitToTab(tab.ID, prompt); err != nil {
			return "", err
		}
	}

	if hasRequest {
		if _, err := agent.AdvanceSessionReceipt(sessionDir, req.RequestID, agent.ReceiptStarted); err != nil {
			return "", err
		}
		rec, err := agent.AdvanceSessionReceipt(sessionDir, req.RequestID, agent.ReceiptCommitted)
		if err != nil {
			return "", err
		}
		sessionID = rec.SessionID
	}
	return sessionID, nil
}

// sessionFileHasContent reports whether a session file already has any content
// (used to make prompt submission idempotent across create/submit/replay).
func (c *appAssistantSessionControl) PendingInteractions(sessionID string) ([]sessiontool.SessionInteraction, error) {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return nil, err
	}
	pi, ok := ctrl.PendingInteraction()
	if !ok {
		return nil, nil
	}
	switch pi.Kind {
	case control.PendingInteractionAsk:
		return []sessiontool.SessionInteraction{{
			Kind: "ask", ID: pi.Ask.ID, Questions: pi.Ask.Questions, DueAt: time.Time{},
		}}, nil
	case control.PendingInteractionApproval:
		return []sessiontool.SessionInteraction{{Kind: "approval", ID: pi.Approval.ID}}, nil
	default:
		return nil, nil
	}
}

// sessionTools returns the Assistant's session query and control tools, bound to
// the live desktop session registry so a supervisor turn can observe and steer
// the sessions it manages.
func (r *AssistantRuntime) sessionTools() []tool.Tool {
	if r == nil || r.app == nil {
		return nil
	}
	dir := config.SessionDir()
	queryDirs := (&desktopSupervisorHost{r: r}).supervisorSessionDirs()
	adapter := &appAssistantSessionControl{app: r.app}
	return []tool.Tool{
		sessiontool.NewSessionListToolDirs(queryDirs),
		sessiontool.NewSessionStatusToolDirs(queryDirs),
		sessiontool.NewSessionReadToolDirs(queryDirs),
		sessiontool.NewSessionSteerTool(adapter, dir),
		sessiontool.NewInteractionAnswerTool(adapter, dir),
		sessiontool.NewSessionCancelTool(adapter, dir),
		sessiontool.NewSessionResumeTool(adapter, dir),
		sessiontool.NewSessionRetryTool(adapter, dir),
		sessiontool.NewSessionForkTool(adapter, dir),
		sessiontool.NewSessionCreateTool(adapter, dir),
		sessiontool.NewInteractionListTool(adapter, dir),
	}
}
