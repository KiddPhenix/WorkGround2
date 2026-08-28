package assistant

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRecordSessionTranscriptAppliesProgress(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	transcript := `done.
<assistant-progress>{"plan_revision":1,"responsibility":"scan","responsibilities":[{"alias":"scan","objective":"scan changes","done_criteria":"report written"}],"complete":["scan"]}</assistant-progress>`

	err := store.RecordSessionTranscript(RecordSessionTranscriptInput{
		RequestID: "writeback-1", AssistantID: "helper-a", SessionID: "s1",
		Transcript: transcript, Now: testEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get("helper-a")
	if snapshot.Plan.Revision != 2 || len(snapshot.Plan.Responsibilities) != 1 {
		t.Fatalf("plan = %+v", snapshot.Plan)
	}
	if snapshot.Plan.Responsibilities[0].Status != RespDone {
		t.Fatalf("responsibility status = %s", snapshot.Plan.Responsibilities[0].Status)
	}

	// Replay is idempotent.
	if err := store.RecordSessionTranscript(RecordSessionTranscriptInput{
		RequestID: "writeback-1", AssistantID: "helper-a", SessionID: "s1",
		Transcript: transcript, Now: testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}
}

func TestStoreRecordSessionTranscriptRejectsInvalidDependency(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	transcript := `<assistant-progress>{"responsibilities":[{"alias":"x","objective":"o","depends_on":["missing"]}]}</assistant-progress>`
	err := store.RecordSessionTranscript(RecordSessionTranscriptInput{
		RequestID: "wb", AssistantID: "helper-a", SessionID: "s1",
		Transcript: transcript, Now: testEpoch,
	})
	if err == nil {
		t.Fatal("RecordSessionTranscript accepted a block referencing a missing dependency")
	}
}

func TestStoreRecordSessionTranscriptSurfacesMalformedJSON(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	transcript := `<assistant-progress>{"plan_revision": 1,</assistant-progress>`
	err := store.RecordSessionTranscript(RecordSessionTranscriptInput{
		RequestID: "wb-malformed", AssistantID: "helper-a", SessionID: "s1",
		Transcript: transcript, Now: testEpoch,
	})
	if err == nil {
		t.Fatal("RecordSessionTranscript accepted malformed JSON")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error should mention malformed: %v", err)
	}
}
