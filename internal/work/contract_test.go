package work

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var sharedFixtureNames = []string{
	"work-view-event-v1.json",
	"work-view-event-resync-v1.json",
	"work-view-event-future.json",
	"work-dto-fields-v1.json",
}

const (
	goFixtureDir = "testdata/archive-v1"
	tsFixtureDir = "../../desktop/frontend/src/work/__fixtures__"
)

func readFixture(t *testing.T, dir, name string) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGoTSFixturesEquivalent(t *testing.T) {
	for _, name := range sharedFixtureNames {
		goData := readFixture(t, goFixtureDir, name)
		tsData := readFixture(t, tsFixtureDir, name)
		if !bytes.Equal(goData, tsData) {
			t.Errorf("Go and TypeScript fixture %q differ", name)
		}
	}
}

func TestGoDTOFieldsMatchContract(t *testing.T) {
	var want map[string][]string
	if err := json.Unmarshal(readFixture(t, goFixtureDir, "work-dto-fields-v1.json"), &want); err != nil {
		t.Fatal(err)
	}

	tests := map[string]any{
		"TaskExecuteInput":   TaskExecuteInput{},
		"RetryTaskInput":     RetryTaskInput{},
		"ResumeRunInput":     ResumeRunInput{},
		"GateResolution":     GateResolution{},
		"WorkView":           WorkView{},
		"RunBlockReason":     RunBlockReason{},
		"RunBlockItem":       RunBlockItem{},
		"WorkViewEvent":      WorkViewEvent{},
		"ViewResync":         ViewResync{},
		"ViewRecoveryIntent": ViewRecoveryIntent{},
	}
	if len(want) != len(tests) {
		t.Fatalf("DTO contract count = %d, want %d", len(want), len(tests))
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			got := jsonFields(reflect.TypeOf(value))
			fields, ok := want[name]
			if !ok {
				t.Fatalf("DTO contract is missing %s", name)
			}
			if !reflect.DeepEqual(got, fields) {
				t.Fatalf("%s JSON fields = %v, want %v", name, got, fields)
			}
		})
	}
}

func jsonFields(typ reflect.Type) []string {
	fields := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Tag.Get("json")
		if comma := bytes.IndexByte([]byte(name), ','); comma >= 0 {
			name = name[:comma]
		}
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	return fields
}

func TestParseWorkViewEventV1(t *testing.T) {
	result, err := ParseWorkViewEvent(readFixture(t, goFixtureDir, "work-view-event-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.FutureError != nil {
		t.Fatalf("unexpected parse result: %+v", result)
	}
	event := result.Event
	if event.SchemaVersion != WorkViewSchemaVersion || event.Type != ViewSnapshot ||
		event.WorkID != "work-abc123" || event.EventID != "evt-snapshot-001" ||
		event.Revision != 5 || event.BaseRevision != 0 ||
		event.RequestID != "req-20260720-001" || event.Object.Kind != ObjectWork ||
		event.Object.ID != event.WorkID {
		t.Fatalf("fixture fields drifted: %+v", event)
	}
	if err := result.RejectWrite(); err != nil {
		t.Fatal(err)
	}
}

func TestParseWorkViewResyncV1(t *testing.T) {
	result, err := ParseWorkViewEvent(readFixture(t, goFixtureDir, "work-view-event-resync-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	event := result.Event
	if event == nil || event.Resync == nil || event.Resync.Reason != ViewResyncOverflow ||
		!event.Resync.Authoritative || event.Resync.Generation != 3 {
		t.Fatalf("resync fixture fields drifted: %+v", event)
	}
}

func TestParseWorkViewEventFuture(t *testing.T) {
	raw := readFixture(t, goFixtureDir, "work-view-event-future.json")
	result, err := ParseWorkViewEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Event != nil || result.FutureError == nil || !bytes.Equal(result.Raw, raw) {
		t.Fatalf("future schema must be raw and read-only: %+v", result)
	}
	var future *ViewFutureSchemaError
	if !errors.As(result.RejectWrite(), &future) {
		t.Fatalf("RejectWrite error = %T", result.RejectWrite())
	}
	if future.Got != 999 || future.Current != WorkViewSchemaVersion || future.EventID != "evt-future-001" {
		t.Fatalf("future error fields drifted: %+v", future)
	}
}

func TestWorkViewEventValidation(t *testing.T) {
	valid := WorkViewEvent{
		SchemaVersion: WorkViewSchemaVersion,
		Type:          ViewDelta,
		WorkID:        "work-1",
		EventID:       "event-2",
		Revision:      2,
		BaseRevision:  1,
		RequestID:     "request-1",
		Object:        ObjectContext{Kind: ObjectBlock, ID: "block-1", ParentID: "work-1"},
		CreatedAt:     mustTime(t, "2026-07-20T10:00:00Z"),
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ParseWorkViewEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DeltaAppliesTo(1) || result.DeltaAppliesTo(0) {
		t.Fatal("delta gap detection is incorrect")
	}

	tests := []struct {
		name string
		edit func(*WorkViewEvent)
	}{
		{"missing request", func(e *WorkViewEvent) { e.RequestID = "" }},
		{"missing object", func(e *WorkViewEvent) { e.Object = ObjectContext{} }},
		{"unknown type", func(e *WorkViewEvent) { e.Type = "future" }},
		{"broken revision", func(e *WorkViewEvent) { e.BaseRevision = e.Revision }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := valid
			test.edit(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("Validate succeeded")
			}
		})
	}
}

func TestWorkViewOverflowResyncValidation(t *testing.T) {
	event := WorkViewEvent{
		SchemaVersion: WorkViewSchemaVersion,
		Type:          ViewSnapshot,
		WorkID:        "work-1",
		EventID:       OverflowResyncEventID("work-1", 7, 3),
		Revision:      7,
		RequestID:     "overflow-recovery",
		Object:        ObjectContext{Kind: ObjectWork, ID: "work-1"},
		Resync:        &ViewResync{Reason: ViewResyncOverflow, Authoritative: true, Generation: 3},
		Payload:       json.RawMessage(`{"revision":7}`),
		CreatedAt:     mustTime(t, "2026-07-20T10:00:00Z"),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("valid overflow resync: %v", err)
	}
	retry := event
	retry.Resync = &ViewResync{Reason: ViewResyncRetry, Authoritative: true, Generation: 4}
	retry.EventID = ResyncEventID(retry.WorkID, retry.Revision, ViewResyncRetry, 4)
	if err := retry.Validate(); err != nil {
		t.Fatalf("valid retry resync: %v", err)
	}
	tests := []struct {
		name string
		edit func(*WorkViewEvent)
	}{
		{"delta", func(e *WorkViewEvent) { e.Type = ViewDelta; e.BaseRevision = 6 }},
		{"other reason", func(e *WorkViewEvent) { e.Resync.Reason = "manual" }},
		{"not authoritative", func(e *WorkViewEvent) { e.Resync.Authoritative = false }},
		{"zero generation", func(e *WorkViewEvent) { e.Resync.Generation = 0 }},
		{"unsafe generation", func(e *WorkViewEvent) { e.Resync.Generation = 1 << 53 }},
		{"wrong event ID", func(e *WorkViewEvent) { e.EventID = "resync-spoofed" }},
		{"cross Work", func(e *WorkViewEvent) { e.Object.ID = "work-2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := event
			resync := *event.Resync
			candidate.Resync = &resync
			test.edit(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate succeeded")
			}
		})
	}
}

func TestWorkEventJSONBoundary(t *testing.T) {
	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "event-1",
		RequestID:     "request-1",
		WorkID:        "work-1",
		Type:          EventWorkCreated,
		Revision:      1,
		BaseRevision:  0,
		Payload:       json.RawMessage(`{"name":"test"}`),
		ContentDigest: "sha256:abc",
		WriterID:      "writer-1",
		CreatedAt:     mustTime(t, "2026-07-20T10:00:00Z"),
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"schemaVersion", "id", "requestId", "workId", "type", "revision", "baseRevision", "payload", "contentDigest", "writerId", "createdAt"} {
		if !bytes.Contains(raw, []byte(`"`+field+`"`)) {
			t.Errorf("persisted event is missing %q", field)
		}
	}
}

func mustTime(t *testing.T, value string) (result time.Time) {
	t.Helper()
	result, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
