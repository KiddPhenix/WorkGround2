package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type definitionPlannerFunc func(context.Context, DefinitionPlanInput) (*DefinitionPlan, error)

func (f definitionPlannerFunc) PlanDefinition(ctx context.Context, input DefinitionPlanInput) (*DefinitionPlan, error) {
	return f(ctx, input)
}

type retryReadBarrierStore struct {
	WorkStore
	requestID string
	read      chan struct{}
	release   chan struct{}
	once      sync.Once
}

type atomicTaskCommitBarrierStore struct {
	WorkStore
	eventType WorkEventType
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (s *atomicTaskCommitBarrierStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	if event.Type == s.eventType {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	return s.WorkStore.CommitEvent(workID, event)
}

func (s *retryReadBarrierStore) LoadState(workID, requestID string) (*Work, WorkEventState, error) {
	value, state, err := s.WorkStore.LoadState(workID, requestID)
	if err == nil && requestID == s.requestID {
		s.once.Do(func() {
			close(s.read)
			<-s.release
		})
	}
	return value, state, err
}

func cloneArtifactSlot(slot *ArtifactSlot) ArtifactSlot {
	clone := *slot
	clone.ArtifactRefs = append([]ArtifactRef(nil), slot.ArtifactRefs...)
	return clone
}

func TestRetryArtifactSlotTwoFileStoresConcurrentReplayAndRestart(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "producer", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := failedCoordinatorRuntime(t, h)
	_, before, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	generating, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "mark-slot-generating",
		State: SlotGenerating, Revision: 2, ExpectedRevision: before.Revision,
		DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "mark-slot-failed",
		State: SlotFailed, Error: &ArtifactError{Code: "render", Message: "failed", Retryable: true},
		Revision: 3, ExpectedRevision: generating.WorkRevision, DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}

	secondStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	executor := &coordinatorExecutor{}
	firstSvc := NewService(h.store, nil, nil)
	secondSvc := NewService(secondStore, nil, nil)
	firstSvc.SetTaskExecutor(executor)
	secondSvc.SetTaskExecutor(executor)
	request := RetryArtifactSlotRequest{
		WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
		ExpectedRevision: failed.WorkRevision, RequestID: "retry-artifact",
	}

	var wg sync.WaitGroup
	results := make([]*RetryArtifactSlotResult, 2)
	errs := make([]error, 2)
	for i, svc := range []*Service{firstSvc, secondSvc} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			results[i], errs[i] = svc.RetryArtifactSlot(context.Background(), request)
		}(i, svc)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || results[i] == nil || !results[i].Committed ||
			results[i].Slot == nil || results[i].Slot.State != SlotGenerating {
			t.Fatalf("call %d: result=%+v err=%v", i, results[i], errs[i])
		}
	}
	if executor.callCount() != 1 {
		t.Fatalf("same request executed producer %d times, want 1", executor.callCount())
	}

	restartedStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(restartedStore, nil, nil)
	restarted.SetTaskExecutor(executor)
	replay, err := restarted.RetryArtifactSlot(context.Background(), request)
	if err != nil || replay == nil || !replay.Duplicate || !replay.Committed {
		t.Fatalf("restart replay: result=%+v err=%v", replay, err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("restart replay repeated producer side effect: %d", executor.callCount())
	}

	refreshed := request
	refreshed.ExpectedRevision++
	if replay, err := restarted.RetryArtifactSlot(context.Background(), refreshed); err != nil ||
		replay == nil || !replay.Duplicate || !replay.Committed {
		t.Fatalf("refreshed revision must replay same intent: result=%+v err=%v", replay, err)
	}
	differentSlot := request
	differentSlot.SlotID = "different-slot"
	if _, err := restarted.RetryArtifactSlot(context.Background(), differentSlot); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("different slot with same requestID must conflict, got %v", err)
	}
	differentDefinition := request
	differentDefinition.DefinitionRevision++
	if _, err := restarted.RetryArtifactSlot(context.Background(), differentDefinition); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("different definition with same requestID must conflict, got %v", err)
	}
	projection, err := restartedStore.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if got := projection.V2TaskRuntimes[runtime.TaskID]; got == nil || got.RunID != h.run {
		t.Fatalf("active task identity changed: %+v", got)
	}
}

func TestRetryArtifactSlotRebasesUnrelatedAggregateRevisionAndReplaysAfterRefresh(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "producer", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	failedCoordinatorRuntime(t, h)
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	generating, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "rebase-generating",
		State: SlotGenerating, Revision: 2, ExpectedRevision: state.Revision,
		DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "rebase-failed",
		State: SlotFailed, Error: &ArtifactError{Code: "render", Message: "failed", Retryable: true},
		Revision: 3, ExpectedRevision: generating.WorkRevision, DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}

	inactive := CopyOnWriteRevision(h.def)
	inactive.Nodes[0].Title = "inactive candidate change"
	if _, err := h.svc.CreateCandidateRevision(
		context.Background(), h.work, inactive, "rebase-unrelated-candidate", failed.WorkRevision,
	); err != nil {
		t.Fatal(err)
	}
	_, latest, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision <= failed.WorkRevision {
		t.Fatalf("unrelated event did not advance aggregate revision: %d <= %d", latest.Revision, failed.WorkRevision)
	}

	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	request := RetryArtifactSlotRequest{
		WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
		ExpectedRevision: failed.WorkRevision, RequestID: "rebase-unrelated-retry",
	}
	result, err := h.svc.RetryArtifactSlot(context.Background(), request)
	if err != nil || result == nil || !result.Committed || result.Slot == nil ||
		result.Slot.State != SlotGenerating {
		t.Fatalf("rebased retry result=%+v err=%v", result, err)
	}

	replayed, err := h.svc.RetryArtifactSlot(context.Background(), request)
	if err != nil || replayed == nil || !replayed.Duplicate || !replayed.Committed {
		t.Fatalf("rebased retry replay=%+v err=%v", replayed, err)
	}
	refreshed := request
	refreshed.ExpectedRevision = latest.Revision
	if replayed, err := h.svc.RetryArtifactSlot(context.Background(), refreshed); err != nil ||
		replayed == nil || !replayed.Duplicate || !replayed.Committed {
		t.Fatalf("same requestID after refresh must replay: result=%+v err=%v", replayed, err)
	}
}

func TestRetryArtifactSlotAlreadyGeneratingOrReadyIsSuccessfulNoop(t *testing.T) {
	for _, target := range []ArtifactSlotState{SlotGenerating, SlotReady} {
		t.Run(string(target), func(t *testing.T) {
			h := newCoordinatorHarness(t, coordinatorDefinition(
				[]NodeDef{{ID: "n1", Title: "producer", ProducesSlotIDs: []string{"slot"}}},
				nil,
			))
			_, state, err := h.store.LoadState(h.work, "")
			if err != nil {
				t.Fatal(err)
			}
			current, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
				WorkID: h.work, SlotID: "slot", RequestID: "noop-generating-" + string(target),
				State: SlotGenerating, Revision: 2, ExpectedRevision: state.Revision,
				DefinitionRev: h.def.Revision,
			})
			if err != nil {
				t.Fatal(err)
			}
			if target == SlotReady {
				current, err = h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
					WorkID: h.work, SlotID: "slot", RequestID: "noop-ready",
					State: SlotReady, Revision: 3, ExpectedRevision: current.WorkRevision,
					DefinitionRev: h.def.Revision,
					Refs: []ArtifactRef{{
						ID: "final", Name: "final.md", Type: "markdown",
						Status: ArtifactRefStatusAvailable, BlobDigest: "sha256:final",
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			beforeSlotRevision := current.Slot.Revision
			beforeWorkRevision := current.WorkRevision
			executor := &coordinatorExecutor{}
			h.svc.SetTaskExecutor(executor)
			request := RetryArtifactSlotRequest{
				WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
				ExpectedRevision: current.WorkRevision, RequestID: "noop-retry-" + string(target),
			}
			result, err := h.svc.RetryArtifactSlot(context.Background(), request)
			if err != nil || result == nil || !result.Committed || result.Slot == nil ||
				result.Slot.State != target || result.Slot.Revision != beforeSlotRevision {
				t.Fatalf("noop retry result=%+v err=%v", result, err)
			}
			if result.Revision != beforeWorkRevision {
				t.Fatalf("noop retry advanced Work revision %d → %d", beforeWorkRevision, result.Revision)
			}
			_, receipt, err := h.store.LoadState(h.work, request.RequestID+"/slot/artifact-slot")
			if err != nil || receipt.RequestFound {
				t.Fatalf("noop retry wrote request receipt: found=%v err=%v", receipt.RequestFound, err)
			}
			if executor.callCount() != 0 {
				t.Fatalf("noop retry scheduled producer %d times", executor.callCount())
			}

			replayed, err := h.svc.RetryArtifactSlot(context.Background(), request)
			if err != nil || replayed == nil || replayed.Slot == nil ||
				replayed.Slot.State != target || replayed.Slot.Revision != beforeSlotRevision {
				t.Fatalf("noop replay result=%+v err=%v", replayed, err)
			}
			if replayed.Revision != beforeWorkRevision {
				t.Fatalf("noop replay advanced Work revision %d → %d", beforeWorkRevision, replayed.Revision)
			}
			_, receipt, err = h.store.LoadState(h.work, request.RequestID+"/slot/artifact-slot")
			if err != nil || receipt.RequestFound {
				t.Fatalf("noop replay wrote request receipt: found=%v err=%v", receipt.RequestFound, err)
			}
			if executor.callCount() != 0 {
				t.Fatalf("noop replay scheduled producer %d times", executor.callCount())
			}
		})
	}
}

func TestRetryArtifactSlotAlreadySatisfiedConcurrentDifferentRequestIDs(t *testing.T) {
	for _, target := range []ArtifactSlotState{SlotGenerating, SlotReady} {
		t.Run(string(target), func(t *testing.T) {
			h := newCoordinatorHarness(t, coordinatorDefinition(
				[]NodeDef{{ID: "n1", Title: "producer", ProducesSlotIDs: []string{"slot"}}},
				nil,
			))
			_, state, err := h.store.LoadState(h.work, "")
			if err != nil {
				t.Fatal(err)
			}
			current, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
				WorkID: h.work, SlotID: "slot", RequestID: "conc-gen-" + string(target),
				State: SlotGenerating, Revision: 2, ExpectedRevision: state.Revision,
				DefinitionRev: h.def.Revision,
			})
			if err != nil {
				t.Fatal(err)
			}
			if target == SlotReady {
				current, err = h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
					WorkID: h.work, SlotID: "slot", RequestID: "conc-ready",
					State: SlotReady, Revision: 3, ExpectedRevision: current.WorkRevision,
					DefinitionRev: h.def.Revision,
					Refs: []ArtifactRef{{
						ID: "final", Name: "final.md", Type: "markdown",
						Status: ArtifactRefStatusAvailable, BlobDigest: "sha256:final",
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			beforeWorkRevision := current.WorkRevision
			beforeSlotRevision := current.Slot.Revision
			executor := &coordinatorExecutor{}
			h.svc.SetTaskExecutor(executor)

			const concurrency = 8
			var wg sync.WaitGroup
			results := make([]*RetryArtifactSlotResult, concurrency)
			errs := make([]error, concurrency)
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					req := RetryArtifactSlotRequest{
						WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
						ExpectedRevision: current.WorkRevision, RequestID: fmt.Sprintf("conc-%s-%d", string(target), idx),
					}
					results[idx], errs[idx] = h.svc.RetryArtifactSlot(context.Background(), req)
				}(i)
			}
			wg.Wait()

			for i := 0; i < concurrency; i++ {
				r, e := results[i], errs[i]
				if e != nil || r == nil || !r.Committed || r.Slot == nil ||
					r.Slot.State != target || r.Slot.Revision != beforeSlotRevision {
					t.Fatalf("concurrent[%d] result=%+v err=%v", i, r, e)
				}
				if r.Revision != beforeWorkRevision {
					t.Fatalf("concurrent[%d] advanced Work revision %d → %d", i, beforeWorkRevision, r.Revision)
				}
				requestID := fmt.Sprintf("conc-%s-%d", string(target), i)
				_, receipt, loadErr := h.store.LoadState(h.work, requestID+"/slot/artifact-slot")
				if loadErr != nil || receipt.RequestFound {
					t.Fatalf("concurrent[%d] wrote request receipt: found=%v err=%v", i, receipt.RequestFound, loadErr)
				}
			}
			if executor.callCount() != 0 {
				t.Fatalf("concurrent retries scheduled producer %d times", executor.callCount())
			}

			_, finalState, err := h.store.LoadState(h.work, "")
			if err != nil {
				t.Fatal(err)
			}
			if finalState.Revision != beforeWorkRevision {
				t.Fatalf("final Work revision advanced %d → %d", beforeWorkRevision, finalState.Revision)
			}
		})
	}
}

func TestRetryArtifactSlotRejectsActiveDefinitionChange(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "producer-v1", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	_, beforeCandidate, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	request := RetryArtifactSlotRequest{
		WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
		ExpectedRevision: beforeCandidate.Revision, RequestID: "retry-after-definition-change",
	}
	candidate := CopyOnWriteRevision(h.def)
	candidate.Nodes[0].Title = "producer-v2"
	candidate, err = h.svc.CreateCandidateRevision(
		context.Background(), h.work, candidate, "retry-definition-change-candidate", beforeCandidate.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, applyState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := h.svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: applyState.Revision, RequestID: "retry-definition-change-apply",
	}); applied == nil || !applied.Committed {
		t.Fatalf("apply result=%+v err=%v", applied, err)
	}

	result, err := h.svc.RetryArtifactSlot(context.Background(), request)
	var conflict *ErrWorkEventConflict
	if result == nil || result.Committed || !errors.As(err, &conflict) ||
		conflict.Kind != WorkEventRequestConflict {
		t.Fatalf("definition change result=%+v err=%T %v", result, err, err)
	}
	_, receipt, loadErr := h.store.LoadState(h.work, request.RequestID+"/slot/artifact-slot")
	if loadErr != nil || receipt.RequestFound {
		t.Fatalf("definition conflict wrote retry receipt=%+v err=%v", receipt, loadErr)
	}
}

func TestRetryWorkNodeTwoFileStoresConcurrentSameRequestAndRestart(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "retry target", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := failedCoordinatorRuntime(t, h)
	secondStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	executor := &coordinatorExecutor{}
	firstSvc := NewService(h.store, nil, nil)
	secondSvc := NewService(secondStore, nil, nil)
	firstSvc.SetTaskExecutor(executor)
	secondSvc.SetTaskExecutor(executor)
	request := RetryWorkNodeRequest{
		WorkID: h.work, RunID: h.run, TaskID: runtime.TaskID,
		ExpectedRevision: runtime.Revision, RequestID: "retry-node-concurrent",
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*RetryWorkNodeResult, 2)
	errs := make([]error, 2)
	for i, svc := range []*Service{firstSvc, secondSvc} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.RetryWorkNode(context.Background(), request)
		}(i, svc)
	}
	close(start)
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] == nil || !results[i].Committed ||
			results[i].Result == nil ||
			(results[i].Result.State != RunRunning && results[i].Result.State != RunCompleted) {
			t.Fatalf("concurrent call %d: result=%+v err=%v", i, results[i], errs[i])
		}
	}
	if executor.callCount() != 1 {
		t.Fatalf("same retry intent executed %d times, want 1", executor.callCount())
	}
	workPath, err := h.store.workPath(h.work)
	if err != nil {
		t.Fatal(err)
	}
	replay, _, err := ReplayWithReducer(workPath, DefaultReducer())
	if err != nil {
		t.Fatal(err)
	}
	readyEvents := 0
	for _, event := range replay.Events {
		if event.Type == EventTaskReady &&
			event.RequestID == request.RequestID+"/retry-node" {
			readyEvents++
		}
	}
	if readyEvents != 1 {
		t.Fatalf("durable retry events=%d, want 1", readyEvents)
	}

	thirdStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(thirdStore, nil, nil)
	restarted.SetTaskExecutor(executor)
	duplicate, err := restarted.RetryWorkNode(context.Background(), request)
	if err != nil || duplicate == nil || !duplicate.Duplicate || !duplicate.Committed ||
		duplicate.Result == nil || duplicate.Result.State != RunCompleted {
		t.Fatalf("ACK-loss restart replay: result=%+v err=%v", duplicate, err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("restart replay repeated executor side effect: %d", executor.callCount())
	}

	conflict := request
	conflict.ExpectedRevision++
	if _, err := restarted.RetryWorkNode(context.Background(), conflict); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("same requestID with different intent must conflict, got %v", err)
	}
	historical := request
	historical.RequestID = "retry-node-historical"
	historical.RunID = "historical-run"
	if _, err := restarted.RetryWorkNode(context.Background(), historical); err == nil {
		t.Fatal("historical run identity was accepted")
	}
	if executor.callCount() != 1 {
		t.Fatalf("historical run caused executor side effect: %d", executor.callCount())
	}
}

func TestRetryArtifactSlotRejectsMissingActiveProducerWithoutSlotWrite(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "unrelated"}},
		nil,
	))
	failedCoordinatorRuntime(t, h)
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	generating, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "missing-producer-generating",
		State: SlotGenerating, Revision: 2, ExpectedRevision: state.Revision,
		DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "missing-producer-failed",
		State: SlotFailed, Error: &ArtifactError{Code: "render", Message: "failed", Retryable: true},
		Revision: 3, ExpectedRevision: generating.WorkRevision,
		DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RetryArtifactSlot(context.Background(), RetryArtifactSlotRequest{
		WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
		ExpectedRevision: failed.WorkRevision, RequestID: "missing-producer-retry",
	}); err == nil {
		t.Fatal("slot without an active producer was retried")
	}
	projection, after, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := FindArtifactSlotRevision(projection, h.def.Revision, "slot")
	if after.Revision != failed.WorkRevision || slot == nil || slot.State != SlotFailed ||
		slot.Revision != 3 {
		t.Fatalf("rejected retry polluted state: event=%d slot=%+v", after.Revision, slot)
	}
}

func TestRetryArtifactSlotRejectsHistoricalRevisionThenReplaysCurrentAcrossStores(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "producer-v2", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	_, requestState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	late := RetryArtifactSlotRequest{
		WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
		ExpectedRevision: requestState.Revision, RequestID: "late-revision-retry",
	}

	candidate := CopyOnWriteRevision(h.def)
	candidate.Nodes[0].Title = "producer-v3"
	candidate, err = h.svc.CreateCandidateRevision(
		context.Background(), h.work, candidate, "candidate-rev3-slot", requestState.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, applyState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	applied, applyErr := h.svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: applyState.Revision, RequestID: "apply-rev3-slot",
	})
	if applied == nil || !applied.Committed {
		t.Fatalf("apply=%+v err=%v", applied, applyErr)
	}
	beforeLate, beforeLateState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	rev3Before, _ := FindArtifactSlotRevision(beforeLate, candidate.Revision, "slot")
	if rev3Before == nil {
		t.Fatal("rev3 slot missing")
	}
	slotSnapshot := *rev3Before
	callsBeforeLate := executor.callCount()
	lateResult, lateErr := h.svc.RetryArtifactSlot(context.Background(), late)
	var conflict *ErrWorkEventConflict
	if lateResult == nil || !errors.As(lateErr, &conflict) ||
		conflict.Kind != WorkEventRequestConflict {
		t.Fatalf("late result=%+v err=%T %v", lateResult, lateErr, lateErr)
	}
	afterLate, afterLateState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	rev3After, _ := FindArtifactSlotRevision(afterLate, candidate.Revision, "slot")
	if afterLateState.Revision != beforeLateState.Revision ||
		rev3After == nil || !reflect.DeepEqual(*rev3After, slotSnapshot) ||
		executor.callCount() != callsBeforeLate {
		t.Fatalf(
			"historical retry polluted rev3: state %d→%d slot before=%+v after=%+v calls %d→%d",
			beforeLateState.Revision, afterLateState.Revision,
			slotSnapshot, rev3After, callsBeforeLate, executor.callCount(),
		)
	}

	generating, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "rev3-slot-generating",
		State: SlotGenerating, Revision: rev3After.Revision + 1,
		ExpectedRevision: afterLateState.Revision, DefinitionRev: candidate.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "rev3-slot-failed",
		State: SlotFailed, Error: &ArtifactError{Code: "render", Message: "failed", Retryable: true},
		Revision:         generating.Slot.Revision + 1,
		ExpectedRevision: generating.WorkRevision, DefinitionRev: candidate.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := NewService(h.store, nil, nil)
	second := NewService(secondStore, nil, nil)
	first.SetTaskExecutor(executor)
	second.SetTaskExecutor(executor)
	currentRequest := RetryArtifactSlotRequest{
		WorkID: h.work, SlotID: "slot", DefinitionRevision: candidate.Revision,
		ExpectedRevision: failed.WorkRevision, RequestID: "rev3-correct-retry",
	}
	type retryOutcome struct {
		result *RetryArtifactSlotResult
		err    error
	}
	outcomes := make(chan retryOutcome, 2)
	for _, service := range []*Service{first, second} {
		go func(service *Service) {
			result, callErr := service.RetryArtifactSlot(context.Background(), currentRequest)
			outcomes <- retryOutcome{result: result, err: callErr}
		}(service)
	}
	for range 2 {
		got := <-outcomes
		if got.err != nil || got.result == nil || !got.result.Committed ||
			got.result.Slot == nil || got.result.Slot.DefinitionRev != candidate.Revision {
			t.Fatalf("current retry=%+v err=%v", got.result, got.err)
		}
	}
	thirdStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	third := NewService(thirdStore, nil, nil)
	third.SetTaskExecutor(executor)
	replay, err := third.RetryArtifactSlot(context.Background(), currentRequest)
	if err != nil || replay == nil || !replay.Committed || !replay.Duplicate ||
		replay.Slot == nil || replay.Slot.DefinitionRev != candidate.Revision {
		t.Fatalf("third-store replay=%+v err=%v", replay, err)
	}
	different := currentRequest
	different.SlotID = "different-slot"
	if _, err := third.RetryArtifactSlot(context.Background(), different); !errors.As(err, &conflict) ||
		conflict.Kind != WorkEventRequestConflict {
		t.Fatalf("same request with different slot must conflict, got %v", err)
	}
}

func TestRetryArtifactSlotRequiresRevisionAndLosesConcurrentDefinitionSwitch_FileWorkStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "producer-v2", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	failedCoordinatorRuntime(t, h)
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	generating, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "switch-slot-generating",
		State: SlotGenerating, Revision: 2, ExpectedRevision: state.Revision,
		DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "switch-slot-failed",
		State: SlotFailed, Error: &ArtifactError{Code: "render", Message: "failed", Retryable: true},
		Revision: 3, ExpectedRevision: generating.WorkRevision, DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := h.svc.RetryArtifactSlot(context.Background(), RetryArtifactSlotRequest{
		WorkID: h.work, SlotID: "slot", ExpectedRevision: failed.WorkRevision,
		RequestID: "missing-definition-revision",
	}); err == nil || result == nil {
		t.Fatalf("missing revision result=%+v err=%v", result, err)
	}
	_, afterMissing, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if afterMissing.Revision != failed.WorkRevision {
		t.Fatalf("missing definition revision wrote event: %d→%d", failed.WorkRevision, afterMissing.Revision)
	}

	candidate := CopyOnWriteRevision(h.def)
	candidate.Nodes[0].Title = "producer-v3"
	candidate, err = h.svc.CreateCandidateRevision(
		context.Background(), h.work, candidate, "switch-candidate-rev3", afterMissing.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, beforeSwitch, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	barrier := &retryReadBarrierStore{
		WorkStore: h.store, requestID: "concurrent-switch-retry/slot/artifact-slot",
		read: make(chan struct{}), release: make(chan struct{}),
	}
	retryService := NewService(barrier, nil, nil)
	retryService.SetDefinitionRevisionStore(h.store)
	executor := &coordinatorExecutor{}
	retryService.SetTaskExecutor(executor)
	resultCh := make(chan *RetryArtifactSlotResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, callErr := retryService.RetryArtifactSlot(context.Background(), RetryArtifactSlotRequest{
			WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
			ExpectedRevision: beforeSwitch.Revision, RequestID: "concurrent-switch-retry",
		})
		resultCh <- result
		errCh <- callErr
	}()
	<-barrier.read
	switchService := NewService(h.store, nil, nil)
	switchService.SetTaskExecutor(executor)
	applied, applyErr := switchService.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: beforeSwitch.Revision, RequestID: "concurrent-switch-apply",
	})
	if applied == nil || !applied.Committed {
		t.Fatalf("switch apply=%+v err=%v", applied, applyErr)
	}
	callsAfterSwitch := executor.callCount()
	// Snapshot the rev3 slot right after ApplyDefinition. The apply may carry
	// kept runtime/slot context from the old run (projectKeptContexts), so the
	// assertion is that the concurrent retry leaves that authoritative slot
	// byte-identical — not a fixed reserved/ready guess.
	projectionBefore, _, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	rev3Before, _ := FindArtifactSlotRevision(projectionBefore, candidate.Revision, "slot")
	if rev3Before == nil {
		t.Fatal("rev3 slot missing after apply")
	}
	slotSnapshot := cloneArtifactSlot(rev3Before)
	close(barrier.release)
	retryResult, retryErr := <-resultCh, <-errCh
	if retryErr == nil || retryResult == nil || retryResult.Committed {
		t.Fatalf("concurrent retry=%+v err=%v", retryResult, retryErr)
	}
	projection, finalState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	rev3, _ := FindArtifactSlotRevision(projection, candidate.Revision, "slot")
	if rev3 == nil || !reflect.DeepEqual(*rev3, slotSnapshot) ||
		executor.callCount() != callsAfterSwitch {
		t.Fatalf("concurrent retry polluted rev3=%+v snapshot=%+v calls %d→%d",
			rev3, slotSnapshot, callsAfterSwitch, executor.callCount())
	}
	_, retryEvent, err := h.store.LoadState(h.work, "concurrent-switch-retry/slot/artifact-slot")
	if err != nil {
		t.Fatal(err)
	}
	if retryEvent.RequestFound || finalState.Revision != retryEvent.Revision {
		t.Fatalf("concurrent retry event leaked: retry=%+v final=%+v", retryEvent, finalState)
	}
}

func TestRetryArtifactSlotRejectsRevisionSwitchAtInvalidationCommit_FileWorkStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "producer-v2", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := completedCoordinatorRuntime(t, h)
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	generating, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "atomic-invalidate-generating",
		State: SlotGenerating, Revision: 2, ExpectedRevision: state.Revision,
		DefinitionRev: h.def.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := h.svc.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: h.work, SlotID: "slot", RequestID: "atomic-invalidate-failed",
		State: SlotFailed, Revision: 3, ExpectedRevision: generating.WorkRevision,
		DefinitionRev: h.def.Revision,
		Error:         &ArtifactError{Code: "render", Message: "failed", Retryable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := CopyOnWriteRevision(h.def)
	candidate.Nodes[0].Title = "producer-v3"
	candidate, err = h.svc.CreateCandidateRevision(
		context.Background(), h.work, candidate, "atomic-invalidate-candidate", failed.WorkRevision,
	)
	if err != nil {
		t.Fatal(err)
	}

	retryStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	switchStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if retryStore == switchStore {
		t.Fatal("retry and definition switch must use independent FileWorkStore instances")
	}
	barrier := &atomicTaskCommitBarrierStore{
		WorkStore: retryStore, eventType: EventTaskInvalidated,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	executor := &coordinatorExecutor{}
	retryService := NewService(barrier, nil, nil)
	retryService.SetDefinitionRevisionStore(retryStore)
	retryService.SetTaskExecutor(executor)
	type outcome struct {
		result *RetryArtifactSlotResult
		err    error
	}
	done := make(chan outcome, 1)
	request := RetryArtifactSlotRequest{
		WorkID: h.work, SlotID: "slot", DefinitionRevision: h.def.Revision,
		ExpectedRevision: failed.WorkRevision + 1, RequestID: "atomic-invalidate-retry",
	}
	go func() {
		result, callErr := retryService.RetryArtifactSlot(context.Background(), request)
		done <- outcome{result: result, err: callErr}
	}()
	<-barrier.entered

	_, applyState, err := switchStore.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	switchService := NewService(switchStore, nil, nil)
	switchService.SetTaskExecutor(executor)
	applied, applyErr := switchService.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: applyState.Revision, RequestID: "atomic-invalidate-apply",
	})
	if applied == nil || !applied.Committed {
		t.Fatalf("apply=%+v err=%v", applied, applyErr)
	}
	callsAfterSwitch := executor.callCount()
	// Snapshot the rev3 slot after apply. ApplyDefinition may carry kept
	// runtime/slot context from the old run, so verify the historical retry
	// leaves the authoritative slot byte-identical instead of guessing state.
	beforeProj, _, loadErr := switchStore.LoadState(h.work, "")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	rev3Before, _ := FindArtifactSlotRevision(beforeProj, candidate.Revision, "slot")
	if rev3Before == nil {
		t.Fatal("rev3 slot missing after apply")
	}
	slotSnapshot := cloneArtifactSlot(rev3Before)
	close(barrier.release)
	got := <-done
	var conflict *ErrWorkEventConflict
	if got.result == nil || !got.result.Committed || !got.result.Recoverable || got.result.Slot == nil ||
		!errors.As(got.err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
		t.Fatalf("retry=%+v err=%T %v", got.result, got.err, got.err)
	}
	firstSlot := *got.result.Slot
	firstSlot.ArtifactRefs = append([]ArtifactRef(nil), got.result.Slot.ArtifactRefs...)
	if got.result.Slot.Error != nil {
		errorCopy := *got.result.Slot.Error
		firstSlot.Error = &errorCopy
	}
	if executor.callCount() != callsAfterSwitch {
		t.Fatalf("historical invalidation triggered executor: %d→%d", callsAfterSwitch, executor.callCount())
	}
	projection, _, err := switchStore.LoadState(h.work, "atomic-invalidate-retry/invalidate")
	if err != nil {
		t.Fatal(err)
	}
	_, invalidationState, _ := switchStore.LoadState(h.work, "atomic-invalidate-retry/invalidate")
	if invalidationState.RequestFound {
		t.Fatal("historical task.invalidated was committed")
	}
	rev3Slot, _ := FindArtifactSlotRevision(projection, candidate.Revision, "slot")
	if rev3Slot == nil || !reflect.DeepEqual(*rev3Slot, slotSnapshot) {
		t.Fatalf("historical retry polluted active slot: %+v snapshot=%+v", rev3Slot, slotSnapshot)
	}
	if old := projection.V2TaskRuntimes[runtime.TaskID]; old == nil {
		t.Fatal("historical runtime disappeared")
	}
	workPath, err := switchStore.workPath(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	if err := CompactWorkEventLog(workPath, projection, DefaultReducer()); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	_, beforeReplay, err := switchStore.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := recoveryEventCounts(workPath)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reopened == retryStore || reopened == switchStore {
		t.Fatal("compaction recovery must reopen through a third FileWorkStore instance")
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetTaskExecutor(executor)
	duplicate, duplicateErr := restarted.RetryArtifactSlot(context.Background(), request)
	var recovery *ErrWorkCommittedRecovery
	if duplicate == nil || !duplicate.Duplicate || !duplicate.Committed || !duplicate.Recoverable ||
		duplicate.Slot == nil || !reflect.DeepEqual(*duplicate.Slot, firstSlot) || !errors.As(duplicateErr, &recovery) {
		t.Fatalf("restart duplicate=%+v err=%T %v", duplicate, duplicateErr, duplicateErr)
	}
	if executor.callCount() != callsAfterSwitch {
		t.Fatalf("restart duplicate triggered executor: %d→%d", callsAfterSwitch, executor.callCount())
	}
	_, afterReplay, err := reopened.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	afterEvents, err := recoveryEventCounts(workPath)
	if err != nil {
		t.Fatal(err)
	}
	if afterReplay.Revision != beforeReplay.Revision || afterEvents != beforeEvents {
		t.Fatalf("receipt replay wrote events: revision %d→%d events %+v→%+v", beforeReplay.Revision, afterReplay.Revision, beforeEvents, afterEvents)
	}
	_, invalidationState, err = reopened.LoadState(h.work, "atomic-invalidate-retry/invalidate")
	if err != nil || invalidationState.RequestFound {
		t.Fatalf("restart leaked invalidation=%+v err=%v", invalidationState, err)
	}
	conflicting := request
	conflicting.SlotID = "other-slot"
	_, beforeConflict, err := reopened.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	conflictEvents, err := recoveryEventCounts(workPath)
	if err != nil {
		t.Fatal(err)
	}
	callsBeforeConflict := executor.callCount()
	if _, err := restarted.RetryArtifactSlot(context.Background(), conflicting); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("different duplicate intent error=%v", err)
	}
	_, afterConflict, err := reopened.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	afterConflictEvents, err := recoveryEventCounts(workPath)
	if err != nil {
		t.Fatal(err)
	}
	if afterConflict.Revision != beforeConflict.Revision ||
		afterConflictEvents != conflictEvents || executor.callCount() != callsBeforeConflict {
		t.Fatalf(
			"conflicting replay mutated state: revision %d→%d events %+v→%+v executor %d→%d",
			beforeConflict.Revision, afterConflict.Revision,
			conflictEvents, afterConflictEvents,
			callsBeforeConflict, executor.callCount(),
		)
	}
}

func TestRetryWorkNodeRejectsRevisionSwitchAtReadyCommitAndRestart_FileWorkStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "retry-v2", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := failedCoordinatorRuntime(t, h)
	_, beforeCandidate, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate := CopyOnWriteRevision(h.def)
	candidate.Nodes[0].Title = "retry-v3"
	candidate, err = h.svc.CreateCandidateRevision(
		context.Background(), h.work, candidate, "atomic-ready-candidate", beforeCandidate.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	retryStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	switchStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if retryStore == switchStore {
		t.Fatal("retry and definition switch must use independent FileWorkStore instances")
	}
	barrier := &atomicTaskCommitBarrierStore{
		WorkStore: retryStore, eventType: EventTaskReady,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	executor := &coordinatorExecutor{}
	retryService := NewService(barrier, nil, nil)
	retryService.SetDefinitionRevisionStore(retryStore)
	retryService.SetTaskExecutor(executor)
	type outcome struct {
		result *RetryWorkNodeResult
		err    error
	}
	done := make(chan outcome, 1)
	request := RetryWorkNodeRequest{
		WorkID: h.work, RunID: h.run, TaskID: runtime.TaskID,
		ExpectedRevision: runtime.Revision, RequestID: "atomic-ready-retry",
	}
	go func() {
		result, callErr := retryService.RetryWorkNode(context.Background(), request)
		done <- outcome{result: result, err: callErr}
	}()
	<-barrier.entered

	_, applyState, err := switchStore.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	switchService := NewService(switchStore, nil, nil)
	switchService.SetTaskExecutor(executor)
	applied, applyErr := switchService.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: applyState.Revision, RequestID: "atomic-ready-apply",
	})
	if applied == nil || !applied.Committed {
		t.Fatalf("apply=%+v err=%v", applied, applyErr)
	}
	callsAfterSwitch := executor.callCount()
	close(barrier.release)
	got := <-done
	var conflict *ErrWorkEventConflict
	if got.result == nil || !errors.As(got.err, &conflict) ||
		conflict.Kind != WorkEventRevisionConflict || got.result.Committed {
		t.Fatalf("retry=%+v err=%T %v", got.result, got.err, got.err)
	}
	_, readyState, err := switchStore.LoadState(h.work, request.RequestID+"/retry-node")
	if err != nil {
		t.Fatal(err)
	}
	if readyState.RequestFound || executor.callCount() != callsAfterSwitch {
		t.Fatalf("historical ready leaked=%v executor %d→%d", readyState.RequestFound, callsAfterSwitch, executor.callCount())
	}

	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetTaskExecutor(executor)
	replay, replayErr := restarted.RetryWorkNode(context.Background(), request)
	if replayErr == nil || replay == nil || replay.Committed {
		t.Fatalf("historical restart retry=%+v err=%v", replay, replayErr)
	}
	if executor.callCount() != callsAfterSwitch {
		t.Fatalf("historical restart triggered executor: %d→%d", callsAfterSwitch, executor.callCount())
	}
}

func TestHistoricalRuntimeReadyCommitRejectedAcrossFileStores(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "runtime-v2", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := failedCoordinatorRuntime(t, h)
	_, beforeCandidate, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate := CopyOnWriteRevision(h.def)
	candidate.Nodes[0].Title = "runtime-v3"
	candidate, err = h.svc.CreateCandidateRevision(
		context.Background(), h.work, candidate, "runtime-ready-candidate", beforeCandidate.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	staleStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	switchStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if staleStore == switchStore {
		t.Fatal("stale runtime writer and definition switch must use independent FileWorkStore instances")
	}

	next := cloneV2Runtime(runtime)
	next.State = TaskReady
	next.Revision = runtime.Revision + 1
	next.UpdatedAt = time.Now().UTC()
	payload, err := json.Marshal(TaskRuntimeUpdatedPayload{
		TaskID: runtime.TaskID, WorkID: h.work, RunID: h.run,
		ExpectedRevision: runtime.Revision, State: TaskReady, Runtime: *next,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := newServiceEventV2(h.work, "historical-runtime-ready", EventTaskRuntimeUpdated, payload, next.UpdatedAt)
	event.Object = ObjectContext{
		Kind: ObjectTask, ID: runtime.TaskID, WorkID: h.work,
		RunID: h.run, TaskID: runtime.TaskID,
		ExpectedRevision: int64Ptr(runtime.Revision), DefinitionRevision: int64Ptr(h.def.Revision),
	}

	_, applyState, err := switchStore.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	switchService := NewService(switchStore, nil, nil)
	applied, applyErr := switchService.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: applyState.Revision, RequestID: "historical-runtime-apply",
	})
	if applied == nil || !applied.Committed {
		t.Fatalf("apply=%+v err=%v", applied, applyErr)
	}
	_, beforeCommit, err := switchStore.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	executor := &coordinatorExecutor{}
	if _, err := staleStore.CommitEvent(h.work, event); err == nil {
		t.Fatal("historical runtime_updated(ready) commit succeeded")
	} else {
		var conflict *ErrWorkEventConflict
		if !errors.As(err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
			t.Fatalf("commit error=%T %v", err, err)
		}
	}
	projection, afterCommit, err := switchStore.LoadState(h.work, event.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if afterCommit.RequestFound || afterCommit.Revision != beforeCommit.Revision || executor.callCount() != 0 {
		t.Fatalf(
			"historical ready leaked=%v revision %d→%d executor=%d",
			afterCommit.RequestFound, beforeCommit.Revision, afterCommit.Revision, executor.callCount(),
		)
	}
	if current := projection.V2TaskRuntimes[runtime.TaskID]; current != nil && current.State == TaskReady {
		t.Fatalf("historical runtime became ready: %+v", current)
	}
}

func TestRetryWorkNodeDuplicateSurvivesCompactionWithoutScheduling_FileWorkStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "retry", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := failedCoordinatorRuntime(t, h)
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	request := RetryWorkNodeRequest{
		WorkID: h.work, RunID: h.run, TaskID: runtime.TaskID,
		ExpectedRevision: runtime.Revision, RequestID: "compact-retry-node",
	}
	first, err := h.svc.RetryWorkNode(context.Background(), request)
	if err != nil || first == nil || !first.Committed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	calls := executor.callCount()
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	workPath, err := h.store.workPath(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	if err := CompactWorkEventLog(workPath, projection, DefaultReducer()); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetTaskExecutor(executor)
	duplicate, err := restarted.RetryWorkNode(context.Background(), request)
	if err != nil || duplicate == nil || !duplicate.Duplicate || !duplicate.Committed {
		t.Fatalf("compacted duplicate=%+v err=%v", duplicate, err)
	}
	if executor.callCount() != calls {
		t.Fatalf("compacted duplicate scheduled again: %d→%d", calls, executor.callCount())
	}
}

type recoveryCounts struct {
	Invalidated    int
	Ready          int
	RuntimeUpdated int
}

func recoveryEventCounts(workPath string) (recoveryCounts, error) {
	replay, _, err := ReplayWithReducer(workPath, DefaultReducer())
	if err != nil {
		return recoveryCounts{}, err
	}
	var counts recoveryCounts
	for _, event := range replay.Events {
		switch event.Type {
		case EventTaskInvalidated:
			counts.Invalidated++
		case EventTaskReady:
			counts.Ready++
		case EventTaskRuntimeUpdated:
			counts.RuntimeUpdated++
		}
	}
	return counts, nil
}

func completedCoordinatorRuntime(t *testing.T, h *coordinatorHarness) *V2TaskRuntime {
	t.Helper()
	now := time.Now().UTC()
	runtime := V2NewTaskRuntime(h.work, h.run, "n1", h.def.Revision, "read", now)
	emit := storeEventEmitter(h.store, h.work)
	if err := emitRuntimeCreated(emit, runtime, now); err != nil {
		t.Fatal(err)
	}
	if err := emitRuntimeUpdated(emit, runtime, TaskReady, nil, now); err != nil {
		t.Fatal(err)
	}
	attempt := &V2Attempt{
		ID: V2RunAttemptID(runtime.TaskID, 0), Index: 0, State: TaskRunning, StartedAt: now,
		DefinitionRev: runtime.DefinitionRev, ExecutionToken: "completed-token",
	}
	if err := emitRuntimeUpdated(emit, runtime, TaskRunning, attempt, now); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Second)
	if err := updateRuntime(emit, runtime, TaskCompleted, nil, finished, func(next *V2TaskRuntime) {
		next.Attempts[0].State = TaskCompleted
		next.Attempts[0].FinishedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestCreateCandidateRevisionWithResultDoesNotSwitchActiveDefinition(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "base", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	before, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	h.svc.SetV2DefinitionPlanner(definitionPlannerFunc(func(_ context.Context, input DefinitionPlanInput) (*DefinitionPlan, error) {
		return &DefinitionPlan{
			Goal:          "adjusted",
			Nodes:         append([]NodeDef(nil), input.Base.Nodes...),
			ArtifactSlots: append([]ArtifactSlotDef(nil), input.Base.ArtifactSlots...),
			InputSpecs:    append([]InputSpec(nil), input.Base.InputSpecs...),
		}, nil
	}))
	request := CreateCandidateRevisionInput{
		WorkID: h.work, Intent: "adjust the goal",
		BaseDefinitionRevision: h.def.Revision,
		ExpectedRevision:       state.Revision,
		RequestID:              "candidate-ui",
	}
	result, err := h.svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil || result == nil || !result.Committed || result.Candidate == nil || result.Impact == nil {
		t.Fatalf("candidate result=%+v err=%v", result, err)
	}
	after, _, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.V2CurrentRevision != before.V2CurrentRevision {
		t.Fatalf("candidate switched active definition: before=%d after=%d", before.V2CurrentRevision, after.V2CurrentRevision)
	}
	replay, err := h.svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil || replay == nil || !replay.Duplicate ||
		replay.Candidate == nil || replay.Candidate.Revision != result.Candidate.Revision {
		t.Fatalf("candidate replay=%+v err=%v", replay, err)
	}
}

func TestCreateCandidateRevisionWithResultInfersNameFromPlannerGoal(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "base", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	h.svc.SetV2DefinitionPlanner(definitionPlannerFunc(func(_ context.Context, input DefinitionPlanInput) (*DefinitionPlan, error) {
		return &DefinitionPlan{
			Goal:          "生成季度经营分析报告",
			Nodes:         append([]NodeDef(nil), input.Base.Nodes...),
			ArtifactSlots: append([]ArtifactSlotDef(nil), input.Base.ArtifactSlots...),
			InputSpecs:    append([]InputSpec(nil), input.Base.InputSpecs...),
		}, nil
	}))
	request := CreateCandidateRevisionInput{
		WorkID: h.work, Intent: "分析经营数据",
		BaseDefinitionRevision: h.def.Revision,
		ExpectedRevision:       state.Revision,
		RequestID:              "candidate-infer-name",
		InferName:              true,
	}
	result, err := h.svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil || result == nil || !result.Committed {
		t.Fatalf("candidate result=%+v err=%v", result, err)
	}
	after, _, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "生成季度经营分析报告" {
		t.Fatalf("Name = %q, want planner-derived name", after.Name)
	}
	replay, err := h.svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil || replay == nil || !replay.Duplicate {
		t.Fatalf("candidate replay=%+v err=%v", replay, err)
	}
	replayed, _, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Name != after.Name {
		t.Fatalf("replayed Name = %q, want stable %q", replayed.Name, after.Name)
	}
}

func TestCreateCandidateRevisionSparseInferableIntentDoesNotAsk(t *testing.T) {
	store := newTestFileWorkStore(t)
	sink := &serviceSink{next: make(chan WorkViewEvent, 32)}
	svc := NewService(store, nil, sink)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "session-" + t.Name(),
		RequestID: "begin-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	record, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	baseRevision := record.V2CurrentRevision
	if baseRevision == 0 {
		baseRevision = record.V2LatestRevision
	}
	svc.SetV2DefinitionPlanner(definitionPlannerFunc(func(_ context.Context, input DefinitionPlanInput) (*DefinitionPlan, error) {
		input.OnProgress(DefinitionPlanProgress{Kind: "raw", Text: `{"goal":"翻译 PDF"`})
		return &DefinitionPlan{
			Goal: "翻译用户提供的 PDF 并生成英文 PDF",
			Nodes: []NodeDef{
				{ID: "read", Title: "读取 PDF"},
				{ID: "translate", Title: "翻译并生成英文 PDF", DependsOn: []string{"read"}, ProducesSlotIDs: []string{"english_pdf"}},
			},
			ArtifactSlots: []ArtifactSlotDef{{
				ID: "english_pdf", Title: "英文 PDF", Kind: "pdf", ExpectedCount: 1, Required: true,
			}},
			InputSpecs: []InputSpec{},
		}, nil
	}))
	request := CreateCandidateRevisionInput{
		WorkID:                 view.Work.ID,
		Intent:                 "翻译一个中文 PDF 到英文",
		BaseDefinitionRevision: baseRevision,
		ExpectedRevision:       state.Revision,
		RequestID:              "candidate-" + t.Name(),
		InferName:              true,
	}
	result, err := svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Candidate == nil || result.Clarification != nil {
		t.Fatalf("result=%+v, want direct commit without clarification", result)
	}
}

func TestCreateCandidateRevisionPausesOnlyForPlannerStructuralAmbiguity(t *testing.T) {
	store := newTestFileWorkStore(t)
	sink := &serviceSink{next: make(chan WorkViewEvent, 32)}
	svc := NewService(store, nil, sink)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "session-" + t.Name(),
		RequestID: "begin-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	record, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	baseRevision := record.V2CurrentRevision
	if baseRevision == 0 {
		baseRevision = record.V2LatestRevision
	}
	var observedAnswers []DefinitionStructuralAnswer
	svc.SetV2DefinitionPlanner(definitionPlannerFunc(func(_ context.Context, input DefinitionPlanInput) (*DefinitionPlan, error) {
		observedAnswers = append([]DefinitionStructuralAnswer(nil), input.StructuralAnswers...)
		plan := &DefinitionPlan{
			Goal: "处理两组报告并生成汇总",
			Nodes: []NodeDef{
				{ID: "process_a", Title: "处理 A 组报告", ProducesSlotIDs: []string{"report_a"}},
				{ID: "process_b", Title: "处理 B 组报告", ProducesSlotIDs: []string{"report_b"}},
				{
					ID: "summary", Title: "生成汇总", DependsOn: []string{"process_a", "process_b"},
					ConsumesSlotIDs: []string{"report_a", "report_b"}, ProducesSlotIDs: []string{"summary"},
				},
			},
			ArtifactSlots: []ArtifactSlotDef{
				{ID: "report_a", Title: "A 组报告", Kind: "document", ExpectedCount: 1, Required: true},
				{ID: "report_b", Title: "B 组报告", Kind: "document", ExpectedCount: 1, Required: true},
				{ID: "summary", Title: "汇总报告", Kind: "document", ExpectedCount: 1, Required: true},
			},
			InputSpecs: []InputSpec{},
		}
		if len(input.StructuralAnswers) == 0 {
			plan.StructuralQuestions = []DefinitionStructuralClarification{{
				ID:          "report_topology",
				Impact:      definitionImpactDependencies,
				Question:    "A、B 两组报告应独立并行处理，还是 B 组必须使用 A 组的结果？",
				Description: "任务说明没有给出两组报告之间的数据依赖，两种结构都会改变执行拓扑。",
				Options: []DefinitionStructuralOption{
					{ID: "parallel", Label: "两组独立并行", Description: "A、B 两组互不依赖，完成后再汇总"},
					{ID: "a_then_b", Label: "A 完成后处理 B", Description: "B 组节点依赖 A 组结果"},
				},
			}}
		}
		return plan, nil
	}))
	request := CreateCandidateRevisionInput{
		WorkID:                 view.Work.ID,
		Intent:                 "处理 A、B 两组报告并汇总；它们的关系由我决定",
		BaseDefinitionRevision: baseRevision,
		ExpectedRevision:       state.Revision,
		RequestID:              "candidate-" + t.Name(),
		InferName:              true,
	}
	first, err := svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Committed || first.Candidate != nil || first.Clarification == nil || !first.Recoverable {
		t.Fatalf("first result=%+v, want recoverable clarification without commit", first)
	}
	if first.Clarification.ID != "report_topology" || first.Clarification.Impact != definitionImpactDependencies {
		t.Fatalf("clarification=%+v, want planner-provided dependency ambiguity", first.Clarification)
	}
	if len(first.Clarification.Flow) != 3 || len(first.Clarification.Options) != 2 {
		t.Fatalf("clarification=%+v, want derived flow and two neutral options", first.Clarification)
	}
	for _, option := range first.Clarification.Options {
		if option.Recommended {
			t.Fatalf("option=%+v, non-inferable choice must not have a default", option)
		}
	}

	request.RequestID += "-answered"
	request.StructuralAnswers = []DefinitionStructuralAnswer{{
		QuestionID: first.Clarification.ID,
		OptionID:   "parallel",
		Value:      "两组独立并行",
	}}
	second, err := svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Committed || second.Candidate == nil || second.Clarification != nil {
		t.Fatalf("second result=%+v, want committed candidate", second)
	}
	if len(observedAnswers) != 1 || observedAnswers[0].OptionID != "parallel" {
		t.Fatalf("planner answers=%+v", observedAnswers)
	}
}

func TestDefinitionStructuralQuestionRejectsInferableDefault(t *testing.T) {
	plan := &DefinitionPlan{
		Nodes: []NodeDef{{ID: "first", Title: "第一步"}, {ID: "second", Title: "第二步"}},
		StructuralQuestions: []DefinitionStructuralClarification{{
			ID:       "execution_shape",
			Impact:   definitionImpactDependencies,
			Question: "两个节点应并行还是串行？",
			Options: []DefinitionStructuralOption{
				{ID: "parallel", Label: "并行", Recommended: true},
				{ID: "sequential", Label: "串行"},
			},
		}},
	}
	_, err := nextDefinitionStructuralClarification(plan, nil)
	if err == nil || !strings.Contains(err.Error(), "must not recommend") {
		t.Fatalf("err=%v, want recommended-default rejection", err)
	}
}

func TestCreateCandidateRevisionPlannerTwoFileStoresReplayACKLossAndRestart(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "base", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	secondStore, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	planner := definitionPlannerFunc(func(_ context.Context, input DefinitionPlanInput) (*DefinitionPlan, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return &DefinitionPlan{
			Goal: "generate and review report",
			Nodes: []NodeDef{
				{ID: "collect", Title: "Collect", InputSpecIDs: []string{"topic"}, ProducesSlotIDs: []string{"source"}},
				{ID: "review", Title: "Review", DependsOn: []string{"collect"}, ConsumesSlotIDs: []string{"source"}, ProducesSlotIDs: []string{"report"}},
			},
			ArtifactSlots: []ArtifactSlotDef{
				{ID: "source", Title: "Source", Kind: "text", ExpectedCount: 1, Required: true},
				{ID: "report", Title: "Report", Kind: "document", ExpectedCount: 1, Required: true},
			},
			InputSpecs: []InputSpec{{ID: "topic", Label: "Topic", Kind: InputText, Required: true}},
		}, nil
	})
	first := NewService(h.store, nil, nil)
	second := NewService(secondStore, nil, nil)
	first.SetV2DefinitionPlanner(planner)
	second.SetV2DefinitionPlanner(planner)
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	request := CreateCandidateRevisionInput{
		WorkID: h.work, Intent: "split collection and review, add topic input and report output",
		BaseDefinitionRevision: h.def.Revision, ExpectedRevision: state.Revision,
		RequestID: "planner-concurrent-restart",
	}
	type outcome struct {
		result *CreateCandidateRevisionResult
		err    error
	}
	results := make(chan outcome, 2)
	go func() {
		result, callErr := first.CreateCandidateRevisionWithResult(context.Background(), request)
		results <- outcome{result: result, err: callErr}
	}()
	<-entered
	go func() {
		result, callErr := second.CreateCandidateRevisionWithResult(context.Background(), request)
		results <- outcome{result: result, err: callErr}
	}()
	close(release)
	left, right := <-results, <-results
	for i, got := range []outcome{left, right} {
		if got.err != nil || got.result == nil || !got.result.Committed || got.result.Candidate == nil {
			t.Fatalf("result[%d]=%+v err=%v", i, got.result, got.err)
		}
		if len(got.result.Candidate.Nodes) != 2 || len(got.result.Candidate.ArtifactSlots) != 2 ||
			len(got.result.Candidate.InputSpecs) != 1 {
			t.Fatalf("planner structure was not preserved: %+v", got.result.Candidate)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("production planner calls=%d, want 1 across two FileWorkStores", calls.Load())
	}

	// Simulate an ACK loss: discard the first response, reopen the store, and
	// retry without any planner configured. The event-backed receipt is enough.
	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	replay, err := restarted.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil || replay == nil || !replay.Committed || !replay.Duplicate || replay.Candidate == nil {
		t.Fatalf("restart replay=%+v err=%v", replay, err)
	}
	_, applyState, err := reopened.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	restarted.SetTaskExecutor(&coordinatorExecutor{})
	applied, applyErr := restarted.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: replay.Candidate.Revision,
		ExpectedRevision: applyState.Revision, RequestID: "apply-after-candidate-ack-loss",
	})
	if applied == nil || !applied.Committed {
		t.Fatalf("apply candidate=%+v err=%v", applied, applyErr)
	}
	afterApplyReplay, err := restarted.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil || afterApplyReplay == nil || !afterApplyReplay.Duplicate ||
		afterApplyReplay.Candidate == nil ||
		afterApplyReplay.Candidate.Revision != replay.Candidate.Revision {
		t.Fatalf("replay after active revision advanced=%+v err=%v", afterApplyReplay, err)
	}
	conflict := request
	conflict.Intent = "a different structure"
	_, err = restarted.CreateCandidateRevisionWithResult(context.Background(), conflict)
	var eventConflict *ErrWorkEventConflict
	if !errors.As(err, &eventConflict) || eventConflict.Kind != WorkEventRequestConflict {
		t.Fatalf("same requestID with different intent must conflict, got %v", err)
	}
}

func TestCreateCandidateRevisionPlannerUnavailableFailsClosedThenRetries(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "base", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	_, before, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	request := CreateCandidateRevisionInput{
		WorkID: h.work, Intent: "change the goal",
		BaseDefinitionRevision: h.def.Revision, ExpectedRevision: before.Revision,
		RequestID: "planner-unavailable-retry",
	}
	failed, err := h.svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if !errors.Is(err, ErrDefinitionPlannerUnavailable) || failed == nil || !failed.Recoverable ||
		failed.TransportError == nil || failed.TransportError.Code != "planner_unavailable" {
		t.Fatalf("unavailable result=%+v err=%v", failed, err)
	}
	_, after, err := h.store.LoadState(h.work, request.RequestID+"/candidate")
	if err != nil {
		t.Fatal(err)
	}
	if after.RequestFound || after.Revision != before.Revision {
		t.Fatalf("planner failure wrote state: before=%d after=%+v", before.Revision, after)
	}
	h.svc.SetV2DefinitionPlanner(definitionPlannerFunc(func(_ context.Context, input DefinitionPlanInput) (*DefinitionPlan, error) {
		return &DefinitionPlan{
			Goal: "changed after retry", Nodes: input.Base.Nodes,
			ArtifactSlots: input.Base.ArtifactSlots, InputSpecs: input.Base.InputSpecs,
		}, nil
	}))
	retried, err := h.svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if err != nil || retried == nil || !retried.Committed || retried.Candidate == nil {
		t.Fatalf("safe retry=%+v err=%v", retried, err)
	}
}

func TestCreateCandidateRevisionRebasesUnrelatedWorkRevisionAfterPlanning(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{
			ID: "n1", Title: "base", InputSpecIDs: []string{"topic"},
			ProducesSlotIDs: []string{"slot"},
		}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	))
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	h.svc.SetV2DefinitionPlanner(definitionPlannerFunc(func(context.Context, DefinitionPlanInput) (*DefinitionPlan, error) {
		calls.Add(1)
		close(entered)
		<-release
		return &DefinitionPlan{
			Goal: "improved plan",
			Nodes: []NodeDef{{
				ID: "n1", Title: "improved", InputSpecIDs: []string{"topic"},
				ProducesSlotIDs: []string{"slot"},
			}},
			ArtifactSlots: []ArtifactSlotDef{{
				ID: "slot", Title: "Output", Kind: "text", ExpectedCount: 1, Required: true,
			}},
			InputSpecs: []InputSpec{{
				ID: "topic", Label: "Topic", Kind: InputText, Required: true,
				ValueSchema: json.RawMessage(`{"type":"string"}`),
			}},
		}, nil
	}))
	_, before, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	request := CreateCandidateRevisionInput{
		WorkID: h.work, Intent: "improve the active work",
		BaseDefinitionRevision: h.def.Revision, ExpectedRevision: before.Revision,
		RequestID: "candidate-rebase-after-planning",
	}
	type outcome struct {
		result *CreateCandidateRevisionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, callErr := h.svc.CreateCandidateRevisionWithResult(context.Background(), request)
		done <- outcome{result: result, err: callErr}
	}()
	<-entered

	// Simulate a runtime/input event committed while the model is planning.
	// The active Definition remains unchanged, so the generated candidate can
	// safely rebase onto the new aggregate Work revision.
	requestCoordinatorInput(t, h, "topic")
	close(release)

	got := <-done
	if got.err != nil || got.result == nil || !got.result.Committed || got.result.Candidate == nil {
		t.Fatalf("candidate did not rebase after unrelated event: result=%+v err=%v", got.result, got.err)
	}
	if got.result.Candidate.ParentRevision != h.def.Revision {
		t.Fatalf("candidate parent=%d, want active definition %d", got.result.Candidate.ParentRevision, h.def.Revision)
	}
	if calls.Load() != 1 {
		t.Fatalf("planner calls=%d, want one preserved plan", calls.Load())
	}
	_, after, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.result.Revision != after.Revision || after.Revision <= before.Revision+1 {
		t.Fatalf("candidate revision=%d state=%d before=%d", got.result.Revision, after.Revision, before.Revision)
	}
}

func TestCreateCandidateRevisionRejectsInvalidPlannerOutputWithoutWrite(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "base", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	_, before, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	h.svc.SetV2DefinitionPlanner(definitionPlannerFunc(func(_ context.Context, _ DefinitionPlanInput) (*DefinitionPlan, error) {
		return &DefinitionPlan{
			Goal:  "invalid",
			Nodes: []NodeDef{{ID: "n1", Title: "no producer"}},
			ArtifactSlots: []ArtifactSlotDef{{
				ID: "orphan", Title: "Orphan", Kind: "text", ExpectedCount: 1, Required: true,
			}},
		}, nil
	}))
	request := CreateCandidateRevisionInput{
		WorkID: h.work, Intent: "create an orphan output",
		BaseDefinitionRevision: h.def.Revision, ExpectedRevision: before.Revision,
		RequestID: "planner-invalid-output",
	}
	result, err := h.svc.CreateCandidateRevisionWithResult(context.Background(), request)
	if !errors.Is(err, ErrDefinitionPlannerFailed) || result == nil || !result.Recoverable ||
		result.TransportError == nil || result.TransportError.Code != "planner_failed" {
		t.Fatalf("invalid result=%+v err=%v", result, err)
	}
	_, after, err := h.store.LoadState(h.work, request.RequestID+"/candidate")
	if err != nil {
		t.Fatal(err)
	}
	if after.RequestFound || after.Revision != before.Revision {
		t.Fatalf("invalid planner output wrote state: before=%d after=%+v", before.Revision, after)
	}
}

func TestSubmitInputCommittedReceiptMissingIsRecoverable_FileWorkStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{
			ID: "n1", Title: "input task", BlockIDs: []string{"b1"},
			InputSpecIDs: []string{"topic"}, ProducesSlotIDs: []string{"slot"},
		}},
		[]InputSpec{{ID: "topic", Label: "Topic", Kind: InputText, Required: true}},
	))
	input := requestCoordinatorInput(t, h, "topic")
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	request := SubmitInputRequest{
		WorkID: h.work, InputID: input.ID, Value: json.RawMessage(`"missing receipt"`),
		DefinitionRev: h.def.Revision, InputRevision: input.Revision,
		ExpectedRevision: state.Revision, RequestID: "submit-missing-receipt",
	}
	payload, _ := json.Marshal(InputSubmittedPayload{
		InputID: input.ID, WorkID: h.work, RunID: input.RunID, TaskID: input.TaskID,
		BlockID: input.BlockID, SpecID: input.SpecID, Value: request.Value,
		Revision: input.Revision + 1, ExpectedRevision: input.Revision,
		AffectedTaskIDs: []string{input.TaskID}, Receipt: nil,
	})
	event := newServiceEventV2(h.work, request.RequestID+"/submit", EventInputSubmitted, payload, time.Now().UTC())
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: input.ID, WorkID: h.work, RunID: input.RunID,
		TaskID: input.TaskID, BlockID: input.BlockID, InputID: input.ID, SpecID: input.SpecID,
		ExpectedRevision: int64Ptr(input.Revision), DefinitionRevision: int64Ptr(h.def.Revision),
	}
	if _, err := h.store.CommitEvent(h.work, event); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	result, err := restarted.SubmitV2Input(context.Background(), request)
	var recovery *ErrWorkCommittedRecovery
	if !errors.As(err, &recovery) || result == nil || !result.Committed || !result.Recoverable ||
		result.Receipt != nil || result.TransportError == nil || result.TransportError.Code != "committed_recovery" {
		t.Fatalf("missing receipt result=%+v err=%T %v", result, err, err)
	}
}

func TestSubmitInputACKLossDuplicateRestartReturnsPersistedReceipt_FileWorkStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{
			ID: "n1", Title: "input task", BlockIDs: []string{"b1"},
			InputSpecIDs: []string{"topic"}, ProducesSlotIDs: []string{"slot"},
		}},
		[]InputSpec{{ID: "topic", Label: "Topic", Kind: InputText, Required: true}},
	))
	input := requestCoordinatorInput(t, h, "topic")
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	request := SubmitInputRequest{
		WorkID: h.work, InputID: input.ID, Value: json.RawMessage(`"ack lost"`),
		DefinitionRev: h.def.Revision, InputRevision: input.Revision,
		ExpectedRevision: state.Revision, RequestID: "submit-ack-loss-restart",
	}
	h.svc.SetTaskExecutor(&coordinatorExecutor{})
	first, err := h.svc.SubmitV2Input(context.Background(), request)
	if err != nil || first == nil || !first.Committed || first.Receipt == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetTaskExecutor(&coordinatorExecutor{})
	replay, err := restarted.SubmitV2Input(context.Background(), request)
	if err != nil || replay == nil || !replay.Committed || !replay.Duplicate ||
		replay.Receipt == nil || replay.Receipt.RequestID != first.Receipt.RequestID ||
		replay.Receipt.IntentDigest != first.Receipt.IntentDigest ||
		replay.Receipt.ResultDigest != first.Receipt.ResultDigest {
		t.Fatalf("restart replay=%+v err=%v first=%+v", replay, err, first)
	}
}

func TestCornerstoneCommittedReceiptMissingIsRecoverable_FileWorkStore(t *testing.T) {
	for _, pin := range []bool{true, false} {
		t.Run(map[bool]string{true: "pin", false: "unpin"}[pin], func(t *testing.T) {
			inputs, svc, store, _ := newInputServiceTest(t)
			workID, inputID := createV2WorkWithInput(t, svc, store)
			workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
			submitted, err := inputs.SubmitInput(context.Background(), SubmitInputRequest{
				WorkID: workID, InputID: inputID, Value: json.RawMessage(`"cornerstone"`),
				DefinitionRev: defRev, InputRevision: inputRev, ExpectedRevision: workRev,
				RequestID: "prepare-" + t.Name(),
			})
			if err != nil || submitted == nil || submitted.Input == nil {
				t.Fatalf("prepare submit=%+v err=%v", submitted, err)
			}
			projection, state, err := store.LoadState(workID, "")
			if err != nil {
				t.Fatal(err)
			}
			index := findInputIndex(projection, inputID)
			if index < 0 {
				t.Fatal("prepared input missing")
			}
			current := projection.V2Inputs[index]
			request := SetInputCornerstoneRequest{
				WorkID: workID, InputID: inputID, Pin: pin,
				DefinitionRevision: defRev, InputRevision: current.Revision,
				ExpectedRevision: state.Revision, RequestID: "cornerstone-missing-" + t.Name(),
			}
			suffix := "/unpin-cs"
			cornerstoneID := ""
			if pin {
				suffix = "/pin-cs"
				cornerstoneID = "cornerstone-legacy"
			}
			payload, _ := json.Marshal(InputCornerstoneChangedPayload{
				InputID: inputID, WorkID: workID, RunID: current.RunID, TaskID: current.TaskID,
				BlockID: current.BlockID, SpecID: current.SpecID, CornerstoneID: cornerstoneID,
				Pinned: pin, Revision: current.Revision + 1, ExpectedRevision: current.Revision,
				Receipt: nil,
			})
			event := newServiceEventV2(workID, request.RequestID+suffix, EventInputCornerstoneChanged, payload, time.Now().UTC())
			event.BaseRevision, event.Revision = state.Revision, state.Revision+1
			event.Object = ObjectContext{
				Kind: ObjectInput, ID: inputID, WorkID: workID, RunID: current.RunID,
				TaskID: current.TaskID, BlockID: current.BlockID, InputID: inputID, SpecID: current.SpecID,
				ExpectedRevision: int64Ptr(current.Revision), DefinitionRevision: int64Ptr(defRev),
			}
			if _, err := store.CommitEvent(workID, event); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewFileWorkStore(store.workDir, 0)
			if err != nil {
				t.Fatal(err)
			}
			restarted := NewService(reopened, nil, nil)
			result, err := restarted.SetInputCornerstone(context.Background(), request)
			var recovery *ErrWorkCommittedRecovery
			if !errors.As(err, &recovery) || result == nil || !result.Committed || !result.Recoverable ||
				result.Receipt != nil || result.TransportError == nil ||
				result.TransportError.Code != "committed_recovery" {
				t.Fatalf("missing receipt result=%+v err=%T %v", result, err, err)
			}
		})
	}
}

func TestSubmitInputTransportReturnsTypedReceipt_FileWorkStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{
			ID: "n1", Title: "input task", BlockIDs: []string{"b1"},
			InputSpecIDs: []string{"topic"}, ProducesSlotIDs: []string{"slot"},
		}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
		}},
	))
	input := requestCoordinatorInput(t, h, "topic")
	h.svc.SetTaskExecutor(&coordinatorExecutor{})
	result, err := submitCoordinatorInput(t, h, input, "submit-typed-receipt")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Committed || result.Receipt == nil ||
		result.Receipt.RequestID != "submit-typed-receipt" ||
		result.Receipt.ResultInput == nil ||
		result.Receipt.ResultInput.ID != input.ID {
		t.Fatalf("typed input result=%+v", result)
	}
}
