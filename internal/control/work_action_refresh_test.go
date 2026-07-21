package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"workground2/internal/permission"
	"workground2/internal/work"
)

func registerActionRefreshSource(t *testing.T, c *Controller, source work.BlockSourceAdapter) {
	t.Helper()
	if err := c.RegisterWorkBlockSource(WorkBlockSource{
		Source: work.BlockSource{Provider: "addon:combined", Ref: "fixture", Mode: "query"},
		Kind:   "markdown", Adapter: source,
		Schedule: work.RefreshSchedule{Interval: time.Hour, Backoff: controlBackoff(5 * time.Second)},
	}); err != nil {
		t.Fatalf("RegisterWorkBlockSource: %v", err)
	}
}

func requireActionReceipt(t *testing.T, receipts []work.ActionReceiptRecord, requestID string, status work.ActionReceiptStatus) {
	t.Helper()
	for _, receipt := range receipts {
		if receipt.RequestID == requestID {
			if receipt.Status != status {
				t.Fatalf("receipt %s status=%s want %s", requestID, receipt.Status, status)
			}
			return
		}
	}
	t.Fatalf("receipt %s not found: %+v", requestID, receipts)
}

func TestControllerCloseStopsActionApprovalAndRefreshManager(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, nil), false, 0)
	source := &controlBlockSource{}
	registerActionRefreshSource(t, fixture.controller, source)

	done := make(chan struct {
		receipt *work.ActionReceipt
		err     error
	}, 1)
	go func() {
		receipt, err := fixture.execute(context.Background(), "combined-close-action")
		done <- struct {
			receipt *work.ActionReceipt
			err     error
		}{receipt: receipt, err: err}
	}()
	approval := waitActionApproval(t, fixture.events)

	blockID := "bp-blank-notes"
	if _, err := fixture.controller.WorkControl().RefreshBlock(context.Background(), fixture.value.ID, blockID, "combined-close-refresh"); err != nil {
		t.Fatalf("RefreshBlock: %v", err)
	}
	if state := fixture.controller.WorkRefreshState(fixture.value.ID, blockID); !state.Subscribed {
		t.Fatalf("refresh subscription missing before Close: %+v", state)
	}

	closed := make(chan struct{})
	go func() {
		fixture.controller.Close()
		close(closed)
	}()
	result := <-done
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not join Action and Refresh lifetimes")
	}
	if !errors.Is(result.err, context.Canceled) || result.receipt == nil || result.receipt.Status != string(work.ActionFailed) {
		t.Fatalf("closed action receipt=%+v err=%v", result.receipt, result.err)
	}
	if state := fixture.controller.WorkRefreshState(fixture.value.ID, blockID); state.Subscribed || state.Online || state.Inflight {
		t.Fatalf("refresh manager remained active after Close: %+v", state)
	}
	if _, pending := fixture.controller.PendingInteraction(); pending {
		t.Fatal("Action approval remained pending after Close")
	}
	fixture.controller.Approve(approval.ID, true, false, false)
	if fixture.calls.Load() != 0 {
		t.Fatalf("late Action approval executed handler: calls=%d", fixture.calls.Load())
	}
	view, err := fixture.service.Get(context.Background(), fixture.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	requireActionReceipt(t, view.Work.ActionReceipts, "combined-close-action", work.ActionFailed)
}

func TestActionSessionGrantAndRefreshSourceRegistryAreIsolated(t *testing.T) {
	fixture := newControlActionFixture(t, permission.New("ask", nil, nil, nil), false, 0)
	defer fixture.controller.Close()
	input := actionPermissionRequest()
	allowActionSession(t, fixture.controller, fixture.events, input)

	source := &controlBlockSource{}
	registerActionRefreshSource(t, fixture.controller, source)
	if _, err := fixture.controller.WorkControl().RefreshBlock(context.Background(), fixture.value.ID, "bp-blank-notes", "combined-registry-refresh"); err != nil {
		t.Fatalf("RefreshBlock after Action session grant: %v", err)
	}
	if source.calls.Load() != 1 {
		t.Fatalf("source registry calls=%d want 1", source.calls.Load())
	}

	input.RequestID = "request:after-refresh-registry"
	result := awaitActionPermission(t, startActionPermission(fixture.controller, input))
	if result.err != nil || !result.decision.Allowed {
		t.Fatalf("Action session grant after Refresh registration=%+v", result)
	}
	if _, pending := fixture.controller.PendingInteraction(); pending {
		t.Fatal("Refresh source registration invalidated the exact Action session grant")
	}
}

func TestArchiveDeleteStopRefreshWithoutLosingActionReceipt(t *testing.T) {
	tests := []struct {
		name string
		stop func(*testing.T, *controlActionFixture, string, string) []work.ActionReceiptRecord
	}{
		{
			name: "archive",
			stop: func(t *testing.T, fixture *controlActionFixture, blockID, requestID string) []work.ActionReceiptRecord {
				record, err := fixture.controller.WorkControl().ArchiveWork(context.Background(), fixture.value.ID, requestID)
				if err != nil {
					t.Fatalf("ArchiveWork: %v", err)
				}
				if state := fixture.controller.WorkRefreshState(fixture.value.ID, blockID); state.Subscribed {
					t.Fatalf("subscription retained after archive: %+v", state)
				}
				return record.Snapshot.ActionReceipts
			},
		},
		{
			name: "delete",
			stop: func(t *testing.T, fixture *controlActionFixture, blockID, requestID string) []work.ActionReceiptRecord {
				if err := fixture.controller.WorkControl().DeleteWork(context.Background(), fixture.value.ID, requestID); err != nil {
					t.Fatalf("DeleteWork: %v", err)
				}
				if state := fixture.controller.WorkRefreshState(fixture.value.ID, blockID); state.Subscribed {
					t.Fatalf("subscription retained after delete: %+v", state)
				}
				view, err := fixture.controller.WorkControl().RestoreWork(context.Background(), fixture.value.ID, requestID+"-restore")
				if err != nil {
					t.Fatalf("RestoreWork after Delete: %v", err)
				}
				return view.Work.ActionReceipts
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newControlActionFixture(t, permission.New("allow", nil, nil, nil), false, 0)
			defer fixture.controller.Close()
			source := &controlBlockSource{}
			registerActionRefreshSource(t, fixture.controller, source)
			actionID := "combined-" + test.name + "-action"
			receipt, err := fixture.execute(context.Background(), actionID)
			if err != nil || receipt == nil || receipt.Status != string(work.ActionSucceeded) {
				t.Fatalf("ExecuteBlockAction receipt=%+v err=%v", receipt, err)
			}
			blockID := "bp-blank-notes"
			if _, err := fixture.controller.WorkControl().RefreshBlock(context.Background(), fixture.value.ID, blockID, "combined-"+test.name+"-refresh"); err != nil {
				t.Fatalf("RefreshBlock: %v", err)
			}
			if state := fixture.controller.WorkRefreshState(fixture.value.ID, blockID); !state.Subscribed {
				t.Fatalf("subscription missing before %s: %+v", test.name, state)
			}
			receipts := test.stop(t, fixture, blockID, "combined-"+test.name+"-stop")
			requireActionReceipt(t, receipts, actionID, work.ActionSucceeded)
		})
	}
}

func TestActionRefreshTypedNilWorkFailsClosed(t *testing.T) {
	var service *work.Service
	c := New(Options{
		Work: service,
		WorkBlockSources: []WorkBlockSource{{
			Source: work.BlockSource{Provider: "addon:nil", Mode: "query"}, Kind: "markdown", Adapter: &controlBlockSource{},
		}},
	})
	c.Close()
	if _, err := c.WorkControl().RefreshBlock(context.Background(), "work", "block", "typed-nil-refresh"); !errors.Is(err, errWorkDisabled) {
		t.Fatalf("typed-nil Work RefreshBlock err=%v", err)
	}
	if _, err := c.WorkControl().ExecuteBlockAction(context.Background(), work.BlockActionRequest{
		WorkID: "work", BlockID: "block", ActionID: "action", RequestID: "typed-nil-action",
	}); !errors.Is(err, errWorkDisabled) {
		t.Fatalf("typed-nil Work ExecuteBlockAction err=%v", err)
	}
	if err := c.RegisterWorkBlockSource(WorkBlockSource{}); !errors.Is(err, errWorkDisabled) {
		t.Fatalf("typed-nil Work RegisterWorkBlockSource err=%v", err)
	}
}
