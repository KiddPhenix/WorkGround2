package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/assistant"
	"workground2/internal/config"
)

func TestAssistantAPICreateAndRunNowAreIdempotent(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
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

func TestAssistantAPIResolveProposalAppliesTypedChange(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	app := &App{assistant: service}
	created, err := app.AssistantCreate(AssistantCreateRequest{
		RequestID: "proposal-api-create",
		Assistant: assistant.Assistant{Name: "Project helper", Mission: "Keep releases healthy"},
		Routines:  []assistant.Routine{{Title: "Release check", Prompt: "Inspect changes", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.AssistantRunNow(AssistantRunNowRequest{AssistantID: created.Assistant.ID, RoutineID: created.Routines[0].ID, RequestID: "proposal-api-run"}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := store.Claim("desktop-test", time.Now(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim: run=%+v ok=%v err=%v", run, ok, err)
	}
	prompt := "Inspect changes, tests, and release notes"
	if _, err := store.CompleteRunWithProgress(assistant.CompleteRunInput{
		RequestID: "proposal-api-complete", RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence,
		Progress: assistant.ProgressBlock{PlanRevision: 1, Proposals: []assistant.ProposalDecl{{
			TargetKind: assistant.ProposalTargetRoutine, TargetID: created.Routines[0].ID,
			Routine: &assistant.RoutineProposalPatch{Prompt: &prompt},
			Summary: "Expand release checks", Reason: "Run evidence found a gap", Evidence: []string{"release notes were missed"},
		}}}, Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get(created.Assistant.ID)
	proposal := snapshot.Proposals[0]
	resolved, err := app.AssistantResolveProposal(AssistantResolveProposalRequest{
		AssistantID: created.Assistant.ID, ProposalID: proposal.ID, RequestID: "proposal-api-accept",
		ExpectedRevision: proposal.Revision, Decision: assistant.ProposalAccept, Resolution: "accepted in desktop test",
	})
	if err != nil || resolved.State != assistant.ProposalApplied {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	after, _ := store.Get(created.Assistant.ID)
	if after.Routines[0].Prompt != prompt || after.Proposals[0].State != assistant.ProposalApplied {
		t.Fatalf("after=%+v", after)
	}
}

func TestAssistantAPIDeleteIsIdempotentAndRemovesFromList(t *testing.T) {
	service, _ := newAssistantTestRuntime(t)
	app := &App{assistant: service}
	created, err := app.AssistantCreate(AssistantCreateRequest{
		RequestID: "delete-create", Assistant: assistant.Assistant{Name: "Disposable", Mission: "Verify deletion"},
		InitialPrompt: "queued work",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := AssistantDeleteRequest{
		AssistantID: created.Assistant.ID, RequestID: "delete-request", ExpectedRevision: created.Revision,
	}
	if err := app.AssistantDelete(req); err != nil {
		t.Fatalf("AssistantDelete: %v", err)
	}
	if err := app.AssistantDelete(req); err != nil {
		t.Fatalf("AssistantDelete replay: %v", err)
	}
	result, err := app.AssistantList()
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("AssistantList after delete = %+v err=%v", result.Items, err)
	}
	if _, err := app.AssistantGet(created.Assistant.ID); !errors.Is(err, assistant.ErrNotFound) {
		t.Fatalf("AssistantGet deleted error = %v, want ErrNotFound", err)
	}
}

func TestAssistantCreateQueuesLearnFirstRun(t *testing.T) {
	service, _ := newAssistantTestRuntime(t)
	app := &App{assistant: service}
	created, err := app.AssistantCreate(AssistantCreateRequest{
		RequestID: "create-learn-first",
		Assistant: assistant.Assistant{
			Name: "Learning helper", Mission: "持续做好项目工作",
			Policy: assistant.Policy{LocalWrite: assistant.AccessAllow, Network: assistant.AccessAllow, Publish: assistant.AccessApprove, Delete: assistant.AccessApprove, Payment: assistant.AccessApprove, Secrets: assistant.AccessApprove, Private: assistant.AccessApprove, ConstraintEdit: assistant.AccessApprove},
		},
		InitialPrompt: "先学习一下再干",
	})
	if err != nil {
		t.Fatalf("AssistantCreate: %v", err)
	}
	if len(created.Runs) != 1 || created.Runs[0].Prompt != "先学习一下再干" || created.Runs[0].State != assistant.RunQueued {
		t.Fatalf("initial learning run = %+v", created.Runs)
	}
}

func TestAssistantSubmitInputRecordsDirectPromptAndIsIdempotent(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	app := &App{assistant: service}
	created, err := app.AssistantCreate(AssistantCreateRequest{
		RequestID: "submit-create", Assistant: assistant.Assistant{Name: "Helper", Mission: "Stay healthy"},
	})
	if err != nil {
		t.Fatalf("AssistantCreate: %v", err)
	}

	req := AssistantSubmitInputRequest{
		AssistantID: created.Assistant.ID, RequestID: "submit-1", Input: "  以后不要静默吞错  ",
	}
	first, err := app.AssistantSubmitInput(req)
	if err != nil {
		t.Fatalf("AssistantSubmitInput: %v", err)
	}
	if first.RoutineID != "" || first.Prompt != "以后不要静默吞错" || first.Trigger != assistant.TriggerManual {
		t.Fatalf("direct input run = %+v, want frozen trimmed prompt", first)
	}

	replay, err := app.AssistantSubmitInput(req)
	if err != nil || replay.ID != first.ID || replay.Revision != first.Revision {
		t.Fatalf("idempotent replay drifted: %+v err=%v", replay, err)
	}

	if _, err := app.AssistantSubmitInput(AssistantSubmitInputRequest{
		AssistantID: created.Assistant.ID, RequestID: "submit-empty", Input: "   \n ",
	}); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty input error = %v, want explicit rejection", err)
	}

	snapshot, err := store.Get(created.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Routines) != 0 {
		t.Fatalf("direct input created routines: %+v", snapshot.Routines)
	}
	if len(snapshot.Runs) != 1 || snapshot.Runs[0].Prompt != "以后不要静默吞错" {
		t.Fatalf("direct input not persisted as a single frozen run: %+v", snapshot.Runs)
	}
}

func TestAssistantListKeepsHealthyItemsAndReportsCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	service, err := NewAssistantRuntime(NewApp(), root)
	if err != nil {
		t.Fatalf("NewAssistantRuntime: %v", err)
	}
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
	if result.Diagnostics[len(result.Diagnostics)-1].Category != assistantDiagnosticData {
		t.Fatalf("corrupt aggregate category = %q, want %q", result.Diagnostics[len(result.Diagnostics)-1].Category, assistantDiagnosticData)
	}
}

func TestAssistantListClassifiesRuntimeDiagnosticsSeparately(t *testing.T) {
	service, _ := newAssistantTestRuntime(t)
	app := &App{assistant: service}
	service.recordDiagnostic("progress_apply", errors.New("invalid transition"))

	result, err := app.AssistantList()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Category != assistantDiagnosticRuntime {
		t.Fatalf("runtime diagnostics = %+v, want one runtime item", result.Diagnostics)
	}
}

func TestNewAssistantRuntimeUsesRequestedStoreRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	app := NewApp()
	service, err := NewAssistantRuntime(app, root)
	if err != nil {
		t.Fatalf("NewAssistantRuntime: %v", err)
	}
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

func TestAssistantPutChannelStoresCredentialOutsideAggregate(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := filepath.Join(t.TempDir(), "assistants")
	service, err := NewAssistantRuntime(NewApp(), root)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{assistant: service}
	created, err := app.AssistantCreate(AssistantCreateRequest{RequestID: "channel-create", Assistant: assistant.Assistant{Name: "Promo", Mission: "promote"}})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := app.AssistantPutChannel(AssistantPutChannelRequest{RequestID: "channel-put", Channel: assistant.ChannelBinding{ID: "channel-discourse", AssistantID: created.Assistant.ID, Name: "Forum", Kind: assistant.ChannelDiscourse, BaseURL: "https://community.example.com", Username: "bot", CredentialKey: "MALICIOUS_SHARED_KEY", CollectIntervalSeconds: 3600, Enabled: true}, APIKey: "secret-api-key"})
	if err != nil {
		t.Fatal(err)
	}
	if channel.CredentialKey == "" || config.ResolveCredential(channel.CredentialKey).Value != "secret-api-key" {
		t.Fatalf("credential key=%q", channel.CredentialKey)
	}
	if channel.CredentialKey == "MALICIOUS_SHARED_KEY" {
		t.Fatal("frontend selected the persisted credential key")
	}
	data, err := os.ReadFile(filepath.Join(root, created.Assistant.ID, "aggregate.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-api-key") {
		t.Fatal("aggregate leaked channel API key")
	}
}

func TestPickAssistantWorkspaceWithoutContextIsACleanCancel(t *testing.T) {
	app := &App{}
	got, err := app.PickAssistantWorkspace("ignored")
	if err != nil {
		t.Fatalf("PickAssistantWorkspace with nil ctx = %v, want no error", err)
	}
	if got != "" {
		t.Fatalf("PickAssistantWorkspace = %q, want empty string", got)
	}
}

func TestCreateAssistantWorkspaceRejectsInvalidInput(t *testing.T) {
	app := &App{}
	parent := t.TempDir()

	cases := []struct {
		name      string
		parentDir string
		dirName   string
	}{
		{"empty parent", "", "child"},
		{"empty name", parent, ""},
		{"dot", parent, "."},
		{"dotdot", parent, ".."},
		{"separator", parent, "a/b"},
		{"windows separator", parent, `a\b`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := app.CreateAssistantWorkspace(tc.parentDir, tc.dirName); err == nil {
				t.Fatalf("CreateAssistantWorkspace(%q, %q) = %q, want error", tc.parentDir, tc.dirName, got)
			}
		})
	}

	if got, err := app.CreateAssistantWorkspace(parent, filepath.Join("..", "escape")); err == nil {
		t.Fatalf("CreateAssistantWorkspace escaped parent: %q, want error", got)
	}
}

func TestCreateAssistantWorkspaceCreatesAndIsIdempotent(t *testing.T) {
	app := &App{}
	parent := filepath.Join(t.TempDir(), "parent")
	want := filepath.Join(parent, "child")

	created, err := app.CreateAssistantWorkspace(parent, "child")
	if err != nil {
		t.Fatalf("CreateAssistantWorkspace: %v", err)
	}
	if created != want {
		t.Fatalf("created = %q, want clean absolute path %q", created, want)
	}
	info, err := os.Stat(created)
	if err != nil || !info.IsDir() {
		t.Fatalf("created path is not a directory: info=%v err=%v", info, err)
	}

	replay, err := app.CreateAssistantWorkspace(parent, "child")
	if err != nil || replay != want {
		t.Fatalf("idempotent replay = %q err=%v, want %q", replay, err, want)
	}

	file := filepath.Join(parent, "occupied")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := app.CreateAssistantWorkspace(parent, "occupied"); err == nil {
		t.Fatalf("CreateAssistantWorkspace over a file = %q, want error", got)
	}
}

func TestAssistantAPISubmitDispatchIdeateFlow(t *testing.T) {
	service, store := newAssistantTestRuntime(t)
	app := &App{assistant: service}
	created, err := app.AssistantCreate(AssistantCreateRequest{
		RequestID: "dispatch-create",
		Assistant: assistant.Assistant{Name: "Project helper", Mission: "Keep releases healthy"},
		Routines:  []assistant.Routine{{Title: "Scan", Prompt: "Inspect changes", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := app.AssistantSubmit(AssistantSubmitRequest{AssistantID: created.Assistant.ID, RequestID: "dispatch-1", Input: "请扫描项目最近修改并跑测试"})
	if err != nil {
		t.Fatalf("AssistantSubmit: %v", err)
	}
	if dispatch.State != assistant.DispatchPendingClassification {
		t.Fatalf("expected pending_classification immediately after submit, got %+v", dispatch)
	}
	replay, err := app.AssistantSubmit(AssistantSubmitRequest{AssistantID: created.Assistant.ID, RequestID: "dispatch-1", Input: "请扫描项目最近修改并跑测试"})
	if err != nil || replay.ID != dispatch.ID {
		t.Fatalf("submit replay drifted: %+v err=%v", replay, err)
	}
	// Classification is background work: advance it explicitly, mirroring the
	// runtime loop that AssistantSubmit wakes.
	pending, _ := store.Get(created.Assistant.ID)
	if err := service.classifyPending(context.Background(), pending); err != nil {
		t.Fatalf("classifyPending: %v", err)
	}
	snapshot, _ := store.Get(created.Assistant.ID)
	if len(snapshot.Dispatches) != 1 || len(snapshot.Jobs) != 0 {
		t.Fatalf("expected one classified dispatch and no frozen jobs, got %d dispatches %d jobs", len(snapshot.Dispatches), len(snapshot.Jobs))
	}
	if snapshot.Dispatches[0].Kind != assistant.DispatchTask {
		t.Fatalf("expected classified task, got %+v", snapshot.Dispatches[0])
	}
	// The supervisor creates a managed Session for the task Dispatch; the stub
	// host cannot run a real Session, so mark the Dispatch executed directly to
	// model the Session's terminal state before reflection.
	executed, err := store.MarkDispatchExecuted(assistant.MarkDispatchExecutedInput{
		RequestID: "executed-dispatch-1", AssistantID: created.Assistant.ID, DispatchID: dispatch.ID, Now: time.Now(),
	})
	if err != nil || executed.State != assistant.DispatchExecuted {
		t.Fatalf("MarkDispatchExecuted: %+v err=%v", executed, err)
	}
	if _, err := service.reflector.Reflect(context.Background(), created.Assistant.ID, dispatch.ID, "reflect-dispatch-1", time.Now()); err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	idea, err := app.AssistantIdeate(AssistantIdeateRequest{AssistantID: created.Assistant.ID, RequestID: "idea-1"})
	if err != nil || idea.State != assistant.IdeaPending {
		t.Fatalf("AssistantIdeate: %+v err=%v", idea, err)
	}
	resolved, err := app.AssistantResolveIdea(AssistantResolveIdeaRequest{AssistantID: created.Assistant.ID, IdeaID: idea.ID, RequestID: "resolve-idea-1", ExpectedRevision: idea.Revision, Decision: assistant.IdeaAccept, Resolution: "ok"})
	if err != nil || resolved.State != assistant.IdeaAccepted {
		t.Fatalf("AssistantResolveIdea: %+v err=%v", resolved, err)
	}
	snapshot, _ = store.Get(created.Assistant.ID)
	if len(snapshot.ContextPacks) != 1 || len(snapshot.Ideas) != 1 {
		t.Fatalf("expected one context pack and one idea, got %d packs %d ideas", len(snapshot.ContextPacks), len(snapshot.Ideas))
	}
}
