package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"workground2/internal/decision"
	"workground2/internal/event"
)

func TestAFKCompletionNotificationIsThresholdedAndIdempotent(t *testing.T) {
	broker, err := decision.Open("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tab := &WorkspaceTab{ID: "tab-a", SessionID: "session-a", TopicTitle: "构建修复版", WorkspaceRoot: `D:\Work\Demo`}
	app := &App{
		tabs: map[string]*WorkspaceTab{"tab-a": tab}, decisionBroker: broker, ownerDecisionEnabled: true,
		ownerNow:       func() time.Time { return now },
		ownerIdleProbe: func() (time.Duration, error) { return 4 * time.Minute, nil },
	}
	sink := &tabEventSink{}
	state := sink.trackOwnerTurn(event.TurnStarted, now)
	candidate, ok := app.ownerNotifyCandidate("tab-a", event.Event{Kind: event.TurnDone}, state)
	if !ok {
		t.Fatal("missing completion candidate")
	}
	app.notifyOwnerAfterAFK(candidate)
	if got := len(broker.List(decision.ListFilter{})); got != 0 {
		t.Fatalf("notifications before threshold = %d", got)
	}
	app.ownerIdleProbe = func() (time.Duration, error) { return 6 * time.Minute, nil }
	app.notifyOwnerAfterAFK(candidate)
	app.notifyOwnerAfterAFK(candidate)
	values := broker.List(decision.ListFilter{})
	if len(values) != 1 {
		t.Fatalf("notifications = %d, want 1", len(values))
	}
	value := values[0]
	if value.Kind != decision.KindNotify || value.Status != decision.StatusApplied || value.Presentation.Title != "任务已完成" {
		t.Fatalf("notification = %+v", value)
	}
	if !strings.Contains(value.Presentation.TaskSummary, "构建修复版") || !strings.Contains(value.Presentation.WhyNow, "6 分钟") {
		t.Fatalf("presentation = %+v", value.Presentation)
	}
}

func TestAFKCompletionSkipsExplicitNotifyMeFromSameTurn(t *testing.T) {
	broker, _ := decision.Open("")
	now := time.Now().UTC()
	tab := &WorkspaceTab{ID: "tab-a", SessionID: "session-a", TopicTitle: "任务 A"}
	app := &App{
		tabs: map[string]*WorkspaceTab{"tab-a": tab}, decisionBroker: broker, ownerDecisionEnabled: true,
		ownerNow:       func() time.Time { return now },
		ownerIdleProbe: func() (time.Duration, error) { return 10 * time.Minute, nil },
	}
	sink := &tabEventSink{}
	state := sink.trackOwnerTurn(event.TurnStarted, now)
	if _, err := broker.Create(decision.CreateRequest{
		IdempotencyKey: "explicit", Kind: decision.KindNotify,
		Origin:       decision.Origin{Kind: "agent", SessionID: "session-a"},
		Presentation: decision.Presentation{Title: "任务已完成", TaskSummary: "任务 A 已完成。"},
	}); err != nil {
		t.Fatal(err)
	}
	candidate, _ := app.ownerNotifyCandidate("tab-a", event.Event{Kind: event.TurnDone}, state)
	app.notifyOwnerAfterAFK(candidate)
	if got := len(broker.List(decision.ListFilter{})); got != 1 {
		t.Fatalf("notifications = %d, want explicit notification only", got)
	}
}

func TestOwnerOutcomeDistinguishesFailureAndCancellation(t *testing.T) {
	title, summary := ownerOutcome("发布任务", errors.New("network timeout\nretry exhausted"))
	if title != "任务执行未完成" || strings.Contains(summary, "network timeout") || !strings.Contains(summary, "查看详情") {
		t.Fatalf("failure = %q / %q", title, summary)
	}
	title, summary = ownerOutcome("发布任务", context.Canceled)
	if title != "任务已停止" || !strings.Contains(summary, "已经停止") {
		t.Fatalf("cancel = %q / %q", title, summary)
	}
}

func TestAFKProbeFailureDoesNotCreateNotification(t *testing.T) {
	broker, _ := decision.Open("")
	app := &App{ownerDecisionEnabled: true, decisionBroker: broker, ownerIdleProbe: func() (time.Duration, error) { return 0, errors.New("unsupported") }}
	app.notifyOwnerAfterAFK(ownerNotifyCandidate{stateKey: "s", sequence: 1, sessionTitle: "任务"})
	if got := len(broker.List(decision.ListFilter{})); got != 0 {
		t.Fatalf("notifications = %d", got)
	}
}

func TestOwnerTurnStateKeepsDuplicateDoneIdempotent(t *testing.T) {
	sink := &tabEventSink{}
	now := time.Now().UTC()
	first := sink.trackOwnerTurn(event.TurnStarted, now)
	done := sink.trackOwnerTurn(event.TurnDone, now.Add(time.Minute))
	duplicate := sink.trackOwnerTurn(event.TurnDone, now.Add(2*time.Minute))
	next := sink.trackOwnerTurn(event.TurnStarted, now.Add(3*time.Minute))
	if first.sequence != 1 || done != first || duplicate != first || next.sequence != 2 || !next.startedAt.Equal(now.Add(3*time.Minute)) {
		t.Fatalf("states first=%+v done=%+v duplicate=%+v next=%+v", first, done, duplicate, next)
	}
}
