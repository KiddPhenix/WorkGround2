package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Helpers ─────────────────────────────────────────────────────────────────

func newFS(t *testing.T) (*FileWorkStore, string, *Service) {
	t.Helper()
	requireFileStoreIntegration(t)
	dir := filepath.Join(os.TempDir(), "wv2t-"+strings.ReplaceAll(t.Name(), "/", "-"))
	os.RemoveAll(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	store, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil, nil)
	return store, dir, svc
}

func v2def(workID string, rev int64) *WorkDefinitionRevision {
	return &WorkDefinitionRevision{
		WorkID: workID, Revision: rev, ParentRevision: rev - 1, Status: DefDraft,
		Goal: "Test review",
		Nodes: []NodeDef{
			{ID: "n1", Title: "Collect", InputSpecIDs: []string{"is1"}},
			{ID: "n2", Title: "Analyze", DependsOn: []string{"n1"}},
		},
		ArtifactSlots: []ArtifactSlotDef{
			{ID: "slot1", Title: "Report", Kind: "docx", ExpectedCount: 1, Required: true},
		},
		InputSpecs: []InputSpec{
			{ID: "is1", Label: "Scope", Kind: InputText, Required: true},
		},
		CreatedBy: "test", CreatedAt: time.Now().UTC(),
	}
}

func mustLoadRevision(store *FileWorkStore, workID string, rev int64) *WorkDefinitionRevision {
	r, err := store.LoadRevision(workID, rev)
	if err != nil {
		panic(fmt.Sprintf("LoadRevision(%s, %d): %v", workID, rev, err))
	}
	return r
}

// ── Unit: digest, validation, CoW, impact ───────────────────────────────────

func TestDigest_Stable(t *testing.T) {
	r := v2def("w", 1)
	d1, _ := ComputeV2RevisionDigest(r)
	d2, _ := ComputeV2RevisionDigest(r)
	if d1 != d2 {
		t.Fatal("digest not stable")
	}
}

func TestValidate_Cycle(t *testing.T) {
	r := v2def("w", 1)
	r.Nodes = []NodeDef{{ID: "a", Title: "A", DependsOn: []string{"b"}}, {ID: "b", Title: "B", DependsOn: []string{"a"}}}
	if err := ValidateDefinitionRevision(r); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle, got %v", err)
	}
}

func TestValidate_SelfDep(t *testing.T) {
	r := v2def("w", 1)
	r.Nodes = []NodeDef{{ID: "a", Title: "A", DependsOn: []string{"a"}}}
	if err := ValidateDefinitionRevision(r); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("expected self-dep, got %v", err)
	}
}

func TestCoW_PreservesParent(t *testing.T) {
	p := v2def("w", 5)
	p.Digest = "sha256:parent"
	p.Nodes[0].DependsOn = []string{"dependency"}
	p.Nodes[0].BlockIDs = []string{"block"}
	p.Nodes[0].ProducesSlotIDs = []string{"slot"}
	p.Nodes[0].ConsumesSlotIDs = []string{"slot"}
	p.InputSpecs = []InputSpec{{
		ID: "input", Kind: InputText,
		ValueSchema: json.RawMessage(`{"type":"string"}`),
	}}
	pb, _ := json.Marshal(p)
	child := CopyOnWriteRevision(p)
	child.Nodes[0].Title = "changed"
	child.Nodes[0].DependsOn[0] = "changed"
	child.Nodes[0].BlockIDs[0] = "changed"
	child.Nodes[0].ProducesSlotIDs[0] = "changed"
	child.Nodes[0].ConsumesSlotIDs[0] = "changed"
	child.InputSpecs[0].ValueSchema[0] = '['
	pa, _ := json.Marshal(p)
	if string(pb) != string(pa) {
		t.Fatal("parent bytes mutated")
	}
}

func TestImpact_StableSort(t *testing.T) {
	a := v2def("w", 1)
	b := v2def("w", 2)
	b.Nodes[0].Title = "Changed"
	r1 := ClassifyRunImpact(a, b)
	r2 := ClassifyRunImpact(a, b)
	if !sort.StringsAreSorted(r1.InvalidatedNodeIDs) || !sort.StringsAreSorted(r1.KeptNodeIDs) {
		t.Fatal("not sorted")
	}
	if r1.InvalidatedNodeIDs[0] != r2.InvalidatedNodeIDs[0] {
		t.Fatal("not deterministic")
	}
	if !r1.RequiresRerun {
		t.Fatal("requires rerun")
	}
	if got := strings.Join(r1.InvalidatedNodeIDs, ","); got != "n1,n2" {
		t.Fatalf("invalidated=%v, want changed node and all descendants", r1.InvalidatedNodeIDs)
	}
}

func TestImpact_JSONUsesArraysForEmptyGroups(t *testing.T) {
	impact := ClassifyRunImpact(v2def("w", 1), v2def("w", 2))
	data, err := json.Marshal(impact)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"invalidatedNodeIds", "newNodeIds", "removedNodeIds"} {
		if bytes.Contains(data, []byte(`"`+field+`":null`)) {
			t.Fatalf("%s must serialize as an array: %s", field, data)
		}
	}

	replayed := impactFromJSON(&RunImpactJSON{})
	replayedData, err := json.Marshal(replayed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(replayedData, []byte(`:null`)) {
		t.Fatalf("legacy receipt impact must normalize null lists: %s", replayedData)
	}
}

func TestReplay_AcceptsLegacyNullImpactGroups(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "s1", RequestID: "legacy-impact-init",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := svc.CreateCandidateRevision(
		context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "legacy-impact-candidate", state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "legacy-impact-apply",
	}); err != nil {
		t.Fatal(err)
	}

	workPath, err := store.workPath(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := ReplayWorkEventLog(workPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range replay.Events {
		event := &replay.Events[i]
		if event.Type != EventRunStarted || event.RequestID != "legacy-impact-apply/run" {
			continue
		}
		var payload runEventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		payload.V2Receipt.Impact.KeptNodeIDs = nil
		payload.V2Receipt.Impact.InvalidatedNodeIDs = nil
		payload.V2Receipt.Impact.RemovedNodeIDs = nil
		event.Payload, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := validateV2DefinitionReplay(workPath, view.Work.ID, replay); err != nil {
		t.Fatalf("legacy null impact groups are semantically empty: %v", err)
	}
}

func TestImpact_ExecutionSemanticsAndDescendants(t *testing.T) {
	tests := map[string]func(*WorkDefinitionRevision){
		"description": func(r *WorkDefinitionRevision) { r.Nodes[0].Description = "new instructions" },
		"dependency": func(r *WorkDefinitionRevision) {
			r.Nodes = append(r.Nodes, NodeDef{ID: "n0", Title: "Prepare"})
			r.Nodes[0].DependsOn = []string{"n0"}
		},
		"input kind":     func(r *WorkDefinitionRevision) { r.InputSpecs[0].Kind = InputNumber },
		"input schema":   func(r *WorkDefinitionRevision) { r.InputSpecs[0].ValueSchema = json.RawMessage(`{"type":"string"}`) },
		"input default":  func(r *WorkDefinitionRevision) { r.InputSpecs[0].DefaultValue = json.RawMessage(`"all"`) },
		"input required": func(r *WorkDefinitionRevision) { r.InputSpecs[0].Required = false },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			base := v2def("w", 1)
			next := v2def("w", 2)
			mutate(next)
			impact := ClassifyRunImpact(base, next)
			if got := strings.Join(impact.InvalidatedNodeIDs, ","); got != "n1,n2" {
				t.Fatalf("invalidated=%v, want [n1 n2]", impact.InvalidatedNodeIDs)
			}
			if !impact.RequiresRerun {
				t.Fatal("semantic change must require rerun")
			}
		})
	}
}

// ── BeginWorkPlanning — body-in-transaction, idempotent, intent conflict ────

func TestConstructors_ReuseDefinitionRevisionStore(t *testing.T) {
	for name, makeService := range map[string]func(*FileWorkStore) *Service{
		"NewService":          func(store *FileWorkStore) *Service { return NewService(store, nil, nil) },
		"NewServiceWithTools": func(store *FileWorkStore) *Service { return NewServiceWithTools(store, nil, nil, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewFileWorkStore(dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			svc := makeService(store)
			view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
				SessionID: "s1", RequestID: "constructor-" + name,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, state, err := store.LoadState(view.Work.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := svc.CreateCandidateRevision(
				context.Background(), view.Work.ID, v2def(view.Work.ID, 2),
				"candidate-"+name, state.Revision,
			)
			if err != nil {
				t.Fatal(err)
			}
			if candidate.Revision != 2 {
				t.Fatalf("candidate revision=%d, want 2", candidate.Revision)
			}
		})
	}
}

func TestBegin_BodyInTransaction(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	// Body must exist immediately (staged atomically with work dir).
	r := mustLoadRevision(store, view.Work.ID, 1)
	if r.Digest == "" || r.Revision != 1 {
		t.Fatalf("body missing or wrong rev: %+v", r)
	}
	// V2CurrentRevision must be 0 (not active).
	proj, _ := store.LoadProjection(view.Work.ID)
	if proj.V2CurrentRevision != 0 {
		t.Fatalf("V2CurrentRevision=%d, want 0 (draft only)", proj.V2CurrentRevision)
	}
	if proj.V2LatestRevision != 1 {
		t.Fatalf("V2LatestRevision=%d, want 1", proj.V2LatestRevision)
	}
}

func TestBegin_Idempotent(t *testing.T) {
	_, _, svc := newFS(t)
	input := BeginWorkPlanningInput{SessionID: "s1", RequestID: "b2"}
	v1, _ := svc.BeginWorkPlanning(context.Background(), input)
	v2, _ := svc.BeginWorkPlanning(context.Background(), input)
	if v1.Work.ID != v2.Work.ID || v1.Revision != v2.Revision {
		t.Fatal("idempotent mismatch")
	}
}

func TestBegin_DifferentSessionConflict(t *testing.T) {
	_, _, svc := newFS(t)
	svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "b3"})
	_, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s2", RequestID: "b3"})
	if err == nil {
		t.Fatal("expected conflict for different SessionID")
	}
	var c *ErrWorkEventConflict
	if !errors.As(err, &c) || c.Kind != WorkEventRequestConflict {
		t.Fatalf("expected typed conflict, got %T: %v", err, err)
	}
}

// ── CreateCandidateRevision — receipt replay, intent match/mismatch ─────────

func TestCandidate_SameRequestSameIntent(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "c-init"})
	c1, _ := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "c-req", view.Revision)
	// Replay same request — must return exact same revision.
	// (Use a fresh LoadState to get current revision for expectedRevision)
	w, st, _ := store.LoadState(view.Work.ID, "")
	c2, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 3), "c-req", st.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Digest != c2.Digest || c1.Revision != c2.Revision {
		t.Fatalf("replay mismatch: rev %d/%d digest %s/%s", c1.Revision, c2.Revision, c1.Digest, c2.Digest)
	}
	// Only one candidate created.
	if latest, _ := store.LoadLatestRevision(view.Work.ID); latest.Revision != 2 {
		t.Fatalf("expected latest=2, got %d", latest.Revision)
	}
	_ = w
}

func TestCandidate_ReplayReturnsOriginalAfterLaterRevision(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "c-history-init"})
	if err != nil {
		t.Fatal(err)
	}
	firstIntent := v2def(view.Work.ID, 2)
	first, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, firstIntent, "c-history-1", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	secondIntent := v2def(view.Work.ID, 3)
	secondIntent.Goal = "Later candidate"
	if _, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, secondIntent, "c-history-2", state.Revision); err != nil {
		t.Fatal(err)
	}
	replayed, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, firstIntent, "c-history-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != first.Revision || replayed.Digest != first.Digest {
		t.Fatalf("replay returned revision %d/%s, want %d/%s", replayed.Revision, replayed.Digest, first.Revision, first.Digest)
	}
}

func TestCandidate_SameRequestDifferentIntent(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "c-init2"})
	// First candidate.
	c1 := v2def(view.Work.ID, 2)
	c1.Goal = "Original goal"
	svc.CreateCandidateRevision(context.Background(), view.Work.ID, c1, "c-diff", view.Revision)
	// Different content, same requestID — must conflict via receipt.
	w, st, _ := store.LoadState(view.Work.ID, "")
	different := v2def(view.Work.ID, 2)
	different.Goal = "Different goal"
	_, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, different, "c-diff", st.Revision)
	var c *ErrWorkEventConflict
	if !errors.As(err, &c) || c.Kind != WorkEventRequestConflict {
		t.Fatalf("expected intent conflict, got %T: %v", err, err)
	}
	_ = w
}

func TestCandidate_ConcurrentStaleBase(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "c-race-init"})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			candidate := v2def(view.Work.ID, 2)
			candidate.Goal = fmt.Sprintf("candidate-%d", i)
			_, commitErr := svc.CreateCandidateRevision(
				context.Background(), view.Work.ID, candidate,
				fmt.Sprintf("c-race-%d", i), view.Revision,
			)
			results <- commitErr
		}()
	}
	var successes, revisionConflicts int
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		var conflict *ErrWorkEventConflict
		if errors.As(err, &conflict) && conflict.Kind == WorkEventRevisionConflict {
			revisionConflicts++
			continue
		}
		t.Fatalf("unexpected concurrent result: %v", err)
	}
	if successes != 1 || revisionConflicts != 1 {
		t.Fatalf("successes=%d revisionConflicts=%d, want 1/1", successes, revisionConflicts)
	}
	latest, err := store.LoadLatestRevision(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision != 2 {
		t.Fatalf("latest revision=%d, want 2", latest.Revision)
	}
}

func TestCandidate_MissingBodyIsRecoverable(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "c-missing-init"})
	if err != nil {
		t.Fatal(err)
	}
	intent := v2def(view.Work.ID, 2)
	if _, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, intent, "c-missing", view.Revision); err != nil {
		t.Fatal(err)
	}
	workPath, err := store.workPath(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	bodyPath := filepath.Join(workPath, v2DefSubDir, "2.json")
	if err := os.Remove(bodyPath); err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateCandidateRevision(context.Background(), view.Work.ID, intent, "c-missing", 0)
	if !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("expected explicit repair error, got %T: %v", err, err)
	}
	if _, err := store.LoadProjection(view.Work.ID); !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("expected replay repair error for missing body, got %v", err)
	}
}

func TestReplay_RejectsMissingParentEventEvenWithOrphanBody(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "s1", RequestID: "missing-parent-init",
	})
	if err != nil {
		t.Fatal(err)
	}
	for revision := int64(2); revision <= 3; revision++ {
		body := v2def(view.Work.ID, revision)
		body.Digest, _ = ComputeV2RevisionDigest(body)
		if err := store.StoreRevision(view.Work.ID, body); err != nil {
			t.Fatal(err)
		}
	}
	workPath, _ := store.workPath(view.Work.ID)
	replay, err := ReplayWorkEventLog(workPath)
	if err != nil {
		t.Fatal(err)
	}
	body3 := mustLoadRevision(store, view.Work.ID, 3)
	payload, _ := json.Marshal(DefRevisionCreatedPayload{
		WorkID: view.Work.ID, Revision: 3, ParentRevision: 2, Digest: body3.Digest,
	})
	replay.Events = append(replay.Events, newServiceEventV2(
		view.Work.ID, "missing-parent/candidate", EventDefRevisionCreated, payload, time.Now().UTC(),
	))
	err = validateV2DefinitionReplay(workPath, view.Work.ID, replay)
	if !errors.Is(err, ErrWorkNeedsRepair) || !strings.Contains(err.Error(), "no committed parent event") {
		t.Fatalf("error=%v, want missing committed parent repair error", err)
	}
}

func TestReplay_RejectsTamperedRevisionBody(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "s1", RequestID: "tamper-init",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ := store.LoadState(view.Work.ID, "")
	if _, err := svc.CreateCandidateRevision(
		context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "tamper-candidate", state.Revision,
	); err != nil {
		t.Fatal(err)
	}
	workPath, _ := store.workPath(view.Work.ID)
	bodyPath := filepath.Join(workPath, v2DefSubDir, "2.json")
	body := mustLoadRevision(store, view.Work.ID, 2)
	body.Goal = "tampered after commit"
	data, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(bodyPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadProjection(view.Work.ID); !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("LoadProjection error=%v, want ErrWorkNeedsRepair", err)
	}
}

func TestCandidate_StaleBaseRevisionConflict(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "c-conf"})
	// Create first candidate (advances work revision).
	svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "c-ok", view.Revision)
	// Stale expectedRevision.
	_, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 3), "c-stale", view.Revision)
	var c *ErrWorkEventConflict
	if !errors.As(err, &c) || c.Kind != WorkEventRevisionConflict {
		t.Fatalf("expected revision conflict, got %T: %v", err, err)
	}
	_ = store
}

func TestCandidate_OrphanBodyRetryConverges(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "orphan-init"})
	if err != nil {
		t.Fatal(err)
	}
	parent := mustLoadRevision(store, view.Work.ID, 1)
	intent := v2def(view.Work.ID, 99)
	orphan := CopyOnWriteRevision(parent)
	orphan.Goal = intent.Goal
	orphan.Nodes = append([]NodeDef(nil), intent.Nodes...)
	orphan.ArtifactSlots = append([]ArtifactSlotDef(nil), intent.ArtifactSlots...)
	orphan.InputSpecs = append([]InputSpec(nil), intent.InputSpecs...)
	orphan.ParentRevision = view.Work.V2CurrentRevision
	orphan.CreatedBy = intent.CreatedBy
	orphan.CreatedAt = time.Unix(123, 0).UTC()
	orphan.Digest, _ = ComputeV2RevisionDigest(orphan)
	if err := store.StoreRevision(view.Work.ID, orphan); err != nil {
		t.Fatal(err)
	}

	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.CreateCandidateRevision(
		context.Background(), view.Work.ID, intent, "orphan-retry", state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != orphan.Revision || got.Digest != orphan.Digest {
		t.Fatalf("retry result=(%d,%s), want orphan=(%d,%s)", got.Revision, got.Digest, orphan.Revision, orphan.Digest)
	}
}

func TestCandidate_IgnoresUncommittedHigherBody(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "higher-init"})
	if err != nil {
		t.Fatal(err)
	}
	orphan := v2def(view.Work.ID, 9)
	orphan.Digest, _ = ComputeV2RevisionDigest(orphan)
	if err := store.StoreRevision(view.Work.ID, orphan); err != nil {
		t.Fatal(err)
	}
	_, state, _ := store.LoadState(view.Work.ID, "")
	got, err := svc.CreateCandidateRevision(
		context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "higher-candidate", state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.ParentRevision != 0 {
		t.Fatalf("candidate lineage=(%d <- %d), want (2 <- 0)", got.Revision, got.ParentRevision)
	}
}

// ── ApplyDefinition — batch commit, receipt, impact recovery ────────────────

func TestApply_SameRequestSameIntent(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "a-init"})
	w, st, _ := store.LoadState(view.Work.ID, "")
	cand, _ := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "a-cand", st.Revision)

	_, st2, _ := store.LoadState(view.Work.ID, "")
	in := ApplyDefinitionInput{WorkID: view.Work.ID, Revision: cand.Revision, ExpectedRevision: st2.Revision, RequestID: "a-req"}
	r1, _ := svc.ApplyDefinition(context.Background(), in)
	// Replay — must return exact same result. expectedRevision is stale but receipt beats it.
	r2, err := svc.ApplyDefinition(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Intent.RunID != r2.Intent.RunID {
		t.Fatal("replay returned different runID")
	}
	if len(r2.View.Work.Runs) != 1 {
		t.Fatal("duplicate run")
	}
	_ = w
}

func TestApply_DuplicateProjectionFailureReturnsCommittedRecovery_FileStore(t *testing.T) {
	store, dir, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "duplicate-recovery", RequestID: "duplicate-recovery-plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := svc.CreateCandidateRevision(
		context.Background(),
		view.Work.ID,
		v2def(view.Work.ID, 2),
		"duplicate-recovery-candidate",
		state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	input := ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "duplicate-recovery-apply",
	}
	first, err := svc.ApplyDefinition(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}

	workPath, err := store.workPath(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	logPath := WorkEventLogPath(workPath)
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restarted.SetDefinitionRevisionStore(&failLoadDefinitionStore{
		DefinitionRevisionStore: reopened,
		fail:                    true,
	})
	restarted.SetV2TransportEnabled(true)

	replay, err := restarted.ApplyDefinition(context.Background(), input)
	var recovery *ErrWorkCommittedRecovery
	if !errors.As(err, &recovery) {
		t.Fatalf("error=%T %v, want committed recovery", err, err)
	}
	if replay == nil || !replay.Duplicate || !replay.Committed || !replay.Recoverable ||
		replay.View != nil || replay.Revision != candidate.Revision ||
		replay.Intent == nil || replay.Intent.RunID != first.Intent.RunID ||
		replay.TransportError == nil || replay.TransportError.Code != "committed_recovery" {
		t.Fatalf("replay=%+v, want observable committed duplicate recovery", replay)
	}
	if recovery.Operation != "apply-view" || recovery.Revision != candidate.Revision ||
		!recovery.Committed || !recovery.Recoverable {
		t.Fatalf("recovery=%+v, want apply-view committed recovery", recovery)
	}
	afterFailure, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFailure) != string(before) {
		t.Fatal("duplicate projection failure rewrote the authoritative event log")
	}

	restarted.SetDefinitionRevisionStore(reopened)
	recovered, err := restarted.ApplyDefinition(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if recovered == nil || !recovered.Duplicate || !recovered.Committed || recovered.Recoverable ||
		recovered.View == nil || recovered.Revision != first.Revision ||
		recovered.Intent == nil || recovered.Intent.RunID != first.Intent.RunID {
		t.Fatalf("recovered=%+v, want successful idempotent replay", recovered)
	}
	projection, err := reopened.LoadProjection(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Runs) != 1 {
		t.Fatalf("duplicate replay created %d runs, want 1", len(projection.Runs))
	}
	afterRecovery, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRecovery) != string(before) {
		t.Fatal("successful duplicate replay rewrote the authoritative event log")
	}
}

func TestApply_SameRequestDifferentIntent(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "a-init2"})
	w, st, _ := store.LoadState(view.Work.ID, "")
	svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "a-c1", st.Revision)
	c2, _ := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 3), "a-c2", st.Revision+1)

	_, st2, _ := store.LoadState(view.Work.ID, "")
	if _, err := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{WorkID: view.Work.ID, Revision: c2.Revision, ExpectedRevision: st2.Revision, RequestID: "a-req2"}); err != nil {
		t.Fatal(err)
	}
	// Same requestID with a genuinely different target revision must conflict.
	_, err := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{WorkID: view.Work.ID, Revision: 2, ExpectedRevision: st2.Revision, RequestID: "a-req2"})
	var conflict *ErrWorkEventConflict
	if !errors.As(err, &conflict) || conflict.Kind != WorkEventRequestConflict {
		t.Fatalf("expected request conflict, got %T: %v", err, err)
	}
	_ = w
}

func TestApply_StaleExpectedRevision(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "a-stale"})
	w, st, _ := store.LoadState(view.Work.ID, "")
	beforeRevision := st.Revision // store revision before candidate

	candidate, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "a-stale-c", st.Revision)
	if err != nil || candidate == nil {
		t.Fatalf("candidate: %v", err)
	}
	_, stAfterCandidate, _ := store.LoadState(view.Work.ID, "")
	afterCandidateRevision := stAfterCandidate.Revision

	if afterCandidateRevision != beforeRevision+1 {
		t.Fatalf("candidate did not bump Work event revision: before=%d after=%d", beforeRevision, afterCandidateRevision)
	}
	t.Logf("candidate: Work event revision %d → %d", beforeRevision, afterCandidateRevision)

	// Use original (stale) expectedRevision — must fail on new request.
	_, err = svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: 2, ExpectedRevision: view.Revision, RequestID: "a-stale-a",
	})
	var c *ErrWorkEventConflict
	if !errors.As(err, &c) || c.Kind != WorkEventRevisionConflict {
		t.Fatalf("expected revision conflict with stale expectedRevision=%d, got %T: %v", view.Revision, err, err)
	}

	// Stale apply must have zero side effects: no revision advance, no receipt, no run.
	_, stAfterConflict, _ := store.LoadState(view.Work.ID, "")
	if stAfterConflict.Revision != afterCandidateRevision {
		t.Fatalf("stale apply advanced revision: before=%d after-conflict=%d", afterCandidateRevision, stAfterConflict.Revision)
	}
	if _, receiptErr := store.LoadV2Receipt(view.Work.ID, "a-stale-a"); receiptErr == nil {
		t.Fatal("stale apply left a receipt — must be zero-side-effect")
	}
	projMid, _ := store.LoadProjection(view.Work.ID)
	if len(projMid.Runs) > 0 {
		t.Fatalf("stale apply created %d runs — must be zero-side-effect", len(projMid.Runs))
	}
	t.Logf("stale apply correctly rejected: revision stays at %d, zero side effects", afterCandidateRevision)

	// Apply with authoritative expectedRevision (post-candidate) must succeed.
	result, applyErr := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: 2, ExpectedRevision: afterCandidateRevision, RequestID: "a-stale-ok",
	})
	if applyErr != nil || result == nil || !result.Committed {
		t.Fatalf("apply with authoritative expectedRevision=%d: err=%v result=%+v", afterCandidateRevision, applyErr, result)
	}
	if result.Duplicate {
		t.Fatalf("apply should not be duplicate on first request")
	}
	_, stAfterApply, _ := store.LoadState(view.Work.ID, "")
	if stAfterApply.Revision <= afterCandidateRevision {
		t.Fatalf("apply did not advance revision: candidate=%d after-apply=%d", afterCandidateRevision, stAfterApply.Revision)
	}
	t.Logf("apply: revision %d → %d", afterCandidateRevision, stAfterApply.Revision)
	// Successful apply must have a receipt.
	if receipt, receiptErr := store.LoadV2Receipt(view.Work.ID, "a-stale-ok"); receiptErr != nil || receipt == nil {
		t.Fatalf("authoritative apply missing receipt: err=%v", receiptErr)
	}

	// Replay with same requestID must be duplicate, not re-apply.
	replay, replayErr := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: 2, ExpectedRevision: afterCandidateRevision, RequestID: "a-stale-ok",
	})
	if replayErr != nil || replay == nil || !replay.Duplicate || !replay.Committed {
		t.Fatalf("replay apply: err=%v duplicate=%v committed=%v", replayErr, replay != nil && replay.Duplicate, replay != nil && replay.Committed)
	}

	// Zero side effects: replay does not re-advance revision.
	_, stFinal, _ := store.LoadState(view.Work.ID, "")
	if stFinal.Revision != stAfterApply.Revision {
		t.Fatalf("replay advanced revision: after-apply=%d after-replay=%d", stAfterApply.Revision, stFinal.Revision)
	}

	_ = w
}

func TestApply_AtomicNoHalfComplete(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "a-atom"})
	w, st, _ := store.LoadState(view.Work.ID, "")
	svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "a-atom-c", st.Revision)

	// Apply with missing body — fails before any commit.
	_, st2, _ := store.LoadState(view.Work.ID, "")
	_, err := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: 999, ExpectedRevision: st2.Revision, RequestID: "a-atom-a",
	})
	if err == nil {
		t.Fatal("expected error for missing revision")
	}
	// Verify no run, no state change.
	proj, _ := store.LoadProjection(view.Work.ID)
	if len(proj.Runs) > 0 {
		t.Fatal("run created despite failed apply")
	}
	if proj.V2CurrentRevision != 0 {
		t.Fatal("active pointer set despite failed apply")
	}
	_ = w
}

func TestCommitEvents_SecondEventFailureIsAtomic(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "batch-init"})
	if err != nil {
		t.Fatal(err)
	}
	workPath, err := store.workPath(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(WorkEventLogPath(workPath))
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := WorkflowRun{
		ID: workflowRunID(view.Work.ID, "batch-fail"), WorkID: view.Work.ID,
		RequestID: "batch-fail", State: RunPending, StartedAt: now,
	}
	runPayload, err := json.Marshal(runEventPayload{Run: run, WorkState: WorkRunning})
	if err != nil {
		t.Fatal(err)
	}
	first := newServiceEvent(view.Work.ID, "batch-fail/run", EventRunStarted, runPayload, now)
	first.BaseRevision = state.Revision
	first.Revision = state.Revision + 1
	second := newServiceEventV2(view.Work.ID, "batch-fail/apply", EventDefRevisionApplied, json.RawMessage(`{}`), now)
	second.BaseRevision = first.Revision
	second.Revision = first.Revision + 1

	if _, err := store.CommitEvents(view.Work.ID, []WorkEvent{first, second}); err == nil {
		t.Fatal("expected invalid second event to reject the batch")
	}
	after, err := os.ReadFile(WorkEventLogPath(workPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("authoritative event log changed after second event failed")
	}
	projection, err := store.LoadProjection(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Runs) != 0 || projection.V2CurrentRevision != 0 {
		t.Fatalf("half-complete batch leaked into projection: runs=%d active=%d", len(projection.Runs), projection.V2CurrentRevision)
	}
}

func TestCommitEvents_CommitPointFailureLeavesLogUntouched(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "commit-point-init"})
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ := store.LoadState(view.Work.ID, "")
	candidate, err := svc.CreateCandidateRevision(
		context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "commit-point-candidate", state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	workPath, _ := store.workPath(view.Work.ID)
	logPath := WorkEventLogPath(workPath)
	before, _ := os.ReadFile(logPath)

	injected := errors.New("injected event log replacement failure")
	originalWrite := writeDerivedFile
	t.Cleanup(func() { writeDerivedFile = originalWrite })
	writeDerivedFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Clean(path) == filepath.Clean(logPath) {
			return injected
		}
		return originalWrite(path, data, mode)
	}
	_, state, _ = store.LoadState(view.Work.ID, "")
	_, err = svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "commit-point-apply",
	})
	writeDerivedFile = originalWrite
	if err == nil {
		t.Fatal("expected commit-point failure")
	}
	var committed *ErrWorkCommittedRecovery
	if errors.As(err, &committed) {
		t.Fatalf("pre-commit failure reported committed: %v", err)
	}
	after, _ := os.ReadFile(logPath)
	if string(after) != string(before) {
		t.Fatal("event log changed when atomic replacement failed")
	}
}

func TestCommitEvents_DerivedFailureReportsCommittedRecovery(t *testing.T) {
	for _, target := range []string{"index", "projection"} {
		t.Run(target, func(t *testing.T) {
			store, _, svc := newFS(t)
			view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
				SessionID: "s1", RequestID: "derived-" + target + "-init",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, state, _ := store.LoadState(view.Work.ID, "")
			candidate, err := svc.CreateCandidateRevision(
				context.Background(), view.Work.ID, v2def(view.Work.ID, 2),
				"derived-"+target+"-candidate", state.Revision,
			)
			if err != nil {
				t.Fatal(err)
			}
			workPath, _ := store.workPath(view.Work.ID)
			logPath := WorkEventLogPath(workPath)
			before, _ := os.ReadFile(logPath)
			injected := errors.New("injected " + target + " failure")
			_, state, err = store.LoadState(view.Work.ID, "")
			if err != nil {
				t.Fatal(err)
			}

			originalIndex := writeBatchIndex
			originalProjection := writeBatchProjection
			t.Cleanup(func() {
				writeBatchIndex = originalIndex
				writeBatchProjection = originalProjection
			})
			if target == "index" {
				writeBatchIndex = func(string, *WorkEventIndex) error { return injected }
			} else {
				writeBatchProjection = func(*FileWorkStore, string, string, *Work, int64) error {
					return injected
				}
			}

			_, err = svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
				WorkID: view.Work.ID, Revision: candidate.Revision,
				ExpectedRevision: state.Revision, RequestID: "derived-" + target + "-apply",
			})
			writeBatchIndex = originalIndex
			writeBatchProjection = originalProjection
			var committed *ErrWorkCommittedRecovery
			if !errors.As(err, &committed) || !committed.Committed || !committed.Recoverable {
				t.Fatalf("error=%v, want committed recovery", err)
			}
			after, _ := os.ReadFile(logPath)
			if string(after) == string(before) {
				t.Fatal("authoritative event log did not reach commit point")
			}
			projection, loadErr := store.LoadProjection(view.Work.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if projection.V2CurrentRevision != candidate.Revision || len(projection.Runs) != 1 {
				t.Fatalf("recovered projection active=%d runs=%d", projection.V2CurrentRevision, len(projection.Runs))
			}
		})
	}
}

func TestCommitEvent_RejectsStandaloneRevisionApplied(t *testing.T) {
	store, _, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "single-apply-init"})
	if err != nil {
		t.Fatal(err)
	}
	event := newServiceEventV2(
		view.Work.ID,
		"single-apply/apply",
		EventDefRevisionApplied,
		json.RawMessage(`{"workId":"`+view.Work.ID+`","revision":1,"previousRevision":0,"expectedRevision":0}`),
		time.Now().UTC(),
	)
	if _, err := store.CommitEvent(view.Work.ID, event); err == nil || !strings.Contains(err.Error(), "atomic definition apply") {
		t.Fatalf("expected standalone apply rejection, got %v", err)
	}
}

func TestApply_OnlyRevisionAppliedSetsActive(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "a-active"})
	w, st, _ := store.LoadState(view.Work.ID, "")
	// Create 3 candidates.
	svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "a-ac1", st.Revision)
	_, st2, _ := store.LoadState(view.Work.ID, "")
	svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 3), "a-ac2", st2.Revision)
	_, st3, _ := store.LoadState(view.Work.ID, "")
	cand, _ := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 4), "a-ac3", st3.Revision)

	// Before apply, V2CurrentRevision is 0.
	proj, _ := store.LoadProjection(view.Work.ID)
	if proj.V2CurrentRevision != 0 {
		t.Fatalf("V2CurrentRevision=%d before apply, want 0", proj.V2CurrentRevision)
	}
	if proj.V2LatestRevision != 4 {
		t.Fatalf("V2LatestRevision=%d, want 4", proj.V2LatestRevision)
	}

	// Apply revision 4.
	_, st4, _ := store.LoadState(view.Work.ID, "")
	svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: cand.Revision, ExpectedRevision: st4.Revision, RequestID: "a-apply4",
	})

	// After apply, V2CurrentRevision is 4.
	proj, _ = store.LoadProjection(view.Work.ID)
	if proj.V2CurrentRevision != 4 {
		t.Fatalf("V2CurrentRevision=%d after apply, want 4", proj.V2CurrentRevision)
	}
	if proj.V2RevisionStates[4] != DefActive {
		t.Fatalf("rev4 status=%s, want active", proj.V2RevisionStates[4])
	}
	_ = w
}

func TestApply_ConcurrentSameParentHasOneActiveAndRejectsRollback_FileStore(t *testing.T) {
	store1, dir, svc1 := newFS(t)
	view, err := svc1.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "s1", RequestID: "concurrent-init",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store1.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc1.CreateCandidateRevision(
		context.Background(), view.Work.ID, v2def(view.Work.ID, 2),
		"concurrent-candidate-1", state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, _ = store1.LoadState(view.Work.ID, "")
	secondIntent := v2def(view.Work.ID, 3)
	secondIntent.Goal = "second candidate"
	second, err := svc1.CreateCandidateRevision(
		context.Background(), view.Work.ID, secondIntent,
		"concurrent-candidate-2", state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.ParentRevision != 0 || second.ParentRevision != 0 {
		t.Fatalf("candidates do not share active parent: first=%d second=%d", first.ParentRevision, second.ParentRevision)
	}
	_, applyState, _ := store1.LoadState(view.Work.ID, "")

	store2, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := NewService(store2, nil, nil)
	start := make(chan struct{})
	type outcome struct {
		revision int64
		err      error
	}
	results := make(chan outcome, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	apply := func(svc *Service, revision int64, requestID string) {
		ready.Done()
		<-start
		_, applyErr := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
			WorkID: view.Work.ID, Revision: revision,
			ExpectedRevision: applyState.Revision, RequestID: requestID,
		})
		results <- outcome{revision: revision, err: applyErr}
	}
	go apply(svc1, first.Revision, "concurrent-apply-1")
	go apply(svc2, second.Revision, "concurrent-apply-2")
	ready.Wait()
	close(start)

	successes := 0
	var winner, loser int64
	for range 2 {
		result := <-results
		if result.err == nil {
			successes++
			winner = result.revision
		} else {
			loser = result.revision
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent applies succeeded=%d, want 1", successes)
	}

	projection, beforeState, err := store1.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	activeCount := 0
	for revision, status := range projection.V2RevisionStates {
		if status == DefActive {
			activeCount++
			if revision != winner {
				t.Fatalf("active revision=%d, winner=%d", revision, winner)
			}
		}
	}
	if projection.V2CurrentRevision != winner || activeCount != 1 || len(projection.Runs) != 1 {
		t.Fatalf("projection current=%d activeCount=%d runs=%d winner=%d",
			projection.V2CurrentRevision, activeCount, len(projection.Runs), winner)
	}

	if _, err := svc1.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: loser,
		ExpectedRevision: beforeState.Revision, RequestID: "rollback-new-request",
	}); err == nil {
		t.Fatal("old-parent candidate rollback succeeded with a new request")
	}
	afterRollback, afterState, err := store1.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Revision != beforeState.Revision ||
		afterRollback.V2CurrentRevision != winner ||
		len(afterRollback.Runs) != 1 {
		t.Fatal("rejected rollback mutated projection")
	}

	reopened, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := reopened.LoadProjection(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	activeCount = 0
	for revision, status := range restarted.V2RevisionStates {
		if status == DefActive {
			activeCount++
			if revision != winner {
				t.Fatalf("restart active revision=%d, winner=%d", revision, winner)
			}
		}
	}
	if restarted.V2CurrentRevision != winner || activeCount != 1 || len(restarted.Runs) != 1 {
		t.Fatalf("restart current=%d activeCount=%d runs=%d",
			restarted.V2CurrentRevision, activeCount, len(restarted.Runs))
	}
}

func TestApply_ImpactRecoverable(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "a-impact"})
	w, st, _ := store.LoadState(view.Work.ID, "")
	// Create first candidate with original nodes, then second with changed node.
	c1 := v2def(view.Work.ID, 2)
	c1.Goal = "Original"
	first, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, c1, "a-imp-c1", st.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, st2, _ := store.LoadState(view.Work.ID, "")
	if _, err := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: first.Revision, ExpectedRevision: st2.Revision, RequestID: "a-imp-a1",
	}); err != nil {
		t.Fatal(err)
	}
	// Create a changed candidate from the now-active revision.
	_, st2, _ = store.LoadState(view.Work.ID, "")
	changed := v2def(view.Work.ID, 3)
	changed.Nodes[0].Title = "Changed title"
	changed.Goal = "Changed"
	cand, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, changed, "a-imp-c2", st2.Revision)
	if err != nil {
		t.Fatal(err)
	}

	_, st3, _ := store.LoadState(view.Work.ID, "")
	result, _ := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: cand.Revision, ExpectedRevision: st3.Revision, RequestID: "a-imp-a",
	})

	// Impact must be deterministic and recoverable from receipt.
	r, _ := store.LoadV2Receipt(view.Work.ID, "a-imp-a")
	if r.Impact == nil {
		t.Fatal("impact not persisted in receipt")
	}
	if got := strings.Join(r.Impact.InvalidatedNodeIDs, ","); got != "n1,n2" {
		t.Fatalf("impact invalidated=%v, want [n1 n2]", r.Impact.InvalidatedNodeIDs)
	}
	if !r.Impact.RequiresRerun {
		t.Fatal("impact.RequiresRerun should be true")
	}
	if len(r.Impact.KeptNodeIDs) != 0 {
		t.Fatalf("impact kept=%v, want none", r.Impact.KeptNodeIDs)
	}
	workPath, err := store.workPath(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workPath, v2ReceiptSubDir, "a-imp-a.json")); !os.IsNotExist(err) {
		t.Fatalf("receipt must be event-backed, sidecar stat error=%v", err)
	}
	_ = result
	_ = w
}

func TestApply_RestartRecovers(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "wv2t-restart")
	os.RemoveAll(dir)
	t.Cleanup(func() { os.RemoveAll(dir) })

	store1, _ := NewFileWorkStore(dir, 0)
	svc1 := NewService(store1, nil, nil)
	svc1.SetDefinitionRevisionStore(store1)

	view, _ := svc1.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "r-init"})
	w, st, _ := store1.LoadState(view.Work.ID, "")
	cand, _ := svc1.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "r-cand", st.Revision)
	_, st2, _ := store1.LoadState(view.Work.ID, "")
	result1, _ := svc1.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: cand.Revision, ExpectedRevision: st2.Revision, RequestID: "r-apply",
	})

	// Restart: new store, same dir.
	store2, _ := NewFileWorkStore(dir, 0)
	svc2 := NewService(store2, nil, nil)
	svc2.SetDefinitionRevisionStore(store2)

	// Projection recovered from event log.
	proj, _ := store2.LoadProjection(view.Work.ID)
	if len(proj.Runs) != 1 {
		t.Fatalf("restart: expected 1 run, got %d", len(proj.Runs))
	}
	if proj.V2CurrentRevision != cand.Revision {
		t.Fatalf("restart: V2CurrentRevision=%d, want %d", proj.V2CurrentRevision, cand.Revision)
	}

	// Receipt recovered.
	receipt, _ := store2.LoadV2Receipt(view.Work.ID, "r-apply")
	if receipt == nil || receipt.ResultRunID != result1.Intent.RunID {
		t.Fatal("restart: receipt missing or runID mismatch")
	}
	if receipt.Impact == nil || len(receipt.Impact.NewNodeIDs) != 2 {
		t.Fatalf("restart: impact not recovered or wrong: %+v", receipt.Impact)
	}

	// Replay returns same deterministic result.
	result2, err := svc2.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: cand.Revision, ExpectedRevision: 0, RequestID: "r-apply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result2.Intent.RunID != result1.Intent.RunID {
		t.Fatal("restart: replay returned different runID")
	}
	_ = w
}

// ── Mixed replay: V1 work.created + V2 events ──────────────────────────────

func TestMixedReplay_V1Created_V2DefinitionEvents(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "mix"})
	w, st, _ := store.LoadState(view.Work.ID, "")
	cand, _ := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "mix-c", st.Revision)
	_, st2, _ := store.LoadState(view.Work.ID, "")
	svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: cand.Revision, ExpectedRevision: st2.Revision, RequestID: "mix-a",
	})

	// Replay from event log must produce consistent projection.
	proj, _ := store.LoadProjection(view.Work.ID)
	if proj.SchemaVersion != SchemaVersionV2 {
		t.Fatalf("schema version %d, want %d", proj.SchemaVersion, SchemaVersionV2)
	}
	if proj.V2CurrentRevision != 2 {
		t.Fatalf("V2CurrentRevision=%d, want 2", proj.V2CurrentRevision)
	}
	if len(proj.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(proj.Runs))
	}
	// The planning draft remains a draft because rev2 branches from the
	// active pointer (virtual root 0), not from an unapplied draft.
	if st := proj.V2RevisionStates[1]; st != DefDraft {
		t.Fatalf("rev1 state=%s, want draft", st)
	}
	if st := proj.V2RevisionStates[2]; st != DefActive {
		t.Fatalf("rev2 state=%s, want active", st)
	}
	_ = w
}

// ── FileWorkStore: per-work lock on StoreRevision ───────────────────────────

func TestFileStore_StoreRevisionUnderLock(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "fs-lock"})
	// StoreRevision acquires per-work lock — must succeed.
	r := v2def(view.Work.ID, 2)
	r.Digest, _ = ComputeV2RevisionDigest(r)
	if err := store.StoreRevision(view.Work.ID, r); err != nil {
		t.Fatal(err)
	}
	// Same content — idempotent.
	if err := store.StoreRevision(view.Work.ID, r); err != nil {
		t.Fatal(err)
	}
}

func TestFileStore_StoreRevisionDeepCopy(t *testing.T) {
	store, _, svc := newFS(t)
	view, _ := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "fs-dc"})
	r := v2def(view.Work.ID, 2)
	r.Digest, _ = ComputeV2RevisionDigest(r)
	store.StoreRevision(view.Work.ID, r)
	loaded, _ := store.LoadRevision(view.Work.ID, 2)
	loaded.Goal = "mutated"
	loaded2, _ := store.LoadRevision(view.Work.ID, 2)
	if loaded2.Goal != r.Goal {
		t.Fatal("file load did not deep copy")
	}
}

// ── buildV2Stages — Stage.StartedAt must be non-zero ────────────────────────

func TestBuildV2Stages_NonZeroStartedAt(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)
	rev := &WorkDefinitionRevision{
		WorkID: "w1", Revision: 1,
		Nodes: []NodeDef{
			{ID: "n1", Title: "Collect"},
			{ID: "n2", Title: "Analyze"},
		},
	}
	stages := buildV2Stages(rev, now)
	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2", len(stages))
	}
	for i, s := range stages {
		if s.StartedAt.IsZero() {
			t.Fatalf("stage[%d] StartedAt is zero", i)
		}
		if !s.StartedAt.Equal(now) {
			t.Fatalf("stage[%d] StartedAt = %v, want %v", i, s.StartedAt, now)
		}
	}
	// nil revision must not panic.
	if stages := buildV2Stages(nil, now); stages != nil {
		t.Fatal("buildV2Stages(nil) should return nil")
	}
}

func TestBuildV2Stages_FileStoreRoundtrip(t *testing.T) {
	store, dir, svc := newFS(t)
	view, err := svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{SessionID: "s1", RequestID: "bst-rt"})
	if err != nil {
		t.Fatal(err)
	}
	cand, err := svc.CreateCandidateRevision(context.Background(), view.Work.ID, v2def(view.Work.ID, 2), "bst-cand", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, st, _ := store.LoadState(view.Work.ID, "")
	result, err := svc.ApplyDefinition(context.Background(), ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: cand.Revision, RequestID: "apply-bst", ExpectedRevision: st.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.View == nil {
		t.Fatal("expected View after ApplyDefinition")
	}
	workID := view.Work.ID

	// Verify non-zero timestamps before restart.
	for _, run := range result.View.Work.Runs {
		for i, stage := range run.Stages {
			if stage.StartedAt.IsZero() {
				t.Fatalf("run %s stage[%d] StartedAt is zero before restart", run.ID, i)
			}
		}
	}

	// Close and reopen the real FileWorkStore via a new store instance on the same dir.
	reopened, err := NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, nil)
	restored, err := restarted.loadView(workID)
	if err != nil {
		t.Fatalf("loadView after restart: %v", err)
	}
	if restored == nil || restored.Work == nil {
		t.Fatal("restored view is nil after restart")
	}
	for _, run := range restored.Work.Runs {
		for i, stage := range run.Stages {
			if stage.StartedAt.IsZero() {
				t.Fatalf("run %s stage[%d] StartedAt is zero after FileWorkStore restart", run.ID, i)
			}
			raw, _ := json.Marshal(stage)
			var back Stage
			if err := json.Unmarshal(raw, &back); err != nil {
				t.Fatalf("stage[%d] JSON unmarshal after restart: %v", i, err)
			}
			if !back.StartedAt.Equal(stage.StartedAt) {
				t.Fatalf("stage[%d] StartedAt changed after restart JSON roundtrip: %v → %v", i, stage.StartedAt, back.StartedAt)
			}
		}
	}
}

// ── Historical zero-time recovery: 0001-01-01T00:00:00Z survives the chain ──

func TestHistoricalZeroTime_JSONRoundTrip(t *testing.T) {
	// Simulate pre-fix data: Go time.Time{} serializes to "0001-01-01T00:00:00Z".
	// Both Go and the frontend parser must handle this.
	zeroStage := Stage{
		ID: "s1", Name: "Historical", State: RunPending,
		Tasks:     []Task{{ID: "t1", Name: "Historical", State: RunPending}},
		StartedAt: time.Time{}, // zero value — the pre-fix bug artefact
	}
	raw, err := json.Marshal(zeroStage)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("historical zero-time stage JSON: %s", string(raw))

	// Must contain 0001-01-01T00:00:00Z.
	if !strings.Contains(string(raw), "0001-01-01T00:00:00Z") {
		t.Fatalf("zero time not serialized as 0001-01-01T00:00:00Z: %s", string(raw))
	}

	// Go roundtrip: unmarshal must succeed.
	var back Stage
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("zero-time stage JSON unmarshal failed: %v", err)
	}
	if !back.StartedAt.IsZero() {
		t.Fatalf("zero-time roundtrip: expected zero, got %v", back.StartedAt)
	}

	// Simulate the full WorkView JSON that the frontend parser receives.
	// This is what Wails/JSON delivers to the desktop frontend.
	viewJSON := fmt.Sprintf(`{
		"schemaVersion": 1,
		"revision": 1,
		"work": {
			"schemaVersion": 1, "id": "hist", "name": "Historical",
			"state": "draft", "archiveState": "active",
			"blueprintRef": {"id": "bp", "schemaVersion": 1, "version": 1},
			"definitionSnapshot": {
				"schemaVersion": 1, "revision": 1,
				"blueprintRef": {"id": "bp", "schemaVersion": 1, "version": 1},
				"promptTemplate": "", "workflow": {"stages": []},
				"blockSpecs": [], "digest": "sha256:abc"
			},
			"blocks": [], "placements": [], "prompt": "",
			"cornerstones": [], "runs": [
				{"id": "run-hist", "workId": "hist", "definitionDigest": "sha256:abc",
				 "state": "pending", "stages": [%s],
				 "startedAt": "2026-07-24T10:00:00Z"}
			],
			"createdWith": {"workSchemaVersion": 1, "eventSchemaVersion": 1, "rendererSetVersion": 1},
			"createdAt": "2026-07-24T10:00:00Z",
			"updatedAt": "2026-07-24T10:00:00Z"
		}
	}`, string(raw))

	// Parse the zero-time WorkView JSON through the Go side.
	var wv WorkView
	if err := json.Unmarshal([]byte(viewJSON), &wv); err != nil {
		t.Fatalf("WorkView with historical zero time unmarshal failed: %v", err)
	}
	for _, run := range wv.Work.Runs {
		for _, stage := range run.Stages {
			if !stage.StartedAt.IsZero() {
				t.Fatalf("expected zero time in historical stage, got %v", stage.StartedAt)
			}
		}
	}
	t.Log("Go: historical zero-time WorkView roundtrip OK")
	// The frontend parser test (work-parse-date.test.ts) covers the JS side.
}
