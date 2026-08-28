package assistant

// gap_g_test.go — 验收测试：自动代答与完整实验编排（设计文档 17.5 / 缺口 G）。
// 覆盖：高置信直接答；低置信至少双候选隔离；候选部分失败/全失败/超时；
// 跨重启恢复；乱序/重复完成不倒退；赢家回答与败者回滚；硬门槛不自动答；
// 等待硬门槛时其他计划推进；Experiment 持久化字段与 revision/fence 语义。

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/event"
)

// gapGControl is the session-control fake for the auto-answer acceptance tests:
// it records forks/answers/cancels and serves pending interactions plus the
// real per-fork status the winner sweep consumes (it plays the role of the
// Session subsystem, which is the single source of truth for fork state).
type gapGControl struct {
	mu        sync.Mutex
	nextFork  int
	forked    []string
	answered  []gapGAnswer
	cancelled []string
	pending   map[string][]SessionInteraction
	statuses  map[string]string
}

type gapGAnswer struct {
	session string
	answers []event.AskAnswer
}

func newGapGControl() *gapGControl {
	return &gapGControl{
		pending:  map[string][]SessionInteraction{},
		statuses: map[string]string{},
	}
}

func (c *gapGControl) Create(req SessionCreateRequest) (string, error) {
	return "managed-" + req.RequestID, nil
}
func (c *gapGControl) Steer(string, string, string) error { return nil }
func (c *gapGControl) Resume(string, string) error        { return nil }
func (c *gapGControl) Retry(string, string) error         { return nil }

func (c *gapGControl) Fork(sessionID, requestID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextFork++
	id := fmt.Sprintf("fork-%d", c.nextFork)
	c.forked = append(c.forked, id)
	return id, nil
}

func (c *gapGControl) AnswerQuestion(sessionID, _ string, answers []event.AskAnswer, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answered = append(c.answered, gapGAnswer{session: sessionID, answers: answers})
	return nil
}

func (c *gapGControl) Cancel(sessionID, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled = append(c.cancelled, sessionID)
	return nil
}

func (c *gapGControl) PendingInteractions(sessionID string) ([]SessionInteraction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]SessionInteraction(nil), c.pending[sessionID]...), nil
}

func (c *gapGControl) setPending(sessionID string, items ...SessionInteraction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[sessionID] = items
}

func (c *gapGControl) setStatus(id, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[id] = status
}

func (c *gapGControl) statusFor(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.statuses[id]
	return st, ok
}

func (c *gapGControl) answersFor(session string) [][]event.AskAnswer {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out [][]event.AskAnswer
	for _, a := range c.answered {
		if a.session == session {
			out = append(out, a.answers)
		}
	}
	return out
}

func (c *gapGControl) cancelledIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.cancelled...)
}

func (c *gapGControl) forkedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.forked...)
}

var _ SessionControl = (*gapGControl)(nil)

// newGapGExecutor builds the shared supervisor executor over a fresh event
// queue with a real AutoAnswer model, the given session control and trial
// status resolver, and a bounded experiment max age.
func newGapGExecutor(t *testing.T, store *Store, control *gapGControl, host SupervisorHost, model RoleModel, maxAge time.Duration) *SupervisorExecutor {
	t.Helper()
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	autoAnswer, err := NewAutoAnswer(model)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := NewSupervisorExecutor(SupervisorExecutorOptions{
		Store: store, Events: q, Host: host,
		Control:           func() SessionControl { return control },
		AutoAnswer:        func() *AutoAnswer { return autoAnswer },
		TrialStatus:       func() TrialStatusResolver { return control.statusFor },
		ExperimentMaxAge:  maxAge,
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

func lowConfidenceModel(t *testing.T) RoleModel {
	t.Helper()
	return RoleModelFunc(func(_ context.Context, _ string) (string, error) {
		return `{"selected":["Staging"],"confidence":0.5,"rationale":"uncertain between environments"}`, nil
	})
}

func highConfidenceModel(t *testing.T) RoleModel {
	t.Helper()
	return RoleModelFunc(func(_ context.Context, _ string) (string, error) {
		return `{"selected":["Staging"],"confidence":0.95,"rationale":"staging matches the mission"}`, nil
	})
}

// mustNotRunModel fails the test if the auto-answer model is ever consulted
// (used by the hard-gate test: a hard gate must short-circuit before any model
// turn).
func mustNotRunModel(t *testing.T) RoleModel {
	t.Helper()
	return RoleModelFunc(func(_ context.Context, _ string) (string, error) {
		t.Fatal("auto-answer model was consulted for a hard gate")
		return "", errors.New("must not run")
	})
}

func envQuestion() []event.AskQuestion {
	return []event.AskQuestion{{
		ID: "q1", Prompt: "Which environment?", Multi: false,
		Options: []event.AskOption{{Label: "Staging"}, {Label: "Production"}},
	}}
}

func askItem(id string, now time.Time) SessionInteraction {
	return SessionInteraction{
		Kind: "ask", ID: id, DueAt: now.Add(-time.Minute),
		Questions: envQuestion(),
	}
}

func experimentDecisionID(a, session, interaction string) string {
	return StableID("decision", a+"/"+session+"/"+interaction+"/experiment")
}

func experimentID(a, session, interaction string) string {
	return StableID("experiment", a+"/"+session+"/"+interaction)
}

// seedRunningExperiment persists one running experiment decision with two
// candidate trials (the first is the inferred, preferred candidate).
func seedRunningExperiment(t *testing.T, store *Store, a Assistant, session, interaction string, createdAt time.Time) {
	t.Helper()
	_, err := store.RecordInteractionDecision(InteractionDecisionRecord{
		ID:            experimentDecisionID(a.ID, session, interaction),
		AssistantID:   a.ID,
		SessionID:     session,
		InteractionID: interaction,
		Source:        DecisionExperiment,
		Confidence:    0.5,
		Candidates:    []string{"Staging", "Production"},
		Trials: []TrialState{
			{SessionID: "fork-1", Worktree: "session:fork-1", Answer: EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Staging"}}}), Status: TrialStatusRunning},
			{SessionID: "fork-2", Worktree: "session:fork-2", Answer: EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Production"}}}), Status: TrialStatusRunning},
		},
		Result:    "running",
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("seed experiment decision: %v", err)
	}
}

func findExperiment(t *testing.T, store *Store, assistantID, id string) Experiment {
	t.Helper()
	snap, err := store.Get(assistantID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, e := range snap.Experiments {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("experiment %s not found", id)
	return Experiment{}
}

// ---- 验收 1：高置信直接答 ------------------------------------------------

func TestGapGHighConfidenceAnswersDirectly(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, highConfidenceModel(t), time.Hour)

	now := time.Now()
	ex.AutoAnswerInteraction(snapshot.Assistant, "sess-1", askItem("ask-1", now), now)

	answers := control.answersFor("sess-1")
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0].Selected[0] != "Staging" {
		t.Fatalf("original answered %d times with %+v, want once with Staging", len(answers), answers)
	}
	if forked := control.forkedIDs(); len(forked) != 0 {
		t.Fatalf("high-confidence decision forked %v, want no isolation", forked)
	}
	rec, ok, err := store.LatestDecision("helper-g", "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if rec.Source != DecisionInfer || rec.Result != "answered" {
		t.Fatalf("decision = %s result=%s, want infer/answered", rec.Source, rec.Result)
	}
}

// ---- 验收 2：低置信至少双候选隔离 + Experiment 持久 -----------------------

func TestGapGLowConfidenceForksIsolatedCandidatesAndPersistsExperiment(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)

	now := time.Now()
	ex.AutoAnswerInteraction(snapshot.Assistant, "sess-1", askItem("ask-1", now), now)

	// The original session is NOT answered; at least two isolated forks are.
	if answered := control.answersFor("sess-1"); len(answered) != 0 {
		t.Fatalf("original session answered %d times, want none (isolation)", len(answered))
	}
	forks := control.forkedIDs()
	if len(forks) < 2 {
		t.Fatalf("forks = %d, want >= 2 isolated candidates", len(forks))
	}
	seen := map[string]bool{}
	for _, id := range forks {
		if seen[id] {
			t.Fatalf("duplicate fork session %s", id)
		}
		seen[id] = true
	}
	// Each fork was answered with its own candidate answer set (no fake
	// results, no shared workspace).
	var forkAnswers []string
	for _, id := range forks {
		a := control.answersFor(id)
		if len(a) != 1 {
			t.Fatalf("fork %s answered %d times, want once", id, len(a))
		}
		forkAnswers = append(forkAnswers, strings.Join(a[0][0].Selected, ","))
	}
	if len(forkAnswers) != len(forks) {
		t.Fatalf("fork answers = %v, want one per fork", forkAnswers)
	}

	rec, ok, err := store.LatestDecision("helper-g", "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if rec.Source != DecisionExperiment || rec.Result != "running" {
		t.Fatalf("decision = %s result=%s, want experiment/running", rec.Source, rec.Result)
	}
	if len(rec.Trials) < 2 || len(rec.Rollback) == 0 {
		t.Fatalf("trials=%d rollback=%q, want >=2 trials with a rollback point", len(rec.Trials), rec.Rollback)
	}
	for _, tr := range rec.Trials {
		if tr.Status != TrialStatusRunning {
			t.Fatalf("trial %s status = %s, want running", tr.SessionID, tr.Status)
		}
		if !strings.HasPrefix(tr.Worktree, "session:") || tr.Worktree == "session:"+tr.SessionID+":shared" {
			t.Fatalf("trial %s worktree = %q, want its own isolation", tr.SessionID, tr.Worktree)
		}
	}

	// The durable Experiment record carries the full candidate/session/status
	// set with a revision fence.
	exp := findExperiment(t, store, "helper-g", experimentID("helper-g", "sess-1", "ask-1"))
	if exp.Status != ExperimentRunning || exp.Result != "running" {
		t.Fatalf("experiment status=%s result=%s, want running/running", exp.Status, exp.Result)
	}
	if len(exp.Candidates) < 2 || len(exp.Trials) != len(rec.Trials) || exp.Revision < 1 || exp.CreatedAt.IsZero() || exp.UpdatedAt.IsZero() {
		t.Fatalf("experiment record incomplete: candidates=%d trials=%d revision=%d", len(exp.Candidates), len(exp.Trials), exp.Revision)
	}
	if exp.Confidence != 0.5 {
		t.Fatalf("experiment confidence = %v, want 0.5", exp.Confidence)
	}
	if strings.TrimSpace(exp.Hypothesis) == "" {
		t.Fatal("experiment record has no hypothesis")
	}
}

// ---- 验收 3：赢家回答原 Session，败者回滚 ----------------------------------

func TestGapGWinnerAnswersOriginalAndRollsBackLosers(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)

	seedRunningExperiment(t, store, snapshot.Assistant, "sess-1", "ask-1", time.Now().Add(-time.Minute))
	control.setStatus("fork-1", TrialStatusFailed)
	control.setStatus("fork-2", TrialStatusDone)

	ex.ResolveExperimentTrials()

	answers := control.answersFor("sess-1")
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0].Selected[0] != "Production" {
		t.Fatalf("original answered with %+v, want the completed candidate Production", answers)
	}
	cancelled := control.cancelledIDs()
	if len(cancelled) != 1 || cancelled[0] != "fork-1" {
		t.Fatalf("cancelled = %v, want [fork-1]", cancelled)
	}

	rec, ok, err := store.LatestDecision("helper-g", "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if rec.Result != "answered:fork-2" || rec.Winner != "fork-2" || rec.Rollback != "fork-1" {
		t.Fatalf("result=%s winner=%s rollback=%s, want answered:fork-2/fork-2/fork-1", rec.Result, rec.Winner, rec.Rollback)
	}
	if rec.Evidence == "" || rec.Cost == "" || rec.SideEffects == "" {
		t.Fatalf("winner decision missing evidence/cost/side-effects: %+v", rec)
	}
	for _, tr := range rec.Trials {
		if tr.SessionID == "fork-2" && tr.Status != TrialStatusDone {
			t.Fatalf("winner trial status = %s, want done", tr.Status)
		}
		if tr.SessionID == "fork-1" && tr.Status != TrialStatusFailed {
			t.Fatalf("loser trial status = %s, want failed", tr.Status)
		}
	}

	// The Experiment record is concluded with the winner, evidence and
	// rollback point.
	exp := findExperiment(t, store, "helper-g", experimentID("helper-g", "sess-1", "ask-1"))
	if exp.Status != ExperimentConcluded || exp.Result != "answered:fork-2" || exp.Winner != "fork-2" {
		t.Fatalf("experiment status=%s result=%s winner=%s, want concluded/answered:fork-2/fork-2", exp.Status, exp.Result, exp.Winner)
	}
	if exp.Evidence == "" || exp.Cost == "" || exp.SideEffects == "" || exp.Rollback != "fork-1" {
		t.Fatalf("experiment conclusion missing comparison fields: %+v", exp)
	}

	// A duplicate sweep must not re-answer or re-cancel (idempotent winner).
	ex.ResolveExperimentTrials()
	if got := control.answersFor("sess-1"); len(got) != 1 {
		t.Fatalf("re-sweep answered %d times, want 1", len(got))
	}
	if got := control.cancelledIDs(); len(got) != 1 {
		t.Fatalf("re-sweep cancelled %d times, want 1", len(got))
	}
}

// ---- 验收 4：全部完成时按偏好序比较选胜者 --------------------------------

func TestGapGPreferenceOrderWinnerWhenAllComplete(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)

	seedRunningExperiment(t, store, snapshot.Assistant, "sess-1", "ask-1", time.Now().Add(-time.Minute))
	control.setStatus("fork-1", TrialStatusDone)
	control.setStatus("fork-2", TrialStatusDone)

	ex.ResolveExperimentTrials()

	answers := control.answersFor("sess-1")
	if len(answers) != 1 || answers[0][0].Selected[0] != "Staging" {
		t.Fatalf("original answered with %+v, want the preferred (inferred) candidate Staging", answers)
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 1 || cancelled[0] != "fork-2" {
		t.Fatalf("cancelled = %v, want [fork-2]", cancelled)
	}
	rec, _, _ := store.LatestDecision("helper-g", "sess-1", "ask-1")
	if rec.Winner != "fork-1" || !strings.Contains(rec.Evidence, "preference order") {
		t.Fatalf("winner=%s evidence=%q, want fork-1 with preference-order comparison", rec.Winner, rec.Evidence)
	}
}

// ---- 验收 5：候选部分失败时等待仍在跑的候选 ------------------------------

func TestGapGPartialFailureKeepsRaceOpenThenSettles(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)

	seedRunningExperiment(t, store, snapshot.Assistant, "sess-1", "ask-1", time.Now().Add(-time.Minute))
	control.setStatus("fork-1", TrialStatusFailed)
	control.setStatus("fork-2", TrialStatusRunning) // still racing, within max age

	ex.ResolveExperimentTrials()
	if got := control.answersFor("sess-1"); len(got) != 0 {
		t.Fatalf("settled while a candidate still races: %+v", got)
	}
	if got := control.cancelledIDs(); len(got) != 0 {
		t.Fatalf("cancelled while a candidate still races: %v", got)
	}

	// The racing candidate completes: now the race settles on it.
	control.setStatus("fork-2", TrialStatusDone)
	ex.ResolveExperimentTrials()
	answers := control.answersFor("sess-1")
	if len(answers) != 1 || answers[0][0].Selected[0] != "Production" {
		t.Fatalf("original answered with %+v, want the surviving candidate Production", answers)
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 1 || cancelled[0] != "fork-1" {
		t.Fatalf("cancelled = %v, want [fork-1]", cancelled)
	}
}

// ---- 验收 6：候选全失败 -> 最可回滚安全答案继续 ---------------------------

func TestGapGAllCandidatesFailFallsBackToSafestAnswer(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)

	seedRunningExperiment(t, store, snapshot.Assistant, "sess-1", "ask-1", time.Now().Add(-time.Minute))
	control.setStatus("fork-1", TrialStatusFailed)
	control.setStatus("fork-2", TrialStatusFailed)

	ex.ResolveExperimentTrials()

	// The original session is answered with the first (inferred, most
	// rollback-safe) candidate and every fork is cancelled: never permanently
	// pending.
	answers := control.answersFor("sess-1")
	if len(answers) != 1 || answers[0][0].Selected[0] != "Staging" {
		t.Fatalf("fallback answered with %+v, want the inferred candidate Staging", answers)
	}
	cancelled := control.cancelledIDs()
	if len(cancelled) != 2 {
		t.Fatalf("cancelled = %v, want both forks", cancelled)
	}
	rec, ok, err := store.LatestDecision("helper-g", "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if rec.Result != "answered-fallback" || rec.Winner != "" {
		t.Fatalf("result=%s winner=%q, want answered-fallback with no winner", rec.Result, rec.Winner)
	}
	if !strings.Contains(rec.Evidence, "rollback-safe") {
		t.Fatalf("fallback evidence = %q, want the rollback-safe rationale", rec.Evidence)
	}
	exp := findExperiment(t, store, "helper-g", experimentID("helper-g", "sess-1", "ask-1"))
	if exp.Status != ExperimentConcluded || exp.Result != "answered-fallback" {
		t.Fatalf("experiment status=%s result=%s, want concluded/answered-fallback", exp.Status, exp.Result)
	}

	// A duplicate sweep must not fall back twice.
	ex.ResolveExperimentTrials()
	if got := control.answersFor("sess-1"); len(got) != 1 {
		t.Fatalf("re-sweep fallback answered %d times, want 1", len(got))
	}
}

// ---- 验收 7：候选超时 -> 最可回滚安全答案继续 ------------------------------

func TestGapGTimeoutSettlesWithSafestAnswer(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	// A 1-minute race budget with forks created 2 minutes ago: both are
	// over-age and must be treated as timed out (terminal).
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Minute)

	seedRunningExperiment(t, store, snapshot.Assistant, "sess-1", "ask-1", time.Now().Add(-2*time.Minute))
	control.setStatus("fork-1", TrialStatusRunning)
	control.setStatus("fork-2", TrialStatusRunning)

	ex.ResolveExperimentTrials()

	answers := control.answersFor("sess-1")
	if len(answers) != 1 || answers[0][0].Selected[0] != "Staging" {
		t.Fatalf("timeout fallback answered with %+v, want the inferred candidate", answers)
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 2 {
		t.Fatalf("cancelled = %v, want both timed-out forks", cancelled)
	}
	rec, ok, _ := store.LatestDecision("helper-g", "sess-1", "ask-1")
	if !ok || rec.Result != "answered-fallback" {
		t.Fatalf("result = %q, want answered-fallback", rec.Result)
	}
	if !strings.Contains(rec.Cost, "timed_out=2") {
		t.Fatalf("cost = %q, want the timed-out count", rec.Cost)
	}
	for _, tr := range rec.Trials {
		if tr.Status != TrialStatusFailed {
			t.Fatalf("timed-out trial %s status = %s, want failed (terminal)", tr.SessionID, tr.Status)
		}
	}
}

// ---- 验收 8：跨重启恢复未完成候选 -----------------------------------------

func TestGapGRestartRecoversRunningExperiment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()

	ex1 := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)
	now := time.Now()
	ex1.AutoAnswerInteraction(snapshot.Assistant, "sess-1", askItem("ask-1", now), now)
	forks := control.forkedIDs()
	if len(forks) < 2 {
		t.Fatalf("forks = %d, want >= 2", len(forks))
	}
	control.setStatus(forks[0], TrialStatusFailed)
	control.setStatus(forks[1], TrialStatusDone)

	// "Restart": a fresh store and event queue over the same root, a fresh
	// executor. The running experiment is recovered from the durable decision
	// and settled without re-running the model or re-forking.
	store2 := testStore(t, root)
	ex2 := newGapGExecutor(t, store2, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)
	ex2.ResolveExperimentTrials()

	answers := control.answersFor("sess-1")
	if len(answers) != 1 {
		t.Fatalf("restart recovered experiment answered %d times, want 1", len(answers))
	}
	rec, ok, err := store2.LatestDecision("helper-g", "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if rec.Result != "answered:"+forks[1] {
		t.Fatalf("restart result = %s, want answered:%s", rec.Result, forks[1])
	}
	exp := findExperiment(t, store2, "helper-g", experimentID("helper-g", "sess-1", "ask-1"))
	if exp.Status != ExperimentConcluded {
		t.Fatalf("restart experiment status = %s, want concluded", exp.Status)
	}
}

// ---- 验收 9：乱序/重复完成不倒退 ------------------------------------------

func TestGapGDuplicateAndOutOfOrderCompletionNoRegression(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)

	seedRunningExperiment(t, store, snapshot.Assistant, "sess-1", "ask-1", time.Now().Add(-time.Minute))

	// The lower-preference candidate completes first; the preferred one is
	// still racing. Two sweeps observe the same state (duplicate/out-of-order
	// events) and must not settle or regress.
	control.setStatus("fork-2", TrialStatusDone)
	control.setStatus("fork-1", TrialStatusRunning)
	ex.ResolveExperimentTrials()
	ex.ResolveExperimentTrials()
	if got := control.answersFor("sess-1"); len(got) != 0 {
		t.Fatalf("settled before all candidates terminal: %+v", got)
	}

	// The preferred candidate completes out of order: the race settles on it
	// (preference order), the other fork is rolled back.
	control.setStatus("fork-1", TrialStatusDone)
	ex.ResolveExperimentTrials()
	answers := control.answersFor("sess-1")
	if len(answers) != 1 || answers[0][0].Selected[0] != "Staging" {
		t.Fatalf("original answered with %+v, want the preferred candidate Staging", answers)
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 1 || cancelled[0] != "fork-2" {
		t.Fatalf("cancelled = %v, want [fork-2]", cancelled)
	}

	// A late duplicate completion observation (fork-2 "done" again) must not
	// re-answer or flip the settled winner.
	ex.ResolveExperimentTrials()
	if got := control.answersFor("sess-1"); len(got) != 1 {
		t.Fatalf("late duplicate re-answered %d times, want 1", len(got))
	}
	if got := control.cancelledIDs(); len(got) != 1 {
		t.Fatalf("late duplicate re-cancelled %d times, want 1", len(got))
	}
	rec, _, _ := store.LatestDecision("helper-g", "sess-1", "ask-1")
	if rec.Winner != "fork-1" || rec.Result != "answered:fork-1" {
		t.Fatalf("settled decision regressed: result=%s winner=%s", rec.Result, rec.Winner)
	}
}

// ---- 验收 10：硬门槛不自动答 ----------------------------------------------

func TestGapGHardGateNeverAutoAnswered(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	// The model must never be consulted for a hard gate.
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, mustNotRunModel(t), time.Hour)

	now := time.Now()
	item := SessionInteraction{
		Kind: "ask", ID: "ask-1", DueAt: now.Add(-time.Minute),
		Questions: []event.AskQuestion{{
			ID: "q1", Prompt: "请提供密码以继续", Multi: false,
			Options: []event.AskOption{{Label: "我提供"}, {Label: "稍后"}},
		}},
	}
	ex.AutoAnswerInteraction(snapshot.Assistant, "sess-1", item, now)

	if got := control.answersFor("sess-1"); len(got) != 0 {
		t.Fatalf("hard gate was auto-answered: %+v", got)
	}
	if forked := control.forkedIDs(); len(forked) != 0 {
		t.Fatalf("hard gate forked candidates: %v", forked)
	}
	rec, ok, err := store.LatestDecision("helper-g", "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if rec.Source != DecisionUser || rec.HardGate == "" || rec.Result != "wait_for_user" {
		t.Fatalf("decision = %s gate=%s result=%s, want user/wait_for_user", rec.Source, rec.HardGate, rec.Result)
	}
}

// ---- 验收 11：等待硬门槛时其他责任继续推进 --------------------------------

func TestGapGOtherWorkAdvancesWhileHardGateWaits(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	host := &fakeSupervisorHost{}
	ex := newGapGExecutor(t, store, control, host, lowConfidenceModel(t), time.Hour)

	// The hard-gate interaction is already left for the user (terminal user
	// decision), and the session is still waiting on it.
	_, err := store.RecordInteractionDecision(InteractionDecisionRecord{
		ID:            StableID("decision", "helper-g/sess-hard/ask-1/user"),
		AssistantID:   snapshot.Assistant.ID,
		SessionID:     "sess-hard",
		InteractionID: "ask-1",
		Source:        DecisionUser,
		HardGate:      HardGateCredentials,
		Result:        "wait_for_user",
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	control.setPending("sess-hard", askItem("ask-1", time.Now().Add(-time.Minute)))
	host.managed = []ManagedSessionSummary{
		{ID: "sess-hard", Status: "waiting", Turns: 1},
		{ID: "sess-other", Status: "running", Turns: 1},
	}

	// First collect: the hard-gate session produces NO interaction_required
	// (the user decision is terminal), while the other session is observed.
	ex.CollectSupervisorEvents(time.Now())
	evs, err := ex.Events().Pending(snapshot.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind == EventInteractionRequired && ev.SessionID == "sess-hard" {
			t.Fatal("hard gate re-enqueued interaction_required: the supervisor loop would spin on it")
		}
	}

	// Same state again: no new events (no spin), the loop stays free.
	before := len(evs)
	ex.CollectSupervisorEvents(time.Now())
	evs, _ = ex.Events().Pending(snapshot.Assistant.ID)
	if len(evs) != before {
		t.Fatalf("idle collect grew the queue: %d -> %d", before, len(evs))
	}

	// Another managed session progresses: its event still wakes the loop, so
	// other responsibilities advance while the hard gate waits.
	host.managed[1] = ManagedSessionSummary{ID: "sess-other", Status: "running", Turns: 2}
	ex.CollectSupervisorEvents(time.Now())
	evs, _ = ex.Events().Pending(snapshot.Assistant.ID)
	sawOther := false
	for _, ev := range evs {
		if ev.Kind == EventSessionProgressed && ev.SessionID == "sess-other" {
			sawOther = true
		}
	}
	if !sawOther {
		t.Fatal("other session progress did not wake the loop while the hard gate waits")
	}
}

// ---- 验收 12：Experiment revision/fence 与请求幂等 -------------------------

func TestGapGExperimentRecordFenceAndIdempotency(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-g")
	control := newGapGControl()
	ex := newGapGExecutor(t, store, control, &fakeSupervisorHost{}, lowConfidenceModel(t), time.Hour)

	// Full start -> conclusion path: the auto-answer interaction creates the
	// running experiment record (revision 1), the sweep concludes it
	// (revision 2) under the revision fence.
	now := time.Now()
	ex.AutoAnswerInteraction(snapshot.Assistant, "sess-1", askItem("ask-1", now), now)
	forks := control.forkedIDs()
	if len(forks) < 2 {
		t.Fatalf("forks = %d, want >= 2", len(forks))
	}
	control.setStatus(forks[0], TrialStatusDone)
	control.setStatus(forks[1], TrialStatusFailed)
	ex.ResolveExperimentTrials()

	exp := findExperiment(t, store, "helper-g", experimentID("helper-g", "sess-1", "ask-1"))
	if exp.Status != ExperimentConcluded || exp.Revision < 2 || exp.Winner != forks[0] {
		t.Fatalf("experiment status=%s revision=%d winner=%s, want concluded with >= 2 revisions", exp.Status, exp.Revision, exp.Winner)
	}

	// A stale fence: a conclusion composed against an older revision is
	// rejected instead of silently overwriting the newer one.
	_, err := store.RecordExperiment(RecordExperimentInput{
		RequestID:        StableID("request", "stale-conclusion"),
		Experiment:       exp,
		ExpectedRevision: exp.Revision - 1,
		Now:              time.Now(),
	})
	var conflictErr *ConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("stale fence err = %v, want *ConflictError", err)
	}

	// Replay idempotency: replaying the start request with the EXACT same
	// input returns the stored record instead of re-applying.
	startRequestID := StableID("request", fmt.Sprintf("experiment/%s/start", exp.ID))
	startExp := Experiment{
		ID:          exp.ID,
		AssistantID: "helper-g",
		Hypothesis:  "Which environment?",
		Isolation:   "session",
		Metric:      "fork session completed / preference order",
		Candidates:  []string{"Staging", "Production"},
		Trials: []TrialState{
			{SessionID: forks[0], Worktree: "session:" + forks[0], Answer: EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Staging"}}}), Status: TrialStatusRunning},
			{SessionID: forks[1], Worktree: "session:" + forks[1], Answer: EncodeTrialAnswer([]event.AskAnswer{{QuestionID: "q1", Selected: []string{"Production"}}}), Status: TrialStatusRunning},
		},
		Result:     "running",
		Confidence: 0.5,
		Rollback:   forks[0] + "," + forks[1],
		Status:     ExperimentRunning,
	}
	replayed, err := store.RecordExperiment(RecordExperimentInput{
		RequestID: startRequestID, Experiment: startExp, ExpectedRevision: 0, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("replay RecordExperiment: %v", err)
	}
	if replayed.ID != exp.ID || replayed.Revision != 1 || replayed.Status != ExperimentRunning {
		t.Fatalf("replay returned %+v, want the stored start record revision 1", replayed)
	}

	// The same request ID reused with DIFFERENT parameters is an explicit
	// conflict (the "同请求不同参数显式失败" rule), never a silent reuse.
	changed := startExp
	changed.Confidence = 0.9
	_, err = store.RecordExperiment(RecordExperimentInput{
		RequestID: startRequestID, Experiment: changed, ExpectedRevision: 0, Now: time.Now(),
	})
	var idemErr *IdempotencyError
	if !errors.As(err, &idemErr) {
		t.Fatalf("same request id with different params err = %v, want *IdempotencyError", err)
	}
}
