package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"workground2/internal/collab"
	"workground2/internal/event"
)

func collaborationRunForCommand(snapshot collab.Snapshot, commandID string) *collab.AgentRun {
	for _, item := range snapshot.Timeline {
		if item.AgentRun != nil && item.AgentRun.CommandID == commandID {
			value := *item.AgentRun
			value.ReferenceIDs = append([]string(nil), item.AgentRun.ReferenceIDs...)
			return &value
		}
	}
	return nil
}

func collaborationStartFingerprint(input StartCollaborationAgentInput) string {
	data, _ := json.Marshal(struct {
		SessionID      string   `json:"sessionId"`
		Instruction    string   `json:"instruction"`
		ReferenceIDs   []string `json:"referenceIds"`
		AgentRequestID string   `json:"agentRequestId"`
	}{strings.TrimSpace(input.SessionID), strings.TrimSpace(input.Instruction), input.ReferenceIDs, strings.TrimSpace(input.AgentRequestID)})
	return stableCollaborationID("start", string(data))
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func collaborationAgentRequest(snapshot collab.Snapshot, id string) *collab.AgentRequest {
	for i := range snapshot.Timeline {
		request := snapshot.Timeline[i].AgentRequest
		if request != nil && request.ID == id {
			value := *request
			value.ReferenceIDs = append([]string(nil), request.ReferenceIDs...)
			return &value
		}
	}
	return nil
}

type collaborationReference struct {
	Sequence uint64
	ID       string
	AuthorID string
	Revision uint64
	Kind     string
	Text     string
}

func collaborationContext(snapshot collab.Snapshot, referenceIDs []string) (string, error) {
	if len(referenceIDs) == 0 {
		return "", nil
	}
	wanted := make(map[string]bool, len(referenceIDs))
	for _, id := range referenceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return "", fmt.Errorf("reference id cannot be empty")
		}
		wanted[id] = true
	}
	refs := make([]collaborationReference, 0, len(wanted))
	for _, item := range snapshot.Timeline {
		if !wanted[item.ID] {
			continue
		}
		ref := collaborationReference{Sequence: item.Sequence, ID: item.ID, Kind: string(item.Type)}
		switch {
		case item.Chat != nil:
			ref.AuthorID, ref.Revision, ref.Text = item.Chat.AuthorID, item.Chat.Revision, item.Chat.Text
		case item.Contribution != nil:
			ref.AuthorID, ref.Revision, ref.Text = item.Contribution.AuthorID, item.Contribution.Revision, item.Contribution.Body
		case item.AgentRequest != nil:
			ref.AuthorID, ref.Text = item.AgentRequest.AuthorID, item.AgentRequest.Instruction
		case item.AgentResult != nil:
			ref.AuthorID, ref.Revision, ref.Text = item.AgentResult.OwnerID, item.AgentResult.Revision, item.AgentResult.Summary
		case item.AgentRun != nil:
			ref.AuthorID, ref.Text = item.AgentRun.OwnerID, item.AgentRun.Instruction
		default:
			return "", fmt.Errorf("timeline item %q cannot be used as Agent context", item.ID)
		}
		refs = append(refs, ref)
	}
	if len(refs) != len(wanted) {
		return "", fmt.Errorf("one or more collaboration references no longer exist")
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Sequence < refs[j].Sequence })
	members := make(map[string]string, len(snapshot.Members))
	for _, member := range snapshot.Members {
		members[member.ID] = member.Name
	}
	var builder strings.Builder
	builder.WriteString("<collaboration-context>\n")
	for _, ref := range refs {
		author := members[ref.AuthorID]
		if author == "" {
			author = ref.AuthorID
		}
		fmt.Fprintf(&builder, "[sequence=%d id=%s type=%s author=%s revision=%d]\n%s\n", ref.Sequence, ref.ID, ref.Kind, author, ref.Revision, ref.Text)
	}
	builder.WriteString("</collaboration-context>")
	return builder.String(), nil
}

func (c *desktopCollaboration) publishRun(ctx context.Context, run *collaborationAgentRun, status collab.AgentRunStatus, summary, errText string) (CollaborationActionResult, error) {
	if run == nil {
		return CollaborationActionResult{}, fmt.Errorf("collaboration run is required")
	}
	c.mu.Lock()
	run.PublishIndex++
	index := run.PublishIndex
	c.mu.Unlock()
	requestID := fmt.Sprintf("%s:status:%d", run.CommandID, index)
	return c.submitRunCommand(ctx, run, requestID, collab.Command{Type: collab.CommandPublishAgentRun, AgentRun: &collab.PublishAgentRunInput{
		RunID: run.RunID, AgentID: run.AgentID, CommandID: run.CommandID, RequestRef: run.AgentRequestID,
		Instruction: sanitizeCollaborationText(run.Instruction), ReferenceIDs: append([]string(nil), run.ReferenceIDs...),
		Status: status, Summary: sanitizeCollaborationText(summary), Error: sanitizeCollaborationText(errText),
	}})
}

func (c *desktopCollaboration) runPublisher(run *collaborationAgentRun) {
	for update := range run.Updates {
		ctx, cancel := context.WithTimeout(c.app.bootContext(), 20*time.Second)
		_, publishErr := c.publishRun(ctx, run, update.Status, update.Summary, update.Error)
		if update.Final && update.Status == collab.RunCompleted {
			if publishErr != nil {
				c.failState(c.snapshot().Status, publishErr, collaborationErrorRetryable(publishErr))
			} else {
				summary := sanitizeCollaborationText(update.Summary)
				if summary == "" {
					summary = "Agent 已完成本地执行。"
				}
				_, _ = c.submitRunCommand(ctx, run, run.CommandID+":result:1", collab.Command{Type: collab.CommandPublishAgentResult, AgentResult: &collab.PublishAgentResultInput{
					ResultID: stableCollaborationID("result", run.RunID), AgentID: run.AgentID, RunID: run.RunID,
					Revision: 1, Summary: summary, ReferenceIDs: append([]string(nil), run.ReferenceIDs...),
				}})
			}
		}
		cancel()
		if update.Final {
			c.mu.Lock()
			if c.runs[run.SessionID] == run {
				delete(c.runs, run.SessionID)
			}
			c.persistLocked()
			c.mu.Unlock()
			return
		}
	}
}

func (a *App) observeCollaborationAgentEvent(tabID string, value event.Event) {
	a.mu.RLock()
	tab := a.tabByEventSinkIDLocked(tabID)
	var sessionID string
	if tab != nil {
		sessionID = tab.SessionID
	}
	a.mu.RUnlock()
	if sessionID == "" {
		return
	}
	a.collaborationMu.Lock()
	runtime := a.collaborations[sessionID]
	a.collaborationMu.Unlock()
	if runtime == nil {
		return
	}
	runtime.observeAgentEvent(sessionID, value)
}

func (c *desktopCollaboration) observeAgentEvent(sessionID string, value event.Event) {
	c.mu.Lock()
	run := c.runs[sessionID]
	if run == nil {
		c.mu.Unlock()
		return
	}
	switch value.Kind {
	case event.Text:
		if value.Source == "" || value.Source == event.UsageSourceExecutor {
			appendCollaborationText(&run.Text, value.Text)
		}
		c.mu.Unlock()
	case event.Message:
		if value.Source == "" || value.Source == event.UsageSourceExecutor {
			run.Text.Reset()
			appendCollaborationText(&run.Text, value.Text)
		}
		c.mu.Unlock()
	case event.ApprovalRequest, event.AskRequest:
		c.mu.Unlock()
		run.Updates <- collaborationRunUpdate{Status: collab.RunWaitingApproval}
	case event.TurnDone:
		summary := sanitizeCollaborationText(run.Text.String())
		status := collab.RunCompleted
		errText := ""
		if value.Err != nil {
			status = collab.RunFailed
			errText = sanitizeCollaborationText(value.Err.Error())
		}
		c.mu.Unlock()
		run.Updates <- collaborationRunUpdate{Status: status, Summary: summary, Error: errText, Final: true}
	default:
		// Reasoning, credentials, tool args and tool output never cross the
		// Client closure. They remain visible only in the owning local Session.
		c.mu.Unlock()
	}
}

const maxCollaborationSummaryBytes = 16 * 1024

func appendCollaborationText(builder *strings.Builder, value string) {
	if builder == nil || value == "" || builder.Len() >= maxCollaborationSummaryBytes {
		return
	}
	remaining := maxCollaborationSummaryBytes - builder.Len()
	if len(value) > remaining {
		value = truncateCollaborationUTF8(value, remaining)
	}
	builder.WriteString(value)
}

var collaborationSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|token|password|secret|authorization)\s*[:=]\s*[^\s,;]+`),
}

func sanitizeCollaborationText(value string) string {
	value = strings.TrimSpace(value)
	for _, pattern := range collaborationSecretPatterns {
		value = pattern.ReplaceAllString(value, "[REDACTED]")
	}
	if len(value) > maxCollaborationSummaryBytes {
		value = truncateCollaborationUTF8(value, maxCollaborationSummaryBytes) + "\n…"
	}
	return value
}

func truncateCollaborationUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
