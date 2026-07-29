package work

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fullV2ViewGolden = "testdata/contract-v2/work-view-v2-full.json"

func TestPromoteV2ViewIncludesTaskLiveState(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	ref := &SessionRef{
		SessionPath: "sessions/work-live.jsonl",
		BranchID:    "work-live",
		ModelRef:    "test-model",
		StartedAt:   now,
	}
	runtime := V2NewTaskRuntime("work-live", "run-live", "node-live", 1, "read", now)
	runtime.State = TaskRunning
	runtime.Progress = "模型实时输出"
	runtime.SessionRef = ref
	view := promoteV2View(&WorkView{
		Work: &Work{
			SchemaVersion:  SchemaVersionV2,
			ID:             "work-live",
			V2TaskRuntimes: map[string]*V2TaskRuntime{runtime.TaskID: runtime},
		},
	}, &WorkDefinitionRevision{
		WorkID:   "work-live",
		Revision: 1,
		Nodes:    []NodeDef{{ID: "node-live", Title: "Live node"}},
	})
	if len(view.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(view.Tasks))
	}
	task := view.Tasks[0]
	if task.Progress != runtime.Progress || task.SessionRef == nil ||
		task.SessionRef.SessionPath != ref.SessionPath {
		t.Fatalf("live Task view = %+v", task)
	}
	if task.SessionRef == ref {
		t.Fatal("transport projection exposed the mutable runtime SessionRef")
	}
}

func TestWorkViewV2FullGolden_FileWorkStore(t *testing.T) {
	h := newFullV2ViewFixture(t)
	view, err := h.svc.Get(context.Background(), h.work)
	if err != nil {
		t.Fatal(err)
	}
	if view.SchemaVersion != WorkViewSchemaVersionV2 || view.Definition == nil ||
		len(view.ArtifactSlots) == 0 || len(view.Tasks) == 0 ||
		len(view.Inputs) == 0 || len(view.PatchPreviews) == 0 {
		t.Fatalf("incomplete V2 projection: %+v", view)
	}
	actual := normalizedV2Golden(t, view)
	path := filepath.FromSlash(fullV2ViewGolden)
	if os.Getenv("UPDATE_WORK_V2_GOLDEN") == "1" {
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (set UPDATE_WORK_V2_GOLDEN=1 to regenerate)", path, err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("V2 production projection differs from %s; regenerate with UPDATE_WORK_V2_GOLDEN=1", path)
	}
}

func TestV2TransportGetSnapshotDeltaRemovedRestart_FileWorkStore(t *testing.T) {
	h := newFullV2ViewFixture(t)
	sink := &serviceSink{}
	h.svc.sink = sink
	first, err := h.svc.Get(context.Background(), h.work)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.emitSnapshot(first, "transport-snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.emitV2MutationSnapshot(first, first.Revision-1, "transport-snapshot"); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileWorkStore(h.store.workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewService(reopened, nil, sink)
	restarted.SetV2TransportEnabled(true)
	second, err := restarted.Get(context.Background(), h.work)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(normalizedV2Golden(t, first), normalizedV2Golden(t, second)) {
		t.Fatal("restart changed authoritative V2 snapshot")
	}
	if err := restarted.Delete(context.Background(), h.work, "transport-delete"); err != nil {
		t.Fatal(err)
	}
	events := sink.Events()
	if len(events) != 3 {
		t.Fatalf("events=%d, want snapshot/mutation-snapshot/removed: %+v", len(events), events)
	}
	for index, want := range []ViewEventType{ViewSnapshot, ViewSnapshot, ViewRemoved} {
		event := events[index]
		if event.SchemaVersion != WorkViewSchemaVersionV2 || event.Type != want ||
			event.Object.WorkID != h.work {
			t.Fatalf("event[%d]=%+v, want schema2 %s", index, event, want)
		}
		if err := event.Validate(); err != nil {
			t.Fatalf("event[%d] invalid: %v", index, err)
		}
	}
	var snapshot map[string]any
	if err := json.Unmarshal(events[0].Payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"definition", "artifactSlots", "tasks", "inputs", "patchPreviews"} {
		if _, ok := snapshot[field]; !ok {
			t.Fatalf("schema2 snapshot missing top-level %s", field)
		}
	}

	// V2 mutation emits a full WorkView snapshot so the frontend
	// can reconstruct the same authoritative Work projection as GetWork.
	var v2mPayload map[string]any
	if err := json.Unmarshal(events[1].Payload, &v2mPayload); err != nil {
		t.Fatal(err)
	}
	work, ok := v2mPayload["work"].(map[string]any)
	if !ok {
		t.Fatal("V2 mutation snapshot payload missing top-level 'work'")
	}
	if _, ok := work["blocks"]; !ok {
		t.Fatal("V2 mutation snapshot payload work missing 'blocks'")
	}
	if _, ok := work["updatedAt"]; !ok {
		t.Fatal("V2 mutation snapshot payload work missing 'updatedAt'")
	}
	for _, field := range []string{"definition", "artifactSlots", "tasks", "inputs", "patchPreviews"} {
		if _, ok := v2mPayload[field]; !ok {
			t.Fatalf("V2 mutation snapshot payload missing top-level %s", field)
		}
	}
}

func TestV2TransportV1AndFlagOffStaySchema1_FileWorkStore(t *testing.T) {
	v1 := newServiceFixture(t)
	created := mustServiceCreate(t, v1.svc, "v1-transport")
	before := mustServiceView(t, v1.svc, created.ID)
	rawBefore, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	v1.svc.SetV2TransportEnabled(true)
	after := mustServiceView(t, v1.svc, created.ID)
	rawAfter, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawBefore, rawAfter) || after.SchemaVersion != WorkViewSchemaVersion {
		t.Fatalf("V1 bytes changed under V2-capable service:\nbefore=%s\nafter=%s", rawBefore, rawAfter)
	}

	v2 := newFullV2ViewFixture(t)
	v2.svc.SetV2TransportEnabled(false)
	flagOff, err := v2.svc.Get(context.Background(), v2.work)
	if err != nil {
		t.Fatal(err)
	}
	if flagOff.SchemaVersion != WorkViewSchemaVersion || flagOff.Definition != nil ||
		len(flagOff.ArtifactSlots) != 0 || len(flagOff.Tasks) != 0 ||
		len(flagOff.Inputs) != 0 || len(flagOff.PatchPreviews) != 0 {
		t.Fatalf("flag-off leaked schema2 top-level projection: %+v", flagOff)
	}
	if flagOff.Work.V2RevisionStates != nil || flagOff.Work.V2ArtifactSlots != nil ||
		flagOff.Work.V2ArtifactReceipts != nil || flagOff.Work.V2Inputs != nil ||
		flagOff.Work.V2InputReceipts != nil || flagOff.Work.V2PatchPreviews != nil ||
		flagOff.Work.V2PatchReceipts != nil || flagOff.Work.V2TaskRuntimes != nil {
		t.Fatalf("flag-off leaked internal V2 persistence fields: %+v", flagOff.Work)
	}
}

func newFullV2ViewFixture(t *testing.T) *coordinatorHarness {
	t.Helper()
	fixtureTime := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	definition := coordinatorDefinition(
		[]NodeDef{{
			ID: "n1", Title: "Produce report", BlockIDs: []string{"b1"},
			InputSpecIDs: []string{"topic"}, ProducesSlotIDs: []string{"slot"},
		}},
		[]InputSpec{{
			ID: "topic", Label: "Topic", Kind: InputText, Required: false,
			PinEligible: true, ValueSchema: json.RawMessage(`{"type":"string"}`),
		}},
	)
	definition.CreatedAt = fixtureTime
	h := newCoordinatorHarness(t, definition)
	h.svc.SetV2TransportEnabled(true)
	input := requestCoordinatorInput(t, h, "topic")
	if input == nil {
		t.Fatal("input fixture missing")
	}

	now := fixtureTime
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	block := BlockInstance{
		ID: "b1", Kind: "markdown", SchemaVersion: 1, Revision: 1,
		Title: "Report", Status: BlockReady, Data: json.RawMessage(`{"content":"draft"}`),
		Source:    BlockSource{Provider: "fixture", Mode: "snapshot"},
		Fallback:  BlockFallback{Summary: "draft"},
		CreatedAt: now, UpdatedAt: now,
	}
	payload, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	event := newServiceEvent(h.work, "fixture-block", EventBlockUpserted, payload, now)
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	if _, err := h.store.CommitEvent(h.work, event); err != nil {
		t.Fatal(err)
	}

	executor := &coordinatorExecutor{}
	h.svc.SetTaskExecutor(executor)
	if _, err := h.svc.ScheduleV2Run(context.Background(), h.work, h.run, []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	h.svc.SetV2PatchPlanner(coordinatorPatchPlanner{})
	if _, err := h.svc.PreviewV2WorkPatch(context.Background(), PreviewWorkPatchInput{
		WorkID: h.work, RunID: h.run, TaskID: input.TaskID, BlockID: "b1",
		SessionID: "fixture-discussion", Instruction: "rename-block",
		DefinitionRevision: h.def.Revision, BlockRevision: 1,
		Scope: PatchBlock, RequestID: "fixture-preview",
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func normalizedV2Golden(t *testing.T, view *WorkView) []byte {
	t.Helper()
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	normalizeGoldenTimes(value)
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func normalizeGoldenTimes(value any) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			normalizeGoldenTimes(item)
		}
	case map[string]any:
		for key, item := range typed {
			if strings.HasSuffix(key, "At") && item != nil {
				typed[key] = "2026-07-24T08:00:00Z"
				continue
			}
			if text, ok := item.(string); ok {
				switch {
				case strings.HasPrefix(text, "sha256:"):
					typed[key] = "sha256:" + strings.Repeat("0", 64)
					continue
				case strings.HasPrefix(text, "run-"):
					typed[key] = "run-fixture"
					continue
				case strings.Contains(text, ":run-"):
					typed[key] = "task-fixture"
					continue
				}
			}
			normalizeGoldenTimes(item)
		}
	}
}
