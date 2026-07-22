package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/i18n"
	"workground2/internal/tool"
	"workground2/internal/work"
)

type boundaryTaskExecutor struct{ sessionPath string }

func (e boundaryTaskExecutor) ExecuteTask(_ context.Context, input work.TaskExecuteInput) (*work.Attempt, error) {
	now := time.Now().UTC()
	return &work.Attempt{
		ID: input.AttemptID, Index: input.AttemptIndex, State: work.RunCompleted,
		SessionRef: work.SessionRef{SessionPath: e.sessionPath, BranchID: agent.BranchID(e.sessionPath)},
		StartedAt:  now, FinishedAt: &now,
	}, nil
}

func (boundaryTaskExecutor) CancelTask(context.Context, work.TaskCancelInput) error { return nil }

func TestCornerstoneMemoryBoundary_CompactForgetSessionDeletePreserveWork(t *testing.T) {
	store, err := work.NewFileWorkStore(filepath.Join(t.TempDir(), "works"), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry := work.NewBlueprintRegistry()
	bp := &work.WorkBlueprint{
		SchemaVersion: work.SchemaVersion, ID: "blueprint:boundary", Version: 1, Name: "Boundary",
		Source: work.BlueprintSystem, PromptTemplate: "run", CreatedAt: time.Now().UTC(),
		Workflow:   work.WorkflowDef{Stages: []work.StageSpec{{ID: "main", Title: "Main", Tasks: []work.TaskSpec{{ID: "run", Title: "Run"}}}}},
		BlockSpecs: []work.BlockSpec{{ID: "notes", Kind: "markdown", SchemaVersion: 1, Label: "Notes", Placement: work.BlockPlacement{Slot: "primary"}}},
	}
	if err := registry.Register(bp); err != nil {
		t.Fatal(err)
	}
	svc := work.NewService(store, registry, work.ViewSinkDiscard)
	sessionPath := filepath.Join(t.TempDir(), "source-session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}

	prov := &taskProvider{name: "fake-provider", text: "unused"}
	executor := agent.New(prov, tool.NewRegistry(), agent.NewSession("stable system"), agent.Options{}, event.Discard)
	var notices []string
	ctrl := New(Options{
		Runner: executor, Executor: executor, SessionPath: sessionPath, Work: svc,
		TaskExecutor: boundaryTaskExecutor{sessionPath: sessionPath},
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.Notice {
				notices = append(notices, e.Text)
			}
		}),
	})

	mgr := work.NewCornerstoneManager(store, store, nil)
	type workGolden struct {
		revision     int64
		cornerstones []byte
	}
	goldens := make(map[string]workGolden)
	for i := 1; i <= 2; i++ {
		requestSuffix := fmt.Sprintf("%d", i)
		value, createErr := svc.Create(t.Context(), work.CreateWorkInput{
			BlueprintRef: work.BlueprintRef{ID: bp.ID, SchemaVersion: bp.SchemaVersion, Version: bp.Version},
			Name:         "Boundary test " + requestSuffix, RequestID: "boundary-create-" + requestSuffix,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		view, getErr := svc.Get(t.Context(), value.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if _, pinErr := mgr.Pin(value.ID, work.PinCornerstoneInput{
			Type: work.CornerstoneDecision, Title: "Keep decision " + requestSuffix,
			Content: "this Work-owned decision survives Session and Memory cleanup",
			Ref:     work.CornerstoneRef{Kind: "session_turn", SessionID: sessionPath, Turn: i},
			Mode:    work.CornerstoneSnapshot, Required: true, ExpectedRevision: view.Revision,
			RequestID: "boundary-pin-" + requestSuffix,
		}); pinErr != nil {
			t.Fatal(pinErr)
		}
		// One Work owns this Session through a persisted Task Attempt; the other
		// references it directly as a pinned session_turn source. Both are real
		// associations and must contribute to the aggregate notice.
		if i == 1 {
			if _, runErr := ctrl.WorkControl().RunWork(t.Context(), value.ID, "boundary-run-"+requestSuffix); runErr != nil {
				t.Fatal(runErr)
			}
		}
		before, getErr := svc.Get(t.Context(), value.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		cornerstones, marshalErr := json.Marshal(before.Work.Cornerstones)
		err = marshalErr
		if err != nil {
			t.Fatal(err)
		}
		goldens[value.ID] = workGolden{revision: before.Revision, cornerstones: cornerstones}
	}
	if err := ctrl.Compact(context.Background(), ""); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := ctrl.ForgetMemory("obsolete-memory"); err != nil {
		t.Fatalf("ForgetMemory: %v", err)
	}
	if err := os.Remove(sessionPath); err != nil {
		t.Fatalf("delete source Session: %v", err)
	}

	wantNotice := fmt.Sprintf(i18n.M.CornerstoneCleanupPreserved, 2)
	seen := 0
	for _, notice := range notices {
		if strings.Contains(notice, wantNotice) {
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("preservation notices = %v, want compact and forget notice %q", notices, wantNotice)
	}
	for workID, golden := range goldens {
		after, getErr := svc.Get(t.Context(), workID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		gotCornerstones, marshalErr := json.Marshal(after.Work.Cornerstones)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if after.Revision != golden.revision {
			t.Fatalf("Work %s revision changed across cleanup boundaries: %d -> %d", workID, golden.revision, after.Revision)
		}
		if string(gotCornerstones) != string(golden.cornerstones) {
			t.Fatalf("Cornerstones changed across cleanup boundaries for %s:\nbefore=%s\nafter=%s", workID, golden.cornerstones, gotCornerstones)
		}
	}
}
