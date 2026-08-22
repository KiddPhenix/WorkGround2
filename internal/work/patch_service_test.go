package work

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type patchPlannerFake struct {
	calls int
	last  PatchPlanInput
}

func (p *patchPlannerFake) PlanPatch(_ context.Context, in PatchPlanInput) (*PatchPlan, error) {
	p.calls++
	p.last = in
	switch in.Instruction {
	case "block-title":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "blocks/b1/title", NewValue: json.RawMessage(`"Updated block"`),
		}}}, nil
	case "derived-block-title":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "blocks/" + in.Block.ID + "/title", NewValue: json.RawMessage(`"Updated block"`),
		}}}, nil
	case "workflow-title":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "nodes/n1/title", NewValue: json.RawMessage(`"Updated node"`),
		}}}, nil
	case "workflow-slot":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "artifactSlots/report/title", NewValue: json.RawMessage(`"Final report"`),
		}}}, nil
	case "pending-workflow-title":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "nodes/n2/title", NewValue: json.RawMessage(`"Updated pending node"`),
		}}}, nil
	case "wrong-before":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "nodes/n1/title", OldValue: json.RawMessage(`"wrong"`),
			NewValue: json.RawMessage(`"Updated node"`),
		}}}, nil
	case "forbidden-path":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "permission/policy", NewValue: json.RawMessage(`"allow"`),
		}}}, nil
	case "forbidden-data":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "blocks/b1/data",
			NewValue: json.RawMessage(`{"secret":"do-not-store"}`),
		}}}, nil
	case "forbidden-runtime-data":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "blocks/b1/data",
			NewValue: json.RawMessage(`{"outer":{"RuNtImE":{"state":"done"}}}`),
		}}}, nil
	case "forbidden-default":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "inputSpecs/topic/defaultValue",
			NewValue: json.RawMessage(`{"outer":{"PermissionPolicy":{"mode":"allow"}}}`),
		}}}, nil
	case "forbidden-schema":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "inputSpecs/topic/valueSchema",
			NewValue: json.RawMessage(`{"properties":{"ACTION_RECEIPT":{"type":"string"}}}`),
		}}}, nil
	case "runtime-state":
		return &PatchPlan{Operations: []PatchOp{{
			Op: "replace", Path: "runs/run/tasks/n1/state", NewValue: json.RawMessage(`"completed"`),
		}}}, nil
	default:
		return nil, errors.New("unknown instruction")
	}
}

type patchClock struct{ now time.Time }

func (c *patchClock) Now() time.Time { return c.now }
func (c *patchClock) After(time.Duration) <-chan time.Time {
	return make(chan time.Time)
}

type patchFaultStore struct {
	*FileWorkStore
	mode   string
	failed bool
}

func (s *patchFaultStore) CommitEvents(workID string, events []WorkEvent) ([]int64, error) {
	if s.failed {
		return s.FileWorkStore.CommitEvents(workID, events)
	}
	s.failed = true
	switch s.mode {
	case "lease":
		workPath, err := s.workPath(workID)
		if err != nil {
			return nil, err
		}
		if err := AcquireWorkLease(workPath); err != nil {
			return nil, err
		}
		defer func() { _ = releaseStoreLease(workPath) }()
		return s.FileWorkStore.CommitEvents(workID, events)
	case "second-event":
		// Revision-chain tampering is no longer a failure: 60425c825 made the
		// store rebase service events onto the authoritative chain. Simulate a
		// batch that fails mid-way by making the second event rejected by the
		// reducer (invalid payload) so the atomic batch aborts.
		corrupt := append([]WorkEvent(nil), events...)
		if len(corrupt) < 2 {
			return nil, errors.New("test fault requires a multi-event batch")
		}
		corrupt[1].Payload = json.RawMessage(`{"broken":`)
		return s.FileWorkStore.CommitEvents(workID, corrupt)
	default:
		return nil, errors.New("unknown patch fault mode")
	}
}

func patchContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type patchHarness struct {
	dir     string
	store   *FileWorkStore
	service *PatchService
	planner *patchPlannerFake
	workID  string
	runID   string
}

func newPatchHarness(t *testing.T) *patchHarness {
	return newPatchHarnessConfig(t, true, true)
}

func newLegacyPatchHarness(t *testing.T) *patchHarness {
	return newPatchHarnessConfig(t, false, false)
}

func newPatchHarnessConfig(t *testing.T, declareBlock, seedBlock bool) *patchHarness {
	t.Helper()
	requireFileStoreIntegration(t)
	dir := t.TempDir()
	store, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	workService := NewService(store, nil, nil)
	ctx := context.Background()
	view, err := workService.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "planning-session", RequestID: "planning-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID
	now := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	def := &WorkDefinitionRevision{
		WorkID: workID, Revision: 2, ParentRevision: 1, Status: DefDraft,
		Goal: "deliver report",
		Nodes: []NodeDef{
			{
				ID: "n1", Title: "Research", InputSpecIDs: []string{"topic"},
				BlockIDs: []string{"b1"}, ProducesSlotIDs: []string{"report"},
			},
			{
				ID: "n2", Title: "Draft", DependsOn: []string{"n1"},
				ConsumesSlotIDs: []string{"report"},
			},
		},
		ArtifactSlots: []ArtifactSlotDef{{
			ID: "report", Title: "Report", Kind: "docx", ExpectedCount: 1, Required: true,
		}},
		InputSpecs: []InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: true,
			ValueSchema: json.RawMessage(`{"type":"string"}`), DefaultValue: json.RawMessage(`"initial"`),
		}},
		CreatedBy: "test", CreatedAt: now,
	}
	if !declareBlock {
		def.Nodes[0].BlockIDs = nil
	}
	def.Digest, err = ComputeV2RevisionDigest(def)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := workService.CreateCandidateRevision(ctx, workID, def,
		"candidate-"+t.Name(), view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = workService.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: candidate.Revision, ExpectedRevision: state.Revision,
		RequestID: "apply-def-" + t.Name(),
	}); err != nil {
		t.Fatal(err)
	}
	current, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Runs) == 0 {
		t.Fatal("definition apply did not create a run")
	}
	if seedBlock {
		block := BlockInstance{
			ID: "b1", Kind: "note", SchemaVersion: 1, Revision: 1,
			Title: "Original block", Status: BlockReady, Data: json.RawMessage(`{"value":"old"}`),
			Source:   BlockSource{Provider: "ai", Mode: "snapshot"},
			Fallback: BlockFallback{Summary: "Original block"}, CreatedAt: now, UpdatedAt: now,
		}
		payload, _ := json.Marshal(block)
		event := newServiceEvent(workID, "seed-block-"+t.Name(), EventBlockUpserted, payload, now)
		event.BaseRevision, event.Revision = state.Revision, state.Revision+1
		if _, err := store.CommitEvent(workID, event); err != nil {
			t.Fatal(err)
		}
	}
	planner := &patchPlannerFake{}
	patchService := NewPatchService(store)
	patchService.SetDefinitionStore(store)
	patchService.SetPlanner(planner)
	return &patchHarness{
		dir: dir, store: store, service: patchService, planner: planner,
		workID: workID, runID: current.Runs[len(current.Runs)-1].ID,
	}
}

func (h *patchHarness) derivedPreviewInput(instruction, requestID string) PreviewWorkPatchInput {
	current, _, _ := h.store.LoadState(h.workID, "")
	return PreviewWorkPatchInput{
		WorkID: h.workID, RunID: h.runID, TaskID: "n1",
		BlockID: V2DiscussionBlockID("n1"), BlockRevision: 1,
		SessionID: "discussion-session", Instruction: instruction,
		DefinitionRevision: current.V2CurrentRevision,
		Scope:              PatchBlock, RequestID: requestID,
	}
}

func (h *patchHarness) previewInput(t *testing.T, scope PatchScope, instruction, requestID string) PreviewWorkPatchInput {
	t.Helper()
	current, _, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	var blockRev int64
	for _, block := range current.Blocks {
		if block.ID == "b1" {
			blockRev = block.Revision
		}
	}
	return PreviewWorkPatchInput{
		WorkID: h.workID, RunID: h.runID, TaskID: "n1", BlockID: "b1",
		SessionID: "discussion-session", Instruction: instruction,
		DefinitionRevision: current.V2CurrentRevision, BlockRevision: blockRev,
		Scope: scope, RequestID: requestID,
	}
}

func compactAndRestartPatchStore(t *testing.T, store *FileWorkStore, workID, requestID string) (*FileWorkStore, PatchIntentReceipt) {
	t.Helper()
	current, _, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	before, ok := current.V2PatchReceipts[requestID]
	if !ok || !before.RequiresRerun {
		t.Fatalf("receipt before compact missing requiresRerun: %+v", before)
	}
	workPath, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	compactErr := CompactWorkEventLog(workPath, current, DefaultReducer())
	releaseErr := ReleaseWorkLease(workPath)
	if err := errors.Join(compactErr, releaseErr); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileWorkStore(store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	afterWork, _, err := reopened.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	after, ok := afterWork.V2PatchReceipts[requestID]
	if !ok || !reflect.DeepEqual(after, before) {
		t.Fatalf("receipt changed across compact/restart: before=%+v after=%+v", before, after)
	}
	return reopened, after
}

func TestPatchPreviewPlannerContextAndRestartReplay(t *testing.T) {
	h := newPatchHarness(t)
	input := h.previewInput(t, PatchWorkflow, "workflow-title", "preview-replay")
	before, _, _ := h.store.LoadState(h.workID, "")
	result, err := h.service.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if h.planner.calls != 1 || h.planner.last.Work == nil || h.planner.last.Definition == nil ||
		h.planner.last.Run == nil || h.planner.last.Task == nil || h.planner.last.Block == nil ||
		h.planner.last.SessionID != input.SessionID || h.planner.last.Instruction != input.Instruction ||
		h.planner.last.TargetNodeID != "n1" {
		t.Fatalf("planner did not receive complete context: %+v", h.planner.last)
	}
	if got := string(result.Preview.Operations[0].OldValue); got != `"Research"` {
		t.Fatalf("before=%s", got)
	}
	if !patchContains(result.Preview.AffectedNodeIDs, "n1") ||
		!patchContains(result.Preview.InvalidatedTaskIDs, "n2") || !result.Preview.RequiresRerun {
		t.Fatalf("impact=%+v", result.Preview)
	}
	if !patchContains(result.Preview.AffectedArtifactSlotIDs, "report") ||
		!patchContains(result.Preview.StaleArtifactSlotIDs, "report") {
		t.Fatalf("artifact impact=%+v", result.Preview)
	}
	def, _ := h.store.LoadRevision(h.workID, 2)
	if def.Nodes[0].Title != "Research" {
		t.Fatal("preview mutated definition")
	}
	after, _, _ := h.store.LoadState(h.workID, "")
	if before.V2CurrentRevision != after.V2CurrentRevision || before.Blocks[0].Revision != after.Blocks[0].Revision {
		t.Fatal("preview mutated business projection")
	}

	reopened, err := NewFileWorkStore(h.dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	planner := &patchPlannerFake{}
	restarted := NewPatchService(reopened)
	restarted.SetDefinitionStore(reopened)
	restarted.SetPlanner(planner)
	replay, err := restarted.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if planner.calls != 0 || replay.Revision != result.Revision ||
		replay.Preview.Digest != result.Preview.Digest || replay.Preview.ExpiresAt != result.Preview.ExpiresAt {
		t.Fatalf("preview replay changed result: first=%+v replay=%+v calls=%d", result, replay, planner.calls)
	}
}

func TestPatchPendingDefinitionTaskPreviewAndApply(t *testing.T) {
	h := newPatchHarness(t)
	current, _, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := DeriveTaskID(h.runID, "n2")
	if err != nil {
		t.Fatal(err)
	}
	if current.V2TaskRuntimes[taskID] != nil {
		t.Fatalf("fixture unexpectedly materialized pending runtime: %+v", current.V2TaskRuntimes[taskID])
	}
	input := PreviewWorkPatchInput{
		WorkID: h.workID, RunID: h.runID, TaskID: taskID,
		BlockID: V2DiscussionBlockID("n2"), BlockRevision: 1,
		SessionID: "discussion-session", Instruction: "pending-workflow-title",
		DefinitionRevision: current.V2CurrentRevision,
		Scope:              PatchWorkflow, RequestID: "preview-pending-definition-task",
	}

	preview, err := h.service.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if h.planner.last.TargetNodeID != "n2" || h.planner.last.Task == nil ||
		h.planner.last.Task.ID != taskID || h.planner.last.Task.Name != "n2" {
		t.Fatalf("planner pending task context=%+v", h.planner.last)
	}
	afterPreview, _, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range afterPreview.Blocks {
		if block.ID == input.BlockID {
			t.Fatalf("workflow preview persisted virtual discussion block: %+v", block)
		}
	}
	apply := ApplyWorkPatchInput{
		WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: PatchWorkflow, ExpectedRevision: preview.Revision, RequestID: "apply-pending-definition-task",
	}
	result, err := h.service.ApplyWorkPatch(context.Background(), apply)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.NewRevision != current.V2CurrentRevision+1 {
		t.Fatalf("pending task apply result=%+v", result)
	}
	applied, err := h.store.LoadRevision(h.workID, result.NewRevision)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Nodes[1].Title != "Updated pending node" {
		t.Fatalf("pending node title=%q", applied.Nodes[1].Title)
	}
	reopened, err := NewFileWorkStore(h.dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, _, err := reopened.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range reloaded.Blocks {
		if block.ID == input.BlockID {
			t.Fatalf("workflow apply persisted virtual discussion block: %+v", block)
		}
	}
	replay, err := h.service.ApplyWorkPatch(context.Background(), apply)
	if err != nil || !replay.Duplicate || replay.WorkRevision != result.WorkRevision {
		t.Fatalf("pending task apply replay=%+v err=%v", replay, err)
	}
}

func TestPatchPendingDefinitionTaskRejectsForgedIdentity(t *testing.T) {
	otherRunTaskID, err := DeriveTaskID("another-run", "n2")
	if err != nil {
		t.Fatal(err)
	}
	missingRunTaskID, err := DeriveTaskID("missing-run", "n2")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		runID  string
		taskID string
	}{
		{name: "forged task", taskID: "forged-task"},
		{name: "task derived for another run", taskID: otherRunTaskID},
		{name: "missing run", runID: "missing-run", taskID: missingRunTaskID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPatchHarness(t)
			current, before, err := h.store.LoadState(h.workID, "")
			if err != nil {
				t.Fatal(err)
			}
			runID := tc.runID
			if runID == "" {
				runID = h.runID
			}
			_, err = h.service.PreviewWorkPatch(context.Background(), PreviewWorkPatchInput{
				WorkID: h.workID, RunID: runID, TaskID: tc.taskID,
				BlockID: V2DiscussionBlockID("n2"), BlockRevision: 1,
				SessionID: "discussion-session", Instruction: "pending-workflow-title",
				DefinitionRevision: current.V2CurrentRevision,
				Scope:              PatchWorkflow, RequestID: "preview-" + strings.ReplaceAll(tc.name, " ", "-"),
			})
			if err == nil || (!strings.Contains(err.Error(), "task") && !strings.Contains(err.Error(), "run")) {
				t.Fatalf("expected identity rejection, got %v", err)
			}
			_, after, loadErr := h.store.LoadState(h.workID, "")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if h.planner.calls != 0 || after.Revision != before.Revision {
				t.Fatalf("invalid identity reached planner or mutated state: calls=%d before=%d after=%d",
					h.planner.calls, before.Revision, after.Revision)
			}
		})
	}
}

func TestPatchPreviewCollectionsEncodeAsArrays(t *testing.T) {
	h := newPatchHarness(t)
	result, err := h.service.PreviewWorkPatch(
		context.Background(),
		h.previewInput(t, PatchBlock, "block-title", "preview-array-contract"),
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.Preview)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"operations",
		"affectedNodeIds",
		"affectedBlockIds",
		"affectedArtifactSlotIds",
		"staleArtifactSlotIds",
		"invalidatedTaskIds",
	} {
		if _, ok := payload[field].([]any); !ok {
			t.Fatalf("%s=%T(%v), want JSON array; payload=%s", field, payload[field], payload[field], raw)
		}
	}
}

func TestV2DiscussionBlockIDUsesStableUTF8Hex(t *testing.T) {
	if got, want := V2DiscussionBlockID("中文"), "v2-node-e4b8ade69687"; got != want {
		t.Fatalf("V2DiscussionBlockID()=%q want %q", got, want)
	}
}

func TestPatchPreviewMaterializesLegacyDiscussionBlockAtomically(t *testing.T) {
	h := newLegacyPatchHarness(t)
	input := h.derivedPreviewInput("derived-block-title", "preview-materialize")
	before, beforeState, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Blocks) != 0 {
		t.Fatalf("legacy fixture already has blocks: %+v", before.Blocks)
	}

	result, err := h.service.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	after, afterState, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Revision != beforeState.Revision+2 || result.Revision != afterState.Revision {
		t.Fatalf("materialize revision before=%d after=%d result=%d",
			beforeState.Revision, afterState.Revision, result.Revision)
	}
	if len(after.Blocks) != 1 || after.Blocks[0].ID != input.BlockID ||
		after.Blocks[0].Revision != 1 || after.Blocks[0].Kind != "markdown" {
		t.Fatalf("discussion block not materialized: %+v", after.Blocks)
	}
	if h.planner.last.Work == nil || len(h.planner.last.Work.Blocks) != 1 ||
		h.planner.last.Block == nil || h.planner.last.Block.ID != input.BlockID {
		t.Fatalf("planner did not receive materialized context: %+v", h.planner.last)
	}
	if !reflect.DeepEqual(result.Preview.AffectedNodeIDs, []string{"n1"}) ||
		!patchContains(result.Preview.InvalidatedTaskIDs, "n2") {
		t.Fatalf("derived block affected the wrong nodes: %+v", result.Preview)
	}
	receipt := after.V2PatchReceipts[input.RequestID]
	if receipt.ResultRevision != result.Revision {
		t.Fatalf("receipt revision=%d want %d", receipt.ResultRevision, result.Revision)
	}

	replay, err := h.service.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, replayState, _ := h.store.LoadState(h.workID, "")
	if !replay.Duplicate || replay.Revision != result.Revision ||
		replayState.Revision != afterState.Revision || len(replayed.Blocks) != 1 ||
		h.planner.calls != 1 {
		t.Fatalf("materialize replay changed state: replay=%+v rev=%d blocks=%d calls=%d",
			replay, replayState.Revision, len(replayed.Blocks), h.planner.calls)
	}
}

func TestPatchPreviewMaterializationHasNoPlannerFailureSideEffect(t *testing.T) {
	h := newLegacyPatchHarness(t)
	input := h.derivedPreviewInput("planner-fails", "preview-materialize-fails")
	before, beforeState, _ := h.store.LoadState(h.workID, "")
	if _, err := h.service.PreviewWorkPatch(context.Background(), input); err == nil ||
		!strings.Contains(err.Error(), "planner") {
		t.Fatalf("expected planner failure, got %v", err)
	}
	after, afterState, _ := h.store.LoadState(h.workID, "")
	if beforeState.Revision != afterState.Revision || len(before.Blocks) != len(after.Blocks) ||
		len(after.V2PatchPreviews) != 0 {
		t.Fatalf("planner failure left side effects: before=%d after=%d blocks=%+v previews=%+v",
			beforeState.Revision, afterState.Revision, after.Blocks, after.V2PatchPreviews)
	}
}

func TestPatchPreviewMaterializationRejectsUnboundBlock(t *testing.T) {
	h := newLegacyPatchHarness(t)
	input := h.derivedPreviewInput("derived-block-title", "preview-forged-block")
	input.BlockID = "forged-block"
	before, beforeState, _ := h.store.LoadState(h.workID, "")
	if _, err := h.service.PreviewWorkPatch(context.Background(), input); err == nil ||
		!strings.Contains(err.Error(), "not bound") {
		t.Fatalf("expected binding rejection, got %v", err)
	}
	after, afterState, _ := h.store.LoadState(h.workID, "")
	if h.planner.calls != 0 || beforeState.Revision != afterState.Revision ||
		len(before.Blocks) != len(after.Blocks) {
		t.Fatalf("forged block reached planner or mutated state: calls=%d before=%d after=%d blocks=%+v",
			h.planner.calls, beforeState.Revision, afterState.Revision, after.Blocks)
	}
}

func TestPatchPreviewMaterializationCannotBypassDeclaredBlock(t *testing.T) {
	h := newPatchHarness(t)
	input := h.derivedPreviewInput("derived-block-title", "preview-derived-bypass")
	before, beforeState, _ := h.store.LoadState(h.workID, "")
	if _, err := h.service.PreviewWorkPatch(context.Background(), input); err == nil ||
		!strings.Contains(err.Error(), "not bound") {
		t.Fatalf("expected declared block binding rejection, got %v", err)
	}
	after, afterState, _ := h.store.LoadState(h.workID, "")
	if h.planner.calls != 0 || beforeState.Revision != afterState.Revision ||
		len(before.Blocks) != len(after.Blocks) {
		t.Fatalf("derived block bypassed declared binding: calls=%d before=%d after=%d blocks=%+v",
			h.planner.calls, beforeState.Revision, afterState.Revision, after.Blocks)
	}
}

func TestPatchPreviewMaterializationBatchFailureCanRetry(t *testing.T) {
	h := newLegacyPatchHarness(t)
	input := h.derivedPreviewInput("derived-block-title", "preview-materialize-retry")
	fault := &patchFaultStore{FileWorkStore: h.store, mode: "second-event"}
	service := NewPatchService(fault)
	service.SetDefinitionStore(fault)
	service.SetPlanner(&patchPlannerFake{})
	before, beforeState, _ := h.store.LoadState(h.workID, "")
	if _, err := service.PreviewWorkPatch(context.Background(), input); err == nil {
		t.Fatal("expected atomic preview batch failure")
	}
	afterFailure, failedState, _ := h.store.LoadState(h.workID, "")
	if beforeState.Revision != failedState.Revision || len(afterFailure.Blocks) != len(before.Blocks) ||
		len(afterFailure.V2PatchPreviews) != 0 {
		t.Fatalf("failed batch left partial materialization: before=%d after=%d blocks=%+v previews=%+v",
			beforeState.Revision, failedState.Revision, afterFailure.Blocks, afterFailure.V2PatchPreviews)
	}
	result, err := h.service.PreviewWorkPatch(context.Background(), input)
	if err != nil || result.Duplicate {
		t.Fatalf("retry failed: result=%+v err=%v", result, err)
	}
	afterRetry, retryState, _ := h.store.LoadState(h.workID, "")
	if len(afterRetry.Blocks) != 1 || retryState.Revision != beforeState.Revision+2 {
		t.Fatalf("retry did not converge: rev=%d blocks=%+v", retryState.Revision, afterRetry.Blocks)
	}
}

func TestPatchBlockApplyAtomicAndRestartIdempotent(t *testing.T) {
	h := newPatchHarness(t)
	preview, err := h.service.PreviewWorkPatch(context.Background(),
		h.previewInput(t, PatchBlock, "block-title", "preview-block"))
	if err != nil {
		t.Fatal(err)
	}
	apply := ApplyWorkPatchInput{
		WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: PatchBlock, ExpectedRevision: preview.Revision, RequestID: "apply-block",
	}
	result, err := h.service.ApplyWorkPatch(context.Background(), apply)
	if err != nil {
		t.Fatal(err)
	}
	current, state, _ := h.store.LoadState(h.workID, "")
	if state.Revision != result.WorkRevision {
		t.Fatalf("work revision=%d want %d", state.Revision, result.WorkRevision)
	}
	if current.Blocks[0].Title != "Updated block" || current.Blocks[0].Revision != 2 || result.NewRevision != 2 {
		t.Fatalf("block apply did not update atomically: block=%+v result=%+v", current.Blocks[0], result)
	}
	if !patchContains(preview.Preview.AffectedBlockIDs, "b1") ||
		!patchContains(preview.Preview.AffectedNodeIDs, "n1") ||
		!patchContains(preview.Preview.InvalidatedTaskIDs, "n2") ||
		!patchContains(result.StaleArtifactSlotIDs, "report") {
		t.Fatalf("block impact preview=%+v result=%+v", preview.Preview, result)
	}

	reopened, receipt := compactAndRestartPatchStore(t, h.store, h.workID, apply.RequestID)
	if !receipt.RequiresRerun || !result.RequiresRerun {
		t.Fatalf("block requiresRerun lost: result=%+v receipt=%+v", result, receipt)
	}
	restarted := NewPatchService(reopened)
	restarted.SetDefinitionStore(reopened)
	restarted.SetPlanner(&patchPlannerFake{})
	replay, err := restarted.ApplyWorkPatch(context.Background(), apply)
	wantReplay := *result
	wantReplay.Duplicate = true
	if err != nil || !reflect.DeepEqual(replay, &wantReplay) {
		t.Fatalf("apply replay=%+v err=%v", replay, err)
	}
	conflict := apply
	conflict.PreviewDigest = "sha256:different"
	if _, err := restarted.ApplyWorkPatch(context.Background(), conflict); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("expected intent conflict, got %v", err)
	}
}

func TestPatchWorkflowApplyActivatesCOWRevision(t *testing.T) {
	h := newPatchHarness(t)
	preview, err := h.service.PreviewWorkPatch(context.Background(),
		h.previewInput(t, PatchWorkflow, "workflow-title", "preview-workflow"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
		WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: PatchWorkflow, ExpectedRevision: preview.Revision, RequestID: "apply-workflow",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, state, _ := h.store.LoadState(h.workID, "")
	if state.Revision != result.WorkRevision || current.V2CurrentRevision != 3 ||
		current.V2RevisionStates[3] != DefActive || result.NewRevision != 3 {
		t.Fatalf("workflow apply projection=%+v result=%+v", current.V2RevisionStates, result)
	}
	def, err := h.store.LoadRevision(h.workID, 3)
	if err != nil || def.ParentRevision != 2 || def.Nodes[0].Title != "Updated node" {
		t.Fatalf("definition=%+v err=%v", def, err)
	}
}

func TestPatchSlotImpactFlowsIntoApplyReceipt(t *testing.T) {
	h := newPatchHarness(t)
	preview, err := h.service.PreviewWorkPatch(context.Background(),
		h.previewInput(t, PatchWorkflow, "workflow-slot", "preview-slot"))
	if err != nil {
		t.Fatal(err)
	}
	if !patchContains(preview.Preview.AffectedArtifactSlotIDs, "report") ||
		!patchContains(preview.Preview.StaleArtifactSlotIDs, "report") ||
		!patchContains(preview.Preview.AffectedNodeIDs, "n1") ||
		!patchContains(preview.Preview.AffectedNodeIDs, "n2") ||
		!preview.Preview.RequiresRerun {
		t.Fatalf("slot preview impact=%+v", preview.Preview)
	}
	result, err := h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
		WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: PatchWorkflow, ExpectedRevision: preview.Revision, RequestID: "apply-slot",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	receipt := current.V2PatchReceipts["apply-slot"]
	if !patchContains(result.AffectedArtifactSlotIDs, "report") ||
		!patchContains(result.StaleArtifactSlotIDs, "report") ||
		!patchContains(receipt.AffectedArtifactSlotIDs, "report") ||
		!patchContains(receipt.StaleArtifactSlotIDs, "report") ||
		!patchContains(receipt.InvalidatedIDs, "n2") {
		t.Fatalf("slot apply impact result=%+v receipt=%+v", result, receipt)
	}
}

func TestPatchApplyRejectsExpiryDigestAndBaseRevision(t *testing.T) {
	t.Run("expected work revision", func(t *testing.T) {
		h := newPatchHarness(t)
		preview, _ := h.service.PreviewWorkPatch(context.Background(),
			h.previewInput(t, PatchBlock, "block-title", "preview-work-revision"))
		result, err := h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
			WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
			Scope: PatchBlock, ExpectedRevision: preview.Revision - 1, RequestID: "apply-work-revision",
		})
		if err != nil || result.Error == "" {
			t.Fatalf("expected explicit work revision conflict, result=%+v err=%v", result, err)
		}
	})

	t.Run("scope", func(t *testing.T) {
		h := newPatchHarness(t)
		preview, _ := h.service.PreviewWorkPatch(context.Background(),
			h.previewInput(t, PatchBlock, "block-title", "preview-scope"))
		_, err := h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
			WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
			Scope: PatchWorkflow, ExpectedRevision: preview.Revision, RequestID: "apply-scope",
		})
		if err == nil || !strings.Contains(err.Error(), "scope mismatch") {
			t.Fatalf("expected scope rejection, got %v", err)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		h := newPatchHarness(t)
		clock := &patchClock{now: time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)}
		h.service.SetClock(clock)
		preview, err := h.service.PreviewWorkPatch(context.Background(),
			h.previewInput(t, PatchBlock, "block-title", "preview-expiry"))
		if err != nil {
			t.Fatal(err)
		}
		clock.now = preview.Preview.ExpiresAt.Add(time.Nanosecond)
		_, err = h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
			WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
			Scope: PatchBlock, ExpectedRevision: preview.Revision, RequestID: "apply-expired",
		})
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expected expiry rejection, got %v", err)
		}
	})

	t.Run("digest", func(t *testing.T) {
		h := newPatchHarness(t)
		preview, _ := h.service.PreviewWorkPatch(context.Background(),
			h.previewInput(t, PatchBlock, "block-title", "preview-digest"))
		_, err := h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
			WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: "sha256:wrong",
			Scope: PatchBlock, ExpectedRevision: preview.Revision, RequestID: "apply-digest",
		})
		if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("expected digest rejection, got %v", err)
		}
	})

	t.Run("block revision", func(t *testing.T) {
		h := newPatchHarness(t)
		preview, _ := h.service.PreviewWorkPatch(context.Background(),
			h.previewInput(t, PatchBlock, "block-title", "preview-base"))
		current, state, _ := h.store.LoadState(h.workID, "")
		block := current.Blocks[0]
		block.Revision++
		block.Title = "concurrent"
		payload, _ := json.Marshal(block)
		event := newServiceEvent(h.workID, "concurrent-block", EventBlockUpserted, payload, time.Now().UTC())
		event.BaseRevision, event.Revision = state.Revision, state.Revision+1
		if _, err := h.store.CommitEvent(h.workID, event); err != nil {
			t.Fatal(err)
		}
		_, err := h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
			WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
			Scope: PatchBlock, ExpectedRevision: event.Revision, RequestID: "apply-base",
		})
		if err == nil || !strings.Contains(err.Error(), "base block revision mismatch") {
			t.Fatalf("expected block revision rejection, got %v", err)
		}
	})

	t.Run("definition revision", func(t *testing.T) {
		h := newPatchHarness(t)
		oldPreview, _ := h.service.PreviewWorkPatch(context.Background(),
			h.previewInput(t, PatchWorkflow, "workflow-title", "preview-old-definition"))
		newPreview, _ := h.service.PreviewWorkPatch(context.Background(),
			h.previewInput(t, PatchWorkflow, "workflow-title", "preview-new-definition"))
		if _, err := h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
			WorkID: h.workID, PatchID: newPreview.Preview.ID, PreviewDigest: newPreview.Preview.Digest,
			Scope: PatchWorkflow, ExpectedRevision: newPreview.Revision, RequestID: "apply-new-definition",
		}); err != nil {
			t.Fatal(err)
		}
		_, state, _ := h.store.LoadState(h.workID, "")
		_, err := h.service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
			WorkID: h.workID, PatchID: oldPreview.Preview.ID, PreviewDigest: oldPreview.Preview.Digest,
			Scope: PatchWorkflow, ExpectedRevision: state.Revision, RequestID: "apply-old-definition",
		})
		if err == nil || !strings.Contains(err.Error(), "base definition revision mismatch") {
			t.Fatalf("expected definition revision rejection, got %v", err)
		}
	})
}

func TestPatchPlannerOutputCannotCrossScopeOrForbiddenZones(t *testing.T) {
	for _, tc := range []struct {
		name        string
		scope       PatchScope
		instruction string
	}{
		{"forbidden path", PatchWorkflow, "forbidden-path"},
		{"secret in data", PatchBlock, "forbidden-data"},
		{"runtime in data", PatchBlock, "forbidden-runtime-data"},
		{"permission policy in default", PatchWorkflow, "forbidden-default"},
		{"action receipt in schema", PatchWorkflow, "forbidden-schema"},
		{"runtime state", PatchWorkflow, "runtime-state"},
		{"wrong before", PatchWorkflow, "wrong-before"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPatchHarness(t)
			_, before, err := h.store.LoadState(h.workID, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.service.PreviewWorkPatch(context.Background(),
				h.previewInput(t, tc.scope, tc.instruction, "preview-"+strings.ReplaceAll(tc.name, " ", "-")))
			if err == nil {
				t.Fatal("expected planner output rejection")
			}
			current, after, _ := h.store.LoadState(h.workID, "")
			if len(current.V2PatchPreviews) != 0 {
				t.Fatal("invalid planner output polluted projection")
			}
			if after.Revision != before.Revision {
				t.Fatalf("invalid planner output advanced revision: before=%d after=%d", before.Revision, after.Revision)
			}
		})
	}
}

func TestPatchBlockCommitFailureLeavesNoHalfApplyAndCanRetry(t *testing.T) {
	h := newPatchHarness(t)
	preview, _ := h.service.PreviewWorkPatch(context.Background(),
		h.previewInput(t, PatchBlock, "block-title", "preview-atomic"))
	apply := ApplyWorkPatchInput{
		WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: PatchBlock, ExpectedRevision: preview.Revision, RequestID: "apply-atomic",
	}
	workPath, _ := h.store.workPath(h.workID)
	if err := AcquireWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.ApplyWorkPatch(context.Background(), apply); !errors.Is(err, ErrWorkLeaseHeld) {
		_ = releaseStoreLease(workPath)
		t.Fatalf("expected lease failure, got %v", err)
	}
	if err := releaseStoreLease(workPath); err != nil {
		t.Fatal(err)
	}
	current, state, _ := h.store.LoadState(h.workID, "")
	if current.Blocks[0].Title != "Original block" || state.Revision != preview.Revision {
		t.Fatal("failed atomic batch left a half-applied block")
	}
	result, err := h.service.ApplyWorkPatch(context.Background(), apply)
	if err != nil || result.Duplicate {
		t.Fatalf("retry failed: result=%+v err=%v", result, err)
	}
}

func TestPatchWorkflowCommitFailureReusesOrphanAfterRestart(t *testing.T) {
	for _, mode := range []string{"lease", "second-event"} {
		t.Run(mode, func(t *testing.T) {
			h := newPatchHarness(t)
			preview, err := h.service.PreviewWorkPatch(context.Background(),
				h.previewInput(t, PatchWorkflow, "workflow-title", "preview-orphan-"+mode))
			if err != nil {
				t.Fatal(err)
			}
			apply := ApplyWorkPatchInput{
				WorkID: h.workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
				Scope: PatchWorkflow, ExpectedRevision: preview.Revision, RequestID: "apply-orphan-" + mode,
			}
			fault := &patchFaultStore{FileWorkStore: h.store, mode: mode}
			service := NewPatchService(fault)
			service.SetDefinitionStore(fault)
			if _, err := service.ApplyWorkPatch(context.Background(), apply); err == nil {
				t.Fatal("expected workflow event batch failure")
			}
			orphan, err := h.store.LoadRevision(h.workID, 3)
			if err != nil {
				t.Fatalf("orphan revision was not persisted: %v", err)
			}
			current, state, err := h.store.LoadState(h.workID, "")
			if err != nil {
				t.Fatal(err)
			}
			if current.V2CurrentRevision != 2 || state.Revision != preview.Revision {
				t.Fatalf("failed batch polluted projection: def=%d rev=%d previewRev=%d",
					current.V2CurrentRevision, state.Revision, preview.Revision)
			}
			if _, exists := current.V2PatchReceipts[apply.RequestID]; exists {
				t.Fatal("failed batch left an apply receipt")
			}

			reopened, err := NewFileWorkStore(h.dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			restarted := NewPatchService(reopened)
			restarted.SetDefinitionStore(reopened)
			result, err := restarted.ApplyWorkPatch(context.Background(), apply)
			if err != nil {
				t.Fatalf("retry after restart failed: %v", err)
			}
			persisted, err := reopened.LoadRevision(h.workID, 3)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Digest != orphan.Digest || persisted.CreatedAt != orphan.CreatedAt ||
				persisted.CreatedBy != orphan.CreatedBy || result.NewRevision != 3 {
				t.Fatalf("orphan changed on retry: before=%+v after=%+v result=%+v",
					orphan, persisted, result)
			}
			reopenedAgain, receipt := compactAndRestartPatchStore(t, reopened, h.workID, apply.RequestID)
			if !result.RequiresRerun || !receipt.RequiresRerun {
				t.Fatalf("workflow requiresRerun lost: result=%+v receipt=%+v", result, receipt)
			}
			restartedAgain := NewPatchService(reopenedAgain)
			restartedAgain.SetDefinitionStore(reopenedAgain)
			replay, err := restartedAgain.ApplyWorkPatch(context.Background(), apply)
			wantReplay := *result
			wantReplay.Duplicate = true
			if err != nil || !reflect.DeepEqual(replay, &wantReplay) {
				t.Fatalf("idempotent replay=%+v err=%v", replay, err)
			}
		})
	}
}

func TestPatchWorkflowOrphanRejectsDifferentIntent(t *testing.T) {
	h := newPatchHarness(t)
	first, err := h.service.PreviewWorkPatch(context.Background(),
		h.previewInput(t, PatchWorkflow, "workflow-title", "preview-orphan-first"))
	if err != nil {
		t.Fatal(err)
	}
	firstApply := ApplyWorkPatchInput{
		WorkID: h.workID, PatchID: first.Preview.ID, PreviewDigest: first.Preview.Digest,
		Scope: PatchWorkflow, ExpectedRevision: first.Revision, RequestID: "apply-orphan-conflict",
	}
	fault := &patchFaultStore{FileWorkStore: h.store, mode: "second-event"}
	faultService := NewPatchService(fault)
	faultService.SetDefinitionStore(fault)
	if _, err := faultService.ApplyWorkPatch(context.Background(), firstApply); err == nil {
		t.Fatal("expected first apply failure")
	}
	orphan, err := h.store.LoadRevision(h.workID, 3)
	if err != nil {
		t.Fatal(err)
	}

	second, err := h.service.PreviewWorkPatch(context.Background(),
		h.previewInput(t, PatchWorkflow, "workflow-slot", "preview-orphan-second"))
	if err != nil {
		t.Fatal(err)
	}
	secondApply := ApplyWorkPatchInput{
		WorkID: h.workID, PatchID: second.Preview.ID, PreviewDigest: second.Preview.Digest,
		Scope: PatchWorkflow, ExpectedRevision: second.Revision, RequestID: firstApply.RequestID,
	}
	if _, err := h.service.ApplyWorkPatch(context.Background(), secondApply); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("expected orphan intent conflict, got %v", err)
	}
	after, err := h.store.LoadRevision(h.workID, 3)
	if err != nil {
		t.Fatal(err)
	}
	current, state, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Digest != orphan.Digest || current.V2CurrentRevision != 2 || state.Revision != second.Revision {
		t.Fatalf("conflict polluted state: orphan=%+v after=%+v current=%d rev=%d",
			orphan, after, current.V2CurrentRevision, state.Revision)
	}
}

// ── Pending-node (unmaterialized V2TaskRuntime) tests ──────────────────────

func TestPatchPreviewForPendingNode(t *testing.T) {
	h := newLegacyPatchHarness(t)
	current, _, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	derivedTaskID, err := DeriveTaskID(h.runID, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if rt := current.V2TaskRuntimes[derivedTaskID]; rt != nil {
		t.Fatalf("harness unexpectedly has V2TaskRuntime for %q: %+v", derivedTaskID, rt)
	}

	input := PreviewWorkPatchInput{
		WorkID:             h.workID,
		RunID:              h.runID,
		TaskID:             derivedTaskID,
		BlockID:            V2DiscussionBlockID("n1"),
		BlockRevision:      1,
		SessionID:          "discussion-session",
		Instruction:        "derived-block-title",
		DefinitionRevision: current.V2CurrentRevision,
		Scope:              PatchBlock,
		RequestID:          "preview-pending-" + t.Name(),
	}
	result, err := h.service.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicate {
		t.Fatal("unexpected duplicate on first call")
	}
	if result.Preview == nil {
		t.Fatal("preview is nil")
	}
	if h.planner.calls != 1 {
		t.Fatalf("planner calls=%d want 1", h.planner.calls)
	}
	if h.planner.last.Task == nil || h.planner.last.Task.ID != derivedTaskID {
		t.Fatalf("planner task %+v, want ID=%q", h.planner.last.Task, derivedTaskID)
	}
	if h.planner.last.TargetNodeID != "n1" {
		t.Fatalf("planner targetNodeID=%q want n1", h.planner.last.TargetNodeID)
	}
	if h.planner.last.Block == nil || h.planner.last.Block.ID != V2DiscussionBlockID("n1") {
		t.Fatalf("planner block %+v", h.planner.last.Block)
	}

	// Idempotent replay.
	replay, err := h.service.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || replay.Preview == nil || h.planner.calls != 1 {
		t.Fatalf("replay duplicate=%v preview=%v calls=%d", replay.Duplicate, replay.Preview != nil, h.planner.calls)
	}
}

func TestPatchPreviewRejectsForgedStableID(t *testing.T) {
	h := newLegacyPatchHarness(t)
	current, _, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}

	// Forged: correct run but a node ID that doesn't exist in definition.
	forgedTaskID, err := DeriveTaskID(h.runID, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	forged := PreviewWorkPatchInput{
		WorkID:             h.workID,
		RunID:              h.runID,
		TaskID:             forgedTaskID,
		BlockID:            V2DiscussionBlockID("n1"),
		BlockRevision:      1,
		SessionID:          "discussion-session",
		Instruction:        "derived-block-title",
		DefinitionRevision: current.V2CurrentRevision,
		Scope:              PatchBlock,
		RequestID:          "preview-forged-node-" + t.Name(),
	}
	if _, err := h.service.PreviewWorkPatch(context.Background(), forged); err == nil ||
		!strings.Contains(err.Error(), "does not match any node") {
		t.Fatalf("expected forged node rejection, got %v", err)
	}

	// Forged: wrong run ID embedded in DeriveTaskID.
	forgedTaskID2, err := DeriveTaskID("wrong-run", "n1")
	if err != nil {
		t.Fatal(err)
	}
	forgedRun := PreviewWorkPatchInput{
		WorkID:             h.workID,
		RunID:              h.runID,
		TaskID:             forgedTaskID2,
		BlockID:            V2DiscussionBlockID("n1"),
		BlockRevision:      1,
		SessionID:          "discussion-session",
		Instruction:        "derived-block-title",
		DefinitionRevision: current.V2CurrentRevision,
		Scope:              PatchBlock,
		RequestID:          "preview-forged-run-" + t.Name(),
	}
	if _, err := h.service.PreviewWorkPatch(context.Background(), forgedRun); err == nil ||
		!strings.Contains(err.Error(), "does not match any node") {
		t.Fatalf("expected cross-run rejection, got %v", err)
	}
}

func TestPatchApplyForPendingNode(t *testing.T) {
	h := newLegacyPatchHarness(t)
	current, _, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	derivedTaskID, err := DeriveTaskID(h.runID, "n1")
	if err != nil {
		t.Fatal(err)
	}

	// Step 1: Preview.
	previewInput := PreviewWorkPatchInput{
		WorkID:             h.workID,
		RunID:              h.runID,
		TaskID:             derivedTaskID,
		BlockID:            V2DiscussionBlockID("n1"),
		BlockRevision:      1,
		SessionID:          "discussion-session",
		Instruction:        "derived-block-title",
		DefinitionRevision: current.V2CurrentRevision,
		Scope:              PatchBlock,
		RequestID:          "preview-apply-pending-" + t.Name(),
	}
	previewResult, err := h.service.PreviewWorkPatch(context.Background(), previewInput)
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Apply.
	applyInput := ApplyWorkPatchInput{
		WorkID:           h.workID,
		PatchID:          previewResult.Preview.ID,
		PreviewDigest:    previewResult.Preview.Digest,
		Scope:            PatchBlock,
		ExpectedRevision: previewResult.Revision,
		RequestID:        "apply-pending-" + t.Name(),
	}
	applyResult, err := h.service.ApplyWorkPatch(context.Background(), applyInput)
	if err != nil {
		t.Fatal(err)
	}
	if !applyResult.Committed {
		t.Fatal("apply not committed")
	}
	if len(applyResult.AffectedBlockIDs) == 0 {
		t.Fatal("no affected blocks")
	}

	// Step 3: Idempotent replay.
	replay, err := h.service.ApplyWorkPatch(context.Background(), applyInput)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate {
		t.Fatal("apply replay not duplicate")
	}
}
