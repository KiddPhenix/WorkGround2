package work

import (
	"encoding/json"
	"errors"
	"strings"
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

func TestArtifactSlotPatchFormatChangePreservesReferencesAndInvalidatesDependents(t *testing.T) {
	base := artifactPatchDefinition()
	ops, err := normalizePatchOps(base, nil, PatchWorkflow, "", []PatchOp{
		{
			Op:       "replace",
			Path:     "artifactSlots/report/title",
			NewValue: rawPatchValue(t, "Report.xlsx"),
		},
		{
			Op:       "replace",
			Path:     "artifactSlots/report/kind",
			NewValue: rawPatchValue(t, "xlsx"),
		},
	})
	if err != nil {
		t.Fatalf("normalize edit: %v", err)
	}
	changed := CopyOnWriteRevision(base)
	if err := applyPatchOpsToDefinition(changed, ops); err != nil {
		t.Fatalf("apply edit: %v", err)
	}
	if err := validatePatchedDefinition(base, changed); err != nil {
		t.Fatalf("validate edit: %v", err)
	}
	slot := changed.ArtifactSlots[0]
	if slot.ID != "report" || slot.Title != "Report.xlsx" || slot.Kind != "xlsx" {
		t.Fatalf("changed slot = %#v", slot)
	}
	if !containsID(changed.Nodes[0].ProducesSlotIDs, "report") ||
		!containsID(changed.Nodes[1].ConsumesSlotIDs, "report") {
		t.Fatalf("references were not preserved: %#v", changed.Nodes)
	}
	impact := new(PatchService).computePatchImpact(base, ops, PatchWorkflow, "make")
	if !containsID(impact.staleSlots, "report") {
		t.Fatalf("changed slot must become stale: %v", impact.staleSlots)
	}
	if !containsID(impact.invalidatedTasks, "make") || !containsID(impact.invalidatedTasks, "use") {
		t.Fatalf("format change invalidation = %v", impact.invalidatedTasks)
	}
}

func TestArtifactSlotPatchCoordinatorReformatDoesNotInvalidateProducer(t *testing.T) {
	base := artifactPatchDefinition()
	ops, err := normalizePatchOps(base, nil, PatchWorkflow, "", []PatchOp{
		{
			Op:       "replace",
			Path:     "artifactSlots/report/title",
			NewValue: rawPatchValue(t, "Report.xlsx"),
		},
		{
			Op:       "replace",
			Path:     "artifactSlots/report/kind",
			NewValue: rawPatchValue(t, "xlsx"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	impact := new(PatchService).computePatchImpactWithActions(
		base,
		ops,
		PatchWorkflow,
		"make",
		[]PatchAction{{Action: PatchActionReformat, ArtifactSlotID: "report"}},
	)
	if len(impact.invalidatedTasks) != 0 || impact.requiresRerun {
		t.Fatalf("semantic reformat invalidated producer: %+v", impact)
	}
	if !containsID(impact.staleSlots, "report") {
		t.Fatalf("reformat must still identify target slot: %+v", impact)
	}
}

// ── Block data normalization & validation ──────────────────────────────────

func markdownBlock() *BlockInstance {
	return &BlockInstance{
		ID:            "b-md",
		Kind:          "markdown",
		SchemaVersion: 1,
		Revision:      1,
		Data:          json.RawMessage(`{"content":"原内容"}`),
	}
}

func str(s string) json.RawMessage { return json.RawMessage(s) }

func TestNormalizeBlockDataValueUnwrapsStringObject(t *testing.T) {
	// Simulates the bug: planner returns newValue as JSON string wrapping an object.
	raw := str(`"{\"content\":\"中文\"}"`)
	normalized, err := normalizeBlockDataValue(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !jsonValuesEqual(normalized, json.RawMessage(`{"content":"中文"}`)) {
		t.Fatalf("normalized = %s", normalized)
	}
}

func TestNormalizeBlockDataValueRejectsStringWrappedArray(t *testing.T) {
	raw := str(`"[1,2,3]"`)
	_, err := normalizeBlockDataValue(raw)
	if err == nil || !strings.Contains(err.Error(), "non-object") {
		t.Fatalf("expected rejection of array, got err=%v", err)
	}
}

func TestNormalizeBlockDataValueRejectsStringWrappedPrimitive(t *testing.T) {
	raw := str(`"123"`)
	_, err := normalizeBlockDataValue(raw)
	if err == nil || !strings.Contains(err.Error(), "non-object") {
		t.Fatalf("expected rejection of number, got err=%v", err)
	}
}

func TestNormalizeBlockDataValueRejectsDoubleWrappedString(t *testing.T) {
	// Double encoding: a JSON string wrapping another JSON string.
	raw := str(`"\"{\\\"content\\\":\\\"x\\\"}\""`)
	// Unwraps once → `"{\"content\":\"x\"}"` which is a valid JSON string but
	// whose content is itself a JSON string (not an object) → reject.
	_, err := normalizeBlockDataValue(raw)
	if err == nil {
		t.Fatal("expected rejection of double-wrapped string")
	}
	if !strings.Contains(err.Error(), "non-object") {
		t.Fatalf("error should mention non-object, got: %v", err)
	}
}

func TestNormalizeBlockDataValueRejectsInvalidJSONContent(t *testing.T) {
	raw := str(`"not json at all"`)
	_, err := normalizeBlockDataValue(raw)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("expected rejection of non-JSON content, got err=%v", err)
	}
}

func TestNormalizeBlockDataValuePassthroughObject(t *testing.T) {
	// Already correct: an object passes through unchanged.
	raw := str(`{"content":"noop"}`)
	normalized, err := normalizeBlockDataValue(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !jsonValuesEqual(normalized, json.RawMessage(`{"content":"noop"}`)) {
		t.Fatalf("normalized = %s", normalized)
	}
}

func TestNormalizeBlockDataValuePassthroughNumber(t *testing.T) {
	// Non-string JSON values pass through (rejected later by validateBlockData).
	raw := str(`42`)
	normalized, err := normalizeBlockDataValue(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !jsonValuesEqual(normalized, json.RawMessage(`42`)) {
		t.Fatalf("normalized = %s", normalized)
	}
}

func TestValidateBlockDataMarkdown(t *testing.T) {
	tests := []struct {
		name string
		data json.RawMessage
		ok   bool
	}{
		{"valid", json.RawMessage(`{"content":"hello"}`), true},
		{"empty content", json.RawMessage(`{"content":""}`), true},
		{"missing content", json.RawMessage(`{}`), false},
		{"extra field", json.RawMessage(`{"content":"x","extra":1}`), false},
		{"content not string", json.RawMessage(`{"content":123}`), false},
		{"not an object", json.RawMessage(`"just a string"`), false},
		{"null", json.RawMessage(`null`), false},
		{"array", json.RawMessage(`[1,2,3]`), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBlockData("markdown", 1, tc.data)
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestApplyPatchOpsToBlockRejectsBadMarkdownData(t *testing.T) {
	block := markdownBlock()
	ops := []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-md/data",
		NewValue: json.RawMessage(`"bad string"`),
		OldValue: block.Data,
	}}
	err := applyPatchOpsToBlock(block, ops)
	if err == nil || !strings.Contains(err.Error(), "block data") {
		t.Fatalf("expected block data validation error, got: %v", err)
	}
}

func TestApplyPatchOpsToBlockAcceptsValidMarkdownData(t *testing.T) {
	block := markdownBlock()
	ops := []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-md/data",
		NewValue: json.RawMessage(`{"content":"新内容"}`),
		OldValue: block.Data,
	}}
	err := applyPatchOpsToBlock(block, ops)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !jsonValuesEqual(block.Data, json.RawMessage(`{"content":"新内容"}`)) {
		t.Fatalf("block data = %s", block.Data)
	}
}

func TestNormalizePatchOpsNormalizesBlockDataStringWrap(t *testing.T) {
	block := markdownBlock()
	// Planner returns newValue as a JSON string wrapping an object — the exact bug.
	ops, err := normalizePatchOps(nil, block, PatchBlock, "b-md", []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-md/data",
		NewValue: str(`"{\"content\":\"更新后的中文\"}"`),
	}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops len = %d", len(ops))
	}
	if !jsonValuesEqual(ops[0].NewValue, json.RawMessage(`{"content":"更新后的中文"}`)) {
		t.Fatalf("newValue was not normalized: %s", ops[0].NewValue)
	}
	// OldValue should be read from the authoritative state (block.Data), not the raw input.
	if !jsonValuesEqual(ops[0].OldValue, json.RawMessage(`{"content":"原内容"}`)) {
		t.Fatalf("oldValue mismatch: %s", ops[0].OldValue)
	}
}

func TestNormalizePatchOpsRejectsBlockDataStringWrappedArray(t *testing.T) {
	block := markdownBlock()
	_, err := normalizePatchOps(nil, block, PatchBlock, "b-md", []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-md/data",
		NewValue: str(`"[1,2,3]"`),
	}})
	if err == nil {
		t.Fatal("expected rejection of string-wrapped array")
	}
	if !strings.Contains(err.Error(), "non-object") {
		t.Fatalf("error should mention non-object: %v", err)
	}
}

func TestNormalizePatchOpsRejectsBlockDataStringWrappedPrimitive(t *testing.T) {
	block := markdownBlock()
	_, err := normalizePatchOps(nil, block, PatchBlock, "b-md", []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-md/data",
		NewValue: str(`"just a string"`),
	}})
	if err == nil {
		t.Fatal("expected rejection of string wrapping a plain string")
	}
}

func TestNormalizePatchOpsBlockDataObjectPassthrough(t *testing.T) {
	block := markdownBlock()
	ops, err := normalizePatchOps(nil, block, PatchBlock, "b-md", []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-md/data",
		NewValue: str(`{"content":"直接对象"}`),
	}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !jsonValuesEqual(ops[0].NewValue, json.RawMessage(`{"content":"直接对象"}`)) {
		t.Fatalf("object was altered: %s", ops[0].NewValue)
	}
}

func TestNormalizePatchOpsValidatesAllBlockKinds(t *testing.T) {
	block := &BlockInstance{
		ID:            "b-table",
		Kind:          "table",
		SchemaVersion: 1,
		Data:          json.RawMessage(`{"columns":[],"rows":[]}`),
	}
	ops, err := normalizePatchOps(nil, block, PatchBlock, block.ID, []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-table/data",
		NewValue: str(`"{\"columns\":[],\"rows\":[]}"`),
	}})
	if err != nil {
		t.Fatalf("normalize table: %v", err)
	}
	if !jsonValuesEqual(ops[0].NewValue, json.RawMessage(`{"columns":[],"rows":[]}`)) {
		t.Fatalf("table newValue was not normalized: %s", ops[0].NewValue)
	}

	_, err = normalizePatchOps(nil, block, PatchBlock, block.ID, []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-table/data",
		NewValue: json.RawMessage(`{"content":"wrong shape"}`),
	}})
	if err == nil {
		t.Fatal("expected table schema rejection")
	}
}

func TestNormalizePatchOpsRejectsFutureBlockSchemaMutation(t *testing.T) {
	block := markdownBlock()
	block.SchemaVersion = 99
	_, err := normalizePatchOps(nil, block, PatchBlock, block.ID, []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-md/data",
		NewValue: json.RawMessage(`{"content":"future"}`),
	}})
	var future *ErrFutureBlockSchema
	if !errors.As(err, &future) {
		t.Fatalf("expected future block schema error, got: %v", err)
	}
}

func TestApplyPatchOpsToBlockRejectsMarkdownExtraFields(t *testing.T) {
	block := markdownBlock()
	ops := []PatchOp{{
		Op:       "replace",
		Path:     "blocks/b-md/data",
		NewValue: json.RawMessage(`{"content":"ok","allowDangerousHtml":true}`),
		OldValue: block.Data,
	}}
	err := applyPatchOpsToBlock(block, ops)
	if err == nil {
		t.Fatal("expected rejection of extra fields in markdown data")
	}
	if !strings.Contains(err.Error(), "unexpected fields") {
		t.Fatalf("error should mention unexpected fields: %v", err)
	}
}
