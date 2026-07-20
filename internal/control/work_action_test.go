package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/event"
	"workground2/internal/permission"
	"workground2/internal/work"
)

type controlActionFixture struct {
	controller *Controller
	service    *work.Service
	value      *work.Work
	events     chan event.Event
	calls      atomic.Int32
}

func newControlActionFixture(t *testing.T, policy permission.Policy, confirm bool, timeout time.Duration) *controlActionFixture {
	t.Helper()
	store, err := work.NewFileWorkStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	views := NewWorkViewBroadcaster()
	service := work.NewService(store, work.NewBlueprintRegistry(), views)
	fixture := &controlActionFixture{service: service, events: make(chan event.Event, 8)}
	registry := work.NewActionRegistry()
	if err := registry.Register(work.ActionRegistration{
		BlockKind: "markdown", ActionID: "publish", Intent: "report.publish", Summary: "Publish report",
		HandlerID: "report.publish", HandlerVersion: "v1",
		Risk: work.RiskExternal, ConfirmRequired: confirm,
		Handler: func(context.Context, work.ActionHandlerContext) (*work.ActionResult, error) {
			fixture.calls.Add(1)
			return &work.ActionResult{Message: "published"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetActionRegistry(registry); err != nil {
		t.Fatal(err)
	}
	fixture.controller = New(Options{
		Sink:   event.FuncSink(func(value event.Event) { fixture.events <- value }),
		Policy: policy, ApprovalTimeout: timeout, Work: service, WorkViews: views,
	})
	fixture.value, err = service.Create(context.Background(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: work.SchemaVersion, Version: 1},
		Name:         "Action", RequestID: "control-action-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.UpsertBlock(context.Background(), work.BlockUpsertInput{
		WorkID: fixture.value.ID, BlockID: "bp-blank-notes", Kind: "markdown", SchemaVersion: work.SchemaVersion,
		Revision: 2, Status: work.BlockReady, Data: json.RawMessage(`{"content":"action"}`),
		Actions:          []work.BlockActionSpec{{ID: "publish", Intent: "report.publish", Risk: "read", ConfirmRequired: confirm}},
		Source:           work.BlockSource{Provider: "controller", Mode: "snapshot", Verified: true},
		ExpectedRevision: 2, RequestID: "control-action-block",
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *controlActionFixture) execute(ctx context.Context, requestID string) (*work.ActionReceipt, error) {
	return f.controller.WorkControl().ExecuteBlockAction(ctx, work.BlockActionRequest{
		WorkID: f.value.ID, BlockID: "bp-blank-notes", ActionID: "publish",
		RequestID: requestID, ExpectedRevision: 3,
	})
}

func waitActionApproval(t *testing.T, events <-chan event.Event) event.Approval {
	t.Helper()
	select {
	case value := <-events:
		if value.Kind != event.ApprovalRequest {
			t.Fatalf("event kind=%v want ApprovalRequest", value.Kind)
		}
		return value.Approval
	case <-time.After(2 * time.Second):
		t.Fatal("ApprovalRequest not emitted")
		return event.Approval{}
	}
}

func TestActionControllerApprovalAllowsAndCarriesContext(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, nil), false, 0)
	type result struct {
		receipt *work.ActionReceipt
		err     error
	}
	done := make(chan result, 1)
	go func() {
		receipt, err := fixture.execute(context.Background(), "control-action-allow")
		done <- result{receipt, err}
	}()
	approval := waitActionApproval(t, fixture.events)
	if approval.WorkID != fixture.value.ID || approval.BlockID != "bp-blank-notes" || approval.ActionID != "publish" || approval.RequestID != "control-action-allow" || approval.Summary != "Publish report" {
		t.Fatalf("approval context=%+v", approval)
	}
	if approval.HandlerID != "report.publish" || approval.HandlerVersion != "v1" {
		t.Fatalf("approval handler identity=%+v", approval)
	}
	if approval.Tool != "report.publish" || !strings.Contains(approval.Subject, "handler:report.publish@v1") || !strings.Contains(approval.Reason, "handler=report.publish@v1") {
		t.Fatalf("approval presentation=%+v", approval)
	}
	fixture.controller.Approve(approval.ID, true, false, false)
	got := <-done
	if got.err != nil || got.receipt == nil || got.receipt.Status != string(work.ActionSucceeded) || fixture.calls.Load() != 1 {
		t.Fatalf("receipt=%+v err=%v calls=%d", got.receipt, got.err, fixture.calls.Load())
	}
}

func TestActionControllerApprovalRejectsAndPersists(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, nil), false, 0)
	done := make(chan struct {
		receipt *work.ActionReceipt
		err     error
	}, 1)
	go func() {
		receipt, err := fixture.execute(context.Background(), "control-action-deny")
		done <- struct {
			receipt *work.ActionReceipt
			err     error
		}{receipt, err}
	}()
	approval := waitActionApproval(t, fixture.events)
	fixture.controller.Approve(approval.ID, false, false, false)
	got := <-done
	var rejected *work.ErrActionRejected
	if !errors.As(got.err, &rejected) || got.receipt.Status != string(work.ActionRejected) || fixture.calls.Load() != 0 {
		t.Fatalf("receipt=%+v err=%T %v calls=%d", got.receipt, got.err, got.err, fixture.calls.Load())
	}
	view, err := fixture.service.Get(context.Background(), fixture.value.ID)
	if err != nil || len(view.Work.ActionReceipts) != 1 || view.Work.ActionReceipts[0].Status != work.ActionRejected {
		t.Fatalf("persisted view=%+v err=%v", view, err)
	}
}

func TestActionControllerApprovalTimeoutPersistsFailure(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, nil), false, 25*time.Millisecond)
	receipt, err := fixture.execute(context.Background(), "control-action-timeout")
	if !errors.Is(err, context.DeadlineExceeded) || receipt == nil || receipt.Status != string(work.ActionFailed) || !receipt.Retryable || fixture.calls.Load() != 0 {
		t.Fatalf("receipt=%+v err=%v calls=%d", receipt, err, fixture.calls.Load())
	}
	if _, pending := fixture.controller.PendingInteraction(); pending {
		t.Fatal("timed-out approval remained pending")
	}
}

func TestActionControllerPolicyDenyDoesNotPrompt(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, []string{"report.publish"}), false, 0)
	receipt, err := fixture.execute(context.Background(), "control-action-policy-deny")
	var rejected *work.ErrActionRejected
	if !errors.As(err, &rejected) || receipt.Status != string(work.ActionRejected) || fixture.calls.Load() != 0 {
		t.Fatalf("receipt=%+v err=%T %v calls=%d", receipt, err, err, fixture.calls.Load())
	}
	select {
	case value := <-fixture.events:
		t.Fatalf("unexpected event: %+v", value)
	default:
	}
}

func TestActionControllerConfirmRequiredOverridesAllowPolicy(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("allow", nil, nil, nil), true, 0)
	done := make(chan error, 1)
	go func() {
		_, err := fixture.execute(context.Background(), "control-action-confirm")
		done <- err
	}()
	approval := waitActionApproval(t, fixture.events)
	fixture.controller.Approve(approval.ID, true, false, false)
	if err := <-done; err != nil || fixture.calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, fixture.calls.Load())
	}
}

func TestActionApprovalReplayKeepsDomainContext(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, nil), false, 0)
	done := make(chan error, 1)
	go func() {
		_, err := fixture.execute(context.Background(), "control-action-replay")
		done <- err
	}()
	first := waitActionApproval(t, fixture.events)
	fixture.controller.ReplayPendingPrompts()
	replayed := waitActionApproval(t, fixture.events)
	if replayed.ID != first.ID || replayed.WorkID != first.WorkID || replayed.BlockID != first.BlockID || replayed.ActionID != first.ActionID || replayed.RequestID != first.RequestID || replayed.Summary != first.Summary {
		t.Fatalf("first=%+v replayed=%+v", first, replayed)
	}
	fixture.controller.Approve(first.ID, true, false, false)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestActionControllerCancelClearsApprovalAndIgnoresLateReply(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, nil), false, 0)
	done := make(chan struct {
		receipt *work.ActionReceipt
		err     error
	}, 1)
	go func() {
		receipt, err := fixture.execute(context.Background(), "control-action-cancel")
		done <- struct {
			receipt *work.ActionReceipt
			err     error
		}{receipt, err}
	}()
	approval := waitActionApproval(t, fixture.events)
	fixture.controller.Cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) || got.receipt == nil || got.receipt.Status != string(work.ActionFailed) || fixture.calls.Load() != 0 {
		t.Fatalf("receipt=%+v err=%v calls=%d", got.receipt, got.err, fixture.calls.Load())
	}
	if _, pending := fixture.controller.PendingInteraction(); pending {
		t.Fatal("cancelled Action approval remained pending")
	}
	fixture.controller.Approve(approval.ID, true, false, false)
	time.Sleep(20 * time.Millisecond)
	if fixture.calls.Load() != 0 {
		t.Fatalf("late approval executed handler: calls=%d", fixture.calls.Load())
	}
	view, err := fixture.service.Get(context.Background(), fixture.value.ID)
	if err != nil || view.Work.ActionReceipts[0].Status != work.ActionFailed {
		t.Fatalf("persisted view=%+v err=%v", view, err)
	}
}

func TestActionControllerCloseCancelsApprovalAndIsIdempotent(t *testing.T) {
	var cleanupCalls atomic.Int32
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, nil), false, 0)
	fixture.controller.cleanup = func() { cleanupCalls.Add(1) }
	done := make(chan struct {
		receipt *work.ActionReceipt
		err     error
	}, 1)
	go func() {
		receipt, err := fixture.execute(context.Background(), "control-action-close")
		done <- struct {
			receipt *work.ActionReceipt
			err     error
		}{receipt, err}
	}()
	approval := waitActionApproval(t, fixture.events)
	closed := make(chan struct{})
	go func() {
		fixture.controller.Close()
		close(closed)
	}()
	got := <-done
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wait for and reap Action")
	}
	fixture.controller.Close()
	fixture.controller.Approve(approval.ID, true, false, false)
	if !errors.Is(got.err, context.Canceled) || got.receipt == nil || got.receipt.Status != string(work.ActionFailed) || fixture.calls.Load() != 0 || cleanupCalls.Load() != 1 {
		t.Fatalf("receipt=%+v err=%v calls=%d cleanup=%d", got.receipt, got.err, fixture.calls.Load(), cleanupCalls.Load())
	}
	if _, pending := fixture.controller.PendingInteraction(); pending {
		t.Fatal("closed Controller retained Action approval")
	}
	if _, err := fixture.execute(context.Background(), "control-action-after-close"); !errors.Is(err, context.Canceled) {
		t.Fatalf("execute after Close err=%v want context.Canceled", err)
	}
}

func TestActionControllerCancelStopsRunningHandler(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("allow", nil, nil, nil), false, 0)
	started := make(chan struct{})
	registry := work.NewActionRegistry()
	if err := registry.Register(work.ActionRegistration{
		BlockKind: "markdown", ActionID: "publish", HandlerID: "report.publish", HandlerVersion: "v2", Intent: "report.publish", Risk: work.RiskExternal,
		Handler: func(ctx context.Context, _ work.ActionHandlerContext) (*work.ActionResult, error) {
			fixture.calls.Add(1)
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetActionRegistry(registry); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		receipt *work.ActionReceipt
		err     error
	}, 1)
	go func() {
		receipt, err := fixture.execute(context.Background(), "control-action-handler-cancel")
		done <- struct {
			receipt *work.ActionReceipt
			err     error
		}{receipt, err}
	}()
	<-started
	fixture.controller.Cancel()
	got := <-done
	var unknown *work.ErrActionOutcomeUnknown
	if !errors.As(got.err, &unknown) || got.receipt == nil || got.receipt.Status != string(work.ActionUnknown) || got.receipt.OutcomeKnown || fixture.calls.Load() != 1 {
		t.Fatalf("receipt=%+v err=%T %v calls=%d", got.receipt, got.err, got.err, fixture.calls.Load())
	}
}

func TestActionControllerCloseIsolatesLateHandlerResult(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("allow", nil, nil, nil), false, 0)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	registry := work.NewActionRegistry()
	if err := registry.Register(work.ActionRegistration{
		BlockKind: "markdown", ActionID: "publish", HandlerID: "report.publish", HandlerVersion: "v2", Intent: "report.publish", Risk: work.RiskExternal,
		Handler: func(context.Context, work.ActionHandlerContext) (*work.ActionResult, error) {
			fixture.calls.Add(1)
			close(started)
			<-release // deliberately ignore cancellation
			close(finished)
			return &work.ActionResult{Message: "too late"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetActionRegistry(registry); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		receipt *work.ActionReceipt
		err     error
	}, 1)
	go func() {
		receipt, err := fixture.execute(context.Background(), "control-action-close-late-handler")
		done <- struct {
			receipt *work.ActionReceipt
			err     error
		}{receipt, err}
	}()
	<-started
	closed := make(chan struct{})
	go func() {
		fixture.controller.Close()
		close(closed)
	}()
	var got struct {
		receipt *work.ActionReceipt
		err     error
	}
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Action remained attached to late handler")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close remained blocked by non-cooperative handler")
	}
	var unknown *work.ErrActionOutcomeUnknown
	if !errors.As(got.err, &unknown) || got.receipt == nil || got.receipt.Status != string(work.ActionUnknown) || got.receipt.OutcomeKnown {
		t.Fatalf("receipt=%+v err=%T %v", got.receipt, got.err, got.err)
	}
	close(release)
	<-finished
	time.Sleep(20 * time.Millisecond)
	view, err := fixture.service.Get(context.Background(), fixture.value.ID)
	if err != nil || view.Work.ActionReceipts[0].Status != work.ActionUnknown {
		t.Fatalf("late handler changed receipt: view=%+v err=%v", view, err)
	}
}

func TestActionControllerCallerCancellationIsRequestScoped(t *testing.T) {
	controller := New(Options{})
	firstParent, cancelFirst := context.WithCancel(context.Background())
	first, finishFirst, ok := controller.beginBlockAction(firstParent, "work:a", "request:a")
	if !ok {
		t.Fatal("first Action registration failed")
	}
	defer finishFirst()
	second, finishSecond, ok := controller.beginBlockAction(context.Background(), "work:b", "request:b")
	if !ok {
		t.Fatal("second Action registration failed")
	}
	defer finishSecond()

	cancelFirst()
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("first request context was not cancelled")
	}
	select {
	case <-second.Done():
		t.Fatal("caller cancellation leaked into unrelated Action request")
	default:
	}
	controller.Cancel()
	select {
	case <-second.Done():
	case <-time.After(time.Second):
		t.Fatal("Controller.Cancel did not cancel current Action")
	}
}
