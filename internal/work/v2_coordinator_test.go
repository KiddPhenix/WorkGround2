package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type coordinatorExecutor struct {
	blockRun string
	started  chan TaskExecuteInput
	release  chan struct{}
	fail     bool

	mu    sync.Mutex
	calls []TaskExecuteInput
}

func (e *coordinatorExecutor) ExecuteTask(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
	e.mu.Lock()
	e.calls = append(e.calls, input)
	callIndex := len(e.calls)
	e.mu.Unlock()
	if input.RunID == e.blockRun && callIndex == 1 {
		e.started <- input
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.release:
		}
	}
	if e.fail {
		return nil, errors.New("injected retryable failure")
	}
	return &Attempt{
		State:      RunCompleted,
		SessionRef: SessionRef{SessionPath: "sessions/" + input.AttemptID + ".jsonl"},
	}, nil
}

func (e *coordinatorExecutor) CancelTask(context.Context, TaskCancelInput) error { return nil }

func (e *coordinatorExecutor) TaskArtifacts(
	_ context.Context,
	input TaskExecuteInput,
	_ *Attempt,
) ([]TaskArtifactOutput, error) {
	outputs := make([]TaskArtifactOutput, 0, len(input.ProducesSlotIDs))
	for _, slotID := range input.ProducesSlotIDs {
		outputs = append(outputs, TaskArtifactOutput{
			SlotID: slotID,
			Refs: []ArtifactRef{{
				ID:          input.AttemptID + "-" + slotID,
				Name:        slotID,
				Type:        "text",
				Status:      ArtifactRefStatusAvailable,
				BlobDigest:  ContentDigest([]byte(input.AttemptID + ":" + slotID)),
				SourceRunID: input.RunID,
			}},
		})
	}
	return outputs, nil
}

func (e *coordinatorExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

type coordinatorPatchPlanner struct{}

func (coordinatorPatchPlanner) PlanPatch(_ context.Context, in PatchPlanInput) (*PatchPlan, error) {
	if in.Instruction != "rename-block" {
		return nil, errors.New("unexpected patch instruction")
	}
	return &PatchPlan{Operations: []PatchOp{{
		Op: "replace", Path: "blocks/b1/title", NewValue: json.RawMessage(`"renamed"`),
	}}}, nil
}

type failLoadDefinitionStore struct {
	DefinitionRevisionStore
	fail bool
}

func (s *failLoadDefinitionStore) LoadRevision(workID string, revision int64) (*WorkDefinitionRevision, error) {
	if s.fail {
		return nil, errors.New("injected definition load failure")
	}
	return s.DefinitionRevisionStore.LoadRevision(workID, revision)
}

type coordinatorHarness struct {
	store *FileWorkStore
	svc   *Service
	work  string
	run   string
	def   *WorkDefinitionRevision
}

func newCoordinatorHarness(t *testing.T, definition *WorkDefinitionRevision) *coordinatorHarness {
	return newCoordinatorHarnessWithSink(t, definition, nil)
}

func newCoordinatorHarnessWithSink(
	t *testing.T,
	definition *WorkDefinitionRevision,
	sink ViewSink,
) *coordinatorHarness {
	t.Helper()
	store := newTestFileWorkStore(t)
	svc := NewService(store, nil, sink)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "session-" + t.Name(),
		RequestID: "begin-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.WorkID = view.Work.ID
	candidate, err := svc.CreateCandidateRevision(
		context.Background(),
		view.Work.ID,
		definition,
		"candidate-"+t.Name(),
		view.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID:           view.Work.ID,
		Revision:         candidate.Revision,
		ExpectedRevision: state.Revision,
		RequestID:        "apply-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &coordinatorHarness{
		store: store,
		svc:   svc,
		work:  view.Work.ID,
		run:   applied.Intent.RunID,
		def:   candidate,
	}
}

func coordinatorDefinition(nodes []NodeDef, specs []InputSpec) *WorkDefinitionRevision {
	return &WorkDefinitionRevision{
		Goal:       "coordinator production test",
		Nodes:      nodes,
		InputSpecs: specs,
		ArtifactSlots: []ArtifactSlotDef{{
			ID: "slot", Title: "Output", Kind: "text", ExpectedCount: 1, Required: true,
		}},
		CreatedBy: "test",
	}
}

func TestV2Coordinator_MaterializesWaitingInputsIdempotently(t *testing.T) {
	specs := []InputSpec{
		{ID: "theme", Label: "派对主题", Kind: InputText, Required: true},
		{ID: "budget", Label: "预算金额", Kind: InputNumber, Required: true},
		{ID: "guests", Label: "宾客人数", Kind: InputNumber, Required: true},
		{ID: "date", Label: "派对日期", Kind: InputDate, Required: true},
	}
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{
			ID: "party", Title: "策划派对", BlockIDs: []string{"party-inputs"},
			InputSpecIDs:    []string{"theme", "budget", "guests", "date"},
			ProducesSlotIDs: []string{"slot"},
		}},
		specs,
	))
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)

	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"party"}); err != nil {
		t.Fatal(err)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := DeriveTaskID(h.run, "party")
	if err != nil {
		t.Fatal(err)
	}
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime == nil || runtime.State != TaskWaitingInput {
		t.Fatalf("runtime = %+v, want waiting_input", runtime)
	}
	if executor.callCount() != 0 {
		t.Fatalf("executor calls = %d, want 0 before required inputs", executor.callCount())
	}
	if len(projection.V2Inputs) != len(specs) {
		t.Fatalf("inputs = %+v, want %d materialized inputs", projection.V2Inputs, len(specs))
	}
	for index, spec := range specs {
		input := findV2TaskInput(projection.V2Inputs, h.run, taskID, spec.ID)
		if input == nil || input.BlockID != "party-inputs" || input.State != InputRequested {
			t.Fatalf("input[%d] %q = %+v", index, spec.ID, input)
		}
	}

	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"party"}); err != nil {
		t.Fatal(err)
	}
	repeated, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated.V2Inputs) != len(specs) {
		t.Fatalf("repeated scheduling created duplicate inputs: %+v", repeated.V2Inputs)
	}
}

func TestV2Coordinator_RepairsCompletedRunWithoutArtifactsAndRetries(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "Producer", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := completedCoordinatorRuntime(t, h)
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)

	if err := h.svc.RecoverV2Scheduling(context.Background(), h.work); err != nil {
		t.Fatal(err)
	}
	repaired, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	repairedRuntime := repaired.V2TaskRuntimes[runtime.TaskID]
	slot, _ := FindArtifactSlotRevision(repaired, h.def.Revision, "slot")
	run := findV2WorkflowRun(repaired, h.run)
	if repaired.State != WorkFailed || run == nil || run.State != RunFailed {
		t.Fatalf("aggregate state = work:%s run:%+v, want failed", repaired.State, run)
	}
	if repairedRuntime == nil || repairedRuntime.State != TaskInvalidated ||
		!strings.Contains(repairedRuntime.Error, missingV2ArtifactReason) {
		t.Fatalf("runtime = %+v, want invalidated missing-output repair", repairedRuntime)
	}
	if slot == nil || slot.State != SlotFailed || slot.Error == nil || !slot.Error.Retryable {
		t.Fatalf("slot = %+v, want retryable failed", slot)
	}
	repairedRevision := state.Revision
	if err := h.svc.RecoverV2Scheduling(context.Background(), h.work); err != nil {
		t.Fatal(err)
	}
	_, repeatedState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if repeatedState.Revision != repairedRevision {
		t.Fatalf("repeated repair revision = %d, want unchanged %d", repeatedState.Revision, repairedRevision)
	}

	result, err := h.svc.RetryWorkNode(context.Background(), RetryWorkNodeRequest{
		WorkID:           h.work,
		RunID:            h.run,
		TaskID:           runtime.TaskID,
		ExpectedRevision: state.Revision,
		RequestID:        "retry-missing-artifact-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Result == nil || result.Result.State != RunCompleted {
		t.Fatalf("retry result = %+v, want completed", result)
	}
	completed, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	completedRun := findV2WorkflowRun(completed, h.run)
	completedSlot, _ := FindArtifactSlotRevision(completed, h.def.Revision, "slot")
	if completed.State != WorkCompleted || completedRun == nil || completedRun.State != RunCompleted {
		t.Fatalf("completed aggregate = work:%s run:%+v", completed.State, completedRun)
	}
	if !v2ArtifactDelivered(completedSlot) {
		t.Fatalf("completed slot = %+v, want delivered", completedSlot)
	}
}

func TestRetryReservedArtifactRecoversLiveStrandedRun(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "Producer", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	completedCoordinatorRuntime(t, h)
	h.svc.SetTaskExecutor(&coordinatorExecutor{})
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.svc.RetryArtifactSlot(context.Background(), RetryArtifactSlotRequest{
		WorkID:             h.work,
		SlotID:             "slot",
		DefinitionRevision: h.def.Revision,
		ExpectedRevision:   state.Revision,
		RequestID:          "retry-reserved-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Committed {
		t.Fatalf("retry result = %+v", result)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := FindArtifactSlotRevision(projection, h.def.Revision, "slot")
	run := findV2WorkflowRun(projection, h.run)
	if projection.State != WorkCompleted || run == nil || run.State != RunCompleted || !v2ArtifactDelivered(slot) {
		t.Fatalf("recovered projection = work:%s run:%+v slot:%+v", projection.State, run, slot)
	}
}

func requestCoordinatorInput(t *testing.T, h *coordinatorHarness, specID string) *WorkInput {
	t.Helper()
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := DeriveTaskID(h.run, "n1")
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewInputService(h.store, nil).RequestInput(context.Background(), RequestInputRequest{
		WorkID:           h.work,
		RunID:            h.run,
		TaskID:           taskID,
		BlockID:          "b1",
		InputID:          "input-" + t.Name(),
		SpecID:           specID,
		DefinitionRev:    h.def.Revision,
		ExpectedRevision: state.Revision,
		RequestID:        "request-input-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func submitCoordinatorInput(t *testing.T, h *coordinatorHarness, input *WorkInput, requestID string) (*SubmitInputResult, error) {
	t.Helper()
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	return h.svc.SubmitV2Input(context.Background(), SubmitInputRequest{
		WorkID:           h.work,
		InputID:          input.ID,
		Value:            json.RawMessage(`"changed"`),
		DefinitionRev:    h.def.Revision,
		InputRevision:    input.Revision,
		ExpectedRevision: state.Revision,
		RequestID:        requestID,
	})
}

func assertStaleAttempt(t *testing.T, projection *Work, taskID string) {
	t.Helper()
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime == nil || len(runtime.Attempts) == 0 {
		t.Fatalf("missing runtime attempt: %+v", runtime)
	}
	attempt := runtime.Attempts[0]
	if !attempt.StaleResult || attempt.ResultRef == "" {
		t.Fatalf("old completion must be retained as stale result: %+v", attempt)
	}
}

func TestV2Coordinator_InputCommitMakesRunningCompletionStale_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{
			ID: "n1", Title: "node", InputSpecIDs: []string{"topic"},
			ProducesSlotIDs: []string{"slot"},
		}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: false,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	))
	input := requestCoordinatorInput(t, h, "topic")
	executor := &coordinatorExecutor{
		blockRun: h.run,
		started:  make(chan TaskExecuteInput, 1),
		release:  make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(executor.release) }) }
	defer release()
	h.svc.SetTaskExecutor(executor)
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"})
		done <- err
	}()
	started := <-executor.started
	result, err := submitCoordinatorInput(t, h, input, "submit-"+t.Name())
	if err != nil || result == nil || result.Error != "" {
		t.Fatalf("submit failed: result=%+v err=%v", result, err)
	}
	afterSubmit, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	slots := append([]ArtifactSlot(nil), afterSubmit.V2ArtifactSlots...)
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleAttempt(t, projection, started.TaskID)
	if !reflect.DeepEqual(projection.V2ArtifactSlots, slots) {
		t.Fatalf("stale completion changed artifact slots: before=%+v after=%+v", slots, projection.V2ArtifactSlots)
	}
	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetTaskExecutor(executor)
	if err := restarted.RecoverV2Scheduling(context.Background(), h.work); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleAttempt(t, recovered, started.TaskID)
	if runtime := recovered.V2TaskRuntimes[started.TaskID]; runtime.State != TaskCompleted ||
		len(runtime.Attempts) != 2 {
		t.Fatalf("restart did not rerun stale task: %+v", runtime)
	}
	if slot, _ := FindArtifactSlotRevision(recovered, h.def.Revision, "slot"); !v2ArtifactDelivered(slot) {
		t.Fatalf("stale recovery did not materialize artifact slot: %+v", slot)
	}
}

func TestV2Coordinator_ApprovalCommitMakesRunningCompletionStale_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{
			ID: "n1", Title: "node", InputSpecIDs: []string{"approval"},
			ProducesSlotIDs: []string{"slot"},
		}},
		[]InputSpec{{
			ID: "approval", Label: "Approval", Kind: InputApproval, Required: false,
		}},
	))
	input := requestCoordinatorInput(t, h, "approval")
	executor := &coordinatorExecutor{
		blockRun: h.run,
		started:  make(chan TaskExecuteInput, 1),
		release:  make(chan struct{}),
	}
	h.svc.SetTaskExecutor(executor)
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"})
		done <- err
	}()
	started := <-executor.started
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.svc.SubmitV2Input(context.Background(), SubmitInputRequest{
		WorkID: h.work, InputID: input.ID, Value: json.RawMessage(`"approved"`),
		DefinitionRev: h.def.Revision, InputRevision: input.Revision,
		ExpectedRevision: state.Revision, RequestID: "approve-" + t.Name(),
	})
	if err != nil || result == nil || result.Error != "" {
		t.Fatalf("approval failed: result=%+v err=%v", result, err)
	}
	afterApproval, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	slots := append([]ArtifactSlot(nil), afterApproval.V2ArtifactSlots...)
	close(executor.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleAttempt(t, projection, started.TaskID)
	if !reflect.DeepEqual(projection.V2ArtifactSlots, slots) {
		t.Fatalf("stale completion changed artifact slots: before=%+v after=%+v", slots, projection.V2ArtifactSlots)
	}
	restarted := NewService(h.store, nil, nil)
	restarted.SetTaskExecutor(executor)
	if err := restarted.RecoverV2Scheduling(context.Background(), h.work); err != nil {
		t.Fatal(err)
	}
	if executor.callCount() != 2 {
		t.Fatalf("approval stale recovery calls=%d, want 2", executor.callCount())
	}
}

func TestV2Coordinator_DefinitionCommitMakesRunningCompletionStale_FileStore(t *testing.T) {
	first := coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "old", ProducesSlotIDs: []string{"slot"}}},
		nil,
	)
	h := newCoordinatorHarness(t, first)
	executor := &coordinatorExecutor{
		blockRun: h.run,
		started:  make(chan TaskExecuteInput, 1),
		release:  make(chan struct{}),
	}
	h.svc.SetTaskExecutor(executor)
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"})
		done <- err
	}()
	started := <-executor.started

	next := coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "new", ProducesSlotIDs: []string{"slot"}}},
		nil,
	)
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := h.svc.CreateCandidateRevision(
		context.Background(), h.work, next, "candidate-next-"+t.Name(), state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "apply-next-" + t.Name(),
	}); err != nil {
		t.Fatal(err)
	}
	afterApply, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	slots := append([]ArtifactSlot(nil), afterApply.V2ArtifactSlots...)
	close(executor.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleAttempt(t, projection, started.TaskID)
	if !reflect.DeepEqual(projection.V2ArtifactSlots, slots) {
		t.Fatalf("stale completion changed artifact slots: before=%+v after=%+v", slots, projection.V2ArtifactSlots)
	}
	restarted := NewService(h.store, nil, nil)
	restarted.SetTaskExecutor(executor)
	if err := restarted.RecoverV2Scheduling(context.Background(), h.work); err != nil {
		t.Fatal(err)
	}
	if executor.callCount() != 2 {
		t.Fatalf("recovery reran superseded definition run: calls=%d", executor.callCount())
	}
}

func TestV2Coordinator_PatchCommitMakesRunningCompletionStale_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{
			ID: "n1", Title: "node", BlockIDs: []string{"b1"},
			ProducesSlotIDs: []string{"slot"},
		}},
		nil,
	))
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	blockPayload, err := json.Marshal(BlockInstance{
		ID: "b1", Kind: "markdown", SchemaVersion: 1, Revision: 1,
		Title: "old", Status: BlockReady, Data: json.RawMessage(`{"content":"old"}`),
		Source:   BlockSource{Provider: "test", Mode: "snapshot"},
		Fallback: BlockFallback{Summary: "old"}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	blockEvent := newServiceEvent(h.work, "block-"+t.Name(), EventBlockUpserted, blockPayload, now)
	blockEvent.BaseRevision, blockEvent.Revision = state.Revision, state.Revision+1
	if _, err := h.store.CommitEvent(h.work, blockEvent); err != nil {
		t.Fatal(err)
	}
	executor := &coordinatorExecutor{
		blockRun: h.run,
		started:  make(chan TaskExecuteInput, 1),
		release:  make(chan struct{}),
	}
	h.svc.SetTaskExecutor(executor)
	h.svc.SetV2PatchPlanner(coordinatorPatchPlanner{})
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"})
		done <- err
	}()
	started := <-executor.started

	projection, _, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := h.svc.PreviewV2WorkPatch(context.Background(), PreviewWorkPatchInput{
		WorkID: h.work, RunID: h.run, TaskID: "n1", BlockID: "b1",
		SessionID: "discussion", Instruction: "rename-block",
		DefinitionRevision: projection.V2CurrentRevision, BlockRevision: projection.Blocks[0].Revision,
		Scope: PatchBlock, RequestID: "preview-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ApplyV2WorkPatch(context.Background(), ApplyWorkPatchInput{
		WorkID: h.work, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: PatchBlock, ExpectedRevision: state.Revision, RequestID: "patch-" + t.Name(),
	}); err != nil {
		t.Fatal(err)
	}
	afterPatch, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	slots := append([]ArtifactSlot(nil), afterPatch.V2ArtifactSlots...)
	close(executor.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	projection, err = h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleAttempt(t, projection, started.TaskID)
	if !reflect.DeepEqual(projection.V2ArtifactSlots, slots) {
		t.Fatalf("stale completion changed artifact slots: before=%+v after=%+v", slots, projection.V2ArtifactSlots)
	}
	restarted := NewService(h.store, nil, nil)
	restarted.SetTaskExecutor(executor)
	if err := restarted.RecoverV2Scheduling(context.Background(), h.work); err != nil {
		t.Fatal(err)
	}
	recovered, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleAttempt(t, recovered, started.TaskID)
	if runtime := recovered.V2TaskRuntimes[started.TaskID]; runtime.State != TaskCompleted ||
		len(runtime.Attempts) != 2 {
		t.Fatalf("patch stale recovery did not rerun affected task: %+v", runtime)
	}
	if slot, _ := FindArtifactSlotRevision(recovered, h.def.Revision, "slot"); !v2ArtifactDelivered(slot) {
		t.Fatalf("patch stale recovery did not materialize artifact slot: %+v", slot)
	}
}

func TestV2Coordinator_SubmitInputAutoSchedulesOnlyAffected_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "target", InputSpecIDs: []string{"topic"}},
			{ID: "n2", Title: "unrelated"},
		},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	))
	input := requestCoordinatorInput(t, h, "topic")
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	result, err := submitCoordinatorInput(t, h, input, "submit-"+t.Name())
	if err != nil || result == nil || result.Error != "" {
		t.Fatalf("submit failed: result=%+v err=%v", result, err)
	}
	taskID, _ := DeriveTaskID(h.run, "n1")
	unrelatedID, _ := DeriveTaskID(h.run, "n2")
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if runtime := projection.V2TaskRuntimes[taskID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("affected task was not resumed: %+v", runtime)
	}
	if runtime := projection.V2TaskRuntimes[unrelatedID]; runtime != nil {
		t.Fatalf("unrelated task must not be scheduled: %+v", runtime)
	}
	if executor.callCount() != 1 {
		t.Fatalf("executor calls=%d, want 1", executor.callCount())
	}
}

func TestV2Coordinator_SubmitInputPublishesRunningBeforeExecutionCompletes_FileStore(t *testing.T) {
	sink := &serviceSink{next: make(chan WorkViewEvent, 256)}
	h := newCoordinatorHarnessWithSink(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "target", InputSpecIDs: []string{"topic"}}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	), sink)
	h.svc.SetV2TransportEnabled(true)

	input := requestCoordinatorInput(t, h, "topic")
	executor := &coordinatorExecutor{
		blockRun: h.run,
		started:  make(chan TaskExecuteInput, 1),
		release:  make(chan struct{}),
	}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	request := SubmitInputRequest{
		WorkID: h.work, InputID: input.ID, Value: json.RawMessage(`"changed"`),
		DefinitionRev: h.def.Revision, InputRevision: input.Revision,
		ExpectedRevision: state.Revision, RequestID: "submit-running-" + t.Name(),
	}

	resultCh := make(chan error, 1)
	go func() {
		_, err := h.svc.SubmitV2Input(context.Background(), request)
		resultCh <- err
	}()

	var started TaskExecuteInput
	select {
	case started = <-executor.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for task execution to start")
	}
	select {
	case err := <-resultCh:
		t.Fatalf("SubmitV2Input returned before the running task completed: %v", err)
	default:
	}

	event := waitForSinkEvent(t, sink, func(event WorkViewEvent) bool {
		if event.Type != ViewSnapshot || event.SchemaVersion != WorkViewSchemaVersionV2 {
			return false
		}
		var view WorkView
		if json.Unmarshal(event.Payload, &view) != nil {
			return false
		}
		for _, task := range view.Tasks {
			if task.ID == started.TaskID && task.State == TaskRunning {
				return true
			}
		}
		return false
	})
	if event.Revision <= 0 {
		t.Fatalf("running snapshot revision = %d", event.Revision)
	}

	close(executor.release)
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SubmitV2Input completion")
	}
}

func TestWorkStoreV2Authority_ObserverFailurePreservesCommittedRuntime(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "observer-node", Title: "observer"}},
		nil,
	))
	now := time.Now().UTC()
	runtime := V2NewTaskRuntime(h.work, h.run, "observer-node", h.def.Revision, "read", now)
	_, event, err := newRuntimeCreatedEvent(runtime, now)
	if err != nil {
		t.Fatal(err)
	}

	var observedBase int64
	authority := &workStoreV2Authority{
		store: h.store, workID: h.work,
		observer: func(_ string, baseRevision int64, _ string) error {
			observedBase = baseRevision
			return errors.New("injected transport failure")
		},
	}
	revision, err := authority.CommitV2Event(event)
	if err != nil {
		t.Fatalf("durable commit must survive observer failure: %v", err)
	}
	if observedBase != revision-1 {
		t.Fatalf("observer base revision = %d, want %d", observedBase, revision-1)
	}
	if observerErr := authority.ObserverError(); observerErr == nil ||
		!strings.Contains(observerErr.Error(), "injected transport failure") {
		t.Fatalf("observer failure must remain explicit: %v", observerErr)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if committed := projection.V2TaskRuntimes[runtime.TaskID]; committed == nil {
		t.Fatal("observer failure rolled back the committed runtime")
	}
}

func TestV2Coordinator_CommittedWakeFailureRetriesIdempotently_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "target", InputSpecIDs: []string{"topic"}}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	))
	input := requestCoordinatorInput(t, h, "topic")
	h.svc.SetTaskExecutor(&coordinatorExecutor{})
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	h.svc.SetTaskExecutor(nil)
	requestID := "submit-" + t.Name()
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	request := SubmitInputRequest{
		WorkID:           h.work,
		InputID:          input.ID,
		Value:            json.RawMessage(`"changed"`),
		DefinitionRev:    h.def.Revision,
		InputRevision:    input.Revision,
		ExpectedRevision: state.Revision,
		RequestID:        requestID,
	}
	result, err := h.svc.SubmitV2Input(context.Background(), request)
	var recovery *ErrWorkCommittedRecovery
	if result == nil || !errors.As(err, &recovery) || !recovery.Committed || !recovery.Recoverable {
		t.Fatalf("expected committed recoverable wake failure: result=%+v err=%v", result, err)
	}
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	replayed, err := h.svc.SubmitV2Input(context.Background(), request)
	if err != nil || replayed == nil || !replayed.Duplicate {
		t.Fatalf("idempotent retry failed: result=%+v err=%v", replayed, err)
	}
	taskID, _ := DeriveTaskID(h.run, "n1")
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if runtime := projection.V2TaskRuntimes[taskID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("retry did not recover scheduling: %+v", runtime)
	}
}

func TestV2Coordinator_RestartRecoversCommittedWake_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "target", InputSpecIDs: []string{"topic"}}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	))
	input := requestCoordinatorInput(t, h, "topic")
	h.svc.SetTaskExecutor(&coordinatorExecutor{})
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	h.svc.SetTaskExecutor(nil)
	if _, err := submitCoordinatorInput(t, h, input, "submit-"+t.Name()); err == nil {
		t.Fatal("expected committed wake failure")
	}

	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	executor := &coordinatorExecutor{}
	restarted.SetTaskExecutor(executor)
	if err := restarted.RecoverV2Scheduling(context.Background(), h.work); err != nil {
		t.Fatal(err)
	}
	taskID, _ := DeriveTaskID(h.run, "n1")
	projection, err := reopened.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if runtime := projection.V2TaskRuntimes[taskID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("restart did not recover scheduling: %+v", runtime)
	}
}

func TestV2Coordinator_AutomaticRecoveryAttemptsAreBounded_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "retryable", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	executor := &coordinatorExecutor{fail: true}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	for executor.callCount() < maxV2AutomaticRecoveryAttempts {
		_ = h.svc.RecoverV2Scheduling(context.Background(), h.work)
	}
	before := executor.callCount()
	err := h.svc.RecoverV2Scheduling(context.Background(), h.work)
	if err == nil || !strings.Contains(err.Error(), "automatic recovery paused") {
		t.Fatalf("recovery limit error = %v", err)
	}
	if after := executor.callCount(); after != before {
		t.Fatalf("automatic recovery exceeded limit: before=%d after=%d", before, after)
	}
	taskID, err := DeriveTaskID(h.run, "n1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime == nil || runtime.State != TaskFailedRetryable ||
		len(runtime.Attempts) != maxV2AutomaticRecoveryAttempts {
		t.Fatalf("bounded runtime = %+v", runtime)
	}
}

func TestV2Controller_SetInputCornerstoneUsesAuthoritativeInputPath_FileStore(t *testing.T) {
	inputs, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	submitted, err := inputs.SubmitInput(context.Background(), SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"pinned"`),
		DefinitionRev: defRev, InputRevision: inputRev, ExpectedRevision: workRev,
		RequestID: "submit-controller-pin",
	})
	if err != nil || submitted == nil || submitted.Error != "" {
		t.Fatalf("submit: result=%+v err=%v", submitted, err)
	}

	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	request := SetInputCornerstoneRequest{
		WorkID: workID, InputID: inputID, Pin: true,
		DefinitionRevision: defRev, InputRevision: inputRev,
		ExpectedRevision: workRev, RequestID: "controller-pin",
	}
	pinned, err := svc.SetInputCornerstone(context.Background(), request)
	if err != nil || pinned == nil || pinned.Error != "" || !pinned.Pinned || pinned.CornerstoneID == "" {
		t.Fatalf("pin: result=%+v err=%v", pinned, err)
	}
	projection, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	index := findInputIndex(projection, inputID)
	if index < 0 || projection.V2Inputs[index].CornerstoneID != pinned.CornerstoneID {
		t.Fatalf("WorkInput linkage was not persisted: input=%+v result=%+v", projection.V2Inputs, pinned)
	}

	reopened, err := NewFileWorkStore(store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	duplicate, err := restarted.SetInputCornerstone(context.Background(), request)
	if err != nil || duplicate == nil || !duplicate.Duplicate ||
		duplicate.CornerstoneID != pinned.CornerstoneID {
		t.Fatalf("restart replay: result=%+v err=%v", duplicate, err)
	}

	conflict := request
	conflict.Pin = false
	if _, err := restarted.SetInputCornerstone(context.Background(), conflict); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("same requestID with different intent must conflict, got %v", err)
	}
}

func TestV2Controller_PinPartialThenOppositeIntentConflictsWithoutPollution_FileStore(t *testing.T) {
	inputs, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	submitted, err := inputs.SubmitInput(context.Background(), SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"partial"`),
		DefinitionRev: defRev, InputRevision: inputRev, ExpectedRevision: workRev,
		RequestID: "partial-controller-submit",
	})
	if err != nil || submitted == nil || submitted.Error != "" {
		t.Fatalf("submit: result=%+v err=%v", submitted, err)
	}
	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	failingStore := &failInputLinkStore{WorkStore: store, fail: true}
	manager := NewCornerstoneManager(store, store, RealClock{})
	controllerSvc := NewService(failingStore, nil, nil)
	controllerSvc.SetCornerstoneManager(manager)
	controllerSvc.SetDefinitionRevisionStore(store)
	request := SetInputCornerstoneRequest{
		WorkID: workID, InputID: inputID, Pin: true,
		DefinitionRevision: defRev, InputRevision: inputRev,
		ExpectedRevision: workRev, RequestID: "partial-controller-pin",
	}
	partial, err := controllerSvc.SetInputCornerstone(context.Background(), request)
	if err != nil || partial == nil || partial.Error == "" ||
		!partial.Pinned || partial.CornerstoneID == "" {
		t.Fatalf("partial pin: result=%+v err=%v", partial, err)
	}
	before, stateBefore, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := json.Marshal(before)
	opposite := request
	opposite.Pin = false
	if _, err := controllerSvc.SetInputCornerstone(context.Background(), opposite); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("opposite intent after partial commit must conflict, got %v", err)
	}
	after, stateAfter, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, _ := json.Marshal(after)
	if stateAfter.Revision != stateBefore.Revision || !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatalf("opposite intent polluted partial state: beforeRev=%d afterRev=%d", stateBefore.Revision, stateAfter.Revision)
	}
}

func failedCoordinatorRuntime(t *testing.T, h *coordinatorHarness) *V2TaskRuntime {
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
		ID: "failed-attempt", Index: 0, State: TaskRunning, StartedAt: now,
		DefinitionRev: h.def.Revision, InputDigest: "inputs:none",
		DependencyDigest: "deps:none", ExecutionToken: "failed-token",
	}
	if err := emitRuntimeUpdated(emit, runtime, TaskRunning, attempt, now); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Second)
	if err := updateRuntime(emit, runtime, TaskFailedRetryable, nil, finished, func(next *V2TaskRuntime) {
		next.Attempts[len(next.Attempts)-1].State = TaskFailedRetryable
		next.Attempts[len(next.Attempts)-1].FinishedAt = &finished
		next.Error = "injected retryable failure"
	}); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestV2Controller_RetryNodeDurableIdempotentAndConflict_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "retry target", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := failedCoordinatorRuntime(t, h)
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	request := RetryWorkNodeRequest{
		WorkID: h.work, RunID: h.run, TaskID: runtime.TaskID,
		ExpectedRevision: runtime.Revision, RequestID: "retry-node",
	}
	task, err := h.svc.RetryWorkNode(context.Background(), request)
	if err != nil || task == nil || task.Result == nil || task.Result.State != RunCompleted ||
		task.Revision == 0 || task.Duplicate || !task.Committed {
		t.Fatalf("retry: task=%+v err=%v", task, err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("executor calls=%d, want 1", executor.callCount())
	}

	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetTaskExecutor(executor)
	duplicate, err := restarted.RetryWorkNode(context.Background(), request)
	if err != nil || duplicate == nil || duplicate.Result == nil ||
		duplicate.Result.State != RunCompleted || !duplicate.Duplicate {
		t.Fatalf("restart replay: task=%+v err=%v", duplicate, err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("idempotent restart replay executed twice: calls=%d", executor.callCount())
	}

	conflict := request
	conflict.ExpectedRevision++
	if _, err := restarted.RetryWorkNode(context.Background(), conflict); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("same requestID with different revision must conflict, got %v", err)
	}

	projection, err := reopened.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	before := projection.V2TaskRuntimes[runtime.TaskID].Revision
	stale := RetryWorkNodeRequest{
		WorkID: h.work, RunID: h.run, TaskID: runtime.TaskID,
		ExpectedRevision: before - 1, RequestID: "retry-stale",
	}
	if _, err := restarted.RetryWorkNode(context.Background(), stale); err == nil {
		t.Fatal("stale retry must fail")
	}
	after, err := reopened.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if after.V2TaskRuntimes[runtime.TaskID].Revision != before {
		t.Fatalf("stale retry polluted runtime revision: before=%d after=%d", before, after.V2TaskRuntimes[runtime.TaskID].Revision)
	}
}

func TestV2Controller_RetryNodeUsesWorkViewRevisionAfterUnrelatedEvent_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "retry target", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := failedCoordinatorRuntime(t, h)
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)

	// A candidate definition advances the aggregate Work revision without
	// mutating this failed task. Desktop only receives that aggregate revision.
	_, beforeCandidate, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate := CopyOnWriteRevision(h.def)
	candidate.Goal = h.def.Goal + " adjusted"
	if _, err := h.svc.CreateCandidateRevision(
		context.Background(),
		h.work,
		candidate,
		"unrelated-candidate",
		beforeCandidate.Revision,
	); err != nil {
		t.Fatal(err)
	}
	projection, afterCandidate, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if projection.V2TaskRuntimes[runtime.TaskID].Revision != runtime.Revision {
		t.Fatal("unrelated definition event changed task runtime revision")
	}
	if afterCandidate.Revision == runtime.Revision {
		t.Fatal("counterexample setup did not advance aggregate revision")
	}

	result, err := h.svc.RetryWorkNode(context.Background(), RetryWorkNodeRequest{
		WorkID: h.work, RunID: h.run, TaskID: runtime.TaskID,
		ExpectedRevision: afterCandidate.Revision, RequestID: "retry-after-unrelated-event",
	})
	if err != nil || result == nil || result.Result == nil || !result.Committed {
		t.Fatalf("retry with authoritative WorkView revision: result=%+v err=%v", result, err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("executor calls=%d, want 1", executor.callCount())
	}
}

func TestV2Controller_RetryNodePostCommitFailureRecoversOnReplay_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "retry target", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	runtime := failedCoordinatorRuntime(t, h)
	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	failingDefinitions := &failLoadDefinitionStore{
		DefinitionRevisionStore: h.store,
		fail:                    true,
	}
	h.svc.SetDefinitionRevisionStore(failingDefinitions)
	request := RetryWorkNodeRequest{
		WorkID: h.work, RunID: h.run, TaskID: runtime.TaskID,
		ExpectedRevision: runtime.Revision, RequestID: "retry-after-commit",
	}
	task, err := h.svc.RetryWorkNode(context.Background(), request)
	var recovery *ErrWorkCommittedRecovery
	if task == nil || task.Result == nil || task.Result.State != RunState("ready") ||
		task.Error == nil || task.Error.Code != "committed_recovery" ||
		!task.Committed || !task.Recoverable || !errors.As(err, &recovery) ||
		!recovery.Committed || !recovery.Recoverable {
		t.Fatalf("expected committed recovery: task=%+v err=%v", task, err)
	}
	if executor.callCount() != 0 {
		t.Fatalf("executor ran without definition: calls=%d", executor.callCount())
	}

	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetTaskExecutor(executor)
	recovered, err := restarted.RetryWorkNode(context.Background(), request)
	if err != nil || recovered == nil || recovered.Result == nil ||
		recovered.Result.State != RunCompleted || !recovered.Duplicate {
		t.Fatalf("replay did not resume committed retry: task=%+v err=%v", recovered, err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("recovered retry calls=%d, want 1", executor.callCount())
	}
}

func TestHasSubmittedApproval_OnlyApprovedDecisionWakes(t *testing.T) {
	specs := []InputSpec{{ID: "approval", Kind: InputApproval, Required: true}}
	base := WorkInput{
		ID: "i", WorkID: "w", RunID: "r", TaskID: "t", SpecID: "approval",
		State: InputSubmitted,
	}
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "approved", value: `"approved"`, want: true},
		{name: "approved normalized", value: `" APPROVED "`, want: true},
		{name: "rejected", value: `"rejected"`, want: false},
		{name: "declined", value: `"declined"`, want: false},
		{name: "empty", value: `""`, want: false},
		{name: "unknown", value: `"later"`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			input.Value = json.RawMessage(tc.value)
			if got := hasSubmittedApproval([]WorkInput{input}, specs, "t"); got != tc.want {
				t.Fatalf("hasSubmittedApproval=%v, want %v", got, tc.want)
			}
		})
	}
}

// ── Dual-instance concurrency: two FileWorkStores on same dir ───────────────

func TestV2Controller_SetInputCornerstoneDualInstance_FileStore(t *testing.T) {
	inputs, svc, store, dir := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	submitted, err := inputs.SubmitInput(context.Background(), SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"dual"`),
		DefinitionRev: defRev, InputRevision: inputRev, ExpectedRevision: workRev,
		RequestID: "dual-submit",
	})
	if err != nil || submitted == nil || submitted.Error != "" {
		t.Fatalf("submit: result=%+v err=%v", submitted, err)
	}
	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)

	// Open a second FileWorkStore instance on the same directory.
	store2, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := NewService(store2, nil, nil)

	request := SetInputCornerstoneRequest{
		WorkID: workID, InputID: inputID, Pin: true,
		DefinitionRevision: defRev, InputRevision: inputRev,
		ExpectedRevision: workRev, RequestID: "dual-pin",
	}

	// Concurrent submission: both instances submit the same request from a barrier.
	var wg sync.WaitGroup
	var res1, res2 *CornerstonePinResult
	var err1, err2 error
	barrier := make(chan struct{})

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-barrier
		res1, err1 = svc.SetInputCornerstone(context.Background(), request)
	}()
	go func() {
		defer wg.Done()
		<-barrier
		res2, err2 = svc2.SetInputCornerstone(context.Background(), request)
	}()

	// Release both goroutines simultaneously.
	close(barrier)
	wg.Wait()

	// At least one must have committed; the other may get a CAS conflict.
	// The conflict loser must retry the same request to get a duplicate replay.
	committed := false
	duplicate := false
	for _, r := range []struct {
		res   **CornerstonePinResult
		err   *error
		label string
		svc   *Service
	}{{&res1, &err1, "instance-1", svc}, {&res2, &err2, "instance-2", svc2}} {
		if *r.err != nil {
			t.Fatalf("%s: SetInputCornerstone: %v", r.label, *r.err)
		}
		// Internal cornerstone CAS conflict: result.Error is set but err is nil.
		// Retry same request → winner's receipt makes this a duplicate replay.
		if (*r.res).Error != "" {
			retry, retryErr := r.svc.SetInputCornerstone(context.Background(), request)
			if retryErr != nil {
				t.Fatalf("%s: CAS-conflict retry failed: %v", r.label, retryErr)
			}
			if retry == nil || (!retry.Duplicate && !retry.Pinned) {
				t.Fatalf("%s: retry did not produce duplicate or committed: %+v", r.label, retry)
			}
			*r.res = retry
			*r.err = nil
		}
		if (*r.res).Duplicate {
			duplicate = true
		} else {
			if !(*r.res).Pinned || (*r.res).CornerstoneID == "" {
				t.Fatalf("%s: committed without cornerstone: %+v", r.label, *r.res)
			}
			committed = true
		}
	}
	if !committed {
		t.Fatal("neither instance committed the cornerstone")
	}
	if !duplicate {
		t.Log("both instances committed directly — store CAS serialized; projection must have single side-effect")
	}

	// Both instances must see the same authoritative projection.
	p1, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := store2.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	idx1 := findInputIndex(p1, inputID)
	idx2 := findInputIndex(p2, inputID)
	if idx1 < 0 || idx2 < 0 {
		t.Fatalf("input missing: idx1=%d idx2=%d", idx1, idx2)
	}
	if p1.V2Inputs[idx1].CornerstoneID != p2.V2Inputs[idx2].CornerstoneID {
		t.Fatalf("instances disagree: c1=%s c2=%s", p1.V2Inputs[idx1].CornerstoneID, p2.V2Inputs[idx2].CornerstoneID)
	}
	// Count EventInputCornerstoneChanged events for this request in the event log.
	// Exactly 1 must exist (single side effect).
	countCornerstoneEvents := func(s *FileWorkStore) int {
		workPath := filepath.Join(s.WorkDir(), workID)
		replay, err := ReplayWorkEventLog(workPath)
		if err != nil {
			t.Fatalf("ReplayWorkEventLog: %v", err)
		}
		count := 0
		for _, ev := range replay.Events {
			if ev.Type == EventInputCornerstoneChanged && strings.Contains(ev.RequestID, "dual-pin") {
				count++
			}
		}
		return count
	}
	beforeCount := countCornerstoneEvents(store)
	if beforeCount != 1 {
		t.Fatalf("EventInputCornerstoneChanged count before restart = %d, want 1", beforeCount)
	}

	// Restart and verify consistency.
	store3, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	afterCount := countCornerstoneEvents(store3)
	if afterCount != 1 {
		t.Fatalf("EventInputCornerstoneChanged count after restart = %d, want 1", afterCount)
	}
	p3, err := store3.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	idx3 := findInputIndex(p3, inputID)
	if idx3 < 0 || p3.V2Inputs[idx3].CornerstoneID != p1.V2Inputs[idx1].CornerstoneID {
		t.Fatalf("after restart: idx=%d cid=%s want=%s", idx3, p3.V2Inputs[idx3].CornerstoneID, p1.V2Inputs[idx1].CornerstoneID)
	}
}

// failingArtifactExecutor wraps a TaskExecutor and fails TaskArtifacts with a
// fixed error. It tests the path where artifact materialisation/reporting fails
// and the producer task becomes failed_retryable.
type failingArtifactExecutor struct {
	TaskExecutor
	err error
}

func (e *failingArtifactExecutor) TaskArtifacts(context.Context, TaskExecuteInput, *Attempt) ([]TaskArtifactOutput, error) {
	return nil, e.err
}

func TestV2Coordinator_FailedArtifactMaterializationFailsSlot(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "Producer", ProducesSlotIDs: []string{"slot"}}},
		nil,
	))
	exec := &failingArtifactExecutor{
		TaskExecutor: &coordinatorExecutor{},
		err:          errors.New("artifact materialization failed: disk full"),
	}
	h.svc.SetTaskExecutor(exec)

	_, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"})
	if err != nil {
		t.Fatalf("ScheduleV2Run: %v", err)
	}

	projection, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := DeriveTaskID(h.run, "n1")
	runtime := projection.V2TaskRuntimes[taskID]
	slot, _ := FindArtifactSlotRevision(projection, h.def.Revision, "slot")

	if runtime == nil || runtime.State != TaskFailedRetryable {
		t.Fatalf("task state = %v, want failed_retryable", runtime)
	}
	if runtime.Error == "" {
		t.Fatal("task error must not be empty after artifact failure")
	}
	if !strings.Contains(runtime.Error, "disk full") {
		t.Fatalf("task error = %q, should contain the artifact failure reason", runtime.Error)
	}
	if slot == nil {
		t.Fatal("slot must exist")
	}
	if slot.State == SlotGenerating {
		t.Fatalf("slot must not stay generating after producer fails; state = %s", slot.State)
	}
	if slot.State != SlotFailed {
		t.Fatalf("slot state = %s, want failed", slot.State)
	}
	if slot.Error == nil || slot.Error.Code == "" {
		t.Fatalf("slot error = %+v, want explicit retryable failure", slot.Error)
	}
	if !slot.Error.Retryable {
		t.Fatalf("slot error.retryable = false, want true")
	}
	if strings.Contains(slot.Error.Message, "snapshot") ||
		strings.Contains(slot.Error.Message, "revision") {
		t.Fatalf("slot error message %q exposes internal protocol text", slot.Error.Message)
	}
	view := promoteV2View(&WorkView{
		Work:          projection,
		SchemaVersion: SchemaVersionV2,
		ArtifactSlots: projection.V2ArtifactSlots,
		Revision:      state.Revision,
	}, h.def)
	var taskView *TaskV2View
	for i := range view.Tasks {
		if view.Tasks[i].ID == taskID {
			taskView = &view.Tasks[i]
			break
		}
	}
	if taskView == nil || !taskView.Retryable {
		t.Fatalf("view task retryable = %v, want true after failed_retryable", taskView)
	}
	var slotInView *ArtifactSlot
	for i := range view.ArtifactSlots {
		if view.ArtifactSlots[i].ID == "slot" && view.ArtifactSlots[i].DefinitionRev == h.def.Revision {
			slotInView = &view.ArtifactSlots[i]
			break
		}
	}
	if slotInView == nil || slotInView.State != SlotFailed {
		t.Fatalf("view slot state = %v, want failed", slotInView)
	}

	if _, err := h.svc.v2.reconcileV2Artifacts(context.Background(), h.work, h.run, h.def); err != nil {
		t.Fatal(err)
	}
	_, repeatedState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if repeatedState.Revision != state.Revision {
		t.Fatalf("idempotent reconciliation advanced revision from %d to %d", state.Revision, repeatedState.Revision)
	}
	repeated, _ := h.store.LoadProjection(h.work)
	repeatedSlot, _ := FindArtifactSlotRevision(repeated, h.def.Revision, "slot")
	if repeatedSlot.State != SlotFailed {
		t.Fatalf("idempotent schedule changed slot state from failed to %s", repeatedSlot.State)
	}
	if repeatedSlot.Revision != slot.Revision {
		t.Fatalf("idempotent schedule advanced slot revision from %d to %d", slot.Revision, repeatedSlot.Revision)
	}
}
