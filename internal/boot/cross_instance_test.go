package boot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"workground2/internal/config"
	"workground2/internal/work"
)

func dualFileStores(t *testing.T) (a, b *work.FileWorkStore) {
	t.Helper()
	dir := t.TempDir()
	var err error
	a, err = work.NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatalf("NewFileWorkStore A: %v", err)
	}
	b, err = work.NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatalf("NewFileWorkStore B: %v", err)
	}
	return a, b
}

func asDefStore(s work.WorkStore) work.DefinitionRevisionStore {
	return s.(work.DefinitionRevisionStore)
}

func setupV2WorkWithDef(t *testing.T, store *work.FileWorkStore, bp *work.BlueprintRegistry) (workID string, svc *work.Service, rev int64, runID string) {
	t.Helper()
	svc = work.NewService(store, bp, work.ViewSinkDiscard)
	svc.SetV2TransportEnabled(true)
	svc.SetTaskExecutor(&bootV2Executor{})
	result, err := svc.BeginWorkPlanning(context.Background(), work.BeginWorkPlanningInput{
		SessionID: "s-setup", RequestID: "req-setup",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	workID = result.Work.ID

	now := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	def, err := svc.CreateCandidateRevision(context.Background(), workID, &work.WorkDefinitionRevision{
		WorkID: workID, Revision: 2, ParentRevision: 1, Status: work.DefDraft, Goal: "test",
		Nodes:         []work.NodeDef{{ID: "n1", Title: "N1", InputSpecIDs: []string{"spec-1"}, BlockIDs: []string{"b1"}}},
		ArtifactSlots: []work.ArtifactSlotDef{{ID: "slot-1", Title: "S1", Kind: "file", ExpectedCount: 1}},
		InputSpecs:    []work.InputSpec{{ID: "spec-1", Label: "L1", Kind: "text", Required: true, PinEligible: true}},
		CreatedBy:     "cross-instance-test", CreatedAt: now,
	}, "req-def-created", result.Revision)
	if err != nil {
		t.Fatalf("CreateCandidateRevision: %v", err)
	}

	_, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatalf("LoadState before ApplyDefinition: %v", err)
	}
	res, err := svc.ApplyDefinition(context.Background(), work.ApplyDefinitionInput{
		WorkID: workID, Revision: def.Revision, ExpectedRevision: state.Revision, RequestID: "req-apply-def",
	})
	if err != nil {
		t.Fatalf("ApplyDefinition: %v", err)
	}

	current, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatalf("LoadState after ApplyDefinition: %v", err)
	}
	if len(current.Runs) == 0 {
		t.Fatal("ApplyDefinition did not create a production run")
	}
	runID = current.Runs[len(current.Runs)-1].ID

	block := work.BlockInstance{
		ID: "b1", Kind: "note", SchemaVersion: 1, Revision: 1,
		Title: "Original block", Status: work.BlockReady, Data: json.RawMessage(`{"value":"old"}`),
		Source:   work.BlockSource{Provider: "test", Mode: "snapshot"},
		Fallback: work.BlockFallback{Summary: "Original block"}, CreatedAt: now, UpdatedAt: now,
	}
	blockPayload, _ := json.Marshal(block)
	blockEvent := work.WorkEvent{
		ID: "ev-block-seed", WorkID: workID, SchemaVersion: 1,
		BaseRevision: state.Revision, Revision: state.Revision + 1,
		RequestID: "req-block-seed", Type: work.EventBlockUpserted, Payload: blockPayload,
		Object:    work.ObjectContext{Kind: work.ObjectBlock, ID: block.ID, WorkID: workID, BlockID: block.ID},
		CreatedAt: now,
	}
	if _, err := store.CommitEvent(workID, blockEvent); err != nil {
		t.Fatalf("CommitEvent block: %v", err)
	}

	inputPayload, _ := json.Marshal(work.InputRequestedPayload{
		InputID: "spec-1", WorkID: workID, RunID: runID, TaskID: "n1", BlockID: "b1", SpecID: "spec-1",
	})
	inputEvent := work.WorkEvent{
		ID: "ev-input-requested", WorkID: workID, SchemaVersion: 2,
		BaseRevision: state.Revision + 1, Revision: state.Revision + 2,
		RequestID: "req-input-requested", Type: work.EventInputRequested, Payload: inputPayload,
		Object: work.ObjectContext{
			Kind: work.ObjectInput, ID: "spec-1", WorkID: workID, RunID: runID,
			TaskID: "n1", BlockID: "b1", InputID: "spec-1", SpecID: "spec-1",
			DefinitionRevision: &def.Revision,
		},
		CreatedAt: now,
	}
	if _, err := store.CommitEvent(workID, inputEvent); err != nil {
		t.Fatalf("CommitEvent input.requested: %v", err)
	}
	return workID, svc, res.Revision + 2, runID
}

// ── SubmitV2Input ──────────────────────────────────────────────────────────

func TestDualFileStoreCrossInstance_SubmitV2Input_DuplicateConflictLate(t *testing.T) {
	storeA, storeB := dualFileStores(t)
	bp := work.NewBlueprintRegistry()
	workID, svcA, rev, _ := setupV2WorkWithDef(t, storeA, bp)

	ctx := context.Background()
	reqID := "req-input-dup-001"

	r1, err := svcA.SubmitWorkInput(ctx, work.SubmitInputRequest{
		WorkID: workID, InputID: "spec-1", Value: json.RawMessage(`"v"`),
		DefinitionRev: 2, ExpectedRevision: rev, RequestID: reqID,
	})
	if err != nil {
		t.Fatalf("SubmitWorkInput A: %v", err)
	}
	if !r1.Committed {
		t.Fatal("expected committed")
	}

	pb, _ := storeB.LoadProjection(workID)
	found := false
	for _, inp := range pb.V2Inputs {
		if inp.ID == "spec-1" && inp.State == "submitted" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("store B did not see input")
	}

	svcB := work.NewService(storeB, bp, work.ViewSinkDiscard)
	svcB.SetV2TransportEnabled(true)
	svcB.SetTaskExecutor(&bootV2Executor{})
	r2, err := svcB.SubmitWorkInput(ctx, work.SubmitInputRequest{
		WorkID: workID, InputID: "spec-1", Value: json.RawMessage(`"v"`),
		DefinitionRev: 2, ExpectedRevision: rev, RequestID: reqID,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !r2.Duplicate {
		t.Fatal("expected duplicate")
	}

	_, err = svcB.SubmitWorkInput(ctx, work.SubmitInputRequest{
		WorkID: workID, InputID: "spec-1", Value: json.RawMessage(`"x"`),
		DefinitionRev: 2, ExpectedRevision: rev, RequestID: reqID,
	})
	if err == nil {
		t.Fatal("expected conflict for different value")
	}

	late, err := svcB.SubmitWorkInput(ctx, work.SubmitInputRequest{
		WorkID: workID, InputID: "spec-1", Value: json.RawMessage(`"s"`),
		DefinitionRev: 2, InputRevision: 0, ExpectedRevision: rev - 1, RequestID: "req-late-999",
	})
	if err == nil && (late == nil || late.Error == "") {
		t.Fatal("expected explicit failure for late event")
	}
	if late != nil && late.Committed {
		t.Fatal("late event without receipt must not report committed")
	}

	svcB2 := work.NewService(storeB, bp, work.ViewSinkDiscard)
	svcB2.SetV2TransportEnabled(true)
	svcB2.SetTaskExecutor(&bootV2Executor{})
	r3, err := svcB2.SubmitWorkInput(ctx, work.SubmitInputRequest{
		WorkID: workID, InputID: "spec-1", Value: json.RawMessage(`"v"`),
		DefinitionRev: 2, ExpectedRevision: rev, RequestID: reqID,
	})
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if !r3.Duplicate {
		t.Fatal("restart replay should be duplicate")
	}
}

// ── Preview + Apply patch ──────────────────────────────────────────────────

type deterministicPatcher struct{}

func (p *deterministicPatcher) PlanPatch(_ context.Context, in work.PatchPlanInput) (*work.PatchPlan, error) {
	return &work.PatchPlan{Operations: []work.PatchOp{{
		Op: "replace", Path: "blocks/" + in.Block.ID + "/title", NewValue: json.RawMessage(`"U"`),
	}}}, nil
}

func TestDualFileStoreCrossInstance_PreviewApplyPatch_DuplicateConflictRestart(t *testing.T) {
	storeA, storeB := dualFileStores(t)
	bp := work.NewBlueprintRegistry()
	workID, svcA, _, runID := setupV2WorkWithDef(t, storeA, bp)

	planner := &deterministicPatcher{}
	svcA.SetV2PatchPlanner(planner)

	preview, err := svcA.PreviewWorkPatch(context.Background(), work.PreviewWorkPatchInput{
		WorkID: workID, RunID: runID, TaskID: "n1", BlockID: "b1",
		SessionID: "s-disc", Instruction: "ch",
		DefinitionRevision: 2, BlockRevision: 1,
		Scope: work.PatchBlock, RequestID: "req-preview-patch-001",
	})
	if err != nil {
		t.Fatalf("PreviewWorkPatch A: %v", err)
	}
	if preview.Preview == nil {
		t.Fatal("expected preview")
	}

	pb, _ := storeB.LoadProjection(workID)
	if _, ok := pb.V2PatchPreviews[preview.Preview.ID]; !ok {
		t.Fatal("store B did not see preview")
	}

	svcB := work.NewService(storeB, bp, work.ViewSinkDiscard)
	svcB.SetV2TransportEnabled(true)
	svcB.SetTaskExecutor(&bootV2Executor{})
	svcB.SetV2PatchPlanner(planner)
	previewReplay, err := svcB.PreviewWorkPatch(context.Background(), work.PreviewWorkPatchInput{
		WorkID: workID, RunID: runID, TaskID: "n1", BlockID: "b1",
		SessionID: "s-disc", Instruction: "ch",
		DefinitionRevision: 2, BlockRevision: 1,
		Scope: work.PatchBlock, RequestID: "req-preview-patch-001",
	})
	if err != nil {
		t.Fatalf("preview replay: %v", err)
	}
	if !previewReplay.Duplicate {
		t.Fatal("expected preview duplicate")
	}

	digestResult, digestErr := svcB.ApplyWorkPatch(context.Background(), work.ApplyWorkPatchInput{
		WorkID: workID, PatchID: preview.Preview.ID, PreviewDigest: "bad",
		Scope: work.PatchBlock, ExpectedRevision: preview.Revision, RequestID: "req-digest-fail",
	})
	if digestErr == nil && (digestResult == nil || digestResult.Error == "") {
		t.Fatal("expected digest conflict")
	}
	if digestResult != nil && digestResult.Committed {
		t.Fatal("digest conflict must not be reported as committed")
	}

	scopeResult, scopeErr := svcB.ApplyWorkPatch(context.Background(), work.ApplyWorkPatchInput{
		WorkID: workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: work.PatchWorkflow, ExpectedRevision: preview.Revision, RequestID: "req-scope-fail",
	})
	if scopeErr == nil && (scopeResult == nil || scopeResult.Error == "") {
		t.Fatal("expected scope conflict")
	}
	if scopeResult != nil && scopeResult.Committed {
		t.Fatal("scope conflict must not be reported as committed")
	}

	revisionResult, revisionErr := svcB.ApplyWorkPatch(context.Background(), work.ApplyWorkPatchInput{
		WorkID: workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: work.PatchBlock, ExpectedRevision: preview.Revision - 1, RequestID: "req-revision-fail",
	})
	if revisionErr == nil && (revisionResult == nil || revisionResult.Error == "") {
		t.Fatal("expected revision conflict")
	}
	if revisionResult != nil && revisionResult.Committed {
		t.Fatal("revision conflict must not be reported as committed")
	}

	ar, err := svcB.ApplyWorkPatch(context.Background(), work.ApplyWorkPatchInput{
		WorkID: workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: work.PatchBlock, ExpectedRevision: preview.Revision, RequestID: "req-apply-patch-001",
	})
	if err != nil {
		t.Fatalf("ApplyWorkPatch B: %v", err)
	}
	if !ar.Committed {
		t.Fatal("expected committed")
	}

	ar2, err := svcA.ApplyWorkPatch(context.Background(), work.ApplyWorkPatchInput{
		WorkID: workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: work.PatchBlock, ExpectedRevision: preview.Revision, RequestID: "req-apply-patch-001",
	})
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if !ar2.Duplicate {
		t.Fatal("expected duplicate")
	}

	svcB2 := work.NewService(storeB, bp, work.ViewSinkDiscard)
	svcB2.SetV2TransportEnabled(true)
	svcB2.SetTaskExecutor(&bootV2Executor{})
	svcB2.SetV2PatchPlanner(planner)
	ar3, err := svcB2.ApplyWorkPatch(context.Background(), work.ApplyWorkPatchInput{
		WorkID: workID, PatchID: preview.Preview.ID, PreviewDigest: preview.Preview.Digest,
		Scope: work.PatchBlock, ExpectedRevision: preview.Revision, RequestID: "req-apply-patch-001",
	})
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if !ar3.Duplicate {
		t.Fatal("restart replay should be duplicate")
	}
}

// ── ApplyV2Definition cross-instance ──────────────────────────────────────

func TestDualFileStoreCrossInstance_ApplyV2Definition_ConflictRestart(t *testing.T) {
	storeA, storeB := dualFileStores(t)
	bp := work.NewBlueprintRegistry()
	workID, svcA, rev, _ := setupV2WorkWithDef(t, storeA, bp)

	candidate := &work.WorkDefinitionRevision{
		WorkID: workID, Revision: 3, ParentRevision: 2, Status: "draft", Goal: "u",
		Nodes:         []work.NodeDef{{ID: "n1", Title: "Updated", InputSpecIDs: []string{"spec-1"}, BlockIDs: []string{"b1"}}, {ID: "n2", Title: "New"}},
		ArtifactSlots: []work.ArtifactSlotDef{{ID: "slot-1", Title: "S1", Kind: "file", ExpectedCount: 1}},
		InputSpecs:    []work.InputSpec{{ID: "spec-1", Label: "L1", Kind: "text", Required: true}},
	}
	d, _ := work.ComputeV2RevisionDigest(candidate)
	candidate.Digest = d
	if err := asDefStore(storeA).StoreRevision(workID, candidate); err != nil {
		t.Fatalf("StoreRevision candidate: %v", err)
	}
	cp2, _ := json.Marshal(map[string]interface{}{
		"workId": workID, "revision": 3, "parentRevision": 2, "digest": d,
	})
	if _, err := storeA.CommitEvent(workID, work.WorkEvent{
		ID: "ev-def-created-3", WorkID: workID, SchemaVersion: 2, BaseRevision: rev,
		Revision: rev + 1, RequestID: "req-def-created-3", Type: work.EventDefRevisionCreated,
		Payload: cp2,
		Object:  work.ObjectContext{Kind: work.ObjectDefinition, ID: workID, WorkID: workID, DefinitionID: workID, DefinitionRevision: &candidate.Revision},
	}); err != nil {
		t.Fatalf("CommitEvent cand-created: %v", err)
	}

	svcB := work.NewService(storeB, bp, work.ViewSinkDiscard)
	svcB.SetV2TransportEnabled(true)
	res, err := svcB.ApplyDefinition(context.Background(), work.ApplyDefinitionInput{
		WorkID: workID, Revision: 3, ExpectedRevision: rev + 1, RequestID: "req-def-apply-x",
	})
	if err != nil {
		t.Fatalf("ApplyDefinition B: %v", err)
	}
	if res.Impact == nil {
		t.Fatal("expected RunImpact")
	}
	if len(res.Impact.NewNodeIDs) != 1 || res.Impact.NewNodeIDs[0] != "n2" {
		t.Fatalf("expected n2, got %v", res.Impact.NewNodeIDs)
	}

	pa, _ := storeA.LoadProjection(workID)
	if pa.V2CurrentRevision != 3 {
		t.Fatalf("expected V2CurrentRevision=3, got %d", pa.V2CurrentRevision)
	}

	r2, err := svcA.ApplyDefinition(context.Background(), work.ApplyDefinitionInput{
		WorkID: workID, Revision: 3, ExpectedRevision: rev + 1, RequestID: "req-def-apply-x",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !r2.Duplicate {
		t.Fatal("expected duplicate")
	}

	svcA2 := work.NewService(storeA, bp, work.ViewSinkDiscard)
	svcA2.SetV2TransportEnabled(true)
	r3, err := svcA2.ApplyDefinition(context.Background(), work.ApplyDefinitionInput{
		WorkID: workID, Revision: 3, ExpectedRevision: rev + 1, RequestID: "req-def-apply-x",
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !r3.Duplicate {
		t.Fatal("restart replay should be duplicate")
	}
}

func TestCrossInstanceBootCompilationSentinel(t *testing.T) {
	_ = config.Config{}
	_ = work.Work{}
}
