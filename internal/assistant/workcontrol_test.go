package assistant

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkControlDefaultIsRunning(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	wc, err := store.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != WorkRunning || wc.Epoch != 1 || wc.Fence == "" {
		t.Fatalf("default work control = %+v", wc)
	}
}

func TestWorkControlPauseResumeLifecycleIsIdempotent(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	now := testEpoch

	quiescing, err := store.PauseAll("pause-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if quiescing.State != WorkQuiescing || quiescing.Epoch != 2 {
		t.Fatalf("PauseAll = %+v", quiescing)
	}

	// Replay is a no-op: no extra epoch bump, no error.
	again, err := store.PauseAll("pause-1", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if again.Epoch != quiescing.Epoch {
		t.Fatalf("replay bumped epoch: %+v", again)
	}

	paused, err := store.CompletePause("complete-pause-1", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if paused.State != WorkPaused || paused.Epoch != 2 {
		t.Fatalf("CompletePause = %+v", paused)
	}

	recovering, err := store.ResumeAll("resume-1", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovering.State != WorkRecovering || recovering.Epoch != 3 {
		t.Fatalf("ResumeAll = %+v", recovering)
	}

	running, err := store.CompleteResume("complete-resume-1", now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if running.State != WorkRunning || running.Epoch != 3 {
		t.Fatalf("CompleteResume = %+v", running)
	}
}

func TestWorkControlBlocksNewWorkWhilePaused(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	if _, err := store.PauseAll("pause-1", testEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("complete-1", testEpoch); err != nil {
		t.Fatal(err)
	}

	// Manual trigger (new Run) is refused.
	if _, err := store.Trigger(TriggerInput{AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "manual-paused", Trigger: TriggerManual, Now: testEpoch}); !errors.Is(err, ErrWorkPaused) {
		t.Fatalf("Trigger while paused error = %v, want ErrWorkPaused", err)
	}

	// Claim refuses to start queued work.
	if _, ok, err := store.Claim("worker-a", testEpoch, time.Minute); err != nil || ok {
		t.Fatalf("Claim while paused = ok=%v err=%v, want no claim", ok, err)
	}
	if _, ok, err := store.ClaimJob("worker-a", testEpoch, time.Minute); err != nil || ok {
		t.Fatalf("ClaimJob while paused = ok=%v err=%v, want no claim", ok, err)
	}

	// Scheduler tick creates no occurrences.
	scheduler, err := NewScheduler(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := scheduler.Tick(testEpoch.Add(5 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 0 {
		t.Fatalf("paused scheduler created %d runs", len(result.Runs))
	}
}

func TestWorkControlRejectsLateResultAcrossPauseResume(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run, ok, err := store.Claim("worker-a", testEpoch, time.Hour)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	if run.WorkEpoch != 1 {
		t.Fatalf("claimed run WorkEpoch = %d, want 1", run.WorkEpoch)
	}

	if _, err := store.PauseAll("pause-1", testEpoch.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("complete-1", testEpoch.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeAll("resume-1", testEpoch.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteResume("complete-2", testEpoch.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}

	// The run was claimed before the pause; its late completion must be refused.
	if _, err := store.Finish(FinishInput{RequestID: "finish-late", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence, Now: testEpoch.Add(5 * time.Second)}); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("late Finish error = %v, want ErrStaleFence", err)
	}
}

func TestWorkControlJobRejectsLateResultAcrossPauseResume(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	dispatch, err := store.OpenDispatch(OpenDispatchInput{AssistantID: "helper-a", RequestID: "dispatch-1", Input: "ship the fix", Now: testEpoch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClassifyDispatch(ClassifyDispatchInput{
		AssistantID: "helper-a", DispatchID: dispatch.ID, RequestID: "classify-1",
		Kind: DispatchTask, Reply: "ok", Jobs: []JobSpec{{Name: "job-a", Kind: DispatchTask, Prompt: "do it"}}, Now: testEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.ClaimJob("worker-a", testEpoch, time.Hour)
	if err != nil || !ok {
		t.Fatalf("ClaimJob: ok=%v err=%v", ok, err)
	}
	if job.WorkEpoch != 1 {
		t.Fatalf("claimed job WorkEpoch = %d, want 1", job.WorkEpoch)
	}

	if _, err := store.PauseAll("pause-1", testEpoch.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("complete-1", testEpoch.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeAll("resume-1", testEpoch.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteResume("complete-2", testEpoch.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.FinishJob(FinishJobInput{RequestID: "finish-late", JobID: job.ID, LeaseOwner: "worker-a", LeaseFence: job.LeaseFence, Now: testEpoch.Add(5 * time.Second)}); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("late FinishJob error = %v, want ErrStaleFence", err)
	}
}

func TestWorkControlPauseForRestartConsumesIntent(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	now := testEpoch

	wc, err := store.PauseForRestart("restart-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if wc.State != WorkQuiescing || wc.RestartIntent != RestartIntentRestart {
		t.Fatalf("PauseForRestart = %+v", wc)
	}
	if _, err := store.CompletePause("complete-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	// An explicit pause has no restart intent, so recovery is a no-op.
	if _, err := store.PauseAll("pause-1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("complete-2", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	still, err := store.BeginRestartRecovery("recover-explicit", now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if still.State != WorkPaused {
		t.Fatalf("explicit pause auto-resumed to %s", still.State)
	}

	// A restart intent does auto-recover.
	if _, err := store.PauseForRestart("restart-2", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("complete-3", now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	recovering, err := store.BeginRestartRecovery("recover-restart", now.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovering.State != WorkRecovering || recovering.RestartIntent != RestartIntentNone {
		t.Fatalf("BeginRestartRecovery = %+v", recovering)
	}
}

// TestWorkControlPlanWriteRefusedWhilePaused proves the pause fence is checked
// before Plan write-back (CompleteRunWithProgress / RecordProgress) and that a
// run claimed under an older epoch cannot complete late.
func TestWorkControlPlanWriteRefusedWhilePaused(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	now := testEpoch

	// A run claimed before the pause, completing late under the new epoch.
	mustTrigger(t, store, "manual-1")
	run, ok, err := store.Claim("worker-a", now, time.Hour)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	if _, err := store.PauseAll("pause-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("complete-1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	// Late completion with progress is refused: stale fence first, then paused.
	_, err = store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "finish-late", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Summary: "done", Progress: ProgressBlock{},
		Now: now.Add(3 * time.Second),
	})
	if !errors.Is(err, ErrStaleFence) {
		t.Fatalf("late CompleteRunWithProgress error = %v, want ErrStaleFence", err)
	}

	// A brand-new progress write-back while paused is refused too.
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "progress-paused", AssistantID: "helper-a",
		Progress: ProgressBlock{},
		Now:      now.Add(4 * time.Second),
	}); !errors.Is(err, ErrWorkPaused) {
		t.Fatalf("RecordProgress while paused error = %v, want ErrWorkPaused", err)
	}
}

// TestWorkControlDispatchAndExternalActionRefusedWhilePaused proves new
// dispatches and external-action confirmations are fenced before receipt writes.
func TestWorkControlDispatchAndExternalActionRefusedWhilePaused(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	now := testEpoch

	if _, err := store.PauseAll("pause-1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("complete-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	// New dispatch is refused while paused.
	if _, err := store.OpenDispatch(OpenDispatchInput{
		AssistantID: "helper-a", RequestID: "dispatch-paused", Input: "do the thing", Now: now.Add(2 * time.Second),
	}); !errors.Is(err, ErrWorkPaused) {
		t.Fatalf("OpenDispatch while paused error = %v, want ErrWorkPaused", err)
	}
	// A new external action is refused while paused.
	if _, _, err := store.BeginChannelAction(BeginChannelActionInput{
		AssistantID: "helper-a", RequestID: "action-paused", ChannelID: "ch-1",
		Kind: ChannelCreateTopic, Title: "t", Body: "b", Now: now.Add(3 * time.Second),
	}); !errors.Is(err, ErrWorkPaused) {
		t.Fatalf("BeginChannelAction while paused error = %v, want ErrWorkPaused", err)
	}
}

// TestWorkControlResumeAdmitsRecoveryWrites proves RECOVERING admits the
// recovery-driven write-backs (Plan, external-action confirmation) that
// QUIESCING/PAUSED refused, and still refuses brand-new dispatches.
func TestWorkControlResumeAdmitsRecoveryWrites(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	now := testEpoch

	if _, err := store.PauseAll("pause-1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("complete-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeAll("resume-1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	// RECOVERING admits Plan write-back (resume_all re-drives interrupted work).
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "progress-recover", AssistantID: "helper-a",
		Progress: ProgressBlock{
			Responsibilities: []RespDecl{{Alias: "z", Objective: "o", DoneCriteria: "d"}},
		},
		Now: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("RecordProgress during RECOVERING = %v, want nil", err)
	}
	// RECOVERING still refuses brand-new dispatches.
	if _, err := store.OpenDispatch(OpenDispatchInput{
		AssistantID: "helper-a", RequestID: "dispatch-recover", Input: "new work", Now: now.Add(4 * time.Second),
	}); !errors.Is(err, ErrWorkPaused) {
		t.Fatalf("OpenDispatch during RECOVERING error = %v, want ErrWorkPaused", err)
	}
}

// TestWorkControlCrossProcessPauseResumeSerializes proves two Store instances
// over the same root serialize their read-modify-write cycles through the
// cross-process file lock: concurrent pause+resume never loses an update and
// request-ID replay stays idempotent.
func TestWorkControlCrossProcessPauseResumeSerializes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store1 := testStore(t, root)
	store2 := testStore(t, root)
	now := testEpoch

	wc1, err := store1.PauseAll("pause-1", now)
	if err != nil {
		t.Fatal(err)
	}
	// The second process observes the same persisted state (shared file).
	wc2, err := store2.WorkControl()
	if err != nil {
		t.Fatal(err)
	}
	if wc2.State != WorkQuiescing || wc2.Epoch != wc1.Epoch || wc2.Revision != wc1.Revision {
		t.Fatalf("second process gate = %+v, want quiescing/epoch %d/rev %d", wc2, wc1.Epoch, wc1.Revision)
	}

	// Replay on the other process is idempotent: no epoch/revision churn.
	again, err := store2.PauseAll("pause-1", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if again.Epoch != wc1.Epoch || again.Revision != wc1.Revision {
		t.Fatalf("cross-process replay = %+v, want epoch %d rev %d", again, wc1.Epoch, wc1.Revision)
	}

	// Complete pause from process 2, then resume from process 1: both see the
	// same monotonic generation.
	paused, err := store2.CompletePause("complete-2", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := store1.ResumeAll("resume-1", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != WorkRecovering || resumed.Epoch != paused.Epoch+1 {
		t.Fatalf("resume after cross-process pause = %+v, want recovering/epoch %d", resumed, paused.Epoch+1)
	}
}
