package assistantdaemon

import (
	"context"
	"errors"
	"os"
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
	"workground2/internal/tool/assistanttool"
	"workground2/internal/tool/sessiontool"
)

// daemonSupervisorSessionControl adapts the headless daemonSessionControl to
// the assistant.SessionControl mirror the supervisor executor consumes. The
// two shapes are kept in sync by the compile-time witness below.
type daemonSupervisorSessionControl struct {
	inner *daemonSessionControl
}

var _ assistant.SessionControl = (*daemonSupervisorSessionControl)(nil)

func (c *daemonSupervisorSessionControl) Steer(sessionID, text, requestID string) error {
	return c.inner.Steer(sessionID, text, requestID)
}
func (c *daemonSupervisorSessionControl) AnswerQuestion(sessionID, questionID string, answers []event.AskAnswer, requestID string) error {
	return c.inner.AnswerQuestion(sessionID, questionID, answers, requestID)
}
func (c *daemonSupervisorSessionControl) Cancel(sessionID, requestID string) error {
	return c.inner.Cancel(sessionID, requestID)
}
func (c *daemonSupervisorSessionControl) Resume(sessionID, requestID string) error {
	return c.inner.Resume(sessionID, requestID)
}
func (c *daemonSupervisorSessionControl) Retry(sessionID, requestID string) error {
	return c.inner.Retry(sessionID, requestID)
}
func (c *daemonSupervisorSessionControl) Fork(sessionID, requestID string) (string, error) {
	return c.inner.Fork(sessionID, requestID)
}
func (c *daemonSupervisorSessionControl) Create(req assistant.SessionCreateRequest) (string, error) {
	return c.inner.Create(sessiontool.SessionCreateRequest{
		Title: req.Title, Prompt: req.Prompt, OwnerID: req.OwnerID, ParentID: req.ParentID,
		Purpose: agent.SessionPurpose(req.Purpose), Workspace: req.Workspace, RequestID: req.RequestID,
	})
}
func (c *daemonSupervisorSessionControl) PendingInteractions(sessionID string) ([]assistant.SessionInteraction, error) {
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

// daemonSupervisorHost adapts the headless daemon session machinery to the
// assistant.SupervisorHost contract: the unique supervisor Session is created
// atomically through the Session subsystem, managed Sessions are listed from
// the shared subsystem, and each supervisor turn runs on the Session's live
// headless Controller — the same core semantics as the desktop host.
type daemonSupervisorHost struct {
	r *Runtime
	// ensureMu serializes supervisor Session creation. boot.Build loads the
	// shared config, which is not safe to run concurrently on Windows; the
	// double-checked lock also keeps concurrent ensure calls to one create.
	ensureMu sync.Mutex
}

var _ assistant.SupervisorHost = (*daemonSupervisorHost)(nil)

// daemonSupervisorDirs returns the session directories a supervisor Session
// may live in: the shared Session subsystem dir, the desktop global workspace
// session dir, plus every assistant workspace session dir.
func daemonSupervisorDirs(r *Runtime) []string {
	dirs := []string{config.SessionDir()}
	if root := globalWorkspaceRootDir(); root != "" {
		if dir := config.ProjectSessionDir(root); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	if r != nil && r.store != nil {
		if assistants, err := r.store.List(); err == nil {
			for _, a := range assistants {
				if a.Scope == assistant.ScopeWorkspace && strings.TrimSpace(a.WorkspaceRoot) != "" {
					if dir := config.ProjectSessionDir(strings.TrimSpace(a.WorkspaceRoot)); dir != "" {
						dirs = append(dirs, dir)
					}
				}
			}
		}
	}
	return dirs
}

// globalWorkspaceRootDir mirrors the desktop's global workspace root so the
// daemon finds supervisor Sessions the desktop created there. It must not
// import the desktop package; the path rule is duplicated deliberately.
func globalWorkspaceRootDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "WorkGround2", "global-workspace")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".WorkGround2", "global-workspace")
}

func (h *daemonSupervisorHost) supervisorSessionDirs() []string {
	return daemonSupervisorDirs(h.r)
}

func (h *daemonSupervisorHost) FindSupervisorSession(assistantID string) (assistant.SupervisorSessionRef, bool) {
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
// Purpose=supervisor Session and its headless Controller. The deterministic
// stable file (O_EXCL) makes concurrent or replayed creates resolve to the same
// path; the BranchMeta purpose stamp is idempotent. No work prompt is
// submitted: the first supervisor context turn is driven by the executor.
func (h *daemonSupervisorHost) EnsureSupervisorSession(a assistant.Assistant) (assistant.SupervisorSessionRef, error) {
	if h.r == nil || h.r.sessionControl == nil {
		return assistant.SupervisorSessionRef{}, errors.New("assistant daemon supervisor host is unavailable")
	}
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
	workspace := ""
	if a.Scope == assistant.ScopeWorkspace {
		workspace = strings.TrimSpace(a.WorkspaceRoot)
	}
	sessionDir := config.SessionDir()
	if workspace != "" {
		sessionDir = config.ProjectSessionDir(workspace)
	}
	if strings.TrimSpace(sessionDir) == "" {
		return assistant.SupervisorSessionRef{}, errors.New("assistant supervisor session dir is unavailable")
	}
	stablePath, _, err := agent.CreateStableSessionFile(sessionDir, a.ID, "supervisor")
	if err != nil {
		return assistant.SupervisorSessionRef{}, err
	}
	ctrl, err := boot.Build(context.Background(), boot.Options{
		Model: h.r.opts.Model, RequireKey: true,
		Sink:            event.FuncSink(func(event.Event) {}),
		Stderr:          h.r.opts.Stderr,
		WorkspaceRoot:   workspace,
		SessionDir:      sessionDir,
		SessionKind:     agent.SessionKindAssistant,
		ExtraTools:      daemonSupervisorTools(h.r, a.ID),
		ApprovalTimeout: 2 * time.Second,
		WorkGate:        h.r.store.WorkGate(),
	})
	if err != nil {
		return assistant.SupervisorSessionRef{}, err
	}
	ctrl.SetSessionPath(stablePath)
	meta, _ := agent.EnsureBranchMeta(stablePath)
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = a.ID
	meta.SessionSource = agent.SessionSourceAssist
	meta.Purpose = agent.PurposeSupervisor
	meta.ToolApprovalMode = control.ToolApprovalAuto
	if err := agent.SaveBranchMetaPreserveUpdated(stablePath, meta); err != nil {
		ctrl.Close()
		return assistant.SupervisorSessionRef{}, err
	}
	id := agent.BranchID(stablePath)
	h.r.sessionControl.mu.Lock()
	h.r.sessionControl.live[id] = ctrl
	h.r.sessionControl.mu.Unlock()
	return assistant.SupervisorSessionRef{ID: id, Path: stablePath}, nil
}

func (h *daemonSupervisorHost) ManagedSessions(assistantID string) []assistant.ManagedSessionSummary {
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

const daemonSupervisorTurnPollInterval = 500 * time.Millisecond

// RunSupervisorTurn runs one real Controller turn on the supervisor Session: it
// restores the headless Controller (a restart resumes the same Session file),
// submits the bounded context prompt, and waits (bounded by budget) for the
// turn to settle. The turn's model history, tool calls, pending interaction and
// checkpoint all land in that Session file.
func (h *daemonSupervisorHost) RunSupervisorTurn(ref assistant.SupervisorSessionRef, prompt string, budget time.Duration) assistant.SupervisorTurnOutcome {
	if h.r == nil || h.r.sessionControl == nil {
		return assistant.SupervisorTurnOutcome{Err: errors.New("assistant daemon supervisor host is unavailable")}
	}
	ctrl, err := h.r.sessionControl.requireCtrl(ref.ID)
	if err != nil {
		return assistant.SupervisorTurnOutcome{Err: err}
	}
	if ctrl.Running() {
		// A prior turn is still in flight (possibly submitted outside the
		// loop): report the current transcript length as the checkpoint
		// baseline so a settle can confirm durable growth.
		return assistant.SupervisorTurnOutcome{Running: true, HistoryLen: daemonSessionHistoryLen(ref.Path)}
	}
	if _, pending := ctrl.PendingInteraction(); pending {
		return assistant.SupervisorTurnOutcome{Pending: true}
	}
	// The durable transcript length right before the submission is the
	// checkpoint baseline: a settle that shows no growth proves the
	// submission never durably landed.
	beforeLen := daemonSessionHistoryLen(ref.Path)
	ctrl.SubmitDisplay(prompt, prompt)
	deadline := time.Now().Add(budget)
	for {
		if _, pending := ctrl.PendingInteraction(); pending {
			out := assistant.SupervisorTurnOutcome{Pending: true}
			out.HistoryLen = daemonSessionHistoryLen(ref.Path)
			return out
		}
		if !ctrl.Running() {
			out := daemonReadTurnOutcome(ref.Path)
			out.HistoryLen = daemonSessionHistoryLen(ref.Path)
			return out
		}
		if time.Now().After(deadline) {
			return assistant.SupervisorTurnOutcome{Running: true, HistoryLen: beforeLen}
		}
		time.Sleep(daemonSupervisorTurnPollInterval)
	}
}

// SettleSupervisorTurn reads the current state of a previously submitted
// supervisor turn WITHOUT submitting a new prompt: a still-running turn reports
// Running, a pending interaction reports Pending, otherwise the finished
// outcome is read from the durable Session transcript. It is the restart-safe
// continuation of a checkpointed turn.
func (h *daemonSupervisorHost) SettleSupervisorTurn(ref assistant.SupervisorSessionRef) assistant.SupervisorTurnOutcome {
	if h.r == nil || h.r.sessionControl == nil {
		return assistant.SupervisorTurnOutcome{Err: errors.New("assistant daemon supervisor host is unavailable")}
	}
	ctrl, err := h.r.sessionControl.requireCtrl(ref.ID)
	if err != nil {
		return assistant.SupervisorTurnOutcome{Err: err}
	}
	if ctrl.Running() {
		return assistant.SupervisorTurnOutcome{Running: true}
	}
	if _, pending := ctrl.PendingInteraction(); pending {
		return assistant.SupervisorTurnOutcome{Pending: true}
	}
	out := daemonReadTurnOutcome(ref.Path)
	out.HistoryLen = daemonSessionHistoryLen(ref.Path)
	return out
}

// SupervisorHistoryLen returns the durable supervisor Session transcript length
// (the number of persisted messages) as a read-only probe. The executor uses it
// as the turn-checkpoint baseline BEFORE a submission; unlike the Running-outcome
// baseline, a probe failure is surfaced so the caller never degrades into an
// unsafe zero baseline on a Session that already has history.
func (h *daemonSupervisorHost) SupervisorHistoryLen(ref assistant.SupervisorSessionRef) (int, error) {
	sess, err := agent.LoadSession(ref.Path)
	if err != nil {
		return 0, err
	}
	return len(sess.Snapshot()), nil
}

// daemonSessionHistoryLen returns the durable Session transcript length (the
// number of persisted messages) of one Session file, or 0 when it cannot be
// read.
func daemonSessionHistoryLen(path string) int {
	sess, err := agent.LoadSession(path)
	if err != nil {
		return 0
	}
	return len(sess.Snapshot())
}

// daemonReadTurnOutcome extracts the final assistant message and tool names of
// the just-finished turn from the durable Session transcript.
func daemonReadTurnOutcome(sessionPath string) assistant.SupervisorTurnOutcome {
	sess, err := agent.LoadSession(sessionPath)
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

// daemonSupervisorTools is the supervisor Session's bounded, read-only
// observation surface: session/schedule/memory/policy/project query tools only.
// The supervisor never acts directly; the loop routes its bounded decision.
func daemonSupervisorTools(r *Runtime, assistantID string) []tool.Tool {
	store := r.store
	queryDirs := daemonSupervisorDirs(r)
	return []tool.Tool{
		sessiontool.NewSessionListToolDirs(queryDirs),
		sessiontool.NewSessionStatusToolDirs(queryDirs),
		sessiontool.NewSessionReadToolDirs(queryDirs),
		assistanttool.NewScheduleListTool(store, assistantID),
		assistanttool.NewScheduleGetTool(store, assistantID),
		assistanttool.NewMemorySearchTool(store, assistantID),
		assistanttool.NewPolicyGetTool(store, assistantID),
		assistanttool.NewProjectStatusTool(store, assistantID),
		assistanttool.NewProjectConstraintsGetTool(store, assistantID),
	}
}

// daemonTrialSessionStatus derives a trial fork session's experiment status
// from the Session subsystem: completed -> done, failed/cancelled -> failed
// (terminal, can never win), any other located state -> running.
func (r *Runtime) daemonTrialSessionStatus(sessionID string) (string, bool) {
	dirs := daemonSupervisorDirs(r)
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		sessions, err := agent.ListSessions(dir)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if agent.BranchID(s.Path) != sessionID {
				continue
			}
			meta, ok, err := agent.LoadBranchMeta(s.Path)
			if err != nil || !ok {
				return "", false
			}
			switch agent.DeriveSessionStatus(meta) {
			case agent.SessionStatusCompleted:
				return assistant.TrialStatusDone, true
			case agent.SessionStatusFailed, agent.SessionStatusCancelled:
				return assistant.TrialStatusFailed, true
			default:
				return assistant.TrialStatusRunning, true
			}
		}
	}
	return "", false
}
