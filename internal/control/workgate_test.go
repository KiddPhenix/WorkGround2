package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/permission"
	"workground2/internal/provider"
	"workground2/internal/workgate"
)

// fakeWorkGate is a mutable in-memory gate for Controller fencing tests.
type fakeWorkGate struct {
	mu    sync.Mutex
	state workgate.State
	epoch int64
}

func newFakeWorkGate(state workgate.State, epoch int64) *fakeWorkGate {
	return &fakeWorkGate{state: state, epoch: epoch}
}

func (g *fakeWorkGate) set(state workgate.State, epoch int64) {
	g.mu.Lock()
	g.state = state
	g.epoch = epoch
	g.mu.Unlock()
}

func (g *fakeWorkGate) State() workgate.State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

func (g *fakeWorkGate) Epoch() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.epoch
}

func (g *fakeWorkGate) Revision() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.epoch
}

func (g *fakeWorkGate) Allowed() bool {
	return g.State() == workgate.Running
}

func (g *fakeWorkGate) AllowedResume() bool {
	s := g.State()
	return s == workgate.Running || s == workgate.Recovering
}

type gateRecordingRunner struct {
	mu     sync.Mutex
	called int
	onRun  func()
}

func (r *gateRecordingRunner) Run(context.Context, string) error {
	r.mu.Lock()
	r.called++
	onRun := r.onRun
	r.mu.Unlock()
	if onRun != nil {
		onRun()
	}
	return nil
}

func (r *gateRecordingRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

type gateCollectingSink struct {
	mu     sync.Mutex
	events []event.Event
}

func (s *gateCollectingSink) Emit(e event.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *gateCollectingSink) notices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.events {
		if e.Kind == event.Notice {
			out = append(out, e.Text)
		}
	}
	return out
}

func gateTestController(t *testing.T, gate workgate.Gate, runner agent.Runner, sink event.Sink) *Controller {
	t.Helper()
	return New(Options{
		Runner:   runner,
		Executor: agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard),
		Sink:     sink,
		WorkGate: gate,
	})
}

func TestWorkGatePausedBlocksRunTurn(t *testing.T) {
	gate := newFakeWorkGate(workgate.Paused, 5)
	runner := &gateRecordingRunner{}
	c := gateTestController(t, gate, runner, event.Discard)
	defer c.Close()

	err := c.RunTurn(context.Background(), "do work")
	if err == nil || !strings.Contains(err.Error(), "work paused (epoch 5)") {
		t.Fatalf("RunTurn error = %v, want a paused-gate error naming epoch 5", err)
	}
	if runner.calls() != 0 {
		t.Fatalf("runner called %d times, want 0 (turn must be refused before executor start)", runner.calls())
	}
}

func TestWorkGatePausedBlocksRun(t *testing.T) {
	gate := newFakeWorkGate(workgate.Paused, 7)
	runner := &gateRecordingRunner{}
	c := gateTestController(t, gate, runner, event.Discard)
	defer c.Close()

	err := c.Run(context.Background(), "do work")
	if err == nil || !strings.Contains(err.Error(), "work paused (epoch 7)") {
		t.Fatalf("Run error = %v, want a paused-gate error naming epoch 7", err)
	}
	if runner.calls() != 0 {
		t.Fatalf("runner called %d times, want 0 (turn must be refused before executor start)", runner.calls())
	}
}

func TestWorkGatePausedBlocksSubmitWithNotice(t *testing.T) {
	gate := newFakeWorkGate(workgate.Paused, 3)
	runner := &gateRecordingRunner{}
	sink := &gateCollectingSink{}
	c := gateTestController(t, gate, runner, sink)
	defer c.Close()

	c.Submit("do work")

	if runner.calls() != 0 {
		t.Fatalf("runner called %d times, want 0 (submit must be refused)", runner.calls())
	}
	found := false
	for _, n := range sink.notices() {
		if strings.Contains(n, "work paused (epoch 3)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no notice emitted for paused submit; notices = %v", sink.notices())
	}
}

func TestWorkGateRunningAllowsAndResumeRestores(t *testing.T) {
	gate := newFakeWorkGate(workgate.Paused, 2)
	runner := &gateRecordingRunner{}
	c := gateTestController(t, gate, runner, event.Discard)
	defer c.Close()

	if err := c.RunTurn(context.Background(), "blocked"); err == nil {
		t.Fatal("RunTurn while paused returned nil, want refusal")
	}
	// Resume: gate returns to RUNNING with a bumped epoch.
	gate.set(workgate.Running, 3)
	if err := c.RunTurn(context.Background(), "allowed"); err != nil {
		t.Fatalf("RunTurn after resume = %v, want nil", err)
	}
	if runner.calls() != 1 {
		t.Fatalf("runner called %d times after resume, want 1", runner.calls())
	}
}

func TestWorkGateEpochChangeMarksTurnStale(t *testing.T) {
	gate := newFakeWorkGate(workgate.Running, 1)
	runner := &gateRecordingRunner{onRun: func() {
		// A pause/resume lands while the turn is in flight.
		gate.set(workgate.Paused, 2)
	}}
	c := gateTestController(t, gate, runner, event.Discard)
	defer c.Close()

	err := c.RunTurn(context.Background(), "in flight")
	if err == nil || !strings.Contains(err.Error(), "late result rejected") {
		t.Fatalf("RunTurn error = %v, want a stale/late-result rejection after epoch moved", err)
	}
	if runner.calls() != 1 {
		t.Fatalf("runner called %d times, want 1 (the turn ran but its result is stale)", runner.calls())
	}
}

func TestExecutorWorkGateBlocksToolWhenPaused(t *testing.T) {
	gate := newFakeWorkGate(workgate.Paused, 5)
	c := New(Options{WorkGate: gate})
	inner := newRuntimePermissionGate(permission.NewGate(permission.Policy{}, nil))
	w := &executorWorkGate{inner: inner, c: c}

	allow, reason, err := w.Check(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`), false)
	if err != nil {
		t.Fatalf("Check error = %v, want a deny (nil err) with a reason", err)
	}
	if allow {
		t.Fatalf("Check allow = true, want false while paused")
	}
	if !strings.Contains(reason, "work paused (epoch 5)") {
		t.Fatalf("Check reason = %q, want work paused (epoch 5)", reason)
	}

	// While RUNNING the wrapper delegates to the permission gate.
	gate.set(workgate.Running, 6)
	allow, _, err = w.Check(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`), false)
	if err != nil || !allow {
		t.Fatalf("Check while running = (%v, %v), want (true, nil)", allow, err)
	}
}

func TestWorkGateNilKeepsSessionsUnfenced(t *testing.T) {
	runner := &gateRecordingRunner{}
	c := New(Options{
		Runner:   runner,
		Executor: agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard),
		Sink:     event.Discard,
	})
	defer c.Close()

	if err := c.RunTurn(context.Background(), "no gate"); err != nil {
		t.Fatalf("RunTurn with nil gate = %v, want nil (default unfenced)", err)
	}
	if runner.calls() != 1 {
		t.Fatalf("runner called %d times, want 1", runner.calls())
	}
}

func TestWorkGateFenceMovedRefusesLateSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "late.jsonl")
	gate := newFakeWorkGate(workgate.Running, 1)
	runner := &gateRecordingRunner{onRun: func() {
		// The pause lands mid-turn: epoch bumps before the turn's persist write.
		gate.set(workgate.Paused, 2)
	}}
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "do it"})
	c := gateTestController(t, gate, runner, event.Discard)
	c.SetSessionPath(path)
	defer c.Close()

	err := c.RunTurn(context.Background(), "in flight")
	if err == nil || !strings.Contains(err.Error(), "late result rejected") {
		t.Fatalf("RunTurn error = %v, want a late-result rejection", err)
	}

	// The late transcript must not have been written: the fence moved before the
	// persist write, so the on-disk session stays at the pre-turn boundary (or
	// never materialized at all when there was no prior save).
	loaded, err := agent.LoadSession(path)
	if err != nil {
		if os.IsNotExist(err) {
			return // nothing ever landed — exactly what the fence guarantees
		}
		t.Fatal(err)
	}
	if n := len(loaded.Snapshot()); n != 1 {
		t.Fatalf("late transcript landed on disk: %d messages, want 1 (pre-turn boundary)", n)
	}
}

func TestWorkGateRecoveringAdmitsResumeTurn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.jsonl")
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "interrupted round"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.InFlightTurn = &agent.InFlightTurnMeta{StartMessageIndex: 1, PreserveUser: true}
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}

	gate := newFakeWorkGate(workgate.Recovering, 3)
	runner := &continueRunnerFake{}
	c := New(Options{Runner: runner, WorkGate: gate})
	c.SetSessionPath(path)
	c.Resume(sess, path)
	defer c.Close()

	if err := c.ContinueTurn(context.Background()); err != nil {
		t.Fatalf("ContinueTurn during RECOVERING = %v, want nil (recovery turn admitted)", err)
	}
	if !runner.continueCalled {
		t.Fatal("ContinueTurn did not drive the runner's Continue during RECOVERING")
	}
}
