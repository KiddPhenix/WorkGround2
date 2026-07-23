package work

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type failOnceCornerstoneBlockedStore struct {
	WorkStore
	requestID string
	failed    atomic.Bool
}

func (s *failOnceCornerstoneBlockedStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	if event.RequestID == s.requestID+"/blocked" && s.failed.CompareAndSwap(false, true) {
		return 0, errors.New("injected cornerstone blocked commit failure")
	}
	return s.WorkStore.CommitEvent(workID, event)
}

func TestCheckRunCornerstones_AllActive(t *testing.T) {
	now := time.Now()
	w := &Work{
		ID: "work-1",
		Cornerstones: []Cornerstone{
			{ID: "cs-1", Type: CornerstoneInstruction, Title: "Test", Status: CornerstoneActive, Mode: CornerstoneSnapshot, PinnedAt: now},
		},
	}
	if blocked := CheckRunCornerstones(w); blocked != nil {
		t.Fatalf("expected no blocking, got %v", blocked)
	}
}

func TestCheckRunCornerstones_RequiredMissing(t *testing.T) {
	now := time.Now()
	w := &Work{
		ID: "work-2",
		Cornerstones: []Cornerstone{
			{ID: "cs-1", Type: CornerstoneInstruction, Title: "Must have", Status: CornerstoneMissing, Required: true, PinnedAt: now},
		},
	}
	blocked := CheckRunCornerstones(w)
	if blocked == nil {
		t.Fatal("expected blocking for required missing cornerstone")
	}
	if !blocked.Assessment.Blocking {
		t.Error("assessment should be blocking")
	}
	var target *ErrRunBlockedByCornerstones
	if !errors.As(blocked, &target) {
		t.Fatalf("expected *ErrRunBlockedByCornerstones, got %T", blocked)
	}
	if target.WorkID != "work-2" {
		t.Errorf("WorkID = %q, want work-2", target.WorkID)
	}
}

func TestCheckRunCornerstones_OptionalMissingNotBlocking(t *testing.T) {
	now := time.Now()
	w := &Work{
		ID: "work-3",
		Cornerstones: []Cornerstone{
			{ID: "cs-1", Type: CornerstoneInstruction, Title: "Optional", Status: CornerstoneMissing, PinnedAt: now},
		},
	}
	if blocked := CheckRunCornerstones(w); blocked != nil {
		t.Fatalf("optional missing should not block, got %v", blocked)
	}
}

func TestCheckRunCornerstones_NilWork(t *testing.T) {
	if blocked := CheckRunCornerstones(nil); blocked != nil {
		t.Fatalf("nil Work should not block, got %v", blocked)
	}
}

func TestErrRunBlockedByCornerstones_Error(t *testing.T) {
	err := &ErrRunBlockedByCornerstones{
		WorkID: "work-x",
		Assessment: CornerstoneAssessment{
			State:    CornerstoneUseBlocked,
			Blocking: true,
			Issues:   []CornerstoneIssue{{CornerstoneID: "cs-1", Problem: "missing", Blocking: true}},
		},
	}
	msg := err.Error()
	if msg == "" {
		t.Fatal("error message should not be empty")
	}
}

func TestRunWorkReResolvesRequiredLiveRefAndBlocksBeforeAttempt(t *testing.T) {
	tests := []struct {
		name       string
		kind       ResolveErrorKind
		wantStatus CornerstoneStatus
	}{
		{name: "missing", kind: ResolveErrorMissing, wantStatus: CornerstoneMissing},
		{name: "denied", kind: ResolveErrorDenied, wantStatus: CornerstoneDenied},
		{name: "network", kind: ResolveErrorNetwork, wantStatus: CornerstoneStale},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunnerFixture(t)
			bp := testBlueprint("blueprint:cornerstone-block-"+tc.name, 1, testWorkflow("main", "run"))
			f.registerBlueprint(t, bp)
			value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: bp.Version}, "create-cornerstone-block-"+tc.name)

			resolver := NewFakeCornerstoneResolver()
			f.svc.SetCornerstoneResolver(resolver)
			mgr := NewCornerstoneManager(f.store, f.store, nil)
			ref := CornerstoneRef{Kind: "workspace_file", Path: "required-" + tc.name + ".txt"}
			resolver.SetContent(ref, "required content")
			view, err := f.svc.Get(t.Context(), value.ID)
			if err != nil {
				t.Fatal(err)
			}
			pinned, err := mgr.Pin(value.ID, PinCornerstoneInput{
				Type: CornerstonePolicy, Title: "Required policy", Content: "required content", Ref: ref,
				Mode: CornerstoneLiveRef, Required: true, ExpectedRevision: view.Revision, RequestID: "pin-required-policy-" + tc.name,
			})
			if err != nil {
				t.Fatalf("Pin: %v", err)
			}
			if pinned.Cornerstone.Status != CornerstoneActive {
				t.Fatalf("initial status = %s, want active", pinned.Cornerstone.Status)
			}
			resolver.SetFault(ref, tc.kind, tc.name, -1)

			var executeCalls atomic.Int32
			f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
				executeCalls.Add(1)
				return nil, errors.New("must not execute")
			}
			requestID := "run-required-" + tc.name
			assertBlocked := func(svc *Service) int64 {
				t.Helper()
				run, err := svc.RunWork(t.Context(), value.ID, requestID)
				if run != nil {
					t.Fatalf("blocked RunWork returned run: %+v", run)
				}
				var blocked *ErrRunBlockedByCornerstones
				if !errors.As(err, &blocked) || !blocked.Assessment.Blocking {
					t.Fatalf("RunWork error = %T %v, want blocking cornerstone error", err, err)
				}
				if got := executeCalls.Load(); got != 0 {
					t.Fatalf("TaskExecutor called %d times for blocked run", got)
				}
				got, getErr := svc.Get(t.Context(), value.ID)
				if getErr != nil {
					t.Fatal(getErr)
				}
				if got.Work.State != WorkWaitingUser {
					t.Fatalf("Work state = %s, want %s", got.Work.State, WorkWaitingUser)
				}
				cs := findCornerstone(got.Work, pinned.Cornerstone.ID)
				if cs == nil || cs.Status != tc.wantStatus {
					t.Fatalf("cornerstone = %+v, want status %s", cs, tc.wantStatus)
				}
				runState := findWorkflowRun(got.Work, workflowRunID(value.ID, requestID))
				if runState == nil || runState.State != RunWaiting {
					t.Fatalf("persisted run = %+v, want waiting", runState)
				}
				if len(runState.Stages) != 0 {
					t.Fatalf("blocked run created Task/RunTurn stages: %+v", runState.Stages)
				}
				return got.Revision
			}

			revision := assertBlocked(f.svc)
			if resolver.CallCount(ref) != 1 {
				t.Fatalf("resolver calls = %d, want 1", resolver.CallCount(ref))
			}
			if replayRevision := assertBlocked(f.svc); replayRevision != revision {
				t.Fatalf("same-process replay revision = %d, want %d", replayRevision, revision)
			}
			restarted := f.restart(t)
			if restartRevision := assertBlocked(restarted); restartRevision != revision {
				t.Fatalf("restart replay revision = %d, want %d", restartRevision, revision)
			}
			if resolver.CallCount(ref) != 1 {
				t.Fatalf("replay resolver calls = %d, want 1", resolver.CallCount(ref))
			}

			resolver.ClearFault(ref)
			restarted.SetCornerstoneResolver(resolver)
			f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
				executeCalls.Add(1)
				now := time.Now()
				return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
			}
			resumed, resumeErr := restarted.ResumeRun(t.Context(), ResumeRunInput{
				WorkID: value.ID, RunID: workflowRunID(value.ID, requestID), RequestID: "resume-required-" + tc.name,
			})
			if resumeErr != nil || resumed == nil || resumed.State != RunCompleted {
				t.Fatalf("ResumeRun after source recovery = %+v, %v", resumed, resumeErr)
			}
			if executeCalls.Load() != 1 || resolver.CallCount(ref) != 2 {
				t.Fatalf("recovered execute/resolver calls = %d/%d, want 1/2", executeCalls.Load(), resolver.CallCount(ref))
			}
		})
	}
}

func TestRunWorkReResolvesOptionalLiveRefAndRunsDegraded(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:cornerstone-optional", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: bp.Version}, "create-cornerstone-optional")
	resolver := NewFakeCornerstoneResolver()
	f.svc.SetCornerstoneResolver(resolver)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "optional.txt"}
	resolver.SetContent(ref, "last-known optional content")
	mgr := NewCornerstoneManager(f.store, f.store, nil)
	view, err := f.svc.Get(t.Context(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := mgr.Pin(value.ID, PinCornerstoneInput{
		Type: CornerstoneSource, Title: "Optional source", Content: "last-known optional content", Ref: ref,
		Mode: CornerstoneLiveRef, ExpectedRevision: view.Revision, RequestID: "pin-optional-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver.SetFault(ref, ResolveErrorMissing, "missing", -1)
	var executeCalls atomic.Int32
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		executeCalls.Add(1)
		now := time.Now()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	run, err := f.svc.RunWork(t.Context(), value.ID, "run-optional-missing")
	if err != nil || run == nil || run.State != RunCompleted {
		t.Fatalf("RunWork = %+v, %v", run, err)
	}
	if executeCalls.Load() != 1 || resolver.CallCount(ref) != 1 {
		t.Fatalf("execute/resolver calls = %d/%d, want 1/1", executeCalls.Load(), resolver.CallCount(ref))
	}
	latest, err := f.svc.Get(t.Context(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	cs := findCornerstone(latest.Work, pinned.Cornerstone.ID)
	if cs == nil || cs.Status != CornerstoneMissing {
		t.Fatalf("optional cornerstone = %+v, want missing", cs)
	}
	block, err := f.svc.BuildCornerstoneContext(t.Context(), value.ID, productionCornerstoneContextConfig())
	if err != nil || !block.Degraded || block.Blocking {
		t.Fatalf("degraded block = %+v, %v", block, err)
	}
	if strings.Contains(block.XML, "last-known optional content") || !strings.Contains(block.XML, `status="missing"`) {
		t.Fatalf("optional degraded XML = %q", block.XML)
	}
}

func TestResumeRunCornerstoneBlockPreservesExistingHistory(t *testing.T) {
	tests := []struct {
		name       string
		kind       ResolveErrorKind
		wantStatus CornerstoneStatus
	}{
		{name: "missing", kind: ResolveErrorMissing, wantStatus: CornerstoneMissing},
		{name: "denied", kind: ResolveErrorDenied, wantStatus: CornerstoneDenied},
		{name: "network", kind: ResolveErrorNetwork, wantStatus: CornerstoneStale},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunnerFixture(t)
			bp := testBlueprint("blueprint:cornerstone-resume-history-"+tc.name, 1, WorkflowDef{Stages: []StageSpec{
				{ID: "completed", Title: "Completed", Tasks: []TaskSpec{{ID: "first", Title: "First"}}},
				{ID: "approval", Title: "Approval", Gate: "approval", Tasks: []TaskSpec{{ID: "second", Title: "Second"}}},
			}})
			f.registerBlueprint(t, bp)
			value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: bp.Version}, "create-resume-history-"+tc.name)

			resolver := NewFakeCornerstoneResolver()
			f.svc.SetCornerstoneResolver(resolver)
			ref := CornerstoneRef{Kind: "workspace_file", Path: "resume-history-" + tc.name + ".txt"}
			resolver.SetContent(ref, "required history content")
			mgr := NewCornerstoneManager(f.store, f.store, nil)
			view, err := f.svc.Get(t.Context(), value.ID)
			if err != nil {
				t.Fatal(err)
			}
			pinned, err := mgr.Pin(value.ID, PinCornerstoneInput{
				Type: CornerstonePolicy, Title: "History policy", Content: "required history content", Ref: ref,
				Mode: CornerstoneLiveRef, Required: true, ExpectedRevision: view.Revision, RequestID: "pin-resume-history-" + tc.name,
			})
			if err != nil {
				t.Fatal(err)
			}

			var executeCalls atomic.Int32
			f.executor.ExecuteFunc = func(_ context.Context, input TaskExecuteInput) (*Attempt, error) {
				call := executeCalls.Add(1)
				now := time.Now().UTC()
				return &Attempt{
					State: RunCompleted,
					SessionRef: SessionRef{
						SessionPath: "sessions/resume-history-" + tc.name + ".jsonl",
						BranchID:    "branch-history-" + tc.name, ModelRef: "provider/model", TurnCount: int(call),
						Preview: "persisted history", StartedAt: now,
					},
					Receipt: &AttemptReceipt{
						RequestID: input.RequestID, Outcome: "accept", Evidence: "persisted receipt",
						SideEffectClass: input.SideEffectClass, ConfirmedAt: now,
					},
					FinishedAt: &now,
				}, nil
			}

			run, err := f.svc.RunWork(t.Context(), value.ID, "run-resume-history-"+tc.name)
			if err != nil || run == nil || run.State != RunWaiting {
				t.Fatalf("RunWork = %+v, %v, want gate waiting", run, err)
			}
			if executeCalls.Load() != 1 || resolver.CallCount(ref) != 1 {
				t.Fatalf("initial execute/resolver calls = %d/%d, want 1/1", executeCalls.Load(), resolver.CallCount(ref))
			}
			if len(run.Stages) != 2 || len(run.Stages[0].Tasks) != 1 || len(run.Stages[0].Tasks[0].Attempts) != 1 {
				t.Fatalf("initial run history = %+v", run.Stages)
			}
			firstAttempt := run.Stages[0].Tasks[0].Attempts[0]
			if firstAttempt.SessionRef.SessionPath == "" || firstAttempt.Receipt == nil {
				t.Fatalf("initial attempt lost SessionRef/receipt: %+v", firstAttempt)
			}
			beforeJSON, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			firstStageJSON, err := json.Marshal(run.Stages[0])
			if err != nil {
				t.Fatal(err)
			}

			resolver.SetFault(ref, tc.kind, tc.name, -1)
			blockedInput := ResumeRunInput{
				WorkID: value.ID, RunID: run.ID, RequestID: "resume-history-blocked-" + tc.name,
				GateResolutions: map[string]GateResolution{"approval": {StageID: "approval", Outcome: "approved"}},
			}
			faultStore := &failOnceCornerstoneBlockedStore{WorkStore: f.store, requestID: blockedInput.RequestID}
			faultSvc := NewService(faultStore, f.registry, f.sink)
			faultSvc.SetTaskExecutor(f.executor)
			faultSvc.SetCornerstoneResolver(resolver)
			f.svc = faultSvc
			const callers = 6
			errs := make([]error, callers)
			var wg sync.WaitGroup
			for i := range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, errs[i] = f.svc.ResumeRun(t.Context(), blockedInput)
				}()
			}
			wg.Wait()
			for i, gotErr := range errs {
				var blocked *ErrRunBlockedByCornerstones
				if !errors.As(gotErr, &blocked) || !blocked.Assessment.Blocking {
					t.Fatalf("blocked ResumeRun[%d] error = %T %v", i, gotErr, gotErr)
				}
			}
			if executeCalls.Load() != 1 || resolver.CallCount(ref) != 2 {
				t.Fatalf("blocked execute/resolver calls = %d/%d, want 1/2", executeCalls.Load(), resolver.CallCount(ref))
			}
			if !faultStore.failed.Load() {
				t.Fatal("partial-commit failure was not injected")
			}
			after, err := f.svc.Get(t.Context(), value.ID)
			if err != nil {
				t.Fatal(err)
			}
			blockedRun := findWorkflowRun(after.Work, run.ID)
			if blockedRun == nil || blockedRun.State != RunWaiting || after.Work.State != WorkWaitingUser {
				t.Fatalf("blocked Work/Run = %s/%+v", after.Work.State, blockedRun)
			}
			blockedJSON, err := json.Marshal(blockedRun)
			if err != nil {
				t.Fatal(err)
			}
			if string(blockedJSON) != string(beforeJSON) {
				t.Fatalf("Resume preflight changed persisted Run history:\nbefore=%s\nafter=%s", beforeJSON, blockedJSON)
			}
			cs := findCornerstone(after.Work, pinned.Cornerstone.ID)
			if cs == nil || cs.Status != tc.wantStatus {
				t.Fatalf("cornerstone = %+v, want %s", cs, tc.wantStatus)
			}
			blockedRevision := after.Revision

			restarted := f.restart(t)
			restarted.SetCornerstoneResolver(resolver)
			if _, replayErr := restarted.ResumeRun(t.Context(), blockedInput); replayErr == nil {
				t.Fatal("restart replay unexpectedly resumed blocked Run")
			}
			replayed, err := restarted.Get(t.Context(), value.ID)
			if err != nil {
				t.Fatal(err)
			}
			if replayed.Revision != blockedRevision || executeCalls.Load() != 1 || resolver.CallCount(ref) != 2 {
				t.Fatalf("blocked replay revision/execute/resolve = %d/%d/%d, want %d/1/2",
					replayed.Revision, executeCalls.Load(), resolver.CallCount(ref), blockedRevision)
			}

			resolver.ClearFault(ref)
			resumed, resumeErr := restarted.ResumeRun(t.Context(), ResumeRunInput{
				WorkID: value.ID, RunID: run.ID, RequestID: "resume-history-recovered-" + tc.name,
				GateResolutions: map[string]GateResolution{"approval": {StageID: "approval", Outcome: "approved"}},
			})
			if resumeErr != nil || resumed == nil || resumed.State != RunCompleted {
				t.Fatalf("recovered ResumeRun = %+v, %v", resumed, resumeErr)
			}
			if executeCalls.Load() != 2 || resolver.CallCount(ref) != 3 {
				t.Fatalf("recovered execute/resolver calls = %d/%d, want 2/3", executeCalls.Load(), resolver.CallCount(ref))
			}
			finalFirstStageJSON, err := json.Marshal(resumed.Stages[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(finalFirstStageJSON) != string(firstStageJSON) {
				t.Fatalf("recovery changed completed history:\nbefore=%s\nafter=%s", firstStageJSON, finalFirstStageJSON)
			}
			if len(resumed.Stages) != 2 || len(resumed.Stages[1].Tasks) != 1 || len(resumed.Stages[1].Tasks[0].Attempts) != 1 {
				t.Fatalf("recovered run shape = %+v", resumed.Stages)
			}
			if _, oldErr := restarted.ResumeRun(t.Context(), blockedInput); oldErr == nil {
				t.Fatal("old blocked request unexpectedly replayed after completion")
			}
			if executeCalls.Load() != 2 || resolver.CallCount(ref) != 3 {
				t.Fatalf("old replay repeated execute/resolve = %d/%d", executeCalls.Load(), resolver.CallCount(ref))
			}
		})
	}
}
