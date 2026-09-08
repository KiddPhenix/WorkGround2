package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/control"
	"workground2/internal/event"
)

// Keep the surrounding model turn active while using the real ask lifecycle.
// No external provider is needed to simulate a slow model call after answering.
type answeringCtrl struct{ *control.Controller }

func (c answeringCtrl) RuntimeStatus() control.RuntimeStatus {
	status := c.Controller.RuntimeStatus()
	status.Running = true
	status.ForegroundActive = true
	status.RunningWork = !status.PendingPrompt
	status.ActiveRuntimeWork = !status.PendingPrompt
	return status
}

func TestDesktopIconAnswerRefreshSkipsDiscovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	asks := make(chan event.Ask, 1)
	ctrl := control.New(control.Options{SessionPath: path, SessionDir: dir, Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.AskRequest {
			asks <- e.Ask
		}
	})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = ctrl.Ask(ctx, []event.AskQuestion{{ID: "q", Prompt: "Pick", Options: []event.AskOption{{Label: "Yes"}}}})
	}()
	select {
	case <-asks:
	case <-time.After(2 * time.Second):
		t.Fatal("missing ask")
	}
	app := newSummaryTestApp(t, &WorkspaceTab{ID: "tab", SessionPath: path, Ctrl: answeringCtrl{ctrl}}, fakeCompletionSummaryGenerator{})
	app.desktopIconProjectTree = func() []ProjectNode {
		t.Fatal("answer refresh waited for unrelated Session discovery")
		return nil
	}
	snapshot := app.GetDesktopIconEntrySnapshot()
	item := findDesktopIconItem(snapshot.Items, "task:tab")
	if item == nil || len(item.Notifications) == 0 {
		t.Fatalf("missing pending task: %+v", snapshot.Items)
	}
	input := DesktopIconActionInput{ItemID: item.ID, NoticeID: item.Notifications[0].ID, Revision: item.Revision, RequestID: "answer-refresh", Action: "answer", Answers: []QuestionAnswer{{QuestionID: "q", Selected: []string{"Yes"}}}}
	for _, want := range []string{"accepted", "already_applied"} {
		result := app.ApplyDesktopIconAction(input)
		if result.Status != want {
			t.Fatalf("answer status = %s: %s; want %s", result.Status, result.Error, want)
		}
		found := false
		for _, next := range result.Snapshot.Items {
			if next.ID != item.ID {
				continue
			}
			found = true
			if next.Status == "needs_input" {
				t.Fatal("resolved task still waiting for input")
			}
			if next.UnreadCount != 0 {
				t.Fatalf("resolved ask badge remains: %d", next.UnreadCount)
			}
			for _, notice := range next.Notifications {
				if notice.Kind == "needs_input" {
					t.Fatal("resolved ask still displayed")
				}
			}
		}
		if !found {
			t.Fatal("answer removed the task instead of updating its state")
		}
	}
}
