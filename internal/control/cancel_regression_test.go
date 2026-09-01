package control

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/event"
)

// TestCancelReachesRunningTurnAndIdleIsNotSticky verifies the user Stop path:
// Cancel reaches the in-flight turn's context (the runner observes the
// cancellation), a repeated Cancel is a safe no-op, and a fresh Send after Stop
// starts a new turn instead of being swallowed by the earlier cancellation.
func TestCancelReachesRunningTurnAndIdleIsNotSticky(t *testing.T) {
	firstStarted := make(chan struct{})
	var calls atomic.Int32
	turnDone := make(chan error, 2)
	c := New(Options{
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone {
				turnDone <- e.Err
			}
		}),
	})
	t.Cleanup(c.Cancel)
	c.runner = controllerRunnerFunc(func(ctx context.Context, _ string) error {
		n := calls.Add(1)
		if n == 1 {
			close(firstStarted)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})

	c.Send("first")
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not start")
	}

	c.Cancel()
	c.Cancel() // repeated Stop must be safe and idempotent

	select {
	case err := <-turnDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first turn error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not finish after Cancel")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want 1 (no continuation work after Stop)", got)
	}
	if c.Running() {
		t.Fatal("controller still reports running after Stop")
	}

	// A new task submitted after Stop must run normally.
	c.Send("second")
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatalf("second turn error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second turn did not complete after Stop")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("runner calls after second send = %d, want 2", got)
	}
}

// TestCancelDuringGoalLoopStopsGoalAndSkipsContinuationTurn verifies that Stop
// while a goal turn is in flight cancels the turn, converges the goal state to
// stopped immediately (not on the loop's own late ctx check), and never starts
// the goal-loop continuation turn afterwards.
func TestCancelDuringGoalLoopStopsGoalAndSkipsContinuationTurn(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseFirst) })
	defer release()
	var calls atomic.Int32
	turnDone := make(chan error, 1)
	c := New(Options{
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.TurnDone {
				turnDone <- e.Err
			}
		}),
	})
	t.Cleanup(c.Cancel)
	c.SetGoalWithResearchMode("finish the migration", GoalResearchOn)
	c.runner = controllerRunnerFunc(func(ctx context.Context, _ string) error {
		n := calls.Add(1)
		if n == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return ctx.Err()
	})

	c.Send("work the goal")
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("goal turn did not start")
	}
	c.Cancel()

	if got := c.GoalStatus(); got != GoalStatusStopped {
		t.Fatalf("GoalStatus() right after Cancel = %q, want stopped", got)
	}

	release()
	select {
	case err := <-turnDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("goal turn error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goal turn did not finish after Cancel")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want 1 (goal continuation must not start a new turn after Stop)", got)
	}
}

func TestGoalCancelBeforeAdvance(t *testing.T) {
	c := New(Options{})
	c.SetGoalWithResearchMode("finish the migration", GoalResearchOff)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := &turnOrchestrator{c: c}
	if err := o.continueGoal(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("continueGoal error = %v, want context.Canceled", err)
	}
	if c.goals.turns != 0 {
		t.Fatalf("goal advanced %d turns after cancellation", c.goals.turns)
	}
	if got := c.GoalStatus(); got != GoalStatusStopped {
		t.Fatalf("goal status = %q, want stopped", got)
	}
}

func TestCancelIsSessionScoped(t *testing.T) {
	controllers := []*Controller{New(Options{}), New(Options{})}
	contexts := make([]context.Context, len(controllers))
	for i, c := range controllers {
		started := make(chan context.Context, 1)
		t.Cleanup(c.Cancel)
		c.runGuarded(func(ctx context.Context) error {
			started <- ctx
			<-ctx.Done()
			return ctx.Err()
		})
		select {
		case contexts[i] = <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("turn did not start")
		}
	}
	controllers[0].Cancel()
	controllers[0].Cancel()
	if !errors.Is(contexts[0].Err(), context.Canceled) {
		t.Fatal("target session was not cancelled")
	}
	if err := contexts[1].Err(); err != nil {
		t.Fatalf("other session was cancelled: %v", err)
	}
}
