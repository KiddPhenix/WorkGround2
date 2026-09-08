package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/event"
)

func TestAskResolutionPublishesStateBeforeResume(t *testing.T) {
	dir := t.TempDir()
	asks := make(chan event.Ask, 2)
	updates := make(chan event.TaskMemory, 2)
	var c *Controller
	c = New(Options{SessionPath: filepath.Join(dir, "session.jsonl"), SessionDir: dir, Sink: event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.AskRequest:
			asks <- e.Ask
		case event.TaskMemoryUpdated:
			if _, pending := c.PendingInteraction(); pending {
				t.Error("resolved notification still exposes a pending ask")
			}
			updates <- e.TaskMemory
		}
	})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 2 {
			if _, err := c.Ask(ctx, testQuestions()); err != nil {
				return
			}
		}
	}()
	nextAsk := func() event.Ask {
		t.Helper()
		select {
		case ask := <-asks:
			return ask
		case <-time.After(2 * time.Second):
			t.Fatal("missing ask")
			return event.Ask{}
		}
	}
	first := nextAsk()
	if !c.ResolveQuestion(first.ID, testAnswers()) {
		t.Fatal("answer rejected")
	}
	select {
	case memory := <-updates:
		if memory.CurrentSource != "runtime" || memory.NextStep != "" {
			t.Fatalf("answer did not publish running state: %+v", memory)
		}
	default:
		t.Fatal("resolution returned without notifying other frontends")
	}
	second := nextAsk()
	if c.ResolveQuestion(first.ID, testAnswers()) {
		t.Fatal("duplicate answer accepted")
	}
	if pending, ok := c.PendingInteraction(); !ok || pending.Ask.ID != second.ID {
		t.Fatal("old answer cleared the new ask")
	}
	select {
	case <-updates:
		t.Fatal("duplicate answer emitted a stale state update")
	default:
	}
	c.ResolveQuestion(second.ID, testAnswers())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ask waiter did not finish")
	}
}
