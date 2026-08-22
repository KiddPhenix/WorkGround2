package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"workground2/internal/bot"
	"workground2/internal/control"
	"workground2/internal/decision"
	"workground2/internal/event"
)

const decisionEventChannel = "decision:state"

type DecisionStateView struct {
	Available bool                 `json:"available"`
	Error     string               `json:"error,omitempty"`
	Revision  int64                `json:"revision"`
	Active    *decision.Decision   `json:"active,omitempty"`
	Queue     []decision.Decision  `json:"queue"`
	Deferred  []decision.Decision  `json:"deferred"`
	History   []decision.Decision  `json:"history"`
	Channels  []decision.Channel   `json:"channels"`
	Settings  DecisionSettingsView `json:"settings"`
}

type DecisionSettingsView struct {
	ExternalMode   string `json:"externalMode"`
	LocalOnlyUntil string `json:"localOnlyUntil,omitempty"`
	SmartGraceSec  int    `json:"smartGraceSec"`
}

type DecisionSettingsInput struct {
	ExternalMode   string `json:"externalMode"`
	LocalOnlyUntil string `json:"localOnlyUntil"`
	SmartGraceSec  int    `json:"smartGraceSec"`
}

type DecisionChannelInput struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Enabled      bool   `json:"enabled"`
	ConnectionID string `json:"connectionId"`
	Domain       string `json:"domain"`
	ChatID       string `json:"chatId"`
	ChatType     string `json:"chatType"`
}

type DecisionSelectionInput struct {
	QuestionID string   `json:"questionId"`
	Selected   []string `json:"selected"`
}

type DecisionResolveInput struct {
	DecisionID string                   `json:"decisionId"`
	Selections []DecisionSelectionInput `json:"selections"`
	Responder  string                   `json:"responder"`
}

type DecisionCreateInput struct {
	IdempotencyKey string                   `json:"idempotencyKey"`
	Kind           string                   `json:"kind"`
	AgentID        string                   `json:"agentId"`
	ThreadID       string                   `json:"threadId"`
	WorkspaceRoot  string                   `json:"workspaceRoot"`
	SessionID      string                   `json:"sessionId"`
	Title          string                   `json:"title"`
	TaskSummary    string                   `json:"taskSummary"`
	WhyNow         string                   `json:"whyNow"`
	Questions      []DecisionQuestionInput  `json:"questions"`
	Recommendation *decision.Recommendation `json:"recommendation,omitempty"`
	NoAnswerPolicy string                   `json:"noAnswerPolicy"`
}

type DecisionQuestionInput struct {
	ID          string            `json:"id"`
	Header      string            `json:"header"`
	Prompt      string            `json:"prompt"`
	Options     []decision.Option `json:"options"`
	MultiSelect bool              `json:"multiSelect"`
}

func (a *App) startDecisionRuntime() {
	if a == nil || a.decisionBroker == nil || a.decisionCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(a.bootContext())
	a.decisionCancel = cancel
	changes, unsubscribe := a.decisionBroker.Subscribe(64)
	go func() {
		defer unsubscribe()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		a.emitDecisionState()
		for {
			select {
			case <-ctx.Done():
				return
			case change, ok := <-changes:
				if !ok {
					return
				}
				a.handleDecisionChange(change)
			case <-ticker.C:
				a.retryDecisionApplications()
				a.suspendLongDecisionPrompt()
				a.kickDecisionDeliveries(ctx)
			}
		}
	}()
}

func (a *App) stopDecisionRuntime() {
	if a == nil || a.decisionCancel == nil {
		return
	}
	a.decisionCancel()
	a.decisionCancel = nil
}

func (a *App) DecisionState() DecisionStateView {
	if a == nil || a.decisionBroker == nil {
		message := "decision broker is unavailable"
		if a != nil && a.decisionErr != nil {
			message = a.decisionErr.Error()
		}
		return DecisionStateView{Error: message, Queue: []decision.Decision{}, Deferred: []decision.Decision{}, History: []decision.Decision{}, Channels: []decision.Channel{}}
	}
	snapshot := a.decisionBroker.Snapshot()
	channels := a.decisionBroker.Channels()
	if channels == nil {
		channels = []decision.Channel{}
	}
	view := DecisionStateView{
		Available: true,
		Revision:  snapshot.Revision,
		Queue:     []decision.Decision{},
		Deferred:  []decision.Decision{},
		History:   []decision.Decision{},
		Channels:  channels,
		Settings:  decisionSettingsView(snapshot.Settings),
	}
	for i := range snapshot.Decisions {
		d := snapshot.Decisions[i]
		switch d.Status {
		case decision.StatusPresented:
			copy := d
			view.Active = &copy
		case decision.StatusQueued:
			view.Queue = append(view.Queue, d)
		case decision.StatusDeferred:
			view.Deferred = append(view.Deferred, d)
		default:
			view.History = append(view.History, d)
		}
	}
	sort.SliceStable(view.Queue, func(i, j int) bool { return view.Queue[i].QueueSeq < view.Queue[j].QueueSeq })
	sort.SliceStable(view.History, func(i, j int) bool { return view.History[i].CreatedAt.After(view.History[j].CreatedAt) })
	return view
}

func (a *App) CreateDecision(input DecisionCreateInput) (decision.Decision, error) {
	if a == nil || a.decisionBroker == nil {
		return decision.Decision{}, errors.New("decision broker is unavailable")
	}
	questions := make([]decision.Question, len(input.Questions))
	for i, question := range input.Questions {
		questions[i] = decision.Question{ID: question.ID, Header: question.Header, Prompt: question.Prompt, Options: question.Options, MultiSelect: question.MultiSelect}
	}
	result, err := a.decisionBroker.Create(decision.CreateRequest{
		IdempotencyKey: input.IdempotencyKey,
		Kind:           decision.Kind(input.Kind),
		Origin: decision.Origin{
			Kind: "agent", AgentID: strings.TrimSpace(input.AgentID), ThreadID: strings.TrimSpace(input.ThreadID),
			WorkspaceRoot: strings.TrimSpace(input.WorkspaceRoot), SessionID: strings.TrimSpace(input.SessionID),
		},
		Presentation: decision.Presentation{
			Title: input.Title, TaskSummary: input.TaskSummary, WhyNow: input.WhyNow, Questions: questions,
			Recommendation: input.Recommendation, NoAnswerPolicy: input.NoAnswerPolicy,
		},
	})
	if err != nil {
		return decision.Decision{}, err
	}
	return result.Decision, nil
}

func (a *App) ResolveDecision(input DecisionResolveInput) (DecisionStateView, error) {
	if a == nil || a.decisionBroker == nil {
		return DecisionStateView{}, errors.New("decision broker is unavailable")
	}
	selections := make([]decision.Selection, len(input.Selections))
	for i, selection := range input.Selections {
		selections[i] = decision.Selection{QuestionID: selection.QuestionID, Selected: append([]string(nil), selection.Selected...)}
	}
	_, err := a.decisionBroker.Resolve(strings.TrimSpace(input.DecisionID), decision.Answer{Selections: selections}, decision.Responder{Kind: "desktop", Label: firstNonEmpty(strings.TrimSpace(input.Responder), "WorkGround2 桌面端")})
	if err != nil {
		return a.DecisionState(), err
	}
	return a.DecisionState(), nil
}

func (a *App) DeferDecision(id string) (DecisionStateView, error) {
	if a == nil || a.decisionBroker == nil {
		return DecisionStateView{}, errors.New("decision broker is unavailable")
	}
	_, err := a.decisionBroker.Defer(id)
	return a.DecisionState(), err
}

func (a *App) ResumeDecision(id string) (DecisionStateView, error) {
	if a == nil || a.decisionBroker == nil {
		return DecisionStateView{}, errors.New("decision broker is unavailable")
	}
	_, err := a.decisionBroker.Resume(id)
	return a.DecisionState(), err
}

func (a *App) CancelDecision(id string) (DecisionStateView, error) {
	if a == nil || a.decisionBroker == nil {
		return DecisionStateView{}, errors.New("decision broker is unavailable")
	}
	_, err := a.decisionBroker.Cancel(id)
	return a.DecisionState(), err
}

func (a *App) SaveDecisionSettings(input DecisionSettingsInput) (DecisionStateView, error) {
	if a == nil || a.decisionBroker == nil {
		return DecisionStateView{}, errors.New("decision broker is unavailable")
	}
	settings := decision.Settings{ExternalMode: decision.ExternalMode(strings.TrimSpace(input.ExternalMode)), SmartGrace: time.Duration(input.SmartGraceSec) * time.Second}
	if raw := strings.TrimSpace(input.LocalOnlyUntil); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return a.DecisionState(), fmt.Errorf("invalid local-only deadline: %w", err)
		}
		settings.LocalOnlyUntil = &value
	}
	_, err := a.decisionBroker.SetSettings(settings)
	return a.DecisionState(), err
}

func (a *App) SaveDecisionChannel(input DecisionChannelInput) (DecisionStateView, error) {
	if a == nil || a.decisionBroker == nil {
		return DecisionStateView{}, errors.New("decision broker is unavailable")
	}
	channelID := strings.TrimSpace(input.ID)
	if channelID == "" {
		sum := sha256.Sum256([]byte(strings.Join([]string{input.Kind, input.ConnectionID, input.Domain, input.ChatID, input.ChatType}, "\x00")))
		channelID = "channel-" + hex.EncodeToString(sum[:8])
	}
	_, err := a.decisionBroker.UpsertChannel(decision.Channel{
		ID: channelID, Name: input.Name, Kind: input.Kind, Enabled: input.Enabled,
		ConnectionID: input.ConnectionID, Domain: input.Domain, ChatID: input.ChatID, ChatType: input.ChatType,
	})
	return a.DecisionState(), err
}

func (a *App) DeleteDecisionChannel(id string) (DecisionStateView, error) {
	if a == nil || a.decisionBroker == nil {
		return DecisionStateView{}, errors.New("decision broker is unavailable")
	}
	err := a.decisionBroker.DeleteChannel(id)
	return a.DecisionState(), err
}

func (a *App) TestDecisionChannel(id string) error {
	if a == nil || a.decisionBroker == nil {
		return errors.New("decision broker is unavailable")
	}
	for _, channel := range a.decisionBroker.Channels() {
		if channel.ID != strings.TrimSpace(id) {
			continue
		}
		if channel.Kind == "desktop" {
			return nil
		}
		if a.botRuntime == nil || !a.botRuntime.Running() {
			return errors.New("bot runtime is not running")
		}
		ctx, cancel := context.WithTimeout(a.bootContext(), 30*time.Second)
		defer cancel()
		_, err := a.botRuntime.SendToAdapter(ctx, channel.ConnectionID, channel.Domain, bot.OutboundMessage{ChatID: channel.ChatID, ChatType: bot.ChatType(channel.ChatType), Text: "WorkGround2 主人决策通道测试：连接可用。"})
		return err
	}
	return decision.ErrNotFound
}

func (a *App) observeDecisionAsk(tabID string, ask event.Ask) bool {
	if a == nil || a.decisionBroker == nil {
		return true
	}
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if tab == nil || ctrl == nil {
		return true
	}
	origin := a.decisionOrigin(tab, ctrl, ask.ID)
	request := decision.CreateRequest{
		IdempotencyKey: decisionAskKey(origin, ask),
		Origin:         origin,
		Presentation:   decisionPresentationForAsk(tab, ctrl, ask),
	}
	result, err := a.decisionBroker.Create(request)
	if err != nil {
		slog.Warn("desktop: register global decision", "tab", tabID, "ask", ask.ID, "err", err)
		return true
	}
	if result.Decision.Status == decision.StatusQueued {
		go ctrl.Cancel()
		return false
	}
	return result.Decision.Status == decision.StatusPresented
}

func (a *App) decisionOrigin(tab *WorkspaceTab, ctrl control.SessionAPI, localID string) decision.Origin {
	if tab == nil {
		return decision.Origin{Kind: "desktop", LocalRequestID: strings.TrimSpace(localID)}
	}
	return decision.Origin{
		Kind:           "desktop",
		WorkspaceRoot:  strings.TrimSpace(tab.WorkspaceRoot),
		SessionID:      strings.TrimSpace(tab.SessionID),
		SessionPath:    sessionRuntimeKey(ctrl.SessionPath()),
		SessionTitle:   strings.TrimSpace(tab.TopicTitle),
		LocalRequestID: strings.TrimSpace(localID),
	}
}

func decisionPresentationForAsk(tab *WorkspaceTab, ctrl control.SessionAPI, ask event.Ask) decision.Presentation {
	questions := make([]decision.Question, len(ask.Questions))
	for i, question := range ask.Questions {
		options := make([]decision.Option, len(question.Options))
		for j, option := range question.Options {
			impact := strings.TrimSpace(option.Description)
			if impact == "" {
				impact = fmt.Sprintf("选择“%s”后，任务将按这个方向继续。", strings.TrimSpace(option.Label))
			}
			options[j] = decision.Option{Label: option.Label, Impact: impact}
		}
		questions[i] = decision.Question{ID: question.ID, Header: question.Header, Prompt: question.Prompt, Options: options, MultiSelect: question.Multi}
	}
	title := "需要主人决定"
	if len(questions) > 0 && strings.TrimSpace(questions[0].Header) != "" {
		title = questions[0].Header
	}
	taskSummary := strings.TrimSpace(tab.TopicTitle)
	whyNow := "来源任务遇到需要由你选择的分支，回答后才会继续。"
	if memory, ok := controllerTaskMemory(ctrl); ok {
		taskSummary = firstNonEmpty(strings.TrimSpace(memory.Goal), strings.TrimSpace(memory.Current), taskSummary)
		if strings.TrimSpace(memory.NextStep) != "" {
			whyNow = "下一步是“" + strings.TrimSpace(memory.NextStep) + "”，需要先确定这个选择。"
		}
	}
	if taskSummary == "" {
		taskSummary = firstNonEmpty(strings.TrimSpace(tab.WorkspaceRoot), "WorkGround2 会话正在执行任务")
	}
	return decision.Presentation{
		Title: title, TaskSummary: taskSummary, WhyNow: whyNow, Questions: questions,
		NoAnswerPolicy: "任务保持暂停，不会自动替你选择。",
	}
}

func decisionAskKey(origin decision.Origin, ask event.Ask) string {
	var parts []string
	parts = append(parts, origin.SessionPath, origin.SessionID, origin.ControllerGen, ask.ID)
	for _, question := range ask.Questions {
		parts = append(parts, question.ID, question.Prompt)
		for _, option := range question.Options {
			parts = append(parts, option.Label, option.Description)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "desktop-ask:" + hex.EncodeToString(sum[:16])
}

func (a *App) handleDecisionChange(change decision.Change) {
	if change.Kind == "resolved" {
		a.applyDecision(change.Decision)
	}
	if change.Promoted != nil {
		a.emitPromotedDecision(*change.Promoted)
	}
	a.emitDecisionState()
	a.kickDecisionDeliveries(a.bootContext())
}

func (a *App) kickDecisionDeliveries(ctx context.Context) {
	if a == nil || !a.decisionSending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.decisionSending.Store(false)
		a.syncDecisionDeliveries(ctx)
	}()
}

func (a *App) emitDecisionState() {
	a.emitRuntimeEvent(decisionEventChannel, a.DecisionState())
}

func (a *App) emitPromotedDecision(value decision.Decision) {
	tab := a.decisionOriginTab(value.Origin)
	if tab == nil || tab.sink == nil {
		return
	}
	ask := event.Ask{ID: value.Origin.LocalRequestID, Questions: decisionEventQuestions(value.Presentation)}
	tab.sink.emitRuntimeEvent(eventChannel, toWireTab(event.Event{Kind: event.AskRequest, Ask: ask}, tab.ID))
	a.observeSessionUnread(tab.ID, event.Event{Kind: event.AskRequest, Ask: ask})
}

func decisionEventQuestions(p decision.Presentation) []event.AskQuestion {
	out := make([]event.AskQuestion, len(p.Questions))
	for i, question := range p.Questions {
		options := make([]event.AskOption, len(question.Options))
		for j, option := range question.Options {
			options[j] = event.AskOption{Label: option.Label, Description: option.Impact}
		}
		out[i] = event.AskQuestion{ID: question.ID, Header: question.Header, Prompt: question.Prompt, Options: options, Multi: question.MultiSelect}
	}
	return out
}

func (a *App) decisionOriginTab(origin decision.Origin) *WorkspaceTab {
	if strings.TrimSpace(origin.SessionID) != "" {
		a.mu.RLock()
		tab := a.tabByIDLocked(origin.SessionID)
		a.mu.RUnlock()
		if tab != nil {
			return tab
		}
	}
	return a.findTabBySessionRuntimeKey(sessionRuntimeKey(origin.SessionPath))
}

type decisionQuestionResolver interface {
	ResolveQuestion(id string, answers []event.AskAnswer) bool
}

func (a *App) applyDecision(value decision.Decision) {
	if a == nil || a.decisionBroker == nil || (value.Status != decision.StatusDecided && value.Status != decision.StatusApplyFailed) || value.Answer == nil {
		return
	}
	a.decisionApplyMu.Lock()
	defer a.decisionApplyMu.Unlock()
	current, ok := a.decisionBroker.Get(value.ID)
	if !ok || (current.Status != decision.StatusDecided && current.Status != decision.StatusApplyFailed) || current.Answer == nil {
		return
	}
	value = current
	if value.Origin.Kind != "desktop" {
		return
	}
	tab := a.decisionOriginTab(value.Origin)
	if tab == nil || tab.Ctrl == nil {
		a.markDecisionApplyFailed(value, "origin session is not open; waiting for recovery")
		return
	}
	ctrl := tab.Ctrl
	answers := make([]event.AskAnswer, len(value.Answer.Selections))
	for i, selection := range value.Answer.Selections {
		answers[i] = event.AskAnswer{QuestionID: selection.QuestionID, Selected: append([]string(nil), selection.Selected...)}
	}
	if resolver, ok := ctrl.(decisionQuestionResolver); ok && resolver.ResolveQuestion(value.Origin.LocalRequestID, answers) {
		_, _ = a.decisionBroker.MarkApplied(value.ID)
		return
	}
	if ctrl.Running() || ctrl.PendingPrompt() {
		a.markDecisionApplyFailed(value, "origin controller is still unwinding; retrying")
		return
	}
	ctrl.SubmitUserTurn(decisionResumePrompt(value), decisionResumeDisplay(value))
	_, _ = a.decisionBroker.MarkApplied(value.ID)
}

func (a *App) markDecisionApplyFailed(value decision.Decision, message string) {
	if value.Status == decision.StatusApplyFailed && value.LastError == message {
		return
	}
	_, _ = a.decisionBroker.MarkApplyFailed(value.ID, errors.New(message))
}

func decisionResumeDisplay(value decision.Decision) string {
	return fmt.Sprintf("已回答主人决策 %s", value.ID)
}

func decisionResumePrompt(value decision.Decision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<owner-decision id=%q>\n", value.ID)
	b.WriteString("The human answered a previously suspended decision. Revalidate the current workspace state before acting; if material context changed, ask again instead of blindly applying a stale choice.\n")
	fmt.Fprintf(&b, "Task: %s\nReason: %s\n", value.Presentation.TaskSummary, value.Presentation.WhyNow)
	for _, selection := range value.Answer.Selections {
		fmt.Fprintf(&b, "- %s: %s\n", selection.QuestionID, strings.Join(selection.Selected, ", "))
	}
	b.WriteString("Resume the suspended task from this decision.\n</owner-decision>")
	return b.String()
}

func (a *App) retryDecisionApplications() {
	if a == nil || a.decisionBroker == nil {
		return
	}
	for _, value := range a.decisionBroker.List(decision.ListFilter{Statuses: []decision.Status{decision.StatusDecided, decision.StatusApplyFailed}}) {
		a.applyDecision(value)
	}
}

func (a *App) suspendLongDecisionPrompt() {
	if a == nil || a.decisionBroker == nil {
		return
	}
	active, ok := a.decisionBroker.Active()
	if !ok || active.PresentedAt == nil {
		return
	}
	grace := a.decisionBroker.Settings().SmartGrace
	if grace <= 0 || time.Since(*active.PresentedAt) < grace {
		return
	}
	tab := a.decisionOriginTab(active.Origin)
	if tab == nil || tab.Ctrl == nil {
		return
	}
	pending, ok := tab.Ctrl.PendingInteraction()
	if ok && pending.Kind == control.PendingInteractionAsk && pending.Ask.ID == active.Origin.LocalRequestID {
		tab.Ctrl.Cancel()
	}
}

func (a *App) decisionForAsk(tabID, localID string) (decision.Decision, bool) {
	if a == nil || a.decisionBroker == nil {
		return decision.Decision{}, false
	}
	tab, _ := a.tabAndCtrlByID(tabID)
	if tab == nil {
		return decision.Decision{}, false
	}
	values := a.decisionBroker.List(decision.ListFilter{})
	for i := len(values) - 1; i >= 0; i-- {
		value := values[i]
		if value.Origin.SessionID == tab.SessionID && value.Origin.LocalRequestID == strings.TrimSpace(localID) {
			return value, true
		}
	}
	return decision.Decision{}, false
}

func (a *App) resolveAskThroughDecision(tabID, localID string, answers []event.AskAnswer, responder string) (bool, error) {
	value, ok := a.decisionForAsk(tabID, localID)
	if !ok {
		return false, nil
	}
	selections := make([]decision.Selection, len(answers))
	for i, answer := range answers {
		selections[i] = decision.Selection{QuestionID: answer.QuestionID, Selected: append([]string(nil), answer.Selected...)}
	}
	_, err := a.decisionBroker.Resolve(value.ID, decision.Answer{Selections: selections}, decision.Responder{Kind: "desktop", Label: firstNonEmpty(strings.TrimSpace(responder), "WorkGround2 桌面端")})
	if err != nil {
		return true, err
	}
	return true, nil
}

func (a *App) syncDecisionDeliveries(ctx context.Context) {
	if a == nil || a.decisionBroker == nil || a.botRuntime == nil {
		return
	}
	snapshot := a.decisionBroker.Snapshot()
	now := time.Now().UTC()
	channels := a.decisionBroker.Channels()
	for _, channel := range channels {
		if !channel.Enabled || channel.Kind == "desktop" {
			continue
		}
		for _, value := range snapshot.Decisions {
			if value.Kind == decision.KindNotify {
				if !decisionWasPresentedTo(snapshot.Deliveries, channel.ID, value.ID) && a.decisionBroker.ExternalDeliveryEnabled(now, true, value.PresentedAt) {
					_, _, _ = a.decisionBroker.EnqueueDelivery(channel.ID, value.ID, decision.DeliveryPresented)
				}
				continue
			}
			if !decisionWasPresentedTo(snapshot.Deliveries, channel.ID, value.ID) {
				continue
			}
			switch value.Status {
			case decision.StatusDecided, decision.StatusApplied, decision.StatusApplyFailed:
				if decisionNeedsResolvedDelivery(value, channel.ID) {
					_, _, _ = a.decisionBroker.EnqueueDelivery(channel.ID, value.ID, decision.DeliveryResolved)
				}
			case decision.StatusCancelled, decision.StatusOrphaned:
				_, _, _ = a.decisionBroker.EnqueueDelivery(channel.ID, value.ID, decision.DeliveryCancelled)
			}
		}
		if active, ok := a.decisionBroker.Active(); ok && a.decisionBroker.ExternalDeliveryEnabled(now, true, active.PresentedAt) {
			_, _, _ = a.decisionBroker.EnqueueDelivery(channel.ID, active.ID, decision.DeliveryPresented)
		}
		a.sendNextDecisionDelivery(ctx, channel)
	}
}

func decisionNeedsResolvedDelivery(value decision.Decision, endpointID string) bool {
	return value.Kind != decision.KindNotify && (value.Responder == nil || value.Responder.EndpointID != endpointID)
}

func decisionWasPresentedTo(deliveries []decision.Delivery, endpointID, decisionID string) bool {
	for _, delivery := range deliveries {
		if delivery.EndpointID == endpointID && delivery.DecisionID == decisionID && delivery.Event == decision.DeliveryPresented {
			return true
		}
	}
	return false
}

func (a *App) sendNextDecisionDelivery(parent context.Context, channel decision.Channel) {
	delivery, ok := a.decisionBroker.NextDelivery(channel.ID, time.Now().UTC())
	if !ok {
		return
	}
	value, ok := a.decisionBroker.Get(delivery.DecisionID)
	if !ok {
		_, _ = a.decisionBroker.CompleteDelivery(delivery.ID, "", decision.ErrNotFound, time.Now().Add(time.Minute))
		return
	}
	if !a.botRuntime.Running() {
		_, _ = a.decisionBroker.CompleteDelivery(delivery.ID, "", errors.New("bot runtime is not running"), time.Now().Add(30*time.Second))
		return
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	result, err := a.botRuntime.SendToAdapter(ctx, channel.ConnectionID, channel.Domain, bot.OutboundMessage{
		ConnectionID: channel.ConnectionID, Domain: channel.Domain, ChatID: channel.ChatID,
		ChatType: bot.ChatType(channel.ChatType), Text: renderDecisionDelivery(value, delivery.Event),
	})
	retry := time.Now().Add(decisionRetryDelay(delivery.Attempts + 1))
	_, completeErr := a.decisionBroker.CompleteDelivery(delivery.ID, result.MessageID, err, retry)
	if completeErr != nil {
		slog.Warn("desktop: complete decision delivery", "delivery", delivery.ID, "err", completeErr)
	}
}

func decisionRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 5 * time.Second * time.Duration(1<<min(attempt-1, 6))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func renderDecisionDelivery(value decision.Decision, kind decision.DeliveryEvent) string {
	if kind == decision.DeliveryResolved {
		return fmt.Sprintf("✅ 这个问题已在其他端回答，当前采用：「%s」。", decisionAnswerText(value))
	}
	if kind == decision.DeliveryCancelled {
		return "这个问题已取消，不再需要回答。"
	}
	var b strings.Builder
	if value.Kind == decision.KindNotify {
		fmt.Fprintf(&b, "【通知｜%s】\n\n%s", value.Presentation.Title, value.Presentation.TaskSummary)
		if value.Presentation.WhyNow != "" {
			fmt.Fprintf(&b, "\n\n%s", value.Presentation.WhyNow)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "【需要你决定｜%s】\n\n", value.Presentation.Title)
	fmt.Fprintf(&b, "正在做：%s\n\n为什么现在问：%s\n", value.Presentation.TaskSummary, value.Presentation.WhyNow)
	for i, question := range value.Presentation.Questions {
		fmt.Fprintf(&b, "\n%d. %s\n\n", i+1, question.Prompt)
		for j, option := range question.Options {
			fmt.Fprintf(&b, "%d) %s\n影响：%s\n\n", j+1, option.Label, option.Impact)
		}
	}
	if rec := value.Presentation.Recommendation; rec != nil {
		fmt.Fprintf(&b, "建议：%s\n原因：%s\n\n", rec.Option, rec.Reason)
	}
	fmt.Fprintf(&b, "未回答时：%s\n\n", value.Presentation.NoAnswerPolicy)
	if len(value.Presentation.Questions) == 1 {
		b.WriteString("请直接回复选项编号，例如：1。")
	} else {
		b.WriteString("请按问题顺序回复选项编号，并用逗号分隔，例如：1,2。")
	}
	return b.String()
}

func decisionAnswerText(value decision.Decision) string {
	if value.Answer == nil {
		return ""
	}
	var out []string
	for _, selection := range value.Answer.Selections {
		out = append(out, strings.Join(selection.Selected, ", "))
	}
	return strings.Join(out, "；")
}

func (a *App) handleDecisionInbound(msg bot.InboundMessage) (string, bool, error) {
	if a == nil || a.decisionBroker == nil {
		return "", false, nil
	}
	channel, ok := a.decisionChannelForInbound(msg)
	if !ok {
		return "", false, nil
	}
	value, choices, handled, err := a.parseDecisionReply(strings.TrimSpace(msg.Text))
	if !handled || err != nil {
		return "", handled, err
	}
	answer, err := decisionAnswerFromChoices(value, choices)
	if err != nil {
		return "", true, err
	}
	responderID := firstNonEmpty(strings.TrimSpace(msg.OperatorID), strings.TrimSpace(msg.UserID))
	responderLabel := decisionResponderLabel(msg, responderID)
	result, err := a.decisionBroker.Resolve(value.ID, answer, decision.Responder{
		Kind:       string(msg.Platform),
		ID:         responderID,
		Label:      responderLabel,
		EndpointID: channel.ID,
	})
	if err != nil {
		return "", true, err
	}
	if result.AlreadyResolved {
		return fmt.Sprintf("这个问题已经回答过，当前采用：「%s」。", decisionAnswerText(result.Decision)), true, nil
	}
	return fmt.Sprintf("✅ 已收到，你的选择是：「%s」。", decisionAnswerText(result.Decision)), true, nil
}

func decisionResponderLabel(msg bot.InboundMessage, responderID string) string {
	label := strings.TrimSpace(msg.UserName)
	if label == "" || label == responderID || strings.Contains(strings.ToLower(label), "@im.wechat") {
		if msg.Platform == bot.PlatformWeixin {
			return "微信用户"
		}
		return "通道用户"
	}
	return label
}

func (a *App) decisionChannelForInbound(msg bot.InboundMessage) (decision.Channel, bool) {
	for _, channel := range a.decisionBroker.Channels() {
		if !channel.Enabled || channel.Kind == "desktop" || strings.TrimSpace(channel.ChatID) != strings.TrimSpace(msg.ChatID) {
			continue
		}
		if channel.ConnectionID != "" && channel.ConnectionID != msg.ConnectionID {
			continue
		}
		if channel.Domain != "" && channel.Domain != msg.Domain {
			continue
		}
		if channel.ChatType != "" && channel.ChatType != string(msg.ChatType) {
			continue
		}
		return channel, true
	}
	return decision.Channel{}, false
}

func (a *App) parseDecisionReply(text string) (decision.Decision, []string, bool, error) {
	fields := strings.Fields(text)
	if len(fields) > 0 && strings.EqualFold(fields[0], "/answer") {
		if len(fields) < 3 {
			return decision.Decision{}, nil, true, errors.New("请直接回复选项编号；有多个问题时用逗号分隔")
		}
		value, ok := a.decisionBroker.Get(fields[1])
		if !ok {
			return decision.Decision{}, nil, true, errors.New("找不到这个问题，它可能已经结束")
		}
		return value, strings.Split(strings.Join(fields[2:], ""), ","), true, nil
	}
	active, ok := a.decisionBroker.Active()
	if !ok || len(active.Presentation.Questions) != 1 || len(fields) != 1 {
		return decision.Decision{}, nil, false, nil
	}
	if _, err := strconv.Atoi(fields[0]); err != nil {
		return decision.Decision{}, nil, false, nil
	}
	return active, fields, true, nil
}

func decisionAnswerFromChoices(value decision.Decision, choices []string) (decision.Answer, error) {
	questions := value.Presentation.Questions
	if len(choices) != len(questions) {
		return decision.Answer{}, fmt.Errorf("需要回答 %d 个问题，请按顺序用逗号分隔", len(questions))
	}
	answer := decision.Answer{Selections: make([]decision.Selection, len(questions))}
	for i, question := range questions {
		parts := strings.Split(choices[i], "+")
		if !question.MultiSelect && len(parts) != 1 {
			return decision.Answer{}, fmt.Errorf("第 %d 问只能选择一项", i+1)
		}
		selected := make([]string, 0, len(parts))
		seen := make(map[int]struct{}, len(parts))
		for _, raw := range parts {
			index, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || index < 1 || index > len(question.Options) {
				return decision.Answer{}, fmt.Errorf("第 %d 问没有选项 %q", i+1, raw)
			}
			if _, duplicate := seen[index]; duplicate {
				return decision.Answer{}, fmt.Errorf("第 %d 问重复选择了 %d", i+1, index)
			}
			seen[index] = struct{}{}
			selected = append(selected, question.Options[index-1].Label)
		}
		answer.Selections[i] = decision.Selection{QuestionID: question.ID, Selected: selected}
	}
	return answer, nil
}

func decisionSettingsView(settings decision.Settings) DecisionSettingsView {
	view := DecisionSettingsView{ExternalMode: string(settings.ExternalMode), SmartGraceSec: int(settings.SmartGrace / time.Second)}
	if settings.LocalOnlyUntil != nil {
		view.LocalOnlyUntil = settings.LocalOnlyUntil.UTC().Format(time.RFC3339)
	}
	return view
}
