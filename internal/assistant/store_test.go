package assistant

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testEpoch = time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)

func TestStoreCreateIsDurablyIdempotentAndRejectsFingerprintConflict(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	in := testCreateInput("helper-a", "create-1")
	first, err := store.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	reopened := testStore(t, root)
	second, err := reopened.Create(in)
	if err != nil {
		t.Fatalf("idempotent retry after reopen: %v", err)
	}
	if first.Assistant.ID != second.Assistant.ID || first.Revision != second.Revision {
		t.Fatalf("retry result changed: first=%+v second=%+v", first, second)
	}
	in.Assistant.Mission = "different mission"
	if _, err := reopened.Create(in); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("fingerprint conflict error = %v, want ErrIdempotency", err)
	}
	got, err := reopened.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Assistant.Mission != "keep the project healthy" || len(got.Receipts) != 1 {
		t.Fatalf("conflicting retry mutated aggregate: %+v", got)
	}
}

func TestStoreUpdateAssistantCASAndIdempotentRetry(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	created := mustCreate(t, store, "helper-a")
	desired := created.Assistant
	desired.Name = "Release helper"
	updated, err := store.UpdateAssistant("update-1", desired, created.Assistant.Revision, testEpoch.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Name != "Release helper" {
		t.Fatalf("updated = %+v", updated)
	}
	retry, err := store.UpdateAssistant("update-1", desired, created.Assistant.Revision, testEpoch.Add(2*time.Minute))
	if err != nil || retry.Revision != updated.Revision || !retry.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("idempotent update retry = %+v, err=%v", retry, err)
	}
	desired.Name = "stale overwrite"
	if _, err := store.UpdateAssistant("update-2", desired, 1, testEpoch.Add(3*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want ErrConflict", err)
	}
}

func TestStoreApplyMemoryCASAndLockedItems(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	created := mustCreate(t, store, "helper-a")
	memory, err := store.ApplyMemory("helper-a", "memory-1", created.Memory.Revision, MemoryPatch{Upsert: []MemoryItem{{
		ID: "charter-1", Kind: MemoryCharter, Body: "Never publish without approval", Locked: true,
	}}}, testEpoch.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if memory.Revision != 2 || len(memory.Items) != 1 || !memory.Items[0].Locked {
		t.Fatalf("memory = %+v", memory)
	}
	if _, err := store.ApplyMemory("helper-a", "memory-stale", 1, MemoryPatch{}, testEpoch.Add(2*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale memory error = %v, want ErrConflict", err)
	}
	if _, err := store.ApplyMemory("helper-a", "memory-delete", memory.Revision, MemoryPatch{Delete: []string{"charter-1"}}, testEpoch.Add(3*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete locked memory error = %v, want ErrConflict", err)
	}
	if _, err := store.ApplyMemory("helper-a", "memory-replace", memory.Revision, MemoryPatch{Upsert: []MemoryItem{{ID: "charter-1", Kind: MemoryFact, Body: "overwrite"}}}, testEpoch.Add(4*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("replace locked memory error = %v, want ErrConflict", err)
	}
}

func TestStoreOccurrenceIsUniqueAndCoalescesLatest(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	firstAt := testEpoch.Add(time.Hour)
	first, err := store.CreateOccurrence(TriggerInput{
		AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "occ-1",
		ScheduledFor: firstAt, Now: testEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.CreateOccurrence(TriggerInput{
		AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "occ-duplicate",
		ScheduledFor: firstAt, Now: testEpoch.Add(time.Minute),
	})
	if err != nil || duplicate.ID != first.ID {
		t.Fatalf("duplicate occurrence = %+v err=%v, want run %s", duplicate, err, first.ID)
	}
	latestAt := firstAt.Add(time.Hour)
	latest, err := store.CreateOccurrence(TriggerInput{
		AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "occ-2",
		ScheduledFor: latestAt, Now: testEpoch.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != first.ID || !latest.ScheduledFor.Equal(latestAt) || len(latest.Occurrences) != 2 {
		t.Fatalf("coalesced run = %+v", latest)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Runs) != 1 {
		t.Fatalf("runs = %d, want one coalesced run", len(snapshot.Runs))
	}
}

func TestStoreClaimIsSingleFlightUnderHundredConcurrentWorkers(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	if _, err := store.Trigger(TriggerInput{AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "manual-1", Trigger: TriggerManual, Now: testEpoch}); err != nil {
		t.Fatal(err)
	}
	const workers = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	var claimed atomic.Int32
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, err := store.Claim("worker", testEpoch.Add(time.Second), time.Minute)
			if err != nil {
				errs <- err
				return
			}
			if ok {
				claimed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Claim: %v", err)
	}
	if got := claimed.Load(); got != 1 {
		t.Fatalf("successful claims = %d, want 1", got)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Runs) != 1 || snapshot.Runs[0].State != RunRunning || snapshot.Runs[0].LeaseFence != 1 {
		t.Fatalf("claimed state = %+v", snapshot.Runs)
	}
}

func TestStoreRejectsStaleLeaseFence(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run, ok, err := store.Claim("worker-a", testEpoch.Add(time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim = %+v ok=%v err=%v", run, ok, err)
	}
	if _, err := store.Renew(run.ID, "worker-a", run.LeaseFence-1, testEpoch.Add(2*time.Second), time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Renew error = %v, want ErrLeaseLost", err)
	}
	if _, err := store.Finish(FinishInput{RequestID: "finish-wrong-owner", RunID: run.ID, LeaseOwner: "other", LeaseFence: run.LeaseFence, Now: testEpoch.Add(2 * time.Second)}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("wrong-owner Finish error = %v, want ErrLeaseLost", err)
	}
	snapshot, _ := store.Get("helper-a")
	if snapshot.Runs[0].State != RunRunning {
		t.Fatalf("stale writer changed state to %s", snapshot.Runs[0].State)
	}
}

func TestStoreRecoverUnknownOutcomeWaitsForAttention(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim = %+v ok=%v err=%v", run, ok, err)
	}
	recovered, err := store.Recover(testEpoch.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].State != RunWaitingAttention || recovered[0].Error == nil || recovered[0].Error.Code != "outcome_unknown" {
		t.Fatalf("recovered = %+v", recovered)
	}
	if _, ok, err := store.Claim("worker-b", testEpoch.Add(2*time.Minute), time.Minute); err != nil || ok {
		t.Fatalf("unknown outcome was automatically claimable: ok=%v err=%v", ok, err)
	}
}

func TestStoreKnownFailureRetriesOnlyWhenDue(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	failed, err := store.Fail(FailInput{
		RequestID: "fail-network-1",
		RunID:     run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Failure: Failure{Code: "network", Message: "temporary", Retryable: true, OutcomeKnown: true, RetryAfter: time.Minute, Now: testEpoch.Add(time.Second)},
	})
	if err != nil || failed.State != RunRetryWait {
		t.Fatalf("Fail = %+v err=%v", failed, err)
	}
	if due, err := store.RetryDue(testEpoch.Add(30 * time.Second)); err != nil || len(due) != 0 {
		t.Fatalf("early RetryDue = %+v err=%v", due, err)
	}
	due, err := store.RetryDue(testEpoch.Add(2 * time.Minute))
	if err != nil || len(due) != 1 || due[0].State != RunQueued || due[0].Trigger != TriggerManual {
		t.Fatalf("due RetryDue = %+v err=%v", due, err)
	}
}

func TestStoreFinishAndFailAreIdempotent(t *testing.T) {
	t.Run("finish", func(t *testing.T) {
		store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
		mustCreate(t, store, "helper-a")
		mustTrigger(t, store, "manual-1")
		run, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
		if err != nil || !ok {
			t.Fatalf("Claim: ok=%v err=%v", ok, err)
		}
		in := FinishInput{RequestID: "finish-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence, Summary: "done", Now: testEpoch.Add(time.Second)}
		first, err := store.Finish(in)
		if err != nil {
			t.Fatal(err)
		}
		in.Now = testEpoch.Add(2 * time.Second)
		replay, err := store.Finish(in)
		if err != nil || replay.Revision != first.Revision || !replay.FinishedAt.Equal(first.FinishedAt) {
			t.Fatalf("Finish replay=%+v err=%v", replay, err)
		}
		in.Summary = "different"
		if _, err := store.Finish(in); !errors.Is(err, ErrIdempotency) {
			t.Fatalf("Finish fingerprint conflict=%v", err)
		}
	})

	t.Run("fail", func(t *testing.T) {
		store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
		mustCreate(t, store, "helper-a")
		mustTrigger(t, store, "manual-1")
		run, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
		if err != nil || !ok {
			t.Fatalf("Claim: ok=%v err=%v", ok, err)
		}
		in := FailInput{
			RequestID: "fail-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
			Failure: Failure{Code: "network", Message: "temporary", Provider: "forum", Retryable: true, OutcomeKnown: true, RetryAfter: time.Minute, Now: testEpoch.Add(time.Second)},
		}
		first, err := store.Fail(in)
		if err != nil {
			t.Fatal(err)
		}
		in.Failure.Now = testEpoch.Add(2 * time.Second)
		replay, err := store.Fail(in)
		if err != nil || replay.Revision != first.Revision || replay.Error.Provider != "forum" || !replay.Error.OutcomeKnown {
			t.Fatalf("Fail replay=%+v err=%v", replay, err)
		}
		in.Failure.Message = "different"
		if _, err := store.Fail(in); !errors.Is(err, ErrIdempotency) {
			t.Fatalf("Fail fingerprint conflict=%v", err)
		}
	})
}

func TestStoreRejectsRequestIDWhitespace(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	in := testCreateInput("helper-a", " create-1")
	if _, err := store.Create(in); err == nil {
		t.Fatal("Create accepted request id with surrounding whitespace")
	}
}

func TestStoreGetReturnsDeepCopy(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	first, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	first.Assistant.Name = "mutated"
	first.Routines[0].Title = "mutated"
	first.Runs[0].State = RunSucceeded
	first.Receipts[0].Operation = "mutated"
	second, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if second.Assistant.Name == "mutated" || second.Routines[0].Title == "mutated" || second.Runs[0].State != RunQueued || second.Receipts[0].Operation == "mutated" {
		t.Fatalf("caller mutation escaped into store: %+v", second)
	}
}

func TestStoreRejectsDangerousRootAndPathEscape(t *testing.T) {
	for _, root := range []string{"", ".", string(filepath.Separator), filepath.VolumeName(t.TempDir()) + string(filepath.Separator)} {
		if _, err := NewStore(root); err == nil {
			t.Errorf("NewStore(%q) succeeded, want rejection", root)
		}
	}
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	for _, id := range []string{"../escape", `..\escape`, "nested/id"} {
		in := testCreateInput(id, "create-escape")
		if _, err := store.Create(in); err == nil {
			t.Errorf("Create assistant %q succeeded, want rejection", id)
		}
	}
}

func TestStoreAggregateReplacementLeavesValidSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	created := mustCreate(t, store, "helper-a")
	current := created.Assistant
	for i := 0; i < 30; i++ {
		current.Description = time.Duration(i).String()
		updated, err := store.UpdateAssistant("update-"+time.Duration(i).String(), current, current.Revision, testEpoch.Add(time.Duration(i+1)*time.Second))
		if err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
		current = updated
		data, err := os.ReadFile(filepath.Join(root, "helper-a", "aggregate.json"))
		if err != nil || len(data) == 0 || data[0] != '{' || data[len(data)-1] != '\n' {
			t.Fatalf("invalid committed snapshot after update %d: len=%d err=%v", i, len(data), err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "helper-a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "aggregate.json" {
		t.Fatalf("aggregate directory contains partial files: %+v", entries)
	}
}

func TestStoreRoutineEditPreservesSchedulingCursor(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	created := mustCreate(t, store, "helper-a")
	cursor := testEpoch.Add(4 * time.Hour)
	advanced, err := store.AdvanceRoutine("helper-a", "routine-a", "cursor-1", created.Routines[0].Revision, cursor, testEpoch.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	edited := *advanced
	edited.Title = "Edited title"
	edited.LastScheduledFor = testEpoch.Add(-time.Hour)
	result, err := store.PutRoutine(RoutineInput{
		RequestID: "routine-edit-1", Routine: edited,
		ExpectedRevision: advanced.Revision, Now: testEpoch.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.LastScheduledFor.Equal(cursor) {
		t.Fatalf("ordinary edit rewound cursor to %s, want %s", result.LastScheduledFor, cursor)
	}
}

func TestStoreQueuedRunFreezesExecutionInputs(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	created := mustCreate(t, store, "helper-a")
	queued := mustTrigger(t, store, "manual-frozen")
	if queued.RoutineRevision != created.Routines[0].Revision || queued.Prompt != created.Routines[0].Prompt || queued.Mission != created.Assistant.Mission || queued.Policy != created.Assistant.Policy {
		t.Fatalf("run did not freeze creation inputs: %+v", queued)
	}

	desired := created.Assistant
	desired.Mission = "new mission"
	desired.Policy.Network = AccessAllow
	if _, err := store.UpdateAssistant("assistant-edit-frozen", desired, desired.Revision, testEpoch.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	routine := created.Routines[0]
	routine.Prompt = "new prompt"
	if _, err := store.PutRoutine(RoutineInput{RequestID: "routine-edit-frozen", Routine: routine, ExpectedRevision: routine.Revision, Now: testEpoch.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Runs[0]
	if got.RoutineRevision != queued.RoutineRevision || got.Prompt != queued.Prompt || got.Mission != queued.Mission || got.Policy != queued.Policy {
		t.Fatalf("queued run inputs changed after edits: before=%+v after=%+v", queued, got)
	}
}

func TestStoreClaimSkipsFutureScheduledRun(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	future := testEpoch.Add(time.Hour)
	if _, err := store.CreateOccurrence(TriggerInput{
		AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "future-occurrence",
		ScheduledFor: future, Now: testEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	if run, ok, err := store.Claim("worker", testEpoch.Add(30*time.Minute), time.Minute); err != nil || ok || run != nil {
		t.Fatalf("future run claimed early: run=%+v ok=%v err=%v", run, ok, err)
	}
	run, ok, err := store.Claim("worker", future, time.Minute)
	if err != nil || !ok || run == nil || run.State != RunRunning {
		t.Fatalf("due run was not claimed: run=%+v ok=%v err=%v", run, ok, err)
	}
}

func TestStoreClaimRecoversExpiredLeaseBeforeSelectingWork(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-expired")
	running, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
	if err != nil || !ok {
		t.Fatalf("initial Claim: run=%+v ok=%v err=%v", running, ok, err)
	}
	if run, ok, err := store.Claim("worker-b", testEpoch.Add(time.Minute), time.Minute); err != nil || ok || run != nil {
		t.Fatalf("Claim should recover to attention, not replay: run=%+v ok=%v err=%v", run, ok, err)
	}
	snapshot, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runs[0].State != RunWaitingAttention || snapshot.Runs[0].Error == nil || snapshot.Runs[0].Error.OutcomeKnown || len(snapshot.Attention) != 1 || snapshot.Attention[0].State != AttentionOpen {
		t.Fatalf("expired lease recovery is incomplete: run=%+v attention=%+v", snapshot.Runs[0], snapshot.Attention)
	}
}

func TestStoreApprovalResolveResumeAndCancelAreIdempotent(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-approval")
	running, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim: run=%+v ok=%v err=%v", running, ok, err)
	}
	approval := ApprovalInput{
		RequestID: "approval-1", RunID: running.ID, LeaseOwner: "worker-a", LeaseFence: running.LeaseFence,
		Action: "publish", Summary: "publish release", SessionPath: "sessions/release", ResumeToken: "resume-1", Now: testEpoch.Add(time.Second),
	}
	waiting, err := store.RequestApproval(approval)
	if err != nil || waiting.State != RunWaitingApproval {
		t.Fatalf("RequestApproval: run=%+v err=%v", waiting, err)
	}
	approval.Now = testEpoch.Add(2 * time.Second)
	replayedApproval, err := store.RequestApproval(approval)
	if err != nil || replayedApproval.Revision != waiting.Revision {
		t.Fatalf("RequestApproval replay: run=%+v err=%v", replayedApproval, err)
	}
	approval.Action = "delete"
	if _, err := store.RequestApproval(approval); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("RequestApproval fingerprint conflict = %v, want ErrIdempotency", err)
	}
	approval.Action = "publish"
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Attention) != 1 || snapshot.Attention[0].State != AttentionOpen {
		t.Fatalf("attention after request = %+v", snapshot.Attention)
	}
	attention := snapshot.Attention[0]
	resolve := ResolveAttentionInput{
		RequestID: "resolve-1", AssistantID: "helper-a", AttentionID: attention.ID,
		ExpectedRevision: attention.Revision, State: AttentionApproved, Resolution: "approved by user", Now: testEpoch.Add(3 * time.Second),
	}
	resolved, err := store.ResolveAttention(resolve)
	if err != nil || resolved.State != AttentionApproved {
		t.Fatalf("ResolveAttention: item=%+v err=%v", resolved, err)
	}
	resolve.Now = testEpoch.Add(4 * time.Second)
	replayedResolve, err := store.ResolveAttention(resolve)
	if err != nil || replayedResolve.Revision != resolved.Revision {
		t.Fatalf("ResolveAttention replay: item=%+v err=%v", replayedResolve, err)
	}
	resume := ResumeInput{RequestID: "resume-1", RunID: running.ID, Now: testEpoch.Add(5 * time.Second)}
	queued, err := store.Resume(resume)
	if err != nil || queued.State != RunQueued {
		t.Fatalf("Resume: run=%+v err=%v", queued, err)
	}
	resume.Now = testEpoch.Add(6 * time.Second)
	replayedResume, err := store.Resume(resume)
	if err != nil || replayedResume.Revision != queued.Revision {
		t.Fatalf("Resume replay: run=%+v err=%v", replayedResume, err)
	}
	cancel := CancelInput{RequestID: "cancel-1", RunID: running.ID, Reason: "user cancelled", Now: testEpoch.Add(7 * time.Second)}
	cancelled, err := store.Cancel(cancel)
	if err != nil || cancelled.State != RunCancelled {
		t.Fatalf("Cancel: run=%+v err=%v", cancelled, err)
	}
	cancel.Now = testEpoch.Add(8 * time.Second)
	replayedCancel, err := store.Cancel(cancel)
	if err != nil || replayedCancel.Revision != cancelled.Revision {
		t.Fatalf("Cancel replay: run=%+v err=%v", replayedCancel, err)
	}
	final, _ := store.Get("helper-a")
	if final.Runs[0].State != RunCancelled || final.Attention[0].State != AttentionApproved {
		t.Fatalf("approval lifecycle did not close cleanly: run=%+v attention=%+v", final.Runs[0], final.Attention[0])
	}
}

func TestStoreRejectedOrCancelledAttentionUnblocksAssistant(t *testing.T) {
	for _, state := range []AttentionState{AttentionRejected, AttentionCancelled} {
		t.Run(string(state), func(t *testing.T) {
			store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
			mustCreate(t, store, "helper-a")
			mustTrigger(t, store, "manual-blocking")
			running, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
			if err != nil || !ok {
				t.Fatalf("Claim: run=%+v ok=%v err=%v", running, ok, err)
			}
			waiting, err := store.RequestApproval(ApprovalInput{
				RequestID: "approval-blocking", RunID: running.ID, LeaseOwner: "worker-a", LeaseFence: running.LeaseFence,
				Action: "publish", Summary: "publish", SessionPath: "sessions/one", ResumeToken: "token-one", Now: testEpoch.Add(time.Second),
			})
			if err != nil || waiting.State != RunWaitingApproval {
				t.Fatalf("RequestApproval: run=%+v err=%v", waiting, err)
			}
			snapshot, _ := store.Get("helper-a")
			attention := snapshot.Attention[0]
			resolved, err := store.ResolveAttention(ResolveAttentionInput{
				RequestID: "resolve-" + string(state), AssistantID: "helper-a", AttentionID: attention.ID,
				ExpectedRevision: attention.Revision, State: state, Resolution: "declined", Now: testEpoch.Add(2 * time.Second),
			})
			if err != nil || resolved.State != state {
				t.Fatalf("ResolveAttention: item=%+v err=%v", resolved, err)
			}
			mustTrigger(t, store, "manual-after-resolution")
			next, ok, err := store.Claim("worker-b", testEpoch.Add(3*time.Second), time.Minute)
			if err != nil || !ok || next == nil || next.RequestID != "manual-after-resolution" {
				t.Fatalf("resolved attention still blocks assistant: run=%+v ok=%v err=%v", next, ok, err)
			}
		})
	}
}

func TestStoreUnknownOutcomeResolutionPaths(t *testing.T) {
	tests := []struct {
		resolution string
		wantState  RunState
	}{
		{resolution: "retry_acknowledged", wantState: RunQueued},
		{resolution: "mark_succeeded", wantState: RunSucceeded},
		{resolution: "mark_failed", wantState: RunFailed},
	}
	for _, tc := range tests {
		t.Run(tc.resolution, func(t *testing.T) {
			store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
			mustCreate(t, store, "helper-a")
			mustTrigger(t, store, "manual-unknown")
			running, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
			if err != nil || !ok {
				t.Fatalf("Claim: run=%+v ok=%v err=%v", running, ok, err)
			}
			if _, err := store.Recover(testEpoch.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := store.Get("helper-a")
			attention := snapshot.Attention[0]
			resolved, err := store.ResolveAttention(ResolveAttentionInput{
				RequestID: "resolve-unknown", AssistantID: "helper-a", AttentionID: attention.ID,
				ExpectedRevision: attention.Revision, State: AttentionApproved, Resolution: tc.resolution, Now: testEpoch.Add(2 * time.Minute),
			})
			if err != nil || resolved.State != AttentionApproved {
				t.Fatalf("ResolveAttention: item=%+v err=%v", resolved, err)
			}
			if tc.resolution == "retry_acknowledged" {
				resumed, err := store.Resume(ResumeInput{RequestID: "resume-unknown", RunID: running.ID, Now: testEpoch.Add(3 * time.Minute)})
				if err != nil || resumed.State != RunQueued {
					t.Fatalf("Resume acknowledged outcome: run=%+v err=%v", resumed, err)
				}
			}
			final, err := store.Get("helper-a")
			if err != nil {
				t.Fatal(err)
			}
			run := final.Runs[0]
			if run.State != tc.wantState {
				t.Fatalf("resolved run state = %s, want %s", run.State, tc.wantState)
			}
			switch tc.resolution {
			case "mark_succeeded":
				if run.Error != nil {
					t.Fatalf("successful resolution retained error: %+v", run.Error)
				}
			case "mark_failed":
				if run.Error == nil || !run.Error.OutcomeKnown {
					t.Fatalf("failed resolution did not mark outcome known: %+v", run.Error)
				}
			}
		})
	}
}

func TestStoreRejectsInvalidTriggerKind(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	if _, err := store.Trigger(TriggerInput{
		AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "invalid-trigger",
		Trigger: TriggerKind("surprise"), Now: testEpoch,
	}); err == nil {
		t.Fatal("Trigger accepted invalid trigger kind")
	}
}

func TestStoreRejectsCorruptAggregatesAndListKeepsHealthyResults(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")
	corruptDir := filepath.Join(root, "helper-b")
	if err := os.MkdirAll(corruptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "aggregate.json"), []byte(`{"version":1,"assistant":{"id":"helper-b"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("helper-b"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get corrupt aggregate error = %v, want ErrCorrupt", err)
	}
	assistants, err := store.List()
	if !errors.Is(err, ErrCorrupt) || len(assistants) != 1 || assistants[0].ID != "helper-a" {
		t.Fatalf("List did not isolate corrupt aggregate: assistants=%+v err=%v", assistants, err)
	}

	t.Run("aggregate symlink", func(t *testing.T) {
		symlinkDir := filepath.Join(root, "helper-c")
		if err := os.MkdirAll(symlinkDir, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(symlinkDir, "aggregate.json")); err != nil {
			t.Skipf("aggregate symlink unavailable: %v", err)
		}
		if _, err := store.Get("helper-c"); err == nil {
			t.Fatal("Get accepted aggregate symlink")
		}
		assistants, err := store.List()
		if !errors.Is(err, ErrCorrupt) || len(assistants) != 1 || assistants[0].ID != "helper-a" {
			t.Fatalf("List did not isolate aggregate symlink: assistants=%+v err=%v", assistants, err)
		}
	})
}

func TestStoreClaimSkipsCorruptAssistantAndRunsHealthyWork(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-healthy")
	corruptDir := filepath.Join(root, "helper-0")
	if err := os.MkdirAll(corruptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "aggregate.json"), []byte(`{"broken":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run, ok, err := store.Claim("worker", testEpoch.Add(time.Second), time.Minute)
	if err != nil || !ok || run == nil || run.AssistantID != "helper-a" {
		t.Fatalf("corrupt assistant blocked healthy claim: run=%+v ok=%v err=%v", run, ok, err)
	}
}

func TestStoreRejectsIDsWithSurroundingWhitespace(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	for _, id := range []string{" helper-a", "helper-a ", "\thelper-a", "helper-a\n"} {
		in := testCreateInput(id, "create-whitespace")
		if _, err := store.Create(in); err == nil {
			t.Errorf("Create accepted assistant ID %q", id)
		}
	}
	in := testCreateInput("helper-a", "create-routine-whitespace")
	in.Routines[0].ID = " routine-a"
	if _, err := store.Create(in); err == nil {
		t.Fatal("Create accepted routine ID with surrounding whitespace")
	}
}

func testStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func testCreateInput(id, requestID string) CreateInput {
	return CreateInput{
		RequestID: requestID,
		Assistant: Assistant{ID: id, Name: "Project helper", Mission: "keep the project healthy", Scope: ScopeGlobal, Lifecycle: LifecycleActive, Policy: DefaultPolicy()},
		Routines: []Routine{{
			ID: "routine-a", AssistantID: id, Title: "Scan changes", Prompt: "Inspect recent changes",
			Schedule: Schedule{Kind: ScheduleInterval, IntervalSeconds: 3600}, Enabled: true, CatchUp: CatchUpCoalesceLatest,
		}},
		Now: testEpoch,
	}
}

func mustCreate(t *testing.T, store *Store, id string) Snapshot {
	t.Helper()
	created, err := store.Create(testCreateInput(id, "create-"+id))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return created
}

func mustTrigger(t *testing.T, store *Store, requestID string) Run {
	t.Helper()
	run, err := store.Trigger(TriggerInput{AssistantID: "helper-a", RoutineID: "routine-a", RequestID: requestID, Trigger: TriggerManual, MaxAttempts: 3, Now: testEpoch})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	return run
}
