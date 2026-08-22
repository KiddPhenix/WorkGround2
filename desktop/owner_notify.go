package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/decision"
	"workground2/internal/event"
)

const ownerAFKNotifyAfter = 5 * time.Minute

type ownerTurnState struct {
	sequence  uint64
	startedAt time.Time
}

type ownerNotifyCandidate struct {
	tabID         string
	stateKey      string
	sessionID     string
	sessionPath   string
	workspaceRoot string
	sessionTitle  string
	sequence      uint64
	startedAt     time.Time
	err           error
}

func (a *App) observeOwnerNotifyEvent(sink *tabEventSink, value event.Event) {
	if a == nil || sink == nil || (value.Kind != event.TurnStarted && value.Kind != event.TurnDone) {
		return
	}
	now := time.Now()
	if a.ownerNow != nil {
		now = a.ownerNow()
	}
	state := sink.trackOwnerTurn(value.Kind, now)
	if value.Kind != event.TurnDone {
		return
	}
	candidate, ok := a.ownerNotifyCandidate(sink.tabID, value, state)
	if !ok {
		return
	}
	a.goSafe("notifyOwnerAfterAFK", func() { a.notifyOwnerAfterAFK(candidate) })
}

func (s *tabEventSink) trackOwnerTurn(kind event.Kind, now time.Time) ownerTurnState {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	if kind == event.TurnStarted {
		s.ownerTurn.sequence++
		s.ownerTurn.startedAt = now
	} else if s.ownerTurn.sequence == 0 {
		s.ownerTurn = ownerTurnState{sequence: 1, startedAt: now}
	}
	return s.ownerTurn
}

func (a *App) ownerNotifyCandidate(tabID string, value event.Event, state ownerTurnState) (ownerNotifyCandidate, bool) {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if tab == nil {
		return ownerNotifyCandidate{}, false
	}
	stateKey := strings.TrimSpace(tab.SessionID)
	if stateKey == "" {
		stateKey = strings.TrimSpace(tab.ID)
	}

	sessionPath := ""
	if ctrl != nil {
		sessionPath = strings.TrimSpace(ctrl.SessionPath())
	}
	return ownerNotifyCandidate{
		tabID: tabID, stateKey: stateKey, sessionID: strings.TrimSpace(tab.SessionID),
		sessionPath: sessionPath, workspaceRoot: strings.TrimSpace(tab.WorkspaceRoot),
		sessionTitle: firstNonEmpty(strings.TrimSpace(tab.TopicTitle), "当前任务"),
		sequence:     state.sequence, startedAt: state.startedAt, err: value.Err,
	}, true
}

func (a *App) notifyOwnerAfterAFK(candidate ownerNotifyCandidate) {
	if a.decisionBroker == nil {
		return
	}
	probe := platformSystemIdleDuration
	if a.ownerIdleProbe != nil {
		probe = a.ownerIdleProbe
	}
	idle, err := probe()
	if err != nil {
		slog.Warn("desktop: read system AFK duration", "err", err)
		return
	}
	if idle < ownerAFKNotifyAfter || a.ownerWasNotifiedThisTurn(candidate) {
		return
	}
	title, summary := ownerOutcome(ownerTaskLabel(candidate), candidate.err)
	request := decision.CreateRequest{
		IdempotencyKey: fmt.Sprintf("desktop:notify-me:afk:%s:%d", candidate.stateKey, candidate.sequence),
		Kind:           decision.KindNotify,
		Origin: decision.Origin{
			Kind: "desktop", WorkspaceRoot: candidate.workspaceRoot, SessionID: candidate.sessionID,
			SessionPath: candidate.sessionPath, SessionTitle: candidate.sessionTitle,
			LocalRequestID: fmt.Sprintf("notify-me:afk:%d", candidate.sequence),
		},
		Presentation: decision.Presentation{
			Title: title, TaskSummary: summary,
			WhyNow: fmt.Sprintf("你已离开电脑约 %d 分钟。打开 WorkGround2 可查看完整结果。", max(5, int(idle.Round(time.Minute)/time.Minute))),
		},
	}
	if _, err := a.decisionBroker.Create(request); err != nil {
		slog.Warn("desktop: create AFK completion notification", "tab", candidate.tabID, "err", err)
		a.noticeForTab(candidate.tabID, "主人完成通知创建失败，可在主人决策中心重试："+err.Error())
	}
}

func (a *App) ownerWasNotifiedThisTurn(candidate ownerNotifyCandidate) bool {
	branchID := agent.BranchID(candidate.sessionPath)
	for _, value := range a.decisionBroker.List(decision.ListFilter{}) {
		if value.Kind != decision.KindNotify || value.CreatedAt.Before(candidate.startedAt) {
			continue
		}
		if value.Origin.SessionID == candidate.sessionID || (branchID != "" && value.Origin.SessionID == branchID) {
			return true
		}
	}
	return false
}

func ownerOutcome(sessionTitle string, runErr error) (string, string) {
	quoted := fmt.Sprintf("“%s”", firstNonEmpty(strings.TrimSpace(sessionTitle), "当前任务"))
	switch {
	case runErr == nil:
		return "任务已完成", quoted + "已经执行完成。"
	case errors.Is(runErr, context.Canceled):
		return "任务已停止", quoted + "已经停止，未继续执行。"
	default:
		return "任务执行未完成", quoted + "已结束，但遇到错误。回到 WorkGround2 可查看详情并重试。"
	}
}

func ownerTaskLabel(candidate ownerNotifyCandidate) string {
	title := firstNonEmpty(strings.TrimSpace(candidate.sessionTitle), "当前任务")
	project := filepath.Base(strings.TrimSpace(candidate.workspaceRoot))
	if project == "" || project == "." {
		return title
	}
	return project + " / " + title
}
