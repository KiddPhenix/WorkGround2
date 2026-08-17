package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"workground2/internal/config"
	"workground2/internal/tool"
)

const notifyMePortFile = "desktop-port"

type notifyMeTool struct {
	stateDir string
	client   *http.Client
}

// NewNotifyMeTool creates the completion-notification tool used by the
// built-in notify-me skill. WorkGround2 Desktop remains the durable owner of
// delivery, retries, channel routing, and history.
func NewNotifyMeTool() tool.Tool {
	return newNotifyMeTool(config.MemoryUserDir(), &http.Client{Timeout: 10 * time.Second})
}

func newNotifyMeTool(stateDir string, client *http.Client) *notifyMeTool {
	return &notifyMeTool{stateDir: stateDir, client: client}
}

func (*notifyMeTool) Name() string { return "notify_me" }

func (*notifyMeTool) Description() string {
	return "Send one durable, no-reply completion notification through WorkGround2's owner channel. Use only when the user explicitly asked to be notified after the current task finishes. Call it once, after validation, with a standalone human-readable result and useful next step."
}

func (*notifyMeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "title":{"type":"string","description":"Short human-readable result title."},
  "task_summary":{"type":"string","description":"Standalone description of what finished and its outcome; assume the owner cannot see this chat."},
  "why_now":{"type":"string","description":"Optional useful next step or reason to look now."}
},
"required":["title","task_summary"]
}`)
}

func (*notifyMeTool) ReadOnly() bool { return false }

func (*notifyMeTool) PlanModeSafe() bool { return false }

func (t *notifyMeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Title       string `json:"title"`
		TaskSummary string `json:"task_summary"`
		WhyNow      string `json:"why_now"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	input.Title = strings.TrimSpace(input.Title)
	input.TaskSummary = strings.TrimSpace(input.TaskSummary)
	input.WhyNow = strings.TrimSpace(input.WhyNow)
	if input.Title == "" || input.TaskSummary == "" {
		return "", fmt.Errorf("title and task_summary are required")
	}
	parentSession := ParentSession(ctx)
	if parentSession == "" {
		return "", fmt.Errorf("notify_me requires a persisted parent session")
	}
	callID, _, _, _ := CallContext(ctx)
	idempotencyKey := notifyMeKey(parentSession, callID, input.Title, input.TaskSummary, input.WhyNow)
	payload := map[string]any{
		"idempotencyKey": idempotencyKey,
		"kind":           "notify",
		"agentId":        "workground2",
		"threadId":       parentSession,
		"sessionId":      parentSession,
		"title":          input.Title,
		"taskSummary":    input.TaskSummary,
		"whyNow":         input.WhyNow,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode notification: %w", err)
	}
	port, err := t.desktopPort()
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/v1/decisions/create", port), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create notification request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := t.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("send owner notification: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("read owner notification response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("send owner notification: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("decode owner notification response: %w", err)
	}
	if strings.TrimSpace(result.ID) == "" {
		return "", fmt.Errorf("owner notification response omitted id")
	}
	return fmt.Sprintf("Owner notification queued: id=%s status=%s", result.ID, result.Status), nil
}

func (t *notifyMeTool) desktopPort() (int, error) {
	raw, err := os.ReadFile(filepath.Join(t.stateDir, notifyMePortFile))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("WorkGround2 Desktop is not running (desktop-port was not found)")
		}
		return 0, fmt.Errorf("read WorkGround2 Desktop port: %w", err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("WorkGround2 Desktop wrote an invalid port file")
	}
	return port, nil
}

func notifyMeKey(parentSession, callID, title, summary, why string) string {
	seed := strings.Join([]string{parentSession, callID, title, summary, why}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	return "notify-me:" + hex.EncodeToString(sum[:12])
}
