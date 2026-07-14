package control

import (
	"context"
	"io"
	"testing"
	"time"

	"workground2/internal/event"
	"workground2/internal/jobs"
)

type approvalBlockingRunner struct {
	c *Controller
}

func (r *approvalBlockingRunner) Run(ctx context.Context, _ string) error {
	_, _, err := gateApprover{c: r.c}.Approve(ctx, "bash", "go test ./...", nil)
	return err
}

type askBlockingRunner struct {
	c *Controller
}

func (r *askBlockingRunner) Run(ctx context.Context, _ string) error {
	_, err := r.c.Ask(ctx, []event.AskQuestion{{
		ID:      "choice",
		Prompt:  "Pick one",
		Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
	}})
	return err
}

func TestCancelClearsPendingApprovalRuntimeStatus(t *testing.T) {
	approvals := make(chan event.Approval, 1)
	done := make(chan event.Event, 1)
	c := New(Options{Sink: event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.ApprovalRequest:
			approvals <- e.Approval
		case event.TurnDone:
			done <- e
		}
	})})
	runner := &approvalBlockingRunner{c: c}
	c.runner = runner

	c.Send("needs approval")
	select {
	case <-approvals:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval request")
	}
	if st := c.RuntimeStatus(); !st.Running || st.RunningWork || !st.PendingPrompt || !st.Cancellable || st.CancelRequested || st.Mode != RuntimeModeWaitingUser || !st.ForegroundActive || !st.ActiveRuntimeWork {
		t.Fatalf("status before cancel = %+v, want running pending cancellable, RunningWork=false", st)
	}

	c.Cancel()
	c.Cancel()
	assertCancelClearedPendingRuntimeStatus(t, c.RuntimeStatus())
	waitTurnDoneEvent(t, done)
	if st := c.RuntimeStatus(); st.Running || st.RunningWork || st.PendingPrompt || st.Cancellable || st.CancelRequested || st.Mode != RuntimeModeIdle || st.ActiveRuntimeWork {
		t.Fatalf("status after turn done = %+v, want idle", st)
	}
}

func TestCancelClearsPendingAskRuntimeStatus(t *testing.T) {
	asks := make(chan event.Ask, 1)
	done := make(chan event.Event, 1)
	c := New(Options{Sink: event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.AskRequest:
			asks <- e.Ask
		case event.TurnDone:
			done <- e
		}
	})})
	runner := &askBlockingRunner{c: c}
	c.runner = runner

	c.Send("ask user")
	select {
	case <-asks:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ask request")
	}
	if st := c.RuntimeStatus(); !st.Running || st.RunningWork || !st.PendingPrompt || !st.Cancellable || st.CancelRequested || st.Mode != RuntimeModeWaitingUser || !st.ForegroundActive || !st.ActiveRuntimeWork {
		t.Fatalf("status before cancel = %+v, want running pending cancellable, RunningWork=false", st)
	}

	c.Cancel()
	assertCancelClearedPendingRuntimeStatus(t, c.RuntimeStatus())
	waitTurnDoneEvent(t, done)
	if st := c.RuntimeStatus(); st.Running || st.RunningWork || st.PendingPrompt || st.Cancellable || st.CancelRequested || st.Mode != RuntimeModeIdle || st.ActiveRuntimeWork {
		t.Fatalf("status after turn done = %+v, want idle", st)
	}
}

func assertCancelClearedPendingRuntimeStatus(t *testing.T, st RuntimeStatus) {
	t.Helper()
	if st.PendingPrompt {
		t.Fatalf("status immediately after cancel = %+v, want pending prompt cleared", st)
	}
	if st.Running {
		if !st.Cancellable || !st.CancelRequested || st.Mode != RuntimeModeCancelling || !st.ForegroundActive || !st.ActiveRuntimeWork || !st.RunningWork {
			t.Fatalf("status immediately after cancel = %+v, want running cancelling without pending prompt", st)
		}
		return
	}
	if st.Cancellable || st.CancelRequested || st.Mode != RuntimeModeIdle || st.ActiveRuntimeWork || st.RunningWork {
		t.Fatalf("status immediately after cancel = %+v, want idle when turn already completed", st)
	}
}

func TestRuntimeStatusModeSeparatesBackgroundOnly(t *testing.T) {
	release := make(chan struct{})
	c := New(Options{})
	c.jobs = jobs.NewManager(event.Discard)
	c.jobs.Start("task", "background", func(ctx context.Context, _ io.Writer) (string, error) {
		select {
		case <-release:
			return "", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	t.Cleanup(func() {
		close(release)
	})

	st := c.RuntimeStatus()
	if st.Mode != RuntimeModeBackgroundOnly || !st.BackgroundOnly || !st.ActiveRuntimeWork || st.ForegroundActive || st.Cancellable || st.Running || st.PendingPrompt || st.BackgroundJobs != 1 || !st.RunningWork {
		t.Fatalf("background-only status = %+v, want background_only without foreground activity, RunningWork=true", st)
	}
}

func waitTurnDoneEvent(t *testing.T, done <-chan event.Event) {
	t.Helper()
	select {
	case e := <-done:
		if e.Kind != event.TurnDone {
			t.Fatalf("event = %v, want TurnDone", e.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn_done")
	}
}

// TestRunningWorkSemantics verifies the user-facing Running field semantics:
//   - waiting_user: RunningWork=false (user must act; not "running")
//   - background_only: RunningWork=true
//   - foreground (accepted): RunningWork=true immediately
//   - cancelling: RunningWork=true
//   - idle: RunningWork=false
func TestRunningWorkWaitingUserIsNotRunning(t *testing.T) {
	approvals := make(chan event.Approval, 1)
	done := make(chan event.Event, 1)
	c := New(Options{Sink: event.FuncSink(func(e event.Event) {
		switch e.Kind {
		case event.ApprovalRequest:
			approvals <- e.Approval
		case event.TurnDone:
			done <- e
		}
	})})
	runner := &approvalBlockingRunner{c: c}
	c.runner = runner

	c.Send("needs approval")
	select {
	case <-approvals:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval request")
	}

	st := c.RuntimeStatus()
	if st.RunningWork {
		t.Fatalf("waiting_user: RunningWork=%v, want false", st.RunningWork)
	}
	if !st.PendingPrompt {
		t.Fatal("waiting_user: PendingPrompt should be true")
	}
	if !st.ActiveRuntimeWork {
		t.Fatal("waiting_user: ActiveRuntimeWork should be true (lifecycle protection)")
	}

	// Cancel to clear pending and wait for completion.
	c.Cancel()
	waitTurnDoneEvent(t, done)

	st = c.RuntimeStatus()
	if st.RunningWork {
		t.Fatalf("idle after approval: RunningWork=%v, want false", st.RunningWork)
	}
}

func TestRunningWorkBackgroundOnly(t *testing.T) {
	release := make(chan struct{})
	c := New(Options{})
	c.jobs = jobs.NewManager(event.Discard)
	c.jobs.Start("task", "background", func(ctx context.Context, _ io.Writer) (string, error) {
		select {
		case <-release:
			return "", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	t.Cleanup(func() { close(release) })

	st := c.RuntimeStatus()
	if !st.RunningWork {
		t.Fatalf("background_only: RunningWork=%v, want true", st.RunningWork)
	}
	if st.Mode != RuntimeModeBackgroundOnly {
		t.Fatalf("background_only: Mode=%v, want background_only", st.Mode)
	}
}

func TestRunningWorkForegroundImmediatelyAfterAccept(t *testing.T) {
	done := make(chan struct{})
	c := New(Options{})
	c.runner = controllerRunnerFunc(func(_ context.Context, _ string) error {
		<-done
		return nil
	})

	c.Send("hello")

	st := c.RuntimeStatus()
	if !st.RunningWork {
		t.Fatalf("foreground immediately after send: RunningWork=%v, want true", st.RunningWork)
	}
	if st.Mode != RuntimeModeForeground {
		t.Fatalf("foreground: Mode=%v, want foreground", st.Mode)
	}
	if st.PendingPrompt {
		t.Fatal("foreground: PendingPrompt should be false")
	}

	close(done)
}

func TestRunningWorkCancelling(t *testing.T) {
	block := make(chan struct{})
	done := make(chan event.Event, 1)
	c := New(Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.TurnDone {
			done <- e
		}
	})})
	c.runner = controllerRunnerFunc(func(_ context.Context, _ string) error {
		<-block
		return nil
	})

	c.Send("long running")
	c.Cancel()

	st := c.RuntimeStatus()
	if !st.RunningWork {
		t.Fatalf("cancelling: RunningWork=%v, want true", st.RunningWork)
	}
	if st.Mode != RuntimeModeCancelling {
		t.Fatalf("cancelling: Mode=%v, want cancelling", st.Mode)
	}

	close(block)
	waitTurnDoneEvent(t, done)
}

func TestRunningWorkIdle(t *testing.T) {
	c := New(Options{})
	st := c.RuntimeStatus()
	if st.RunningWork {
		t.Fatalf("idle: RunningWork=%v, want false", st.RunningWork)
	}
	if st.Mode != RuntimeModeIdle {
		t.Fatalf("idle: Mode=%v, want idle", st.Mode)
	}
}

type controllerRunnerFunc func(ctx context.Context, input string) error

func (f controllerRunnerFunc) Run(ctx context.Context, input string) error {
	return f(ctx, input)
}
