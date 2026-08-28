package main

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/config"
	"workground2/internal/event"
	"workground2/internal/tool/sessiontool"
)

// ── 受管 Session 视图 ─────────────────────────────────────────────────────
//
// AssistantManagedSessionView is the read-only UI projection of one
// Purpose=managed Session. Every field is derived from the authoritative
// Session subsystem (session file + BranchMeta); the UI never writes execution
// state here.

type AssistantManagedSessionView struct {
	ID               string    `json:"id"`
	Path             string    `json:"path"`
	Title            string    `json:"title"`
	Preview          string    `json:"preview"`
	Status           string    `json:"status"`
	Turns            int       `json:"turns"`
	OwnerID          string    `json:"owner_id"`
	Purpose          string    `json:"purpose"`
	ResponsibilityID string    `json:"responsibility_id,omitempty"`
	WorkspaceRoot    string    `json:"workspace_root,omitempty"`
	UpdatedAt        time.Time `json:"updated_at" ts_type:"string"`
}

// AssistantSessionControlResult is the unified write-outcome for every
// user-initiated Session control action. The vocabulary mirrors the design
// section 7: accepted | already_applied | stale | retryable_error | invalid |
// blocked_by_policy. The caller replays with the same request_id after a lost
// response; the backend dedups by request_id so a replay never re-executes.
type AssistantSessionControlResult struct {
	Outcome       string    `json:"outcome"`
	SessionID     string    `json:"session_id,omitempty"`
	SessionStatus string    `json:"session_status,omitempty"`
	Revision      int64     `json:"revision,omitempty"`
	NextHint      string    `json:"next_hint,omitempty"`
	Message       string    `json:"message,omitempty"`
	At            time.Time `json:"at" ts_type:"string"`
}

// AssistantSessionStatusView is the bounded status of one Session targeted by
// id, plus any pending interactions (questions/approvals) on it.
type AssistantSessionStatusView struct {
	ID           string                        `json:"id"`
	Path         string                        `json:"path"`
	Title        string                        `json:"title"`
	Status       string                        `json:"status"`
	Turns        int                           `json:"turns"`
	Purpose      string                        `json:"purpose"`
	Running      bool                          `json:"running"`
	UpdatedAt    time.Time                     `json:"updated_at" ts_type:"string"`
	Interactions []AssistantSessionInteraction `json:"interactions,omitempty"`
}

// AssistantSessionInteraction is the clean JSON projection of one pending
// interaction (ask/approval) on a Session. It converts the backend
// event.AskQuestion shape so the frontend contract stays stable and lowercase
// even though the underlying event types carry no json tags.
type AssistantSessionInteraction struct {
	Kind      string                 `json:"kind"`
	ID        string                 `json:"id"`
	Questions []AssistantAskQuestion `json:"questions,omitempty"`
	DueAt     time.Time              `json:"due_at" ts_type:"string"`
}

type AssistantAskQuestion struct {
	ID      string               `json:"id"`
	Header  string               `json:"header,omitempty"`
	Prompt  string               `json:"prompt"`
	Options []AssistantAskOption `json:"options,omitempty"`
	Multi   bool                 `json:"multi,omitempty"`
}

type AssistantAskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func assistantSessionInteractionView(item sessiontool.SessionInteraction) AssistantSessionInteraction {
	view := AssistantSessionInteraction{Kind: item.Kind, ID: item.ID, DueAt: item.DueAt}
	for _, q := range item.Questions {
		question := AssistantAskQuestion{ID: q.ID, Header: q.Header, Prompt: q.Prompt, Multi: q.Multi}
		for _, opt := range q.Options {
			question.Options = append(question.Options, AssistantAskOption{Label: opt.Label, Description: opt.Description})
		}
		view.Questions = append(view.Questions, question)
	}
	return view
}

// AssistantSteerRequest steers a running managed Session.
type AssistantSteerRequest struct {
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
	RequestID string `json:"requestId"`
}

// AssistantAnswerRequest answers one pending interaction on a Session.
type AssistantAnswerRequest struct {
	SessionID     string            `json:"sessionId"`
	InteractionID string            `json:"interactionId"`
	Answers       []event.AskAnswer `json:"answers"`
	RequestID     string            `json:"requestId"`
}

// AssistantSessionRequest carries the stable identity of one Session control
// action that only needs a session id + request_id (status/cancel/resume/fork).
type AssistantSessionRequest struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
}

func (a *App) assistantSessionControl() *appAssistantSessionControl {
	return &appAssistantSessionControl{app: a}
}

func sessionControlOutcome(sessionID, requestID string, err error, at time.Time) AssistantSessionControlResult {
	if err == nil {
		return AssistantSessionControlResult{
			Outcome: "accepted", SessionID: sessionID, At: at,
			NextHint: "accepted",
		}
	}
	switch {
	case errors.Is(err, agent.ErrOutcomeUnknown):
		return AssistantSessionControlResult{Outcome: "invalid", SessionID: sessionID, Message: err.Error(), At: at}
	case errors.Is(err, agent.ErrBlockedPolicy):
		return AssistantSessionControlResult{Outcome: "blocked_by_policy", SessionID: sessionID, Message: err.Error(), At: at}
	case errors.Is(err, agent.ErrBlockedDependency):
		return AssistantSessionControlResult{Outcome: "retryable_error", SessionID: sessionID, Message: err.Error(), At: at}
	case errors.Is(err, assistant.ErrLeaseLost):
		return AssistantSessionControlResult{Outcome: "stale", SessionID: sessionID, Message: err.Error(), At: at}
	case isInputConflict(err):
		return AssistantSessionControlResult{Outcome: "invalid", SessionID: sessionID, Message: err.Error(), At: at}
	case isAlreadyApplied(err):
		return AssistantSessionControlResult{Outcome: "already_applied", SessionID: sessionID, Message: err.Error(), At: at}
	default:
		return AssistantSessionControlResult{Outcome: "retryable_error", SessionID: sessionID, Message: err.Error(), At: at}
	}
}

// isAlreadyApplied recognizes the idempotent-replay outcomes: a receipt-driven
// Session control that was already committed by a prior identical request.
func isAlreadyApplied(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already applied") || strings.Contains(message, "resolves here")
}

func isInputConflict(err error) bool {
	var sessionConflict *agent.SessionReceiptConflictError
	var opConflict *agent.OpReceiptConflictError
	if errors.As(err, &sessionConflict) || errors.As(err, &opConflict) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "different input") ||
		strings.Contains(message, "different parent") ||
		strings.Contains(message, "different parameters") ||
		strings.Contains(message, "reused with") && strings.Contains(message, "conflict")
}

func invalidSessionAction(sessionID, requestID string, at time.Time) (AssistantSessionControlResult, bool) {
	if strings.TrimSpace(sessionID) == "" {
		return AssistantSessionControlResult{Outcome: "invalid", Message: "session id is required", At: at}, true
	}
	if strings.TrimSpace(requestID) == "" {
		return AssistantSessionControlResult{Outcome: "invalid", SessionID: sessionID, Message: "request id is required", At: at}, true
	}
	return AssistantSessionControlResult{}, false
}

var assistantSessionActionLocks sync.Map

func lockAssistantSessionAction(dir, key string) func() {
	value, _ := assistantSessionActionLocks.LoadOrStore(dir+"\x00"+key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// applyAssistantSessionAction gives the Desktop UI the same durable replay
// semantics as the session tools. Accepted outcomes are recorded under the
// Session directory; a retry with the same request and fingerprint returns the
// original target without executing fn again, while changed input is invalid.
func applyAssistantSessionAction(dir, operation, requestID, fingerprint string, fn func() (string, error)) (string, bool, error) {
	key := operation + "\x00" + strings.TrimSpace(requestID)
	unlock := lockAssistantSessionAction(dir, key)
	defer unlock()
	if rec, ok, err := agent.ReadOpReceipt(dir, key); err != nil {
		return "", false, err
	} else if ok {
		if rec.Fingerprint != "" && rec.Fingerprint != fingerprint {
			return "", false, &agent.OpReceiptConflictError{Key: key, Fingerprint: rec.Fingerprint}
		}
		return rec.SessionID, true, nil
	}
	sessionID, err := fn()
	if err != nil {
		return "", false, err
	}
	rec, recorded, err := agent.WriteOpReceipt(dir, key, agent.OpReceipt{
		Status: "accepted", SessionID: sessionID, Fingerprint: fingerprint,
	})
	if err != nil {
		return "", false, err
	}
	if !recorded {
		return rec.SessionID, true, nil
	}
	return sessionID, false, nil
}

func (a *App) runAssistantSessionAction(operation, sessionID, requestID, fingerprint string, at time.Time, fn func() (string, error)) AssistantSessionControlResult {
	resultID, replayed, err := applyAssistantSessionAction(config.SessionDir(), operation, requestID, fingerprint, fn)
	result := sessionControlOutcome(sessionID, requestID, err, at)
	if err != nil {
		return result
	}
	if resultID != "" {
		result.SessionID = resultID
	}
	if replayed {
		result.Outcome = "already_applied"
	}
	if path, pathErr := a.assistantSessionControl().sessionPathByID(result.SessionID); pathErr == nil {
		if meta, ok, metaErr := agent.LoadBranchMeta(path); metaErr == nil && ok {
			result.SessionStatus = string(agent.DeriveSessionStatus(meta))
			result.Revision = meta.Revision
		}
	}
	return result
}

// AssistantManagedSessions lists the assistant's Purpose=managed Sessions with
// their derived status, owner, purpose, bound responsibility, workspace and
// update time. It reads through the same supervisor host scan the supervisor
// turns use, so the UI and the Assistant see one execution view.
func (a *App) AssistantManagedSessions(assistantID string) ([]AssistantManagedSessionView, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return nil, err
	}
	if service.executor == nil {
		return nil, errors.New("assistant supervisor executor is not started")
	}
	host := service.executor.Host()
	if host == nil {
		return nil, errors.New("assistant supervisor host is not started")
	}
	views := make([]AssistantManagedSessionView, 0)
	for _, s := range host.ManagedSessions(assistantID) {
		view := AssistantManagedSessionView{
			ID: s.ID, Path: s.Path, Title: s.Title, Preview: s.Preview,
			Status: s.Status, Turns: s.Turns, OwnerID: assistantID, Purpose: "managed",
		}
		if meta, ok, err := agent.LoadBranchMeta(s.Path); err == nil && ok {
			view.Purpose = string(meta.Purpose)
			view.ResponsibilityID = meta.ResponsibilityID
			view.WorkspaceRoot = meta.WorkspaceRoot
			if !meta.UpdatedAt.IsZero() {
				view.UpdatedAt = meta.UpdatedAt
			}
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Status != views[j].Status {
			return statusRank(views[i].Status) < statusRank(views[j].Status)
		}
		return views[i].UpdatedAt.After(views[j].UpdatedAt)
	})
	return views, nil
}

func statusRank(status string) int {
	switch status {
	case "running":
		return 0
	case "waiting":
		return 1
	case "retrying":
		return 2
	case "failed":
		return 3
	case "completed":
		return 4
	default:
		return 5
	}
}

// AssistantSessionStatus returns the bounded status of one Session, including
// its pending interactions. It never guesses: a Session the subsystem cannot
// locate is an explicit not-found error.
func (a *App) AssistantSessionStatus(sessionID string) (AssistantSessionStatusView, error) {
	ctrl := a.assistantSessionControl()
	tab, api := a.sessionCtrlByID(sessionID)
	view := AssistantSessionStatusView{ID: sessionID}
	if tab != nil && api != nil {
		view.Running = api.Running()
		view.Path = strings.TrimSpace(api.SessionPath())
	}
	if view.Path == "" {
		path, err := ctrl.sessionPathByID(sessionID)
		if err != nil {
			return AssistantSessionStatusView{}, err
		}
		view.Path = path
	}
	if meta, ok, err := agent.LoadBranchMeta(view.Path); err == nil && ok {
		view.Title = meta.CustomTitle
		view.Status = string(agent.DeriveSessionStatus(meta))
		view.Purpose = string(meta.Purpose)
		view.Turns = meta.Turns
		if !meta.UpdatedAt.IsZero() {
			view.UpdatedAt = meta.UpdatedAt
		}
	}
	interactions, err := ctrl.PendingInteractions(sessionID)
	if err != nil {
		return AssistantSessionStatusView{}, err
	}
	for _, item := range interactions {
		view.Interactions = append(view.Interactions, assistantSessionInteractionView(item))
	}
	return view, nil
}

// AssistantSessionSteer inserts guidance into a running managed Session. The
// request_id makes replays idempotent; a missing target Session is explicit.
func (a *App) AssistantSessionSteer(req AssistantSteerRequest) (AssistantSessionControlResult, error) {
	at := time.Now()
	if invalid, ok := invalidSessionAction(req.SessionID, req.RequestID, at); ok {
		return invalid, nil
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return AssistantSessionControlResult{Outcome: "invalid", SessionID: req.SessionID, Message: "steer text must not be empty", At: at}, nil
	}
	result := a.runAssistantSessionAction("session_steer", req.SessionID, req.RequestID,
		agent.SessionReceiptFingerprint("steer", req.SessionID, text), at, func() (string, error) {
			return req.SessionID, a.assistantSessionControl().Steer(req.SessionID, text, req.RequestID)
		})
	return result, nil
}

// AssistantSessionAnswer answers one pending interaction on a Session.
func (a *App) AssistantSessionAnswer(req AssistantAnswerRequest) (AssistantSessionControlResult, error) {
	at := time.Now()
	if invalid, ok := invalidSessionAction(req.SessionID, req.RequestID, at); ok {
		return invalid, nil
	}
	if req.InteractionID == "" || len(req.Answers) == 0 {
		return AssistantSessionControlResult{Outcome: "invalid", SessionID: req.SessionID, Message: "interaction and answers are required", At: at}, nil
	}
	answerBytes, err := json.Marshal(req.Answers)
	if err != nil {
		return sessionControlOutcome(req.SessionID, req.RequestID, err, at), nil
	}
	result := a.runAssistantSessionAction("interaction_answer", req.SessionID, req.RequestID,
		agent.SessionReceiptFingerprint("answer", req.SessionID, req.InteractionID, string(answerBytes)), at, func() (string, error) {
			return req.SessionID, a.assistantSessionControl().AnswerQuestion(req.SessionID, req.InteractionID, req.Answers, req.RequestID)
		})
	return result, nil
}

// AssistantSessionCancel stops a running Session and saves its recoverable
// state.
func (a *App) AssistantSessionCancel(req AssistantSessionRequest) (AssistantSessionControlResult, error) {
	at := time.Now()
	if invalid, ok := invalidSessionAction(req.SessionID, req.RequestID, at); ok {
		return invalid, nil
	}
	result := a.runAssistantSessionAction("session_cancel", req.SessionID, req.RequestID,
		agent.SessionReceiptFingerprint("cancel", req.SessionID), at, func() (string, error) {
			return req.SessionID, a.assistantSessionControl().Cancel(req.SessionID, req.RequestID)
		})
	return result, nil
}

// AssistantSessionResume resumes an interrupted Session from its durable
// checkpoint.
func (a *App) AssistantSessionResume(req AssistantSessionRequest) (AssistantSessionControlResult, error) {
	at := time.Now()
	if invalid, ok := invalidSessionAction(req.SessionID, req.RequestID, at); ok {
		return invalid, nil
	}
	result := a.runAssistantSessionAction("session_resume", req.SessionID, req.RequestID,
		agent.SessionReceiptFingerprint("resume", req.SessionID), at, func() (string, error) {
			return req.SessionID, a.assistantSessionControl().Resume(req.SessionID, req.RequestID)
		})
	return result, nil
}

// AssistantSessionFork creates an isolated branch of a Session for a reversible
// trial. A replayed request resolves to the same fork branch id.
func (a *App) AssistantSessionFork(req AssistantSessionRequest) (AssistantSessionControlResult, error) {
	at := time.Now()
	if invalid, ok := invalidSessionAction(req.SessionID, req.RequestID, at); ok {
		return invalid, nil
	}
	result := a.runAssistantSessionAction("session_fork", req.SessionID, req.RequestID,
		agent.SessionReceiptFingerprint("fork", req.SessionID), at, func() (string, error) {
			return a.assistantSessionControl().Fork(req.SessionID, req.RequestID)
		})
	if result.Outcome == "accepted" || result.Outcome == "already_applied" {
		result.NextHint = "forked"
	}
	return result, nil
}
