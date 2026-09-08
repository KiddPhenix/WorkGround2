package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent/testutil"
	"workground2/internal/event"
	"workground2/internal/memorycompiler"
	"workground2/internal/provider"
)

type waitingClassifier struct {
	entered chan struct{}
	release chan struct{}
}

func (c waitingClassifier) IsTask(ctx context.Context, _ string) (bool, error) {
	close(c.entered)
	select {
	case <-c.release:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func TestPromptVisibleDuringPreparation(t *testing.T) {
	rt := memorycompiler.New(t.TempDir())
	_, seed := rt.StartTurn(context.Background(), "fix a bug", nil)
	seed.RecordToolResults([]memorycompiler.ToolRecord{
		{Name: "bash", Error: "exit status 1"},
		{Name: "bash", Error: "exit status 1"},
	})
	seed.Finish(nil)
	session := NewSession("system")
	mp := testutil.NewMock("m", testutil.Turn{Text: "done"})
	started := make(chan []provider.Message, 1)
	a := New(mp, echoRegistry(), session, Options{
		MemoryCompiler: rt, MemoryCompilerVerbosity: MemoryCompilerVerbosityCompact,
	}, event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnStarted {
			started <- session.Snapshot()
		}
	}))
	classifier := waitingClassifier{entered: make(chan struct{}), release: make(chan struct{})}
	a.classifier = classifier
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, "fix the prompt visibility bug") }()
	select {
	case <-classifier.entered:
	case <-ctx.Done():
		t.Fatal("classification did not start")
	}
	assertUser := func(msgs []provider.Message) {
		t.Helper()
		if len(msgs) != 2 || msgs[1].Role != provider.RoleUser || !strings.Contains(msgs[1].Content, "fix the prompt visibility bug") {
			t.Fatalf("accepted prompt missing during preparation: %+v", msgs)
		}
	}
	assertUser(<-started)
	// Reopening through Controller.History reads this snapshot even though
	// the classification call has not returned and no model request was sent.
	assertUser(session.Snapshot())
	if len(mp.Requests()) != 0 {
		t.Fatal("model request started before preparation completed")
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	close(classifier.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	user := lastUserMessage(t, mp.Requests())
	if !strings.Contains(user.Content, "<memory-compiler-execution>") {
		t.Fatal("memory preparation did not reach the model")
	}
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, msg := range loaded.Snapshot() {
		if msg.Role == provider.RoleUser {
			users++
			if msg.Content != user.Content {
				t.Fatal("saved prompt did not converge to prepared content")
			}
		}
	}
	if users != 1 {
		t.Fatalf("saved user messages = %d, want 1", users)
	}
}
