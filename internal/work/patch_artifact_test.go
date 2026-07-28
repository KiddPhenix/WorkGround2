package work

import (
	"encoding/json"
	"testing"
)

func artifactPatchDefinition() *WorkDefinitionRevision {
	return &WorkDefinitionRevision{
		WorkID:   "work",
		Revision: 2,
		Status:   DefActive,
		Goal:     "deliver",
		Nodes: []NodeDef{
			{ID: "make", Title: "Make", ProducesSlotIDs: []string{"report"}},
			{ID: "use", Title: "Use", DependsOn: []string{"make"}, ConsumesSlotIDs: []string{"report"}},
		},
		ArtifactSlots: []ArtifactSlotDef{
			{ID: "report", Title: "Report", Kind: "document", ExpectedCount: 1, Required: true},
		},
		InputSpecs: []InputSpec{},
	}
}

func rawPatchValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestArtifactSlotPatchAddAndRemoveAreAtomic(t *testing.T) {
	base := artifactPatchDefinition()
	addOps, err := normalizePatchOps(base, nil, PatchWorkflow, "", []PatchOp{
		{
			Op:   "add",
			Path: "artifactSlots/summary",
			NewValue: rawPatchValue(t, ArtifactSlotDef{
				ID: "summary", Title: "Summary", Kind: "text", ExpectedCount: 1, Required: false,
			}),
		},
		{
			Op:       "replace",
			Path:     "nodes/make/producesSlotIds",
			NewValue: rawPatchValue(t, []string{"report", "summary"}),
		},
	})
	if err != nil {
		t.Fatalf("normalize add: %v", err)
	}
	added := CopyOnWriteRevision(base)
	if err := applyPatchOpsToDefinition(added, addOps); err != nil {
		t.Fatalf("apply add: %v", err)
	}
	if err := validatePatchedDefinition(base, added); err != nil {
		t.Fatalf("validate add: %v", err)
	}
	if len(added.ArtifactSlots) != 2 || added.ArtifactSlots[1].ID != "summary" {
		t.Fatalf("added slots = %#v", added.ArtifactSlots)
	}
	impact := new(PatchService).computePatchImpact(base, addOps, PatchWorkflow, "make")
	if containsID(impact.staleSlots, "summary") {
		t.Fatalf("new slot must not be stale: %v", impact.staleSlots)
	}
	if !containsID(impact.invalidatedTasks, "make") || !containsID(impact.invalidatedTasks, "use") {
		t.Fatalf("add invalidation = %v", impact.invalidatedTasks)
	}

	removeOps, err := normalizePatchOps(added, nil, PatchWorkflow, "", []PatchOp{
		{Op: "remove", Path: "artifactSlots/summary"},
		{
			Op:       "replace",
			Path:     "nodes/make/producesSlotIds",
			NewValue: rawPatchValue(t, []string{"report"}),
		},
	})
	if err != nil {
		t.Fatalf("normalize remove: %v", err)
	}
	removed := CopyOnWriteRevision(added)
	if err := applyPatchOpsToDefinition(removed, removeOps); err != nil {
		t.Fatalf("apply remove: %v", err)
	}
	if err := validatePatchedDefinition(added, removed); err != nil {
		t.Fatalf("validate remove: %v", err)
	}
	if len(removed.ArtifactSlots) != 1 || removed.ArtifactSlots[0].ID != "report" {
		t.Fatalf("removed slots = %#v", removed.ArtifactSlots)
	}
	removeImpact := new(PatchService).computePatchImpact(added, removeOps, PatchWorkflow, "make")
	if containsID(removeImpact.staleSlots, "summary") {
		t.Fatalf("removed slot must not become stale in the new definition: %v", removeImpact.staleSlots)
	}
}

func TestArtifactSlotPatchRejectsMissingProducerAndDanglingReference(t *testing.T) {
	base := artifactPatchDefinition()
	ops, err := normalizePatchOps(base, nil, PatchWorkflow, "", []PatchOp{{
		Op:   "add",
		Path: "artifactSlots/orphan",
		NewValue: rawPatchValue(t, ArtifactSlotDef{
			ID: "orphan", Title: "Orphan", Kind: "text", ExpectedCount: 1,
		}),
	}})
	if err != nil {
		t.Fatalf("normalize orphan: %v", err)
	}
	orphan := CopyOnWriteRevision(base)
	if err := applyPatchOpsToDefinition(orphan, ops); err != nil {
		t.Fatalf("apply orphan: %v", err)
	}
	if err := validatePatchedDefinition(base, orphan); err == nil {
		t.Fatal("expected missing producer to fail")
	}

	removeOps, err := normalizePatchOps(base, nil, PatchWorkflow, "", []PatchOp{{
		Op: "remove", Path: "artifactSlots/report",
	}})
	if err != nil {
		t.Fatalf("normalize remove: %v", err)
	}
	dangling := CopyOnWriteRevision(base)
	if err := applyPatchOpsToDefinition(dangling, removeOps); err != nil {
		t.Fatalf("apply remove: %v", err)
	}
	if err := validatePatchedDefinition(base, dangling); err == nil {
		t.Fatal("expected dangling producer/consumer references to fail")
	}
}
