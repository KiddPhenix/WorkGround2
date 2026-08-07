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
	"workground2/internal/control"
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
		Automatic      bool     `json:"automatic,omitempty"`
	}{strings.TrimSpace(input.SessionID), strings.TrimSpace(input.Instruction), input.ReferenceIDs, strings.TrimSpace(input.AgentRequestID), input.Automatic})
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

func collaborationContext(snapshot collab.Snapshot, referenceIDs []string, fileRefs map[string]string) (string, error) {
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
		case item.File != nil:
			ref.AuthorID, ref.Revision = item.File.OwnerID, item.File.Revision
			ref.Text = fmt.Sprintf("Shared file metadata: %s (%d bytes, SHA-256 %s). The file contents are not automatically included.", item.File.Name, item.File.Size, item.File.SHA256)
			if refPath, ok := fileRefs[item.ID]; ok {
				ref.Text = fmt.Sprintf("Shared file: %s (%d bytes, SHA-256 %s). The received file is available for reading at %s.", item.File.Name, item.File.Size, item.File.SHA256, refPath)
			}
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

const (
	roomHandoffsStart = "<room-handoffs>"
	roomHandoffsEnd   = "</room-handoffs>"
)

type roomAgentRosterEntry struct {
	AgentID      string   `json:"agentId"`
	AgentName    string   `json:"agentName"`
	AgentRole    string   `json:"agentRole,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	MemberID     string   `json:"memberId"`
	MemberName   string   `json:"memberName"`
	MemberRole   string   `json:"memberRole,omitempty"`
}

func collaborationRoomPrompt(snapshot collab.Snapshot, selfAgentID string) string {
	roster := make([]roomAgentRosterEntry, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		if member.Status == collab.MemberOffline || strings.TrimSpace(member.Agent.ID) == "" {
			continue
		}
		roster = append(roster, roomAgentRosterEntry{
			AgentID: member.Agent.ID, AgentName: member.Agent.Name, AgentRole: member.Agent.Role,
			Capabilities: append([]string(nil), member.Agent.Capabilities...),
			MemberID:     member.ID, MemberName: member.Name, MemberRole: member.Role,
		})
	}
	sort.Slice(roster, func(i, j int) bool { return roster[i].AgentID < roster[j].AgentID })
	data, _ := json.Marshal(roster)
	return fmt.Sprintf(`<room-collaboration self-agent-id=%q>
Online Room Agents: %s
Complete the current task yourself when you can. When a concrete follow-up requires another Room Agent's role, repository, verification, or decision, append one machine-readable JSON array inside %s and %s after your public answer. Do not wrap this block in a Markdown code fence. Each entry must use an exact agentId from the roster and contain targetAgentId, instruction, referenceIds, reason, expectedOutcome, and requiresResponse. Set requiresResponse=true only when that Agent must act or answer. Do not hand off acknowledgements, repeated conclusions, completed work, or messages with no concrete next action. Multi-step collaboration is allowed; each handoff must carry a specific new responsibility. Do not write the machine-readable block when no handoff is needed.
</room-collaboration>`, selfAgentID, data, roomHandoffsStart, roomHandoffsEnd)
}

func collaborationAgentInput(snapshot collab.Snapshot, selfAgentID, instruction string, referenceIDs []string, fileRefs map[string]string) (string, error) {
	contextText, err := collaborationContext(snapshot, referenceIDs, fileRefs)
	if err != nil {
		return "", err
	}
	parts := []string{collaborationRoomPrompt(snapshot, selfAgentID), instruction}
	if contextText != "" {
		parts = append(parts, contextText)
	}
	return strings.Join(parts, "\n\n"), nil
}

func collaborationAgentOutput(summary string, snapshot collab.Snapshot, selfAgentID string, fallbackReferences []string) (string, []collab.AgentHandoff) {
	start := strings.LastIndex(summary, roomHandoffsStart)
	if start < 0 {
		return summary, nil
	}
	relativeEnd := strings.Index(summary[start+len(roomHandoffsStart):], roomHandoffsEnd)
	if relativeEnd < 0 {
		return summary, nil
	}
	end := start + len(roomHandoffsStart) + relativeEnd
	var decoded []collab.AgentHandoff
	if err := json.Unmarshal([]byte(strings.TrimSpace(summary[start+len(roomHandoffsStart):end])), &decoded); err != nil {
		return summary, nil
	}
	agents := make(map[string]bool, len(snapshot.Members))
	items := make(map[string]bool, len(snapshot.Timeline))
	for _, member := range snapshot.Members {
		if member.Status != collab.MemberOffline && member.Agent.ID != "" {
			agents[member.Agent.ID] = true
		}
	}
	for _, item := range snapshot.Timeline {
		items[item.ID] = true
	}
	result := make([]collab.AgentHandoff, 0, len(decoded))
	seen := map[string]bool{}
	for _, handoff := range decoded {
		if len(result) >= collab.MaxMembers {
			break
		}
		handoff.TargetAgentID = strings.TrimSpace(handoff.TargetAgentID)
		handoff.Instruction = sanitizeCollaborationText(handoff.Instruction)
		handoff.Reason = truncateCollaborationUTF8(sanitizeCollaborationText(handoff.Reason), collab.MaxShortText)
		handoff.ExpectedOutcome = truncateCollaborationUTF8(sanitizeCollaborationText(handoff.ExpectedOutcome), collab.MaxShortText)
		if !agents[handoff.TargetAgentID] || handoff.TargetAgentID == selfAgentID || handoff.Instruction == "" {
			continue
		}
		refs := make([]string, 0, len(handoff.ReferenceIDs))
		seenRefs := map[string]bool{}
		for _, id := range handoff.ReferenceIDs {
			id = strings.TrimSpace(id)
			if items[id] && !seenRefs[id] && len(refs) < collab.MaxReferences {
				seenRefs[id] = true
				refs = append(refs, id)
			}
		}
		if len(refs) == 0 {
			for _, id := range fallbackReferences {
				if items[id] && !seenRefs[id] && len(refs) < collab.MaxReferences {
					seenRefs[id] = true
					refs = append(refs, id)
				}
			}
		}
		handoff.ReferenceIDs = refs
		key := handoff.TargetAgentID + "\x00" + handoff.Instruction
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, handoff)
	}
	public := strings.TrimSpace(summary[:start] + summary[end+len(roomHandoffsEnd):])
	if public == "" {
		public = "Agent 已完成本地执行。"
	}
	return public, result
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
		summary := update.Summary
		var handoffs []collab.AgentHandoff
		if update.Final && update.Status == collab.RunCompleted {
			state := c.snapshot()
			summary, handoffs = collaborationAgentOutput(summary, state.Snapshot, run.AgentID, run.ReferenceIDs)
		}
		ctx, cancel := context.WithTimeout(c.app.bootContext(), 20*time.Second)
		_, publishErr := c.publishRun(ctx, run, update.Status, summary, update.Error)
		if update.Final && update.Status == collab.RunCompleted {
			if publishErr != nil {
				c.failState(c.snapshot().Status, publishErr, collaborationErrorRetryable(publishErr))
			} else {
				summary := sanitizeCollaborationText(summary)
				if summary == "" {
					summary = "Agent 已完成本地执行。"
				}
				_, resultErr := c.submitRunCommand(ctx, run, run.CommandID+":result:1", collab.Command{Type: collab.CommandPublishAgentResult, AgentResult: &collab.PublishAgentResultInput{
					ResultID: stableCollaborationID("result", run.RunID), AgentID: run.AgentID, RunID: run.RunID,
					Revision: 1, Summary: summary, ReferenceIDs: append([]string(nil), run.ReferenceIDs...), Handoffs: cloneCollaborationHandoffs(handoffs),
				}})
				if resultErr != nil {
					c.failState(c.snapshot().Status, resultErr, collaborationErrorRetryable(resultErr))
				}
			}
		}
		cancel()
		if update.Final {
			c.restoreAgentApproval(run)
			c.mu.Lock()
			removed := false
			if c.runs[run.SessionID] == run {
				delete(c.runs, run.SessionID)
				removed = true
			}
			c.persistLocked()
			c.mu.Unlock()
			if removed {
				c.emitState()
				go c.startNextQueuedAgent(run.SessionID)
			}
			return
		}
	}
}

func (c *desktopCollaboration) prepareAgentApproval(run *collaborationAgentRun) error {
	if run == nil || !run.Automatic || c.prepareAutoAgent == nil {
		return nil
	}
	previous, err := c.prepareAutoAgent(run.SessionID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	run.RestoreMode = previous
	c.mu.Unlock()
	return nil
}

func (c *desktopCollaboration) restoreAgentApproval(run *collaborationAgentRun) {
	if run == nil || c.restoreAutoAgent == nil {
		return
	}
	c.mu.Lock()
	previous := run.RestoreMode
	run.RestoreMode = ""
	c.mu.Unlock()
	if previous != "" {
		c.restoreAutoAgent(run.SessionID, previous)
	}
}

func (c *desktopCollaboration) startNextQueuedAgent(sessionID string) {
	ctx := context.Background()
	if c.app != nil {
		ctx = c.app.bootContext()
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	if c.runs[sessionID] != nil || len(c.queuedRuns) == 0 {
		c.mu.Unlock()
		return
	}
	var run *collaborationAgentRun
	for _, candidate := range c.queuedRuns {
		if candidate == nil || candidate.SessionID != sessionID {
			continue
		}
		run = candidate
		break
	}
	if run == nil {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	ready := true
	var err error
	if c.agentReady != nil {
		ready, err = c.agentReady(sessionID)
	} else if c.validateAgent != nil {
		err = c.validateAgent(sessionID)
	}
	if err != nil {
		c.mu.Lock()
		c.state.LastError = "queued Agent cannot start: " + err.Error()
		c.state.Retryable = true
		c.persistLocked()
		c.mu.Unlock()
		c.emitState()
		return
	}
	if !ready {
		c.waitForQueuedAgent(sessionID)
		return
	}

	c.mu.Lock()
	if c.runs[sessionID] != nil || c.removeQueuedRunLocked(run.RunID) != run {
		c.mu.Unlock()
		return
	}
	run.StartedAt = time.Now().UTC()
	c.runs[sessionID] = run
	state := cloneCollaborationState(c.state)
	c.persistLocked()
	c.mu.Unlock()
	c.emitState()

	fullInput, err := collaborationAgentInput(state.Snapshot, run.AgentID, run.Instruction, run.ReferenceIDs, c.roomAttachmentRefs(run.ReferenceIDs))
	if err != nil {
		c.failAgentRun(ctx, run, err)
		return
	}
	_ = c.launchAgent(ctx, run, fullInput)
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

func collaborationAgentPromptFromEvent(runID string, value event.Event) *CollaborationAgentPrompt {
	switch value.Kind {
	case event.ApprovalRequest:
		subject := strings.TrimSpace(value.Approval.Subject)
		if subject == "" {
			subject = strings.TrimSpace(value.Approval.Summary)
		}
		return &CollaborationAgentPrompt{
			RunID: runID, Kind: control.PendingInteractionApproval, ID: value.Approval.ID,
			Tool: value.Approval.Tool, Subject: subject, Reason: value.Approval.Reason,
		}
	case event.AskRequest:
		questions := make([]CollaborationAgentPromptQuestion, 0, len(value.Ask.Questions))
		for _, question := range value.Ask.Questions {
			options := make([]CollaborationAgentPromptOption, 0, len(question.Options))
			for _, option := range question.Options {
				options = append(options, CollaborationAgentPromptOption{Label: option.Label, Description: option.Description})
			}
			questions = append(questions, CollaborationAgentPromptQuestion{
				ID: question.ID, Header: question.Header, Prompt: question.Prompt, Options: options, Multi: question.Multi,
			})
		}
		return &CollaborationAgentPrompt{RunID: runID, Kind: control.PendingInteractionAsk, ID: value.Ask.ID, Questions: questions}
	default:
		return nil
	}
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
		c.emitState()
	case event.ApprovalRequest, event.AskRequest:
		run.PromptOpen = true
		run.Prompt = collaborationAgentPromptFromEvent(run.RunID, value)
		c.mu.Unlock()
		c.emitState()
		run.Updates <- collaborationRunUpdate{Status: collab.RunWaitingApproval}
	case event.TurnDone:
		summary := sanitizeCollaborationText(run.Text.String())
		status := collab.RunCompleted
		errText := ""
		if run.StopRequested {
			status = collab.RunCancelled
			errText = "cancelled by the Agent owner"
		} else if value.Err != nil {
			status = collab.RunFailed
			errText = sanitizeCollaborationText(value.Err.Error())
		}
		run.PromptOpen = false
		run.Prompt = nil
		c.mu.Unlock()
		c.emitState()
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
