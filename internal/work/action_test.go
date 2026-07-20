package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const actionBlockID = "bp-blank-notes"

type actionHarness struct {
	fixture *serviceFixture
	svc     *Service
	reg     *ActionRegistry
	perm    *fakeActionPermission
}

type fakeActionPermission struct {
	mu       sync.Mutex
	decision PermissionDecision
	err      error
	check    func(context.Context, PermissionRequest) (PermissionDecision, error)
	requests []PermissionRequest
}

func (f *fakeActionPermission) CheckPermission(ctx context.Context, request PermissionRequest) (PermissionDecision, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	check, decision, err := f.check, f.decision, f.err
	f.mu.Unlock()
	if check != nil {
		return check(ctx, request)
	}
	return decision, err
}

func (f *fakeActionPermission) last() PermissionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func newActionHarness(t *testing.T) *actionHarness {
	t.Helper()
	fixture := newServiceFixture(t)
	reg := NewActionRegistry()
	perm := &fakeActionPermission{decision: PermissionDecision{Allowed: true}}
	fixture.svc.SetActionRegistry(reg)
	fixture.svc.SetPermissionChecker(perm)
	return &actionHarness{fixture: fixture, svc: fixture.svc, reg: reg, perm: perm}
}

func (h *actionHarness) restart(t *testing.T) {
	t.Helper()
	h.fixture.restart(t)
	h.svc = h.fixture.svc
	h.svc.SetActionRegistry(h.reg)
	h.svc.SetPermissionChecker(h.perm)
}

func (h *actionHarness) register(t *testing.T, actionID, intent string, risk ActionRisk, handler ActionHandler) {
	t.Helper()
	if err := h.reg.Register(ActionRegistration{
		BlockKind: "markdown", ActionID: actionID, Intent: intent, Summary: "Test " + actionID,
		Risk: risk, Handler: handler,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

func setupActionBlock(t *testing.T, svc *Service, requestID string, actions ...BlockActionSpec) *Work {
	t.Helper()
	value := mustServiceCreate(t, svc, requestID+"-create")
	_, err := svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID: value.ID, BlockID: actionBlockID, Kind: "markdown", SchemaVersion: SchemaVersion,
		Revision: 2, Title: "Actions", Status: BlockReady, Data: json.RawMessage(`{"content":"test"}`),
		Actions: actions, Source: BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2, RequestID: requestID + "-block",
	})
	if err != nil {
		t.Fatalf("UpsertBlock: %v", err)
	}
	return value
}

func actionRequest(value *Work, actionID, requestID string) BlockActionRequest {
	return BlockActionRequest{WorkID: value.ID, BlockID: actionBlockID, ActionID: actionID, RequestID: requestID, ExpectedRevision: 3}
}

func TestActionRiskAndCanonicalResolution(t *testing.T) {
	h := newActionHarness(t)
	var calls atomic.Int32
	var got ActionHandlerContext
	if err := h.reg.Register(ActionRegistration{
		BlockKind: "markdown", ActionID: "publish", Intent: "trusted.publish", Summary: "Publish report",
		Risk: RiskExternal, Payload: json.RawMessage(`{"target":"trusted"}`),
		Handler: func(_ context.Context, input ActionHandlerContext) (*ActionResult, error) {
			calls.Add(1)
			got = input
			return &ActionResult{Message: "done"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	value := setupActionBlock(t, h.svc, "risk", BlockActionSpec{
		ID: "publish", Intent: "trusted.publish", Risk: "read", Payload: json.RawMessage(`{"command":"rm -rf *"}`),
	})
	receipt, err := h.svc.ExecuteBlockAction(context.Background(), actionRequest(value, "publish", "risk-action"))
	if err != nil || receipt.Status != string(ActionSucceeded) || calls.Load() != 1 {
		t.Fatalf("receipt=%+v calls=%d err=%v", receipt, calls.Load(), err)
	}
	permission := h.perm.last()
	if permission.Risk != string(RiskExternal) || permission.ToolName != "trusted.publish" || permission.Summary != "Publish report" {
		t.Fatalf("permission used untrusted metadata: %+v", permission)
	}
	if string(got.Payload) != `{"target":"trusted"}` {
		t.Fatalf("handler payload = %s", got.Payload)
	}
}

func TestActionRejectsIntentMismatchAndUnknownHandler(t *testing.T) {
	h := newActionHarness(t)
	h.register(t, "safe", "trusted.safe", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) { return nil, nil })
	value := setupActionBlock(t, h.svc, "mismatch", BlockActionSpec{ID: "safe", Intent: "trusted.danger", Risk: "read"})
	_, err := h.svc.ExecuteBlockAction(context.Background(), actionRequest(value, "safe", "mismatch-action"))
	var mismatch *ErrActionDefinitionMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("want ErrActionDefinitionMismatch, got %T %v", err, err)
	}

	value2 := setupActionBlock(t, h.svc, "unknown", BlockActionSpec{ID: "missing", Intent: "anything", Risk: "read"})
	_, err = h.svc.ExecuteBlockAction(context.Background(), actionRequest(value2, "missing", "unknown-action"))
	var unknown *ErrActionUnknownIntent
	if !errors.As(err, &unknown) {
		t.Fatalf("want ErrActionUnknownIntent, got %T %v", err, err)
	}
}

func TestActionApprovalRiskMatrix(t *testing.T) {
	tests := []struct {
		name    string
		risk    ActionRisk
		confirm bool
		checks  int
	}{
		{"read", RiskRead, false, 0},
		{"read-confirm", RiskRead, true, 1},
		{"write", RiskWrite, false, 1},
		{"destructive", RiskDestructive, false, 1},
		{"external", RiskExternal, false, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newActionHarness(t)
			var calls atomic.Int32
			h.register(t, "run", "trusted."+test.name, test.risk, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
				calls.Add(1)
				return nil, nil
			})
			value := setupActionBlock(t, h.svc, "matrix-"+test.name, BlockActionSpec{ID: "run", Intent: "trusted." + test.name, Risk: string(test.risk), ConfirmRequired: test.confirm})
			receipt, err := h.svc.ExecuteBlockAction(context.Background(), actionRequest(value, "run", "matrix-action-"+test.name))
			if err != nil || receipt.Status != string(ActionSucceeded) || calls.Load() != 1 {
				t.Fatalf("receipt=%+v calls=%d err=%v", receipt, calls.Load(), err)
			}
			h.perm.mu.Lock()
			checks := len(h.perm.requests)
			h.perm.mu.Unlock()
			if checks != test.checks {
				t.Fatalf("permission checks=%d want=%d", checks, test.checks)
			}
		})
	}
}

func TestActionConcurrentDuplicateReturnsSameReceipt(t *testing.T) {
	h := newActionHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	h.register(t, "slow", "trusted.slow", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return &ActionResult{Message: "same", Data: json.RawMessage(`{"ok":true}`)}, nil
	})
	value := setupActionBlock(t, h.svc, "concurrent", BlockActionSpec{ID: "slow", Intent: "trusted.slow", Risk: "read"})
	req := actionRequest(value, "slow", "concurrent-action")

	const count = 12
	receipts := make([]*ActionReceipt, count)
	errs := make([]error, count)
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			receipts[index], errs[index] = h.svc.ExecuteBlockAction(context.Background(), req)
		}()
	}
	<-started
	close(release)
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("handler calls=%d want=1", calls.Load())
	}
	for index := range count {
		if errs[index] != nil || receipts[index] == nil || receipts[index].Status != string(ActionSucceeded) || receipts[index].Fingerprint != receipts[0].Fingerprint || !sameJSON(receipts[index].Result, json.RawMessage(`{"ok":true}`)) {
			t.Fatalf("result[%d]=%+v err=%v", index, receipts[index], errs[index])
		}
	}
}

func TestActionCompetingExecutorCannotRunAfterRejection(t *testing.T) {
	h := newActionHarness(t)
	var calls atomic.Int32
	h.register(t, "write", "trusted.write", RiskWrite, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
		calls.Add(1)
		return nil, nil
	})
	value := setupActionBlock(t, h.svc, "competing", BlockActionSpec{ID: "write", Intent: "trusted.write", Risk: "write"})
	entered, release := make(chan struct{}), make(chan struct{})
	firstPermission := &fakeActionPermission{check: func(context.Context, PermissionRequest) (PermissionDecision, error) {
		close(entered)
		<-release
		return PermissionDecision{Allowed: true}, nil
	}}
	h.svc.SetPermissionChecker(firstPermission)
	second := NewService(h.svc.store, NewBlueprintRegistry(), ViewSinkDiscard)
	second.SetActionRegistry(h.reg)
	second.SetPermissionChecker(&fakeActionPermission{decision: PermissionDecision{Reason: "denied elsewhere"}})
	req := actionRequest(value, "write", "competing-action")
	type result struct {
		receipt *ActionReceipt
		err     error
	}
	firstDone := make(chan result, 1)
	go func() {
		receipt, err := h.svc.ExecuteBlockAction(context.Background(), req)
		firstDone <- result{receipt, err}
	}()
	<-entered
	secondReceipt, secondErr := second.ExecuteBlockAction(context.Background(), req)
	var rejected *ErrActionRejected
	if !errors.As(secondErr, &rejected) || secondReceipt.Status != string(ActionRejected) {
		t.Fatalf("second receipt=%+v err=%T %v", secondReceipt, secondErr, secondErr)
	}
	close(release)
	first := <-firstDone
	if first.err != nil || first.receipt.Status != string(ActionRejected) || calls.Load() != 0 {
		t.Fatalf("first receipt=%+v err=%v calls=%d", first.receipt, first.err, calls.Load())
	}
}

func TestActionFingerprintConflict(t *testing.T) {
	h := newActionHarness(t)
	var calls atomic.Int32
	h.register(t, "run", "trusted.run", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
		calls.Add(1)
		return nil, nil
	})
	value := setupActionBlock(t, h.svc, "fingerprint", BlockActionSpec{ID: "run", Intent: "trusted.run", Risk: "read"})
	req := actionRequest(value, "run", "fingerprint-action")
	req.Input = map[string]any{"value": 1}
	if _, err := h.svc.ExecuteBlockAction(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Input = map[string]any{"value": 2}
	_, err := h.svc.ExecuteBlockAction(context.Background(), req)
	var conflict *ErrActionFingerprintConflict
	if !errors.As(err, &conflict) || calls.Load() != 1 {
		t.Fatalf("err=%T %v calls=%d", err, err, calls.Load())
	}
}

func TestActionStaleRevisionIncludesLatestSnapshot(t *testing.T) {
	h := newActionHarness(t)
	h.register(t, "run", "trusted.run", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) { return nil, nil })
	value := setupActionBlock(t, h.svc, "stale", BlockActionSpec{ID: "run", Intent: "trusted.run", Risk: "read"})
	req := actionRequest(value, "run", "stale-action")
	req.ExpectedRevision = 2
	_, err := h.svc.ExecuteBlockAction(context.Background(), req)
	var conflict *ErrActionRevisionConflict
	if !errors.As(err, &conflict) || conflict.Current != 3 || conflict.Latest == nil || conflict.Latest.Revision != 3 || conflict.Latest.Work.ID != value.ID {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestActionHandlerFailureAndDenialPersist(t *testing.T) {
	t.Run("handler", func(t *testing.T) {
		h := newActionHarness(t)
		var calls atomic.Int32
		h.register(t, "fail", "trusted.fail", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
			calls.Add(1)
			return &ActionResult{Retryable: true}, errors.New("boom")
		})
		value := setupActionBlock(t, h.svc, "handler-fail", BlockActionSpec{ID: "fail", Intent: "trusted.fail", Risk: "read"})
		receipt, err := h.svc.ExecuteBlockAction(context.Background(), actionRequest(value, "fail", "handler-fail-action"))
		if err == nil || receipt.Status != string(ActionFailed) || !receipt.Retryable || !receipt.OutcomeKnown {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
		h.restart(t)
		replay, replayErr := h.svc.ExecuteBlockAction(context.Background(), BlockActionRequest{WorkID: value.ID, BlockID: actionBlockID, ActionID: "fail", RequestID: "handler-fail-action"})
		if replayErr != nil || replay.Status != string(ActionFailed) || calls.Load() != 1 {
			t.Fatalf("replay=%+v err=%v calls=%d", replay, replayErr, calls.Load())
		}
	})

	t.Run("denied", func(t *testing.T) {
		h := newActionHarness(t)
		h.perm.decision = PermissionDecision{Reason: "user denied"}
		var calls atomic.Int32
		h.register(t, "write", "trusted.write", RiskWrite, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
			calls.Add(1)
			return nil, nil
		})
		value := setupActionBlock(t, h.svc, "denied", BlockActionSpec{ID: "write", Intent: "trusted.write", Risk: "write"})
		receipt, err := h.svc.ExecuteBlockAction(context.Background(), actionRequest(value, "write", "denied-action"))
		var rejected *ErrActionRejected
		if !errors.As(err, &rejected) || receipt.Status != string(ActionRejected) || !receipt.Retryable || calls.Load() != 0 {
			t.Fatalf("receipt=%+v err=%T %v calls=%d", receipt, err, err, calls.Load())
		}
		view, _ := h.svc.Get(context.Background(), value.ID)
		stored, _, _ := findActionReceipt(view.Work.ActionReceipts, "denied-action")
		if stored.Status != ActionRejected || stored.Message != "user denied" {
			t.Fatalf("stored=%+v", stored)
		}
	})
}

func TestActionApprovalCancellationPersistsRetryableFailure(t *testing.T) {
	h := newActionHarness(t)
	h.perm.check = func(ctx context.Context, _ PermissionRequest) (PermissionDecision, error) {
		<-ctx.Done()
		return PermissionDecision{}, ctx.Err()
	}
	h.register(t, "write", "trusted.write", RiskWrite, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
		t.Fatal("handler must not run")
		return nil, nil
	})
	value := setupActionBlock(t, h.svc, "cancel", BlockActionSpec{ID: "write", Intent: "trusted.write", Risk: "write"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	receipt, err := h.svc.ExecuteBlockAction(ctx, actionRequest(value, "write", "cancel-action"))
	if !errors.Is(err, context.DeadlineExceeded) || receipt.Status != string(ActionFailed) || !receipt.Retryable || !receipt.OutcomeKnown {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	view, _ := h.svc.Get(context.Background(), value.ID)
	stored, _, _ := findActionReceipt(view.Work.ActionReceipts, "cancel-action")
	if stored.Status != ActionFailed || !stored.Retryable {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestActionRestartRecoveryPendingAndRunning(t *testing.T) {
	t.Run("pending definitely not executed resumes", func(t *testing.T) {
		h := newActionHarness(t)
		var calls atomic.Int32
		h.register(t, "write", "trusted.write", RiskWrite, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
			calls.Add(1)
			return &ActionResult{Message: "resumed"}, nil
		})
		value := setupActionBlock(t, h.svc, "pending-recovery", BlockActionSpec{ID: "write", Intent: "trusted.write", Risk: "write"})
		reserveActionForTest(t, h.svc, value, "write", "pending-recovery-action", ActionPending)
		h.restart(t)
		receipt, err := h.svc.ExecuteBlockAction(context.Background(), BlockActionRequest{WorkID: value.ID, BlockID: actionBlockID, ActionID: "write", RequestID: "pending-recovery-action"})
		if err != nil || receipt.Status != string(ActionSucceeded) || calls.Load() != 1 {
			t.Fatalf("receipt=%+v err=%v calls=%d", receipt, err, calls.Load())
		}
	})

	t.Run("running becomes persisted unknown", func(t *testing.T) {
		h := newActionHarness(t)
		var calls atomic.Int32
		h.register(t, "external", "trusted.external", RiskExternal, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
			calls.Add(1)
			return nil, nil
		})
		value := setupActionBlock(t, h.svc, "running-recovery", BlockActionSpec{ID: "external", Intent: "trusted.external", Risk: "external"})
		reserveActionForTest(t, h.svc, value, "external", "running-recovery-action", ActionRunning)
		h.restart(t)
		receipt, err := h.svc.ExecuteBlockAction(context.Background(), BlockActionRequest{WorkID: value.ID, BlockID: actionBlockID, ActionID: "external", RequestID: "running-recovery-action"})
		var unknown *ErrActionOutcomeUnknown
		if !errors.As(err, &unknown) || receipt.Status != string(ActionUnknown) || receipt.OutcomeKnown || receipt.Retryable || calls.Load() != 0 {
			t.Fatalf("receipt=%+v err=%T %v calls=%d", receipt, err, err, calls.Load())
		}
		h.restart(t)
		replay, replayErr := h.svc.ExecuteBlockAction(context.Background(), BlockActionRequest{WorkID: value.ID, BlockID: actionBlockID, ActionID: "external", RequestID: "running-recovery-action"})
		if !errors.As(replayErr, &unknown) || replay.Status != string(ActionUnknown) || calls.Load() != 0 {
			t.Fatalf("replay=%+v err=%v calls=%d", replay, replayErr, calls.Load())
		}
	})
}

func TestActionPendingRecoveryFailureIsPersisted(t *testing.T) {
	h := newActionHarness(t)
	h.register(t, "run", "trusted.run", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
		t.Fatal("handler must not run")
		return nil, nil
	})
	value := setupActionBlock(t, h.svc, "pending-invalid", BlockActionSpec{ID: "run", Intent: "trusted.run", Risk: "read"})
	reserveActionForTest(t, h.svc, value, "run", "pending-invalid-action", ActionPending)
	h.reg = NewActionRegistry()
	h.restart(t)
	receipt, err := h.svc.ExecuteBlockAction(context.Background(), BlockActionRequest{
		WorkID: value.ID, BlockID: actionBlockID, ActionID: "run", RequestID: "pending-invalid-action",
	})
	var unknown *ErrActionUnknownIntent
	if !errors.As(err, &unknown) || receipt.Status != string(ActionFailed) || receipt.Retryable {
		t.Fatalf("receipt=%+v err=%T %v", receipt, err, err)
	}
	view, _ := h.svc.Get(context.Background(), value.ID)
	stored, _, _ := findActionReceipt(view.Work.ActionReceipts, "pending-invalid-action")
	if stored.Status != ActionFailed {
		t.Fatalf("stored=%+v", stored)
	}
}

func reserveActionForTest(t *testing.T, svc *Service, value *Work, actionID, requestID string, target ActionReceiptStatus) {
	t.Helper()
	view, err := svc.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.resolveAction(view.Work, actionBlockID, actionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, _ := actionInputDigest(value.ID, actionBlockID, actionID, nil)
	now := time.Now().UTC()
	record := ActionReceiptRecord{
		WorkID: value.ID, BlockID: actionBlockID, BlockKind: resolved.block.Kind, ActionID: actionID,
		Status: ActionPending, RequestID: requestID, InputDigest: inputDigest, Fingerprint: resolved.fingerprint,
		Intent: resolved.registration.Intent, Summary: resolved.summary, Risk: resolved.risk,
		ConfirmRequired: resolved.confirm, Retryable: true, OutcomeKnown: true, CreatedAt: now, UpdatedAt: now,
	}
	revision, err := svc.commitActionRecord(value.ID, requestID+"/action/reserved", EventBlockActionReserved, record, view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if target == ActionRunning {
		if _, _, err := svc.transitionAction(context.Background(), record, ActionPending, ActionRunning, "", false, false, nil); err != nil {
			t.Fatal(err)
		}
		_ = revision
	}
}

func TestActionExternalUnknownOutcome(t *testing.T) {
	h := newActionHarness(t)
	h.register(t, "external", "trusted.external", RiskExternal, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
		return &ActionResult{UnknownOutcome: true, Message: "remote timeout"}, context.DeadlineExceeded
	})
	value := setupActionBlock(t, h.svc, "unknown-outcome", BlockActionSpec{ID: "external", Intent: "trusted.external", Risk: "external"})
	receipt, err := h.svc.ExecuteBlockAction(context.Background(), actionRequest(value, "external", "unknown-outcome-action"))
	var unknown *ErrActionOutcomeUnknown
	if !errors.As(err, &unknown) || receipt.Status != string(ActionUnknown) || receipt.OutcomeKnown || receipt.Retryable {
		t.Fatalf("receipt=%+v err=%T %v", receipt, err, err)
	}
}

type failReserveStore struct {
	WorkStore
	err error
}

func (s *failReserveStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	if event.Type == EventBlockActionReserved {
		return 0, s.err
	}
	return s.WorkStore.CommitEvent(workID, event)
}

func TestActionReservationFailurePreventsSideEffect(t *testing.T) {
	h := newActionHarness(t)
	var calls atomic.Int32
	h.register(t, "run", "trusted.run", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
		calls.Add(1)
		return nil, nil
	})
	value := setupActionBlock(t, h.svc, "reserve-fail", BlockActionSpec{ID: "run", Intent: "trusted.run", Risk: "read"})
	sentinel := errors.New("disk unavailable")
	h.svc.store = &failReserveStore{WorkStore: h.svc.store, err: sentinel}
	_, err := h.svc.ExecuteBlockAction(context.Background(), actionRequest(value, "run", "reserve-fail-action"))
	if !errors.Is(err, sentinel) || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestActionEventReplayAndFingerprintCanonical(t *testing.T) {
	h := newActionHarness(t)
	var calls atomic.Int32
	h.register(t, "run", "trusted.run", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) {
		calls.Add(1)
		return &ActionResult{Data: json.RawMessage(`{"answer":42}`)}, nil
	})
	value := setupActionBlock(t, h.svc, "replay", BlockActionSpec{ID: "run", Intent: "trusted.run", Risk: "read"})
	req := actionRequest(value, "run", "replay-action")
	req.Input = map[string]any{"a": 1, "b": 2}
	receipt, err := h.svc.ExecuteBlockAction(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	h.restart(t)
	req.Input = map[string]any{"b": 2, "a": 1}
	replay, err := h.svc.ExecuteBlockAction(context.Background(), req)
	if err != nil || replay.Fingerprint != receipt.Fingerprint || !sameJSON(replay.Result, json.RawMessage(`{"answer":42}`)) || calls.Load() != 1 {
		t.Fatalf("replay=%+v err=%v calls=%d", replay, err, calls.Load())
	}
	view, _ := h.svc.Get(context.Background(), value.ID)
	stored, _, found := findActionReceipt(view.Work.ActionReceipts, "replay-action")
	if !found || stored.Status != ActionSucceeded || stored.Fingerprint == "" {
		t.Fatalf("stored=%+v found=%v", stored, found)
	}
}

func TestActionRegistryValidation(t *testing.T) {
	registry := NewActionRegistry()
	handler := func(context.Context, ActionHandlerContext) (*ActionResult, error) { return nil, nil }
	for _, registration := range []ActionRegistration{
		{ActionID: "a", Intent: "i", Risk: RiskRead, Handler: handler},
		{BlockKind: "k", Intent: "i", Risk: RiskRead, Handler: handler},
		{BlockKind: "k", ActionID: "a", Risk: RiskRead, Handler: handler},
		{BlockKind: "k", ActionID: "a", Intent: "i", Risk: "bad", Handler: handler},
		{BlockKind: "k", ActionID: "a", Intent: "i", Risk: RiskRead},
		{BlockKind: "k", ActionID: "a", Intent: "i", Risk: RiskRead, Payload: json.RawMessage(`{`), Handler: handler},
	} {
		if err := registry.Register(registration); err == nil {
			t.Fatalf("invalid registration accepted: %+v", registration)
		}
	}
	valid := ActionRegistration{BlockKind: "k", ActionID: "a", Intent: "i", Risk: RiskRead, Handler: handler}
	if err := registry.Register(valid); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(valid); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	got, ok := registry.Lookup("k", "a")
	if !ok || got.Intent != "i" {
		t.Fatalf("lookup=%+v ok=%v", got, ok)
	}
}

func TestActionReducerRejectsInvalidTransition(t *testing.T) {
	h := newActionHarness(t)
	h.register(t, "run", "trusted.run", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) { return nil, nil })
	value := setupActionBlock(t, h.svc, "bad-transition", BlockActionSpec{ID: "run", Intent: "trusted.run", Risk: "read"})
	reserveActionForTest(t, h.svc, value, "run", "bad-transition-action", ActionPending)
	view, _ := h.svc.Get(context.Background(), value.ID)
	record, _, _ := findActionReceipt(view.Work.ActionReceipts, "bad-transition-action")
	record.Status, record.UpdatedAt = ActionSucceeded, time.Now().UTC()
	payload, _ := json.Marshal(record)
	event := newServiceEvent(value.ID, "bad-transition/succeeded", EventBlockActionChanged, payload, record.UpdatedAt)
	event.BaseRevision, event.Revision = view.Revision, view.Revision+1
	if _, err := h.svc.store.CommitEvent(value.ID, event); err == nil {
		t.Fatal("pending -> succeeded transition accepted")
	}
}

func TestActionInputRejectsNonJSONValues(t *testing.T) {
	h := newActionHarness(t)
	h.register(t, "run", "trusted.run", RiskRead, func(context.Context, ActionHandlerContext) (*ActionResult, error) { return nil, nil })
	value := setupActionBlock(t, h.svc, "bad-input", BlockActionSpec{ID: "run", Intent: "trusted.run", Risk: "read"})
	req := actionRequest(value, "run", "bad-input-action")
	req.Input = map[string]any{"bad": func() {}}
	if _, err := h.svc.ExecuteBlockAction(context.Background(), req); err == nil {
		t.Fatal("non-JSON input accepted")
	}
}

func sameJSON(left, right json.RawMessage) bool {
	leftCanonical, leftErr := canonicalJSON(left)
	rightCanonical, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func ExampleActionRegistry_Register() {
	registry := NewActionRegistry()
	err := registry.Register(ActionRegistration{
		BlockKind: "action_entry", ActionID: "publish", Intent: "report.publish", Risk: RiskExternal,
		Handler: func(context.Context, ActionHandlerContext) (*ActionResult, error) { return nil, nil },
	})
	fmt.Println(err)
	// Output: <nil>
}
