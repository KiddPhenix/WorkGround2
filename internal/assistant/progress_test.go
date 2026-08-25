package assistant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAndStripProgressBlocks(t *testing.T) {
	t.Parallel()
	text := "done here\n<assistant-progress>{\"complete\":[\"a\"]}</assistant-progress>\nand more"
	blocks, errs := ParseProgressBlocks(text)
	if len(errs) != 0 {
		t.Fatalf("parse errors = %v", errs)
	}
	if len(blocks) != 1 || len(blocks[0].Complete) != 1 || blocks[0].Complete[0] != "a" {
		t.Fatalf("blocks = %+v", blocks)
	}
	if got := StripProgressBlocks(text); strings.Contains(got, "<assistant-progress>") || !strings.Contains(got, "done here") || !strings.Contains(got, "and more") {
		t.Fatalf("stripped = %q", got)
	}
}

func TestParseProgressBlocksReportsMalformed(t *testing.T) {
	t.Parallel()
	_, errs := ParseProgressBlocks("x<assistant-progress>not-json</assistant-progress>y")
	if len(errs) == 0 {
		t.Fatal("expected a malformed-block error")
	}
	_, errs = ParseProgressBlocks("x<assistant-progress>{")
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "unterminated") {
		t.Fatalf("unterminated errors = %v", errs)
	}
	if got := StripProgressBlocks("before<assistant-progress>dangling"); got != "before" {
		t.Fatalf("dangling strip = %q", got)
	}
}

func mustClaimRun(t *testing.T, store *Store) Run {
	t.Helper()
	run, ok, err := store.Claim("worker-a", testEpoch, time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim = %+v ok=%v err=%v", run, ok, err)
	}
	return *run
}

func TestStoreCompleteRunWithProgressAppliesPlan(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)

	done, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Summary: "shipped scan", SessionPath: "s.json",
		Progress: ProgressBlock{
			PlanRevision:   1,
			Responsibility: "scan",
			Responsibilities: []RespDecl{
				{Alias: "scan", Objective: "scan changes", DoneCriteria: "report written", NextAction: "run scan"},
			},
			Complete:      []string{"scan"},
			Artifacts:     []ArtifactDecl{{Resp: "scan", Title: "scan report", Kind: "report", Content: "clean"}},
			Opportunities: []OpportunityDecl{{Resp: "scan", Reason: "ready to publish"}},
		},
		Now: testEpoch.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if done.State != RunSucceeded || done.ResponsibilityID == "" {
		t.Fatalf("completed run = %+v", done)
	}
	snapshot, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Plan.Revision != 2 || len(snapshot.Plan.Responsibilities) != 1 {
		t.Fatalf("plan = %+v", snapshot.Plan)
	}
	resp := snapshot.Plan.Responsibilities[0]
	if resp.Alias != "scan" || resp.Status != RespDone || resp.DoneCriteria != "report written" || resp.NextAction != "run scan" {
		t.Fatalf("responsibility = %+v", resp)
	}
	if len(snapshot.Artifacts) != 1 || snapshot.Artifacts[0].RunID != run.ID || snapshot.Artifacts[0].RespID != resp.ID {
		t.Fatalf("artifacts = %+v", snapshot.Artifacts)
	}
	if len(snapshot.Opportunities) != 1 || snapshot.Opportunities[0].RunID != run.ID {
		t.Fatalf("opportunities = %+v", snapshot.Opportunities)
	}
}

func TestStoreCompleteRunUnlocksDownstreamInSamePatch(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	// First run declares two responsibilities and completes the upstream one.
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Summary: "upstream done",
		Progress: ProgressBlock{
			PlanRevision: 1,
			Responsibilities: []RespDecl{
				{Alias: "up", Objective: "do up"},
				{Alias: "down", Objective: "do down", DependsOn: []string{"up"}},
			},
			Complete: []string{"up"},
		},
		Now: testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get("helper-a")
	down := findRespByAlias(snapshot, "down")
	if down == nil || down.Status != RespReady || down.BlockReason != "" {
		t.Fatalf("downstream not unlocked: %+v", down)
	}

	// Second run completes the downstream responsibility.
	mustTrigger(t, store, "manual-2")
	run2 := mustClaimRun(t, store)
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-2", RunID: run2.ID, LeaseOwner: "worker-a", LeaseFence: run2.LeaseFence,
		Summary: "downstream done",
		Progress: ProgressBlock{
			PlanRevision: 2,
			Complete:     []string{"down"},
		},
		Now: testEpoch.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.Get("helper-a")
	down = findRespByAlias(snapshot, "down")
	if down == nil || down.Status != RespDone {
		t.Fatalf("downstream not completed: %+v", down)
	}
}

func TestStoreCompleteRunRejectsSelfDependency(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)

	_, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "self", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{{Alias: "a", Objective: "x", DependsOn: []string{"a"}}}},
		Now:      testEpoch.Add(time.Second),
	})
	if err == nil || !strings.Contains(err.Error(), "depend on itself") {
		t.Fatalf("self-dependency error = %v", err)
	}
	snapshot, _ := store.Get("helper-a")
	if snapshot.Runs[0].State != RunRunning {
		t.Fatalf("failed patch moved run to %s", snapshot.Runs[0].State)
	}
}

func TestStoreCompleteRunRejectsCycle(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)

	_, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "cycle", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{
			{Alias: "a", Objective: "a", DependsOn: []string{"b"}},
			{Alias: "b", Objective: "b", DependsOn: []string{"a"}},
		}},
		Now: testEpoch.Add(time.Second),
	})
	if !errors.Is(err, ErrTransition) {
		t.Fatalf("cycle error = %v, want ErrTransition", err)
	}
}

func TestStoreCompleteRunRejectsBlockedCompletion(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)
	_, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "blocked", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{
			{Alias: "a", Objective: "a"},
			{Alias: "b", Objective: "b", DependsOn: []string{"a"}},
		}, Complete: []string{"b"}},
		Now: testEpoch.Add(time.Second),
	})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("blocked completion error = %v, want ErrBlocked", err)
	}
}

func TestStoreCompleteRunRejectsStalePlanRevision(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{{Alias: "a", Objective: "a"}}},
		Now:      testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	mustTrigger(t, store, "manual-2")
	run2 := mustClaimRun(t, store)
	_, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "stale", RunID: run2.ID, LeaseOwner: "worker-a", LeaseFence: run2.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{{Alias: "b", Objective: "b"}}},
		Now:      testEpoch.Add(2 * time.Second),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision error = %v, want ErrConflict", err)
	}
}

func TestStoreCompleteRunIsIdempotentAndRejectsFingerprintConflict(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)
	in := CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Summary: "done", Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{{Alias: "a", Objective: "a"}}},
		Now: testEpoch.Add(time.Second),
	}
	first, err := store.CompleteRunWithProgress(in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.CompleteRunWithProgress(in)
	if err != nil || replay.Revision != first.Revision {
		t.Fatalf("idempotent replay = %+v err=%v", replay, err)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Plan.Responsibilities) != 1 {
		t.Fatalf("replay duplicated responsibilities: %+v", snapshot.Plan.Responsibilities)
	}
	in.Progress.Responsibilities[0].Objective = "different"
	if _, err := store.CompleteRunWithProgress(in); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("fingerprint conflict error = %v, want ErrIdempotency", err)
	}
}

func TestStoreCompleteRunReceiptSurvivesReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)
	in := CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Summary: "done", Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{{Alias: "a", Objective: "a"}}},
		Now: testEpoch.Add(time.Second),
	}
	first, err := store.CompleteRunWithProgress(in)
	if err != nil {
		t.Fatal(err)
	}

	// A crash after the write is modelled by reopening over the same root. The
	// receipt embedded in aggregate.json must return the original result without
	// re-applying.
	reopened := testStore(t, root)
	replay, err := reopened.CompleteRunWithProgress(in)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Revision != first.Revision || replay.State != RunSucceeded {
		t.Fatalf("replay = %+v, want original %+v", replay, first)
	}
	snapshot, err := reopened.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Plan.Responsibilities) != 1 || len(snapshot.Artifacts) != 0 {
		t.Fatalf("replay re-applied plan: %+v", snapshot.Plan)
	}
}

func TestStoreOldAggregateLazilyLoadsEmptyPlan(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")

	// Simulate a pre-plan aggregate by removing the plan fields from disk.
	path := filepath.Join(root, "helper-a", "aggregate.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "plan")
	delete(raw, "artifacts")
	delete(raw, "opportunities")
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened := testStore(t, root)
	snapshot, err := reopened.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Plan.Revision != 1 || len(snapshot.Plan.Responsibilities) != 0 {
		t.Fatalf("lazy plan = %+v", snapshot.Plan)
	}
	if snapshot.Artifacts == nil || snapshot.Opportunities == nil {
		t.Fatalf("lazy artifact slices should be non-nil")
	}
}

func TestStoreReadRejectsPersistedResponsibilityCycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{
			{Alias: "a", Objective: "a"},
			{Alias: "b", Objective: "b", DependsOn: []string{"a"}},
		}},
		Now: testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "helper-a", "aggregate.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted aggregate
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	a := responsibilityIndex(&persisted, StableID("resp", "helper-a/a"))
	b := responsibilityIndex(&persisted, StableID("resp", "helper-a/b"))
	if a < 0 || b < 0 {
		t.Fatalf("seed responsibilities missing: %+v", persisted.Plan.Responsibilities)
	}
	persisted.Plan.Responsibilities[a].DependsOn = []string{persisted.Plan.Responsibilities[b].ID}
	data, err = json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := testStore(t, root).Get("helper-a"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("persisted cycle error = %v, want ErrCorrupt", err)
	}
}

func TestStoreCompleteRunCompletesDownstreamAndUpstreamInOnePatch(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)

	// down depends on up, and both are completed in one patch. Slice order must
	// not matter, so the downstream is listed before its upstream.
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Summary: "done both",
		Progress: ProgressBlock{
			PlanRevision: 1,
			Responsibilities: []RespDecl{
				{Alias: "up", Objective: "do up"},
				{Alias: "down", Objective: "do down", DependsOn: []string{"up"}},
			},
			Complete: []string{"down", "up"},
		},
		Now: testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get("helper-a")
	down, up := findRespByAlias(snapshot, "down"), findRespByAlias(snapshot, "up")
	if down == nil || down.Status != RespDone || up == nil || up.Status != RespDone {
		t.Fatalf("both should be done: down=%+v up=%+v", down, up)
	}
}

func TestStoreCompleteRunUpdatesExistingDependenciesReversibly(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)

	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{
			{Alias: "up", Objective: "do up"},
			{Alias: "down", Objective: "do down"},
		}},
		Now: testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get("helper-a")
	down := findRespByAlias(snapshot, "down")
	if down == nil || down.Status != RespReady {
		t.Fatalf("down should be ready without deps: %+v", down)
	}
	downRev, planRev := down.Revision, snapshot.Plan.Revision

	// Adding an incomplete dependency must demote the ready responsibility.
	mustTrigger(t, store, "manual-2")
	run2 := mustClaimRun(t, store)
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-2", RunID: run2.ID, LeaseOwner: "worker-a", LeaseFence: run2.LeaseFence,
		Progress: ProgressBlock{PlanRevision: planRev, Responsibilities: []RespDecl{
			{Alias: "down", Objective: "do down", DependsOn: []string{"up"}},
		}},
		Now: testEpoch.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.Get("helper-a")
	down = findRespByAlias(snapshot, "down")
	if down == nil || down.Status != RespBlocked || len(down.DependsOn) != 1 || down.Revision <= downRev {
		t.Fatalf("down should be blocked on up with bumped revision: %+v", down)
	}
	planRev = snapshot.Plan.Revision

	// Clearing the dependency must promote it back to ready.
	mustTrigger(t, store, "manual-3")
	run3 := mustClaimRun(t, store)
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-3", RunID: run3.ID, LeaseOwner: "worker-a", LeaseFence: run3.LeaseFence,
		Progress: ProgressBlock{PlanRevision: planRev, Responsibilities: []RespDecl{
			{Alias: "down", Objective: "do down", DependsOn: []string{}},
		}},
		Now: testEpoch.Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.Get("helper-a")
	down = findRespByAlias(snapshot, "down")
	if down == nil || down.Status != RespReady || len(down.DependsOn) != 0 {
		t.Fatalf("down should be ready with cleared deps: %+v", down)
	}
}

func TestStoreCompleteRunRejectsDoneGainingIncompleteDependency(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)

	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{
			{Alias: "up", Objective: "do up"},
			{Alias: "pending", Objective: "not done yet"},
		}, Complete: []string{"up"}},
		Now: testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get("helper-a")
	planRev := snapshot.Plan.Revision

	mustTrigger(t, store, "manual-2")
	run2 := mustClaimRun(t, store)
	_, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-2", RunID: run2.ID, LeaseOwner: "worker-a", LeaseFence: run2.LeaseFence,
		Progress: ProgressBlock{PlanRevision: planRev, Responsibilities: []RespDecl{
			{Alias: "up", Objective: "do up", DependsOn: []string{"pending"}},
		}},
		Now: testEpoch.Add(2 * time.Second),
	})
	if !errors.Is(err, ErrTransition) {
		t.Fatalf("done gaining incomplete dep error = %v, want ErrTransition", err)
	}
}

func TestStoreCompleteRunRedeclaringUnchangedIsNoop(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)
	decl := RespDecl{Alias: "scan", Objective: "scan changes", DoneCriteria: "report written", NextAction: "run scan"}
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-1", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		Progress: ProgressBlock{PlanRevision: 1, Responsibilities: []RespDecl{decl}},
		Now:      testEpoch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get("helper-a")
	resp := findRespByAlias(snapshot, "scan")
	if resp == nil {
		t.Fatal("scan missing")
	}
	respRev, planRev := resp.Revision, snapshot.Plan.Revision

	mustTrigger(t, store, "manual-2")
	run2 := mustClaimRun(t, store)
	if _, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "progress-2", RunID: run2.ID, LeaseOwner: "worker-a", LeaseFence: run2.LeaseFence,
		Progress: ProgressBlock{PlanRevision: planRev, Responsibilities: []RespDecl{decl}},
		Now:      testEpoch.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.Get("helper-a")
	resp = findRespByAlias(snapshot, "scan")
	if resp == nil || resp.Revision != respRev {
		t.Fatalf("unchanged redeclaration bumped responsibility revision: %d -> %+v", respRev, resp)
	}
	if snapshot.Plan.Revision != planRev {
		t.Fatalf("unchanged redeclaration bumped plan revision: %d -> %d", planRev, snapshot.Plan.Revision)
	}
}

func TestStoreCompleteRunRejectsMissingResponsibilityID(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)

	_, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "missing-resp", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		ResponsibilityID: "resp-missing",
		Now:              testEpoch.Add(time.Second),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing responsibility error = %v, want ErrNotFound", err)
	}
	snapshot, _ := store.Get("helper-a")
	if snapshot.Runs[0].State != RunRunning || snapshot.Runs[0].ResponsibilityID != "" {
		t.Fatalf("missing responsibility mutated run: %+v", snapshot.Runs[0])
	}
}

func TestStoreCompleteRunRejectsResponsibilityIDMismatchWithBlock(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	mustTrigger(t, store, "manual-1")
	run := mustClaimRun(t, store)

	_, err := store.CompleteRunWithProgress(CompleteRunInput{
		RequestID: "mismatch-resp", RunID: run.ID, LeaseOwner: "worker-a", LeaseFence: run.LeaseFence,
		ResponsibilityID: "resp-other",
		Progress: ProgressBlock{
			PlanRevision:     1,
			Responsibility:   "scan",
			Responsibilities: []RespDecl{{Alias: "scan", Objective: "scan changes"}},
		},
		Now: testEpoch.Add(time.Second),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("responsibility mismatch error = %v, want ErrConflict", err)
	}
}

func findRespByAlias(snapshot Snapshot, alias string) *Responsibility {
	for i := range snapshot.Plan.Responsibilities {
		if snapshot.Plan.Responsibilities[i].Alias == alias {
			return &snapshot.Plan.Responsibilities[i]
		}
	}
	return nil
}
