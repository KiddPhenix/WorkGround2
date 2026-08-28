package assistant

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/event"
)

// fakeSupervisorHost simulates the host side of the supervisor loop: an
// atomically-created supervisor Session plus a recorded turn outcome. The
// outcome is returned for both RunSupervisorTurn and SettleSupervisorTurn so
// tests can drive budget-expiry (Running) and restart-settle flows.
type fakeSupervisorHost struct {
	mu         sync.Mutex
	ref        SupervisorSessionRef
	created    int
	managed    []ManagedSessionSummary
	outcome    SupervisorTurnOutcome
	prompts    []string
	lastRef    SupervisorSessionRef
	runCalls   int
	historyLen int
	historyErr error
	// panicOnRun simulates a process crash inside RunSupervisorTurn: the intent
	// checkpoint was already saved, but no submission durably landed.
	panicOnRun bool
}

func (h *fakeSupervisorHost) FindSupervisorSession(assistantID string) (SupervisorSessionRef, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ref.ID == "" {
		return SupervisorSessionRef{}, false
	}
	return h.ref, true
}

func (h *fakeSupervisorHost) EnsureSupervisorSession(a Assistant) (SupervisorSessionRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.created++
	if h.ref.ID == "" {
		h.ref = SupervisorSessionRef{ID: "supervisor-" + a.ID, Path: "/tmp/supervisor-" + a.ID + ".jsonl"}
	}
	return h.ref, nil
}

func (h *fakeSupervisorHost) ManagedSessions(assistantID string) []ManagedSessionSummary {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ManagedSessionSummary(nil), h.managed...)
}

func (h *fakeSupervisorHost) RunSupervisorTurn(ref SupervisorSessionRef, prompt string, budget time.Duration) SupervisorTurnOutcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.panicOnRun {
		panic("assistant: simulated crash after intent checkpoint, before submit")
	}
	h.prompts = append(h.prompts, prompt)
	h.lastRef = ref
	h.runCalls++
	return h.outcome
}

func (h *fakeSupervisorHost) SupervisorHistoryLen(ref SupervisorSessionRef) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastRef = ref
	if h.historyErr != nil {
		return 0, h.historyErr
	}
	return h.historyLen, nil
}

func (h *fakeSupervisorHost) SettleSupervisorTurn(ref SupervisorSessionRef) SupervisorTurnOutcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastRef = ref
	return h.outcome
}

func (h *fakeSupervisorHost) setOutcome(outcome SupervisorTurnOutcome) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outcome = outcome
}

func (h *fakeSupervisorHost) submitCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runCalls
}

// fakeSessionControl records acting-phase intents for routing tests.
type fakeSessionControl struct {
	mu       sync.Mutex
	creates  []SessionCreateRequest
	steers   []string
	answers  []string
	steerErr error // injected steer failure for route-failure tests
}

func (c *fakeSessionControl) Create(req SessionCreateRequest) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creates = append(c.creates, req)
	return "managed-" + req.RequestID, nil
}
func (c *fakeSessionControl) Steer(sessionID, text, requestID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steers = append(c.steers, sessionID)
	if c.steerErr != nil {
		return c.steerErr
	}
	return nil
}
func (c *fakeSessionControl) AnswerQuestion(sessionID, questionID string, answers []event.AskAnswer, requestID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answers = append(c.answers, sessionID)
	return nil
}
func (c *fakeSessionControl) Cancel(string, string) error         { return nil }
func (c *fakeSessionControl) Resume(string, string) error         { return nil }
func (c *fakeSessionControl) Retry(string, string) error          { return nil }
func (c *fakeSessionControl) Fork(string, string) (string, error) { return "", nil }
func (c *fakeSessionControl) PendingInteractions(string) ([]SessionInteraction, error) {
	return nil, nil
}

func (c *fakeSessionControl) createdCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.creates)
}

func newTestExecutor(t *testing.T, store *Store, host *fakeSupervisorHost, control *fakeSessionControl) *SupervisorExecutor {
	t.Helper()
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	ex, err := NewSupervisorExecutor(SupervisorExecutorOptions{
		Store: store, Events: q, Host: host,
		Control:           func() SessionControl { return control },
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// TestSupervisorExecutorEnsureAtomicConcurrent proves concurrent ensure passes
// create exactly one supervisor Session: the host contract is atomic, so racing
// find-then-create from many goroutines resolves to a single Session.
func TestSupervisorExecutorEnsureAtomicConcurrent(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{}

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ex := newTestExecutor(t, store, host, &fakeSessionControl{})
			ex.EnsureSupervisorSessions([]Assistant{snapshot.Assistant})
		}()
	}
	wg.Wait()
	if host.created != 1 {
		t.Fatalf("supervisor sessions created = %d, want exactly 1", host.created)
	}
}

// TestSupervisorExecutorRunTurnRoutesThroughSession proves a real supervisor
// turn: pending events gate the turn, the host runs it through the supervisor
// Session Controller with the bounded context prompt, the decision is parsed
// from the turn's final message, the acting phase routes through the Session
// subsystem with a stable request ID, and the trigger events are consumed.
func TestSupervisorExecutorRunTurnRoutesThroughSession(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-a")
	now := time.Now()
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "plan-e2e", AssistantID: snapshot.Assistant.ID,
		Progress: ProgressBlock{
			PlanRevision:     snapshot.Plan.Revision,
			Responsibilities: []RespDecl{{Alias: "scan", Objective: "Scan", NextAction: "run the scan"}},
		},
		Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	host := &fakeSupervisorHost{ref: SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}}
	host.outcome = SupervisorTurnOutcome{Text: `{"action":"advance","target":"scan","rationale":"scan is ready"}`}
	control := &fakeSessionControl{}
	ex := newTestExecutor(t, store, host, control)

	if err := ex.EnqueueUserInput("helper-a", "req-1", "scan now"); err != nil {
		t.Fatal(err)
	}
	ex.RunTurns(context.Background(), now)

	if len(host.prompts) != 1 {
		t.Fatalf("supervisor turns run = %d, want 1", len(host.prompts))
	}
	prompt := host.prompts[0]
	if !strings.Contains(prompt, "使命：") || !strings.Contains(prompt, "待处理事件") || !strings.Contains(prompt, "user_input") {
		t.Fatalf("prompt lacks the bounded context or event summary: %.200s", prompt)
	}
	if host.lastRef.ID != "supervisor-helper-a" {
		t.Fatalf("turn ran on %+v, want the supervisor session", host.lastRef)
	}
	if control.createdCount() != 1 {
		t.Fatalf("advance created %d managed sessions, want 1", control.createdCount())
	}
	if control.creates[0].Purpose != "managed" || control.creates[0].OwnerID != "helper-a" {
		t.Fatalf("create = %+v", control.creates[0])
	}
	if control.creates[0].RequestID == "" {
		t.Fatal("advance create has no stable request id (replays could double-create)")
	}
	hasPending, err := ex.events.HasPending("helper-a")
	if err != nil || hasPending {
		t.Fatalf("events pending after routed turn: %v err=%v", hasPending, err)
	}
	cycle, ok := store.LatestCycle("helper-a")
	if !ok || cycle.State != CycleCompleted {
		t.Fatalf("cycle after routed turn = %+v ok=%v, want completed", cycle, ok)
	}
}

// TestSupervisorExecutorTurnFailureKeepsEventsPending proves a failed or
// unresolved turn never loses its trigger events: they stay pending so the next
// tick retries, and no acting phase runs.
func TestSupervisorExecutorTurnFailureKeepsEventsPending(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{ref: SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}}
	host.outcome = SupervisorTurnOutcome{Err: context.DeadlineExceeded}
	control := &fakeSessionControl{}
	ex := newTestExecutor(t, store, host, control)
	if err := ex.EnqueueUserInput("helper-a", "req-1", "scan"); err != nil {
		t.Fatal(err)
	}
	ex.RunTurns(context.Background(), time.Now())
	if control.createdCount() != 0 {
		t.Fatalf("failed turn still acted: %d creates", control.createdCount())
	}
	hasPending, err := ex.events.HasPending("helper-a")
	if err != nil || !hasPending {
		t.Fatalf("events lost after failed turn: pending=%v err=%v", hasPending, err)
	}
}

// TestSupervisorExecutorPendingInteractionConsumesEvents proves a turn that
// ends waiting on a PendingInteraction (the supervisor asked the user) consumes
// its trigger events without routing a decision; the user's answer wakes the
// next turn as a user_input event.
func TestSupervisorExecutorPendingInteractionConsumesEvents(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{ref: SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}}
	host.outcome = SupervisorTurnOutcome{Pending: true}
	control := &fakeSessionControl{}
	ex := newTestExecutor(t, store, host, control)
	if err := ex.EnqueueUserInput("helper-a", "req-1", "go"); err != nil {
		t.Fatal(err)
	}
	ex.RunTurns(context.Background(), time.Now())
	hasPending, err := ex.events.HasPending("helper-a")
	if err != nil || hasPending {
		t.Fatalf("events still pending after pending-interaction turn: %v err=%v", hasPending, err)
	}
	if control.createdCount() != 0 {
		t.Fatalf("pending turn acted: %d creates", control.createdCount())
	}
}

// TestSupervisorExecutorCollectEventsEnqueuesLifecycleTransitions proves the
// durable diff: a managed Session appearing as running enqueues session_started,
// later completing enqueues session_completed, and a second collect with no
// change enqueues nothing (no duplicates).
func TestSupervisorExecutorCollectEventsEnqueuesLifecycleTransitions(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{}
	host.managed = []ManagedSessionSummary{{ID: "sess-1", Path: "/tmp/sess-1.jsonl", Status: "running", Turns: 1}}
	ex := newTestExecutor(t, store, host, &fakeSessionControl{})

	if n := ex.CollectSupervisorEvents(time.Now()); n != 1 {
		t.Fatalf("first collect enqueued %d events, want 1 (started)", n)
	}
	events, _ := ex.events.Pending("helper-a")
	if len(events) != 1 || events[0].Kind != EventSessionStarted {
		t.Fatalf("pending = %+v, want session_started", events)
	}
	// Same state again: nothing new.
	if n := ex.CollectSupervisorEvents(time.Now()); n != 0 {
		t.Fatalf("second collect enqueued %d events, want 0", n)
	}
	// Session completes: a terminal transition is enqueued exactly once.
	host.managed = []ManagedSessionSummary{{ID: "sess-1", Path: "/tmp/sess-1.jsonl", Status: "completed", Turns: 3}}
	if n := ex.CollectSupervisorEvents(time.Now()); n != 1 {
		t.Fatalf("completion collect enqueued %d events, want 1", n)
	}
	host.managed = []ManagedSessionSummary{{ID: "sess-1", Path: "/tmp/sess-1.jsonl", Status: "completed", Turns: 3}}
	if n := ex.CollectSupervisorEvents(time.Now()); n != 0 {
		t.Fatalf("repeat completion collect enqueued %d events, want 0", n)
	}
	events, _ = ex.events.Pending("helper-a")
	kinds := map[SupervisorEventKind]int{}
	for _, ev := range events {
		kinds[ev.Kind]++
	}
	if kinds[EventSessionStarted] != 1 || kinds[EventSessionCompleted] != 1 {
		t.Fatalf("pending kinds = %+v, want started+completed exactly once each", kinds)
	}
}

// TestSupervisorExecutorCollectEventsRestartNonLossy proves the non-lossy
// contract: when the collector crashes between enqueue and state save, a fresh
// executor over the same root re-derives the transition and the queue dedups it
// — the event is present exactly once, never lost.
func TestSupervisorExecutorCollectEventsRestartNonLossy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")

	host := &fakeSupervisorHost{managed: []ManagedSessionSummary{{ID: "sess-1", Path: "/tmp/sess-1.jsonl", Status: "running", Turns: 1}}}
	q1, _ := NewSupervisorEventQueue(root)
	ex1, _ := NewSupervisorExecutor(SupervisorExecutorOptions{
		Store: store, Events: q1, Host: host, HeartbeatInterval: time.Hour,
	})
	ex1.CollectSupervisorEvents(time.Now())

	// Simulated crash: the event was enqueued but the state sidecar never
	// advanced (revert it to empty). A fresh executor re-diffs the same
	// transition; the queue dedups the identical event instead of losing it.
	_ = q1.SaveState("helper-a", SupervisorEventState{})

	q2, _ := NewSupervisorEventQueue(root)
	ex2, _ := NewSupervisorExecutor(SupervisorExecutorOptions{
		Store: store, Events: q2, Host: host, HeartbeatInterval: time.Hour,
	})
	ex2.CollectSupervisorEvents(time.Now())
	events, _ := q2.Pending("helper-a")
	if len(events) != 1 || events[0].Kind != EventSessionStarted {
		t.Fatalf("pending after restart = %+v, want exactly one session_started", events)
	}
}

// TestSupervisorExecutorCollectEventsHeartbeat proves a long idle assistant
// receives exactly one heartbeat event, and only after the interval elapsed.
func TestSupervisorExecutorCollectEventsHeartbeat(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{}
	q, _ := NewSupervisorEventQueue(store.Root())
	ex, err := NewSupervisorExecutor(SupervisorExecutorOptions{
		Store: store, Events: q, Host: host, HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if n := ex.CollectSupervisorEvents(now); n != 1 {
		t.Fatalf("idle collect enqueued %d, want 1 heartbeat", n)
	}
	if n := ex.CollectSupervisorEvents(now.Add(10 * time.Minute)); n != 0 {
		t.Fatalf("repeat collect enqueued %d, want 0 before interval", n)
	}
	// The first heartbeat was consumed by a turn; the next interval passes and
	// a fresh heartbeat is enqueued.
	events, _ := q.Pending("helper-a")
	if err := q.MarkConsumed("helper-a", events[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := ex.CollectSupervisorEvents(now.Add(2 * time.Hour)); n != 1 {
		t.Fatalf("post-interval collect enqueued %d, want 1 heartbeat", n)
	}
}
