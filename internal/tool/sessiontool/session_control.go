package sessiontool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/event"
)

// SessionControl is the host-provided capability the Assistant session-control
// write tools delegate to. A desktop/daemon host maps a stable Session ID to its
// live control.SessionAPI (via its session registry) and adapts it to this
// interface; it also owns creating new sessions. The tools themselves only
// submit intent and report an explicit outcome — they never hold execution
// state or guess which session "current" means.
type SessionControl interface {
	Steer(sessionID, text, requestID string) error
	AnswerQuestion(sessionID, questionID string, answers []event.AskAnswer, requestID string) error
	Cancel(sessionID, requestID string) error
	Resume(sessionID, requestID string) error
	Retry(sessionID, requestID string) error
	Fork(sessionID, requestID string) (newID string, err error)
	Create(req SessionCreateRequest) (newID string, err error)
	PendingInteractions(sessionID string) ([]SessionInteraction, error)
}

type ownerControl struct {
	SessionControl
	owner     string
	workspace string
}

// BindOwner scopes session_create calls made by one managed Assistant Session.
// The model may omit owner_id/workspace, but cannot impersonate another
// Assistant; the host remains authoritative for validating an explicit path.
func BindOwner(control SessionControl, owner, workspace string) SessionControl {
	owner = strings.TrimSpace(owner)
	if control == nil || owner == "" {
		return control
	}
	return &ownerControl{SessionControl: control, owner: owner, workspace: strings.TrimSpace(workspace)}
}

func (c *ownerControl) Create(req SessionCreateRequest) (string, error) {
	if owner := strings.TrimSpace(req.OwnerID); owner != "" && owner != c.owner {
		return "", fmt.Errorf("session_create owner %q conflicts with bound Assistant %q", owner, c.owner)
	}
	req.OwnerID = c.owner
	if strings.TrimSpace(req.Workspace) == "" {
		req.Workspace = c.workspace
	}
	return c.SessionControl.Create(req)
}

// SessionCreateRequest is the intent for a new managed session. Title, prompt,
// owner assistant and RequestID are required; the host resolves workspace and
// policy. RequestID is the stable idempotency key: replaying the same RequestID
// returns the same Session ID instead of creating a second Session.
type SessionCreateRequest struct {
	Title     string
	Prompt    string
	OwnerID   string
	ParentID  string
	Purpose   agent.SessionPurpose
	Workspace string
	RequestID string
	// IntentPrompt is the stable user intent used by the idempotency fingerprint
	// and as the persisted display text for the first managed Session turn.
	// Prompt still carries the creation-time context envelope seen by the model;
	// ordinary callers leave IntentPrompt empty.
	IntentPrompt string
	// ResponsibilityID optionally binds the session to one plan responsibility;
	// the host persists it in the Session meta so execution state derives from
	// the Session without writing it back to the plan.
	ResponsibilityID string
}

// FingerprintPrompt returns the stable intent included in the Session receipt.
// Context-enveloped managed prompts use IntentPrompt; ordinary callers keep the
// historical full-prompt behavior.
func (r SessionCreateRequest) FingerprintPrompt() string {
	if intent := strings.TrimSpace(r.IntentPrompt); intent != "" {
		return intent
	}
	return strings.TrimSpace(r.Prompt)
}

// SessionInteraction is a bounded view of one pending ask/approval on a session.
type SessionInteraction struct {
	Kind      string // "ask" | "approval"
	ID        string
	Questions []event.AskQuestion
	DueAt     time.Time
}

type controlResult struct {
	// Outcome is the unified write-outcome vocabulary from the design:
	// accepted | already_applied | stale | retryable_error | invalid |
	// blocked_by_policy. Status mirrors it for backward compatibility.
	Status  string `json:"status"`
	Outcome string `json:"outcome"`
	// SessionStatus is the durable lifecycle status of the target session
	// (queued/running/waiting/completed/failed/cancelled/idle), filled by the
	// host after a successful write when it can be derived.
	SessionStatus string `json:"session_status,omitempty"`
	// Revision is the durable session meta revision after the write.
	Revision int64 `json:"revision,omitempty"`
	// NextHint is a short, actionable next step for the Assistant loop.
	NextHint string `json:"next_hint,omitempty"`
	Message  string `json:"message,omitempty"`
	Session  string `json:"session,omitempty"`
}

func (r controlResult) String() string {
	if r.Outcome == "" {
		r.Outcome = r.Status
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf(`{"status":"retryable_error","outcome":"retryable_error","message":%q}`, err.Error())
	}
	return string(b)
}

func controlError(status string, err error) string {
	return controlResult{Status: status, Outcome: status, Message: err.Error()}.String()
}

// controlResultFromErr maps a host error to the unified outcome vocabulary.
// Typed agent errors (outcome_unknown / blocked_policy / not-retryable /
// no-checkpoint) surface as explicit outcomes so the Assistant never guesses a
// redo of external actions; everything else stays retryable_error.
func controlResultFromErr(err error, sessionID string) controlResult {
	if err == nil {
		return controlResult{Status: "accepted", Outcome: "accepted", Session: sessionID}
	}
	switch {
	case errors.Is(err, agent.ErrOutcomeUnknown):
		return controlResult{Status: "invalid", Outcome: "outcome_unknown", Session: sessionID, Message: err.Error()}
	case errors.Is(err, agent.ErrBlockedPolicy):
		return controlResult{Status: "blocked_by_policy", Outcome: "blocked_by_policy", Session: sessionID, Message: err.Error()}
	case errors.Is(err, agent.ErrBlockedDependency):
		return controlResult{Status: "retryable_error", Outcome: "blocked_dependency", Session: sessionID, Message: err.Error()}
	case errors.Is(err, agent.ErrNotRetryable):
		return controlResult{Status: "invalid", Outcome: "not_retryable", Session: sessionID, Message: err.Error()}
	case errors.Is(err, agent.ErrNoFailureCheckpoint):
		return controlResult{Status: "invalid", Outcome: "no_failure_checkpoint", Session: sessionID, Message: err.Error()}
	default:
		return controlResult{Status: "retryable_error", Outcome: "retryable_error", Session: sessionID, Message: err.Error()}
	}
}

// applyOnce runs fn only when requestID has not already been accepted, using a
// durable Session-subsystem receipt (agent.OpReceipt) so a replay survives
// restart. A previously accepted requestID returns the stored result with
// Status/Outcome set to already_applied — but only when the caller's
// fingerprint matches the recorded one; the same request ID reused with
// different parameters is an explicit conflict (invalid), never a silent
// reuse of the wrong outcome. Invalid and retryable_error outcomes are never
// recorded, so a failed request can be retried with the same requestID. The
// recorded SessionID is taken from the accepted result, so fork/create record
// the new Session ID while steer/answer/cancel/resume/retry record the target.
func applyOnce(toolName, requestID, fingerprint, receiptDir string, fn func() controlResult) string {
	rid := strings.TrimSpace(requestID)
	key := toolName + "\x00" + rid
	if rid != "" && receiptDir != "" {
		if rec, ok, err := agent.ReadOpReceipt(receiptDir, key); err == nil && ok {
			if fingerprint != "" && rec.Fingerprint != "" && rec.Fingerprint != fingerprint {
				return controlResult{Status: "invalid", Outcome: "invalid",
					Message: fmt.Sprintf("request %q was already applied with different parameters (conflict)", rid)}.String()
			}
			res := controlResult{Status: "already_applied", Outcome: rec.Status,
				Session: rec.SessionID, Message: rec.Message}
			return res.String()
		}
	}
	res := fn()
	if res.Status == "accepted" && rid != "" && receiptDir != "" {
		res = enrichResult(res, receiptDir, res.Session)
		_, _, _ = agent.WriteOpReceipt(receiptDir, key, agent.OpReceipt{
			Status: res.Status, SessionID: res.Session, Message: res.Message, Fingerprint: fingerprint,
		})
	}
	return res.String()
}

// requestFingerprint derives the idempotency fingerprint of a write request
// from its full input set, so a request ID reused with different parameters is
// detected as a conflict instead of silently returning the old outcome.
func requestFingerprint(parts ...string) string {
	return agent.SessionReceiptFingerprint(parts...)
}

// enrichResult fills the unified outcome/status/revision/next_hint fields of an
// accepted write result from the durable Session subsystem: it locates the
// target session by ID under dir, reads its BranchMeta, and derives the
// lifecycle status, revision, and a short next hint. A session that cannot be
// located (different dir, transient) keeps its zero fields — the outcome
// itself is already durable via the receipt.
func enrichResult(res controlResult, dir, sessionID string) controlResult {
	if sessionID == "" {
		return res
	}
	path := findSessionPath(dir, sessionID)
	if path == "" {
		return res
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok {
		return res
	}
	res.SessionStatus = string(agent.DeriveSessionStatus(meta))
	res.Revision = meta.Revision
	res.NextHint = nextHintFor(string(res.SessionStatus))
	return res
}

// nextHintFor returns a short, actionable next step for the Assistant loop
// given the session's derived lifecycle status.
func nextHintFor(status string) string {
	switch status {
	case "waiting":
		return "answer the pending interaction or steer the session"
	case "failed":
		return "inspect the failure classification before retrying"
	case "running":
		return "wait for the session to settle before steering"
	case "cancelled":
		return "resume from the checkpoint or fork an alternative"
	case "":
		return ""
	default:
		return ""
	}
}

// findSessionPath resolves a stable Session ID to its durable transcript path
// under dir (non-recursive listing), or "" when not found.
func findSessionPath(dir, sessionID string) string {
	all, err := agent.ListSessions(dir)
	if err != nil {
		return ""
	}
	for _, s := range all {
		if agent.BranchID(s.Path) == sessionID {
			return s.Path
		}
	}
	return ""
}

type controlTool struct {
	control    SessionControl
	receiptDir string
}

func newControlTool(c SessionControl, receiptDir string) *controlTool {
	return &controlTool{control: c, receiptDir: receiptDir}
}

func requireSessionID(args json.RawMessage) (string, error) {
	var in struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	id := strings.TrimSpace(in.SessionID)
	if id == "" {
		return "", fmt.Errorf("session_id is required and must be an explicit stable ID")
	}
	return id, nil
}

// requireSessionIDRequest parses the shared session_id plus the optional
// request_id idempotency key used by the write tools.
func requireSessionIDRequest(args json.RawMessage) (id, requestID string, err error) {
	var in struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", "", err
	}
	id = strings.TrimSpace(in.SessionID)
	if id == "" {
		return "", "", fmt.Errorf("session_id is required and must be an explicit stable ID")
	}
	return id, in.RequestID, nil
}

// ---- session_steer ----------------------------------------------------------

type sessionSteerTool struct{ *controlTool }

// NewSessionSteerTool inserts guidance into a running session.
func NewSessionSteerTool(c SessionControl, receiptDir string) *sessionSteerTool {
	return &sessionSteerTool{controlTool: newControlTool(c, receiptDir)}
}

func (t *sessionSteerTool) Name() string   { return "session_steer" }
func (t *sessionSteerTool) ReadOnly() bool { return false }

func (t *sessionSteerTool) Description() string {
	return "Insert guidance into a running session identified by an explicit stable session_id. The session is not stopped; it consumes the steer on its next turn."
}

func (t *sessionSteerTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string"},"text":{"type":"string"},"request_id":{"type":"string","description":"Stable idempotency key; a replay with the same request_id is not executed twice."}},"required":["session_id","text"]}`)
}

func (t *sessionSteerTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return controlError("invalid", err), nil
	}
	id := strings.TrimSpace(in.SessionID)
	if id == "" || strings.TrimSpace(in.Text) == "" {
		return controlError("invalid", fmt.Errorf("session_id and text are required")), nil
	}
	return applyOnce(t.Name(), in.RequestID, requestFingerprint("steer", id, in.Text), t.receiptDir, func() controlResult {
		if err := t.control.Steer(id, in.Text, in.RequestID); err != nil {
			return controlResultFromErr(err, id)
		}
		return controlResult{Status: "accepted", Outcome: "accepted", Session: id}
	}), nil
}

// ---- interaction_answer -----------------------------------------------------

type interactionAnswerTool struct{ *controlTool }

// NewInteractionAnswerTool answers one pending question of a session.
func NewInteractionAnswerTool(c SessionControl, receiptDir string) *interactionAnswerTool {
	return &interactionAnswerTool{controlTool: newControlTool(c, receiptDir)}
}

func (t *interactionAnswerTool) Name() string   { return "interaction_answer" }
func (t *interactionAnswerTool) ReadOnly() bool { return false }

func (t *interactionAnswerTool) Description() string {
	return "Answer one pending question on a session identified by session_id and question_id. Selected carries the chosen option label(s)."
}

func (t *interactionAnswerTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string"},"question_id":{"type":"string"},"selected":{"type":"array","items":{"type":"string"}},"request_id":{"type":"string","description":"Stable idempotency key; a replay with the same request_id is not executed twice."}},"required":["session_id","question_id","selected"]}`)
}

func (t *interactionAnswerTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		SessionID  string   `json:"session_id"`
		QuestionID string   `json:"question_id"`
		Selected   []string `json:"selected"`
		RequestID  string   `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return controlError("invalid", err), nil
	}
	if strings.TrimSpace(in.SessionID) == "" || strings.TrimSpace(in.QuestionID) == "" {
		return controlError("invalid", fmt.Errorf("session_id and question_id are required")), nil
	}
	answers := []event.AskAnswer{{QuestionID: in.QuestionID, Selected: in.Selected}}
	answerBytes, _ := json.Marshal(answers)
	return applyOnce(t.Name(), in.RequestID, requestFingerprint("answer", in.SessionID, in.QuestionID, string(answerBytes)), t.receiptDir, func() controlResult {
		if err := t.control.AnswerQuestion(in.SessionID, in.QuestionID, answers, in.RequestID); err != nil {
			return controlResultFromErr(err, in.SessionID)
		}
		return controlResult{Status: "accepted", Outcome: "accepted", Session: in.SessionID}
	}), nil
}

// ---- session_cancel / resume / retry ----------------------------------------

type sessionCancelTool struct{ *controlTool }

// NewSessionCancelTool stops a session and preserves recoverable state.
func NewSessionCancelTool(c SessionControl, receiptDir string) *sessionCancelTool {
	return &sessionCancelTool{controlTool: newControlTool(c, receiptDir)}
}

func (t *sessionCancelTool) Name() string   { return "session_cancel" }
func (t *sessionCancelTool) ReadOnly() bool { return false }

func (t *sessionCancelTool) Description() string {
	return "Stop a session identified by session_id, preserving its recoverable checkpoint."
}

func (t *sessionCancelTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string"},"request_id":{"type":"string","description":"Stable idempotency key; a replay with the same request_id is not executed twice."}},"required":["session_id"]}`)
}

func (t *sessionCancelTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	id, requestID, err := requireSessionIDRequest(args)
	if err != nil {
		return controlError("invalid", err), nil
	}
	return applyOnce(t.Name(), requestID, requestFingerprint("cancel", id), t.receiptDir, func() controlResult {
		if err := t.control.Cancel(id, requestID); err != nil {
			return controlResultFromErr(err, id)
		}
		return controlResult{Status: "accepted", Outcome: "accepted", Session: id}
	}), nil
}

type sessionResumeTool struct{ *controlTool }

// NewSessionResumeTool resumes an interrupted session.
func NewSessionResumeTool(c SessionControl, receiptDir string) *sessionResumeTool {
	return &sessionResumeTool{controlTool: newControlTool(c, receiptDir)}
}

func (t *sessionResumeTool) Name() string   { return "session_resume" }
func (t *sessionResumeTool) ReadOnly() bool { return false }

func (t *sessionResumeTool) Description() string {
	return "Resume a paused or interrupted session identified by session_id."
}

func (t *sessionResumeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string"},"request_id":{"type":"string","description":"Stable idempotency key; a replay with the same request_id is not executed twice."}},"required":["session_id"]}`)
}

func (t *sessionResumeTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	id, requestID, err := requireSessionIDRequest(args)
	if err != nil {
		return controlError("invalid", err), nil
	}
	return applyOnce(t.Name(), requestID, requestFingerprint("resume", id), t.receiptDir, func() controlResult {
		if err := t.control.Resume(id, requestID); err != nil {
			return controlResultFromErr(err, id)
		}
		return controlResult{Status: "accepted", Outcome: "accepted", Session: id}
	}), nil
}

type sessionRetryTool struct{ *controlTool }

// NewSessionRetryTool safely retries a failed session.
func NewSessionRetryTool(c SessionControl, receiptDir string) *sessionRetryTool {
	return &sessionRetryTool{controlTool: newControlTool(c, receiptDir)}
}

func (t *sessionRetryTool) Name() string   { return "session_retry" }
func (t *sessionRetryTool) ReadOnly() bool { return false }

func (t *sessionRetryTool) Description() string {
	return "Safely retry a failed session identified by session_id from its recoverable context."
}

func (t *sessionRetryTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string"},"request_id":{"type":"string","description":"Stable idempotency key; a replay with the same request_id is not executed twice."}},"required":["session_id"]}`)
}

func (t *sessionRetryTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	id, requestID, err := requireSessionIDRequest(args)
	if err != nil {
		return controlError("invalid", err), nil
	}
	return applyOnce(t.Name(), requestID, requestFingerprint("retry", id), t.receiptDir, func() controlResult {
		if err := t.control.Retry(id, requestID); err != nil {
			return controlResultFromErr(err, id)
		}
		return controlResult{Status: "accepted", Outcome: "accepted", Session: id}
	}), nil
}

// ---- session_fork -----------------------------------------------------------

type sessionForkTool struct{ *controlTool }

// NewSessionForkTool forks a session from its checkpoint.
func NewSessionForkTool(c SessionControl, receiptDir string) *sessionForkTool {
	return &sessionForkTool{controlTool: newControlTool(c, receiptDir)}
}

func (t *sessionForkTool) Name() string   { return "session_fork" }
func (t *sessionForkTool) ReadOnly() bool { return false }

func (t *sessionForkTool) Description() string {
	return "Fork a session identified by session_id into an isolated branch for trying a mutually exclusive alternative. Returns the new session_id."
}

func (t *sessionForkTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string"},"request_id":{"type":"string","description":"Stable idempotency key; a replay with the same request_id is not executed twice."}},"required":["session_id"]}`)
}

func (t *sessionForkTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	id, requestID, err := requireSessionIDRequest(args)
	if err != nil {
		return controlError("invalid", err), nil
	}
	return applyOnce(t.Name(), requestID, requestFingerprint("fork", id), t.receiptDir, func() controlResult {
		newID, err := t.control.Fork(id, requestID)
		if err != nil {
			return controlResultFromErr(err, id)
		}
		return controlResult{Status: "accepted", Outcome: "accepted", Session: newID}
	}), nil
}

// ---- session_create ---------------------------------------------------------

type sessionCreateTool struct{ *controlTool }

// NewSessionCreateTool creates a new managed session.
func NewSessionCreateTool(c SessionControl, receiptDir string) *sessionCreateTool {
	return &sessionCreateTool{controlTool: newControlTool(c, receiptDir)}
}

func (t *sessionCreateTool) Name() string   { return "session_create" }
func (t *sessionCreateTool) ReadOnly() bool { return false }

func (t *sessionCreateTool) Description() string {
	return "Create a new managed session with a stable request_id so a duplicated request does not create a second session. Pass responsibility_id to bind the session to a plan responsibility (the host persists it in the session meta). Returns the new session_id."
}

func (t *sessionCreateTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"prompt":{"type":"string"},"owner_id":{"type":"string"},"parent_id":{"type":"string"},"purpose":{"type":"string"},"workspace":{"type":"string"},"responsibility_id":{"type":"string"},"request_id":{"type":"string"}},"required":["title","prompt","request_id"]}`)
}

func (t *sessionCreateTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Title            string `json:"title"`
		Prompt           string `json:"prompt"`
		OwnerID          string `json:"owner_id"`
		ParentID         string `json:"parent_id"`
		Purpose          string `json:"purpose"`
		Workspace        string `json:"workspace"`
		ResponsibilityID string `json:"responsibility_id"`
		RequestID        string `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return controlError("invalid", err), nil
	}
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Prompt) == "" || strings.TrimSpace(in.RequestID) == "" {
		return controlError("invalid", fmt.Errorf("title, prompt and request_id are required")), nil
	}
	return applyOnce(t.Name(), in.RequestID, requestFingerprint("create", in.Title, in.Prompt, in.OwnerID, in.ParentID, in.Purpose, in.Workspace, in.ResponsibilityID), t.receiptDir, func() controlResult {
		newID, err := t.control.Create(SessionCreateRequest{
			Title: in.Title, Prompt: in.Prompt, OwnerID: in.OwnerID,
			ParentID: in.ParentID, Purpose: agent.SessionPurpose(in.Purpose), Workspace: in.Workspace,
			RequestID: in.RequestID, ResponsibilityID: in.ResponsibilityID,
		})
		if err != nil {
			return controlResultFromErr(err, "")
		}
		return controlResult{Status: "accepted", Outcome: "accepted", Session: newID}
	}), nil
}

// ---- interaction_list -------------------------------------------------------

type interactionListTool struct{ *controlTool }

// NewInteractionListTool lists pending asks/approvals of a session.
func NewInteractionListTool(c SessionControl, receiptDir string) *interactionListTool {
	return &interactionListTool{controlTool: newControlTool(c, receiptDir)}
}

func (t *interactionListTool) Name() string   { return "interaction_list" }
func (t *interactionListTool) ReadOnly() bool { return true }

func (t *interactionListTool) Description() string {
	return "List pending questions and approvals for a session identified by session_id."
}

func (t *interactionListTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string"}},"required":["session_id"]}`)
}

func (t *interactionListTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	id, err := requireSessionID(args)
	if err != nil {
		return controlError("invalid", err), nil
	}
	items, err := t.control.PendingInteractions(id)
	if err != nil {
		return controlError("retryable_error", err), nil
	}
	b, err := json.Marshal(items)
	if err != nil {
		return controlError("retryable_error", err), nil
	}
	return string(b), nil
}
