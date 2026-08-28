package assistantdaemon

// gap_g_test.go — daemon 侧语义对等测试：daemon 与 desktop 共用同一个
// assistant.SupervisorExecutor（runtime.go / desktop 的 wiring 完全一致），
// 这里用真实 Session 文件验证 daemon 专属的 trial 状态解析（completed ->
// done，failed/cancelled -> failed），并驱动共享 executor 走完低置信分叉、
// 赢家结算与全失败回退，证明 daemon 与 desktop 的自动代答/实验编排语义一致。

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/config"
	"workground2/internal/event"
)

// daemonGapGControl is a fake sessiontool control whose Fork creates REAL
// session files in the daemon session dir, so the daemon's
// daemonTrialSessionStatus resolver can locate them by BranchID exactly like a
// production fork branch.
type daemonGapGControl struct {
	mu        sync.Mutex
	dir       string
	answered  []string
	cancelled []string
}

func (c *daemonGapGControl) Create(req assistant.SessionCreateRequest) (string, error) {
	return "managed-" + req.RequestID, nil
}
func (c *daemonGapGControl) Steer(string, string, string) error { return nil }
func (c *daemonGapGControl) Resume(string, string) error        { return nil }
func (c *daemonGapGControl) Retry(string, string) error         { return nil }

func (c *daemonGapGControl) Fork(sessionID, requestID string) (string, error) {
	path, _, err := agent.CreateStableSessionFile(c.dir, "helper-parity", "fork-"+requestID)
	if err != nil {
		return "", err
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		return "", err
	}
	meta.Status = agent.SessionStatusQueued
	meta.AssistantID = "helper-parity"
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		return "", err
	}
	// Stamp authoritative listing counts (turns >= 1) so ListSessions — and
	// therefore the daemon's trial resolver — sees the fork branch, exactly
	// like a real fork transcript.
	if err := agent.UpdateSessionMeta(path, "", "fork", 1, false); err != nil {
		return "", err
	}
	return agent.BranchID(path), nil
}

func (c *daemonGapGControl) AnswerQuestion(sessionID, _ string, _ []event.AskAnswer, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answered = append(c.answered, sessionID)
	return nil
}

func (c *daemonGapGControl) Cancel(sessionID, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled = append(c.cancelled, sessionID)
	return nil
}

func (c *daemonGapGControl) PendingInteractions(sessionID string) ([]assistant.SessionInteraction, error) {
	now := time.Now()
	return []assistant.SessionInteraction{{
		Kind: "ask", ID: "ask-1", DueAt: now.Add(-time.Minute),
		Questions: []event.AskQuestion{{
			ID: "q1", Prompt: "Which environment?", Multi: false,
			Options: []event.AskOption{{Label: "Staging"}, {Label: "Production"}},
		}},
	}}, nil
}

func (c *daemonGapGControl) answeredCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.answered)
}

// answeredSessions lists the sessions that received an answer (used to assert
// the original session is never answered during isolation).
func (c *daemonGapGControl) answeredSessions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.answered...)
}

// answersFor counts how many answers were submitted to one session.
func (c *daemonGapGControl) answersFor(session string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, s := range c.answered {
		if s == session {
			n++
		}
	}
	return n
}

func (c *daemonGapGControl) cancelledIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.cancelled...)
}

var _ assistant.SessionControl = (*daemonGapGControl)(nil)

// setForkStatus records a real terminal status onto one fork session file so
// the daemon resolver observes exactly what the Session subsystem would.
func setForkStatus(t *testing.T, dir, forkID string, status agent.SessionStatus) {
	t.Helper()
	sessions, err := agent.ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if agent.BranchID(s.Path) != forkID {
			continue
		}
		meta, ok, err := agent.LoadBranchMeta(s.Path)
		if err != nil || !ok {
			t.Fatalf("load meta for %s: ok=%v err=%v", forkID, ok, err)
		}
		meta.Status = status
		if err := agent.SaveBranchMetaPreserveUpdated(s.Path, meta); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("fork session %s not found under %s", forkID, dir)
}

// TestDaemonTrialStatusResolverMapsTerminalStates proves the daemon's
// resolver turns the Session subsystem statuses into the experiment
// vocabulary: completed -> done (a winner candidate), failed/cancelled ->
// failed (terminal, can never win), queued/running -> running.
func TestDaemonTrialStatusResolverMapsTerminalStates(t *testing.T) {
	bootstrapDaemonSupervisorConfig(t)
	r := &Runtime{}
	dir := config.SessionDir()

	cases := []struct {
		key    string
		status agent.SessionStatus
		want   string
	}{
		{"done", agent.SessionStatusCompleted, assistant.TrialStatusDone},
		{"fail", agent.SessionStatusFailed, assistant.TrialStatusFailed},
		{"cancel", agent.SessionStatusCancelled, assistant.TrialStatusFailed},
		{"run", agent.SessionStatusRunning, assistant.TrialStatusRunning},
	}
	for _, tc := range cases {
		path, _, err := agent.CreateStableSessionFile(dir, "helper-parity", tc.key)
		if err != nil {
			t.Fatalf("create %s: %v", tc.key, err)
		}
		meta, err := agent.EnsureBranchMeta(path)
		if err != nil {
			t.Fatal(err)
		}
		meta.Status = tc.status
		if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
			t.Fatal(err)
		}
		// Stamp authoritative listing counts so the resolver's ListSessions
		// sees the branch (real fork transcripts always have content).
		if err := agent.UpdateSessionMeta(path, "", "fork", 1, false); err != nil {
			t.Fatal(err)
		}
		id := agent.BranchID(path)
		got, ok := r.daemonTrialSessionStatus(id)
		if !ok || got != tc.want {
			t.Fatalf("%s: resolver = %q ok=%v, want %q", tc.key, got, ok, tc.want)
		}
	}

	// An unknown session is reported as not found (the executor treats it as
	// terminal-failed, never as a fake completion).
	if _, ok := r.daemonTrialSessionStatus("no-such-session"); ok {
		t.Fatal("resolver reported a vanished session as found")
	}
}

// TestDaemonAutoAnswerExperimentParity drives the shared supervisor executor
// with the daemon's own wiring (daemonSupervisorHost + daemonTrialSessionStatus)
// through the full experiment lifecycle: low-confidence fork, winner sweep with
// a real completed fork, and the all-failed fallback. It asserts the same
// semantics the desktop and assistant-package tests assert.
func TestDaemonAutoAnswerExperimentParity(t *testing.T) {
	bootstrapDaemonSupervisorConfig(t)
	store, err := assistant.NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatal(err)
	}
	a := mustCreateDaemonAssistant(t, store, "helper-parity")

	control := &daemonGapGControl{dir: config.SessionDir()}
	r := &Runtime{store: store}
	events, err := assistant.NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	autoAnswer, err := assistant.NewAutoAnswer(assistant.RoleModelFunc(func(_ context.Context, _ string) (string, error) {
		return `{"selected":["Staging"],"confidence":0.5,"rationale":"uncertain"}`, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ex, err := assistant.NewSupervisorExecutor(assistant.SupervisorExecutorOptions{
		Store:  store,
		Events: events,
		Host:   &daemonSupervisorHost{r: r},
		Control: func() assistant.SessionControl {
			return control
		},
		AutoAnswer: func() *assistant.AutoAnswer { return autoAnswer },
		TrialStatus: func() assistant.TrialStatusResolver {
			return r.daemonTrialSessionStatus
		},
		Diagnostic: func(operation string, err error) {
			t.Logf("supervisor diagnostic %s: %v", operation, err)
		},
		ExperimentMaxAge: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Low-confidence question: two isolated fork sessions, original untouched.
	now := time.Now()
	pending, err := control.PendingInteractions("sess-1")
	if err != nil || len(pending) == 0 {
		t.Fatalf("pending interactions: %v", err)
	}
	ex.AutoAnswerInteraction(a, "sess-1", pending[0], now)
	for _, s := range control.answeredSessions() {
		if s == "sess-1" {
			t.Fatalf("original session answered during isolation: %v", control.answeredSessions())
		}
	}
	rec, ok, err := store.LatestDecision(a.ID, "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if rec.Source != assistant.DecisionExperiment || len(rec.Trials) < 2 {
		t.Fatalf("source=%s trials=%d, want experiment with >= 2 isolated candidates", rec.Source, len(rec.Trials))
	}

	// The daemon resolver classifies the forks from their real session files:
	// one failed, one completed -> the completed candidate wins and the loser
	// is cancelled, exactly like desktop.
	dir := config.SessionDir()
	setForkStatus(t, dir, rec.Trials[0].SessionID, agent.SessionStatusFailed)
	setForkStatus(t, dir, rec.Trials[1].SessionID, agent.SessionStatusCompleted)
	ex.ResolveExperimentTrials()

	winnerRec, ok, err := store.LatestDecision(a.ID, "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision after sweep ok=%v err=%v", ok, err)
	}
	if winnerRec.Result != "answered:"+rec.Trials[1].SessionID || winnerRec.Winner != rec.Trials[1].SessionID {
		t.Fatalf("result=%s winner=%s, want answered:%s", winnerRec.Result, winnerRec.Winner, rec.Trials[1].SessionID)
	}
	if cancelled := control.cancelledIDs(); len(cancelled) != 1 || cancelled[0] != rec.Trials[0].SessionID {
		t.Fatalf("cancelled = %v, want the failed fork %s", cancelled, rec.Trials[0].SessionID)
	}

	// All-failed fallback: a second experiment with both forks failed settles
	// with the most rollback-safe inferred answer and cancels both forks.
	control2 := &daemonGapGControl{dir: config.SessionDir()}
	ex2, err := assistant.NewSupervisorExecutor(assistant.SupervisorExecutorOptions{
		Store: store, Events: events, Host: &daemonSupervisorHost{r: r},
		Control:          func() assistant.SessionControl { return control2 },
		AutoAnswer:       func() *assistant.AutoAnswer { return autoAnswer },
		TrialStatus:      func() assistant.TrialStatusResolver { return r.daemonTrialSessionStatus },
		ExperimentMaxAge: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending2, err := control2.PendingInteractions("sess-2")
	if err != nil || len(pending2) == 0 {
		t.Fatalf("pending interactions sess-2: %v", err)
	}
	ex2.AutoAnswerInteraction(a, "sess-2", pending2[0], now)
	fallbackRec, ok, err := store.LatestDecision(a.ID, "sess-2", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision sess-2 ok=%v err=%v", ok, err)
	}
	if len(fallbackRec.Trials) < 2 {
		t.Fatalf("sess-2 trials = %d, want >= 2", len(fallbackRec.Trials))
	}
	for _, tr := range fallbackRec.Trials {
		setForkStatus(t, dir, tr.SessionID, agent.SessionStatusFailed)
	}
	ex2.ResolveExperimentTrials()
	settled, ok, err := store.LatestDecision(a.ID, "sess-2", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision sess-2 after sweep ok=%v err=%v", ok, err)
	}
	if settled.Result != "answered-fallback" || settled.Winner != "" {
		t.Fatalf("fallback result=%s winner=%q, want answered-fallback with no winner", settled.Result, settled.Winner)
	}
	if got := control2.answersFor("sess-2"); got != 1 {
		t.Fatalf("fallback answered original %d times, want exactly 1", got)
	}
	if cancelled := control2.cancelledIDs(); len(cancelled) != 2 {
		t.Fatalf("fallback cancelled = %v, want both forks", cancelled)
	}
	if ex, err := store.Get(a.ID); err == nil {
		for _, e := range ex.Experiments {
			if e.Result == "answered-fallback" && e.Status != assistant.ExperimentConcluded {
				t.Fatalf("fallback experiment not concluded: %+v", e)
			}
		}
	}
}
