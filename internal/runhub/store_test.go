package runhub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRun(id RunID, rev uint64) AgentRun {
	now := time.Unix(1_700_000_000, 0)
	return AgentRun{
		ID: id, Source: SourceDSH, Ownership: OwnershipManaged,
		State: StateRunning, Revision: rev,
		LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	run := testRun("run_abc", 7)
	if err := s.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	got, ok, err := s.LoadRun(run.ID)
	if err != nil || !ok {
		t.Fatalf("LoadRun: ok=%v err=%v", ok, err)
	}
	if got.ID != run.ID || got.Revision != run.Revision || got.State != run.State {
		t.Fatalf("run round-trip mismatch: %+v", got)
	}

	lr := LaunchReceipt{RequestID: "req-1", RunID: run.ID, Status: ReceiptAccepted, CreatedAt: run.CreatedAt}
	if err := s.SaveLaunchReceipt("req-1", lr); err != nil {
		t.Fatalf("SaveLaunchReceipt: %v", err)
	}
	gotLR, ok, err := s.LoadLaunchReceipt("req-1")
	if err != nil || !ok || gotLR.RunID != run.ID {
		t.Fatalf("launch receipt round-trip: ok=%v err=%v %+v", ok, err, gotLR)
	}

	evt := RunEvent{EventID: "evt-1", RunID: run.ID, Type: EventRunning, OccurredAt: run.CreatedAt}
	if err := s.AppendEvent(evt); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	er := EventReceipt{EventID: "evt-1", RunID: run.ID, Status: ReceiptAccepted, Revision: 8, Event: evt}
	if err := s.SaveEventReceipt("evt-1", er); err != nil {
		t.Fatalf("SaveEventReceipt: %v", err)
	}
	gotER, ok, err := s.LoadEventReceipt("evt-1")
	if err != nil || !ok || gotER.Status != ReceiptAccepted {
		t.Fatalf("event receipt round-trip: ok=%v err=%v %+v", ok, err, gotER)
	}

	runs, err := s.ListRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: n=%d err=%v", len(runs), err)
	}
}

func TestStoreBindingRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := RunID("run_bind")
	if err := s.SaveRun(testRun(id, 1)); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	rec := BindingRecord{
		RunID:   id,
		Binding: RunnerBinding{RunID: id, NativeSessionID: "sess-1", ProtocolVersion: "2.0", ProcessRef: "123", Attempt: 1},
		State:   StateRunning,
		SavedAt: time.Now(),
	}
	if err := s.SaveBinding(rec); err != nil {
		t.Fatalf("SaveBinding: %v", err)
	}
	got, ok, err := s.LoadBinding(id)
	if err != nil || !ok {
		t.Fatalf("LoadBinding: ok=%v err=%v", ok, err)
	}
	if got.Binding.NativeSessionID != "sess-1" || got.State != StateRunning {
		t.Fatalf("binding round-trip mismatch: %+v", got)
	}

	list, err := s.ListBindings()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListBindings: n=%d err=%v", len(list), err)
	}
}

func TestLoadBindingRejectsRunIDMismatch(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SaveBinding(BindingRecord{
		RunID:   "run_a",
		Binding: RunnerBinding{RunID: "run_b"},
		State:   StateRunning,
		SavedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadBinding("run_a"); err == nil || ok {
		t.Fatalf("LoadBinding mismatch: ok=%v err=%v, want error", ok, err)
	}
}

func TestStoreCorruptMetaFailsExplicitly(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	badDir := filepath.Join(root, "runs", "run_bad")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "meta.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A sibling valid run must not mask the corruption.
	if err := s.SaveRun(testRun("run_good", 1)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.LoadRun("run_bad"); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("LoadRun corrupt: got %v, want corrupt error", err)
	}
	if _, err := s.ListRuns(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("ListRuns corrupt: got %v, want corrupt error", err)
	}
}

func TestStoreCorruptReceiptFailsExplicitly(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "launches", receiptFileName("req-1")), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListLaunchReceipts(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("ListLaunchReceipts corrupt: got %v, want corrupt error", err)
	}
}

func TestReceiptFilenameIsHashOfOpaqueID(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SaveLaunchReceipt("req:1", LaunchReceipt{RequestID: "req:1", RunID: "run_1", Status: ReceiptAccepted, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "launches"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) != 1 || names[0] != receiptFileName("req:1") {
		t.Fatalf("launch receipt filenames = %v, want [%s]", names, receiptFileName("req:1"))
	}
	if names[0] == "req:1.json" {
		t.Fatalf("raw opaque id leaked into filename")
	}
}

func TestListLaunchReceiptsRejectsFilenameRecordMismatch(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec := LaunchReceipt{RequestID: "good", RunID: "run_1", Status: ReceiptAccepted, CreatedAt: time.Now()}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	// Record id "good" stored under the hashed filename of a different id.
	if err := os.WriteFile(filepath.Join(root, "launches", receiptFileName("evil")), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListLaunchReceipts(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ListLaunchReceipts mismatch: got %v, want mismatch error", err)
	}
}

func TestListEventReceiptsRejectsFilenameRecordMismatch(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec := EventReceipt{EventID: "good", RunID: "run_1", Status: ReceiptAccepted, Revision: 1}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inbox", receiptFileName("evil")), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListEventReceipts(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ListEventReceipts mismatch: got %v, want mismatch error", err)
	}
}

func TestLoadReceiptRejectsRecordIDMismatch(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Write a receipt whose inner record id differs from the requested path id.
	if err := s.SaveLaunchReceipt("want", LaunchReceipt{RequestID: "other", RunID: "run_1", Status: ReceiptAccepted, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadLaunchReceipt("want"); err == nil || ok {
		t.Fatalf("LoadLaunchReceipt mismatch: ok=%v err=%v, want error", ok, err)
	}
}

func TestListRunsRejectsMetaDirMismatch(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dir := filepath.Join(root, "runs", "run_real")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(testRun("run_other", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListRuns(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ListRuns meta/dir mismatch: got %v, want mismatch error", err)
	}
}

func TestReloadRejectsInvalidRunContent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	run := testRun("run_1", 1)
	run.State = RunState("bogus")
	if err := s.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil || !strings.Contains(err.Error(), "invalid state") {
		t.Fatalf("reload on invalid state: got %v, want invalid state error", err)
	}
}

func TestListEventsRejectsCorruptStableLine(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	id := RunID("run_1")
	if err := os.MkdirAll(s.runDir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.runEventsPath(id), []byte("{broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListEvents(id); err == nil || !strings.Contains(err.Error(), "corrupt event log") {
		t.Fatalf("ListEvents corrupt line: %v", err)
	}
}

func TestListEventsRejectsDuplicateID(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	evt := RunEvent{EventID: "evt:1", RunID: "run_1", Source: SourceDSH, Type: EventRunning}
	if err := s.AppendEvent(evt); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(evt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListEvents(evt.RunID); err == nil || !strings.Contains(err.Error(), "duplicate event") {
		t.Fatalf("ListEvents duplicate: %v", err)
	}
}

func TestValidateBindingRecord(t *testing.T) {
	valid := BindingRecord{
		RunID:   "run_x",
		Binding: RunnerBinding{RunID: "run_x", NativeSessionID: "sess-1", ProtocolVersion: "2.0", ProcessRef: "pid:1", Attempt: 1},
		State:   StateRunning,
		SavedAt: time.Now(),
	}
	if err := validateBindingRecord(valid); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*BindingRecord)
	}{
		{"unsafe run id", func(r *BindingRecord) { r.RunID = "../bad" }},
		{"diverging binding id", func(r *BindingRecord) { r.Binding.RunID = "run_y" }},
		{"bad state", func(r *BindingRecord) { r.State = RunState("bogus") }},
		{"zero attempt", func(r *BindingRecord) { r.Binding.Attempt = 0 }},
		{"zero savedAt", func(r *BindingRecord) { r.SavedAt = time.Time{} }},
		{"empty session", func(r *BindingRecord) { r.Binding.NativeSessionID = "" }},
		{"empty process ref", func(r *BindingRecord) { r.Binding.ProcessRef = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := valid
			tc.mutate(&rec)
			if err := validateBindingRecord(rec); err == nil {
				t.Fatalf("validateBindingRecord(%+v) = nil, want error", rec)
			}
		})
	}
}

func TestListBindingsRejectsOrphanBinding(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := RunID("run_x")
	if err := s.SaveBinding(BindingRecord{
		RunID:   id,
		Binding: RunnerBinding{RunID: id, NativeSessionID: "sess-1", ProtocolVersion: "2.0", ProcessRef: "pid:1", Attempt: 1},
		State:   StateRunning,
		SavedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListBindings(); err == nil || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("ListBindings orphan: got %v, want orphan error", err)
	}
}

func TestListBindingsRejectsIllegalRunDir(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "runs", "bad!"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListBindings(); err == nil || !strings.Contains(err.Error(), "illegal run directory") {
		t.Fatalf("ListBindings illegal dir: got %v, want illegal error", err)
	}
}
