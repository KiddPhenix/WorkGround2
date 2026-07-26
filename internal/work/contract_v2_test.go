package work

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ── Golden fixture equivalence ──────────────────────────────────────────────

var sharedV2FixtureNames = []string{
	"work-v2-artifact-slot.json", "work-v2-definition-revision.json",
	"work-v2-patch-preview.json", "work-v2-work-input.json",
	"work-v2-patch-receipt.json", "work-v2-patch-apply-result.json",
	"work-view-event-v2.json", "work-dto-fields-v2.json",
}

func TestGoTSV2FixturesEquivalent(t *testing.T) {
	for _, name := range sharedV2FixtureNames {
		goData := readFixture(t, v2FixtureDir, name)
		tsData := readFixture(t, tsFixtureDir, name)
		if !bytes.Equal(goData, tsData) {
			t.Errorf("Go and TypeScript V2 fixture %q differ", name)
		}
	}
}

func TestGoV2DTOFieldsMatchContract(t *testing.T) {
	var want map[string][]string
	raw := readFixture(t, v2FixtureDir, "work-dto-fields-v2.json")
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	tests := map[string]any{
		"ArtifactSlot": ArtifactSlot{}, "ArtifactError": ArtifactError{},
		"WorkDefinitionRevision": WorkDefinitionRevision{}, "NodeDef": NodeDef{},
		"ArtifactSlotDef": ArtifactSlotDef{}, "InputSpec": InputSpec{},
		"WorkInput": WorkInput{}, "WorkPatchPreview": WorkPatchPreview{}, "PatchOp": PatchOp{},
		"SubmitWorkInputRequest": SubmitWorkInputRequest{}, "InputSubmissionResult": InputSubmissionResult{},
		"SetInputCornerstoneRequest": SetInputCornerstoneRequest{}, "CornerstonePinResult": CornerstonePinResult{},
		"PreviewWorkPatchRequest": PreviewWorkPatchRequest{}, "ApplyWorkPatchRequest": ApplyWorkPatchRequest{},
		"ApplyDefinitionInput": ApplyDefinitionInput{}, "RetryWorkNodeRequest": RetryWorkNodeRequest{},
		"BeginWorkPlanningResult": BeginWorkPlanningResult{}, "ApplyDefinitionResult": ApplyDefinitionResult{},
		"SubmitInputResult": SubmitInputResult{}, "PreviewWorkPatchResult": PreviewWorkPatchResult{},
		"AutoSwitchFaceIntent": AutoSwitchFaceIntent{}, "RunImpact": RunImpact{},
		"RetryWorkNodeResult": RetryWorkNodeResult{}, "WorkTransportError": WorkTransportError{},
		"WorkViewV2": WorkViewV2{}, "TaskV2View": TaskV2View{},
		"DefPlanningStartedPayload": DefPlanningStartedPayload{},
		"DefRevisionCreatedPayload": DefRevisionCreatedPayload{}, "DefRevisionAppliedPayload": DefRevisionAppliedPayload{},
		"ArtifactSlotDeclaredPayload": ArtifactSlotDeclaredPayload{}, "ArtifactSlotUpdatedPayload": ArtifactSlotUpdatedPayload{},
		"InputRequestedPayload": InputRequestedPayload{}, "InputDraftSavedPayload": InputDraftSavedPayload{},
		"InputSubmittedPayload": InputSubmittedPayload{},
		"InputRejectedPayload":  InputRejectedPayload{}, "InputCornerstoneChangedPayload": InputCornerstoneChangedPayload{},
		"PatchPreviewedPayload": PatchPreviewedPayload{}, "PatchAppliedPayload": PatchAppliedPayload{},
		"PatchIntentReceipt": PatchIntentReceipt{}, "ApplyWorkPatchResult": ApplyWorkPatchResult{},
		"TaskReadyPayload": TaskReadyPayload{}, "TaskInvalidatedPayload": TaskInvalidatedPayload{},
		"TaskWaitingPayload": TaskWaitingPayload{},
	}
	if len(want) != len(tests) {
		t.Fatalf("contract count %d want %d", len(want), len(tests))
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			got := v2FrozenFields(name, jsonFields(reflect.TypeOf(value)))
			if f, ok := want[name]; !ok {
				t.Fatalf("missing %s", name)
			} else if !reflect.DeepEqual(got, f) {
				t.Fatalf("%s: got %v want %v", name, got, f)
			}
		})
	}
}

// The V2 fixture remains the frozen launch contract. These optional additive
// fields were introduced by the ArtifactSlot runtime implementation and are
// deliberately tested as a compatibility addendum instead of rewriting the
// frozen field list.
func v2FrozenFields(name string, fields []string) []string {
	addendum := map[string]map[string]bool{
		"NodeDef":                    {"blockIds": true, "producesSlotIds": true, "consumesSlotIds": true, "globalGate": true},
		"ArtifactSlot":               {"upstreamDigest": true},
		"ArtifactSlotUpdatedPayload": {"upstreamDigest": true, "receipt": true},
		"WorkInput":                  {"error": true, "source": true, "updatedBy": true},
		"PatchPreviewedPayload":      {"baseDefinitionRev": true, "baseBlockRev": true, "operations": true, "digest": true, "expiresAt": true, "receipt": true},
		"PatchAppliedPayload":        {"receipt": true},
	}
	omit := addendum[name]
	if len(omit) == 0 {
		return fields
	}
	base := make([]string, 0, len(fields))
	for _, field := range fields {
		if !omit[field] {
			base = append(base, field)
		}
	}
	return base
}

func TestV2ArtifactSlotContractAddendumIsOptional(t *testing.T) {
	for _, test := range []struct {
		typ   reflect.Type
		field string
	}{
		{reflect.TypeOf(ArtifactSlot{}), "UpstreamDigest"},
		{reflect.TypeOf(NodeDef{}), "BlockIDs"},
		{reflect.TypeOf(NodeDef{}), "ProducesSlotIDs"},
		{reflect.TypeOf(NodeDef{}), "ConsumesSlotIDs"},
		{reflect.TypeOf(ArtifactSlotUpdatedPayload{}), "UpstreamDigest"},
		{reflect.TypeOf(ArtifactSlotUpdatedPayload{}), "Receipt"},
		{reflect.TypeOf(PatchPreviewedPayload{}), "BaseDefinitionRev"},
		{reflect.TypeOf(PatchPreviewedPayload{}), "BaseBlockRev"},
		{reflect.TypeOf(PatchPreviewedPayload{}), "Operations"},
		{reflect.TypeOf(PatchPreviewedPayload{}), "Digest"},
		{reflect.TypeOf(PatchPreviewedPayload{}), "ExpiresAt"},
		{reflect.TypeOf(PatchPreviewedPayload{}), "Receipt"},
		{reflect.TypeOf(PatchAppliedPayload{}), "Receipt"},
		{reflect.TypeOf(NodeDef{}), "GlobalGate"},
	} {
		field, ok := test.typ.FieldByName(test.field)
		if !ok || !strings.Contains(field.Tag.Get("json"), "omitempty") {
			t.Fatalf("%s.%s must be an optional contract addendum", test.typ.Name(), test.field)
		}
	}
}

// ── 16 V2 event positive marshal roundtrip ──────────────────────────────────

var allV2Types = []WorkEventType{
	EventDefPlanningStarted, EventDefRevisionCreated, EventDefRevisionApplied,
	EventArtifactSlotDeclared, EventArtifactSlotUpdated,
	EventInputRequested, EventInputDraftSaved, EventInputSubmitted,
	EventInputRejected, EventInputCornerstoneChanged,
	EventPatchPreviewed, EventPatchApplied,
	EventTaskInvalidated, EventTaskReady, EventTaskWaitingInput, EventTaskWaitingApproval,
	EventTaskRuntimeCreated, EventTaskRuntimeUpdated, EventTaskStaleResult,
}

func TestAllV2WorkEventsMarshalRoundTrip(t *testing.T) {
	for _, typ := range allV2Types {
		t.Run(string(typ), func(t *testing.T) {
			ev := validV2Event(typ)
			if err := ValidateV2WorkEvent(ev); err != nil {
				t.Errorf("%s: %v", typ, err)
			}
			if err := ValidateV2WorkEventPayload(typ, ev.Payload); err != nil {
				t.Errorf("%s payload: %v", typ, err)
			}
			raw, _ := json.Marshal(ev)
			var d WorkEvent
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if d.RequestID != ev.RequestID || d.Type != typ {
				t.Error("round-trip drift")
			}
			v1Ctx := ObjectContext{Kind: ObjectWork, ID: "w1"}
			v1Raw, _ := json.Marshal(v1Ctx)
			for _, f := range []string{`"workID"`, `"expectedRevision"`, `"definitionRevision"`} {
				if bytes.Contains(v1Raw, []byte(f)) {
					t.Errorf("V1 drift: %s", f)
				}
			}
		})
	}
}

// ── ObjectContext negative tests ───────────────────────────────────────────

func TestValidateV2WorkEvent_ContextMissingRunID(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.Object.RunID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing runID")
	}
}
func TestValidateV2WorkEvent_ContextMissingTaskID(t *testing.T) {
	ev := validV2Event(EventTaskWaitingInput)
	ev.Object.TaskID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing taskID")
	}
}
func TestValidateV2WorkEvent_ContextMissingBlockID(t *testing.T) {
	ev := validV2Event(EventInputRequested)
	ev.Object.BlockID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing blockID")
	}
}
func TestValidateV2WorkEvent_ContextMissingInputID(t *testing.T) {
	ev := validV2Event(EventInputSubmitted)
	ev.Object.InputID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing inputID")
	}
}
func TestValidateV2WorkEvent_ContextMissingDefinitionID(t *testing.T) {
	ev := validV2Event(EventDefPlanningStarted)
	ev.Object.DefinitionID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing definitionID")
	}
}
func TestValidateV2WorkEvent_ContextMissingSlotID(t *testing.T) {
	ev := validV2Event(EventArtifactSlotDeclared)
	ev.Object.ArtifactSlotID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing slotID")
	}
}
func TestValidateV2WorkEvent_ContextMissingPatchID(t *testing.T) {
	ev := validV2Event(EventPatchPreviewed)
	ev.Object.PatchID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing patchID")
	}
}
func TestValidateV2WorkEvent_ContextMissingExpectedRevision(t *testing.T) {
	ev := validV2Event(EventDefRevisionApplied)
	ev.Object.ExpectedRevision = nil
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing expectedRevision")
	}
}
func TestValidateV2WorkEvent_ContextNegativeExpectedRevision(t *testing.T) {
	ev := validV2Event(EventDefRevisionApplied)
	ev.Object.ExpectedRevision = int64Ptr(-1)
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject negative expectedRevision")
	}
}
func TestValidateV2WorkEvent_ContextWorkIDMismatch(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.Object.WorkID = "DIFF"
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject workID mismatch")
	}
}
func TestValidateV2WorkEvent_ContextKindIDMismatch(t *testing.T) {
	ev := validV2Event(EventArtifactSlotDeclared)
	ev.Object.ArtifactSlotID = ev.Object.ID // ok — must match primary typed ID
	if err := ValidateV2WorkEvent(ev); err != nil {
		t.Fatal(err)
	}
	ev.Object.ArtifactSlotID = "different"
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject kind/ID mismatch")
	}
}
func TestValidateV2WorkEvent_ContextWrongKind(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.Object.Kind = ObjectDefinition
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject wrong Kind")
	}
}
func TestValidateV2WorkEvent_ContextMissingDefinitionRevision(t *testing.T) {
	ev := validV2Event(EventDefRevisionCreated)
	ev.Object.DefinitionRevision = nil
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject missing definitionRevision")
	}
}
func TestValidateV2WorkEvent_ContextNegativeDefinitionRevision(t *testing.T) {
	ev := validV2Event(EventDefRevisionCreated)
	ev.Object.DefinitionRevision = int64Ptr(-1)
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject negative definitionRevision")
	}
}

// ── Per-type payload negative tests (30+) ──────────────────────────────────

func TestValidateV2WorkEventPayload_NegativePerType(t *testing.T) {
	tests := []struct {
		name    string
		typ     WorkEventType
		payload string
	}{
		{"planning missing sessionId", EventDefPlanningStarted, `{"workId":"w1"}`},
		{"planning missing workId", EventDefPlanningStarted, `{"sessionId":"s1"}`},
		{"revision-created missing digest", EventDefRevisionCreated, `{"workId":"w1","revision":1,"parentRevision":0}`},
		{"revision-created negative revision", EventDefRevisionCreated, `{"workId":"w1","revision":-1,"parentRevision":0,"digest":"d"}`},
		{"revision-applied missing expected", EventDefRevisionApplied, `{"workId":"w1","revision":2,"previousRevision":1}`},
		{"revision-applied negative expected", EventDefRevisionApplied, `{"workId":"w1","revision":2,"previousRevision":1,"expectedRevision":-1}`},
		{"slot-declared missing title", EventArtifactSlotDeclared, `{"slotId":"s","workId":"w","definitionRev":1,"kind":"docx","expectedCount":1}`},
		{"slot-declared expectedCount 0", EventArtifactSlotDeclared, `{"slotId":"s","workId":"w","definitionRev":1,"title":"T","kind":"docx","expectedCount":0}`},
		{"slot-declared negative definitionRev", EventArtifactSlotDeclared, `{"slotId":"s","workId":"w","definitionRev":-1,"title":"T","kind":"docx","expectedCount":1}`},
		{"slot-updated invalid state", EventArtifactSlotUpdated, `{"slotId":"s","workId":"w","state":"BOGUS","revision":1}`},
		{"slot-updated failed no error", EventArtifactSlotUpdated, `{"slotId":"s","workId":"w","state":"failed","revision":1}`},
		{"slot-updated progress >1", EventArtifactSlotUpdated, `{"slotId":"s","workId":"w","state":"generating","revision":1,"progress":1.5}`},
		{"slot-updated negative revision", EventArtifactSlotUpdated, `{"slotId":"s","workId":"w","state":"generating","revision":-1}`},
		{"slot-updated empty ref status", EventArtifactSlotUpdated, `{"slotId":"s","workId":"w","state":"generating","revision":1,"refs":[{"id":"a","status":""}]}`},
		{"slot-updated unknown ref status", EventArtifactSlotUpdated, `{"slotId":"s","workId":"w","state":"generating","revision":1,"refs":[{"id":"a","status":"uploaded"}]}`},
		{"input-requested missing taskId", EventInputRequested, `{"inputId":"i","workId":"w","runId":"r","blockId":"b","specId":"s"}`},
		{"input-requested missing specId", EventInputRequested, `{"inputId":"i","workId":"w","runId":"r","taskId":"t","blockId":"b"}`},
		{"input-draft missing blockId", EventInputDraftSaved, `{"inputId":"i","workId":"w","runId":"r","taskId":"t","specId":"s"}`},
		{"input-submitted missing revision", EventInputSubmitted, `{"inputId":"i","workId":"w","expectedRevision":1}`},
		{"input-submitted negative expectedRevision", EventInputSubmitted, `{"inputId":"i","workId":"w","revision":1,"expectedRevision":-1}`},
		{"input-rejected missing inputId", EventInputRejected, `{"workId":"w"}`},
		{"cornerstone missing cornerstoneId", EventInputCornerstoneChanged, `{"inputId":"i","workId":"w","pinned":true,"expectedRevision":1}`},
		{"cornerstone negative expected", EventInputCornerstoneChanged, `{"inputId":"i","workId":"w","cornerstoneId":"c","pinned":true,"expectedRevision":-1}`},
		{"patch-preview invalid scope", EventPatchPreviewed, `{"patchId":"p","workId":"w","scope":"BOGUS","affectedNodeIds":["n1"],"requiresRerun":true}`},
		{"patch-preview empty element affected", EventPatchPreviewed, `{"patchId":"p","workId":"w","scope":"block","affectedNodeIds":[""],"requiresRerun":true}`},
		{"patch-applied missing expected", EventPatchApplied, `{"patchId":"p","workId":"w","scope":"workflow","newRevision":1}`},
		{"patch-applied negative newRevision", EventPatchApplied, `{"patchId":"p","workId":"w","scope":"workflow","newRevision":-1,"expectedRevision":1}`},
		{"task-invalidated missing runID", EventTaskInvalidated, `{"taskId":"t","workId":"w"}`},
		{"task-ready missing taskId", EventTaskReady, `{"workId":"w","runId":"r"}`},
		{"task-waiting empty inputIds", EventTaskWaitingInput, `{"taskId":"t","workId":"w","runId":"r","inputIds":[]}`},
		{"task-waiting-approval empty token", EventTaskWaitingApproval, `{"taskId":"t","workId":"w","runId":"r","approvalToken":""}`},
		{"not JSON object", EventTaskReady, `"not an object"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateV2WorkEventPayload(tc.typ, json.RawMessage(tc.payload)); err == nil {
				t.Fatalf("must reject: %s", tc.name)
			}
		})
	}
}

func TestValidateV2WorkEvent_RejectsV1Schema(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.SchemaVersion = SchemaVersion
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject V1 schema")
	}
}
func TestValidateV2WorkEvent_RejectsUnknownType(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.Type = "v2.bogus"
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject unknown")
	}
}
func TestValidateV2WorkEvent_MissingRequestID(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.RequestID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject empty requestID")
	}
}
func TestValidateV2WorkEvent_MissingID(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.ID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject empty ID")
	}
}
func TestValidateV2WorkEvent_MissingWorkID(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.WorkID = ""
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject empty workID")
	}
}
func TestValidateV2WorkEvent_NegativeBaseRevision(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.BaseRevision = -1
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject negative baseRevision")
	}
}
func TestValidateV2WorkEvent_PayloadWorkIDMismatch(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.WorkID = "DIFFERENT"
	if err := ValidateV2WorkEvent(ev); err == nil {
		t.Fatal("reject workID mismatch")
	}
}

// ── Append/Replay V2 event ─────────────────────────────────────────────────

func TestAppendAndReplayV2Event(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "w-v2-append")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workDir); err != nil {
		t.Fatal(err)
	}
	defer ReleaseWorkLease(workDir)
	ev := validV2Event(EventDefPlanningStarted)
	rev, err := AppendWorkEvent(workDir, ev, false)
	if err != nil {
		t.Fatalf("Append V2: %v", err)
	}
	if rev != 1 {
		t.Fatalf("rev=%d want 1", rev)
	}
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ReadOnly {
		t.Fatalf("read-only: %s", replay.ReadOnlyReason)
	}
	if len(replay.Events) != 1 {
		t.Fatalf("events=%d", len(replay.Events))
	}
	if replay.Events[0].SchemaVersion != SchemaVersionV2 {
		t.Fatal("schema lost")
	}
}

func TestAppendV2Event_Schema1RejectsV2Type(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "w1")
	os.MkdirAll(workDir, 0o755)
	AcquireWorkLease(workDir)
	defer ReleaseWorkLease(workDir)
	ev := validV2Event(EventTaskReady)
	ev.SchemaVersion = SchemaVersion
	_, err := AppendWorkEvent(workDir, ev, false)
	if err == nil {
		t.Fatal("schema=1 must reject V2 type")
	}
}
func TestAppendV2Event_Schema2RejectsV1Type(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "w2")
	os.MkdirAll(workDir, 0o755)
	AcquireWorkLease(workDir)
	defer ReleaseWorkLease(workDir)
	ev := validV2Event(EventTaskReady)
	ev.Type = EventWorkCreated
	_, err := AppendWorkEvent(workDir, ev, false)
	if err == nil {
		t.Fatal("schema=2 must reject V1 type")
	}
}
func TestAppendV2Event_Schema0Rejected(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "w0")
	os.MkdirAll(workDir, 0o755)
	AcquireWorkLease(workDir)
	defer ReleaseWorkLease(workDir)
	ev := validV2Event(EventTaskReady)
	ev.SchemaVersion = 0
	_, err := AppendWorkEvent(workDir, ev, false)
	if err == nil {
		t.Fatal("schema=0 must be rejected")
	}
}
func TestAppendV2Event_SchemaFutureRejected(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "w99")
	os.MkdirAll(workDir, 0o755)
	AcquireWorkLease(workDir)
	defer ReleaseWorkLease(workDir)
	ev := validV2Event(EventTaskReady)
	ev.SchemaVersion = 99
	_, err := AppendWorkEvent(workDir, ev, false)
	if err == nil {
		t.Fatal("schema=99 must be rejected")
	}
}
func TestAppendV2EventRejectedByV1OnlyPath(t *testing.T) {
	if err := CheckSchemaVersion("WorkEvent", SchemaVersionV2); err == nil {
		t.Fatal("V1-only CheckSchemaVersion must reject V2")
	}
}

// ── V2 digest includes ObjectContext ───────────────────────────────────────

func TestV2DigestChangesWithDifferentObject(t *testing.T) {
	ev1 := validV2Event(EventTaskReady)
	ev2 := validV2Event(EventTaskReady)
	ev2.Object.BlockID = "block-different"
	if !bytes.Equal(ev1.Payload, ev2.Payload) {
		t.Fatal("payloads must be identical for this test")
	}
	r1 := recordFromEvent(ev1)
	r2 := recordFromEvent(ev2)
	d1, _ := workEventContentDigest(r1)
	d2, _ := workEventContentDigest(r2)
	if d1 == d2 {
		t.Fatal("different Object must produce different digest")
	}
}

func TestV2SameRequestDifferentObjectConflict(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "w-v2-conflict")
	os.MkdirAll(workDir, 0o755)
	AcquireWorkLease(workDir)
	defer ReleaseWorkLease(workDir)

	ev1 := validV2Event(EventTaskReady)
	if _, err := AppendWorkEvent(workDir, ev1, false); err != nil {
		t.Fatal(err)
	}

	ev2 := validV2Event(EventTaskReady)
	ev2.Revision = 0
	ev2.BaseRevision = 0
	ev2.Object.BlockID = "block-other"
	if !bytes.Equal(ev1.Payload, ev2.Payload) {
		t.Fatal("payloads must be identical")
	}
	_, err := AppendWorkEvent(workDir, ev2, false)
	if err == nil {
		t.Fatal("same requestID + different Object must conflict")
	}
	var conflict *ErrWorkEventConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("must be *ErrWorkEventConflict, got %T", err)
	}
	if conflict.Kind != WorkEventRequestConflict {
		t.Fatalf("Kind=%q want %q", conflict.Kind, WorkEventRequestConflict)
	}
}

func TestV1WorkEventJSONHasObjectFieldAbsent(t *testing.T) {
	rec := workEventRecord{SchemaVersion: SchemaVersion}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"object"`)) {
		t.Fatal("V1 event JSON must not contain object field")
	}
}

func TestV2WorkEventJSONHasObjectField(t *testing.T) {
	rec := workEventRecord{SchemaVersion: SchemaVersionV2, Object: ObjectContext{WorkID: "w1", Kind: ObjectTask, ID: "t1", TaskID: "t1", RunID: "r1"}}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"object"`)) {
		t.Fatal("V2 event JSON must contain object field")
	}
}

func TestV2ZeroObjectMarshalStillHasObjectField(t *testing.T) {
	rec := workEventRecord{SchemaVersion: SchemaVersionV2}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"object"`)) {
		t.Fatal("V2 zero Object must still serialize object field")
	}
}

func TestPayloadContextCrossCheck_RunIDMismatch(t *testing.T) {
	ev := validV2Event(EventTaskReady)
	ev.Object.RunID = "run-wrong"
	if err := validateV2PayloadContextCrossCheck(ev.Type, ev.Payload, ev.Object); err == nil {
		t.Fatal("detect runID mismatch")
	}
}
func TestPayloadContextCrossCheck_TaskIDMismatch(t *testing.T) {
	ev := validV2Event(EventTaskInvalidated)
	ev.Object.TaskID = "task-wrong"
	if err := validateV2PayloadContextCrossCheck(ev.Type, ev.Payload, ev.Object); err == nil {
		t.Fatal("detect taskID mismatch")
	}
}
func TestPayloadContextCrossCheck_SlotIDMismatch(t *testing.T) {
	ev := validV2Event(EventArtifactSlotDeclared)
	ev.Object.ArtifactSlotID = "slot-wrong"
	if err := validateV2PayloadContextCrossCheck(ev.Type, ev.Payload, ev.Object); err == nil {
		t.Fatal("detect slotID mismatch")
	}
}
func TestPayloadContextCrossCheck_PatchIDMismatch(t *testing.T) {
	ev := validV2Event(EventPatchPreviewed)
	ev.Object.PatchID = "patch-wrong"
	if err := validateV2PayloadContextCrossCheck(ev.Type, ev.Payload, ev.Object); err == nil {
		t.Fatal("detect patchID mismatch")
	}
}
func TestPayloadContextCrossCheck_InputBlockIDMismatch(t *testing.T) {
	ev := validV2Event(EventInputRequested)
	ev.Object.BlockID = "block-wrong"
	if err := validateV2PayloadContextCrossCheck(ev.Type, ev.Payload, ev.Object); err == nil {
		t.Fatal("detect blockID mismatch")
	}
}

// ── 16-event WorkID + specific ID mismatch table ────────────────────────────

var allV2TypesForTest = []WorkEventType{
	EventDefPlanningStarted, EventDefRevisionCreated, EventDefRevisionApplied,
	EventArtifactSlotDeclared, EventArtifactSlotUpdated,
	EventInputRequested, EventInputDraftSaved, EventInputSubmitted, EventInputRejected,
	EventInputCornerstoneChanged,
	EventPatchPreviewed, EventPatchApplied,
	EventTaskInvalidated, EventTaskReady, EventTaskWaitingInput, EventTaskWaitingApproval,
}

func Test16Events_WorkIDMismatchViaValidateV2WorkEvent(t *testing.T) {
	for _, typ := range allV2TypesForTest {
		t.Run(string(typ), func(t *testing.T) {
			ev := validV2Event(typ)
			origPayload := append([]byte(nil), ev.Payload...)
			ev.Object.WorkID = "WRONG"
			ev.WorkID = "WRONG"
			if !bytes.Equal(ev.Payload, origPayload) {
				t.Fatal("payload must stay unchanged")
			}
			if err := ValidateV2WorkEvent(ev); err == nil {
				t.Fatal("detect workID mismatch")
			}
		})
	}
}

func Test16Events_SpecificIDMismatch(t *testing.T) {
	tests := []struct {
		name   string
		typ    WorkEventType
		mutate func(ev *WorkEvent)
	}{
		{"def planning defID wrong", EventDefPlanningStarted, func(ev *WorkEvent) { ev.Object.DefinitionID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"def created defID wrong", EventDefRevisionCreated, func(ev *WorkEvent) { ev.Object.DefinitionID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"def applied defID wrong", EventDefRevisionApplied, func(ev *WorkEvent) { ev.Object.DefinitionID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"slot declared slotID wrong", EventArtifactSlotDeclared, func(ev *WorkEvent) { ev.Object.ArtifactSlotID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"slot updated slotID wrong", EventArtifactSlotUpdated, func(ev *WorkEvent) { ev.Object.ArtifactSlotID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"input requested inputID wrong", EventInputRequested, func(ev *WorkEvent) { ev.Object.InputID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"input requested runID wrong", EventInputRequested, func(ev *WorkEvent) { ev.Object.RunID = "WRONG" }},
		{"input requested taskID wrong", EventInputRequested, func(ev *WorkEvent) { ev.Object.TaskID = "WRONG" }},
		{"input requested blockID wrong", EventInputRequested, func(ev *WorkEvent) { ev.Object.BlockID = "WRONG" }},
		{"input draft inputID wrong", EventInputDraftSaved, func(ev *WorkEvent) { ev.Object.InputID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"cornerstone inputID wrong", EventInputCornerstoneChanged, func(ev *WorkEvent) { ev.Object.InputID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"patch preview patchID wrong", EventPatchPreviewed, func(ev *WorkEvent) { ev.Object.PatchID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"patch applied patchID wrong", EventPatchApplied, func(ev *WorkEvent) { ev.Object.PatchID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"task invalidated run+task wrong", EventTaskInvalidated, func(ev *WorkEvent) { ev.Object.TaskID = "WRONG"; ev.Object.ID = "WRONG"; ev.Object.RunID = "WRONG" }},
		{"task ready taskID wrong", EventTaskReady, func(ev *WorkEvent) { ev.Object.TaskID = "WRONG"; ev.Object.ID = "WRONG" }},
		{"task waiting runID wrong", EventTaskWaitingInput, func(ev *WorkEvent) { ev.Object.RunID = "WRONG" }},
		{"task waiting approval taskID wrong", EventTaskWaitingApproval, func(ev *WorkEvent) { ev.Object.TaskID = "WRONG"; ev.Object.ID = "WRONG" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := validV2Event(tc.typ)
			tc.mutate(&ev)
			if err := ValidateV2WorkEvent(ev); err == nil {
				t.Fatal("detect mismatch")
			}
		})
	}
}

func TestV1AppendJSONHasNoObjectField(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "w-v1-golden")
	os.MkdirAll(workDir, 0o755)
	AcquireWorkLease(workDir)
	defer ReleaseWorkLease(workDir)
	ev := WorkEvent{SchemaVersion: SchemaVersion, ID: "ev-1", RequestID: "r1", WorkID: "w1", Type: EventWorkCreated,
		Payload: json.RawMessage(`{"schemaVersion":1,"id":"w1","name":"test","state":"draft","archiveState":"active","blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"definitionSnapshot":{"schemaVersion":1,"revision":1,"blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"promptTemplate":"","workflow":{"stages":[]},"blockSpecs":[],"digest":"sha256:abc"},"blocks":[],"placements":[],"prompt":"","cornerstones":[],"runs":[],"createdWith":{"workSchemaVersion":1,"eventSchemaVersion":1,"rendererSetVersion":1},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`)}
	if _, err := AppendWorkEvent(workDir, ev, false); err != nil {
		t.Fatal(err)
	}
	logPath := WorkEventLogPath(workDir)
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(logData, []byte(`"object"`)) {
		t.Fatal("V1 events.jsonl must not contain object field")
	}
	// Legacy V1 digest: old algorithm (excludes Object) must match current.
	rec := recordFromEvent(ev)
	legacy, _ := legacyV1ContentDigest(rec)
	actual, _ := workEventContentDigest(rec)
	if legacy != actual {
		t.Fatalf("V1 digest changed: legacy=%s actual=%s", legacy, actual)
	}
	// V1 replay must succeed.
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if replay.NeedsRepair {
		t.Fatal("V1 replay must not NeedsRepair")
	}
}

func legacyV1ContentDigest(r workEventRecord) (string, error) {
	c := struct {
		ID           string          `json:"id"`
		RequestID    string          `json:"requestId"`
		WorkID       string          `json:"workId"`
		Type         WorkEventType   `json:"type"`
		Revision     int64           `json:"revision"`
		BaseRevision int64           `json:"baseRevision"`
		Payload      json.RawMessage `json:"payload"`
	}{r.ID, r.RequestID, r.WorkID, r.Type, r.Revision, r.BaseRevision, r.Payload}
	return hashCanonical(c)
}

func TestV2ReplayDetectsTamperedObject(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "w-v2-tamper")
	os.MkdirAll(workDir, 0o755)
	AcquireWorkLease(workDir)
	defer ReleaseWorkLease(workDir)
	ev := validV2Event(EventTaskReady)
	if _, err := AppendWorkEvent(workDir, ev, false); err != nil {
		t.Fatal(err)
	}
	logPath := WorkEventLogPath(workDir)
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logData, []byte(`"runID":"run-test"`)) {
		t.Fatal("original must contain expected runID")
	}
	tampered := bytes.Replace(logData, []byte(`"runID":"run-test"`), []byte(`"runID":"run-tampered"`), 1)
	if bytes.Equal(tampered, logData) {
		t.Fatal("tamper must actually change the data")
	}
	os.WriteFile(logPath, tampered, 0o644)
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.NeedsRepair {
		t.Fatal("tampered Object must cause NeedsRepair")
	}
	if len(replay.Events) > 0 {
		t.Fatal("tampered event must not be in trusted Events history")
	}
	if !replay.NeedsRepair {
		t.Fatal("tampered Object must cause NeedsRepair")
	}
}

// ── validV2Event helper ────────────────────────────────────────────────────

func validV2Event(typ WorkEventType) WorkEvent {
	now, _ := time.Parse(time.RFC3339, "2026-07-23T10:00:00Z")
	expires := now.Add(time.Hour)
	ev := WorkEvent{SchemaVersion: SchemaVersionV2, ID: "ev-test", RequestID: "req-test", WorkID: "w-test", Type: typ, Revision: 1, BaseRevision: 0, ContentDigest: "sha256:test", WriterID: "writer-test", CreatedAt: now}
	switch typ {
	case EventDefPlanningStarted:
		ev.Payload = mustJSON(DefPlanningStartedPayload{WorkID: "w-test", SessionID: "s1"})
		ev.Object = ObjectContext{Kind: ObjectDefinition, ID: "w-test", WorkID: "w-test", DefinitionID: "w-test"}
	case EventDefRevisionCreated:
		ev.Payload = mustJSON(DefRevisionCreatedPayload{WorkID: "w-test", Revision: 1, ParentRevision: 0, Digest: "sha256:abc"})
		ev.Object = ObjectContext{Kind: ObjectDefinition, ID: "w-test", WorkID: "w-test", DefinitionID: "w-test", DefinitionRevision: int64Ptr(1)}
	case EventDefRevisionApplied:
		ev.Payload = mustJSON(DefRevisionAppliedPayload{WorkID: "w-test", Revision: 2, PreviousRevision: 1, ExpectedRevision: 1})
		ev.Object = ObjectContext{Kind: ObjectDefinition, ID: "w-test", WorkID: "w-test", DefinitionID: "w-test", ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(2)}
	case EventArtifactSlotDeclared:
		ev.Payload = mustJSON(ArtifactSlotDeclaredPayload{SlotID: "slot-1", WorkID: "w-test", DefinitionRev: 1, Title: "S", Kind: "docx", ExpectedCount: 1, Required: true})
		ev.Object = ObjectContext{Kind: ObjectArtifactSlot, ID: "slot-1", WorkID: "w-test", ArtifactSlotID: "slot-1", DefinitionRevision: int64Ptr(1)}
	case EventArtifactSlotUpdated:
		ev.Payload = mustJSON(ArtifactSlotUpdatedPayload{SlotID: "slot-2", WorkID: "w-test", State: SlotGenerating, Revision: 3})
		ev.Object = ObjectContext{Kind: ObjectArtifactSlot, ID: "slot-2", WorkID: "w-test", ArtifactSlotID: "slot-2", DefinitionRevision: int64Ptr(1)}
	case EventInputRequested:
		ev.Payload = mustJSON(InputRequestedPayload{InputID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", SpecID: "spec-test"})
		ev.Object = ObjectContext{Kind: ObjectInput, ID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", InputID: "i1", SpecID: "spec-test", DefinitionRevision: int64Ptr(1)}
	case EventInputDraftSaved:
		ev.Payload = mustJSON(InputDraftSavedPayload{InputID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", SpecID: "spec-test", Value: json.RawMessage(`"draft"`), Revision: 2, ExpectedRevision: 1})
		ev.Object = ObjectContext{Kind: ObjectInput, ID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", InputID: "i1", SpecID: "spec-test", ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1)}
	case EventInputSubmitted:
		ev.Payload = mustJSON(InputSubmittedPayload{InputID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", SpecID: "spec-test", Value: json.RawMessage(`"value"`), Revision: 2, ExpectedRevision: 1})
		ev.Object = ObjectContext{Kind: ObjectInput, ID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", InputID: "i1", SpecID: "spec-test", ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1)}
	case EventInputRejected:
		ev.Payload = mustJSON(InputRejectedPayload{InputID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", SpecID: "spec-test", Value: json.RawMessage(`"bad"`), Revision: 2, ExpectedRevision: 1})
		ev.Object = ObjectContext{Kind: ObjectInput, ID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", InputID: "i1", SpecID: "spec-test", ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1)}
	case EventInputCornerstoneChanged:
		ev.Payload = mustJSON(InputCornerstoneChangedPayload{InputID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", SpecID: "spec-test", CornerstoneID: "c1", Pinned: true, Revision: 2, ExpectedRevision: 1})
		ev.Object = ObjectContext{Kind: ObjectInput, ID: "i1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", InputID: "i1", SpecID: "spec-test", ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1)}
	case EventPatchPreviewed:
		ev.Payload = mustJSON(PatchPreviewedPayload{
			PatchID: "p1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test",
			BlockID: "blk-test", SessionID: "session-test", Scope: PatchBlock,
			BaseDefinitionRev: 1, BaseBlockRev: 1,
			Operations:      []PatchOp{{Op: "replace", Path: "blocks/blk-test/title", NewValue: json.RawMessage(`"new"`)}},
			AffectedNodeIDs: []string{"n1"}, RequiresRerun: true,
			Digest: "sha256:patch", ExpiresAt: &expires,
		})
		ev.Object = ObjectContext{Kind: ObjectPatch, ID: "p1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", PatchID: "p1", DefinitionRevision: int64Ptr(1)}
	case EventPatchApplied:
		ev.Payload = mustJSON(PatchAppliedPayload{PatchID: "p1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", Scope: PatchWorkflow, NewRevision: 4, ExpectedRevision: 3})
		ev.Object = ObjectContext{Kind: ObjectPatch, ID: "p1", WorkID: "w-test", RunID: "run-test", TaskID: "task-test", BlockID: "blk-test", PatchID: "p1", ExpectedRevision: int64Ptr(3), DefinitionRevision: int64Ptr(4)}
	case EventTaskInvalidated:
		ev.Payload = mustJSON(TaskInvalidatedPayload{TaskID: "t1", WorkID: "w-test", RunID: "run-test"})
		ev.Object = ObjectContext{Kind: ObjectTask, ID: "t1", WorkID: "w-test", RunID: "run-test", TaskID: "t1"}
	case EventTaskReady:
		ev.Payload = mustJSON(TaskReadyPayload{TaskID: "t1", WorkID: "w-test", RunID: "run-test"})
		ev.Object = ObjectContext{Kind: ObjectTask, ID: "t1", WorkID: "w-test", RunID: "run-test", TaskID: "t1"}
	case EventTaskWaitingInput:
		ev.Payload = mustJSON(TaskWaitingPayload{TaskID: "t1", WorkID: "w-test", RunID: "run-test", InputIDs: []string{"i1"}})
		ev.Object = ObjectContext{Kind: ObjectTask, ID: "t1", WorkID: "w-test", RunID: "run-test", TaskID: "t1"}
	case EventTaskWaitingApproval:
		ev.Payload = mustJSON(TaskWaitingPayload{TaskID: "t1", WorkID: "w-test", RunID: "run-test", ApprovalToken: "tok1"})
		ev.Object = ObjectContext{Kind: ObjectTask, ID: "t1", WorkID: "w-test", RunID: "run-test", TaskID: "t1"}
	case EventTaskRuntimeCreated:
		ev.Payload = mustJSON(TaskRuntimeCreatedPayload{
			TaskID: "t1", WorkID: "w-test", RunID: "run-test",
			NodeID: "n1", ExpectedRevision: 0, DefinitionRev: 1, SideEffectClass: "read",
			Runtime: V2TaskRuntime{
				TaskID: "t1", WorkID: "w-test", RunID: "run-test",
				NodeID: "n1", DefinitionRev: 1, SideEffectClass: "read",
				State: TaskPending, Revision: 1, UpdatedAt: now,
			},
		})
		ev.Object = ObjectContext{Kind: ObjectTask, ID: "t1", WorkID: "w-test", RunID: "run-test", TaskID: "t1", ExpectedRevision: int64Ptr(0), DefinitionRevision: int64Ptr(1)}
	case EventTaskRuntimeUpdated:
		ev.Payload = mustJSON(TaskRuntimeUpdatedPayload{
			TaskID: "t1", WorkID: "w-test", RunID: "run-test",
			ExpectedRevision: 1, State: TaskRunning,
			Runtime: V2TaskRuntime{
				TaskID: "t1", WorkID: "w-test", RunID: "run-test",
				NodeID: "n1", DefinitionRev: 1, SideEffectClass: "read",
				State: TaskRunning, Revision: 2, UpdatedAt: now,
			},
		})
		ev.Object = ObjectContext{Kind: ObjectTask, ID: "t1", WorkID: "w-test", RunID: "run-test", TaskID: "t1", ExpectedRevision: int64Ptr(1), DefinitionRevision: int64Ptr(1)}
	case EventTaskStaleResult:
		ev.Payload = mustJSON(TaskStaleResultPayload{
			TaskID: "t1", WorkID: "w-test", RunID: "run-test",
			ExpectedRevision: 2, AttemptID: "t1-attempt-0", StaleToken: "tok-old", CurrentToken: "tok-new",
		})
		ev.Object = ObjectContext{Kind: ObjectTask, ID: "t1", WorkID: "w-test", RunID: "run-test", TaskID: "t1", ExpectedRevision: int64Ptr(2), DefinitionRevision: int64Ptr(1)}
	}
	return ev
}

func v2MustMarshal(v any) []byte     { raw, _ := json.Marshal(v); return raw }
func mustJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
