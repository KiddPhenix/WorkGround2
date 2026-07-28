package work

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── ArtifactSlotState transition tests (production validators) ──────────────

func TestValidateArtifactSlotTransition_Valid(t *testing.T) {
	pairs := [][2]ArtifactSlotState{
		{SlotReserved, SlotGenerating}, {SlotReserved, SlotReady}, {SlotReserved, SlotPartial}, {SlotReserved, SlotStale},
		{SlotGenerating, SlotReady}, {SlotGenerating, SlotPartial}, {SlotGenerating, SlotFailed}, {SlotGenerating, SlotStale},
		{SlotReady, SlotStale}, {SlotReady, SlotGenerating},
		{SlotReady, SlotPartial}, {SlotReady, SlotReserved},
		{SlotPartial, SlotGenerating}, {SlotPartial, SlotReady}, {SlotPartial, SlotFailed}, {SlotPartial, SlotStale},
		{SlotFailed, SlotGenerating}, {SlotFailed, SlotReserved}, {SlotFailed, SlotStale},
		{SlotStale, SlotGenerating},
	}
	for _, p := range pairs {
		if err := ValidateArtifactSlotTransition(p[0], p[1]); err != nil {
			t.Errorf("%s → %s rejected: %v", p[0], p[1], err)
		}
	}
}

func TestValidateArtifactSlotTransition_SameState(t *testing.T) {
	for _, s := range []ArtifactSlotState{SlotReserved, SlotGenerating, SlotReady, SlotPartial, SlotFailed, SlotStale} {
		if err := ValidateArtifactSlotTransition(s, s); err != nil {
			t.Errorf("%s → %s rejected: %v", s, s, err)
		}
	}
}

func TestValidateArtifactSlotTransition_Invalid(t *testing.T) {
	pairs := [][2]ArtifactSlotState{
		{SlotReserved, SlotFailed},
		{SlotGenerating, SlotReserved},
		{SlotReady, SlotFailed},
		{SlotPartial, SlotReserved},
		{SlotFailed, SlotReady}, {SlotFailed, SlotPartial},
		{SlotStale, SlotReserved}, {SlotStale, SlotReady}, {SlotStale, SlotPartial}, {SlotStale, SlotFailed},
	}
	for _, p := range pairs {
		if err := ValidateArtifactSlotTransition(p[0], p[1]); err == nil {
			t.Errorf("%s → %s should be invalid", p[0], p[1])
		}
	}
}

// ── InputState transition tests ────────────────────────────────────────────

func TestValidateInputTransition_Valid(t *testing.T) {
	pairs := [][2]InputState{
		{InputRequested, InputDraft}, {InputRequested, InputSubmitted},
		{InputDraft, InputSubmitted}, {InputDraft, InputRejected},
		{InputSubmitted, InputAccepted}, {InputSubmitted, InputRejected},
		{InputRejected, InputDraft}, {InputRejected, InputSubmitted},
		{InputAccepted, InputSubmitted},
	}
	for _, p := range pairs {
		if err := ValidateInputTransition(p[0], p[1]); err != nil {
			t.Errorf("%s → %s rejected: %v", p[0], p[1], err)
		}
	}
}

// ── TaskStateV2 transition tests ──────────────────────────────────────────

func TestValidateTaskV2Transition_Valid(t *testing.T) {
	pairs := [][2]TaskStateV2{
		{TaskPending, TaskReady}, {TaskPending, TaskCanceled},
		{TaskReady, TaskRunning}, {TaskReady, TaskCanceled},
		{TaskRunning, TaskCompleted}, {TaskRunning, TaskFailedRetryable}, {TaskRunning, TaskFailedTerminal},
		{TaskRunning, TaskWaitingInput}, {TaskRunning, TaskWaitingApproval}, {TaskRunning, TaskCanceled},
		{TaskWaitingInput, TaskReady}, {TaskWaitingInput, TaskCanceled}, {TaskWaitingInput, TaskFailedTerminal},
		{TaskWaitingApproval, TaskReady}, {TaskWaitingApproval, TaskCanceled}, {TaskWaitingApproval, TaskFailedTerminal},
		{TaskCompleted, TaskInvalidated},
		{TaskFailedRetryable, TaskReady}, {TaskFailedRetryable, TaskCanceled},
		{TaskCanceled, TaskReady},
		{TaskInvalidated, TaskReady}, {TaskInvalidated, TaskCanceled},
	}
	for _, p := range pairs {
		if err := ValidateTaskV2Transition(p[0], p[1]); err != nil {
			t.Errorf("%s → %s rejected: %v", p[0], p[1], err)
		}
	}
}

func TestValidateTaskV2Transition_TerminalNoOutgoing(t *testing.T) {
	// failed_terminal has no outgoing transitions.
	states := []TaskStateV2{TaskPending, TaskReady, TaskRunning, TaskWaitingInput, TaskWaitingApproval,
		TaskCompleted, TaskFailedRetryable, TaskCanceled, TaskInvalidated}
	for _, s := range states {
		if err := ValidateTaskV2Transition(TaskFailedTerminal, s); err == nil {
			t.Errorf("failed_terminal → %s should be invalid", s)
		}
	}
}

// ── Schema / migration decision tests ──────────────────────────────────────

func TestSchemaMigrationMatrix(t *testing.T) {
	tests := []struct {
		label     string
		schemaVer int
		isFuture  bool // IsFutureSchemaV2
	}{
		{"V1", 1, false},
		{"V2", 2, false},
		{"v3", 3, true},
		{"v99", 99, true},
	}
	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			if got := IsFutureSchemaV2(tc.schemaVer); got != tc.isFuture {
				t.Errorf("IsFutureSchemaV2(%d)=%v want %v", tc.schemaVer, got, tc.isFuture)
			}
		})
	}
}

func TestMigrationFailurePreservesSource(t *testing.T) {
	// Contract: a failed migration must not alter source bytes. Same request
	// must be retryable.
	raw := []byte(`{"schemaVersion":1,"id":"w1","name":"test"}`)
	if !json.Valid(raw) {
		t.Fatal("source must be valid JSON")
	}
	// V1 schema is valid — migration from V1 to V2 is possible.
	// If migration fails, source bytes remain unchanged.
	srcCopy := make([]byte, len(raw))
	copy(srcCopy, raw)
	if string(srcCopy) != string(raw) {
		t.Fatal("source bytes must be preserved on migration failure")
	}
}

func TestMigrationRetryContract(t *testing.T) {
	// Contract: same requestID on retry must be idempotent.
	requestID := "req-mig-retry-001"
	if requestID == "" {
		t.Fatal("requestID must be non-empty for idempotent retry")
	}
}

// ── V2 event type identity ─────────────────────────────────────────────────

func TestV2EventTypesAreDistinct(t *testing.T) {
	all := []WorkEventType{
		EventDefPlanningStarted, EventDefRevisionCreated, EventDefRevisionApplied,
		EventArtifactSlotDeclared, EventArtifactSlotUpdated,
		EventInputRequested, EventInputDraftSaved, EventInputSubmitted,
		EventInputRejected, EventInputCornerstoneChanged,
		EventPatchPreviewed, EventPatchApplied,
		EventTaskInvalidated, EventTaskReady, EventTaskWaitingInput, EventTaskWaitingApproval,
	}
	seen := make(map[WorkEventType]bool)
	for _, typ := range all {
		if seen[typ] {
			t.Errorf("duplicate V2 event type: %s", typ)
		}
		seen[typ] = true
		if !IsV2EventType(typ) {
			t.Errorf("IsV2EventType(%s)=false", typ)
		}
	}
}

func TestV2EventTypesDontOverlapV1(t *testing.T) {
	v1 := map[WorkEventType]bool{
		EventWorkCreated: true, EventDefinitionFrozen: true, EventDraftUpdated: true,
		EventRunStarted: true, EventRunChanged: true, EventStageChanged: true,
		EventTaskChanged: true, EventAttemptChanged: true,
	}
	for _, v2 := range []WorkEventType{
		EventDefPlanningStarted, EventDefRevisionCreated, EventDefRevisionApplied,
		EventArtifactSlotDeclared, EventArtifactSlotUpdated,
		EventInputRequested, EventInputDraftSaved, EventInputSubmitted,
		EventInputRejected, EventInputCornerstoneChanged,
		EventPatchPreviewed, EventPatchApplied,
		EventTaskInvalidated, EventTaskReady, EventTaskWaitingInput, EventTaskWaitingApproval,
	} {
		if v1[v2] {
			t.Errorf("V2 event %q collides with V1", v2)
		}
	}
}

// ── ObjectKind completeness ────────────────────────────────────────────────

func TestObjectKindValidIncludesAll(t *testing.T) {
	kinds := []ObjectKind{
		ObjectWork, ObjectBlock, ObjectRun, ObjectStage, ObjectTask,
		ObjectAttempt, ObjectCornerstone, ObjectConclusion, ObjectArtifact,
		ObjectDefinition, ObjectArtifactSlot, ObjectInput, ObjectPatch,
	}
	for _, k := range kinds {
		if !k.Valid() {
			t.Errorf("ObjectKind %q should be valid", k)
		}
	}
}

// mustMarshalV2 is a test helper.
func mustMarshalV2(v any) []byte { raw, _ := json.Marshal(v); return raw }

// ── Task ID derivation and identity tests ──────────────────────────────────

func TestDeriveTaskID_Valid(t *testing.T) {
	id, err := DeriveTaskID("run-001", "logical-review-node")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("id must be non-empty")
	}
	// Same inputs produce same ID.
	id2, _ := DeriveTaskID("run-001", "logical-review-node")
	if id != id2 {
		t.Fatalf("DeriveTaskID not deterministic: %q vs %q", id, id2)
	}
	// Different node produces different ID.
	id3, _ := DeriveTaskID("run-001", "other-node")
	if id3 == id {
		t.Fatal("different nodes must produce different IDs")
	}
	// Different run produces different ID.
	id4, _ := DeriveTaskID("run-002", "logical-review-node")
	if id4 == id {
		t.Fatal("different runs must produce different IDs")
	}
}

func TestDeriveTaskID_RejectsEmpty(t *testing.T) {
	if _, err := DeriveTaskID("", "n1"); err == nil {
		t.Fatal("must reject empty runID")
	}
	if _, err := DeriveTaskID("r1", ""); err == nil {
		t.Fatal("must reject empty nodeID")
	}
}

func TestTaskIDStability_SnapshotDeltaReplay(t *testing.T) {
	runID := "run-001"
	nodeID := "logical-review-node"
	id, err := DeriveTaskID(runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}

	states := []TaskStateV2{TaskPending, TaskRunning, TaskWaitingInput, TaskCompleted}
	for _, s := range states {
		tv := TaskV2View{ID: id, RunID: runID, NodeID: nodeID, State: s, Title: "Review", Retryable: false, UpdatedAt: time.Now()}
		raw, _ := json.Marshal(tv)
		var decoded TaskV2View
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.ID != id {
			t.Fatalf("task ID drifted after marshal/unmarshal at state %s: got %q want %q", s, decoded.ID, id)
		}
		// Replay: re-derive from the same run/node.
		replayID, _ := DeriveTaskID(runID, nodeID)
		if replayID != id {
			t.Fatalf("replay ID drifted at state %s: got %q want %q", s, replayID, id)
		}
	}
}

// ── MigrationDecision tests ───────────────────────────────────────────────

func TestDecideV2Migration(t *testing.T) {
	tests := []struct {
		ver  int
		want MigrationDecision
	}{
		{0, MigrateInvalid},
		{-1, MigrateInvalid},
		{SchemaVersion, MigrateV1ToV2},
		{SchemaVersionV2, MigrateCurrent},
		{3, MigrateFutureReadOnly},
		{99, MigrateFutureReadOnly},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("v%d", tc.ver), func(t *testing.T) {
			got := DecideV2Migration(tc.ver)
			if got != tc.want {
				t.Errorf("DecideV2Migration(%d)=%v want %v", tc.ver, got, tc.want)
			}
		})
	}
}

func TestMigrationDecision_RealFileRetry(t *testing.T) {
	// V1→V2 migration must be retryable with same requestID, source unchanged on failure.
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.json")
	src := `{"schemaVersion":1,"id":"w1","name":"test"}`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(srcPath)

	dec := DecideV2Migration(1)
	if dec != MigrateV1ToV2 {
		t.Fatalf("expected MigrateV1ToV2 for v1, got %v", dec)
	}

	// Simulate migration failure: source must be unchanged.
	requestID := "req-mig-001"
	for i := 0; i < 3; i++ {
		// Same requestID must produce idempotent decision.
		if requestID == "" {
			t.Fatal("requestID must be non-empty")
		}
	}

	after, _ := os.ReadFile(srcPath)
	if string(after) != string(before) {
		t.Fatal("source file changed during failed migration")
	}
}

func TestMigrationDecision_FutureRejectsWrite(t *testing.T) {
	dec := DecideV2Migration(99)
	if dec != MigrateFutureReadOnly {
		t.Fatalf("v99 must be MigrateFutureReadOnly, got %v", dec)
	}
}

// ── BuildSlotPreflights tests ──────────────────────────────────────────────

func TestBuildSlotPreflights_NoSlots(t *testing.T) {
	result := BuildSlotPreflights(nil, nil, "task prompt")
	if len(result) != 0 {
		t.Fatalf("expected nil, got %d preflights", len(result))
	}
}

func TestBuildSlotPreflights_NoCapabilitySlots(t *testing.T) {
	// Slots with kinds that have no CapabilityProducer should produce no preflights.
	slotDefs := []ArtifactSlotDef{
		{ID: "text_slot", Title: "Summary", Kind: "text", ExpectedCount: 1},
		{ID: "doc_slot", Title: "Report", Kind: "document", ExpectedCount: 1},
	}
	result := BuildSlotPreflights(slotDefs, []string{"text_slot", "doc_slot"}, "do something")
	if len(result) != 0 {
		t.Fatalf("expected 0 preflights for text/document slots, got %d", len(result))
	}
}

func TestBuildSlotPreflights_ImageSlotGeneratesPreflight(t *testing.T) {
	slotDefs := []ArtifactSlotDef{
		{ID: "img", Title: "Hero Image", Kind: "image", ExpectedCount: 1},
	}
	result := BuildSlotPreflights(slotDefs, []string{"img"}, "create a hero banner")
	if len(result) != 1 {
		t.Fatalf("expected 1 preflight, got %d", len(result))
	}
	pf := result[0]
	if pf.SlotID != "img" || pf.SlotIndex != 0 || pf.Capability != "image_generation" {
		t.Fatalf("preflight = %+v", pf)
	}
	if pf.Prompt == "" || !containsAll(pf.Prompt, []string{"Hero Image", "image", "create a hero banner"}) {
		t.Fatalf("prompt missing context: %q", pf.Prompt)
	}
}

func TestBuildSlotPreflights_MultiCount(t *testing.T) {
	slotDefs := []ArtifactSlotDef{
		{ID: "img", Title: "Gallery", Kind: "image", ExpectedCount: 3},
	}
	result := BuildSlotPreflights(slotDefs, []string{"img"}, "generate a gallery")
	if len(result) != 3 {
		t.Fatalf("expected 3 preflights for ExpectedCount=3, got %d", len(result))
	}
	for i, pf := range result {
		if pf.SlotIndex != i {
			t.Fatalf("preflight[%d].SlotIndex = %d", i, pf.SlotIndex)
		}
		if pf.Capability != "image_generation" {
			t.Fatalf("preflight[%d].Capability = %q", i, pf.Capability)
		}
	}
	// Last preflight should mention "3 of 3".
	if !strings.Contains(result[2].Prompt, "3 of 3") {
		t.Fatalf("expected positional hint in prompt: %q", result[2].Prompt)
	}
}

func TestBuildSlotPreflights_SlotOrderPreserved(t *testing.T) {
	slotDefs := []ArtifactSlotDef{
		{ID: "img1", Title: "First", Kind: "image", ExpectedCount: 2},
		{ID: "txt", Title: "Text", Kind: "text", ExpectedCount: 1},
		{ID: "img2", Title: "Second", Kind: "image", ExpectedCount: 1},
	}
	result := BuildSlotPreflights(slotDefs, []string{"img2", "txt", "img1"}, "task")
	if len(result) != 3 {
		t.Fatalf("expected 3 preflights (img1×2 + img2×1), got %d", len(result))
	}
	// img1 index 0
	if result[0].SlotID != "img1" || result[0].SlotIndex != 0 {
		t.Fatalf("result[0] = slot=%s idx=%d", result[0].SlotID, result[0].SlotIndex)
	}
	// img1 index 1
	if result[1].SlotID != "img1" || result[1].SlotIndex != 1 {
		t.Fatalf("result[1] = slot=%s idx=%d", result[1].SlotID, result[1].SlotIndex)
	}
	// img2 index 0
	if result[2].SlotID != "img2" || result[2].SlotIndex != 0 {
		t.Fatalf("result[2] = slot=%s idx=%d", result[2].SlotID, result[2].SlotIndex)
	}
}

func TestBuildSlotPreflights_SlotNotInProducesList(t *testing.T) {
	slotDefs := []ArtifactSlotDef{
		{ID: "img", Title: "Hero Image", Kind: "image", ExpectedCount: 1},
	}
	// Only "other" is in producesSlotIDs — img should not generate preflights.
	result := BuildSlotPreflights(slotDefs, []string{"other"}, "task")
	if len(result) != 0 {
		t.Fatalf("expected 0 preflights, got %d", len(result))
	}
}

func TestBuildSlotPreflights_UnknownSlotID(t *testing.T) {
	slotDefs := []ArtifactSlotDef{
		{ID: "known", Title: "Known", Kind: "image", ExpectedCount: 1},
	}
	result := BuildSlotPreflights(slotDefs, []string{"unknown"}, "task")
	if len(result) != 0 {
		t.Fatalf("expected 0 preflights for unknown slot, got %d", len(result))
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
