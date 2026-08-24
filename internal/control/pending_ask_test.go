package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/store"
)

type countingRunner struct {
	mu     sync.Mutex
	calls  int
	inputs []string
}

type blockingAskRecoveryRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingAskRecoveryRunner) Run(context.Context, string) error {
	close(r.started)
	<-r.release
	return nil
}

type failingAskRecoveryRunner struct{}

func (failingAskRecoveryRunner) Run(context.Context, string) error {
	return errors.New("recovery failed")
}

func (r *countingRunner) Run(_ context.Context, input string) error {
	r.mu.Lock()
	r.calls++
	r.inputs = append(r.inputs, input)
	r.mu.Unlock()
	return nil
}

func (r *countingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *countingRunner) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.inputs) == 0 {
		return ""
	}
	return r.inputs[len(r.inputs)-1]
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func testQuestions() []event.AskQuestion {
	return []event.AskQuestion{{
		ID:      "q1",
		Header:  "Library",
		Prompt:  "Which library?",
		Options: []event.AskOption{{Label: "A", Description: "first"}, {Label: "B"}},
		Multi:   false,
	}}
}

func testAnswers() []event.AskAnswer {
	return []event.AskAnswer{{QuestionID: "q1", Selected: []string{"A"}}}
}

// TestPendingAskPersistAndHydrateRoundTrip proves the durable sidecar survives a
// process boundary: persist writes the exact question, and loadPendingAsk
// re-projects it through PendingInteraction / PendingPrompt / RuntimeStatus.
func TestPendingAskPersistAndHydrateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	c := New(Options{SessionPath: path, SessionDir: dir})

	if err := c.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); err != nil {
		t.Fatalf("pending-ask sidecar not written: %v", err)
	}

	c.loadPendingAsk(path)

	pending, ok := c.PendingInteraction()
	if !ok || pending.Kind != PendingInteractionAsk {
		t.Fatalf("PendingInteraction = %+v, %v; want recovered ask", pending, ok)
	}
	if pending.Ask.ID != "7" || len(pending.Ask.Questions) != 1 || pending.Ask.Questions[0].Prompt != "Which library?" {
		t.Fatalf("recovered ask = %+v, want id 7 and original question", pending.Ask)
	}
	if !c.PendingPrompt() {
		t.Fatalf("PendingPrompt = false, want true after hydrate")
	}
	if got := c.RuntimeStatus().Mode; got != RuntimeModeWaitingUser {
		t.Fatalf("RuntimeStatus mode = %q, want waiting_user", got)
	}
}

func TestResumeRestoresPendingAsk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	seed := New(Options{SessionPath: path, SessionDir: dir})
	if err := seed.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}

	loaded := agent.NewSession("sys")
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	c := New(Options{Executor: exec, SessionDir: dir, Label: "test"})
	c.Resume(loaded, path)

	pending, ok := c.PendingInteraction()
	if !ok || pending.Kind != PendingInteractionAsk || pending.Ask.ID != "7" {
		t.Fatalf("PendingInteraction after Resume = %+v, %v; want recovered ask 7", pending, ok)
	}
}

// TestRecoveredAskAnswerRunsExactlyOnce proves answering a recovered ask starts
// exactly one recovery turn, clears the sidecar, and rejects a repeat answer.
func TestRecoveredAskAnswerRunsExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	runner := &countingRunner{}
	c := New(Options{Runner: runner, SessionPath: path, SessionDir: dir})
	defer c.autosaveWG.Wait()

	if err := c.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}
	c.loadPendingAsk(path)

	if !c.ResolveQuestion("7", testAnswers()) {
		t.Fatalf("ResolveQuestion(recovered) = false, want true")
	}
	waitUntil(t, 2*time.Second, func() bool { return runner.count() == 1 })
	time.Sleep(20 * time.Millisecond)
	if runner.count() != 1 {
		t.Fatalf("recovery turn ran %d times, want exactly 1", runner.count())
	}
	if got := runner.last(); !strings.Contains(got, "Which library?") || !strings.Contains(got, "A") {
		t.Fatalf("recovery prompt missing question/answer: %q", got)
	}
	if got := askRecoveryDisplay(testQuestions(), testAnswers()); got != "已回答重启前的问题：Library：A" {
		t.Fatalf("recovery display = %q", got)
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); !os.IsNotExist(err) {
		t.Fatalf("sidecar still present after answer: %v", err)
	}
	if _, ok := c.PendingInteraction(); ok {
		t.Fatalf("PendingInteraction still set after answer")
	}
	// Duplicate and late answers must not start another turn.
	if c.ResolveQuestion("7", testAnswers()) {
		t.Fatalf("duplicate ResolveQuestion returned true, want false")
	}
	if c.ResolveQuestion("999", testAnswers()) {
		t.Fatalf("late/unknown ResolveQuestion returned true, want false")
	}
	if runner.count() != 1 {
		t.Fatalf("duplicate/late answer started another turn: %d calls", runner.count())
	}
}

func TestRecoveredAskRecordsConciseDisplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	c := New(Options{Runner: appendingRunner{session: sess}, Executor: exec, SessionPath: path, SessionDir: dir})
	defer c.autosaveWG.Wait()
	var recordedContent, recordedDisplay string
	c.SetDisplayRecorder(func(content, display string) {
		recordedContent, recordedDisplay = content, display
	})
	if err := c.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}
	c.loadPendingAsk(path)

	if !c.ResolveQuestion("7", testAnswers()) {
		t.Fatal("ResolveQuestion(recovered) = false")
	}
	waitUntil(t, 2*time.Second, func() bool { return !c.Running() })

	if !strings.Contains(recordedContent, "The session was interrupted") {
		t.Fatalf("recorded content lost recovery context: %q", recordedContent)
	}
	if recordedDisplay != "已回答重启前的问题：Library：A" {
		t.Fatalf("recorded display = %q", recordedDisplay)
	}
}

func TestRecoveredAskKeepsSidecarUntilTurnCompletes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	runner := &blockingAskRecoveryRunner{started: make(chan struct{}), release: make(chan struct{})}
	c := New(Options{Runner: runner, SessionPath: path, SessionDir: dir})
	defer c.autosaveWG.Wait()
	if err := c.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}
	c.loadPendingAsk(path)

	if !c.ResolveQuestion("7", testAnswers()) {
		t.Fatal("ResolveQuestion(recovered) = false")
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery turn did not start")
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); err != nil {
		t.Fatalf("sidecar was cleared before recovery completed: %v", err)
	}
	close(runner.release)
	waitUntil(t, 2*time.Second, func() bool { return !c.Running() })
	if _, err := os.Stat(store.SessionPendingAsk(path)); !os.IsNotExist(err) {
		t.Fatalf("sidecar still exists after successful recovery: %v", err)
	}
}

func TestRecoveredAskFailureRestoresRetryablePrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	asks := make(chan event.Ask, 1)
	c := New(Options{Runner: failingAskRecoveryRunner{}, SessionPath: path, SessionDir: dir, Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.AskRequest {
			asks <- e.Ask
		}
	})})
	defer c.autosaveWG.Wait()
	if err := c.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}
	c.loadPendingAsk(path)

	if !c.ResolveQuestion("7", testAnswers()) {
		t.Fatal("ResolveQuestion(recovered) = false")
	}
	waitUntil(t, 2*time.Second, func() bool { return !c.Running() })
	pending, ok := c.PendingInteraction()
	if !ok || pending.Ask.ID != "7" {
		t.Fatalf("failed recovery did not restore prompt: %+v, %v", pending, ok)
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); err != nil {
		t.Fatalf("failed recovery lost sidecar: %v", err)
	}
	select {
	case ask := <-asks:
		if ask.ID != "7" {
			t.Fatalf("re-emitted ask ID = %q", ask.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed recovery did not re-emit AskRequest")
	}
}

// TestRecoveredAskReplayReEmitsWithoutRunning proves a recovered ask re-emits its
// AskRequest for a reconnected frontend but never starts a turn on its own.
func TestRecoveredAskReplayReEmitsWithoutRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	asks := make(chan event.Ask, 4)
	runner := &countingRunner{}
	c := New(Options{Runner: runner, SessionPath: path, SessionDir: dir, Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.AskRequest {
			asks <- e.Ask
		}
	})})

	if err := c.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}
	c.loadPendingAsk(path)
	c.ReplayPendingPrompts()

	select {
	case a := <-asks:
		if a.ID != "7" || len(a.Questions) != 1 {
			t.Fatalf("replayed ask = %+v, want id 7", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no AskRequest re-emitted")
	}
	if runner.count() != 0 {
		t.Fatalf("replay started a turn: %d calls", runner.count())
	}
}

// TestRecoveredAskCancelConvergesSidecar proves an explicit cancel of a recovered
// ask (no running goroutine) still drops the durable sidecar.
func TestRecoveredAskCancelConvergesSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	c := New(Options{SessionPath: path, SessionDir: dir})

	if err := c.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}
	c.loadPendingAsk(path)

	c.Cancel()

	if _, ok := c.PendingInteraction(); ok {
		t.Fatalf("PendingInteraction still set after cancel")
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); !os.IsNotExist(err) {
		t.Fatalf("sidecar still present after cancel: %v", err)
	}
}

// TestLiveAskAnswerConvergesSidecar proves the normal runtime path writes the
// sidecar before emitting, and answering through the live reply channel converges
// both in-memory and durable state.
func TestLiveAskAnswerConvergesSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	asks := make(chan event.Ask, 4)
	c := New(Options{SessionPath: path, SessionDir: dir, Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.AskRequest {
			asks <- e.Ask
		}
	})})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Ask(context.Background(), testQuestions())
	}()

	var id string
	select {
	case a := <-asks:
		id = a.ID
	case <-time.After(2 * time.Second):
		t.Fatalf("no AskRequest emitted")
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); err != nil {
		t.Fatalf("live ask did not persist sidecar: %v", err)
	}

	c.AnswerQuestion(id, testAnswers())
	<-done

	if _, ok := c.PendingInteraction(); ok {
		t.Fatalf("PendingInteraction still set after answer")
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); !os.IsNotExist(err) {
		t.Fatalf("sidecar still present after answer: %v", err)
	}
}

// TestAskContextCancelKeepsSidecar proves a non-answer context cancellation (the
// process-shutdown case) clears the in-memory prompt but deliberately keeps the
// durable sidecar so the unanswered question survives a restart.
func TestAskContextCancelKeepsSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	asks := make(chan event.Ask, 4)
	c := New(Options{SessionPath: path, SessionDir: dir, Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.AskRequest {
			asks <- e.Ask
		}
	})})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Ask(ctx, testQuestions())
	}()

	select {
	case <-asks:
	case <-time.After(2 * time.Second):
		t.Fatalf("no AskRequest emitted")
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); err != nil {
		t.Fatalf("live ask did not persist sidecar: %v", err)
	}

	cancel()
	<-done

	if _, ok := c.PendingInteraction(); ok {
		t.Fatalf("PendingInteraction still set after context cancel")
	}
	if _, err := os.Stat(store.SessionPendingAsk(path)); err != nil {
		t.Fatalf("context cancel wrongly deleted the unanswered sidecar: %v", err)
	}
}

// TestLoadPendingAskDiscardsCorruptSidecar proves a torn/corrupt sidecar is
// dropped (and removed) rather than resurrecting a broken prompt forever.
func TestLoadPendingAskDiscardsCorruptSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	sidecar := store.SessionPendingAsk(path)
	if err := os.WriteFile(sidecar, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt sidecar: %v", err)
	}
	c := New(Options{SessionPath: path, SessionDir: dir})

	c.loadPendingAsk(path)

	if _, ok := c.PendingInteraction(); ok {
		t.Fatalf("corrupt sidecar produced a pending interaction")
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("corrupt sidecar not removed: %v", err)
	}
}

func TestPendingAskFollowsRecoveryBranch(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "session.jsonl")
	to := filepath.Join(dir, "session-recovery.jsonl")
	c := New(Options{SessionPath: from, SessionDir: dir})
	if err := c.persistPendingAsk("7", testQuestions()); err != nil {
		t.Fatalf("persistPendingAsk: %v", err)
	}

	c.transplantPendingAskSidecar(from, to)

	if _, err := os.Stat(store.SessionPendingAsk(from)); !os.IsNotExist(err) {
		t.Fatalf("source pending ask still exists: %v", err)
	}
	data, err := os.ReadFile(store.SessionPendingAsk(to))
	if err != nil {
		t.Fatalf("recovery pending ask missing: %v", err)
	}
	if !strings.Contains(string(data), `"id": "7"`) {
		t.Fatalf("recovery pending ask changed: %s", data)
	}
}

func TestPendingAskTransplantDoesNotOverwriteDifferentQuestion(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "session.jsonl")
	to := filepath.Join(dir, "session-recovery.jsonl")
	fromData := []byte(`{"id":"7","questions":[{"id":"q1"}]}`)
	toData := []byte(`{"id":"8","questions":[{"id":"q2"}]}`)
	if err := os.WriteFile(store.SessionPendingAsk(from), fromData, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(store.SessionPendingAsk(to), toData, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	c := New(Options{SessionPath: from, SessionDir: dir})

	c.transplantPendingAskSidecar(from, to)

	got, err := os.ReadFile(store.SessionPendingAsk(to))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(toData) {
		t.Fatalf("target was overwritten: %s", got)
	}
	if _, err := os.Stat(store.SessionPendingAsk(from)); err != nil {
		t.Fatalf("source should remain retryable: %v", err)
	}
}
