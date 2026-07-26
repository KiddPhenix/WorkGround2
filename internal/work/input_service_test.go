package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Test helpers ────────────────────────────────────────────────────────────

func newInputServiceTest(t *testing.T) (*InputService, *Service, *FileWorkStore, string) {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "wist-"+strings.ReplaceAll(t.Name(), "/", "-"))
	os.RemoveAll(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	store, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil, nil)
	inputSvc := NewInputService(store, svc.cornerstones)
	return inputSvc, svc, store, dir
}

func inputGuards(t *testing.T, store *FileWorkStore, workID, inputID string) (workRev, inputRev, defRev int64) {
	t.Helper()
	current, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	idx := findInputIndex(current, inputID)
	if idx < 0 {
		t.Fatalf("input %q missing", inputID)
	}
	return state.Revision, current.V2Inputs[idx].Revision, current.V2CurrentRevision
}

type failInputLinkStore struct {
	WorkStore
	fail bool
}

func (s *failInputLinkStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	if s.fail && event.Type == EventInputCornerstoneChanged {
		s.fail = false
		return 0, errors.New("injected input link commit failure")
	}
	return s.WorkStore.CommitEvent(workID, event)
}

// createV2WorkWithInput creates a V2 work with a full definition lifecycle
// (planning → candidate → apply) plus an input.requested event, then returns
// the workID and inputID ready for testing.
func createV2WorkWithInput(t *testing.T, svc *Service, store *FileWorkStore) (workID, inputID string) {
	t.Helper()
	ctx := context.Background()
	inputID = "in-test-001"
	specID := "spec-focus"

	// Step 1: BeginWorkPlanning creates a V2 work.
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "sess-" + t.Name()[:10], RequestID: "plan-" + t.Name()[:10],
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	workID = view.Work.ID

	// Step 2: Create a candidate revision with InputSpecs.
	now := time.Now().UTC()
	candidate := &WorkDefinitionRevision{
		WorkID: workID, Revision: 2, ParentRevision: 1, Status: DefDraft,
		Goal: "Input test",
		Nodes: []NodeDef{
			{ID: "n1", Title: "Task One", InputSpecIDs: []string{specID}},
		},
		ArtifactSlots: []ArtifactSlotDef{
			{ID: "slot1", Title: "Output", Kind: "text", ExpectedCount: 1, Required: true},
		},
		InputSpecs: []InputSpec{
			{ID: specID, Label: "Focus", Kind: InputText, Required: true, PinEligible: true},
		},
		CreatedBy: "test", CreatedAt: now,
	}
	_, st, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatalf("LoadState before candidate: %v", err)
	}
	candidate, err = svc.CreateCandidateRevision(ctx, workID, candidate, "cand", st.Revision)
	if err != nil {
		t.Fatalf("CreateCandidateRevision: %v", err)
	}

	// Step 3: Apply the definition.
	_, applySt, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatalf("LoadState before apply: %v", err)
	}
	_, err = svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: candidate.Revision,
		ExpectedRevision: applySt.Revision, RequestID: "apply-def",
	})
	if err != nil {
		t.Fatalf("ApplyDefinition: %v", err)
	}

	// Step 4: Commit an input.requested event directly.
	reqPayload, _ := json.Marshal(InputRequestedPayload{
		InputID: inputID, WorkID: workID, RunID: "run-1", TaskID: "task-n1",
		BlockID: "blk-1", SpecID: specID,
	})
	reqEvent := newServiceEventV2(workID, "req-input-"+inputID, EventInputRequested, reqPayload, now)
	_, requestState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatalf("LoadState before input.requested: %v", err)
	}
	reqEvent.BaseRevision, reqEvent.Revision = requestState.Revision, requestState.Revision+1
	reqEvent.Object = ObjectContext{
		Kind: ObjectInput, ID: inputID, WorkID: workID,
		RunID: "run-1", TaskID: "task-n1", BlockID: "blk-1", InputID: inputID, SpecID: specID,
		DefinitionRevision: int64Ptr(candidate.Revision),
	}
	if _, err := store.CommitEvent(workID, reqEvent); err != nil {
		t.Fatalf("Commit input.requested: %v", err)
	}

	return workID, inputID
}

// ── Test: RequestInput ─────────────────────────────────────────────────────

func TestInputService_RequestInput(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)

	// The input already exists from setup — verify it.
	work, _, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	idx := findInputIndex(work, inputID)
	if idx < 0 {
		t.Fatal("expected input to exist in projection")
	}
	in := work.V2Inputs[idx]
	if in.State != InputRequested {
		t.Fatalf("expected requested state, got %s", in.State)
	}
	if in.TaskID != "task-n1" {
		t.Fatalf("expected taskID task-n1, got %s", in.TaskID)
	}

	// Idempotent: requesting again returns same input.
	ctx := context.Background()
	workRev, _, defRev := inputGuards(t, store, workID, inputID)
	_, err = inputSvc.RequestInput(ctx, RequestInputRequest{
		WorkID: workID, InputID: "in-other", RunID: "run-1", TaskID: "task-n1",
		BlockID: "blk-1", SpecID: in.SpecID, DefinitionRev: defRev,
		ExpectedRevision: workRev, RequestID: "req-other",
	})
	if err != nil {
		t.Fatalf("RequestInput: %v", err)
	}
	dup, err := inputSvc.RequestInput(ctx, RequestInputRequest{
		WorkID: workID, InputID: "in-other", RunID: "run-1", TaskID: "task-n1",
		BlockID: "blk-1", SpecID: in.SpecID, DefinitionRev: defRev,
		ExpectedRevision: workRev, RequestID: "req-other",
	})
	if err != nil || dup.ID != "in-other" {
		t.Fatalf("idempotent RequestInput: input=%#v err=%v", dup, err)
	}
}

// ── Test: SaveDraft ─────────────────────────────────────────────────────────

func TestInputService_SaveDraft(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Save a draft.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	req := SaveDraftRequest{
		WorkID:        workID,
		InputID:       inputID,
		Value:         json.RawMessage(`"draft text"`),
		Source:        "user",
		UpdatedBy:     "tester",
		DefinitionRev: defRev, InputRevision: inputRev, ExpectedRevision: workRev,
		RequestID: "draft-1",
	}
	result, err := inputSvc.SaveDraft(ctx, req)
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Input.State != InputDraft {
		t.Fatalf("expected draft state, got %s", result.Input.State)
	}
	if string(result.Input.Value) != `"draft text"` {
		t.Fatalf("expected draft value, got %s", string(result.Input.Value))
	}
	if result.Input.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", result.Input.Revision)
	}

	// Idempotent: same requestID returns the same result.
	dup, err := inputSvc.SaveDraft(ctx, req)
	if err != nil {
		t.Fatalf("idempotent SaveDraft: %v", err)
	}
	if dup.Input.Revision != 1 {
		t.Fatalf("idempotent should have same revision, got %d", dup.Input.Revision)
	}
	conflictReq := req
	conflictReq.Value = json.RawMessage(`"different"`)
	if _, err := inputSvc.SaveDraft(ctx, conflictReq); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("same requestID with different draft must conflict, got %v", err)
	}
}

// ── Test: SaveDraft validation failure preserves draft ──────────────────────

func TestInputService_SaveDraft_RevisionConflict(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// First draft.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	_, err := inputSvc.SaveDraft(ctx, SaveDraftRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"v1"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "draft-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second draft with stale expectedRevision.
	workRev, _, defRev = inputGuards(t, store, workID, inputID)
	result, err := inputSvc.SaveDraft(ctx, SaveDraftRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"v2"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "draft-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected revision conflict error")
	}
	if !strings.Contains(result.Error, "revision conflict") {
		t.Fatalf("expected revision conflict, got %q", result.Error)
	}
	// First draft value preserved.
	if result.Input.Revision != 1 {
		t.Fatalf("expected preserved revision 1, got %d", result.Input.Revision)
	}
}

// ── Test: SubmitInput ───────────────────────────────────────────────────────

func TestInputService_SubmitInput(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Submit with a valid value.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	req := SubmitInputRequest{
		WorkID:           workID,
		InputID:          inputID,
		Value:            json.RawMessage(`"my focus"`),
		Source:           "user",
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev,
		RequestID: "submit-1",
	}
	result, err := inputSvc.SubmitInput(ctx, req)
	if err != nil {
		t.Fatalf("SubmitInput: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Input.State != InputSubmitted {
		t.Fatalf("expected submitted state, got %s", result.Input.State)
	}
	if string(result.Input.Value) != `"my focus"` {
		t.Fatalf("expected submitted value, got %s", string(result.Input.Value))
	}
	if len(result.AffectedTaskIDs) == 0 || result.AffectedTaskIDs[0] != "task-n1" {
		t.Fatalf("expected affected task task-n1, got %v", result.AffectedTaskIDs)
	}

	// Idempotent: same requestID returns duplicate.
	dup, err := inputSvc.SubmitInput(ctx, req)
	if err != nil {
		t.Fatalf("idempotent SubmitInput: %v", err)
	}
	if !dup.Duplicate {
		t.Fatal("expected duplicate flag")
	}
	if dup.Input.Revision != 1 {
		t.Fatalf("expected preserved revision 1, got %d", dup.Input.Revision)
	}
}

// ── Test: SubmitInput validation failure preserves state ────────────────────

func TestInputService_SubmitInput_ValidationFailure(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Submit with invalid value (number for text spec).
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	req := SubmitInputRequest{
		WorkID:           workID,
		InputID:          inputID,
		Value:            json.RawMessage(`42`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev,
		RequestID: "submit-bad",
	}
	result, err := inputSvc.SubmitInput(ctx, req)
	if err != nil {
		t.Fatalf("SubmitInput should return result with error, not raw error: %v", err)
	}
	if result.Error == "" {
		t.Fatal("expected validation error")
	}
	if result.Input.State != InputRejected || string(result.Input.Value) != "42" || result.Input.Revision != inputRev+1 {
		t.Fatalf("validation rejection not persisted: %#v", result.Input)
	}
	restarted := NewInputService(store, svc.cornerstones)
	duplicate, err := restarted.SubmitInput(ctx, req)
	if err != nil || !duplicate.Duplicate || duplicate.Error == "" || duplicate.Input.State != InputRejected {
		t.Fatalf("restart validation receipt: result=%#v err=%v", duplicate, err)
	}
	work, _, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	idx := findInputIndex(work, inputID)
	if idx < 0 || work.V2Inputs[idx].Error == "" || string(work.V2Inputs[idx].Value) != "42" {
		t.Fatalf("replayed validation draft missing: %#v", work.V2Inputs)
	}
	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	retry, err := restarted.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"fixed"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "submit-fixed",
	})
	if err != nil || retry.Error != "" || retry.Input.State != InputSubmitted {
		t.Fatalf("retry rejected draft: result=%#v err=%v", retry, err)
	}
}

// ── Test: SubmitInput revision conflict ─────────────────────────────────────

func TestInputService_SubmitInput_RevisionConflict(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// First submit.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	_, err := inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"v1"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "sub-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second submit with stale expectedRevision.
	workRev, _, defRev = inputGuards(t, store, workID, inputID)
	result, err := inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"v2"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "sub-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected revision conflict")
	}
}

// ── Test: RejectInput ───────────────────────────────────────────────────────

func TestInputService_RejectInput(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Reject.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	req := RejectInputRequest{WorkID: workID, InputID: inputID, Reason: "not needed", Source: "reviewer",
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "reject-1"}
	result, err := inputSvc.RejectInput(ctx, req)
	if err != nil {
		t.Fatalf("RejectInput: %v", err)
	}
	if result.State != InputRejected {
		t.Fatalf("expected rejected state, got %s", result.State)
	}
	if result.Error != "not needed" {
		t.Fatalf("expected reason, got %q", result.Error)
	}

	// Idempotent.
	dup, err := inputSvc.RejectInput(ctx, req)
	if err != nil {
		t.Fatalf("idempotent RejectInput: %v", err)
	}
	if dup.Error != "not needed" {
		t.Fatalf("idempotent should preserve original reason, got %q", dup.Error)
	}
	conflictReq := req
	conflictReq.Reason = "different"
	if _, err := inputSvc.RejectInput(ctx, conflictReq); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("same requestID with different rejection must conflict, got %v", err)
	}
}

// ── Test: TaskID vs NodeID distinction ──────────────────────────────────────

func TestInputService_TaskIDNotNodeID(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Submit — affectedTaskIDs must use TaskID, not NodeID.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	result, err := inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"x"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "task-id-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	// TaskID should be "task-n1" (the actual TaskID from the input), not "n1" (the NodeID).
	if len(result.AffectedTaskIDs) != 1 || result.AffectedTaskIDs[0] != "task-n1" {
		t.Fatalf("affectedTaskIDs should contain TaskID 'task-n1', got %v", result.AffectedTaskIDs)
	}
}

// ── Test: Pin/unpin is independent of submit ─────────────────────────────────

func TestInputService_PinUnpin(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Pin before submit should fail.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	pinReq := PinInputRequest{
		WorkID: workID, InputID: inputID,
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "pin-early",
	}
	pinResult, err := inputSvc.PinInput(ctx, pinReq)
	if err != nil {
		t.Fatalf("PinInput: %v", err)
	}
	if pinResult.Error == "" {
		t.Fatal("expected pin-before-submit failure")
	}
	if !strings.Contains(pinResult.Error, "submitted") {
		t.Fatalf("expected 'submitted' in error, got %q", pinResult.Error)
	}

	// Submit first.
	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	_, err = inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"pinnable"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "sub-for-pin",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now pin.
	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	pinReq.RequestID = "pin-after-sub"
	pinReq.ExpectedRevision, pinReq.InputRevision, pinReq.DefinitionRev = workRev, inputRev, defRev
	pinResult, err = inputSvc.PinInput(ctx, pinReq)
	if err != nil {
		t.Fatalf("PinInput after submit: %v", err)
	}
	if pinResult.Error != "" {
		t.Fatalf("unexpected pin error: %s", pinResult.Error)
	}
	if !pinResult.Pinned {
		t.Fatal("expected pinned=true")
	}
	if pinResult.CornerstoneID == "" {
		t.Fatal("expected non-empty cornerstoneId")
	}

	// Verify input has cornerstoneId.
	work, _, _ := store.LoadState(workID, "")
	idx := findInputIndex(work, inputID)
	if idx < 0 {
		t.Fatal("input not found")
	}
	if work.V2Inputs[idx].CornerstoneID != pinResult.CornerstoneID {
		t.Fatalf("cornerstoneID mismatch: input has %q, result has %q",
			work.V2Inputs[idx].CornerstoneID, pinResult.CornerstoneID)
	}

	// Unpin.
	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	pinReq.ExpectedRevision, pinReq.InputRevision, pinReq.DefinitionRev = workRev, inputRev, defRev
	pinReq.RequestID = "unpin-after-sub"
	unpinResult, err := inputSvc.UnpinInput(ctx, pinReq)
	if err != nil {
		t.Fatalf("UnpinInput: %v", err)
	}
	if unpinResult.Error != "" {
		t.Fatalf("unexpected unpin error: %s", unpinResult.Error)
	}
	if unpinResult.Pinned {
		t.Fatal("expected pinned=false")
	}
	work, _, _ = store.LoadState(workID, "")
	removed := findCornerstone(work, pinResult.CornerstoneID)
	if removed == nil || !removed.Tombstone {
		t.Fatalf("unpin must tombstone Cornerstone through manager, got %#v", removed)
	}

	// Idempotent unpin.
	dupUnpin, _ := inputSvc.UnpinInput(ctx, pinReq)
	if !dupUnpin.Duplicate {
		t.Fatal("expected duplicate on idempotent unpin")
	}
}

// ── Test: Pin failure does not rollback submit ──────────────────────────────

func TestInputService_PinFailureDoesNotRollbackSubmit(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Submit.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	largeValue, _ := json.Marshal(strings.Repeat("independent-", CornerstoneInlineThreshold))
	submitResult, err := inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: largeValue,
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "sub-indep",
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitResult.Error != "" {
		t.Fatalf("submit failed: %s", submitResult.Error)
	}

	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	pinReq := PinInputRequest{WorkID: workID, InputID: inputID, ExpectedRevision: workRev,
		InputRevision: inputRev, DefinitionRev: defRev, RequestID: "pin-retry"}
	noBlob := NewInputService(store, NewCornerstoneManager(store, nil, RealClock{}))
	failed, err := noBlob.PinInput(ctx, pinReq)
	if err != nil || failed.Error == "" {
		t.Fatalf("expected manager pin failure result, got result=%#v err=%v", failed, err)
	}
	work, _, _ := store.LoadState(workID, "")
	idx := findInputIndex(work, inputID)
	if idx < 0 || work.V2Inputs[idx].State != InputSubmitted || work.V2Inputs[idx].CornerstoneID != "" ||
		string(work.V2Inputs[idx].Value) != string(largeValue) {
		t.Fatalf("input changed after pin failure: %#v", work.V2Inputs[idx])
	}
	restarted := NewInputService(store, NewCornerstoneManager(store, store, RealClock{}))
	retried, err := restarted.PinInput(ctx, pinReq)
	if err != nil || retried.Error != "" || !retried.Pinned || retried.CornerstoneID == "" {
		t.Fatalf("same-request pin retry failed: result=%#v err=%v", retried, err)
	}
}

func TestInputService_PinPartialCommitRetry(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	submitted, err := inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"partial"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "partial-submit",
	})
	if err != nil || submitted.Error != "" {
		t.Fatalf("submit: result=%#v err=%v", submitted, err)
	}
	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	req := PinInputRequest{WorkID: workID, InputID: inputID, ExpectedRevision: workRev,
		InputRevision: inputRev, DefinitionRev: defRev, RequestID: "partial-pin"}
	manager := NewCornerstoneManager(store, store, RealClock{})
	failingStore := &failInputLinkStore{WorkStore: store, fail: true}
	failingSvc := NewInputService(failingStore, manager)
	failingSvc.SetDefinitionStore(store)
	partial, err := failingSvc.PinInput(ctx, req)
	if err != nil || partial.Error == "" || !partial.Pinned || partial.CornerstoneID == "" {
		t.Fatalf("expected partial pin result: result=%#v err=%v", partial, err)
	}
	current, _, _ := store.LoadState(workID, "")
	idx := findInputIndex(current, inputID)
	if current.V2Inputs[idx].CornerstoneID != "" || findCornerstone(current, partial.CornerstoneID) == nil {
		t.Fatalf("partial state must keep manager result without input link: input=%#v cornerstones=%#v", current.V2Inputs[idx], current.Cornerstones)
	}
	restarted := NewInputService(failingStore, manager)
	restarted.SetDefinitionStore(store)
	recovered, err := restarted.PinInput(ctx, req)
	if err != nil || recovered.Error != "" || recovered.CornerstoneID != partial.CornerstoneID {
		t.Fatalf("partial pin recovery: result=%#v err=%v", recovered, err)
	}
	duplicate, err := restarted.PinInput(ctx, req)
	if err != nil || !duplicate.Duplicate || duplicate.CornerstoneID != recovered.CornerstoneID {
		t.Fatalf("completed pin replay: result=%#v err=%v", duplicate, err)
	}
}

// ── Test: Restart replay restores input state ───────────────────────────────

func TestInputService_RestartReplay(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Save draft and submit.
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	_, err := inputSvc.SaveDraft(ctx, SaveDraftRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"replay"`),
		Source: "user", UpdatedBy: "tester",
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "replay-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	workRev, inputRev, defRev = inputGuards(t, store, workID, inputID)
	submitReq := SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"replay"`),
		Source: "user", UpdatedBy: "tester",
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "replay-sub",
	}
	_, err = inputSvc.SubmitInput(ctx, submitReq)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate restart: reload from event log.
	wp, wpErr := store.workPath(workID)
	if wpErr != nil {
		t.Fatalf("workPath: %v", wpErr)
	}
	_, proj, err := ReplayWithReducer(wp, DefaultReducer())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if proj == nil {
		t.Fatal("replay returned nil projection")
	}
	ri := findInputIndex(proj, inputID)
	if ri < 0 {
		t.Fatal("input not found after replay")
	}
	replayed := proj.V2Inputs[ri]
	if replayed.State != InputSubmitted {
		t.Fatalf("expected submitted after replay, got %s", replayed.State)
	}
	if string(replayed.Value) != `"replay"` {
		t.Fatalf("expected value preserved, got %s", string(replayed.Value))
	}
	if replayed.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", replayed.Revision)
	}
	if replayed.Source != "user" {
		t.Fatalf("expected source preserved, got %q", replayed.Source)
	}
	receipt, ok := proj.V2InputReceipts[submitReq.RequestID]
	if !ok || receipt.Operation != "SubmitInput" || receipt.ResultInput == nil {
		t.Fatalf("authoritative submit receipt missing after replay: %#v", receipt)
	}
	loaded, err := store.LoadInputReceipt(workID, submitReq.RequestID)
	if err != nil || loaded.IntentDigest != receipt.IntentDigest {
		t.Fatalf("LoadInputReceipt: receipt=%#v err=%v", loaded, err)
	}
	restarted := NewInputService(store, svc.cornerstones)
	duplicate, err := restarted.SubmitInput(ctx, submitReq)
	if err != nil || !duplicate.Duplicate || duplicate.Revision != receipt.ResultRevision {
		t.Fatalf("restart duplicate: result=%#v err=%v", duplicate, err)
	}
}

// ── Test: Late input (stale revision) does not overwrite ────────────────────

func TestInputService_LateDraftDoesNotOverwrite(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	// Submit first (revision 1).
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	_, err := inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"latest"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "late-sub",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A late draft with old revision should be rejected.
	workRev, _, defRev = inputGuards(t, store, workID, inputID)
	lateResult, err := inputSvc.SaveDraft(ctx, SaveDraftRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"late"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "late-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lateResult.Error == "" {
		t.Fatal("expected late draft error")
	}

	// The submitted value is preserved.
	work, _, _ := store.LoadState(workID, "")
	idx := findInputIndex(work, inputID)
	if idx < 0 {
		t.Fatal("input not found")
	}
	in := work.V2Inputs[idx]
	if string(in.Value) != `"latest"` {
		t.Fatalf("expected 'latest', got %s", string(in.Value))
	}
	if in.State != InputSubmitted {
		t.Fatalf("expected submitted, got %s", in.State)
	}
}

// ── Test: Same requestID + same intent → same result, no extra revision ─────

func TestInputService_SameRequestIDSameIntent(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	req := SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"idem"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "idem-req",
	}

	r1, err := inputSvc.SubmitInput(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Duplicate {
		t.Fatal("first call should not be duplicate")
	}
	rev1 := r1.Revision

	r2, err := inputSvc.SubmitInput(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Duplicate {
		t.Fatal("second call should be duplicate")
	}
	if r2.Revision != rev1 {
		t.Fatalf("expected same revision %d, got %d", rev1, r2.Revision)
	}
	if r2.Input.Revision != 1 {
		t.Fatalf("expected input revision 1, got %d", r2.Input.Revision)
	}
}

// ── Test: Same requestID + different intent → conflict ──────────────────────

func TestInputService_SameRequestIDDifferentIntent(t *testing.T) {
	inputSvc, svc, store, _ := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	ctx := context.Background()

	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)

	// First submit.
	_, err := inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"first"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "conflict-req",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Same requestID, different value — should fail with conflict.
	_, err = inputSvc.SubmitInput(ctx, SubmitInputRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"second"`),
		ExpectedRevision: workRev, InputRevision: inputRev, DefinitionRev: defRev, RequestID: "conflict-req",
	})
	if err == nil {
		t.Fatal("expected conflict error for different intent with same requestID")
	}
	if !strings.Contains(err.Error(), "conflict") && !strings.Contains(err.Error(), "different intent") {
		t.Fatalf("expected conflict or different intent error, got %v", err)
	}
}

func TestInputCommitPreflightRejectsWithoutPollution(t *testing.T) {
	inputSvc, svc, store, dir := newInputServiceTest(t)
	workID, inputID := createV2WorkWithInput(t, svc, store)
	workRev, inputRev, defRev := inputGuards(t, store, workID, inputID)
	draft, err := inputSvc.SaveDraft(context.Background(), SaveDraftRequest{
		WorkID: workID, InputID: inputID, Value: json.RawMessage(`"baseline"`),
		DefinitionRev: defRev, InputRevision: inputRev, ExpectedRevision: workRev,
		RequestID: "preflight-baseline",
	})
	if err != nil || draft.Error != "" {
		t.Fatalf("SaveDraft baseline: result=%#v err=%v", draft, err)
	}
	current, before, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	idx := findInputIndex(current, inputID)
	if idx < 0 {
		t.Fatal("input missing")
	}
	input := current.V2Inputs[idx]
	originalInput, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	workPath, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	logPath := WorkEventLogPath(workPath)
	originalLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		request  string
		typ      WorkEventType
		payload  any
		expected int64
	}{
		{
			name: "stale draft revision", request: "preflight-stale", typ: EventInputDraftSaved,
			payload: InputDraftSavedPayload{
				InputID: inputID, WorkID: workID, RunID: input.RunID, TaskID: input.TaskID,
				BlockID: input.BlockID, SpecID: input.SpecID, Value: json.RawMessage(`"late"`),
				Revision: 1, ExpectedRevision: 99,
			},
			expected: 99,
		},
		{
			name: "cornerstone before submit", request: "preflight-state", typ: EventInputCornerstoneChanged,
			payload: InputCornerstoneChangedPayload{
				InputID: inputID, WorkID: workID,
				RunID: input.RunID, TaskID: input.TaskID, BlockID: input.BlockID, SpecID: input.SpecID,
				CornerstoneID: "c-invalid", Pinned: true,
				Revision: input.Revision + 1, ExpectedRevision: input.Revision,
			},
			expected: input.Revision,
		},
		{
			name: "same revision different state", request: "preflight-same-revision-state", typ: EventInputSubmitted,
			payload: InputSubmittedPayload{
				InputID: inputID, WorkID: workID,
				RunID: input.RunID, TaskID: input.TaskID, BlockID: input.BlockID, SpecID: input.SpecID,
				Value:    json.RawMessage(`"changed-state"`),
				Revision: input.Revision, ExpectedRevision: input.Revision,
			},
			expected: input.Revision,
		},
		{
			name: "same revision different value", request: "preflight-same-revision-value", typ: EventInputDraftSaved,
			payload: InputDraftSavedPayload{
				InputID: inputID, WorkID: workID, RunID: input.RunID, TaskID: input.TaskID,
				BlockID: input.BlockID, SpecID: input.SpecID, Value: json.RawMessage(`"changed-value"`),
				Revision: input.Revision, ExpectedRevision: input.Revision,
			},
			expected: input.Revision,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			event := newServiceEventV2(workID, tc.request, tc.typ, payload, time.Now().UTC())
			event.BaseRevision, event.Revision = before.Revision, before.Revision+1
			event.Object = ObjectContext{
				Kind: ObjectInput, ID: inputID, WorkID: workID,
				RunID: input.RunID, TaskID: input.TaskID, BlockID: input.BlockID, InputID: inputID,
				SpecID:           input.SpecID,
				ExpectedRevision: int64Ptr(tc.expected), DefinitionRevision: int64Ptr(current.V2CurrentRevision),
			}
			if _, err := store.CommitEvent(workID, event); err == nil {
				t.Fatal("expected preflight rejection")
			}
			afterWork, after, err := store.LoadState(workID, tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if after.Revision != before.Revision || after.RequestFound {
				t.Fatalf("preflight polluted revision/index: before=%#v after=%#v", before, after)
			}
			afterLog, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(originalLog, afterLog) {
				t.Fatal("preflight rejection changed authoritative log")
			}
			afterInput := afterWork.V2Inputs[findInputIndex(afterWork, inputID)]
			afterInputJSON, err := json.Marshal(afterInput)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(originalInput, afterInputJSON) {
				t.Fatalf("preflight rejection changed projection: %#v", afterInput)
			}

			restarted, err := NewFileWorkStore(dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			restartedWork, restartedState, err := restarted.LoadState(workID, tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if restartedState.Revision != before.Revision || restartedState.RequestFound {
				t.Fatalf("restart observed polluted revision/index: before=%#v after=%#v", before, restartedState)
			}
			restartedInput := restartedWork.V2Inputs[findInputIndex(restartedWork, inputID)]
			restartedInputJSON, err := json.Marshal(restartedInput)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(originalInput, restartedInputJSON) {
				t.Fatalf("restart observed polluted projection: %#v", restartedInput)
			}
		})
	}
}
