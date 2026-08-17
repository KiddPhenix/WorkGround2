package main

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/assistant"
)

func TestAssistantAPICreateAndRunNowAreIdempotent(t *testing.T) {
	service, store := newAssistantTestRuntime(t, &assistantHostStub{})
	app := &App{assistant: service}
	req := AssistantCreateRequest{
		RequestID: "create-api-1",
		Assistant: assistant.Assistant{Name: "Project helper", Mission: "Keep the project healthy"},
		Routines: []assistant.Routine{{
			Title: "Scan changes", Prompt: "Review recent changes", Enabled: true,
		}},
	}
	first, err := app.AssistantCreate(req)
	if err != nil {
		t.Fatalf("AssistantCreate: %v", err)
	}
	second, err := app.AssistantCreate(req)
	if err != nil {
		t.Fatalf("AssistantCreate replay: %v", err)
	}
	if first.Assistant.ID == "" || first.Assistant.ID != second.Assistant.ID || first.Revision != second.Revision {
		t.Fatalf("create replay drifted: first=%+v second=%+v", first, second)
	}
	if len(first.Routines) != 1 || first.Routines[0].ID == "" {
		t.Fatalf("default routine identity missing: %+v", first.Routines)
	}
	if _, err := store.Get(first.Assistant.ID); err != nil {
		t.Fatalf("assistant was not persisted: %v", err)
	}

	runReq := AssistantRunNowRequest{
		AssistantID: first.Assistant.ID, RoutineID: first.Routines[0].ID, RequestID: "run-api-1",
	}
	run1, err := app.AssistantRunNow(runReq)
	if err != nil {
		t.Fatalf("AssistantRunNow: %v", err)
	}
	run2, err := app.AssistantRunNow(runReq)
	if err != nil {
		t.Fatalf("AssistantRunNow replay: %v", err)
	}
	if run1.ID != run2.ID || run1.Revision != run2.Revision {
		t.Fatalf("run replay drifted: first=%+v second=%+v", run1, run2)
	}
}

func TestAssistantListKeepsHealthyItemsAndReportsCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	service, err := NewAssistantRuntime(NewApp(), root)
	if err != nil {
		t.Fatalf("NewAssistantRuntime: %v", err)
	}
	service.host = &assistantHostStub{}
	app := &App{assistant: service}
	created, err := app.AssistantCreate(AssistantCreateRequest{
		RequestID: "list-healthy", Assistant: assistant.Assistant{Name: "Healthy", Mission: "Stay visible"},
	})
	if err != nil {
		t.Fatalf("AssistantCreate: %v", err)
	}
	brokenDir := filepath.Join(root, "broken")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "aggregate.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := app.AssistantList()
	if err != nil {
		t.Fatalf("AssistantList: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != created.Assistant.ID {
		t.Fatalf("healthy items lost: %+v", result.Items)
	}
	if len(result.Diagnostics) == 0 {
		t.Fatal("corrupt aggregate diagnostic was not returned")
	}
}

func TestNewAssistantRuntimeUsesRequestedStoreRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	app := NewApp()
	service, err := NewAssistantRuntime(app, root)
	if err != nil {
		t.Fatalf("NewAssistantRuntime: %v", err)
	}
	service.host = &assistantHostStub{}
	app.assistant = service
	created, err := app.AssistantCreate(AssistantCreateRequest{
		RequestID: "path-create",
		Assistant: assistant.Assistant{Name: "Path", Mission: "Verify storage"},
	})
	if err != nil {
		t.Fatalf("AssistantCreate: %v", err)
	}
	if _, err := service.store.Get(created.Assistant.ID); err != nil {
		t.Fatalf("read from configured store: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(root, created.Assistant.ID, "*.json")); err != nil || len(matches) == 0 {
		t.Fatalf("configured store root has no assistant snapshot: matches=%v err=%v", matches, err)
	}
}
