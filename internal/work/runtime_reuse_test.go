package work

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type reuseExecutor struct {
	mu        sync.Mutex
	calls     []TaskExecuteInput
	failFirst bool
}

func (e *reuseExecutor) ExecuteTask(_ context.Context, input TaskExecuteInput) (*Attempt, error) {
	e.mu.Lock()
	e.calls = append(e.calls, input)
	index := len(e.calls)
	e.mu.Unlock()
	state := RunCompleted
	if e.failFirst && index == 1 {
		state = RunFailed
	}
	return &Attempt{
		State:      state,
		SessionRef: SessionRef{SessionPath: "sessions/" + input.AttemptID + ".jsonl"},
	}, nil
}

func (e *reuseExecutor) CancelTask(context.Context, TaskCancelInput) error { return nil }

func (e *reuseExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

type reusePatchPlanner struct{}

func (reusePatchPlanner) PlanPatch(_ context.Context, _ PatchPlanInput) (*PatchPlan, error) {
	return &PatchPlan{Operations: []PatchOp{{
		Op: "replace", Path: "nodes/n2/title", NewValue: json.RawMessage(`"changed"`),
	}}}, nil
}

type targetPatchPlanner struct {
	nodeID string
}

func (p targetPatchPlanner) PlanPatch(_ context.Context, input PatchPlanInput) (*PatchPlan, error) {
	if input.TargetNodeID != p.nodeID {
		return nil, errors.New("target node context mismatch")
	}
	return &PatchPlan{Operations: []PatchOp{{
		Op:       "replace",
		Path:     "nodes/" + p.nodeID + "/description",
		NewValue: json.RawMessage(`"updated guidance"`),
	}}}, nil
}

type failReuseBatchStore struct {
	*FileWorkStore
	fail bool
}

func (s *failReuseBatchStore) CommitEvents(workID string, events []WorkEvent) ([]int64, error) {
	if s.fail {
		return nil, errors.New("injected kept runtime batch failure")
	}
	return s.FileWorkStore.CommitEvents(workID, events)
}

func requestTaskInput(
	t *testing.T,
	h *coordinatorHarness,
	runID, nodeID, inputID, specID string,
) *WorkInput {
	t.Helper()
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := DeriveTaskID(runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewInputService(h.store, nil).RequestInput(
		context.Background(),
		RequestInputRequest{
			WorkID: h.work, RunID: runID, TaskID: taskID, BlockID: "block-" + nodeID,
			InputID: inputID, SpecID: specID, DefinitionRev: h.def.Revision,
			ExpectedRevision: state.Revision, RequestID: "request-" + inputID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func submitTaskInput(t *testing.T, h *coordinatorHarness, input *WorkInput, value string) {
	t.Helper()
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewInputService(h.store, nil).SubmitInput(
		context.Background(),
		SubmitInputRequest{
			WorkID: h.work, InputID: input.ID, Value: json.RawMessage(value),
			DefinitionRev: h.def.Revision, InputRevision: input.Revision,
			ExpectedRevision: state.Revision, RequestID: "submit-" + input.ID,
		},
	)
	if err != nil || result == nil || result.Error != "" {
		t.Fatalf("submit failed: result=%+v err=%v", result, err)
	}
}

func createAndApplyNext(
	t *testing.T,
	h *coordinatorHarness,
	next *WorkDefinitionRevision,
	requestID string,
) *ApplyDefinitionResult {
	t.Helper()
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	next.WorkID = h.work
	candidate, err := h.svc.CreateCandidateRevision(
		context.Background(), h.work, next, requestID+"/candidate", state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: requestID + "/apply",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func persistCompletedRuntime(
	t *testing.T,
	h *coordinatorHarness,
	node NodeDef,
	receipt *AttemptReceipt,
) {
	t.Helper()
	now := time.Now().UTC()
	taskID, err := DeriveTaskID(h.run, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	inputDigest := ComputeInputDigest(nil, h.work, h.run, taskID, node.InputSpecIDs)
	dependencyDigest := ComputeDependencyDigest(nil, node.DependsOn)
	token := GenerateExecutionToken(taskID, h.def.Revision, inputDigest, dependencyDigest)
	class := DeriveV2SideEffectClass(node.ToolHints)
	requestID := h.run + "/run/v2/attempt/" + taskID + "/0"
	runtime := V2NewTaskRuntime(h.work, h.run, node.ID, h.def.Revision, class, now)
	runtime.State = TaskCompleted
	runtime.InputDigest = inputDigest
	runtime.DependencyDigest = dependencyDigest
	runtime.ExecutionToken = token
	runtime.Attempts = []V2Attempt{{
		ID:               V2RunAttemptID(taskID, 0),
		RequestID:        requestID,
		Index:            0,
		State:            TaskCompleted,
		StartedAt:        now.Add(-time.Second),
		FinishedAt:       &now,
		ResultRef:        "sessions/" + taskID + ".jsonl",
		Receipt:          receipt,
		SideEffectClass:  class,
		DefinitionRev:    h.def.Revision,
		InputDigest:      inputDigest,
		DependencyDigest: dependencyDigest,
		ExecutionToken:   token,
	}}
	_, event, err := newRuntimeCreatedEvent(runtime, now)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, err := h.store.CommitEvent(h.work, event); err != nil {
		t.Fatal(err)
	}
}

func TestV2InputScope_OtherTaskSameSpecCannotSatisfyRequired_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "target", InputSpecIDs: []string{"topic"}},
			{ID: "n2", Title: "other", InputSpecIDs: []string{"topic"}},
		},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	))
	other := requestTaskInput(t, h, h.run, "n2", "other-input", "topic")
	submitTaskInput(t, h, other, `"other"`)
	executor := &reuseExecutor{}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	targetID, _ := DeriveTaskID(h.run, "n1")
	otherID, _ := DeriveTaskID(h.run, "n2")
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	target := projection.V2TaskRuntimes[targetID]
	if target == nil || target.State != TaskWaitingInput || executor.callCount() != 0 {
		t.Fatalf("other task input released target: runtime=%+v calls=%d", target, executor.callCount())
	}
	targetDigest := ComputeInputDigest(projection.V2Inputs, h.work, h.run, targetID, []string{"topic"})
	otherDigest := ComputeInputDigest(projection.V2Inputs, h.work, h.run, otherID, []string{"topic"})
	if target.InputDigest != targetDigest || targetDigest == otherDigest {
		t.Fatalf("cross-task digest collision: target=%s other=%s runtime=%s", targetDigest, otherDigest, target.InputDigest)
	}
}

func TestV2KeptRuntime_ExternalReceiptMustBeConclusive_FileStore(t *testing.T) {
	node := NodeDef{ID: "n1", Title: "external", ToolHints: []string{"side_effect=external_write"}}
	cases := []struct {
		name    string
		receipt func(requestID string, now time.Time) *AttemptReceipt
		reused  bool
	}{
		{name: "valid", reused: true, receipt: func(requestID string, now time.Time) *AttemptReceipt {
			return &AttemptReceipt{
				RequestID: requestID, Outcome: "succeeded",
				SideEffectClass: "external_write", ConfirmedAt: now,
			}
		}},
		{name: "missing"},
		{name: "retry-outcome", receipt: func(requestID string, now time.Time) *AttemptReceipt {
			return &AttemptReceipt{
				RequestID: requestID, Outcome: "retry",
				SideEffectClass: "external_write", ConfirmedAt: now,
			}
		}},
		{name: "zero-confirmed-at", receipt: func(requestID string, _ time.Time) *AttemptReceipt {
			return &AttemptReceipt{
				RequestID: requestID, Outcome: "succeeded",
				SideEffectClass: "external_write",
			}
		}},
		{name: "side-effect-mismatch", receipt: func(requestID string, now time.Time) *AttemptReceipt {
			return &AttemptReceipt{
				RequestID: requestID, Outcome: "succeeded",
				SideEffectClass: "read", ConfirmedAt: now,
			}
		}},
		{name: "request-mismatch", receipt: func(_ string, now time.Time) *AttemptReceipt {
			return &AttemptReceipt{
				RequestID: "other-request", Outcome: "succeeded",
				SideEffectClass: "external_write", ConfirmedAt: now,
			}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newCoordinatorHarness(t, coordinatorDefinition([]NodeDef{node}, nil))
			taskID, _ := DeriveTaskID(h.run, node.ID)
			requestID := h.run + "/run/v2/attempt/" + taskID + "/0"
			var receipt *AttemptReceipt
			if testCase.receipt != nil {
				receipt = testCase.receipt(requestID, time.Now().UTC())
			}
			persistCompletedRuntime(t, h, node, receipt)

			applied := createAndApplyNext(
				t,
				h,
				coordinatorDefinition([]NodeDef{node}, nil),
				"receipt-next-"+testCase.name,
			)
			newTaskID, _ := DeriveTaskID(applied.Intent.RunID, node.ID)
			before, err := h.store.LoadProjection(h.work)
			if err != nil {
				t.Fatal(err)
			}
			if testCase.reused {
				runtime := before.V2TaskRuntimes[newTaskID]
				if runtime == nil || runtime.State != TaskCompleted {
					t.Fatalf("valid receipt was not reused: %+v", runtime)
				}
				return
			}
			if before.V2TaskRuntimes[newTaskID] != nil {
				t.Fatalf("malformed receipt %q was reused", testCase.name)
			}

			executor := &reuseExecutor{}
			h.svc.SetTaskExecutor(executor)
			if _, err := h.svc.ScheduleV2Run(
				context.Background(), h.work, applied.Intent.RunID, []string{node.ID},
			); err != nil {
				t.Fatal(err)
			}
			if executor.callCount() != 1 {
				t.Fatalf("malformed receipt %q should execute once, calls=%d", testCase.name, executor.callCount())
			}
		})
	}
}

func TestV2KeptRuntime_RejectsNonCurrentParentProjection_FileStore(t *testing.T) {
	node := NodeDef{ID: "n1", Title: "kept"}
	h := newCoordinatorHarness(t, coordinatorDefinition([]NodeDef{node}, nil))
	executor := &reuseExecutor{}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, nil); err != nil {
		t.Fatal(err)
	}
	if executor.callCount() != 1 {
		t.Fatalf("initial run calls=%d, want 1", executor.callCount())
	}
	_ = createAndApplyNext(
		t,
		h,
		coordinatorDefinition([]NodeDef{node}, nil),
		"advance-active-"+t.Name(),
	)
	current, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if current.V2CurrentRevision == h.def.Revision {
		t.Fatal("test setup did not advance active revision")
	}
	staleNext := CopyOnWriteRevision(h.def)
	staleNext.Revision = current.V2LatestRevision + 1
	staleNext.ParentRevision = h.def.Revision
	staleNext.Digest, _ = ComputeV2RevisionDigest(staleNext)
	impact := ClassifyRunImpact(h.def, staleNext)
	if _, projected := projectKeptContexts(
		current, h.def, staleNext, "manual-stale-parent-run", impact, time.Now().UTC(),
	); len(projected) != 0 {
		t.Fatalf("non-current parent projected %d runtimes", len(projected))
	}
}

func TestV2InputScope_OldRunSameSpecCannotSatisfyCurrentRun_FileStore(t *testing.T) {
	definition := coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "target", InputSpecIDs: []string{"topic"}}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	)
	h := newCoordinatorHarness(t, definition)
	old := requestTaskInput(t, h, h.run, "n1", "old-run-input", "topic")
	submitTaskInput(t, h, old, `"old"`)
	applied := createAndApplyNext(t, h, coordinatorDefinition(
		definition.Nodes,
		definition.InputSpecs,
	), "next-"+t.Name())

	executor := &reuseExecutor{}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(
		context.Background(), h.work, applied.Intent.RunID, []string{"n1"},
	); err != nil {
		t.Fatal(err)
	}
	taskID, _ := DeriveTaskID(applied.Intent.RunID, "n1")
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime == nil || runtime.State != TaskWaitingInput || executor.callCount() != 0 {
		t.Fatalf("old-run input released current task: runtime=%+v calls=%d", runtime, executor.callCount())
	}
}

func TestV2KeptRuntime_DefinitionProjectsUpstreamAndRunsChangedSuccessor_FileStore(t *testing.T) {
	first := coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "kept"},
			{ID: "n2", Title: "old", DependsOn: []string{"n1"}},
		},
		nil,
	)
	h := newCoordinatorHarness(t, first)
	executor := &reuseExecutor{}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, nil); err != nil {
		t.Fatal(err)
	}
	oldTaskID, _ := DeriveTaskID(h.run, "n1")
	before, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	oldRef := before.V2TaskRuntimes[oldTaskID].Attempts[0].ResultRef

	applied := createAndApplyNext(t, h, coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "kept"},
			{ID: "n2", Title: "changed", DependsOn: []string{"n1"}},
		},
		nil,
	), "next-"+t.Name())
	newUpstreamID, _ := DeriveTaskID(applied.Intent.RunID, "n1")
	newSuccessorID, _ := DeriveTaskID(applied.Intent.RunID, "n2")
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	upstream := projection.V2TaskRuntimes[newUpstreamID]
	successor := projection.V2TaskRuntimes[newSuccessorID]
	if upstream == nil || upstream.State != TaskCompleted ||
		len(upstream.Attempts) != 1 || upstream.Attempts[0].ResultRef != oldRef {
		t.Fatalf("kept upstream was not projected: %+v", upstream)
	}
	if successor == nil || successor.State != TaskCompleted {
		t.Fatalf("changed successor did not advance: %+v", successor)
	}
	if executor.callCount() != 3 {
		t.Fatalf("kept node re-executed: calls=%d, want old n1+n2 and new n2", executor.callCount())
	}
}

func TestV2KeptRuntime_WorkflowPatchProjectsUpstream_FileStore(t *testing.T) {
	definition := coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "kept", InputSpecIDs: []string{"topic"}},
			{ID: "n2", Title: "old", DependsOn: []string{"n1"}, BlockIDs: []string{"b1"}},
		},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	)
	h := newCoordinatorHarness(t, definition)
	keptInput := requestTaskInput(t, h, h.run, "n1", "kept-topic", "topic")
	submitTaskInput(t, h, keptInput, `"animals"`)
	now := time.Now().UTC()
	blockPayload, _ := json.Marshal(BlockInstance{
		ID: "b1", Kind: "markdown", SchemaVersion: 1, Revision: 1,
		Title: "block", Status: BlockReady, Data: json.RawMessage(`{"content":"old"}`),
		Source:   BlockSource{Provider: "test", Mode: "snapshot"},
		Fallback: BlockFallback{Summary: "block"}, CreatedAt: now, UpdatedAt: now,
	})
	_, state, _ := h.store.LoadState(h.work, "")
	blockEvent := newServiceEvent(h.work, "block-"+t.Name(), EventBlockUpserted, blockPayload, now)
	blockEvent.BaseRevision, blockEvent.Revision = state.Revision, state.Revision+1
	if _, err := h.store.CommitEvent(h.work, blockEvent); err != nil {
		t.Fatal(err)
	}
	executor := &reuseExecutor{}
	h.svc.SetTaskExecutor(executor)
	h.svc.SetV2PatchPlanner(reusePatchPlanner{})
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, nil); err != nil {
		t.Fatal(err)
	}
	current, state, _ := h.store.LoadState(h.work, "")
	preview, err := h.svc.PreviewV2WorkPatch(context.Background(), PreviewWorkPatchInput{
		WorkID: h.work, RunID: h.run, TaskID: "n2", BlockID: "b1",
		SessionID: "discussion", Instruction: "change successor",
		DefinitionRevision: current.V2CurrentRevision, BlockRevision: current.Blocks[0].Revision,
		Scope: PatchWorkflow, RequestID: "preview-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ = h.store.LoadState(h.work, "")
	applyInput := ApplyWorkPatchInput{
		WorkID: h.work, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: PatchWorkflow, ExpectedRevision: state.Revision, RequestID: "patch-apply-" + t.Name(),
	}
	result, err := h.svc.ApplyV2WorkPatch(context.Background(), applyInput)
	if err != nil {
		t.Fatal(err)
	}
	appliedDefinition, err := h.store.LoadRevision(h.work, result.NewRevision)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	newRunID := latestRunIDForDigest(projection, appliedDefinition.Digest)
	upstreamID, _ := DeriveTaskID(newRunID, "n1")
	successorID, _ := DeriveTaskID(newRunID, "n2")
	if runtime := projection.V2TaskRuntimes[upstreamID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("workflow patch did not project kept upstream: %+v", runtime)
	}
	carriedInput := findV2TaskInput(projection.V2Inputs, newRunID, upstreamID, "topic")
	if carriedInput == nil || carriedInput.ID == keptInput.ID ||
		carriedInput.BlockID != keptInput.BlockID || carriedInput.State != InputSubmitted ||
		string(carriedInput.Value) != `"animals"` {
		t.Fatalf("workflow patch did not carry kept block data into new input identity: old=%+v new=%+v",
			keptInput, carriedInput)
	}
	if runtime := projection.V2TaskRuntimes[successorID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("workflow patch successor did not run: %+v", runtime)
	}
	if executor.callCount() != 3 {
		t.Fatalf("workflow patch re-executed kept node: calls=%d", executor.callCount())
	}
	beforeReplayRuns := len(projection.Runs)
	beforeReplayRuntimes := len(projection.V2TaskRuntimes)
	beforeReplayInputs := len(projection.V2Inputs)
	replayedResult, err := h.svc.ApplyV2WorkPatch(context.Background(), applyInput)
	if err != nil || replayedResult == nil || !replayedResult.Duplicate {
		t.Fatalf("workflow patch replay failed: result=%+v err=%v", replayedResult, err)
	}
	afterReplay, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterReplay.Runs) != beforeReplayRuns ||
		len(afterReplay.V2TaskRuntimes) != beforeReplayRuntimes ||
		len(afterReplay.V2Inputs) != beforeReplayInputs ||
		executor.callCount() != 3 {
		t.Fatal("workflow patch replay duplicated run, input, runtime, or execution")
	}
}

func TestV2KeptContext_ApplyDefinitionCarriesInputData_FileStore(t *testing.T) {
	definition := coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "kept", InputSpecIDs: []string{"topic"}},
			{ID: "n2", Title: "changed", InputSpecIDs: []string{"tone"}},
		},
		[]InputSpec{
			{ID: "topic", Label: "Topic", Kind: InputText, Required: true, ValueSchema: json.RawMessage(`{"type":"string"}`)},
			{ID: "tone", Label: "Tone", Kind: InputText, Required: true, ValueSchema: json.RawMessage(`{"type":"string"}`)},
		},
	)
	h := newCoordinatorHarness(t, definition)
	oldInput := requestTaskInput(t, h, h.run, "n1", "definition-topic", "topic")
	submitTaskInput(t, h, oldInput, `"preserve me"`)
	changedInput := requestTaskInput(t, h, h.run, "n2", "definition-tone", "tone")
	submitTaskInput(t, h, changedInput, `"old tone"`)
	executor := &reuseExecutor{}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, nil); err != nil {
		t.Fatal(err)
	}

	next := coordinatorDefinition(definition.Nodes, definition.InputSpecs)
	next.Nodes[1].Description = "new execution semantics"
	applied := createAndApplyNext(t, h, next, "carry-input-"+t.Name())
	newTaskID, _ := DeriveTaskID(applied.Intent.RunID, "n1")
	changedTaskID, _ := DeriveTaskID(applied.Intent.RunID, "n2")
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	carried := findV2TaskInput(projection.V2Inputs, applied.Intent.RunID, newTaskID, "topic")
	if carried == nil || carried.ID == oldInput.ID || carried.BlockID != oldInput.BlockID ||
		carried.State != InputSubmitted || string(carried.Value) != `"preserve me"` {
		t.Fatalf("ApplyDefinition did not carry kept block data: old=%+v new=%+v", oldInput, carried)
	}
	runtime := projection.V2TaskRuntimes[newTaskID]
	if runtime == nil || runtime.State != TaskCompleted || executor.callCount() != 2 {
		t.Fatalf("kept task reran or lost completion: runtime=%+v calls=%d", runtime, executor.callCount())
	}
	changed := findV2TaskInput(projection.V2Inputs, applied.Intent.RunID, changedTaskID, "tone")
	if changed == nil || changed.ID == changedInput.ID || changed.State != InputRequested ||
		string(changed.Value) == `"old tone"` {
		t.Fatalf("changed task inherited superseded block data: old=%+v new=%+v", changedInput, changed)
	}
}

func TestV2WorkflowPatch_CompletedTargetRerunsEntireDownstream_FileStore(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "Plan theme", BlockIDs: []string{"b1"}},
			{ID: "n2", Title: "Create jokes", DependsOn: []string{"n1"}},
			{ID: "n3", Title: "Compile series", DependsOn: []string{"n2"}, ProducesSlotIDs: []string{"slot"}},
		},
		nil,
	))
	now := time.Now().UTC()
	blockPayload, _ := json.Marshal(BlockInstance{
		ID: "b1", Kind: "markdown", SchemaVersion: 1, Revision: 1,
		Title: "Plan theme", Status: BlockReady, Data: json.RawMessage(`{"content":"original guidance"}`),
		Source:   BlockSource{Provider: "test", Mode: "snapshot"},
		Fallback: BlockFallback{Summary: "original guidance"}, CreatedAt: now, UpdatedAt: now,
	})
	_, state, _ := h.store.LoadState(h.work, "")
	blockEvent := newServiceEvent(h.work, "block-"+t.Name(), EventBlockUpserted, blockPayload, now)
	blockEvent.BaseRevision, blockEvent.Revision = state.Revision, state.Revision+1
	if _, err := h.store.CommitEvent(h.work, blockEvent); err != nil {
		t.Fatal(err)
	}

	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	h.svc.SetV2PatchPlanner(targetPatchPlanner{nodeID: "n1"})
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, nil); err != nil {
		t.Fatal(err)
	}
	completed, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != WorkCompleted || executor.callCount() != 3 {
		t.Fatalf("initial run did not complete once per node: state=%s calls=%d", completed.State, executor.callCount())
	}

	_, state, _ = h.store.LoadState(h.work, "")
	preview, err := h.svc.PreviewV2WorkPatch(context.Background(), PreviewWorkPatchInput{
		WorkID: h.work, RunID: h.run, TaskID: "n1", BlockID: "b1",
		SessionID: "discussion", Instruction: "use animal themes",
		DefinitionRevision: completed.V2CurrentRevision, BlockRevision: 1,
		Scope: PatchWorkflow, RequestID: "preview-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.Preview.InvalidatedTaskIDs; !containsAllIDs(got, "n1", "n2", "n3") {
		t.Fatalf("preview invalidation=%v, want target and all descendants", got)
	}
	_, state, _ = h.store.LoadState(h.work, "")
	applyInput := ApplyWorkPatchInput{
		WorkID: h.work, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: PatchWorkflow, ExpectedRevision: state.Revision, RequestID: "patch-apply-" + t.Name(),
	}
	result, err := h.svc.ApplyV2WorkPatch(context.Background(), applyInput)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RequiresRerun || !containsAllIDs(result.InvalidatedTaskIDs, "n1", "n2", "n3") {
		t.Fatalf("apply impact=%+v, want target and all descendants", result)
	}

	definition, err := h.store.LoadRevision(h.work, result.NewRevision)
	if err != nil {
		t.Fatal(err)
	}
	after, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	newRunID := latestRunIDForDigest(after, definition.Digest)
	if newRunID == "" || newRunID == h.run || after.State != WorkCompleted {
		t.Fatalf("revision run not settled independently: run=%q old=%q state=%s", newRunID, h.run, after.State)
	}
	for _, nodeID := range []string{"n1", "n2", "n3"} {
		taskID, _ := DeriveTaskID(newRunID, nodeID)
		runtime := after.V2TaskRuntimes[taskID]
		if runtime == nil || runtime.State != TaskCompleted || len(runtime.Attempts) != 1 {
			t.Fatalf("revised runtime %s=%+v", nodeID, runtime)
		}
	}
	if executor.callCount() != 6 {
		t.Fatalf("completed target patch executed %d tasks, want old 3 + revised 3", executor.callCount())
	}

	replayed, err := h.svc.ApplyV2WorkPatch(context.Background(), applyInput)
	if err != nil || replayed == nil || !replayed.Duplicate || executor.callCount() != 6 {
		t.Fatalf("duplicate apply was not idempotent: result=%+v err=%v calls=%d", replayed, err, executor.callCount())
	}
}

func containsAllIDs(values []string, expected ...string) bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	for _, value := range expected {
		if !set[value] {
			return false
		}
	}
	return true
}

func TestV2KeptRuntime_FailedRuntimeIsNotReused_FileStore(t *testing.T) {
	definition := coordinatorDefinition([]NodeDef{
		{ID: "n1", Title: "kept"},
		{ID: "n2", Title: "old", DependsOn: []string{"n1"}},
	}, nil)
	h := newCoordinatorHarness(t, definition)
	executor := &reuseExecutor{failFirst: true}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, nil); err != nil {
		t.Fatal(err)
	}
	applied := createAndApplyNext(t, h, coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "kept"},
			{ID: "n2", Title: "changed", DependsOn: []string{"n1"}},
		}, nil,
	), "next-"+t.Name())
	upstreamID, _ := DeriveTaskID(applied.Intent.RunID, "n1")
	successorID, _ := DeriveTaskID(applied.Intent.RunID, "n2")
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if runtime := projection.V2TaskRuntimes[upstreamID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("failed kept runtime was reused instead of rerun: %+v", runtime)
	}
	if runtime := projection.V2TaskRuntimes[successorID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("changed successor stayed blocked after kept rerun: %+v", runtime)
	}
	if executor.callCount() != 3 {
		t.Fatalf("failed kept runtime/new successor calls=%d, want 3", executor.callCount())
	}
}

func TestV2KeptRuntime_StaleRuntimeIsNotReused_FileStore(t *testing.T) {
	definition := coordinatorDefinition(
		[]NodeDef{{ID: "n1", Title: "same", InputSpecIDs: []string{"topic"}}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: false,
			ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	)
	h := newCoordinatorHarness(t, definition)
	input := requestTaskInput(t, h, h.run, "n1", "stale-input", "topic")
	executor := &coordinatorExecutor{
		blockRun: h.run,
		started:  make(chan TaskExecuteInput, 1),
		release:  make(chan struct{}),
	}
	h.svc.SetTaskExecutor(executor)
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, nil)
		done <- err
	}()
	oldCall := <-executor.started
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	submitResult, err := h.svc.SubmitV2Input(context.Background(), SubmitInputRequest{
		WorkID: h.work, InputID: input.ID, Value: json.RawMessage(`"changed"`),
		DefinitionRev: h.def.Revision, InputRevision: input.Revision,
		ExpectedRevision: state.Revision, RequestID: "submit-stale-" + t.Name(),
	})
	if err != nil || submitResult == nil || submitResult.Error != "" {
		t.Fatalf("submit failed: result=%+v err=%v", submitResult, err)
	}
	applied := createAndApplyNext(t, h, coordinatorDefinition(
		definition.Nodes, definition.InputSpecs,
	), "next-"+t.Name())
	newTaskID, _ := DeriveTaskID(applied.Intent.RunID, "n1")
	during, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if runtime := during.V2TaskRuntimes[newTaskID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("new run reused or waited on stale old runtime/input: %+v", runtime)
	}
	close(executor.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	assertStaleAttempt(t, after, oldCall.TaskID)
	if executor.callCount() != 2 {
		t.Fatalf("stale kept runtime should execute new run exactly once: calls=%d", executor.callCount())
	}
}

func TestV2KeptRuntime_BatchFailureHasNoHalfProjectionAndRetries_FileStore(t *testing.T) {
	first := coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "kept"},
			{ID: "n2", Title: "old", DependsOn: []string{"n1"}},
		},
		nil,
	)
	h := newCoordinatorHarness(t, first)
	executor := &reuseExecutor{}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, nil); err != nil {
		t.Fatal(err)
	}
	next := coordinatorDefinition(
		[]NodeDef{
			{ID: "n1", Title: "kept"},
			{ID: "n2", Title: "changed", DependsOn: []string{"n1"}},
		},
		nil,
	)
	_, state, _ := h.store.LoadState(h.work, "")
	next.WorkID = h.work
	candidate, err := h.svc.CreateCandidateRevision(
		context.Background(), h.work, next, "next-candidate-"+t.Name(), state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	before, state, _ := h.store.LoadState(h.work, "")
	fault := &failReuseBatchStore{FileWorkStore: h.store, fail: true}
	failingService := NewService(fault, nil, nil)
	request := ApplyDefinitionInput{
		WorkID: h.work, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "next-apply-" + t.Name(),
	}
	if _, err := failingService.ApplyDefinition(context.Background(), request); err == nil {
		t.Fatal("expected injected batch failure")
	}
	afterFailure, afterState, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Revision != state.Revision || len(afterFailure.Runs) != len(before.Runs) ||
		len(afterFailure.V2TaskRuntimes) != len(before.V2TaskRuntimes) {
		t.Fatalf("batch failure left half projection: beforeRev=%d afterRev=%d", state.Revision, afterState.Revision)
	}
	h.svc.SetTaskExecutor(executor)
	result, err := h.svc.ApplyDefinition(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	replayedResult, err := h.svc.ApplyDefinition(context.Background(), request)
	if err != nil || replayedResult == nil || replayedResult.Intent.RunID != result.Intent.RunID {
		t.Fatalf("definition replay failed: result=%+v err=%v", replayedResult, err)
	}
	afterReplay, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterReplay.Runs) != len(beforeReplay.Runs) ||
		len(afterReplay.V2TaskRuntimes) != len(beforeReplay.V2TaskRuntimes) {
		t.Fatal("definition replay duplicated run or kept runtime")
	}
	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := DeriveTaskID(result.Intent.RunID, "n1")
	if runtime := replayed.V2TaskRuntimes[taskID]; runtime == nil || runtime.State != TaskCompleted {
		t.Fatalf("retry/restart lost kept projection: %+v", runtime)
	}
}
