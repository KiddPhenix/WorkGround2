package assistantdaemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/boot"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
	"workground2/internal/tool/sessiontool"
)

// daemonSessionControl is the headless SessionControl implementation the daemon
// uses to execute the converged Session path. Create builds a headless
// Controller, stamps the durable Assistant identity and Purpose, submits the
// work prompt, and keeps the controller alive so the turn runs to completion;
// the returned stable Session ID is the session's BranchID. Runs and RunnerJobs
// are never involved.
type daemonSessionControl struct {
	ctx    context.Context
	model  string
	stderr io.Writer
	store  *assistant.Store
	tools  func(assistantID, executionID string) []tool.Tool

	mu   sync.Mutex
	live map[string]*control.Controller

	// restoreMu serializes resumeCtrl so concurrent restores of the same (or
	// different) Session build at most one Controller per Session. Restores are
	// rare (restart / first reference), so a single lock is sufficient.
	restoreMu sync.Mutex

	// build is an optional restore seam for tests; when nil, resumeCtrlLocked
	// performs the real boot.Build + Resume. It runs under restoreMu.
	build func(sessionID string) (*control.Controller, error)

	// onRestoreOpts is an optional test hook observing the boot.Options a
	// restore would use, so parity (workspace/policy/tool surface) can be
	// asserted without booting a real model.
	onRestoreOpts func(boot.Options)
}

var _ sessiontool.SessionControl = (*daemonSessionControl)(nil)

func newDaemonSessionControl(model string, stderr io.Writer, store *assistant.Store, tools func(assistantID, executionID string) []tool.Tool) *daemonSessionControl {
	return &daemonSessionControl{
		ctx: context.Background(), model: model, stderr: stderr, store: store,
		tools: tools, live: map[string]*control.Controller{},
	}
}

func (c *daemonSessionControl) Create(req sessiontool.SessionCreateRequest) (string, error) {
	title := strings.TrimSpace(req.Title)
	prompt := strings.TrimSpace(req.Prompt)
	if title == "" || prompt == "" {
		return "", errors.New("session_create requires a title and prompt")
	}
	workspace := strings.TrimSpace(req.Workspace)
	if workspace == "" && c.store != nil && strings.TrimSpace(req.OwnerID) != "" {
		if snap, err := c.store.Get(req.OwnerID); err == nil && snap.Assistant.Scope == assistant.ScopeWorkspace {
			workspace = snap.Assistant.WorkspaceRoot
		}
	}
	sessionDir := config.SessionDir()
	if workspace != "" {
		sessionDir = config.ProjectSessionDir(workspace)
	}
	hasRequest := req.RequestID != "" && strings.TrimSpace(req.OwnerID) != ""
	// Full-input fingerprint: a request ID reused with any differing input
	// (owner, workspace, purpose, parent, title, prompt) is a conflict, not a
	// silent reuse of the wrong Session.
	fingerprint := agent.SessionReceiptInput{
		Owner: req.OwnerID, RequestID: req.RequestID, Workspace: workspace,
		Purpose: string(req.Purpose), Parent: req.ParentID, Title: title, Prompt: prompt,
	}.Fingerprint()

	// Reserve the (requestID -> SessionID) binding BEFORE any controller work, so
	// a replayed or concurrently-racing request resolves here and never submits a
	// second Session. The deterministic SessionID is the stable path's BranchID.
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
		// Already submitted by a prior host/tick: return the bound Session ID.
		if rec.State.AtLeast(agent.ReceiptStarted) {
			return rec.SessionID, nil
		}
		reserved = rec
	}

	var extra []tool.Tool
	if c.tools != nil {
		extra = c.tools(req.OwnerID, req.OwnerID)
	}
	ctrl, err := boot.Build(c.ctx, boot.Options{
		Model: c.model, RequireKey: true,
		Sink:            event.FuncSink(func(event.Event) {}),
		Stderr:          c.stderr,
		WorkspaceRoot:   workspace,
		SessionDir:      sessionDir,
		SessionKind:     agent.SessionKindAssistant,
		ExtraTools:      extra,
		ApprovalTimeout: 2 * time.Second,
		WorkGate:        c.store.WorkGate(),
	})
	if err != nil {
		return "", err
	}
	// Deterministic session path by stable identity (atomic O_EXCL); a concurrent
	// loser resolves to the same file.
	sessionPath := agent.NewSessionPath(ctrl.SessionDir(), ctrl.Label())
	if hasRequest {
		p, _, err := agent.CreateStableSessionFile(sessionDir, req.OwnerID, req.RequestID)
		if err != nil {
			return "", err
		}
		sessionPath = p
	}
	ctrl.SetSessionPath(sessionPath)
	meta, _ := agent.EnsureBranchMeta(ctrl.SessionPath())
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = strings.TrimSpace(req.OwnerID)
	meta.SessionSource = agent.SessionSourceAssist
	meta.Purpose = req.Purpose
	if meta.Purpose == "" {
		meta.Purpose = agent.PurposeManaged
	}
	if strings.TrimSpace(req.ResponsibilityID) != "" {
		meta.ResponsibilityID = strings.TrimSpace(req.ResponsibilityID)
	}
	if strings.TrimSpace(req.RequestID) != "" {
		meta.CreateRequestID = strings.TrimSpace(req.RequestID)
	}
	if meta.WorkspaceRoot == "" && workspace != "" {
		meta.WorkspaceRoot = workspace
	}
	meta.Status = agent.SessionStatusQueued
	_ = agent.SaveBranchMetaPreserveUpdated(ctrl.SessionPath(), meta)
	if hasRequest {
		if _, err := agent.AdvanceSessionReceipt(sessionDir, req.RequestID, agent.ReceiptMetaReady); err != nil {
			return "", err
		}
	}

	id := agent.BranchID(ctrl.SessionPath())
	c.mu.Lock()
	c.live[id] = ctrl
	c.mu.Unlock()
	// Submit only when the session file has no content yet AND the receipt has
	// not recorded the submit (idempotent submit across crash/concurrency). The
	// Controller persists the user turn as part of the turn; the receipt advance
	// to started records that the model turn began.
	if !agent.SessionFileHasContent(ctrl.SessionPath()) && !reserved.State.AtLeast(agent.ReceiptStarted) {
		ctrl.SubmitDisplay(prompt, prompt)
	}
	if hasRequest {
		if _, err := agent.AdvanceSessionReceipt(sessionDir, req.RequestID, agent.ReceiptStarted); err != nil {
			return "", err
		}
		rec, err := agent.AdvanceSessionReceipt(sessionDir, req.RequestID, agent.ReceiptCommitted)
		if err != nil {
			return "", err
		}
		id = rec.SessionID
	}
	return id, nil
}

func (c *daemonSessionControl) requireCtrl(sessionID string) (*control.Controller, error) {
	if ctrl := c.liveCtrl(sessionID); ctrl != nil {
		return ctrl, nil
	}
	// Serialize restores and re-check under the lock: a concurrent caller that
	// loses the race observes the winner's Controller instead of building a
	// second one for the same Session.
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()
	if ctrl := c.liveCtrl(sessionID); ctrl != nil {
		return ctrl, nil
	}
	if c.build != nil {
		ctrl, err := c.build(sessionID)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.live[sessionID] = ctrl
		c.mu.Unlock()
		return ctrl, nil
	}
	return c.resumeCtrlLocked(sessionID)
}

// liveCtrl returns the loaded Controller for sessionID, or nil.
func (c *daemonSessionControl) liveCtrl(sessionID string) *control.Controller {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live[sessionID]
}

// resumeCtrlLocked restores a live Controller from its persisted Session file
// when it is not already loaded (e.g. after a daemon restart). It resolves the
// stable Session ID to a durable path, loads the transcript, restores the
// durable WorkspaceRoot and Assistant identity, builds a headless Controller,
// and resumes it at the checkpoint. The caller holds restoreMu.
func (c *daemonSessionControl) resumeCtrlLocked(sessionID string) (*control.Controller, error) {
	path, err := c.sessionPathByID(sessionID)
	if err != nil {
		return nil, err
	}
	sess, err := agent.LoadSession(path)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", sessionID, err)
	}
	meta, ok, _ := agent.LoadBranchMeta(path)
	assistantID := ""
	workspaceRoot := ""
	if ok {
		assistantID = meta.AssistantID
		workspaceRoot = strings.TrimSpace(meta.WorkspaceRoot)
	}
	var extra []tool.Tool
	if c.tools != nil && assistantID != "" {
		extra = c.tools(assistantID, sessionID)
	}
	ctrl, err := boot.Build(c.ctx, c.restoreOptions(path, workspaceRoot, assistantID, sessionID, extra))
	if err != nil {
		return nil, err
	}
	ctrl.Resume(sess, path)
	c.mu.Lock()
	c.live[sessionID] = ctrl
	c.mu.Unlock()
	return ctrl, nil
}

// restoreOptions builds the boot.Options a restore uses so the durable
// WorkspaceRoot, the Assistant's tool surface, and the shared WorkGate are all
// re-established exactly as the session's creator intended. The observed
// options are handed to onRestoreOpts when set (test seam).
func (c *daemonSessionControl) restoreOptions(path, workspaceRoot, assistantID, sessionID string, extra []tool.Tool) boot.Options {
	opts := boot.Options{
		Model: c.model, RequireKey: true,
		Sink: event.FuncSink(func(event.Event) {}), Stderr: c.stderr,
		WorkspaceRoot:   workspaceRoot,
		SessionDir:      filepath.Dir(path),
		SessionKind:     agent.SessionKindAssistant,
		ExtraTools:      extra,
		ApprovalTimeout: 2 * time.Second,
	}
	if c.store != nil {
		opts.WorkGate = c.store.WorkGate()
	}
	if c.onRestoreOpts != nil {
		c.onRestoreOpts(opts)
	}
	return opts
}

// sessionPathByID resolves a stable Session ID to a durable session file across
// the global and every project session dir.
func (c *daemonSessionControl) sessionPathByID(sessionID string) (string, error) {
	dirs := []string{config.SessionDir()}
	if c.store != nil {
		if assistants, err := c.store.List(); err == nil {
			for _, a := range assistants {
				if a.Scope == assistant.ScopeWorkspace && strings.TrimSpace(a.WorkspaceRoot) != "" {
					dirs = append(dirs, config.ProjectSessionDir(strings.TrimSpace(a.WorkspaceRoot)))
				}
			}
		}
	}
	for _, dir := range dirs {
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

func (c *daemonSessionControl) Steer(sessionID, text, requestID string) error {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return err
	}
	ctrl.Steer(text)
	return nil
}

func (c *daemonSessionControl) AnswerQuestion(sessionID, interactionID string, answers []event.AskAnswer, requestID string) error {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return err
	}
	ctrl.AnswerQuestion(interactionID, answers)
	return nil
}

func (c *daemonSessionControl) Cancel(sessionID, requestID string) error {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return err
	}
	ctrl.Cancel()
	return nil
}

// turnResumer is the narrow controller surface resume/retry need: path,
// running state, pending interaction, and checkpoint continue. It is satisfied
// by *control.Controller and by the test fakes, so resume/retry decisions are
// unit-testable without booting a real model.
type turnResumer interface {
	SessionPath() string
	Running() bool
	PendingInteraction() (control.PendingInteraction, bool)
	ContinueTurn(ctx context.Context) error
}

// resumeFromCheckpoint continues an interrupted model/tool round of ctrl when
// the session has a durable InFlightTurnMeta (an unfinished round). It never
// re-submits the last user turn: the transcript already carries it, and
// ContinueTurn picks the round up from the checkpoint. Sessions without an
// in-flight round return a typed error instead of inventing work.
// hasUnfinishedRound reports whether the durable session at path has an
// unfinished model/tool round: the InFlightTurnMeta marker is present, OR the
// transcript ends on a user turn with no assistant reply after it. Resume
// strips the partial tail and clears the marker, so hosts must check this
// BEFORE restoring the controller — after Resume the marker is gone and only
// the history shape (ends-on-user) remains.
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

// resumeFromCheckpoint continues an interrupted model/tool round of ctrl when
// the session has an unfinished round. The caller must have confirmed
// hasUnfinishedRound BEFORE restoring the controller (Resume clears the
// marker). It never re-submits the last user turn: the transcript already
// carries it, and ContinueTurn picks the round up from the checkpoint.
// Sessions without an unfinished round return a typed error instead of
// inventing work.
func (c *daemonSessionControl) resumeFromCheckpoint(ctrl turnResumer) error {
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
	return ctrl.ContinueTurn(context.Background())
}

// resumeCtrl is the shared resolve step of Resume/Retry: it returns a live
// Controller, restoring the durable session when it is not already loaded.
func (c *daemonSessionControl) resumeCtrl(sessionID string) (*control.Controller, error) {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return nil, err
	}
	return ctrl, nil
}

func (c *daemonSessionControl) Resume(sessionID, requestID string) error {
	ctrl, err := c.resumeCtrl(sessionID)
	if err != nil {
		return err
	}
	return c.resumeFromCheckpoint(ctrl)
}

func (c *daemonSessionControl) Retry(sessionID, requestID string) error {
	ctrl, err := c.resumeCtrl(sessionID)
	if err != nil {
		return err
	}
	return c.retryFromFailure(ctrl)
}

// retryFromFailure allows an in-place retry only when the session's durable
// failure record is classified retryable_known. outcome_unknown, blocked_policy,
// blocked_dependency, failed_known, and the absence of a failure checkpoint are
// all explicit typed refusals — the host never guesses a redo of external work.
func (c *daemonSessionControl) retryFromFailure(ctrl turnResumer) error {
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

// forkStablePath resolves the deterministic fork branch path for
// (parentSessionID, requestID) inside dir. The path is derived from
// agent.ForkStableID so a replayed or concurrent fork request resolves to the
// same branch before any fork executes.
func forkStablePath(dir, parentSessionID, requestID string) string {
	return filepath.Join(dir, agent.ForkStableID(parentSessionID, requestID)+".jsonl")
}

// forkExecuter is the narrow fork surface forkSession needs. It is satisfied
// by *control.Controller and by test fakes, so the fork idempotency + crash
// recovery flow is unit-testable without booting a real model.
type forkExecuter interface {
	SessionPath() string
	Turn() int
	ForkSessionAt(turn int, name, forkPath string) (string, error)
}

// forkSession runs one idempotent fork: it claims the (requestID -> fork ID)
// binding FIRST (receipt), then forks the parent session into the
// deterministic branch path, inherits the parent's Assistant/workspace/purpose
// metadata onto the branch, and commits the receipt. A crash at any point
// between receipt, fork and metadata inheritance is recovered by the next call
// with the same requestID — it resolves the receipt and never forks twice.
// The fork branch's Session ID is the deterministic ForkStableID, so the
// returned ID is stable across hosts and restarts.
func (c *daemonSessionControl) forkSession(sessionID, requestID string) (string, error) {
	ctrl, err := c.requireCtrl(sessionID)
	if err != nil {
		return "", err
	}
	return c.forkSessionWith(sessionID, requestID, ctrl)
}

func (c *daemonSessionControl) forkSessionWith(sessionID, requestID string, ctrl forkExecuter) (string, error) {
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
			return "", err // includes *agent.SessionReceiptConflictError for typed handling
		}
		if rec.State.AtLeast(agent.ReceiptStarted) {
			return rec.SessionID, nil // already forked by a prior host/crash
		}
	}

	// Fork into the deterministic path (idempotent file; a prior crash that
	// already wrote it is reused, not forked twice).
	if _, err := ctrl.ForkSessionAt(ctrl.Turn(), "", forkPath); err != nil {
		return "", err
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

func (c *daemonSessionControl) Fork(sessionID, requestID string) (string, error) {
	return c.forkSession(sessionID, requestID)
}

func (c *daemonSessionControl) PendingInteractions(sessionID string) ([]sessiontool.SessionInteraction, error) {
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
		return []sessiontool.SessionInteraction{{Kind: "ask", ID: pi.Ask.ID, Questions: pi.Ask.Questions}}, nil
	case control.PendingInteractionApproval:
		return []sessiontool.SessionInteraction{{Kind: "approval", ID: pi.Approval.ID}}, nil
	default:
		return nil, nil
	}
}

// Close shuts down every live headless controller. It is idempotent.
func (c *daemonSessionControl) Close() error {
	c.mu.Lock()
	live := c.live
	c.live = map[string]*control.Controller{}
	c.mu.Unlock()
	for _, ctrl := range live {
		ctrl.Close()
	}
	return nil
}
