package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/collab"
)

const (
	agentCollaborationPrefix = "agent-collab-"
	mentionPrefix            = "mention-"
	autoQuestionPrefix       = "agent-question-"
	schedulerTickInterval    = 30 * time.Second
)

// schedulerWakeReason distinguishes why the scheduler loop woke up so
// scheduleOnce can enforce recognition-mode semantics correctly:
//
//   - message: scan request/question only on signal (snapshot update).
//   - interval: scan request/question only on the 30s tick.
//   - off: never scan them.
//
// Mentions always fire on signal; handoffs use their own configured interval.
type schedulerWakeReason int

const (
	wakeSignal schedulerWakeReason = iota + 1
	wakeTicker
)

// collaborationScheduler drives automatic Personal Agent responses for a
// single joined Room Session. It runs in its own goroutine and is woken by
// snapshot updates (message mode) and a periodic ticker (interval mode).
// The frontend must NOT duplicate this scheduling; it becomes projection and
// manual controls only.
type collaborationScheduler struct {
	mu                  sync.Mutex
	running             bool
	lastCollabAttemptAt time.Time
	wake                chan schedulerWakeReason
}

func newCollaborationScheduler() *collaborationScheduler {
	return &collaborationScheduler{
		wake: make(chan schedulerWakeReason, 1),
	}
}

// signal wakes the scheduler with the given reason. Non-blocking.
func (s *collaborationScheduler) signal(reason schedulerWakeReason) {
	select {
	case s.wake <- reason:
	default:
	}
}

// run is the main scheduler loop. It exits when ctx is done.
func (s *collaborationScheduler) run(ctx context.Context, c *desktopCollaboration) {
	ticker := time.NewTicker(schedulerTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-s.wake:
			s.scheduleOnce(ctx, c, reason)
		case <-ticker.C:
			s.scheduleOnce(ctx, c, wakeTicker)
		}
	}
}

func (s *collaborationScheduler) scheduleOnce(ctx context.Context, c *desktopCollaboration, reason schedulerWakeReason) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	state := c.snapshot()
	if state.Status != "connected" {
		return
	}

	snapshot := state.Snapshot
	memberID := state.MemberID
	agentID := state.AgentID
	sessionID := state.SessionID
	config := state.AgentConfig
	// Transport/unread residency intentionally outlives the local Controller.
	// Do not turn an off-tab Room message into a queued Agent command; the same
	// snapshot is reconsidered after the workspace becomes ready.
	if c.startAgentHook == nil && c.agentReady != nil {
		ready, err := c.agentReady(sessionID)
		if err != nil || !ready {
			return
		}
	}

	// Build the set of already-handled references from existing agent runs.
	handledRefs := buildSchedulerHandledRefs(snapshot, memberID)

	// Priority 1: @mentions (always active on signal). In question-only mode
	// only question-shaped mentions fire, and the run is read-only at runtime.
	if reason == wakeSignal {
		if input := s.nextMention(snapshot, memberID, agentID, sessionID, config, handledRefs); input != nil {
			s.startOrLog(ctx, c, *input)
			return
		}
	}

	// Priority 2: Agent requests (recognition-mode gated).
	scanRequests := false
	scanQuestions := false
	switch config.RecognitionMode {
	case "message":
		scanRequests = reason == wakeSignal
		scanQuestions = reason == wakeSignal
	case "interval":
		scanRequests = reason == wakeTicker
		scanQuestions = reason == wakeTicker
	}
	if scanRequests && config.AutoRespondRequests {
		if input := s.nextRequest(snapshot, memberID, handledRefs); input != nil {
			// Accept the request and start the agent in one atomic operation.
			reqInput := RespondCollaborationRequestInput{
				RequestID:      "auto-" + input.RequestID,
				AgentRequestID: input.AgentRequestID,
				Action:         "accept",
				Instruction:    input.Instruction,
				SessionID:      sessionID,
				Automatic:      true,
			}
			result, err := c.respond(ctx, reqInput)
			if err != nil {
				s.recordError(c, "auto-accept request "+input.AgentRequestID+" failed: "+err.Error())
			} else if result.Code != "" || result.Error != "" {
				s.recordError(c, "auto-accept request "+input.AgentRequestID+" deferred: "+schedulerResultError(result))
			}
			return
		}
	}

	// Priority 3: Unanswered questions from non-agent members.
	if scanQuestions && config.AutoRespondQuestions {
		if input := s.nextQuestion(snapshot, memberID, agentID, sessionID, handledRefs); input != nil {
			// Question-only mode answers in text only; the runtime boundary
			// refuses write tools, commands and other side-effecting calls.
			input.ReadOnly = schedulerQuestionOnlyMode(config)
			s.startOrLog(ctx, c, *input)
			return
		}
	}

	// Priority 4: Agent handoffs (autoRespondAgents gated, interval + clock limited).
	if config.AutoRespondAgents {
		if input := s.nextHandoff(snapshot, memberID, agentID, sessionID, config, handledRefs); input != nil {
			if s.startOrLog(ctx, c, *input) {
				s.mu.Lock()
				s.lastCollabAttemptAt = time.Now()
				s.mu.Unlock()
			}
		}
	}
}

// startOrLog calls c.startAgent and surfaces errors into LastError so they
// remain observable and retryable. Never silently drops failures.
func (s *collaborationScheduler) startOrLog(ctx context.Context, c *desktopCollaboration, input StartCollaborationAgentInput) bool {
	if c.startAgentHook != nil {
		c.startAgentHook(ctx, input)
		return true
	}
	result, err := c.startAgent(ctx, input)
	if err != nil {
		s.recordError(c, "auto-start Agent "+input.RequestID+" failed: "+err.Error())
		return false
	}
	if result.Code != "" || result.Error != "" {
		s.recordError(c, "auto-start Agent "+input.RequestID+" deferred: "+schedulerResultError(result))
		return false
	}
	return true
}

func schedulerResultError(result CollaborationActionResult) string {
	if value := strings.TrimSpace(result.Error); value != "" {
		return value
	}
	return strings.TrimSpace(result.Code)
}

func (s *collaborationScheduler) recordError(c *desktopCollaboration, message string) {
	c.mu.Lock()
	c.state.LastError = message
	c.state.Retryable = true
	c.persistLocked()
	c.mu.Unlock()
	c.emitState()
}

// buildSchedulerHandledRefs returns the set of timeline item IDs that have
// already been handled by an agent_command owned by this member.
func buildSchedulerHandledRefs(snapshot collab.Snapshot, memberID string) map[string]bool {
	handled := make(map[string]bool)
	for _, item := range snapshot.Timeline {
		if item.Type != collab.TimelineAgentRun || item.AgentRun == nil {
			continue
		}
		if item.AgentRun.OwnerID != memberID {
			continue
		}
		for _, ref := range item.AgentRun.ReferenceIDs {
			handled[ref] = true
		}
	}
	return handled
}

// ---------------------------------------------------------------------------
// Mention detection
// ---------------------------------------------------------------------------

func schedulerMentionRequestID(itemID, agentID string) string {
	value := itemID + "\x00" + agentID
	return mentionPrefix + schedulerStableHash(value, 2166136261) + schedulerStableHash(value, 2246822519)
}

func schedulerStableHash(value string, seed uint32) string {
	var current uint32 = seed
	for i := 0; i < len(value); i++ {
		current ^= uint32(value[i])
		current *= 16777619
	}
	return strings.ToLower(hex.EncodeToString([]byte{
		byte(current >> 24), byte(current >> 16), byte(current >> 8), byte(current),
	}))
}

func (s *collaborationScheduler) nextMention(
	snapshot collab.Snapshot,
	memberID, agentID, sessionID string,
	config CollaborationAgentConfig,
	handledRefs map[string]bool,
) *StartCollaborationAgentInput {
	if memberID == "" || agentID == "" {
		return nil
	}
	// In question-only mode an @mention must itself be a question; operation
	// instructions ("@Agent 帮我写文件") must not trigger execution. Mentions in
	// manual or operations mode keep their existing unconditional semantics.
	questionOnly := schedulerQuestionOnlyMode(config)
	for _, item := range snapshot.Timeline {
		if item.Type != collab.TimelineChat || item.Chat == nil {
			continue
		}
		chat := item.Chat
		if !schedulerHasString(chat.MentionMemberIDs, memberID) &&
			!schedulerHasString(chat.MentionAgentIDs, agentID) {
			continue
		}
		if handledRefs[item.ID] {
			continue
		}
		if questionOnly && !schedulerQuestionRE.MatchString(chat.Text) {
			continue
		}
		requestID := schedulerMentionRequestID(item.ID, agentID)
		if schedulerHasAgentCommandID(snapshot, memberID, requestID) {
			continue
		}
		return &StartCollaborationAgentInput{
			RequestID:    requestID,
			SessionID:    sessionID,
			Instruction:  "在房间协作中提到你：" + chat.Text,
			ReferenceIDs: []string{item.ID},
			Automatic:    true,
			ReadOnly:     questionOnly,
		}
	}
	return nil
}

// schedulerQuestionOnlyMode reports whether the Agent is configured to answer
// questions only: automatic operation requests are disabled. In this mode
// question-triggered runs must answer in text and are forced read-only at
// runtime (no write tools, commands, or other side effects).
func schedulerQuestionOnlyMode(config CollaborationAgentConfig) bool {
	return config.AutoRespondQuestions && !config.AutoRespondRequests
}

// ---------------------------------------------------------------------------
// Agent request detection
// ---------------------------------------------------------------------------

func (s *collaborationScheduler) nextRequest(
	snapshot collab.Snapshot,
	memberID string,
	handledRefs map[string]bool,
) *StartCollaborationAgentInput {
	for _, item := range snapshot.Timeline {
		if item.Type != collab.TimelineAgentRequest || item.AgentRequest == nil {
			continue
		}
		req := item.AgentRequest
		if req.TargetMemberID != memberID {
			continue
		}
		if req.Status != collab.RequestPending {
			continue
		}
		if handledRefs[item.ID] {
			continue
		}
		return &StartCollaborationAgentInput{
			RequestID:      stableCollaborationID("agent_command", req.ID+"\x00"+memberID),
			AgentRequestID: req.ID,
			Instruction:    req.Instruction,
			ReferenceIDs:   req.ReferenceIDs,
			Automatic:      true,
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Question detection
// ---------------------------------------------------------------------------

var schedulerQuestionRE = regexp.MustCompile(`[?？]\s*$`)

func (s *collaborationScheduler) nextQuestion(
	snapshot collab.Snapshot,
	memberID, agentID, sessionID string,
	handledRefs map[string]bool,
) *StartCollaborationAgentInput {
	if memberID == "" {
		return nil
	}
	for _, item := range snapshot.Timeline {
		if handledRefs[item.ID] {
			continue
		}
		if schedulerItemFromAgent(snapshot, item) {
			continue
		}
		if item.Type == collab.TimelineChat && item.Chat != nil {
			if schedulerHasString(item.Chat.MentionMemberIDs, memberID) ||
				schedulerHasString(item.Chat.MentionAgentIDs, agentID) {
				continue
			}
			if schedulerQuestionRE.MatchString(item.Chat.Text) {
				return schedulerQuestionInput(sessionID, item.ID, item.Chat.Text)
			}
		}
		if item.Type == collab.TimelineContribution && item.Contribution != nil {
			if item.Contribution.Kind == collab.ContributionQuestion {
				return schedulerQuestionInput(sessionID, item.ID, item.Contribution.Body)
			}
		}
	}
	return nil
}

func schedulerQuestionInput(sessionID, itemID, text string) *StartCollaborationAgentInput {
	return &StartCollaborationAgentInput{
		RequestID:    autoQuestionPrefix + schedulerStableID(itemID+"\x00"+sessionID),
		SessionID:    sessionID,
		Instruction:  "请根据协作上下文回答这个问题：" + text,
		ReferenceIDs: []string{itemID},
		Automatic:    true,
	}
}

func schedulerStableID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

// ---------------------------------------------------------------------------
// Agent handoff / collaboration detection
// ---------------------------------------------------------------------------

func schedulerHasString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func schedulerHasAgentCommandID(snapshot collab.Snapshot, memberID, commandID string) bool {
	for _, item := range snapshot.Timeline {
		if item.Type != collab.TimelineAgentRun || item.AgentRun == nil {
			continue
		}
		if item.AgentRun.OwnerID == memberID && item.AgentRun.CommandID == commandID {
			return true
		}
	}
	return false
}

// schedulerItemFromAgent returns true when a timeline item was authored by an
// agent — any member whose Agent.ID is non-empty. Member online/offline status
// is irrelevant: a historical Agent-authored item remains Agent-authored.
func schedulerItemFromAgent(snapshot collab.Snapshot, item collab.TimelineItem) bool {
	authorID := schedulerItemAuthorID(item)
	if authorID == "" {
		return false
	}
	for _, m := range snapshot.Members {
		if m.ID == authorID {
			return m.Agent.ID != ""
		}
	}
	return false
}

func schedulerItemAuthorID(item collab.TimelineItem) string {
	switch item.Type {
	case collab.TimelineChat:
		if item.Chat != nil {
			return item.Chat.AuthorID
		}
	case collab.TimelineContribution:
		if item.Contribution != nil {
			return item.Contribution.AuthorID
		}
	case collab.TimelineAgentRequest:
		if item.AgentRequest != nil {
			return item.AgentRequest.AuthorID
		}
	case collab.TimelineAgentRun:
		if item.AgentRun != nil {
			return item.AgentRun.OwnerID
		}
	case collab.TimelineAgentResult:
		if item.AgentResult != nil {
			return item.AgentResult.OwnerID
		}
	case collab.TimelineFile:
		if item.File != nil {
			return item.File.OwnerID
		}
	}
	return ""
}

// schedulerItemCreatedAt extracts the CreatedAt/UpdatedAt timestamp from a
// timeline item variant. Returns the zero time when unavailable.
func schedulerItemCreatedAt(item collab.TimelineItem) time.Time {
	switch item.Type {
	case collab.TimelineChat:
		if item.Chat != nil {
			return item.Chat.CreatedAt
		}
	case collab.TimelineContribution:
		if item.Contribution != nil {
			return item.Contribution.CreatedAt
		}
	case collab.TimelineAgentRequest:
		if item.AgentRequest != nil {
			return item.AgentRequest.CreatedAt
		}
	case collab.TimelineAgentRun:
		if item.AgentRun != nil {
			return item.AgentRun.UpdatedAt
		}
	case collab.TimelineAgentResult:
		if item.AgentResult != nil {
			return item.AgentResult.CreatedAt
		}
	case collab.TimelineFile:
		if item.File != nil {
			return item.File.CreatedAt
		}
	}
	return time.Time{}
}

func schedulerAgentCollabRequestID(items []collab.TimelineItem, agentID string) string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	sort.Strings(ids)
	value := agentID + "\x00" + strings.Join(ids, "\x00")
	return agentCollaborationPrefix + schedulerStableHash(value, 2166136261) + schedulerStableHash(value, 2246822519)
}

func (s *collaborationScheduler) nextHandoff(
	snapshot collab.Snapshot,
	memberID, agentID, sessionID string,
	config CollaborationAgentConfig,
	handledRefs map[string]bool,
) *StartCollaborationAgentInput {
	if memberID == "" || agentID == "" || !config.AutoRespondAgents {
		return nil
	}

	clock := schedulerAgentClock(snapshot, config)
	if !clock.unlimited && clock.remaining <= 0 {
		return nil
	}

	now := time.Now()
	intervalMS := clampInt(config.AgentResponseIntervalSeconds, 5, 3600) * 1000
	s.mu.Lock()
	last := s.lastCollabAttemptAt
	s.mu.Unlock()
	if persisted := schedulerLastHandoffAt(snapshot, memberID); persisted.After(last) {
		last = persisted
	}
	if !last.IsZero() && now.Sub(last) < time.Duration(intervalMS)*time.Millisecond {
		return nil
	}

	type candidate struct {
		item    collab.TimelineItem
		handoff collab.AgentHandoff
	}
	var candidates []candidate
	for _, item := range snapshot.Timeline {
		if item.Type != collab.TimelineAgentResult || item.AgentResult == nil {
			continue
		}
		result := item.AgentResult
		if result.OwnerID == memberID {
			continue
		}
		if handledRefs[item.ID] {
			continue
		}
		if !schedulerAfterClockReset(item, clock.resetItem, clock.woundAt) {
			continue
		}
		for _, h := range result.Handoffs {
			if h.TargetAgentID == agentID && h.RequiresResponse {
				candidates = append(candidates, candidate{item: item, handoff: h})
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].item.Sequence < candidates[j].item.Sequence
	})
	limit := 8
	if len(candidates) > limit {
		candidates = candidates[len(candidates)-limit:]
	}

	items := make([]collab.TimelineItem, len(candidates))
	refs := make([]string, 0, len(candidates)*2)
	var msgParts []string
	seenRefs := make(map[string]bool)
	for i, c := range candidates {
		items[i] = c.item
		if !seenRefs[c.item.ID] {
			refs = append(refs, c.item.ID)
			seenRefs[c.item.ID] = true
		}
		for _, id := range c.handoff.ReferenceIDs {
			if !seenRefs[id] {
				refs = append(refs, id)
				seenRefs[id] = true
			}
		}
		msgParts = append(msgParts, schedulerHandoffMessage(c.handoff))
	}
	requestID := schedulerAgentCollabRequestID(items, agentID)
	if schedulerHasAgentCommandID(snapshot, memberID, requestID) {
		return nil
	}
	return &StartCollaborationAgentInput{
		RequestID:    requestID,
		SessionID:    sessionID,
		Instruction:  "以下 Agent 向你移交了任务：\n\n" + strings.Join(msgParts, "\n\n"),
		ReferenceIDs: refs,
		Automatic:    true,
	}
}

func schedulerLastHandoffAt(snapshot collab.Snapshot, memberID string) time.Time {
	var latest time.Time
	for _, item := range snapshot.Timeline {
		if item.Type != collab.TimelineAgentRun || item.AgentRun == nil || item.AgentRun.OwnerID != memberID ||
			!strings.HasPrefix(item.AgentRun.CommandID, agentCollaborationPrefix) {
			continue
		}
		if createdAt := schedulerItemCreatedAt(item); createdAt.After(latest) {
			latest = createdAt
		}
	}
	return latest
}

func schedulerHandoffMessage(h collab.AgentHandoff) string {
	var parts []string
	if h.Instruction != "" {
		parts = append(parts, h.Instruction)
	}
	if h.Reason != "" {
		parts = append(parts, "原因："+h.Reason)
	}
	if h.ExpectedOutcome != "" {
		parts = append(parts, "预期结果："+h.ExpectedOutcome)
	}
	return strings.Join(parts, "\n")
}

type schedulerClock struct {
	unlimited bool
	remaining int
	resetItem *collab.TimelineItem
	woundAt   time.Time
}

func schedulerAgentClock(snapshot collab.Snapshot, config CollaborationAgentConfig) schedulerClock {
	limit := clampInt(config.AgentClockTurns, 1, 100)
	if limit <= 0 {
		limit = 12
	}
	unlimited := config.AgentClockUnlimited
	var woundAt time.Time
	if t, err := time.Parse(time.RFC3339Nano, config.AgentClockWoundAt); err == nil {
		woundAt = t
	}

	var resetItem *collab.TimelineItem
	for i := range snapshot.Timeline {
		item := &snapshot.Timeline[i]
		if item.Type == collab.TimelineSystem || item.Type == collab.TimelineAgentResult {
			continue
		}
		if item.Type == collab.TimelineAgentRun {
			if item.AgentRun != nil && !strings.HasPrefix(item.AgentRun.CommandID, agentCollaborationPrefix) {
				resetItem = item
			}
			continue
		}
		if !schedulerItemFromAgent(snapshot, *item) {
			resetItem = item
		}
	}

	used := 0
	for _, item := range snapshot.Timeline {
		if item.Type != collab.TimelineAgentRun || item.AgentRun == nil {
			continue
		}
		if !strings.HasPrefix(item.AgentRun.CommandID, agentCollaborationPrefix) {
			continue
		}
		if schedulerAfterClockReset(item, resetItem, woundAt) {
			used++
		}
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	return schedulerClock{unlimited: unlimited, remaining: remaining, resetItem: resetItem, woundAt: woundAt}
}

// schedulerAfterClockReset returns true when item is after both the resetItem
// (sequentially) and the woundAt timestamp (by its own CreatedAt/UpdatedAt).
// This matches the frontend afterClockReset: sequence for resetItem, time for woundAt.
func schedulerAfterClockReset(item collab.TimelineItem, resetItem *collab.TimelineItem, woundAt time.Time) bool {
	if resetItem != nil {
		if item.Sequence < resetItem.Sequence {
			return false
		}
		if item.Sequence == resetItem.Sequence && item.ID <= resetItem.ID {
			return false
		}
	}
	if !woundAt.IsZero() {
		itemTime := schedulerItemCreatedAt(item)
		if itemTime.IsZero() {
			// No timestamp available: conservative; treat as before woundAt.
			return false
		}
		if !itemTime.After(woundAt) {
			return false
		}
	}
	return true
}

func clampInt(value, minVal, maxVal int) int {
	if value < minVal {
		return minVal
	}
	if value > maxVal {
		return maxVal
	}
	return value
}
