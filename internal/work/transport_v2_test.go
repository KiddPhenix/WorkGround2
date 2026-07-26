package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestTransportErrorFromCommittedRecoveryStableDTO(t *testing.T) {
	cause := errors.New("injected post-commit scheduling failure")
	err := errors.Join(
		errors.New("outer transport wrapper"),
		committedRecovery("retry-work-node-wake", "work-1", "request-1", 9, cause),
	)
	got := TransportErrorFrom(err)
	if got == nil || got.Code != "committed_recovery" ||
		got.Operation != "retry-work-node-wake" || got.WorkID != "work-1" ||
		got.RequestID != "request-1" || got.Revision != 9 ||
		!got.Committed || !got.Recoverable {
		t.Fatalf("unstable committed recovery DTO: %+v", got)
	}
}

func TestTransportErrorFromConflictAndFutureSchema(t *testing.T) {
	conflict := TransportErrorFrom(&ErrWorkEventConflict{
		WorkID: "work-1", RequestID: "request-1",
		Kind: WorkEventRevisionConflict, Reason: "stale",
	})
	if conflict == nil || conflict.Code != "revision_conflict" || !conflict.Recoverable {
		t.Fatalf("revision conflict DTO: %+v", conflict)
	}
	future := TransportErrorFrom(&ErrFutureSchema{Kind: "Work", Got: 3, CurrentMax: 2})
	if future == nil || future.Code != "future_schema" || future.Recoverable {
		t.Fatalf("future schema DTO: %+v", future)
	}
}

func TestBeginPlanningResultDuplicateRestartAndConflict_FileWorkStore(t *testing.T) {
	_, dir, svc := newFS(t)
	input := BeginWorkPlanningInput{SessionID: "session-a", RequestID: "begin-result"}
	first, err := svc.BeginWorkPlanningWithResult(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Result == nil || first.Duplicate || !first.Committed || first.Revision == 0 {
		t.Fatalf("first = %+v", first)
	}
	reopened, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	replay, err := restarted.BeginWorkPlanningWithResult(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Result == nil || !replay.Duplicate || !replay.Committed ||
		replay.Revision != first.Revision || !reflect.DeepEqual(replay.Result, first.Result) {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	conflict, err := restarted.BeginWorkPlanningWithResult(context.Background(), BeginWorkPlanningInput{
		SessionID: "session-b", RequestID: input.RequestID,
	})
	var typed *ErrWorkEventConflict
	if !errors.As(err, &typed) || conflict == nil || !conflict.Duplicate ||
		conflict.TransportError == nil || conflict.TransportError.Code != "request_conflict" {
		t.Fatalf("conflict = %+v, err = %v", conflict, err)
	}
}

func TestApplyDefinitionResultDuplicateRestartAndConflict_FileWorkStore(t *testing.T) {
	store, dir, svc := newFS(t)
	planning, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "apply-result", RequestID: "apply-result-plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := svc.CreateCandidateRevision(
		context.Background(), planning.Work.ID, v2def(planning.Work.ID, 2),
		"apply-result-candidate", planning.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(planning.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	input := ApplyDefinitionInput{
		WorkID: planning.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "apply-result",
	}
	first, err := svc.ApplyDefinition(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !first.Committed || first.Revision == 0 || first.View == nil {
		t.Fatalf("first = %+v", first)
	}
	reopened, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	replay, err := restarted.ApplyDefinition(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || !replay.Committed || replay.Revision != first.Revision ||
		!reflect.DeepEqual(replay.View, first.View) ||
		!reflect.DeepEqual(replay.Intent, first.Intent) ||
		!reflect.DeepEqual(replay.Impact, first.Impact) {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	_, err = restarted.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: input.WorkID, Revision: 1,
		ExpectedRevision: input.ExpectedRevision, RequestID: input.RequestID,
	})
	var conflict *ErrWorkEventConflict
	if !errors.As(err, &conflict) || conflict.Kind != WorkEventRequestConflict {
		t.Fatalf("different intent err = %v", err)
	}
}

func TestPreviewPatchResultDuplicateRestartAndConflict_FileWorkStore(t *testing.T) {
	h := newPatchHarness(t)
	input := h.previewInput(t, PatchBlock, "block-title", "preview-result")
	service := NewService(h.store, nil, nil)
	service.SetV2PatchPlanner(h.planner)
	first, err := service.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !first.Committed || first.Revision == 0 || first.Preview == nil ||
		first.Receipt == nil || first.Receipt.RequestID != input.RequestID {
		t.Fatalf("first = %+v", first)
	}
	reopened, err := NewFileWorkStore(h.dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetV2PatchPlanner(&patchPlannerFake{})
	replay, err := restarted.PreviewWorkPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || !replay.Committed || replay.Revision != first.Revision ||
		!reflect.DeepEqual(replay.Preview, first.Preview) ||
		!reflect.DeepEqual(replay.Receipt, first.Receipt) {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	conflicting := input
	conflicting.Instruction = "workflow-title"
	conflictResult, err := restarted.PreviewWorkPatch(context.Background(), conflicting)
	if !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("different intent err = %v", err)
	}
	if conflictResult != nil {
		t.Fatalf("different intent result = %+v", conflictResult)
	}
}

func TestApplyPatchTransportReturnsTypedReceiptAndSlotImpact_FileWorkStore(t *testing.T) {
	h := newPatchHarness(t)
	service := NewService(h.store, nil, nil)
	service.SetV2PatchPlanner(h.planner)
	preview, err := service.PreviewWorkPatch(context.Background(),
		h.previewInput(t, PatchWorkflow, "workflow-slot", "transport-slot-preview"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyWorkPatch(context.Background(), ApplyWorkPatchInput{
		WorkID: h.workID, PatchID: preview.Preview.ID,
		PreviewDigest: preview.Preview.Digest, Scope: PatchWorkflow,
		ExpectedRevision: preview.Revision, RequestID: "transport-slot-apply",
	})
	if err == nil || result == nil || !result.Committed || !result.Recoverable ||
		result.TransportError == nil || result.TransportError.Code != "committed_recovery" {
		t.Fatalf("scheduler failure must be typed committed recovery: result=%+v err=%v", result, err)
	}
	if result.Receipt == nil || result.Receipt.RequestID != "transport-slot-apply" ||
		!patchContains(result.AffectedArtifactSlotIDs, "report") ||
		!patchContains(result.StaleArtifactSlotIDs, "report") ||
		!patchContains(result.Receipt.AffectedArtifactSlotIDs, "report") ||
		!patchContains(result.Receipt.StaleArtifactSlotIDs, "report") {
		t.Fatalf("typed apply result=%+v", result)
	}
}

// TestBlankDraftPersistsAndRestartsAcrossInstances_FileWorkStore verifies that
// a blank draft Definition (goal="", nodes=[]) produced by BeginWorkPlanning
// survives persistence, restart, and cross-instance recovery. The blank draft
// must remain identical after reopening; CreateCandidateRevision must still
// work on the recovered draft; and ApplyDefinition must be accepted after the
// candidate is committed.
func TestBlankDraftPersistsAndRestartsAcrossInstances_FileWorkStore(t *testing.T) {
	store, dir, svc := newFS(t)
	ctx := context.Background()

	// 1. BeginWorkPlanning creates a blank draft revision 1.
	planInput := BeginWorkPlanningInput{SessionID: "blank-draft-session", RequestID: "blank-draft-plan"}
	view, err := svc.BeginWorkPlanning(ctx, planInput)
	if err != nil {
		t.Fatal(err)
	}
	def, err := store.LoadRevision(view.Work.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if def.Goal != "" {
		t.Fatalf("blank draft goal must be empty, got %q", def.Goal)
	}
	if len(def.Nodes) != 0 {
		t.Fatalf("blank draft nodes must be empty, got %d", len(def.Nodes))
	}
	if def.Status != DefDraft {
		t.Fatalf("blank draft status must be draft, got %q", def.Status)
	}
	if def.Revision != 1 || def.ParentRevision != 0 {
		t.Fatalf("blank draft revision=%d parent=%d", def.Revision, def.ParentRevision)
	}

	// 2. File on disk: definitions/1.json must contain the blank draft body.
	defFile := filepath.Join(dir, view.Work.ID, "definitions", "1.json")
	if _, err := os.Stat(defFile); err != nil {
		t.Fatalf("definitions/1.json missing: %v", err)
	}

	// 3. Restart: reopen the store and verify the blank draft is identical.
	reopened, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetV2TransportEnabled(true)
	restarted.SetDefinitionRevisionStore(reopened)
	recovered, err := restarted.Get(ctx, view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Definition == nil {
		t.Fatal("recovered definition is nil after restart")
	}
	if recovered.Definition.Goal != "" {
		t.Fatalf("recovered blank draft goal must be empty, got %q", recovered.Definition.Goal)
	}
	if len(recovered.Definition.Nodes) != 0 {
		t.Fatalf("recovered blank draft nodes must be empty, got %d", len(recovered.Definition.Nodes))
	}
	if recovered.Definition.Status != DefDraft {
		t.Fatalf("recovered blank draft status must be draft, got %q", recovered.Definition.Status)
	}
	if recovered.Definition.Revision != 1 {
		t.Fatalf("recovered blank draft revision must be 1, got %d", recovered.Definition.Revision)
	}
	if recovered.Definition.Digest != def.Digest {
		t.Fatalf("digest changed across restart: %q != %q", recovered.Definition.Digest, def.Digest)
	}

	// 4. Generate candidate + ApplyDefinition → verify transport status=active.
	_, state, err := reopened.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate := v2def(view.Work.ID, 2)
	candidate.Status = DefDraft
	candidate.ParentRevision = 1
	candidate.CreatedBy = "planning"
	candidate.CreatedAt = time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	digest, err := ComputeV2RevisionDigest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Digest = digest
	candidateRev, err := restarted.CreateCandidateRevision(ctx, view.Work.ID, candidate, "blank-draft-candidate", state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if candidateRev.Revision != 2 {
		t.Fatalf("candidate revision must be 2, got %d", candidateRev.Revision)
	}

	_, state, err = reopened.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	applyInput := ApplyDefinitionInput{
		WorkID:           view.Work.ID,
		Revision:         candidateRev.Revision,
		ExpectedRevision: state.Revision,
		RequestID:        "blank-draft-apply",
	}
	applyResult, err := restarted.ApplyDefinition(ctx, applyInput)
	if err != nil {
		t.Fatal(err)
	}
	if applyResult.Duplicate || !applyResult.Committed || applyResult.View == nil {
		t.Fatalf("apply failed: %+v", applyResult)
	}
	// Transport view must show active status projected from V2RevisionStates.
	if applyResult.View.Definition == nil {
		t.Fatal("applied definition is nil in transport view")
	}
	if applyResult.View.Definition.Status != DefActive {
		t.Fatalf("transport definition status must be active after Apply, got %q", applyResult.View.Definition.Status)
	}

	// 4a. Disk body for revision 2 must still be "draft" (immutable).
	diskBody, err := reopened.LoadRevision(view.Work.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if diskBody.Status != DefDraft {
		t.Fatalf("disk body status must remain draft (immutable), got %q", diskBody.Status)
	}

	// 5. Cross-instance recovery: new Service.Get must return active definition.
	reopened2, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted2 := NewService(reopened2, nil, nil)
	restarted2.SetV2TransportEnabled(true)
	restarted2.SetDefinitionRevisionStore(reopened2)
	recovered2, err := restarted2.Get(ctx, view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered2.Definition == nil {
		t.Fatal("cross-instance recovered definition is nil")
	}
	if recovered2.Definition.Status != DefActive {
		t.Fatalf("cross-instance definition status must be active, got %q", recovered2.Definition.Status)
	}
	if recovered2.Definition.Revision != 2 {
		t.Fatalf("cross-instance definition revision must be 2, got %d", recovered2.Definition.Revision)
	}
}
