package sessiontool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"workground2/internal/agent"
	"workground2/internal/provider"
)

type sessionSummary struct {
	ID           string               `json:"id"`
	Title        string               `json:"title,omitempty"`
	Status       agent.SessionStatus  `json:"status"`
	Purpose      agent.SessionPurpose `json:"purpose,omitempty"`
	OwnerID      string               `json:"owner_id,omitempty"`
	ParentID     string               `json:"parent_id,omitempty"`
	Workspace    string               `json:"workspace,omitempty"`
	Kind         agent.SessionKind    `json:"kind,omitempty"`
	LastActivity string               `json:"last_activity,omitempty"`
	Preview      string               `json:"preview,omitempty"`
	Turns        int                  `json:"turns,omitempty"`
}

func summarize(info agent.SessionInfo) sessionSummary {
	status := agent.SessionStatusIdle
	if meta, ok, err := agent.LoadBranchMeta(info.Path); err == nil && ok {
		status = agent.DeriveSessionStatus(meta)
	}
	title := info.CustomTitle
	if title == "" {
		title = info.TopicTitle
	}
	var last string
	if !info.LastActivityAt.IsZero() {
		last = info.LastActivityAt.UTC().Format("2006-01-02 15:04:05")
	}
	return sessionSummary{
		ID: agent.BranchID(info.Path), Title: title, Status: status, Purpose: info.Purpose,
		OwnerID: info.AssistantID, ParentID: info.ParentID, Workspace: info.WorkspaceRoot,
		Kind: info.SessionKind, LastActivity: last, Preview: info.Preview, Turns: info.Turns,
	}
}

// ---- session_list -----------------------------------------------------------

type sessionListTool struct {
	sessionDirs []string
}

// NewSessionListTool lists sessions with status/purpose/owner filters.
func NewSessionListTool(sessionDir string) *sessionListTool {
	return NewSessionListToolDirs([]string{sessionDir})
}

func NewSessionListToolDirs(sessionDirs []string) *sessionListTool {
	return &sessionListTool{sessionDirs: uniqueSessionDirs(sessionDirs)}
}

func (t *sessionListTool) Name() string   { return "session_list" }
func (t *sessionListTool) ReadOnly() bool { return true }

func (t *sessionListTool) Description() string {
	return "List sessions owned by an assistant, a workspace, a session kind, or a status, each with its derived lifecycle status and purpose. Use session_status for specific IDs and session_read for bounded conversation context."
}

func (t *sessionListTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"owner_assistant_id":{"type":"string"},"workspace_root":{"type":"string"},"session_kind":{"type":"string"},"status":{"type":"string","description":"queued|running|waiting|completed|failed|cancelled|idle"}},"required":[]}`)
}

func (t *sessionListTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		OwnerID     string `json:"owner_assistant_id"`
		Workspace   string `json:"workspace_root"`
		SessionKind string `json:"session_kind"`
		Status      string `json:"status"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("session_list: invalid args: %w", err)
		}
	}
	all, err := listSessions(t.sessionDirs)
	if err != nil {
		return "", fmt.Errorf("session_list: %w", err)
	}
	var out []sessionSummary
	for _, s := range all {
		sum := summarize(s)
		if in.OwnerID != "" && sum.OwnerID != in.OwnerID {
			continue
		}
		if in.Workspace != "" && sum.Workspace != in.Workspace {
			continue
		}
		if in.SessionKind != "" && string(sum.Kind) != in.SessionKind {
			continue
		}
		if in.Status != "" && string(sum.Status) != in.Status {
			continue
		}
		out = append(out, sum)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("session_list: marshal: %w", err)
	}
	return string(b), nil
}

// ---- session_status ---------------------------------------------------------

type sessionStatusTool struct {
	sessionDirs []string
}

// NewSessionStatusTool reads the status of one or more sessions.
func NewSessionStatusTool(sessionDir string) *sessionStatusTool {
	return NewSessionStatusToolDirs([]string{sessionDir})
}

func NewSessionStatusToolDirs(sessionDirs []string) *sessionStatusTool {
	return &sessionStatusTool{sessionDirs: uniqueSessionDirs(sessionDirs)}
}

func (t *sessionStatusTool) Name() string   { return "session_status" }
func (t *sessionStatusTool) ReadOnly() bool { return true }

func (t *sessionStatusTool) Description() string {
	return "Read the derived lifecycle status and purpose of one or more sessions by explicit session ID."
}

func (t *sessionStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_ids":{"type":"array","items":{"type":"string"}}},"required":["session_ids"]}`)
}

func (t *sessionStatusTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		SessionIDs []string `json:"session_ids"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("session_status: invalid args: %w", err)
	}
	if len(in.SessionIDs) == 0 {
		return "", fmt.Errorf("session_status: session_ids is required")
	}
	all, err := listSessions(t.sessionDirs)
	if err != nil {
		return "", fmt.Errorf("session_status: %w", err)
	}
	byID := make(map[string]agent.SessionInfo, len(all))
	for _, s := range all {
		byID[agent.BranchID(s.Path)] = s
	}
	var out []sessionSummary
	for _, id := range in.SessionIDs {
		id = strings.TrimSpace(id)
		if s, ok := byID[id]; ok {
			out = append(out, summarize(s))
		} else {
			out = append(out, sessionSummary{ID: id, Status: agent.SessionStatusIdle})
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("session_status: marshal: %w", err)
	}
	return string(b), nil
}

func uniqueSessionDirs(dirs []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}

func listSessions(dirs []string) ([]agent.SessionInfo, error) {
	seen := map[string]struct{}{}
	var out []agent.SessionInfo
	for _, dir := range dirs {
		items, err := agent.ListSessions(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			id := agent.BranchID(item.Path)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, item)
		}
	}
	return out, nil
}

// ---- session_read -----------------------------------------------------------

type sessionReadTool struct {
	sessionDirs []string
}

type sessionReadMessage struct {
	Role    string `json:"role"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
}

type sessionReadResult struct {
	Session  sessionSummary       `json:"session"`
	Messages []sessionReadMessage `json:"messages"`
}

func NewSessionReadTool(sessionDir string) *sessionReadTool {
	return NewSessionReadToolDirs([]string{sessionDir})
}

func NewSessionReadToolDirs(sessionDirs []string) *sessionReadTool {
	return &sessionReadTool{sessionDirs: uniqueSessionDirs(sessionDirs)}
}

func (t *sessionReadTool) Name() string   { return "session_read" }
func (t *sessionReadTool) ReadOnly() bool { return true }

func (t *sessionReadTool) Description() string {
	return "Read a bounded tail of user, assistant, and tool messages from one explicit stable session ID. System prompts are excluded; use limit to progressively load recent context."
}

func (t *sessionReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":20,"default":8}},"required":["session_id"]}`)
}

func (t *sessionReadTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("session_read: invalid args: %w", err)
	}
	in.SessionID = strings.TrimSpace(in.SessionID)
	if in.SessionID == "" {
		return "", fmt.Errorf("session_read: session_id is required")
	}
	if in.Limit == 0 {
		in.Limit = 8
	}
	if in.Limit < 1 || in.Limit > 20 {
		return "", fmt.Errorf("session_read: limit must be between 1 and 20")
	}
	all, err := listSessions(t.sessionDirs)
	if err != nil {
		return "", fmt.Errorf("session_read: %w", err)
	}
	var info agent.SessionInfo
	found := false
	for _, item := range all {
		if agent.BranchID(item.Path) == in.SessionID {
			info, found = item, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("session_read: session %s not found", in.SessionID)
	}
	sess, err := agent.LoadSession(info.Path)
	if err != nil {
		return "", fmt.Errorf("session_read: load %s: %w", in.SessionID, err)
	}
	var messages []sessionReadMessage
	for _, message := range sess.Snapshot() {
		if message.Role != provider.RoleUser && message.Role != provider.RoleAssistant && message.Role != provider.RoleTool {
			continue
		}
		messages = append(messages, sessionReadMessage{
			Role: string(message.Role), Name: message.Name, Content: boundedSessionText(message.Content, 4096),
		})
	}
	if len(messages) > in.Limit {
		messages = messages[len(messages)-in.Limit:]
	}
	b, err := json.Marshal(sessionReadResult{Session: summarize(info), Messages: messages})
	if err != nil {
		return "", fmt.Errorf("session_read: marshal: %w", err)
	}
	return string(b), nil
}

func boundedSessionText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(text[:cut]) {
		cut--
	}
	return text[:cut] + "…"
}
