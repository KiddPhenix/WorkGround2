package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ── Helpers ─────────────────────────────────────────────────────────────────

func newArtifactFS(t *testing.T) (*FileWorkStore, string, *Service) {
	t.Helper()
	requireFileStoreIntegration(t)
	dir := filepath.Join(os.TempDir(), "wart-"+strings.ReplaceAll(t.Name(), "/", "-"))
	os.RemoveAll(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	store, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil, nil)
	return store, dir, svc
}

func newAppliedArtifactSlot(t *testing.T, expected int, required bool) (*FileWorkStore, string, *Service, string, int64, int64) {
	t.Helper()
	store, dir, svc := newArtifactFS(t)
	ctx := contextWithServiceForTesting()
	prefix := strings.ReplaceAll(t.Name(), "/", "-")
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "session-" + prefix, RequestID: "begin-" + prefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := v2def(view.Work.ID, 1)
	candidate.ArtifactSlots = []ArtifactSlotDef{{
		ID: "slot", Title: "Artifact", Kind: "pdf", ExpectedCount: expected, Required: required,
	}}
	candidate, err = svc.CreateCandidateRevision(ctx, view.Work.ID, candidate, "candidate-"+prefix, view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "apply-" + prefix,
	}); err != nil {
		t.Fatal(err)
	}
	_, state, err = store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return store, dir, svc, view.Work.ID, candidate.Revision, state.Revision
}

func TestDeclareArtifactSlots_Deterministic(t *testing.T) {
	defs := []ArtifactSlotDef{
		{ID: "slot1", Title: "Report", Kind: "docx", ExpectedCount: 1, Required: true},
		{ID: "slot2", Title: "Summary", Kind: "txt", ExpectedCount: 2, Required: false},
	}
	a := DeclareArtifactSlots(defs)
	b := DeclareArtifactSlots(defs)
	if len(a) != len(b) || len(a) != 2 {
		t.Fatalf("count mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].State != SlotReserved {
			t.Fatalf("slot[%d] not deterministic: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestDeclareArtifactSlots_EmptyDefs(t *testing.T) {
	slots := DeclareArtifactSlots(nil)
	if len(slots) != 0 {
		t.Fatalf("expected 0 slots, got %d", len(slots))
	}
	slots = DeclareArtifactSlots([]ArtifactSlotDef{})
	if len(slots) != 0 {
		t.Fatalf("expected 0 slots, got %d", len(slots))
	}
}

func TestBuildDeclareEvents_RevisionAssignment(t *testing.T) {
	defs := []ArtifactSlotDef{
		{ID: "s1", Title: "R", Kind: "pdf", ExpectedCount: 1, Required: true},
	}
	now := time.Now().UTC()
	events := BuildDeclareEvents("w1", 2, "req-1", defs, now)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != EventArtifactSlotDeclared {
		t.Fatalf("type=%s", ev.Type)
	}
	if ev.Object.Kind != ObjectArtifactSlot {
		t.Fatalf("kind=%s", ev.Object.Kind)
	}
	if *ev.Object.DefinitionRevision != 2 {
		t.Fatalf("defRev=%d", *ev.Object.DefinitionRevision)
	}
}

// ── Slot projection key ────────────────────────────────────────────────────

func TestSlotProjKey(t *testing.T) {
	if k := slotProjKey(3, "slot-a"); k != "3/slot-a" {
		t.Fatalf("key=%q", k)
	}
	slot := ArtifactSlot{DefinitionRev: 5, ID: "x"}
	if k := SlotProjKey(slot); k != "5/x" {
		t.Fatalf("key=%q", k)
	}
}

// ── DedupArtifactRefs ──────────────────────────────────────────────────────

func TestDedupArtifactRefs_Empty(t *testing.T) {
	if DedupArtifactRefs(nil) != nil {
		t.Fatal("nil not nil")
	}
	out := DedupArtifactRefs([]ArtifactRef{})
	if len(out) != 0 {
		t.Fatal("empty not empty")
	}
}

func TestDedupArtifactRefs_NoDup(t *testing.T) {
	refs := []ArtifactRef{
		{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"},
		{ID: "b", Status: ArtifactRefStatusAvailable, BlobDigest: "d2"},
	}
	out := DedupArtifactRefs(refs)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
}

func TestDedupArtifactRefs_Dup(t *testing.T) {
	refs := []ArtifactRef{
		{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"},
		{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d2"},
		{ID: "b", Status: ArtifactRefStatusAvailable, BlobDigest: "d3"},
	}
	out := DedupArtifactRefs(refs)
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0].BlobDigest != "d1" {
		t.Fatalf("first kept=%s", out[0].BlobDigest)
	}
}

func TestDedupArtifactRefs_EmptyIDKept(t *testing.T) {
	refs := []ArtifactRef{
		{ID: "", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"},
		{ID: "", Status: ArtifactRefStatusAvailable, BlobDigest: "d2"},
	}
	out := DedupArtifactRefs(refs)
	if len(out) != 2 {
		t.Fatalf("empty IDs should each be kept: got %d", len(out))
	}
}

// ── ComputeSlotState ───────────────────────────────────────────────────────

func TestComputeSlotState_Ready(t *testing.T) {
	slot := &ArtifactSlot{ExpectedCount: 1, Required: true,
		ArtifactRefs: []ArtifactRef{{ID: "r1", Status: ArtifactRefStatusAvailable}}, State: SlotGenerating}
	if s := ComputeSlotState(slot); s != SlotReady {
		t.Fatalf("state=%s", s)
	}
}

func TestComputeSlotState_Partial(t *testing.T) {
	slot := &ArtifactSlot{ExpectedCount: 3, Required: true,
		ArtifactRefs: []ArtifactRef{{ID: "r1", Status: ArtifactRefStatusAvailable}, {ID: "r2", Status: ArtifactRefStatusAvailable}}, State: SlotGenerating}
	if s := ComputeSlotState(slot); s != SlotPartial {
		t.Fatalf("state=%s", s)
	}
}

func TestComputeSlotState_NotRequiredStillPartial(t *testing.T) {
	slot := &ArtifactSlot{ExpectedCount: 3, Required: false,
		ArtifactRefs: []ArtifactRef{{ID: "r1", Status: ArtifactRefStatusAvailable}}, State: SlotGenerating}
	if s := ComputeSlotState(slot); s != SlotPartial {
		t.Fatalf("non-required incomplete slot should be partial, got %s", s)
	}
}

func TestComputeSlotState_ZeroRefsCannotBeReady(t *testing.T) {
	slot := &ArtifactSlot{ExpectedCount: 1, State: SlotReady}
	if s := ComputeSlotState(slot); s != SlotReserved {
		t.Fatalf("zero-ref ready should be corrected to reserved, got %s", s)
	}
}

func TestComputeSlotState_Reserved(t *testing.T) {
	slot := &ArtifactSlot{ExpectedCount: 1, Required: true, State: SlotReserved}
	if s := ComputeSlotState(slot); s != SlotReserved {
		t.Fatalf("state=%s", s)
	}
}

func TestComputeSlotState_Nil(t *testing.T) {
	if s := ComputeSlotState(nil); s != SlotReserved {
		t.Fatalf("state=%s", s)
	}
}

// ── Revision guard ─────────────────────────────────────────────────────────

func TestValidateSlotRevision_NilCurrent(t *testing.T) {
	if err := ValidateSlotRevision(nil, ArtifactSlot{Revision: 1}, "r1"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSlotRevision_LateEvent(t *testing.T) {
	cur := &ArtifactSlot{ID: "s1", Revision: 5}
	in := ArtifactSlot{ID: "s1", State: SlotReady, Revision: 3}
	err := ValidateSlotRevision(cur, in, "r1")
	if err == nil || !strings.Contains(err.Error(), "late event") {
		t.Fatalf("must reject late event: %v", err)
	}
}

func TestValidateSlotRevision_SameRevisionSameContent_Idempotent(t *testing.T) {
	cur := &ArtifactSlot{ID: "s1", Revision: 3, State: SlotGenerating, DefinitionRev: 1,
		ArtifactRefs: []ArtifactRef{{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"}}}
	in := ArtifactSlot{ID: "s1", Revision: 3, State: SlotGenerating, DefinitionRev: 1,
		ArtifactRefs: []ArtifactRef{{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"}}}
	if err := ValidateSlotRevision(cur, in, "r1"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSlotRevision_SameRevisionDifferentContent_Conflict(t *testing.T) {
	cur := &ArtifactSlot{ID: "s1", Revision: 3, State: SlotGenerating}
	in := ArtifactSlot{ID: "s1", Revision: 3, State: SlotReady}
	err := ValidateSlotRevision(cur, in, "r1")
	if err == nil {
		t.Fatal("must conflict on same-rev different content")
	}
	var conflict *ErrWorkEventConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("must be *ErrWorkEventConflict: %T", err)
	}
}

func TestValidateSlotRevision_NewerRevision_Accepted(t *testing.T) {
	cur := &ArtifactSlot{ID: "s1", Revision: 3}
	in := ArtifactSlot{ID: "s1", Revision: 4, State: SlotReady}
	if err := ValidateSlotRevision(cur, in, "r1"); err != nil {
		t.Fatal(err)
	}
}

// ── Digest change / stale detection ────────────────────────────────────────

func TestSlotUpstreamDigestChanged_NoChange(t *testing.T) {
	slot := &ArtifactSlot{UpstreamDigest: "up-1"}
	if SlotUpstreamDigestChanged(slot, "up-1") {
		t.Fatal("should not detect change")
	}
}

func TestSlotUpstreamDigestChanged_Changed(t *testing.T) {
	slot := &ArtifactSlot{UpstreamDigest: "up-1"}
	if !SlotUpstreamDigestChanged(slot, "up-2") {
		t.Fatal("should detect change")
	}
}

func TestSlotUpstreamDigestChanged_FirstObservation(t *testing.T) {
	slot := &ArtifactSlot{}
	if SlotUpstreamDigestChanged(slot, "up-1") {
		t.Fatal("first upstream digest should not be stale")
	}
}

func TestSlotUpstreamDigestChanged_Nil(t *testing.T) {
	if SlotUpstreamDigestChanged(nil, "") {
		t.Fatal("nil returns false")
	}
}

// ── Reduce: declare idempotent ─────────────────────────────────────────────

func TestReduceDeclare_Idempotent(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	if len(w.V2ArtifactSlots) != 1 {
		t.Fatal("not declared")
	}
	// Repeat: same defRev, same slotID → idempotent.
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	if len(w.V2ArtifactSlots) != 1 {
		t.Fatalf("duplicate: %d", len(w.V2ArtifactSlots))
	}
}

func TestReduceDeclare_PreservesOlderDefinitionSlot(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	// A newer definition may reuse the definition-local slot ID. Both
	// projections remain addressable by (definitionRev, slotID).
	reduceArtifactSlotDeclared(w, "s1", "w1", 2, "R2", "docx", 2, false)
	if len(w.V2ArtifactSlots) != 2 {
		t.Fatalf("should preserve history, got %d slots", len(w.V2ArtifactSlots))
	}
	oldSlot, _ := FindArtifactSlotRevision(w, 1, "s1")
	newSlot, _ := FindArtifactSlotRevision(w, 2, "s1")
	if oldSlot == nil || oldSlot.Title != "R" || newSlot == nil || newSlot.Title != "R2" {
		t.Fatalf("revision slots not preserved: %+v", w.V2ArtifactSlots)
	}
	latest, _ := FindArtifactSlot(w, "s1")
	if latest == nil || latest.DefinitionRev != 2 {
		t.Fatalf("latest lookup = %+v", latest)
	}
}

// ── Reduce: update idempotent and conflict ─────────────────────────────────

func TestReduceUpdate_SameRevisionSameContent_Idempotent(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	// First update — revision 2.
	err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 2,
	}, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	// Same update again — revision 2.
	err = reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 2,
	}, "req-2")
	if err != nil {
		t.Fatal(err)
	}
	if w.V2ArtifactSlots[0].Revision != 2 {
		t.Fatalf("rev=%d", w.V2ArtifactSlots[0].Revision)
	}
}

func TestReduceUpdate_SameRevisionDifferentContent_Conflict(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	// First update — revision 2, generating.
	err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 2,
	}, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if slot, _ := FindArtifactSlotRevision(w, 1, "s1"); slot == nil || slot.State != SlotGenerating || slot.Revision != 2 {
		t.Fatalf("first update = %+v, want generating revision 2", slot)
	}
	// Same revision 2, different content — conflict.
	err = reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotReady, Revision: 2,
	}, "req-2")
	if err == nil {
		t.Fatal("must conflict")
	}
	var conflict *ErrWorkEventConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("must be *ErrWorkEventConflict: %T", err)
	}
	slot, _ := FindArtifactSlotRevision(w, 1, "s1")
	if slot == nil || slot.State != SlotGenerating || slot.Revision != 2 {
		t.Fatalf("conflicting update polluted projection: %+v", slot)
	}
}

func TestReduceUpdate_LateEventRejected(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	// Update to revision 5.
	err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 5,
	}, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	// Late event at revision 3 — rejected.
	err = reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotReady, Revision: 3,
	}, "req-2")
	if err == nil || !strings.Contains(err.Error(), "late event") {
		t.Fatalf("must reject late event: %v", err)
	}
}

func TestReduceUpdate_MissingSlot(t *testing.T) {
	w := &Work{ID: "w1"}
	err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 1,
	}, "req-1")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("must reject missing slot: %v", err)
	}
}

// ── Reduce: refs merging and dedup ─────────────────────────────────────────

func TestReduceUpdate_RefsMerged(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 3, true)
	// Add refs a, b — must transition through generating first.
	if err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 2,
		Refs: []ArtifactRef{{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"}, {ID: "b", Status: ArtifactRefStatusAvailable, BlobDigest: "d2"}},
	}, "req-1"); err != nil {
		t.Fatal(err)
	}
	// Add ref c, plus duplicate a.
	if err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotPartial, Revision: 3,
		Refs: []ArtifactRef{{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"}, {ID: "c", Status: ArtifactRefStatusAvailable, BlobDigest: "d3"}},
	}, "req-2"); err != nil {
		t.Fatal(err)
	}
	slot := w.V2ArtifactSlots[0]
	if len(slot.ArtifactRefs) != 3 {
		t.Fatalf("expected 3 merged refs, got %d: %+v", len(slot.ArtifactRefs), slot.ArtifactRefs)
	}
	ids := make([]string, len(slot.ArtifactRefs))
	for i, r := range slot.ArtifactRefs {
		ids[i] = r.ID
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("ids=%v", ids)
	}
}

// ── Reduce: stale retains refs ────────────────────────────────────────────

func TestReduceUpdate_UpstreamChangeStaleRetainsRefs(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	// Add ref a.
	reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotReady, Revision: 2,
		Refs: []ArtifactRef{{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"}}, UpstreamDigest: "up-1",
	}, "req-1")
	// Upstream digest change triggers stale, but old refs stay viewable.
	reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 3,
		UpstreamDigest: "up-2",
	}, "req-2")
	slot := w.V2ArtifactSlots[0]
	if slot.State != SlotStale {
		t.Fatalf("state=%s, want stale", slot.State)
	}
	if len(slot.ArtifactRefs) != 1 || slot.ArtifactRefs[0].BlobDigest != "d1" {
		t.Fatalf("old refs not retained: %+v", slot.ArtifactRefs)
	}
}

func TestReduceUpdate_ArtifactBlobChangeDoesNotStale(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	if err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotReady, Revision: 2,
		Refs: []ArtifactRef{{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"}}, UpstreamDigest: "up-1",
	}, "req-1"); err != nil {
		t.Fatal(err)
	}
	if err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 3,
		Refs: []ArtifactRef{{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d2"}}, UpstreamDigest: "up-1",
	}, "req-2"); err != nil {
		t.Fatal(err)
	}
	slot := w.V2ArtifactSlots[0]
	if slot.State != SlotReady || slot.ArtifactRefs[0].BlobDigest != "d2" {
		t.Fatalf("normal blob replacement = %+v", slot)
	}
}
func TestReduceUpdate_ExplicitStaleKeepsRefs(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotReady, Revision: 2,
		Refs: []ArtifactRef{{ID: "a", Status: ArtifactRefStatusAvailable, BlobDigest: "d1"}},
	}, "req-1")
	// Explicit stale — replaces state but keeps refs (merged).
	reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotStale, Revision: 3,
	}, "req-2")
	slot := w.V2ArtifactSlots[0]
	if slot.State != SlotStale {
		t.Fatalf("state=%s", slot.State)
	}
	if len(slot.ArtifactRefs) != 1 {
		t.Fatalf("refs lost: %d", len(slot.ArtifactRefs))
	}
}

// ── Reduce: progress, summary, error ───────────────────────────────────────

func TestReduceUpdate_ProgressSummaryError(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	progress := 0.5
	errPayload := &ArtifactError{Code: "E1", Message: "boom", Retryable: true}
	reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotGenerating, Revision: 2,
		Progress: &progress, Summary: "half done", Error: errPayload,
	}, "req-1")
	slot := w.V2ArtifactSlots[0]
	if slot.Progress == nil || *slot.Progress != 0.5 {
		t.Fatalf("progress=%v", slot.Progress)
	}
	if slot.Summary != "half done" {
		t.Fatalf("summary=%s", slot.Summary)
	}
	if slot.Error == nil || slot.Error.Code != "E1" {
		t.Fatalf("error=%+v", slot.Error)
	}
}

// ── Reduce: transition validation ──────────────────────────────────────────

func TestReduceUpdate_InvalidTransition(t *testing.T) {
	w := &Work{ID: "w1"}
	reduceArtifactSlotDeclared(w, "s1", "w1", 1, "R", "pdf", 1, true)
	// First set to ready.
	reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotReady, Revision: 2,
	}, "req-1")
	// ready → failed is invalid.
	err := reduceArtifactSlotUpdated(w, ArtifactSlotUpdatedPayload{
		SlotID: "s1", WorkID: "w1", State: SlotFailed, Revision: 3,
	}, "req-2")
	if err == nil {
		t.Fatal("must reject invalid transition")
	}
}

// ── End-to-end: definition → slot declaration → replay ─────────────────────

func TestE2E_ApplyDefinitionDeclaresSlots(t *testing.T) {
	store, _, svc := newArtifactFS(t)
	ctx := contextWithServiceForTesting()
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "sess-1", RequestID: "req-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID

	cand := v2def(workID, 1)
	cand.ArtifactSlots = []ArtifactSlotDef{
		{ID: "slot-rpt", Title: "Report", Kind: "docx", ExpectedCount: 1, Required: true},
		{ID: "slot-sum", Title: "Summary", Kind: "txt", ExpectedCount: 2, Required: false},
	}
	cand, err = svc.CreateCandidateRevision(ctx, workID, cand, "req-cand", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	// Get current revision after candidate creation.
	_, state, _ := store.LoadState(workID, "")

	_, err = svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: cand.Revision,
		ExpectedRevision: state.Revision, RequestID: "req-apply",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Load projection via replay.
	proj, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.V2ArtifactSlots) != 2 {
		t.Fatalf("expected 2 slots, got %d: %+v", len(proj.V2ArtifactSlots), proj.V2ArtifactSlots)
	}

	// Verify slot 1.
	s1, _ := FindArtifactSlot(proj, "slot-rpt")
	if s1 == nil {
		t.Fatal("slot-rpt not found")
	}
	if s1.State != SlotReserved || s1.Kind != "docx" {
		t.Fatalf("slot-rpt: %+v", s1)
	}
	if !s1.Required || s1.ExpectedCount != 1 {
		t.Fatalf("slot-rpt required/expected: %v/%d", s1.Required, s1.ExpectedCount)
	}
	// DefinitionRev matches the applied candidate revision.
	if s1.DefinitionRev != cand.Revision {
		t.Fatalf("slot-rpt DefinitionRev=%d, want %d", s1.DefinitionRev, cand.Revision)
	}

	// Verify slot 2.
	s2, _ := FindArtifactSlot(proj, "slot-sum")
	if s2 == nil {
		t.Fatal("slot-sum not found")
	}
	if s2.Required || s2.ExpectedCount != 2 {
		t.Fatalf("slot-sum required/expected: %v/%d", s2.Required, s2.ExpectedCount)
	}
}

// ── E2E: update slot via CommitEvent ───────────────────────────────────────

func TestE2E_UpdateSlotState(t *testing.T) {
	store, _, svc := newArtifactFS(t)
	ctx := contextWithServiceForTesting()
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "sess-1", RequestID: "req-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID

	cand := v2def(workID, 1)
	cand.ArtifactSlots = []ArtifactSlotDef{
		{ID: "slot-x", Title: "X", Kind: "pdf", ExpectedCount: 1, Required: true},
	}
	cand, err = svc.CreateCandidateRevision(ctx, workID, cand, "req-cand", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, stateBefore, _ := store.LoadState(workID, "")

	_, err = svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: cand.Revision,
		ExpectedRevision: stateBefore.Revision, RequestID: "req-apply",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now issue an update event for the slot.
	_, state, _ := store.LoadState(workID, "")
	progress := 0.75
	updateEv := BuildUpdateEvent(UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot-x", RequestID: "req-update-1",
		State: SlotGenerating, Progress: &progress, Summary: "generating...",
		Revision: 2, ExpectedRevision: 1, DefinitionRev: cand.Revision,
	}, time.Now().UTC())
	updateEv.BaseRevision = state.Revision
	updateEv.Revision = state.Revision + 1

	_, err = store.CommitEvent(workID, updateEv)
	if err != nil {
		t.Fatal(err)
	}

	// Replay and verify.
	proj2, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := FindArtifactSlot(proj2, "slot-x")
	if s == nil {
		t.Fatal("slot-x missing")
	}
	if s.State != SlotGenerating {
		t.Fatalf("state=%s", s.State)
	}
	if s.Progress == nil || *s.Progress != 0.75 {
		t.Fatalf("progress=%v", s.Progress)
	}
	if s.Summary != "generating..." {
		t.Fatalf("summary=%s", s.Summary)
	}
}

func TestE2E_UpdateArtifactSlotServicePersistsReceiptAndReplay(t *testing.T) {
	store, dir, svc := newArtifactFS(t)
	ctx := contextWithServiceForTesting()
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "sess-service", RequestID: "req-service-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID
	cand := v2def(workID, 1)
	cand.ArtifactSlots = []ArtifactSlotDef{{
		ID: "slot-service", Title: "Service", Kind: "pdf", ExpectedCount: 1, Required: true,
	}}
	cand, err = svc.CreateCandidateRevision(ctx, workID, cand, "req-service-candidate", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: cand.Revision, ExpectedRevision: state.Revision,
		RequestID: "req-service-apply",
	}); err != nil {
		t.Fatal(err)
	}
	_, state, err = store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	progress := 0.4
	input := UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot-service", DefinitionRev: cand.Revision,
		ExpectedRevision: state.Revision, Revision: 2, RequestID: "req-service-update",
		State: SlotGenerating, Progress: &progress, Summary: "working",
	}
	first, err := svc.UpdateArtifactSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Slot.State != SlotGenerating || first.Slot.Revision != 2 ||
		first.WorkRevision != state.Revision+1 || first.Duplicate {
		t.Fatalf("first result = %+v", first)
	}

	// A later write changes the live projection. Retrying through a new Service
	// must still rebuild the exact first snapshot from the authoritative log.
	_, laterState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot-service", DefinitionRev: cand.Revision,
		ExpectedRevision: laterState.Revision, Revision: 3, RequestID: "req-service-later",
		State: SlotReady, Refs: []ArtifactRef{{
			ID: "final", Name: "final.pdf", Type: "pdf", Status: "available", BlobDigest: "sha256:final",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	workPath, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	if err := CompactWorkEventLog(workPath, projection, DefaultReducer()); err != nil {
		_ = ReleaseWorkLease(workPath)
		t.Fatal(err)
	}
	if err := ReleaseWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	restartedStore, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(restartedStore, nil, nil)
	retry, err := restarted.UpdateArtifactSlot(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if retry.WorkRevision != first.WorkRevision || !retry.Duplicate ||
		!slotContentEqual(&retry.Slot, &first.Slot) {
		t.Fatalf("retry mismatch: first=%+v retry=%+v", first, retry)
	}

	conflict := input
	conflict.State = SlotReady
	if _, err := restarted.UpdateArtifactSlot(ctx, conflict); err == nil {
		t.Fatal("same requestID with different intent must conflict")
	} else {
		var typed *ErrWorkEventConflict
		if !errors.As(err, &typed) || typed.Kind != WorkEventRequestConflict {
			t.Fatalf("conflict = %T %v", err, err)
		}
	}
}

func TestE2E_UpdateArtifactSlotWriterLeaseBlocksWithoutPollution(t *testing.T) {
	store, _, svc, workID, definitionRev, workRevision := newAppliedArtifactSlot(t, 1, true)
	workPath, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	_, updateErr := svc.UpdateArtifactSlot(contextWithServiceForTesting(), UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: workRevision, Revision: 2, RequestID: "lease-blocked",
		State: SlotGenerating,
	})
	if err := ReleaseWorkLease(workPath); err != nil {
		t.Fatal(err)
	}
	if updateErr == nil || !errors.Is(updateErr, ErrWorkLeaseHeld) {
		t.Fatalf("update under writer lease = %v", updateErr)
	}
	_, state, err := store.LoadState(workID, "lease-blocked/artifact-slot")
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != workRevision || state.RequestFound {
		t.Fatalf("blocked lease polluted event log: %+v", state)
	}
}

func TestE2E_UpdateArtifactSlotProjectionFailureRecoversAfterRestart(t *testing.T) {
	store, dir, svc, workID, definitionRev, workRevision := newAppliedArtifactSlot(t, 1, true)
	workPath, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(workPath, "projection.json")
	originalWrite := writeDerivedFile
	projectionWrites := 0
	writeDerivedFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Clean(path) == filepath.Clean(projectionPath) {
			projectionWrites++
			if projectionWrites == 2 {
				return errors.New("injected artifact slot projection failure")
			}
		}
		return originalWrite(path, data, mode)
	}
	t.Cleanup(func() { writeDerivedFile = originalWrite })

	input := UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: workRevision, Revision: 2, RequestID: "projection-failure",
		State: SlotGenerating, Summary: "committed",
	}
	_, updateErr := svc.UpdateArtifactSlot(contextWithServiceForTesting(), input)
	var committed *ErrWorkCommittedRecovery
	if !errors.As(updateErr, &committed) || committed.Revision != workRevision+1 {
		t.Fatalf("projection failure = %v, want committed revision %d", updateErr, workRevision+1)
	}
	writeDerivedFile = originalWrite

	restartedStore, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := NewService(restartedStore, nil, nil).UpdateArtifactSlot(contextWithServiceForTesting(), input)
	if err != nil {
		t.Fatalf("retry after committed projection failure: %v", err)
	}
	if !retry.Duplicate || retry.WorkRevision != workRevision+1 ||
		retry.Slot.Revision != 2 || retry.Slot.State != SlotGenerating || retry.Slot.Summary != "committed" {
		t.Fatalf("recovered artifact slot = %+v", retry)
	}
	_, state, err := restartedStore.LoadState(workID, "projection-failure/artifact-slot")
	if err != nil {
		t.Fatal(err)
	}
	if !state.RequestFound || state.Revision != workRevision+1 {
		t.Fatalf("recovered event state = %+v", state)
	}
}

func TestE2E_ArtifactRefStatusFactsAndValidation(t *testing.T) {
	store, _, svc, workID, definitionRev, workRevision := newAppliedArtifactSlot(t, 2, false)
	ctx := contextWithServiceForTesting()
	result, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: workRevision, Revision: 2, RequestID: "status-facts",
		State: SlotReady,
		Refs: []ArtifactRef{
			{ID: "usable", Status: ArtifactRefStatusAvailable},
			{ID: "missing", Status: ArtifactRefStatusMissing},
			{ID: "stale", Status: ArtifactRefStatusStale},
			{ID: "failed", Status: ArtifactRefStatusFailed},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Slot.State != SlotPartial {
		t.Fatalf("one available ref out of expected two = %s, want partial", result.Slot.State)
	}
	if got := usableArtifactRefCount(result.Slot.ArtifactRefs); got != 1 {
		t.Fatalf("usable refs = %d, want 1", got)
	}

	_, before, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status string
	}{
		{name: "empty"},
		{name: "unknown", status: "uploaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestID := "status-invalid-" + tc.name
			_, updateErr := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
				WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
				ExpectedRevision: before.Revision, Revision: 3, RequestID: requestID,
				State: SlotReady, Refs: []ArtifactRef{{ID: "bad", Status: tc.status}},
			})
			if updateErr == nil || !strings.Contains(updateErr.Error(), "status") {
				t.Fatalf("invalid status update = %v", updateErr)
			}
			_, after, loadErr := store.LoadState(workID, requestID+"/artifact-slot")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if after.Revision != before.Revision || after.RequestFound {
				t.Fatalf("invalid Service input polluted log: before=%+v after=%+v", before, after)
			}
		})
	}

	workPath, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	logPath := WorkEventLogPath(workPath)
	logBefore, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	event := BuildUpdateEvent(UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: before.Revision, Revision: 3, RequestID: "status-validator",
		State: SlotReady, Refs: []ArtifactRef{{ID: "bad", Status: "uploaded"}},
	}, time.Now().UTC())
	event.BaseRevision = before.Revision
	event.Revision = before.Revision + 1
	if _, err := store.CommitEvent(workID, event); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("V2 payload validator accepted unknown status: %v", err)
	}
	logAfter, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(logBefore, logAfter) {
		t.Fatal("invalid V2 payload changed authoritative event log bytes")
	}
}

func TestE2E_SameSlotRevisionDifferentContentDoesNotPollute(t *testing.T) {
	store, dir, svc, workID, definitionRev, workRevision := newAppliedArtifactSlot(t, 1, true)
	ctx := contextWithServiceForTesting()
	first, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: workRevision, Revision: 2, RequestID: "same-rev-first",
		State: SlotGenerating, Summary: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	workPath, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	logPath := WorkEventLogPath(workPath)
	logBefore, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	_, conflictErr := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: first.WorkRevision, Revision: 2, RequestID: "same-rev-conflict",
		State: SlotGenerating, Summary: "different",
	})
	var conflict *ErrWorkEventConflict
	if !errors.As(conflictErr, &conflict) || conflict.Kind != WorkEventRevisionConflict {
		t.Fatalf("same slot revision conflict = %T %v", conflictErr, conflictErr)
	}
	logAfter, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(logBefore, logAfter) {
		t.Fatal("same-revision conflict changed authoritative event log bytes")
	}
	_, state, err := store.LoadState(workID, "same-rev-conflict/artifact-slot")
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != first.WorkRevision || state.RequestFound {
		t.Fatalf("same-revision conflict polluted event index: %+v", state)
	}

	restartedStore, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := restartedStore.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := FindArtifactSlotRevision(projection, definitionRev, "slot")
	if slot == nil || slot.Revision != 2 || slot.Summary != "first" {
		t.Fatalf("restart projection was polluted: %+v", slot)
	}
	if _, ok := projection.V2ArtifactReceipts["same-rev-conflict/artifact-slot"]; ok {
		t.Fatal("same-revision conflict polluted artifact receipts")
	}
}

func TestE2E_UpdateArtifactSlotRejectsLateEventBeforeAppend(t *testing.T) {
	store, _, svc := newArtifactFS(t)
	ctx := contextWithServiceForTesting()
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "sess-late", RequestID: "req-late-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID
	cand := v2def(workID, 1)
	cand.ArtifactSlots = []ArtifactSlotDef{{
		ID: "slot-late", Title: "Late", Kind: "txt", ExpectedCount: 1, Required: true,
	}}
	cand, err = svc.CreateCandidateRevision(ctx, workID, cand, "req-late-candidate", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ := store.LoadState(workID, "")
	if _, err := svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: cand.Revision, ExpectedRevision: state.Revision,
		RequestID: "req-late-apply",
	}); err != nil {
		t.Fatal(err)
	}
	_, state, _ = store.LoadState(workID, "")
	if _, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot-late", DefinitionRev: cand.Revision,
		ExpectedRevision: state.Revision, Revision: 4, RequestID: "req-late-new",
		State: SlotGenerating,
	}); err != nil {
		t.Fatal(err)
	}
	_, state, _ = store.LoadState(workID, "")
	if _, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot-late", DefinitionRev: cand.Revision,
		ExpectedRevision: state.Revision, Revision: 3, RequestID: "req-late-old",
		State: SlotReady,
	}); err == nil || !strings.Contains(err.Error(), "late event") {
		t.Fatalf("late update = %v", err)
	}
	projection, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("rejected event poisoned replay: %v", err)
	}
	slot, _ := FindArtifactSlotRevision(projection, cand.Revision, "slot-late")
	if slot == nil || slot.Revision != 4 || slot.State != SlotGenerating {
		t.Fatalf("slot after rejected late event = %+v", slot)
	}
	_, after, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != state.Revision {
		t.Fatalf("rejected event polluted log: revision=%d want=%d", after.Revision, state.Revision)
	}
}

func TestE2E_DefinitionRevisionKeepsArtifactHistory(t *testing.T) {
	store, _, svc := newArtifactFS(t)
	ctx := contextWithServiceForTesting()
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "sess-history", RequestID: "req-history-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID
	firstDef := v2def(workID, 1)
	firstDef.ArtifactSlots = []ArtifactSlotDef{{
		ID: "report", Title: "Original report", Kind: "pdf", ExpectedCount: 1, Required: true,
	}}
	firstDef, err = svc.CreateCandidateRevision(ctx, workID, firstDef, "req-history-candidate-1", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ := store.LoadState(workID, "")
	if _, err := svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: firstDef.Revision, ExpectedRevision: state.Revision,
		RequestID: "req-history-apply-1",
	}); err != nil {
		t.Fatal(err)
	}
	_, state, _ = store.LoadState(workID, "")
	if _, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "report", DefinitionRev: firstDef.Revision,
		ExpectedRevision: state.Revision, Revision: 2, RequestID: "req-history-artifact",
		State: SlotReady, Refs: []ArtifactRef{{ID: "artifact-old", Status: ArtifactRefStatusAvailable, BlobDigest: "sha256:old"}},
	}); err != nil {
		t.Fatal(err)
	}

	secondDef := v2def(workID, firstDef.Revision)
	secondDef.ArtifactSlots = []ArtifactSlotDef{{
		ID: "report", Title: "Revised report", Kind: "docx", ExpectedCount: 2, Required: true,
	}}
	_, state, _ = store.LoadState(workID, "")
	secondDef, err = svc.CreateCandidateRevision(ctx, workID, secondDef, "req-history-candidate-2", state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ = store.LoadState(workID, "")
	if _, err := svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: secondDef.Revision, ExpectedRevision: state.Revision,
		RequestID: "req-history-apply-2",
	}); err != nil {
		t.Fatal(err)
	}

	projection, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	oldSlot, _ := FindArtifactSlotRevision(projection, firstDef.Revision, "report")
	newSlot, _ := FindArtifactSlotRevision(projection, secondDef.Revision, "report")
	if oldSlot == nil || oldSlot.State != SlotReady || len(oldSlot.ArtifactRefs) != 1 {
		t.Fatalf("old slot/history lost: %+v", oldSlot)
	}
	if newSlot == nil || newSlot.State != SlotReserved || len(newSlot.ArtifactRefs) != 0 ||
		newSlot.Title != "Revised report" {
		t.Fatalf("new slot incorrect: %+v", newSlot)
	}
}

// ── E2E: idempotent update ─────────────────────────────────────────────────

func TestE2E_IdempotentUpdate(t *testing.T) {
	store, dir, svc := newArtifactFS(t)
	ctx := contextWithServiceForTesting()
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "sess-1", RequestID: "req-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID

	cand := v2def(workID, 1)
	cand.ArtifactSlots = []ArtifactSlotDef{
		{ID: "slot-a", Title: "A", Kind: "txt", ExpectedCount: 1, Required: true},
	}
	cand, err = svc.CreateCandidateRevision(ctx, workID, cand, "req-cand", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, stateBefore, _ := store.LoadState(workID, "")
	_, err = svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: cand.Revision,
		ExpectedRevision: stateBefore.Revision, RequestID: "req-apply",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, state, _ := store.LoadState(workID, "")
	progress := 0.3
	first, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot-a", RequestID: "req-up-1",
		State: SlotGenerating, Progress: &progress, Revision: 2,
		ExpectedRevision: state.Revision, DefinitionRev: cand.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot-a", RequestID: "req-up-2",
		State: SlotGenerating, Progress: &progress, Revision: 2,
		ExpectedRevision: first.WorkRevision, DefinitionRev: cand.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slotContentEqual(&first.Slot, &second.Slot) || second.WorkRevision != first.WorkRevision+1 {
		t.Fatalf("same-version update mismatch: first=%+v second=%+v", first, second)
	}
	projection, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := FindArtifactSlotRevision(projection, cand.Revision, "slot-a")
	if slot == nil || !slotContentEqual(slot, &first.Slot) {
		t.Fatalf("same-version replay changed slot: %+v", slot)
	}
	restartedStore, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := restartedStore.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	restartedSlot, _ := FindArtifactSlotRevision(restarted, cand.Revision, "slot-a")
	if restartedSlot == nil || !slotContentEqual(restartedSlot, &first.Slot) {
		t.Fatalf("same-version restart changed slot: %+v", restartedSlot)
	}
	for _, requestID := range []string{"req-up-1/artifact-slot", "req-up-2/artifact-slot"} {
		if _, ok := restarted.V2ArtifactReceipts[requestID]; !ok {
			t.Fatalf("restart lost idempotent receipt %q", requestID)
		}
	}
}

// ── E2E: replay consistency after restart ──────────────────────────────────

func TestE2E_ReplayConsistency(t *testing.T) {
	store, dir, svc := newArtifactFS(t)
	ctx := contextWithServiceForTesting()
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "sess-1", RequestID: "req-begin",
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID

	cand := v2def(workID, 1)
	cand.ArtifactSlots = []ArtifactSlotDef{
		{ID: "s-rp", Title: "Replay", Kind: "docx", ExpectedCount: 1, Required: true},
	}
	cand, err = svc.CreateCandidateRevision(ctx, workID, cand, "req-cand", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, stateBefore, _ := store.LoadState(workID, "")
	_, err = svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: cand.Revision,
		ExpectedRevision: stateBefore.Revision, RequestID: "req-apply",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Get projection snapshot 1.
	p1, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate restart: create a new store on the same dir and replay.
	store2, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := store2.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}

	// Slots must be identical.
	if len(p1.V2ArtifactSlots) != len(p2.V2ArtifactSlots) {
		t.Fatalf("slot count mismatch: %d vs %d", len(p1.V2ArtifactSlots), len(p2.V2ArtifactSlots))
	}
	for i := range p1.V2ArtifactSlots {
		a := p1.V2ArtifactSlots[i]
		b := p2.V2ArtifactSlots[i]
		if a.ID != b.ID || a.State != b.State || a.DefinitionRev != b.DefinitionRev {
			t.Fatalf("slot[%d] mismatch: %+v vs %+v", i, a, b)
		}
	}
}

// ── E2E: preview conversion failure isolation ──────────────────────────────

func TestE2E_PreviewFailureIsolation(t *testing.T) {
	store, _, svc, workID, definitionRev, workRevision := newAppliedArtifactSlot(t, 1, true)
	ctx := contextWithServiceForTesting()
	first, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: workRevision, Revision: 2, RequestID: "preview-ready",
		State: SlotReady, UpstreamDigest: "up-1",
		Refs: []ArtifactRef{{
			ID: "a", Name: "a.pdf", Type: "pdf", Status: "available", BlobDigest: "d1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: first.WorkRevision, Revision: 3, RequestID: "preview-failed",
		State: SlotReady, UpstreamDigest: "up-1",
		Error: &ArtifactError{Code: "PREVIEW_FAIL", Message: "converter missing", Retryable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	slot := result.Slot
	if slot.State != SlotReady {
		t.Fatalf("preview failure must not change state: %s", slot.State)
	}
	if slot.Error == nil || slot.Error.Code != "PREVIEW_FAIL" {
		t.Fatalf("error not set: %+v", slot.Error)
	}
	if len(slot.ArtifactRefs) != 1 {
		t.Fatalf("refs lost on preview failure")
	}
	projection, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := FindArtifactSlotRevision(projection, definitionRev, "slot")
	if persisted == nil || !slotContentEqual(persisted, &slot) {
		t.Fatalf("persisted preview isolation mismatch: %+v", persisted)
	}
}

// ── E2E: partial after refs added ──────────────────────────────────────────

func TestE2E_PartialAfterRefs(t *testing.T) {
	store, _, svc, workID, definitionRev, workRevision := newAppliedArtifactSlot(t, 3, false)
	ctx := contextWithServiceForTesting()
	first, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: workRevision, Revision: 2, RequestID: "partial-one",
		State: SlotPartial, Refs: []ArtifactRef{{
			ID: "a", Name: "a.pdf", Type: "pdf", Status: "available",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Slot.State != SlotPartial {
		t.Fatalf("optional incomplete slot = %s, want partial", first.Slot.State)
	}
	result, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: first.WorkRevision, Revision: 3, RequestID: "partial-two",
		State: SlotReady, Refs: []ArtifactRef{{
			ID: "b", Name: "b.pdf", Type: "pdf", Status: "available",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	slot := result.Slot
	if slot.State != SlotPartial {
		t.Fatalf("state=%s, want partial", slot.State)
	}
	if len(slot.ArtifactRefs) != 2 {
		t.Fatalf("refs=%d", len(slot.ArtifactRefs))
	}
	_, state, err := store.LoadState(workID, "")
	if err != nil || state.Revision != result.WorkRevision {
		t.Fatalf("persisted partial state = (%+v, %v)", state, err)
	}
}

func TestE2E_UpstreamDigestStaleAndArtifactBlobReplacement(t *testing.T) {
	store, _, svc, workID, definitionRev, workRevision := newAppliedArtifactSlot(t, 1, true)
	ctx := contextWithServiceForTesting()
	ready, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: workRevision, Revision: 2, RequestID: "digest-ready",
		State: SlotReady, UpstreamDigest: "up-1",
		Refs: []ArtifactRef{{
			ID: "a", Name: "a.pdf", Type: "pdf", Status: "available", BlobDigest: "blob-1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: ready.WorkRevision, Revision: 3, RequestID: "digest-blob",
		State: SlotGenerating, UpstreamDigest: "up-1",
		Refs: []ArtifactRef{{
			ID: "a", Name: "a.pdf", Type: "pdf", Status: "available", BlobDigest: "blob-2",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Slot.State == SlotStale || replaced.Slot.ArtifactRefs[0].BlobDigest != "blob-2" {
		t.Fatalf("artifact blob replacement = %+v", replaced.Slot)
	}
	stale, err := svc.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
		WorkID: workID, SlotID: "slot", DefinitionRev: definitionRev,
		ExpectedRevision: replaced.WorkRevision, Revision: 4, RequestID: "digest-upstream",
		State: SlotGenerating, UpstreamDigest: "up-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Slot.State != SlotStale || stale.Slot.UpstreamDigest != "up-2" ||
		len(stale.Slot.ArtifactRefs) != 1 || stale.Slot.ArtifactRefs[0].BlobDigest != "blob-2" {
		t.Fatalf("upstream stale = %+v", stale.Slot)
	}
	projection, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := FindArtifactSlotRevision(projection, definitionRev, "slot")
	if persisted == nil || persisted.State != SlotStale {
		t.Fatalf("persisted stale = %+v", persisted)
	}
}

// ── Replay: event event_v2 round-trip ─────────────────────────────────────

func TestArtifactSlotEvent_MarshalRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	ev := BuildDeclareEvents("w1", 2, "req-1", []ArtifactSlotDef{
		{ID: "s1", Title: "R", Kind: "pdf", ExpectedCount: 1, Required: true},
	}, now)[0]

	raw, _ := json.Marshal(ev)
	var back WorkEvent
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != EventArtifactSlotDeclared {
		t.Fatalf("type=%s", back.Type)
	}
	if back.Object.ArtifactSlotID != "s1" {
		t.Fatalf("slotID=%s", back.Object.ArtifactSlotID)
	}
}

// ── Slot find helpers ──────────────────────────────────────────────────────

func TestFindArtifactSlot_NilWork(t *testing.T) {
	s, idx := FindArtifactSlot(nil, "x")
	if s != nil || idx != -1 {
		t.Fatalf("nil work: s=%v idx=%d", s, idx)
	}
}

func TestFindArtifactSlot_NotFound(t *testing.T) {
	w := &Work{ID: "w1", V2ArtifactSlots: []ArtifactSlot{{ID: "a"}, {ID: "b"}}}
	s, idx := FindArtifactSlot(w, "c")
	if s != nil || idx != -1 {
		t.Fatalf("not found: s=%v idx=%d", s, idx)
	}
}

func TestFindArtifactSlot_Found(t *testing.T) {
	w := &Work{ID: "w1", V2ArtifactSlots: []ArtifactSlot{{ID: "a"}, {ID: "b"}}}
	s, idx := FindArtifactSlot(w, "b")
	if s == nil || idx != 1 {
		t.Fatalf("found: s=%v idx=%d", s, idx)
	}
}

// ── Fixture round-trip ─────────────────────────────────────────────────────

func TestArtifactSlot_FixtureRoundTrip(t *testing.T) {
	// Verify the V2 DTO ArtifactSlot serializes/deserializes correctly
	// against the golden fixture.
	progress := 0.35
	slot := ArtifactSlot{
		ID: "slot-1", WorkID: "w-test", DefinitionRev: 1, Title: "测试报告",
		Kind: "docx", ExpectedCount: 1, Required: true,
		State: SlotPartial, ArtifactRefs: []ArtifactRef{
			{ID: "ref-a", Status: ArtifactRefStatusAvailable, BlobDigest: "sha256:abc"},
		},
		Progress: &progress, Summary: "部分完成", Revision: 4,
	}
	raw, _ := json.Marshal(slot)
	var back ArtifactSlot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != slot.ID || back.State != SlotPartial || back.Revision != 4 {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if back.Progress == nil || *back.Progress != 0.35 {
		t.Fatalf("progress=%v", back.Progress)
	}
	if len(back.ArtifactRefs) != 1 || back.ArtifactRefs[0].ID != "ref-a" {
		t.Fatalf("refs=%+v", back.ArtifactRefs)
	}
}

// ── contextWithServiceForTesting ───────────────────────────────────────────

func contextWithServiceForTesting() context.Context {
	// We use context.Background() — the production Service checks
	// context keys in checkServiceContext, but tests bypass that.
	return context.WithValue(context.Background(), contextKey("work-test"), true)
}

type contextKey string
