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

// ── EvaluateReadySet pure function tests ────────────────────────────────────

func TestEvaluateReadySet_EmptyNodes(t *testing.T) {
	rs := EvaluateReadySet(nil, nil)
	if len(rs.Ready) != 0 || len(rs.Blocked) != 0 {
		t.Fatal("empty nodes should produce empty result")
	}
}

func TestEvaluateReadySet_AllPendingNoDeps(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B"},
		{ID: "c", Title: "C"},
	}
	rs := EvaluateReadySet(nodes, nil)
	if len(rs.Ready) != 3 {
		t.Fatalf("all pending no-deps nodes should be ready, got %v", rs.Ready)
	}
}

func TestEvaluateReadySet_ChainDependencies(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B", DependsOn: []string{"a"}},
		{ID: "c", Title: "C", DependsOn: []string{"b"}},
	}
	runtimes := map[string]*V2TaskRuntime{
		"a": {NodeID: "a", State: TaskCompleted},
	}
	rs := EvaluateReadySet(nodes, runtimes)
	if len(rs.Ready) != 1 || rs.Ready[0] != "b" {
		t.Fatalf("only b should be ready, got %v", rs.Ready)
	}
}

func TestEvaluateReadySet_DiamondDAG(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B", DependsOn: []string{"a"}},
		{ID: "c", Title: "C", DependsOn: []string{"a"}},
		{ID: "d", Title: "D", DependsOn: []string{"b", "c"}},
	}
	runtimes := map[string]*V2TaskRuntime{
		"a": {NodeID: "a", State: TaskCompleted},
	}
	rs := EvaluateReadySet(nodes, runtimes)
	if len(rs.Ready) != 2 {
		t.Fatalf("b and c should both be ready, got %v", rs.Ready)
	}
}

func TestEvaluateReadySet_PartialWaitingDoesNotBlockUnrelated(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B", DependsOn: []string{"a"}},
		{ID: "c", Title: "C", DependsOn: []string{"a"}},
		{ID: "d", Title: "D", DependsOn: []string{"b"}},
	}
	runtimes := map[string]*V2TaskRuntime{
		"a": {NodeID: "a", State: TaskCompleted},
		"b": {NodeID: "b", State: TaskWaitingInput},
	}
	rs := EvaluateReadySet(nodes, runtimes)
	if len(rs.Ready) != 1 || rs.Ready[0] != "c" {
		t.Fatalf("c should be ready (b blocks only d), got ready=%v blocked=%v", rs.Ready, rs.Blocked)
	}
}

func TestEvaluateReadySet_GlobalGateBlocksAll(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B"},
		{ID: "g", Title: "GlobalGate", GlobalGate: "release_approval"},
	}
	runtimes := map[string]*V2TaskRuntime{
		"a": {NodeID: "a", State: TaskCompleted},
	}
	rs := EvaluateReadySet(nodes, runtimes)
	if !rs.HasGlobalBlock || !reflect.DeepEqual(rs.Ready, []string{"g"}) {
		t.Fatalf("global gate should allow only itself, got ready=%v hasGlobalBlock=%v", rs.Ready, rs.HasGlobalBlock)
	}
}

func TestEvaluateReadySet_GlobalGateCompleted(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "g", Title: "GlobalGate", GlobalGate: "release"},
	}
	runtimes := map[string]*V2TaskRuntime{
		"g": {NodeID: "g", State: TaskCompleted},
	}
	rs := EvaluateReadySet(nodes, runtimes)
	if rs.HasGlobalBlock {
		t.Fatal("completed global gate should not block")
	}
	if len(rs.Ready) != 1 || rs.Ready[0] != "a" {
		t.Fatalf("a should be ready, got %v", rs.Ready)
	}
}

func TestEvaluateReadySet_DependentGlobalGateWaitsForUpstream(t *testing.T) {
	nodes := []NodeDef{
		{ID: "prepare"},
		{ID: "gate", DependsOn: []string{"prepare"}, GlobalGate: "release"},
		{ID: "unrelated"},
	}
	before := EvaluateReadySet(nodes, nil)
	if before.HasGlobalBlock || !reflect.DeepEqual(before.Ready, []string{"prepare", "unrelated"}) {
		t.Fatalf("dependent gate blocked before ready: %+v", before)
	}
	after := EvaluateReadySet(nodes, map[string]*V2TaskRuntime{
		"prepare": {NodeID: "prepare", State: TaskCompleted},
	})
	if !after.HasGlobalBlock || !reflect.DeepEqual(after.Ready, []string{"gate"}) {
		t.Fatalf("ready dependent gate must become global: %+v", after)
	}
}

func TestEvaluateReadySet_FailedRetryableIsReady(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
	}
	runtimes := map[string]*V2TaskRuntime{
		"a": {NodeID: "a", State: TaskFailedRetryable},
	}
	rs := EvaluateReadySet(nodes, runtimes)
	if len(rs.Ready) != 1 {
		t.Fatalf("failed_retryable should be ready, got %v", rs.Ready)
	}
}

func TestEvaluateReadySet_InvalidatedIsReady(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
	}
	runtimes := map[string]*V2TaskRuntime{
		"a": {NodeID: "a", State: TaskInvalidated},
	}
	rs := EvaluateReadySet(nodes, runtimes)
	if len(rs.Ready) != 1 {
		t.Fatalf("invalidated should be ready, got %v", rs.Ready)
	}
}

func TestEvaluateReadySet_RunningIsNotReady(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
	}
	runtimes := map[string]*V2TaskRuntime{
		"a": {NodeID: "a", State: TaskRunning},
	}
	rs := EvaluateReadySet(nodes, runtimes)
	if len(rs.Ready) != 0 {
		t.Fatalf("running node should not be ready, got %v", rs.Ready)
	}
}

// ── AffectedNodes tests ────────────────────────────────────────────────────

func TestAffectedNodes_Diamond(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B", DependsOn: []string{"a"}},
		{ID: "c", Title: "C", DependsOn: []string{"a"}},
		{ID: "d", Title: "D", DependsOn: []string{"b", "c"}},
	}
	affected := AffectedNodes(nodes, []string{"a"})
	if len(affected) != 4 {
		t.Fatalf("all 4 nodes affected by a change, got %v", affected)
	}
	affectedB := AffectedNodes(nodes, []string{"b"})
	if len(affectedB) != 2 {
		t.Fatalf("b change affects b,d only, got %v", affectedB)
	}
}

func TestAffectedNodes_NoDownstream(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B", DependsOn: []string{"a"}},
	}
	affected := AffectedNodes(nodes, []string{"b"})
	if len(affected) != 1 || affected[0] != "b" {
		t.Fatalf("leaf node affects only itself, got %v", affected)
	}
}

// ── Digest computation tests ────────────────────────────────────────────────

func TestComputeInputDigest_Empty(t *testing.T) {
	d := ComputeInputDigest(nil, "w", "r", "t", nil)
	if d != "inputs:none" {
		t.Fatalf("empty inputs: %s", d)
	}
}

func TestComputeInputDigest_Deterministic(t *testing.T) {
	inputs := []WorkInput{
		{ID: "i1", WorkID: "w", RunID: "r", TaskID: "t", SpecID: "s1", Value: json.RawMessage(`"hello"`), State: InputSubmitted, Revision: 1},
		{ID: "i2", WorkID: "w", RunID: "r", TaskID: "t", SpecID: "s2", Value: json.RawMessage(`42`), State: InputSubmitted, Revision: 2},
	}
	d1 := ComputeInputDigest(inputs, "w", "r", "t", []string{"s1", "s2"})
	d2 := ComputeInputDigest(inputs, "w", "r", "t", []string{"s2", "s1"})
	if d1 != d2 {
		t.Fatalf("digest order-dependent: %s vs %s", d1, d2)
	}
}

func TestComputeInputDigest_SkipsNonSubmitted(t *testing.T) {
	inputs := []WorkInput{
		{ID: "i1", WorkID: "w", RunID: "r", TaskID: "t", SpecID: "s1", Value: json.RawMessage(`"ok"`), State: InputSubmitted, Revision: 1},
		{ID: "i2", WorkID: "w", RunID: "r", TaskID: "t", SpecID: "s2", Value: json.RawMessage(`"draft"`), State: InputDraft, Revision: 1},
	}
	d := ComputeInputDigest(inputs, "w", "r", "t", []string{"s1", "s2"})
	if d == "inputs:none" {
		t.Fatal("digest should not be empty")
	}
	// draft s2 should be excluded — digest should reflect only s1.
	dOnlyS1 := ComputeInputDigest(inputs, "w", "r", "t", []string{"s1"})
	if d != dOnlyS1 {
		t.Fatalf("draft input should be excluded: %s vs %s", d, dOnlyS1)
	}
}

func TestHasAllRequiredInputs_Missing(t *testing.T) {
	specs := []InputSpec{
		{ID: "s1", Required: true, Kind: InputText},
		{ID: "s2", Required: true, Kind: InputFile},
	}
	inputs := []WorkInput{
		{WorkID: "w", RunID: "r", TaskID: "t", SpecID: "s1", State: InputSubmitted},
	}
	hasAll, missing := HasAllRequiredInputs(inputs, specs, "w", "r", "t", []string{"s1", "s2"})
	if hasAll {
		t.Fatal("should report missing s2")
	}
	if len(missing) != 1 || missing[0] != "s2" {
		t.Fatalf("missing: %v", missing)
	}
}

func TestHasAllRequiredInputs_AllPresent(t *testing.T) {
	specs := []InputSpec{
		{ID: "s1", Required: true, Kind: InputText},
	}
	inputs := []WorkInput{
		{WorkID: "w", RunID: "r", TaskID: "t", SpecID: "s1", State: InputAccepted},
	}
	hasAll, _ := HasAllRequiredInputs(inputs, specs, "w", "r", "t", []string{"s1"})
	if !hasAll {
		t.Fatal("should report all inputs present")
	}
}

func TestHasAllRequiredInputs_ApprovalRequiresApprovedDecision(t *testing.T) {
	specs := []InputSpec{{ID: "approval", Kind: InputApproval, Required: true}}
	base := WorkInput{
		ID: "i", WorkID: "w", RunID: "r", TaskID: "t", SpecID: "approval",
		State: InputSubmitted,
	}
	for _, tc := range []struct {
		name  string
		value string
		state InputState
		want  bool
	}{
		{name: "approved", value: `"approved"`, state: InputSubmitted, want: true},
		{name: "approved normalized", value: `" APPROVED "`, state: InputAccepted, want: true},
		{name: "rejected", value: `"rejected"`, state: InputSubmitted, want: false},
		{name: "declined", value: `"declined"`, state: InputSubmitted, want: false},
		{name: "empty", value: `""`, state: InputSubmitted, want: false},
		{name: "unknown", value: `"later"`, state: InputAccepted, want: false},
		{name: "requested state", value: `"approved"`, state: InputRequested, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			input.Value = json.RawMessage(tc.value)
			input.State = tc.state
			got, missing := HasAllRequiredInputs(
				[]WorkInput{input}, specs, "w", "r", "t", []string{"approval"},
			)
			if got != tc.want {
				t.Fatalf("HasAllRequiredInputs=%v, want %v; missing=%v", got, tc.want, missing)
			}
		})
	}
}

func TestHasAllRequiredInputs_OrdinarySubmittedInputUnchanged(t *testing.T) {
	specs := []InputSpec{{ID: "text", Kind: InputText, Required: true}}
	input := WorkInput{
		ID: "i", WorkID: "w", RunID: "r", TaskID: "t", SpecID: "text",
		Value: json.RawMessage(`"rejected"`), State: InputSubmitted,
	}
	got, missing := HasAllRequiredInputs(
		[]WorkInput{input}, specs, "w", "r", "t", []string{"text"},
	)
	if !got || len(missing) != 0 {
		t.Fatalf("ordinary submitted input changed: got=%v missing=%v", got, missing)
	}
}

// ── Execution token tests ───────────────────────────────────────────────────

func TestGenerateExecutionToken_Stable(t *testing.T) {
	t1 := GenerateExecutionToken("t1", 1, "sha256:abc", "sha256:def")
	t2 := GenerateExecutionToken("t1", 1, "sha256:abc", "sha256:def")
	if t1 != t2 {
		t.Fatalf("token not deterministic: %s vs %s", t1, t2)
	}
}

func TestGenerateExecutionToken_ChangesWithDigest(t *testing.T) {
	t1 := GenerateExecutionToken("t1", 1, "sha256:abc", "sha256:def")
	t2 := GenerateExecutionToken("t1", 1, "sha256:xyz", "sha256:def")
	if t1 == t2 {
		t.Fatal("token should change with input digest")
	}
}

// ── DeriveV2SideEffectClass tests ───────────────────────────────────────────

func TestDeriveV2SideEffectClass_DefaultRead(t *testing.T) {
	c := DeriveV2SideEffectClass(nil)
	if c != "read" {
		t.Fatalf("default: %s", c)
	}
}

func TestDeriveV2SideEffectClass_ExternalWrite(t *testing.T) {
	c := DeriveV2SideEffectClass([]string{"side_effect=external_write"})
	if c != "external_write" {
		t.Fatalf("external_write: %s", c)
	}
}

func TestDeriveV2SideEffectClass_DestructiveWins(t *testing.T) {
	c := DeriveV2SideEffectClass([]string{"side_effect=external_write", "side_effect=destructive"})
	if c != "destructive" {
		t.Fatalf("destructive should win: %s", c)
	}
}

func TestUpgradeV2SideEffectClass_NeverDowngrades(t *testing.T) {
	if got := UpgradeV2SideEffectClass("external_write", "read"); got != "external_write" {
		t.Fatalf("risk downgraded: %q", got)
	}
	if got := UpgradeV2SideEffectClass("workspace_write", "destructive"); got != "destructive" {
		t.Fatalf("risk did not upgrade: %q", got)
	}
}

// ── FileWorkStore integration: event replay → projection → ready set ───────

func TestFileWorkStore_V2TaskRuntimeRoundTrip(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-rt-001"
	runID := "run-001"
	taskID, _ := DeriveTaskID(runID, "n1")
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}

	rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)
	payload, _ := json.Marshal(TaskRuntimeCreatedPayload{
		TaskID: rt.TaskID, WorkID: rt.WorkID, RunID: rt.RunID,
		NodeID: rt.NodeID, ExpectedRevision: 0, DefinitionRev: rt.DefinitionRev,
		SideEffectClass: rt.SideEffectClass, Runtime: *rt,
	})

	if _, err := store.CommitEvent(workID, WorkEvent{
		SchemaVersion: SchemaVersionV2,
		ID:            workID + "/rt/create/" + taskID,
		Type:          EventTaskRuntimeCreated,
		WorkID:        workID,
		RequestID:     taskID + "/create",
		Payload:       json.RawMessage(payload),
		Object: ObjectContext{
			Kind: ObjectTask, WorkID: workID, ID: taskID,
			RunID: runID, TaskID: taskID, ExpectedRevision: int64Ptr(0), DefinitionRevision: int64Ptr(1),
		},
	}); err != nil {
		t.Fatal(err)
	}

	proj, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	if proj.V2TaskRuntimes == nil || proj.V2TaskRuntimes[taskID] == nil {
		t.Fatal("V2TaskRuntime not found in projection")
	}
	if proj.V2TaskRuntimes[taskID].NodeID != "n1" {
		t.Fatalf("nodeID: %s", proj.V2TaskRuntimes[taskID].NodeID)
	}
}

func TestFileWorkStore_V2TaskRuntimeReplay(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-replay-001"
	runID := "run-001"
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}

	emit := storeEventEmitter(store, workID)
	rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)

	if err := emitRuntimeCreated(emit, rt, now); err != nil {
		t.Fatal(err)
	}
	if err := emitRuntimeUpdated(emit, rt, TaskReady, nil, now); err != nil {
		t.Fatal(err)
	}
	attempt1 := V2Attempt{
		ID: "att-0", Index: 0, State: TaskRunning, StartedAt: now,
		DefinitionRev: 1, InputDigest: "inputs:none", DependencyDigest: "deps:none",
		ExecutionToken: "tok1",
	}
	if err := emitRuntimeUpdated(emit, rt, TaskRunning, &attempt1, now); err != nil {
		t.Fatal(err)
	}
	if err := emitRuntimeUpdated(emit, rt, TaskCompleted, nil, now); err != nil {
		t.Fatal(err)
	}

	proj, _ := store.LoadProjection(workID)
	taskID, _ := DeriveTaskID(runID, "n1")
	stored := proj.V2TaskRuntimes[taskID]
	if stored == nil || stored.State != TaskCompleted || len(stored.Attempts) != 1 {
		t.Fatalf("replay inconsistent: state=%s attempts=%d", stored.State, len(stored.Attempts))
	}
}

func TestFileWorkStore_DuplicateRuntimeCreatedIsIdempotent(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-dup-001"
	runID := "run-001"
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}

	emit := storeEventEmitter(store, workID)
	rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)

	if err := emitRuntimeCreated(emit, rt, now); err != nil {
		t.Fatal(err)
	}
	firstManifest, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate create — same requestID — idempotent.
	if err := emitRuntimeCreated(emit, rt, now); err != nil {
		t.Fatal("duplicate runtime_created should be idempotent:", err)
	}

	taskID, _ := DeriveTaskID(runID, "n1")
	proj, _ := store.LoadProjection(workID)
	if proj.V2TaskRuntimes[taskID] == nil || proj.V2TaskRuntimes[taskID].Revision != 1 {
		t.Fatal("duplicate created should not change revision")
	}
	manifest, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Revision != firstManifest.Revision {
		t.Fatalf("duplicate event polluted work revision: got %d want %d", manifest.Revision, firstManifest.Revision)
	}
}

func TestFileWorkStore_StaleResultEvent(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-stale-001"
	runID := "run-001"
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}

	taskID, _ := DeriveTaskID(runID, "n1")
	emit := storeEventEmitter(store, workID)
	slotPayload := mustMarshalV2(ArtifactSlotDeclaredPayload{
		SlotID: "slot-1", WorkID: workID, DefinitionRev: 1,
		Title: "Output", Kind: "file", ExpectedCount: 1, Required: true,
	})
	if _, err := store.CommitEvent(workID, WorkEvent{
		SchemaVersion: SchemaVersionV2,
		ID:            workID + "/slot/slot-1",
		Type:          EventArtifactSlotDeclared,
		WorkID:        workID,
		RequestID:     workID + "/slot/slot-1",
		Payload:       slotPayload,
		Object: ObjectContext{
			Kind: ObjectArtifactSlot, WorkID: workID, ID: "slot-1",
			ArtifactSlotID: "slot-1", DefinitionRevision: int64Ptr(1),
		},
	}); err != nil {
		t.Fatal(err)
	}
	rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)

	if err := emitRuntimeCreated(emit, rt, now); err != nil {
		t.Fatal(err)
	}
	if err := emitRuntimeUpdated(emit, rt, TaskReady, nil, now); err != nil {
		t.Fatal(err)
	}
	att := V2Attempt{
		ID: "att-0", Index: 0, State: TaskRunning, StartedAt: now,
		DefinitionRev: 1, InputDigest: "inputs:none", DependencyDigest: "deps:none",
		ExecutionToken: "old-token",
	}
	if err := emitRuntimeUpdated(emit, rt, TaskRunning, &att, now); err != nil {
		t.Fatal(err)
	}

	beforeStale, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	slotsBefore := append([]ArtifactSlot(nil), beforeStale.V2ArtifactSlots...)
	stalePayload, _ := json.Marshal(TaskStaleResultPayload{
		TaskID: taskID, WorkID: workID, RunID: runID,
		ExpectedRevision: rt.Revision,
		AttemptID:        "att-0", StaleToken: "old-token", CurrentToken: "new-token",
		ResultRef: "stale-output",
	})
	if _, err := store.CommitEvent(workID, WorkEvent{
		SchemaVersion: SchemaVersionV2, ID: workID + "/stale/" + taskID,
		Type:   EventTaskStaleResult,
		WorkID: workID, RequestID: taskID + "/stale",
		Payload: json.RawMessage(stalePayload),
		Object: ObjectContext{
			Kind: ObjectTask, WorkID: workID, ID: taskID,
			RunID: runID, TaskID: taskID, ExpectedRevision: int64Ptr(rt.Revision), DefinitionRevision: int64Ptr(1),
		},
	}); err != nil {
		t.Fatal(err)
	}

	proj, _ := store.LoadProjection(workID)
	stored := proj.V2TaskRuntimes[taskID]
	if stored == nil || len(stored.Attempts) == 0 || !stored.Attempts[0].StaleResult {
		t.Fatal("attempt should be marked stale")
	}
	if stored.Attempts[0].ResultRef != "stale-output" || stored.State != TaskRunning {
		t.Fatalf("stale result must preserve evidence without overwriting current state: %+v", stored)
	}
	if !reflect.DeepEqual(proj.V2ArtifactSlots, slotsBefore) {
		t.Fatalf("stale result must not overwrite artifact slots:\n got %+v\nwant %+v", proj.V2ArtifactSlots, slotsBefore)
	}
}

func TestFileWorkStore_TaskV2StateEvents(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-v2state-001"
	runID := "run-001"
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}

	taskID, _ := DeriveTaskID(runID, "n1")

	events := []struct {
		typ     WorkEventType
		payload json.RawMessage
	}{
		{EventTaskReady, mustMarshalV2(TaskReadyPayload{TaskID: taskID, WorkID: workID, RunID: runID})},
	}

	for i, ev := range events {
		_, err := store.CommitEvent(workID, WorkEvent{
			SchemaVersion: SchemaVersionV2, ID: fmt.Sprintf("%s-%s-%d", workID, ev.typ, i),
			Type:   ev.typ,
			WorkID: workID, RequestID: taskID + "/" + string(ev.typ),
			Payload: ev.payload,
			Object: ObjectContext{
				Kind: ObjectTask, WorkID: workID, ID: taskID,
				RunID: runID, TaskID: taskID,
			},
		})
		if err != nil {
			t.Fatalf("%s: %v", ev.typ, err)
		}
	}

	proj, _ := store.LoadProjection(workID)
	stored := proj.V2TaskRuntimes[taskID]
	if stored == nil || stored.State != TaskReady {
		t.Fatalf("final state should be ready, got %v", stored)
	}
}

func TestFileWorkStore_RestartRebuildsTaskRuntimes(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-restart-001"
	runID := "run-001"
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}

	emit := storeEventEmitter(store, workID)
	rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)

	if err := emitRuntimeCreated(emit, rt, now); err != nil {
		t.Fatal(err)
	}
	if err := emitRuntimeUpdated(emit, rt, TaskReady, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := emitRuntimeUpdated(emit, rt, TaskRunning, &V2Attempt{
		ID: "att-0", Index: 0, State: TaskRunning, StartedAt: now,
		DefinitionRev: 1, InputDigest: "inputs:none", DependencyDigest: "deps:none",
		ExecutionToken: "tok1", SideEffectClass: "read",
	}, now); err != nil {
		t.Fatal(err)
	}

	// Simulate restart: reload from same file system.
	proj, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := DeriveTaskID(runID, "n1")
	if proj.V2TaskRuntimes[taskID] == nil || proj.V2TaskRuntimes[taskID].State != TaskRunning {
		t.Fatalf("runtime should survive restart: %v", proj.V2TaskRuntimes[taskID])
	}
}

// ── Scheduler integration tests ─────────────────────────────────────────────

type fakeV2Executor struct {
	mu      sync.Mutex
	results map[string]func() (*Attempt, error)
	calls   []TaskExecuteInput
}

func (f *fakeV2Executor) ExecuteTask(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
	f.mu.Lock()
	f.calls = append(f.calls, input)
	fn, ok := f.results[input.TaskID]
	f.mu.Unlock()
	if ok {
		return fn()
	}
	return &Attempt{State: RunCompleted}, nil
}

func (f *fakeV2Executor) CancelTask(ctx context.Context, input TaskCancelInput) error { return nil }

func newFakeV2Executor() *fakeV2Executor {
	return &fakeV2Executor{results: make(map[string]func() (*Attempt, error))}
}

func (f *fakeV2Executor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeV2Executor) callIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		ids = append(ids, call.TaskID)
	}
	return ids
}

type blockingV2Executor struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
	max     atomic.Int32
	calls   atomic.Int32
}

func (f *blockingV2Executor) ExecuteTask(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
	f.calls.Add(1)
	active := f.active.Add(1)
	for {
		current := f.max.Load()
		if active <= current || f.max.CompareAndSwap(current, active) {
			break
		}
	}
	f.started <- input.TaskID
	select {
	case <-ctx.Done():
		f.active.Add(-1)
		return nil, ctx.Err()
	case <-f.release:
		f.active.Add(-1)
		return &Attempt{State: RunCompleted}, nil
	}
}

func (f *blockingV2Executor) CancelTask(context.Context, TaskCancelInput) error { return nil }

type gateSyncExecutor struct {
	gateTaskID string
	gateStart  chan struct{}
	release    chan struct{}
	startOnce  sync.Once
	calls      atomic.Int32
}

func (f *gateSyncExecutor) ExecuteTask(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
	f.calls.Add(1)
	if input.TaskID == f.gateTaskID {
		f.startOnce.Do(func() { close(f.gateStart) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.release:
		}
	}
	return &Attempt{State: RunCompleted}, nil
}

func (f *gateSyncExecutor) CancelTask(context.Context, TaskCancelInput) error { return nil }

type memoryV2Authority struct {
	mu         sync.Mutex
	projection *Work
	events     []WorkEvent
}

func newMemoryV2Authority(workID string, runtimes map[string]*V2TaskRuntime) *memoryV2Authority {
	return &memoryV2Authority{projection: &Work{
		SchemaVersion:  SchemaVersionV2,
		ID:             workID,
		V2TaskRuntimes: cloneV2RuntimeMap(runtimes),
	}}
}

func (a *memoryV2Authority) CommitV2Event(event WorkEvent) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Date(2026, 7, 24, 1, 0, len(a.events), 0, time.UTC)
	}
	raw, err := json.Marshal(a.projection)
	if err != nil {
		return 0, err
	}
	var cloned Work
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return 0, err
	}
	next, err := DefaultReducer()(event, &cloned)
	if err != nil {
		return 0, err
	}
	a.projection = next
	a.events = append(a.events, event)
	return int64(len(a.events)), nil
}

func (a *memoryV2Authority) LoadV2Projection() (*Work, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, err := json.Marshal(a.projection)
	if err != nil {
		return nil, err
	}
	var cloned Work
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func (a *memoryV2Authority) eventSnapshot() []WorkEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]WorkEvent(nil), a.events...)
}

func TestV2Scheduler_ScheduleSimpleDAG(t *testing.T) {
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "b", Title: "B", DependsOn: []string{"a"}},
	}

	authority := newMemoryV2Authority("w1", nil)
	result, err := sched.Schedule(context.Background(), "w1", "r1", nodes, nil, 1, nil, nil, authority)
	if err != nil {
		t.Fatal(err)
	}
	// a completes immediately (fake executor), so b becomes ready in same cycle.
	if len(result.Executed) < 1 || result.Executed[0] != "a" {
		t.Fatalf("expected a executed first, got %v", result.Executed)
	}
}

func TestV2Scheduler_GlobalGateBlocks(t *testing.T) {
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	nodes := []NodeDef{
		{ID: "a", Title: "A"},
		{ID: "g", Title: "Gate", GlobalGate: "release"},
	}

	authority := newMemoryV2Authority("w1", nil)
	result, err := sched.Schedule(context.Background(), "w1", "r1", nodes, nil, 1, nil, nil, authority)
	if err != nil {
		t.Fatal(err)
	}
	gateID, _ := DeriveTaskID("r1", "g")
	otherID, _ := DeriveTaskID("r1", "a")
	if !reflect.DeepEqual(result.Executed, []string{"a", "g"}) {
		t.Fatalf("gate should run before releasing the other node, got %v", result.Executed)
	}
	if got := exec.callIDs(); !reflect.DeepEqual(got, []string{gateID, otherID}) {
		t.Fatalf("gate execution order: got %v want [%s %s]", got, gateID, otherID)
	}
}

func TestV2Scheduler_DependentGlobalGateDoesNotBlockUpstream(t *testing.T) {
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	runID := "r-dependent-gate"
	nodes := []NodeDef{
		{ID: "prepare"},
		{ID: "gate", DependsOn: []string{"prepare"}, GlobalGate: "release"},
		{ID: "unrelated"},
	}
	authority := newMemoryV2Authority("w-dependent-gate", nil)
	if _, err := sched.Schedule(
		context.Background(),
		"w-dependent-gate",
		runID,
		nodes,
		nil,
		1,
		nil,
		nil,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	calls := exec.callIDs()
	gateID, _ := DeriveTaskID(runID, "gate")
	if len(calls) != 3 || calls[len(calls)-1] != gateID {
		t.Fatalf("dependent gate ran before upstream/unrelated work: %v", calls)
	}
}

func TestV2Scheduler_GlobalGateReleaseAdvancesAllBlockedBranchesFileStore(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-gate-release", "r-gate-release"
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	gate := V2NewTaskRuntime(workID, runID, "release", 1, "read", now)
	emit := storeEventEmitter(store, workID)
	if err := emitRuntimeCreated(emit, gate, now); err != nil {
		t.Fatal(err)
	}
	if err := updateRuntime(emit, gate, TaskWaitingApproval, nil, now, func(next *V2TaskRuntime) {
		next.ApprovalToken = "release-approved"
	}); err != nil {
		t.Fatal(err)
	}
	authority, err := NewFileV2RuntimeAuthority(store, workID)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeV2Executor()
	nodes := []NodeDef{
		{ID: "release", Title: "Release", GlobalGate: "approval"},
		{ID: "branch-a", Title: "Branch A"},
		{ID: "branch-b", Title: "Branch B"},
	}
	if _, err := NewV2Scheduler(exec).WakeAndScheduleAffected(
		context.Background(),
		workID,
		runID,
		nodes,
		1,
		nil,
		nil,
		[]string{gate.TaskID},
		V2WakeApproval,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]bool)
	for _, node := range nodes {
		taskID, _ := DeriveTaskID(runID, node.ID)
		want[taskID] = true
	}
	got := exec.callIDs()
	if len(got) != len(want) {
		t.Fatalf("gate release executor calls = %v", got)
	}
	for _, taskID := range got {
		if !want[taskID] {
			t.Fatalf("unexpected task after gate release: %s", taskID)
		}
		delete(want, taskID)
	}
	if len(want) != 0 {
		t.Fatalf("branches remained blocked after gate release: %v", want)
	}
	projection, err := authority.LoadV2Projection()
	if err != nil {
		t.Fatal(err)
	}
	for _, runtime := range projection.V2TaskRuntimes {
		if runtime.State != TaskCompleted {
			t.Fatalf("gate release runtime did not complete: %+v", runtime)
		}
	}
}

func TestV2Scheduler_CompletedGlobalGateRestartRescansWholeDAGFileStore(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-gate-restart", "r-gate-restart"
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	gate := V2NewTaskRuntime(workID, runID, "release", 1, "read", now)
	emit := storeEventEmitter(store, workID)
	if err := emitRuntimeCreated(emit, gate, now); err != nil {
		t.Fatal(err)
	}
	if err := updateRuntime(emit, gate, TaskReady, nil, now, nil); err != nil {
		t.Fatal(err)
	}
	attempt := V2Attempt{
		ID: V2RunAttemptID(gate.TaskID, 0), Index: 0, State: TaskRunning,
		StartedAt: now, DefinitionRev: 1, SideEffectClass: "read",
	}
	if err := updateRuntime(emit, gate, TaskRunning, &attempt, now, nil); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Second)
	if err := updateRuntime(emit, gate, TaskCompleted, nil, finished, func(next *V2TaskRuntime) {
		next.Attempts[0].State = TaskCompleted
		next.Attempts[0].FinishedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileWorkStore(store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewFileV2RuntimeAuthority(reopened, workID)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeV2Executor()
	nodes := []NodeDef{
		{ID: "release", Title: "Release", GlobalGate: "approval"},
		{ID: "branch-a", Title: "Branch A"},
		{ID: "branch-b", Title: "Branch B"},
	}
	if _, err := NewV2Scheduler(exec).WakeAndScheduleAffected(
		context.Background(),
		workID,
		runID,
		nodes,
		1,
		nil,
		nil,
		[]string{gate.TaskID},
		V2WakeApproval,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	if got := exec.callIDs(); len(got) != 2 {
		t.Fatalf("restart after completed gate did not resume all blocked branches: %v", got)
	}
	for _, taskID := range exec.callIDs() {
		if taskID == gate.TaskID {
			t.Fatalf("completed gate re-executed after restart: %v", exec.callIDs())
		}
	}
}

func TestV2Scheduler_GlobalGateWakeAcrossInstancesFileStore(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-gate-cross-instance", "r-gate-cross-instance"
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	gate := V2NewTaskRuntime(workID, runID, "release", 1, "read", now)
	emit := storeEventEmitter(store, workID)
	if err := emitRuntimeCreated(emit, gate, now); err != nil {
		t.Fatal(err)
	}
	if err := updateRuntime(emit, gate, TaskWaitingApproval, nil, now, func(next *V2TaskRuntime) {
		next.ApprovalToken = "release-approved"
	}); err != nil {
		t.Fatal(err)
	}
	firstAuthority, _ := NewFileV2RuntimeAuthority(store, workID)
	secondStore, err := NewFileWorkStore(store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondAuthority, _ := NewFileV2RuntimeAuthority(secondStore, workID)
	exec := &gateSyncExecutor{
		gateTaskID: gate.TaskID,
		gateStart:  make(chan struct{}),
		release:    make(chan struct{}),
	}
	nodes := []NodeDef{
		{ID: "release", Title: "Release", GlobalGate: "approval"},
		{ID: "branch-a", Title: "Branch A"},
		{ID: "branch-b", Title: "Branch B"},
	}
	type scheduleOutcome struct {
		result V2ScheduleResult
		err    error
	}
	firstDone := make(chan scheduleOutcome, 1)
	go func() {
		result, err := NewV2Scheduler(exec).WakeAndScheduleAffected(
			context.Background(), workID, runID, nodes, 1, nil, nil,
			[]string{gate.TaskID}, V2WakeApproval, firstAuthority,
		)
		firstDone <- scheduleOutcome{result: result, err: err}
	}()
	select {
	case <-exec.gateStart:
	case <-time.After(3 * time.Second):
		t.Fatal("first scheduler did not start gate")
	}
	secondResult, secondErr := NewV2Scheduler(exec).WakeAndScheduleAffected(
		context.Background(), workID, runID, nodes, 1, nil, nil,
		[]string{gate.TaskID}, V2WakeApproval, secondAuthority,
	)
	if secondErr != nil {
		t.Fatalf("late duplicate cross-instance wake: %v", secondErr)
	}
	if len(secondResult.Executed) != 0 {
		t.Fatalf("second instance duplicated in-flight gate: %+v", secondResult)
	}
	close(exec.release)
	select {
	case first := <-firstDone:
		if first.err != nil {
			t.Fatal(first.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first scheduler did not finish after gate release")
	}
	if got := exec.calls.Load(); got != 3 {
		t.Fatalf("cross-instance gate flow executor calls = %d, want gate + 2 branches", got)
	}
	projection, err := secondAuthority.LoadV2Projection()
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.V2TaskRuntimes) != 3 {
		t.Fatalf("cross-instance projection runtimes = %d, want 3", len(projection.V2TaskRuntimes))
	}
	for _, runtime := range projection.V2TaskRuntimes {
		if runtime.State != TaskCompleted {
			t.Fatalf("cross-instance runtime incomplete: %+v", runtime)
		}
	}
}

func TestV2Scheduler_SingleFlight(t *testing.T) {
	exec := newFakeV2Executor()
	taskID, _ := DeriveTaskID("r1", "a")
	exec.results[taskID] = func() (*Attempt, error) {
		return &Attempt{State: RunRunning}, nil
	}
	sched := NewV2Scheduler(exec)
	nodes := []NodeDef{{ID: "a", Title: "A"}}

	authority := newMemoryV2Authority("w1", nil)
	result, err := sched.Schedule(context.Background(), "w1", "r1", nodes, nil, 1, nil, nil, authority)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Executed) != 1 || len(authority.eventSnapshot()) == 0 {
		t.Fatalf("should execute once: executed=%v events=%d", result.Executed, len(authority.eventSnapshot()))
	}
}

func TestV2Scheduler_ExecutorError(t *testing.T) {
	exec := newFakeV2Executor()
	taskID, _ := DeriveTaskID("r1", "a")
	exec.results[taskID] = func() (*Attempt, error) {
		return nil, errors.New("simulated crash")
	}
	sched := NewV2Scheduler(exec)
	nodes := []NodeDef{{ID: "a", Title: "A"}}

	authority := newMemoryV2Authority("w1", nil)
	_, err := sched.Schedule(context.Background(), "w1", "r1", nodes, nil, 1, nil, nil, authority)
	// Executor errors are recorded as TaskFailedRetryable, not returned.
	if err != nil {
		t.Fatalf("executor errors should be recorded, not returned: %v", err)
	}
	if len(authority.eventSnapshot()) == 0 {
		t.Fatal("should emit events before executor failure")
	}
	if exec.callCount() != 1 {
		t.Fatalf("retryable failure must run once per Schedule call, got %d", exec.callCount())
	}
}

func TestV2Scheduler_DeclaredArtifactsRequireReporter(t *testing.T) {
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	authority := newMemoryV2Authority("w-artifact-contract", nil)
	taskID, _ := DeriveTaskID("r-artifact-contract", "producer")

	if _, err := sched.Schedule(
		context.Background(),
		"w-artifact-contract",
		"r-artifact-contract",
		[]NodeDef{{ID: "producer", Title: "Producer", ProducesSlotIDs: []string{"result"}}},
		nil,
		1,
		nil,
		nil,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	projection, err := authority.LoadV2Projection()
	if err != nil {
		t.Fatal(err)
	}
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime == nil || runtime.State != TaskFailedRetryable ||
		!strings.Contains(runtime.Error, "cannot report declared artifact outputs") {
		t.Fatalf("runtime = %+v, want explicit retryable artifact failure", runtime)
	}
}

func TestV2Scheduler_ReceiptGuardWaitingApproval(t *testing.T) {
	exec := newFakeV2Executor()
	taskID, _ := DeriveTaskID("r1", "a")
	exec.results[taskID] = func() (*Attempt, error) {
		return &Attempt{State: RunCompleted, SideEffectClass: "read"}, nil
	}
	sched := NewV2Scheduler(exec)
	nodes := []NodeDef{{ID: "a", Title: "A", ToolHints: []string{"side_effect=external_write"}}}

	authority := newMemoryV2Authority("w1", nil)
	_, err := sched.Schedule(context.Background(), "w1", "r1", nodes, nil, 1, nil, nil, authority)
	if err != nil {
		t.Fatal(err)
	}
	var final TaskRuntimeUpdatedPayload
	for _, event := range authority.eventSnapshot() {
		if event.Type == EventTaskRuntimeUpdated {
			if err := json.Unmarshal(event.Payload, &final); err != nil {
				t.Fatal(err)
			}
		}
	}
	if final.State != TaskWaitingApproval || final.Runtime.ApprovalToken == "" {
		t.Fatalf("missing external receipt must require human takeover: %+v", final.Runtime)
	}
	if got := final.Runtime.Attempts[len(final.Runtime.Attempts)-1].SideEffectClass; got != "external_write" {
		t.Fatalf("executor risk downgrade bypassed frozen declaration: got %q", got)
	}
	if got := final.Runtime.SideEffectClass; got != "external_write" {
		t.Fatalf("runtime risk floor was not preserved: got %q", got)
	}
}

func TestV2Scheduler_ObservedRiskUpgradePersistsOnRuntime(t *testing.T) {
	exec := newFakeV2Executor()
	taskID, _ := DeriveTaskID("r-risk-upgrade", "a")
	exec.results[taskID] = func() (*Attempt, error) {
		return &Attempt{State: RunCompleted, SideEffectClass: "destructive"}, nil
	}
	authority := newMemoryV2Authority("w-risk-upgrade", nil)
	if _, err := NewV2Scheduler(exec).Schedule(
		context.Background(),
		"w-risk-upgrade",
		"r-risk-upgrade",
		[]NodeDef{{ID: "a"}},
		nil,
		1,
		nil,
		nil,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	projection, err := authority.LoadV2Projection()
	if err != nil {
		t.Fatal(err)
	}
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime == nil || runtime.SideEffectClass != "destructive" ||
		runtime.State != TaskWaitingApproval {
		t.Fatalf("observed destructive risk was not persisted as runtime floor: %+v", runtime)
	}
}

func TestV2Scheduler_StartsEntireReadySetConcurrently(t *testing.T) {
	exec := &blockingV2Executor{
		started: make(chan string, 2),
		release: make(chan struct{}),
	}
	sched := NewV2Scheduler(exec)
	authority := newMemoryV2Authority("w-parallel", nil)
	done := make(chan error, 1)
	go func() {
		_, err := sched.Schedule(
			context.Background(),
			"w-parallel",
			"r-parallel",
			[]NodeDef{{ID: "a"}, {ID: "b"}},
			nil,
			1,
			nil,
			nil,
			authority,
		)
		done <- err
	}()

	for range 2 {
		select {
		case <-exec.started:
		case <-time.After(2 * time.Second):
			t.Fatal("all ready nodes did not start concurrently")
		}
	}
	if got := exec.max.Load(); got != 2 {
		t.Fatalf("max parallel executions: got %d want 2", got)
	}
	close(exec.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestV2Scheduler_FileWorkStoreReadySetRunsConcurrently(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-file-parallel", "r-file-parallel"
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	authority, err := NewFileV2RuntimeAuthority(store, workID)
	if err != nil {
		t.Fatal(err)
	}
	exec := &blockingV2Executor{
		started: make(chan string, 2),
		release: make(chan struct{}),
	}
	sched := NewV2Scheduler(exec)
	done := make(chan error, 1)
	go func() {
		_, scheduleErr := sched.Schedule(
			context.Background(),
			workID,
			runID,
			[]NodeDef{{ID: "a"}, {ID: "b"}},
			nil,
			1,
			nil,
			nil,
			authority,
		)
		done <- scheduleErr
	}()
	for range 2 {
		select {
		case <-exec.started:
		case <-time.After(3 * time.Second):
			t.Fatal("FileWorkStore ready set did not start concurrently")
		}
	}
	close(exec.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	projection, err := authority.LoadV2Projection()
	if err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"a", "b"} {
		taskID, _ := DeriveTaskID(runID, nodeID)
		if runtime := projection.V2TaskRuntimes[taskID]; runtime == nil || runtime.State != TaskCompleted {
			t.Fatalf("parallel runtime %s not completed: %+v", nodeID, runtime)
		}
	}
}

func TestV2Scheduler_PerTaskSingleFlightAcrossConcurrentSchedules(t *testing.T) {
	exec := &blockingV2Executor{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	sched := NewV2Scheduler(exec)
	authority := newMemoryV2Authority("w-flight", nil)
	first := make(chan error, 1)
	go func() {
		_, err := sched.Schedule(
			context.Background(),
			"w-flight",
			"r-flight",
			[]NodeDef{{ID: "a"}},
			nil,
			1,
			nil,
			nil,
			authority,
		)
		first <- err
	}()
	select {
	case <-exec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first execution did not start")
	}
	otherScheduler := NewV2Scheduler(exec)
	second, err := otherScheduler.Schedule(
		context.Background(),
		"w-flight",
		"r-flight",
		[]NodeDef{{ID: "a"}},
		nil,
		1,
		nil,
		nil,
		authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Executed) != 0 {
		t.Fatalf("concurrent schedule must not duplicate execution: %v", second.Executed)
	}
	close(exec.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if got := exec.calls.Load(); got != 1 {
		t.Fatalf("executor calls: got %d want 1", got)
	}
}

func TestV2Scheduler_RestartExternalRunningRequiresHumanTakeover(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-recover-external", "r-recover-external"
	now := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	rt := V2NewTaskRuntime(workID, runID, "publish", 1, "external_write", now)
	emit := storeEventEmitter(store, workID)
	if err := emitRuntimeCreated(emit, rt, now); err != nil {
		t.Fatal(err)
	}
	if err := emitRuntimeUpdated(emit, rt, TaskReady, nil, now); err != nil {
		t.Fatal(err)
	}
	token := GenerateExecutionToken(rt.TaskID, 1, "inputs:none", "deps:none")
	attempt := V2Attempt{
		ID:               V2RunAttemptID(rt.TaskID, 0),
		RequestID:        runID + "/attempt/0",
		Index:            0,
		State:            TaskRunning,
		StartedAt:        now,
		DefinitionRev:    1,
		InputDigest:      "inputs:none",
		DependencyDigest: "deps:none",
		ExecutionToken:   token,
		SideEffectClass:  "external_write",
	}
	if err := emitRuntimeUpdated(emit, rt, TaskRunning, &attempt, now); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileWorkStore(store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := reopened.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	authority, err := NewFileV2RuntimeAuthority(reopened, workID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sched.Schedule(
		context.Background(),
		workID,
		runID,
		[]NodeDef{{ID: "publish", ToolHints: []string{"side_effect=external_write"}}},
		projection.V2TaskRuntimes,
		1,
		nil,
		nil,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	if exec.callCount() != 0 {
		t.Fatalf("external/destructive running attempt must not replay, calls=%d", exec.callCount())
	}
	replayed, err := reopened.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	stored := replayed.V2TaskRuntimes[rt.TaskID]
	if stored.State != TaskWaitingApproval || stored.ApprovalToken == "" ||
		len(stored.Attempts) != 1 || stored.Attempts[0].State != TaskWaitingApproval {
		t.Fatalf("restart takeover projection: %+v", stored)
	}
}

func TestV2Scheduler_RestartReadRunningRetriesSafely(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-recover-read", "r-recover-read"
	now := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	rt := V2NewTaskRuntime(workID, runID, "inspect", 1, "read", now)
	emit := storeEventEmitter(store, workID)
	if err := emitRuntimeCreated(emit, rt, now); err != nil {
		t.Fatal(err)
	}
	if err := emitRuntimeUpdated(emit, rt, TaskReady, nil, now); err != nil {
		t.Fatal(err)
	}
	attempt := V2Attempt{
		ID:               V2RunAttemptID(rt.TaskID, 0),
		RequestID:        runID + "/attempt/0",
		Index:            0,
		State:            TaskRunning,
		StartedAt:        now,
		DefinitionRev:    1,
		InputDigest:      "inputs:none",
		DependencyDigest: "deps:none",
		ExecutionToken:   GenerateExecutionToken(rt.TaskID, 1, "inputs:none", "deps:none"),
		SideEffectClass:  "read",
	}
	if err := emitRuntimeUpdated(emit, rt, TaskRunning, &attempt, now); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileWorkStore(store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := reopened.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	authority, err := NewFileV2RuntimeAuthority(reopened, workID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sched.Schedule(
		context.Background(),
		workID,
		runID,
		[]NodeDef{{ID: "inspect"}},
		projection.V2TaskRuntimes,
		1,
		nil,
		nil,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	if exec.callCount() != 1 {
		t.Fatalf("safe interrupted attempt should retry once, calls=%d", exec.callCount())
	}
	replayed, err := reopened.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	stored := replayed.V2TaskRuntimes[rt.TaskID]
	if stored.State != TaskCompleted || len(stored.Attempts) != 2 {
		t.Fatalf("safe retry projection: %+v", stored)
	}
}

func TestV2Scheduler_AuthoritativeRefreshProducesStaleResult(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID, nodeID := "w-live-stale", "r-live-stale", "render"
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	authority, err := NewFileV2RuntimeAuthority(store, workID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := DeriveTaskID(runID, nodeID)
	exec := newFakeV2Executor()
	exec.results[taskID] = func() (*Attempt, error) {
		projection, loadErr := authority.LoadV2Projection()
		if loadErr != nil {
			return nil, loadErr
		}
		runtime := projection.V2TaskRuntimes[taskID]
		updateErr := updateRuntime(
			authority.CommitV2Event,
			runtime,
			TaskRunning,
			nil,
			now.Add(time.Second),
			func(next *V2TaskRuntime) {
				next.DefinitionRev = 2
				next.InputDigest = "sha256:new-input"
				next.DependencyDigest = "sha256:new-dependency"
				next.ExecutionToken = GenerateExecutionToken(
					next.TaskID,
					next.DefinitionRev,
					next.InputDigest,
					next.DependencyDigest,
				)
			},
		)
		if updateErr != nil {
			return nil, updateErr
		}
		return &Attempt{
			State:      RunCompleted,
			SessionRef: SessionRef{SessionPath: "sessions/stale-result.jsonl"},
		}, nil
	}

	sched := NewV2Scheduler(exec)
	if _, err := sched.Schedule(
		context.Background(),
		workID,
		runID,
		[]NodeDef{{ID: nodeID}},
		nil,
		1,
		nil,
		nil,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	projection, err := authority.LoadV2Projection()
	if err != nil {
		t.Fatal(err)
	}
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime.State != TaskRunning || runtime.DefinitionRev != 2 || len(runtime.Attempts) != 1 {
		t.Fatalf("stale completion overwrote authoritative runtime: %+v", runtime)
	}
	attempt := runtime.Attempts[0]
	if !attempt.StaleResult || attempt.ResultRef != "sessions/stale-result.jsonl" {
		t.Fatalf("stale evidence was not preserved: %+v", attempt)
	}
}

func TestV2Scheduler_WakeAndScheduleAffected(t *testing.T) {
	cases := []struct {
		name  string
		cause V2WakeCause
		state TaskStateV2
	}{
		{name: "input", cause: V2WakeInput, state: TaskWaitingInput},
		{name: "approval", cause: V2WakeApproval, state: TaskWaitingApproval},
		{name: "patch", cause: V2WakePatch, state: TaskWaitingInput},
		{name: "definition", cause: V2WakeDefinition, state: TaskWaitingInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestFileWorkStore(t)
			workID := "w-wake-" + tc.name
			runID := "r-wake-" + tc.name
			now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
			if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
				t.Fatal(err)
			}
			rt := V2NewTaskRuntime(workID, runID, "a", 1, "read", now)
			emit := storeEventEmitter(store, workID)
			if err := emitRuntimeCreated(emit, rt, now); err != nil {
				t.Fatal(err)
			}
			if err := updateRuntime(emit, rt, tc.state, nil, now, func(next *V2TaskRuntime) {
				if tc.state == TaskWaitingInput {
					next.WaitingInputIDs = []string{"input-a"}
				} else {
					next.ApprovalToken = "approval-a"
				}
			}); err != nil {
				t.Fatal(err)
			}
			authority, err := NewFileV2RuntimeAuthority(store, workID)
			if err != nil {
				t.Fatal(err)
			}
			exec := newFakeV2Executor()
			sched := NewV2Scheduler(exec)
			nodes := []NodeDef{
				{ID: "a", InputSpecIDs: []string{"spec-a"}},
				{ID: "b"},
				{ID: "c", DependsOn: []string{"a"}},
			}
			inputs := []WorkInput{{
				ID: "input-a", WorkID: workID, RunID: runID, TaskID: rt.TaskID,
				SpecID: "spec-a", State: InputSubmitted, Value: json.RawMessage(`"ok"`), Revision: 1,
			}}
			specs := []InputSpec{{ID: "spec-a", Kind: InputText, Required: true}}
			if _, err := sched.WakeAndScheduleAffected(
				context.Background(),
				workID,
				runID,
				nodes,
				1,
				inputs,
				specs,
				[]string{rt.TaskID},
				tc.cause,
				authority,
			); err != nil {
				t.Fatal(err)
			}
			aID, _ := DeriveTaskID(runID, "a")
			cID, _ := DeriveTaskID(runID, "c")
			if got := exec.callIDs(); !reflect.DeepEqual(got, []string{aID, cID}) {
				t.Fatalf("wake scanned/executed outside affected subgraph: got %v want [%s %s]", got, aID, cID)
			}
			projection, err := authority.LoadV2Projection()
			if err != nil {
				t.Fatal(err)
			}
			bID, _ := DeriveTaskID(runID, "b")
			if projection.V2TaskRuntimes[bID] != nil {
				t.Fatalf("unrelated node was materialized: %+v", projection.V2TaskRuntimes[bID])
			}
		})
	}
}

func TestV2Scheduler_StructuralWakeDoesNotBypassApproval(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-wake-approval-safe", "r-wake-approval-safe"
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	rt := V2NewTaskRuntime(workID, runID, "publish", 1, "external_write", now)
	emit := storeEventEmitter(store, workID)
	if err := emitRuntimeCreated(emit, rt, now); err != nil {
		t.Fatal(err)
	}
	if err := updateRuntime(emit, rt, TaskWaitingApproval, nil, now, func(next *V2TaskRuntime) {
		next.ApprovalToken = "approval-required"
	}); err != nil {
		t.Fatal(err)
	}
	authority, err := NewFileV2RuntimeAuthority(store, workID)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	if _, err := sched.WakeAndScheduleAffected(
		context.Background(),
		workID,
		runID,
		[]NodeDef{{ID: "publish", ToolHints: []string{"side_effect=external_write"}}},
		1,
		nil,
		nil,
		[]string{rt.TaskID},
		V2WakePatch,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	if exec.callCount() != 0 {
		t.Fatalf("patch wake bypassed approval, calls=%d", exec.callCount())
	}
	projection, err := authority.LoadV2Projection()
	if err != nil {
		t.Fatal(err)
	}
	if projection.V2TaskRuntimes[rt.TaskID].State != TaskWaitingApproval {
		t.Fatalf("approval state changed: %+v", projection.V2TaskRuntimes[rt.TaskID])
	}
}

func TestUpdateRuntime_EmitterFailureLeavesRuntimeUntouched(t *testing.T) {
	now := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	rt := V2NewTaskRuntime("w-zero", "r-zero", "n-zero", 1, "read", now)
	before := cloneV2Runtime(rt)
	errBoom := errors.New("injected append failure")
	err := emitRuntimeUpdated(
		func(WorkEvent) (int64, error) { return 0, errBoom },
		rt,
		TaskReady,
		nil,
		now.Add(time.Second),
	)
	if !errors.Is(err, errBoom) {
		t.Fatalf("got %v want injected failure", err)
	}
	if !reflect.DeepEqual(rt, before) {
		t.Fatalf("failed preflight polluted runtime:\n got %+v\nwant %+v", rt, before)
	}
}

func TestFileWorkStore_RuntimePreflightFailureDoesNotAppend(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-preflight-zero", "r-preflight-zero"
	now := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	before, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatal(err)
	}
	rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)
	rt.State = TaskReady
	rt.Revision = 7 // invalid: no runtime exists and the first update must be revision 1.
	payload := mustMarshalV2(TaskRuntimeUpdatedPayload{
		TaskID: rt.TaskID, WorkID: workID, RunID: runID,
		ExpectedRevision: 1, State: TaskReady, Runtime: *rt,
	})
	_, err = store.CommitEvent(workID, WorkEvent{
		SchemaVersion: SchemaVersionV2,
		ID:            "bad-runtime-update",
		Type:          EventTaskRuntimeUpdated,
		WorkID:        workID,
		RequestID:     "bad-runtime-update",
		Payload:       payload,
		Object: ObjectContext{
			Kind: ObjectTask, WorkID: workID, ID: rt.TaskID,
			RunID: runID, TaskID: rt.TaskID, ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1),
		},
	})
	if err == nil {
		t.Fatal("invalid runtime revision must fail preflight")
	}
	after, loadErr := store.LoadManifest(workID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	projection, projectionErr := store.LoadProjection(workID)
	if projectionErr != nil {
		t.Fatal(projectionErr)
	}
	if after.Revision != before.Revision || len(projection.V2TaskRuntimes) != 0 {
		t.Fatalf("failed preflight polluted store: before=%d after=%d runtimes=%d", before.Revision, after.Revision, len(projection.V2TaskRuntimes))
	}
}

func TestFileWorkStore_RuntimeWriteExpectedRevisionPreflight(t *testing.T) {
	t.Run("runtime_created", func(t *testing.T) {
		store := newTestFileWorkStore(t)
		workID, runID := "w-created-expected", "r-created-expected"
		now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
		if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
			t.Fatal(err)
		}
		before, _ := store.LoadManifest(workID)
		rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)
		payload := mustMarshalV2(TaskRuntimeCreatedPayload{
			TaskID: rt.TaskID, WorkID: workID, RunID: runID, NodeID: "n1",
			ExpectedRevision: 1, DefinitionRev: 1, SideEffectClass: "read", Runtime: *rt,
		})
		_, err := store.CommitEvent(workID, WorkEvent{
			SchemaVersion: SchemaVersionV2,
			ID:            "bad-created-expected",
			Type:          EventTaskRuntimeCreated,
			WorkID:        workID,
			RequestID:     "bad-created-expected",
			Payload:       payload,
			Object: ObjectContext{
				Kind: ObjectTask, WorkID: workID, ID: rt.TaskID,
				RunID: runID, TaskID: rt.TaskID,
				ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1),
			},
		})
		if err == nil {
			t.Fatal("runtime_created with non-zero expectedRevision must fail")
		}
		after, _ := store.LoadManifest(workID)
		projection, loadErr := store.LoadProjection(workID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if after.Revision != before.Revision || len(projection.V2TaskRuntimes) != 0 {
			t.Fatalf("created preflight polluted store: before=%d after=%d", before.Revision, after.Revision)
		}
	})

	t.Run("stale_result", func(t *testing.T) {
		store := newTestFileWorkStore(t)
		workID, runID := "w-stale-expected", "r-stale-expected"
		now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
		if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
			t.Fatal(err)
		}
		rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)
		emit := storeEventEmitter(store, workID)
		if err := emitRuntimeCreated(emit, rt, now); err != nil {
			t.Fatal(err)
		}
		if err := emitRuntimeUpdated(emit, rt, TaskReady, nil, now); err != nil {
			t.Fatal(err)
		}
		attempt := V2Attempt{
			ID: V2RunAttemptID(rt.TaskID, 0), Index: 0, State: TaskRunning, StartedAt: now,
			DefinitionRev: 1, InputDigest: "inputs:none", DependencyDigest: "deps:none",
			ExecutionToken: "old-token", SideEffectClass: "read",
		}
		if err := emitRuntimeUpdated(emit, rt, TaskRunning, &attempt, now); err != nil {
			t.Fatal(err)
		}
		before, _ := store.LoadManifest(workID)
		wrongExpected := rt.Revision + 1
		payload := mustMarshalV2(TaskStaleResultPayload{
			TaskID: rt.TaskID, WorkID: workID, RunID: runID,
			ExpectedRevision: wrongExpected, AttemptID: attempt.ID,
			StaleToken: "old-token", CurrentToken: "new-token", ResultRef: "stale-ref",
		})
		_, err := store.CommitEvent(workID, WorkEvent{
			SchemaVersion: SchemaVersionV2,
			ID:            "bad-stale-expected",
			Type:          EventTaskStaleResult,
			WorkID:        workID,
			RequestID:     "bad-stale-expected",
			Payload:       payload,
			Object: ObjectContext{
				Kind: ObjectTask, WorkID: workID, ID: rt.TaskID,
				RunID: runID, TaskID: rt.TaskID,
				ExpectedRevision: int64Ptr(wrongExpected), DefinitionRevision: int64Ptr(1),
			},
		})
		if err == nil {
			t.Fatal("stale_result with wrong expectedRevision must fail")
		}
		after, _ := store.LoadManifest(workID)
		projection, loadErr := store.LoadProjection(workID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if after.Revision != before.Revision ||
			projection.V2TaskRuntimes[rt.TaskID].Attempts[0].StaleResult {
			t.Fatalf("stale preflight polluted store: before=%d after=%d", before.Revision, after.Revision)
		}
	})
}

func TestFileWorkStore_RuntimeWriteContextMismatchDoesNotAppend(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-runtime-context", "r-runtime-context"
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)
	before, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatal(err)
	}
	payload := mustMarshalV2(TaskRuntimeCreatedPayload{
		TaskID: rt.TaskID, WorkID: workID, RunID: runID, NodeID: rt.NodeID,
		ExpectedRevision: 0, DefinitionRev: 1, SideEffectClass: "read", Runtime: *rt,
	})
	_, err = store.CommitEvent(workID, WorkEvent{
		SchemaVersion: SchemaVersionV2,
		ID:            "runtime-context-mismatch",
		Type:          EventTaskRuntimeCreated,
		WorkID:        workID,
		RequestID:     "runtime-context-mismatch",
		Payload:       payload,
		Object: ObjectContext{
			Kind: ObjectTask, WorkID: workID, ID: rt.TaskID,
			RunID: runID, TaskID: rt.TaskID,
			ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1),
		},
	})
	if err == nil {
		t.Fatal("payload/context expectedRevision mismatch must fail")
	}
	after, loadErr := store.LoadManifest(workID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	projection, projectionErr := store.LoadProjection(workID)
	if projectionErr != nil {
		t.Fatal(projectionErr)
	}
	if after.Revision != before.Revision || len(projection.V2TaskRuntimes) != 0 {
		t.Fatalf("context mismatch polluted store: before=%d after=%d runtimes=%d",
			before.Revision, after.Revision, len(projection.V2TaskRuntimes))
	}
}

func TestFileWorkStore_RuntimeUpdateRequiresCreatedRuntime(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID, runID := "w-runtime-sequence", "r-runtime-sequence"
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	if err := createMinimalV2WorkStore(t, store, workID, now); err != nil {
		t.Fatal(err)
	}
	rt := V2NewTaskRuntime(workID, runID, "n1", 1, "read", now)
	rt.State = TaskReady
	rt.Revision = 2
	before, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatal(err)
	}
	payload := mustMarshalV2(TaskRuntimeUpdatedPayload{
		TaskID: rt.TaskID, WorkID: workID, RunID: runID,
		ExpectedRevision: 1, State: TaskReady, Runtime: *rt,
	})
	_, err = store.CommitEvent(workID, WorkEvent{
		SchemaVersion: SchemaVersionV2,
		ID:            "runtime-update-without-create",
		Type:          EventTaskRuntimeUpdated,
		WorkID:        workID,
		RequestID:     "runtime-update-without-create",
		Payload:       payload,
		Object: ObjectContext{
			Kind: ObjectTask, WorkID: workID, ID: rt.TaskID,
			RunID: runID, TaskID: rt.TaskID,
			ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1),
		},
	})
	if err == nil {
		t.Fatal("runtime_updated without runtime_created must fail")
	}
	after, loadErr := store.LoadManifest(workID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	projection, projectionErr := store.LoadProjection(workID)
	if projectionErr != nil {
		t.Fatal(projectionErr)
	}
	if after.Revision != before.Revision || len(projection.V2TaskRuntimes) != 0 {
		t.Fatalf("invalid runtime sequence polluted store: before=%d after=%d runtimes=%d",
			before.Revision, after.Revision, len(projection.V2TaskRuntimes))
	}
}

func TestEvaluateAffectedReadySet_OnlyReturnsChangedSubgraph(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c"},
	}
	runtimes := map[string]*V2TaskRuntime{
		"a": {NodeID: "a", State: TaskCompleted},
	}
	result := EvaluateAffectedReadySet(nodes, runtimes, []string{"a"})
	if !reflect.DeepEqual(result.Ready, []string{"b"}) {
		t.Fatalf("affected ready set: got %v want [b]", result.Ready)
	}
}

func TestEvaluateAffectedReadySet_EmptySeedsDoesNotScheduleWholeDAG(t *testing.T) {
	nodes := []NodeDef{{ID: "a"}, {ID: "b"}}
	result := EvaluateAffectedReadySet(nodes, nil, nil)
	if len(result.Ready) != 0 || len(result.Blocked) != 0 || result.HasGlobalBlock {
		t.Fatalf("empty affected seed set must be a no-op: %+v", result)
	}
}

// ── Task ID stability ──────────────────────────────────────────────────────

func TestTaskID_StableAcrossRestarts_ReadySet(t *testing.T) {
	id1, _ := DeriveTaskID("run-stable", "build-docs")
	id2, _ := DeriveTaskID("run-stable", "build-docs")
	if id1 != id2 {
		t.Fatalf("task ID should be stable: %s vs %s", id1, id2)
	}
}

// ── ValidateStaleCompletion ─────────────────────────────────────────────────

func TestValidateStaleCompletion_TokenMatch(t *testing.T) {
	att := &V2Attempt{
		DefinitionRev: 3, InputDigest: "input-a", DependencyDigest: "dep-a", ExecutionToken: "tok1",
	}
	current := DefTokenSet{
		DefinitionRev: 3, InputDigest: "input-a", DependencyDigest: "dep-a", ExecutionToken: "tok1",
	}
	if ValidateStaleCompletion(att, current) {
		t.Fatal("matching token should not be stale")
	}
}

func TestValidateStaleCompletion_TokenMismatch(t *testing.T) {
	att := &V2Attempt{
		DefinitionRev: 3, InputDigest: "input-a", DependencyDigest: "dep-a", ExecutionToken: "tok1",
	}
	current := DefTokenSet{
		DefinitionRev: 3, InputDigest: "input-a", DependencyDigest: "dep-a", ExecutionToken: "tok2",
	}
	if !ValidateStaleCompletion(att, current) {
		t.Fatal("mismatched token should be stale")
	}
}

func TestValidateStaleCompletion_RevisionAndDigestMismatch(t *testing.T) {
	base := &V2Attempt{
		DefinitionRev: 3, InputDigest: "input-a", DependencyDigest: "dep-a", ExecutionToken: "tok1",
	}
	cases := []DefTokenSet{
		{DefinitionRev: 4, InputDigest: "input-a", DependencyDigest: "dep-a", ExecutionToken: "tok1"},
		{DefinitionRev: 3, InputDigest: "input-b", DependencyDigest: "dep-a", ExecutionToken: "tok1"},
		{DefinitionRev: 3, InputDigest: "input-a", DependencyDigest: "dep-b", ExecutionToken: "tok1"},
	}
	for _, current := range cases {
		if !ValidateStaleCompletion(base, current) {
			t.Fatalf("mismatched completion context must be stale: %+v", current)
		}
	}
}

// ── V2 node prompt includes submitted inputs ─────────────────────────────────

func TestV2NodePrompt_IncludesSubmittedInput(t *testing.T) {
	node := &NodeDef{
		ID:           "story",
		Title:        "Write a story",
		Description:  "Write a short story based on the provided theme.",
		InputSpecIDs: []string{"theme"},
	}
	inputs := []WorkInput{{
		ID: "in-1", WorkID: "w", RunID: "r", TaskID: "t",
		SpecID: "theme", Value: json.RawMessage(`"鹦鹉和猴子"`),
		State: InputSubmitted, Revision: 1,
	}}
	specs := []InputSpec{
		{ID: "theme", Label: "故事主题", Kind: InputText, Required: true},
	}

	prompt := v2NodePrompt(node, inputs, specs, "w", "r", "t")

	if !strings.Contains(prompt, "鹦鹉和猴子") {
		t.Fatalf("prompt missing submitted value: %s", prompt)
	}
	if !strings.Contains(prompt, "故事主题") {
		t.Fatalf("prompt missing spec label: %s", prompt)
	}
	if !strings.Contains(prompt, "theme") {
		t.Fatalf("prompt missing spec id: %s", prompt)
	}
	if !strings.Contains(prompt, "text") {
		t.Fatalf("prompt missing spec kind: %s", prompt)
	}
}

func TestV2NodePrompt_IsolatesOtherRun(t *testing.T) {
	node := &NodeDef{
		ID:           "story",
		Title:        "Write a story",
		InputSpecIDs: []string{"theme"},
	}
	// Same WorkID but different RunID — must not leak.
	inputs := []WorkInput{{
		ID: "in-1", WorkID: "w", RunID: "other-run", TaskID: "t",
		SpecID: "theme", Value: json.RawMessage(`"入侵者"`),
		State: InputSubmitted, Revision: 1,
	}}
	specs := []InputSpec{{ID: "theme", Label: "主题", Kind: InputText}}

	prompt := v2NodePrompt(node, inputs, specs, "w", "r", "t")

	if strings.Contains(prompt, "入侵者") {
		t.Fatalf("other-run value leaked into prompt: %s", prompt)
	}
}

func TestV2NodePrompt_IsolatesOtherTask(t *testing.T) {
	node := &NodeDef{
		ID:           "story",
		Title:        "Write a story",
		InputSpecIDs: []string{"theme"},
	}
	// Same WorkID/RunID but different TaskID — must not leak.
	inputs := []WorkInput{{
		ID: "in-1", WorkID: "w", RunID: "r", TaskID: "other-task",
		SpecID: "theme", Value: json.RawMessage(`"入侵者"`),
		State: InputSubmitted, Revision: 1,
	}}
	specs := []InputSpec{{ID: "theme", Label: "主题", Kind: InputText}}

	prompt := v2NodePrompt(node, inputs, specs, "w", "r", "t")

	if strings.Contains(prompt, "入侵者") {
		t.Fatalf("other-task value leaked into prompt: %s", prompt)
	}
}

func TestV2NodePrompt_ExcludesUndeclaredSpec(t *testing.T) {
	node := &NodeDef{
		ID:           "story",
		Title:        "Write a story",
		InputSpecIDs: []string{"theme"},
	}
	// Input with SpecID "background" is not declared — must not appear.
	inputs := []WorkInput{
		{ID: "in-1", WorkID: "w", RunID: "r", TaskID: "t",
			SpecID: "theme", Value: json.RawMessage(`"鹦鹉"`),
			State: InputSubmitted, Revision: 1},
		{ID: "in-2", WorkID: "w", RunID: "r", TaskID: "t",
			SpecID: "background", Value: json.RawMessage(`"秘密"`),
			State: InputSubmitted, Revision: 1},
	}
	specs := []InputSpec{
		{ID: "theme", Label: "主题", Kind: InputText},
		{ID: "background", Label: "背景", Kind: InputText},
	}

	prompt := v2NodePrompt(node, inputs, specs, "w", "r", "t")

	if !strings.Contains(prompt, "鹦鹉") {
		t.Fatalf("prompt missing declared input: %s", prompt)
	}
	if strings.Contains(prompt, "秘密") {
		t.Fatalf("undeclared spec value leaked into prompt: %s", prompt)
	}
}

func TestV2NodePrompt_ExcludesNonSubmitted(t *testing.T) {
	node := &NodeDef{
		ID:           "story",
		Title:        "Write a story",
		InputSpecIDs: []string{"theme"},
	}
	inputs := []WorkInput{
		{ID: "in-1", WorkID: "w", RunID: "r", TaskID: "t",
			SpecID: "theme", Value: json.RawMessage(`"已提交"`),
			State: InputSubmitted, Revision: 1},
		{ID: "in-2", WorkID: "w", RunID: "r", TaskID: "t",
			SpecID: "theme", Value: json.RawMessage(`"草稿"`),
			State: InputDraft, Revision: 1},
	}
	specs := []InputSpec{{ID: "theme", Label: "主题", Kind: InputText}}

	prompt := v2NodePrompt(node, inputs, specs, "w", "r", "t")

	if !strings.Contains(prompt, "已提交") {
		t.Fatalf("submitted input missing: %s", prompt)
	}
	if strings.Contains(prompt, "草稿") {
		t.Fatalf("draft input leaked: %s", prompt)
	}
}

func TestV2NodePrompt_DeterministicOrder(t *testing.T) {
	node := &NodeDef{
		ID:           "review",
		Title:        "Review",
		InputSpecIDs: []string{"a", "b"},
	}
	inputs := []WorkInput{
		{ID: "i-b", WorkID: "w", RunID: "r", TaskID: "t",
			SpecID: "b", Value: json.RawMessage(`"beta"`),
			State: InputSubmitted, Revision: 1},
		{ID: "i-a", WorkID: "w", RunID: "r", TaskID: "t",
			SpecID: "a", Value: json.RawMessage(`"alpha"`),
			State: InputSubmitted, Revision: 1},
	}
	specs := []InputSpec{
		{ID: "a", Label: "A", Kind: InputText},
		{ID: "b", Label: "B", Kind: InputText},
	}

	p1 := v2NodePrompt(node, inputs, specs, "w", "r", "t")
	p2 := v2NodePrompt(node, inputs, specs, "w", "r", "t")

	if p1 != p2 {
		t.Fatalf("prompt not deterministic:\n---1---\n%s\n---2---\n%s", p1, p2)
	}

	posA := strings.Index(p1, "alpha")
	posB := strings.Index(p1, "beta")
	if posA < 0 || posB < 0 || posA > posB {
		t.Fatalf("spec order not respected: a(alpha) at %d, b(beta) at %d\n%s", posA, posB, p1)
	}
}

// ── V2 scheduler end-to-end: prompt carries submitted inputs ────────────────

func TestV2Scheduler_PromptCarriesSubmittedInputE2E(t *testing.T) {
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	nodes := []NodeDef{{
		ID:              "story",
		Title:           "Write a story",
		InputSpecIDs:    []string{"theme", "style"},
		ProducesSlotIDs: []string{"story_text"},
	}}
	runID := "r-e2e-prompt"
	taskID, _ := DeriveTaskID(runID, "story")
	inputs := []WorkInput{
		{ID: "in-theme", WorkID: "w-e2e", RunID: runID, TaskID: taskID,
			SpecID: "theme", Value: json.RawMessage(`"鹦鹉和猴子"`),
			State: InputSubmitted, Revision: 1},
		{ID: "in-style", WorkID: "w-e2e", RunID: runID, TaskID: taskID,
			SpecID: "style", Value: json.RawMessage(`"寓言"`),
			State: InputAccepted, Revision: 1},
	}
	specs := []InputSpec{
		{ID: "theme", Label: "故事主题", Kind: InputText, Required: true},
		{ID: "style", Label: "风格", Kind: InputText, Required: true},
	}

	authority := newMemoryV2Authority("w-e2e", nil)
	if _, err := sched.Schedule(
		context.Background(), "w-e2e", runID, nodes, nil, 1, inputs, specs, authority,
	); err != nil {
		t.Fatal(err)
	}

	captured := exec.calls
	var storyCall *TaskExecuteInput
	for i := range captured {
		if captured[i].TaskID == taskID {
			storyCall = &captured[i]
			break
		}
	}
	if storyCall == nil {
		t.Fatal("story node was never executed")
	}

	if !strings.Contains(storyCall.Prompt, "鹦鹉和猴子") {
		t.Fatalf("E2E prompt missing theme value: %s", storyCall.Prompt)
	}
	if !strings.Contains(storyCall.Prompt, "寓言") {
		t.Fatalf("E2E prompt missing style value: %s", storyCall.Prompt)
	}
	if !strings.Contains(storyCall.Prompt, "故事主题") {
		t.Fatalf("E2E prompt missing spec label: %s", storyCall.Prompt)
	}
	if !strings.Contains(storyCall.Prompt, "story_text") {
		t.Fatalf("E2E prompt missing artifact slot hint: %s", storyCall.Prompt)
	}
}

func TestV2Scheduler_PromptIsolatesOtherTaskInputs(t *testing.T) {
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	runID := "r-isolate"
	storyTask, _ := DeriveTaskID(runID, "story")
	otherTask, _ := DeriveTaskID(runID, "other")
	nodes := []NodeDef{
		{ID: "story", InputSpecIDs: []string{"theme"}},
		{ID: "other", InputSpecIDs: []string{"data"}},
	}
	// Both tasks have submitted inputs — story must only see its own.
	inputs := []WorkInput{
		{ID: "i-story", WorkID: "w-iso", RunID: runID, TaskID: storyTask,
			SpecID: "theme", Value: json.RawMessage(`"鹦鹉"`),
			State: InputSubmitted, Revision: 1},
		{ID: "i-other", WorkID: "w-iso", RunID: runID, TaskID: otherTask,
			SpecID: "data", Value: json.RawMessage(`"秘密数据"`),
			State: InputSubmitted, Revision: 1},
	}
	specs := []InputSpec{
		{ID: "theme", Label: "主题", Kind: InputText, Required: true},
		{ID: "data", Label: "数据", Kind: InputText, Required: true},
	}

	authority := newMemoryV2Authority("w-iso", nil)
	if _, err := sched.Schedule(
		context.Background(), "w-iso", runID, nodes, nil, 1, inputs, specs, authority,
	); err != nil {
		t.Fatal(err)
	}

	for _, call := range exec.calls {
		if call.TaskID == storyTask {
			if strings.Contains(call.Prompt, "秘密数据") {
				t.Fatalf("other task's input leaked into story prompt: %s", call.Prompt)
			}
		}
	}
}

func TestV2Scheduler_PromptStableOnRerun(t *testing.T) {
	exec := newFakeV2Executor()
	sched := NewV2Scheduler(exec)
	runID := "r-stable"
	taskID, _ := DeriveTaskID(runID, "story")
	nodes := []NodeDef{{
		ID:           "story",
		Title:        "Write a story",
		InputSpecIDs: []string{"theme", "style"},
	}}
	inputs := []WorkInput{
		{ID: "i-t", WorkID: "w-stable", RunID: runID, TaskID: taskID,
			SpecID: "theme", Value: json.RawMessage(`"鹦鹉"`),
			State: InputSubmitted, Revision: 1},
		{ID: "i-s", WorkID: "w-stable", RunID: runID, TaskID: taskID,
			SpecID: "style", Value: json.RawMessage(`"寓言"`),
			State: InputSubmitted, Revision: 1},
	}
	specs := []InputSpec{
		{ID: "theme", Label: "主题", Kind: InputText, Required: true},
		{ID: "style", Label: "风格", Kind: InputText, Required: true},
	}

	authority1 := newMemoryV2Authority("w-stable", nil)
	if _, err := sched.Schedule(
		context.Background(), "w-stable", runID, nodes, nil, 1, inputs, specs, authority1,
	); err != nil {
		t.Fatal(err)
	}

	prompt1 := exec.calls[0].Prompt

	// Second scheduler run with the same inputs (different authority to simulate separate run).
	exec2 := newFakeV2Executor()
	sched2 := NewV2Scheduler(exec2)
	authority2 := newMemoryV2Authority("w-stable", nil)
	if _, err := sched2.Schedule(
		context.Background(), "w-stable", runID, nodes, nil, 1, inputs, specs, authority2,
	); err != nil {
		t.Fatal(err)
	}

	prompt2 := exec2.calls[0].Prompt
	if prompt1 != prompt2 {
		t.Fatalf("prompt not deterministic across scheduler instances:\n---1---\n%s\n---2---\n%s", prompt1, prompt2)
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func storeEventEmitter(store *FileWorkStore, workID string) V2EventEmitter {
	return func(ev WorkEvent) (int64, error) {
		return store.CommitEvent(workID, ev)
	}
}

func createMinimalV2WorkStore(t *testing.T, store *FileWorkStore, workID string, now time.Time) error {
	t.Helper()
	w := &Work{
		SchemaVersion: SchemaVersionV2,
		ID:            workID,
		Name:          "Test Work",
		State:         WorkDraft,
		ArchiveState:  ArchiveActive,
		BlueprintRef:  BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return store.CreateWorkDir(CreateWorkDirInput{
		RequestID: workID + "/create",
		Work:      w,
	})
}
