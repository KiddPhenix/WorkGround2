package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
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

// supervisorSessionControl adapts the desktop sessiontool.SessionControl to the
// assistant.SessionControl mirror the supervisor executor consumes. The two
// shapes are kept in sync by the compile-time witness below.
type supervisorSessionControl struct {
	inner sessiontool.SessionControl
}

var _ assistant.SessionControl = supervisorSessionControl{}

func (c supervisorSessionControl) Steer(sessionID, text, requestID string) error {
	return c.inner.Steer(sessionID, text, requestID)
}

func (c supervisorSessionControl) AnswerQuestion(sessionID, questionID string, answers []event.AskAnswer, requestID string) error {
	return c.inner.AnswerQuestion(sessionID, questionID, answers, requestID)
}

func (c supervisorSessionControl) Cancel(sessionID, requestID string) error {
	return c.inner.Cancel(sessionID, requestID)
}

func (c supervisorSessionControl) Resume(sessionID, requestID string) error {
	return c.inner.Resume(sessionID, requestID)
}

func (c supervisorSessionControl) Retry(sessionID, requestID string) error {
	return c.inner.Retry(sessionID, requestID)
}

func (c supervisorSessionControl) Fork(sessionID, requestID string) (string, error) {
	return c.inner.Fork(sessionID, requestID)
}

func (c supervisorSessionControl) Create(req assistant.SessionCreateRequest) (string, error) {
	return c.inner.Create(sessiontool.SessionCreateRequest{
		Title: req.Title, Prompt: req.Prompt, OwnerID: req.OwnerID, ParentID: req.ParentID,
		Purpose: agent.SessionPurpose(req.Purpose), Workspace: req.Workspace, RequestID: req.RequestID,
	})
}

func (c supervisorSessionControl) PendingInteractions(sessionID string) ([]assistant.SessionInteraction, error) {
	items, err := c.inner.PendingInteractions(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.SessionInteraction, 0, len(items))
	for _, it := range items {
		out = append(out, assistant.SessionInteraction{Kind: it.Kind, ID: it.ID, Questions: it.Questions, DueAt: it.DueAt})
	}
	return out, nil
}

// desktopSupervisorHost adapts the desktop session machinery (tabs + the app
// session registry) to the assistant.SupervisorHost contract: it guarantees the
// unique supervisor Session through the Session subsystem (deterministic file +
// BranchMeta stamp), lists managed Sessions from the shared Session subsystem,
// and runs each supervisor turn on the Session's live Controller.
type desktopSupervisorHost struct {
	r *AssistantRuntime
	// ensureMu serializes supervisor Session creation so concurrent ticks or
	// callers never build two controllers (or race the shared config) for the
	// same assistant.
	ensureMu sync.Mutex
}

var _ assistant.SupervisorHost = (*desktopSupervisorHost)(nil)

// supervisorSessionDirs returns the session directories a supervisor Session
// may live in: the shared Session subsystem dir, the desktop global workspace
// session dir, plus every project workspace session dir. It is the union the
// desktop and daemon create sessions in, so a session created by either host is
// found by both.
func (h *desktopSupervisorHost) supervisorSessionDirs() []string {
	dirs := []string{config.SessionDir()}
	if root := globalWorkspaceRoot(); root != "" {
		if dir := desktopSessionDir(root); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	for _, p := range loadProjectsFile().Projects {
		if dir := config.ProjectSessionDir(p.Root); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func (h *desktopSupervisorHost) FindSupervisorSession(assistantID string) (assistant.SupervisorSessionRef, bool) {
	for _, dir := range h.supervisorSessionDirs() {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if s, ok := agent.FindSupervisorSessionByMeta(dir, assistantID); ok {
			return assistant.SupervisorSessionRef{ID: agent.BranchID(s.Path), Path: s.Path}, true
		}
	}
	return assistant.SupervisorSessionRef{}, false
}

// EnsureSupervisorSession atomically creates the assistant's unique
// Purpose=supervisor Session and opens its tab. Atomicity lives in the Session
// subsystem: the deterministic stable file (O_EXCL) makes concurrent or
// replayed creates resolve to the same path, and the BranchMeta purpose stamp
// is idempotent. The supervisor Session is deliberately created WITHOUT a work
// prompt: its first real turn is the first supervisor context submitted by the
// executor.
func (h *desktopSupervisorHost) EnsureSupervisorSession(a assistant.Assistant) (assistant.SupervisorSessionRef, error) {
	if ref, ok := h.FindSupervisorSession(a.ID); ok {
		return ref, nil
	}
	h.ensureMu.Lock()
	defer h.ensureMu.Unlock()
	// Double-check under the lock: a concurrent creator may have finished while
	// we waited; reuse its Session instead of building a second one.
	if ref, ok := h.FindSupervisorSession(a.ID); ok {
		return ref, nil
	}
	// Create in the desktop's own session convention (the same dir the app's
	// managed assistant sessions live in) so the sidebar lists it, while the
	// shared dir is searched too.
	dir := desktopSessionDir(globalWorkspaceRoot())
	if a.Scope == assistant.ScopeWorkspace {
		dir = config.ProjectSessionDir(strings.TrimSpace(a.WorkspaceRoot))
	}
	if strings.TrimSpace(dir) == "" {
		return assistant.SupervisorSessionRef{}, errors.New("assistant supervisor session dir is unavailable")
	}
	stablePath, _, err := agent.CreateStableSessionFile(dir, a.ID, "supervisor")
	if err != nil {
		return assistant.SupervisorSessionRef{}, err
	}
	// Stamp the durable identity BEFORE the tab builds so the controller is
	// assembled with the supervisor's read-only tool surface.
	meta, err := agent.EnsureBranchMeta(stablePath)
	if err != nil {
		return assistant.SupervisorSessionRef{}, err
	}
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = a.ID
	meta.SessionSource = agent.SessionSourceAssist
	meta.Purpose = agent.PurposeSupervisor
	if err := agent.SaveBranchMetaPreserveUpdated(stablePath, meta); err != nil {
		return assistant.SupervisorSessionRef{}, err
	}
	run := assistant.Run{
		AssistantID: a.ID, Prompt: a.Mission, Mission: a.Mission,
		Scope: a.Scope, WorkspaceRoot: a.WorkspaceRoot,
	}
	if _, err := h.r.app.ensureAssistantBackgroundTab(run, stablePath); err != nil {
		return assistant.SupervisorSessionRef{}, err
	}
	return assistant.SupervisorSessionRef{ID: agent.BranchID(stablePath), Path: stablePath}, nil
}

func (h *desktopSupervisorHost) ManagedSessions(assistantID string) []assistant.ManagedSessionSummary {
	seen := map[string]struct{}{}
	var out []assistant.ManagedSessionSummary
	for _, dir := range h.supervisorSessionDirs() {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		sessions, err := agent.ListSessionsByOwnerByMeta(dir, assistantID)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if s.Purpose != agent.PurposeManaged {
				continue
			}
			id := agent.BranchID(s.Path)
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			status := string(agent.SessionStatusIdle)
			if meta, ok, err := agent.LoadBranchMeta(s.Path); err == nil && ok {
				status = string(agent.DeriveSessionStatus(meta))
			}
			out = append(out, assistant.ManagedSessionSummary{
				ID: id, Path: s.Path, Title: s.CustomTitle, Preview: s.Preview,
				Status: status, Turns: s.Turns,
			})
		}
	}
	return out
}

// supervisorTurnPollInterval is how often RunSupervisorTurn re-checks the live
// controller while a supervisor turn is in flight.
const supervisorTurnPollInterval = 500 * time.Millisecond

// RunSupervisorTurn runs one real Controller turn on the supervisor Session: it
// restores the Session's tab (a restart resumes the same Session file), waits
// for the controller, submits the bounded context prompt, and waits (bounded by
// budget) for the turn to settle. The turn's model history, tool calls, pending
// interaction and checkpoint all land in that Session file; the outcome reports
// the final assistant message and any pending interaction.
func (h *desktopSupervisorHost) RunSupervisorTurn(ref assistant.SupervisorSessionRef, prompt string, budget time.Duration) assistant.SupervisorTurnOutcome {
	if h.r == nil || h.r.app == nil {
		return assistant.SupervisorTurnOutcome{Err: errors.New("assistant supervisor host is unavailable")}
	}
	tab, err := h.r.app.ensureTabForSessionPath(ref.Path)
	if err != nil {
		return assistant.SupervisorTurnOutcome{Err: err}
	}
	ctrl, err := h.waitController(tab)
	if err != nil {
		return assistant.SupervisorTurnOutcome{Err: err}
	}
	if ctrl.Running() {
		// A prior turn is still in flight (possibly submitted outside the
		// loop): report the current transcript length as the checkpoint
		// baseline so a settle can confirm durable growth.
		return assistant.SupervisorTurnOutcome{Running: true, HistoryLen: sessionHistoryLen(ref.Path)}
	}
	if _, pending := ctrl.PendingInteraction(); pending {
		return assistant.SupervisorTurnOutcome{Pending: true} // waiting on the user
	}
	// The durable transcript length right before the submission is the
	// checkpoint baseline: a settle that shows no growth proves the
	// submission never durably landed.
	beforeLen := sessionHistoryLen(ref.Path)
	if err := h.r.app.SubmitToTab(tab.ID, prompt); err != nil {
		return assistant.SupervisorTurnOutcome{Err: err}
	}
	return h.waitTurn(tab, ref, budget, beforeLen)
}

// SettleSupervisorTurn reads the current state of a previously submitted
// supervisor turn WITHOUT submitting a new prompt: a still-running turn reports
// Running, a pending interaction reports Pending, otherwise the finished
// outcome is read from the durable Session transcript. It is the restart-safe
// continuation of a checkpointed turn.
func (h *desktopSupervisorHost) SettleSupervisorTurn(ref assistant.SupervisorSessionRef) assistant.SupervisorTurnOutcome {
	if h.r == nil || h.r.app == nil {
		return assistant.SupervisorTurnOutcome{Err: errors.New("assistant supervisor host is unavailable")}
	}
	tab, err := h.r.app.ensureTabForSessionPath(ref.Path)
	if err != nil {
		return assistant.SupervisorTurnOutcome{Err: err}
	}
	ctrl, err := h.waitController(tab)
	if err != nil {
		return assistant.SupervisorTurnOutcome{Err: err}
	}
	if ctrl.Running() {
		return assistant.SupervisorTurnOutcome{Running: true}
	}
	if _, pending := ctrl.PendingInteraction(); pending {
		return assistant.SupervisorTurnOutcome{Pending: true}
	}
	out := h.readOutcome(ref)
	out.HistoryLen = sessionHistoryLen(ref.Path)
	return out
}

// SupervisorHistoryLen returns the durable supervisor Session transcript length
// (the number of persisted messages) as a read-only probe. The executor uses it
// as the turn-checkpoint baseline BEFORE a submission; unlike the Running-outcome
// baseline, a probe failure is surfaced so the caller never degrades into an
// unsafe zero baseline on a Session that already has history.
func (h *desktopSupervisorHost) SupervisorHistoryLen(ref assistant.SupervisorSessionRef) (int, error) {
	sess, err := agent.LoadSession(ref.Path)
	if err != nil {
		return 0, err
	}
	return len(sess.Snapshot()), nil
}

// sessionHistoryLen returns the durable Session transcript length (the number
// of persisted messages) of one Session file, or 0 when it cannot be read.
func sessionHistoryLen(path string) int {
	sess, err := agent.LoadSession(path)
	if err != nil {
		return 0
	}
	return len(sess.Snapshot())
}

// waitController polls until the restored supervisor tab's controller is ready.
func (h *desktopSupervisorHost) waitController(tab *WorkspaceTab) (control.SessionAPI, error) {
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, ctrl := h.r.app.tabAndCtrlByID(tab.ID)
		if ctrl != nil {
			return ctrl, nil
		}
		if tab.Ready && strings.TrimSpace(tab.StartupErr) != "" {
			return nil, fmt.Errorf("assistant supervisor session startup: %s", tab.StartupErr)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("assistant supervisor session startup timed out")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitTurn polls the tab's live controller until the submitted turn settles:
// the supervisor asked the user (pending), the turn finished, or the budget
// elapsed (still running). The controller is re-fetched every poll so a model
// rebuild mid-turn cannot leave us polling a stale instance; the final decision
// is read from the durable Session file — exactly what was persisted. The
// Running outcome carries the pre-submission transcript length as the
// checkpoint baseline.
func (h *desktopSupervisorHost) waitTurn(tab *WorkspaceTab, ref assistant.SupervisorSessionRef, budget time.Duration, beforeLen int) assistant.SupervisorTurnOutcome {
	deadline := time.Now().Add(budget)
	for {
		_, ctrl := h.r.app.tabAndCtrlByID(tab.ID)
		if ctrl != nil {
			if _, pending := ctrl.PendingInteraction(); pending {
				out := assistant.SupervisorTurnOutcome{Pending: true}
				out.HistoryLen = sessionHistoryLen(ref.Path)
				return out
			}
			if !ctrl.Running() {
				out := h.readOutcome(ref)
				out.HistoryLen = sessionHistoryLen(ref.Path)
				return out
			}
		}
		if time.Now().After(deadline) {
			return assistant.SupervisorTurnOutcome{Running: true, HistoryLen: beforeLen}
		}
		time.Sleep(supervisorTurnPollInterval)
	}
}

// readOutcome extracts the final assistant message and the tool names of the
// just-finished turn from the durable Session transcript.
func (h *desktopSupervisorHost) readOutcome(ref assistant.SupervisorSessionRef) assistant.SupervisorTurnOutcome {
	sess, err := agent.LoadSession(ref.Path)
	if err != nil {
		return assistant.SupervisorTurnOutcome{Err: err}
	}
	msgs := sess.Snapshot()
	var text string
	var toolNames []string
	seenTools := map[string]struct{}{}
	for _, m := range msgs {
		if m.Role != provider.RoleTool {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		if _, dup := seenTools[name]; !dup {
			seenTools[name] = struct{}{}
			toolNames = append(toolNames, name)
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == provider.RoleAssistant && strings.TrimSpace(msgs[i].Content) != "" {
			text = msgs[i].Content
			break
		}
	}
	return assistant.SupervisorTurnOutcome{Text: text, ToolNames: toolNames}
}

// supervisorTab reports whether a tab's durable session is an assistant's
// supervisor Session (used to bound its tool surface to read-only).
func supervisorTab(tab *WorkspaceTab) bool {
	if tab == nil {
		return false
	}
	path := strings.TrimSpace(tab.currentSessionPath())
	if path == "" {
		return false
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	return err == nil && ok && agent.IsSupervisorSession(meta)
}

// filterReadOnlyTools keeps only read-only tools, so the supervisor Session's
// controller can observe (session_list/status, schedule_get, memory_search,
// policy_get, project status) but never acts directly — the loop routes the
// bounded decision and applies fences before any side effect.
func filterReadOnlyTools(tools []tool.Tool) []tool.Tool {
	if len(tools) == 0 {
		return tools
	}
	out := make([]tool.Tool, 0, len(tools))
	for _, t := range tools {
		if t.ReadOnly() {
			out = append(out, t)
		}
	}
	return out
}
