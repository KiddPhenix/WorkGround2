package work

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// ── memory store tests ─────────────────────────────────────────────────────

func TestMemoryStoreAcquireReleaseRef(t *testing.T) {
	s := NewMemorySessionRefStore()
	owner := SessionOwner{OwnerType: OwnerWork, OwnerID: "work-1"}

	if err := s.AcquireRef("/sessions/s1.jsonl", owner, "req-1"); err != nil {
		t.Fatalf("AcquireRef: %v", err)
	}
	ref, err := s.IsReferenced("/sessions/s1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !ref {
		t.Fatal("expected referenced after acquire")
	}

	// ReleaseRef fully removes the owner.
	if err := s.ReleaseRef("/sessions/s1.jsonl", owner, "req-2"); err != nil {
		t.Fatalf("ReleaseRef: %v", err)
	}
	ref, err = s.IsReferenced("/sessions/s1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if ref {
		t.Fatal("expected unreferenced after release")
	}
}

func TestMemoryStoreAcquireDuplicateOwnerIdempotent(t *testing.T) {
	s := NewMemorySessionRefStore()
	owner := SessionOwner{OwnerType: OwnerWork, OwnerID: "work-1"}

	if err := s.AcquireRef("/sessions/s1.jsonl", owner, "req-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AcquireRef("/sessions/s1.jsonl", owner, "req-1"); err != nil {
		t.Fatal(err)
	}

	impact, err := s.ForcePurgeImpact("/sessions/s1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if impact == nil || len(impact.AffectedOwners) != 1 {
		t.Fatalf("expected exactly 1 owner after duplicate acquire, got %+v", impact)
	}
}

func TestMemoryStoreReleaseUnknownNoOp(t *testing.T) {
	s := NewMemorySessionRefStore()
	if err := s.ReleaseRef("/sessions/ghost.jsonl", SessionOwner{OwnerType: OwnerWork, OwnerID: "work-1"}, "req-1"); err != nil {
		t.Fatalf("ReleaseRef on unknown session should be no-op: %v", err)
	}
}

func TestMemoryStoreSharedReferences(t *testing.T) {
	s := NewMemorySessionRefStore()
	sp := "/sessions/shared.jsonl"

	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "work-1"}, "r1")
	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "work-2"}, "r2")
	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerBranch, OwnerID: "branch-a"}, "r3")

	ref, _ := s.IsReferenced(sp)
	if !ref {
		t.Fatal("shared session should be referenced")
	}

	// Trash one work — still referenced (active owners remain).
	s.TrashRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "work-1"}, "r4")
	ref, _ = s.IsReferenced(sp)
	if !ref {
		t.Fatal("should still be referenced after trashing one owner")
	}

	impact, _ := s.ForcePurgeImpact(sp)
	// ForcePurgeImpact includes ALL owners (active + trashed).
	if len(impact.AffectedWorkIDs) != 2 {
		t.Fatalf("work IDs (including trashed) = %v", impact.AffectedWorkIDs)
	}
}

func TestMemoryStoreOwnerWorkIDs(t *testing.T) {
	s := NewMemorySessionRefStore()
	sp := "/sessions/s1.jsonl"
	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "work-A"}, "r1")
	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "work-B"}, "r2")
	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerBranch, OwnerID: "br-1"}, "r3")

	ids, err := s.OwnerWorkIDs(sp)
	if err != nil {
		t.Fatal(err)
	}
	expect := []string{"work-A", "work-B"}
	slices.Sort(ids)
	if !slices.Equal(ids, expect) {
		t.Fatalf("OwnerWorkIDs = %v, want %v", ids, expect)
	}
}

func TestMemoryStoreValidateOwner(t *testing.T) {
	s := NewMemorySessionRefStore()
	// Empty owner ID.
	if err := s.AcquireRef("/sessions/s1.jsonl", SessionOwner{OwnerType: OwnerWork, OwnerID: ""}, "r1"); err == nil {
		t.Fatal("expected error for empty owner ID")
	}
	// Unknown owner type.
	if err := s.AcquireRef("/sessions/s1.jsonl", SessionOwner{OwnerType: "bad", OwnerID: "x"}, "r1"); err == nil {
		t.Fatal("expected error for unknown owner type")
	}
}

// ── retention / trash-restore tests ─────────────────────────────────────────

func TestTrashRefMarksOwnerTrashed(t *testing.T) {
	s := NewMemorySessionRefStore(WithRetention(1 * time.Hour))
	sp := "/sessions/s1.jsonl"
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}

	s.AcquireRef(sp, ow, "r1")
	ref, _ := s.IsReferenced(sp)
	if !ref {
		t.Fatal("should be referenced")
	}

	s.TrashRef(sp, ow, "r2")
	ref, _ = s.IsReferenced(sp)
	if ref {
		t.Fatal("should not be referenced after trash (no active owners)")
	}

	// Still not purgeable (within retention).
	purgeable, _ := s.IsPurgeable(sp, time.Now().UnixMilli())
	if purgeable {
		t.Fatal("should not be purgeable within retention period")
	}
}

func TestRestoreRefBringsBackActive(t *testing.T) {
	s := NewMemorySessionRefStore()
	sp := "/sessions/s1.jsonl"
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}

	s.AcquireRef(sp, ow, "r1")
	s.TrashRef(sp, ow, "r2")
	ref, _ := s.IsReferenced(sp)
	if ref {
		t.Fatal("should not be referenced after trash")
	}

	s.RestoreRef(sp, ow, "r3")
	ref, _ = s.IsReferenced(sp)
	if !ref {
		t.Fatal("should be referenced after restore")
	}
}

func TestRestoreRefUnknownOwnerAddsActive(t *testing.T) {
	s := NewMemorySessionRefStore()
	sp := "/sessions/s1.jsonl"
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: "w-new"}

	// Restore on a session with no prior record — adds active owner (reconcile).
	s.RestoreRef(sp, ow, "r1")
	ref, _ := s.IsReferenced(sp)
	if !ref {
		t.Fatal("should be referenced after restore-add")
	}
}

func TestIsPurgeableAfterRetention(t *testing.T) {
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	clock := &fixedClock{t: base}
	s := NewMemorySessionRefStore(WithRetention(1*time.Hour), WithClock(clock.Now))
	sp := "/sessions/s1.jsonl"
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}

	s.AcquireRef(sp, ow, "r1")
	s.TrashRef(sp, ow, "r2")

	// Not purgeable yet.
	purgeable, _ := s.IsPurgeable(sp, base.UnixMilli())
	if purgeable {
		t.Fatal("should not be purgeable immediately after trash")
	}

	// Advance past retention.
	clock.Advance(2 * time.Hour)
	purgeable, _ = s.IsPurgeable(sp, base.UnixMilli())
	if !purgeable {
		t.Fatal("should be purgeable after retention expires")
	}
}

func TestIsPurgeableSharedSessionMixedStates(t *testing.T) {
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	clock := &fixedClock{t: base}
	s := NewMemorySessionRefStore(WithRetention(1*time.Hour), WithClock(clock.Now))
	sp := "/sessions/shared.jsonl"

	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}, "r1")
	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w2"}, "r2")

	// Trash w1 only. w2 still active → not purgeable.
	s.TrashRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}, "r3")
	purgeable, _ := s.IsPurgeable(sp, base.UnixMilli())
	if purgeable {
		t.Fatal("should not be purgeable while w2 is still active")
	}

	// Trash w2. Both trashed, but within retention.
	s.TrashRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w2"}, "r4")
	purgeable, _ = s.IsPurgeable(sp, base.UnixMilli())
	if purgeable {
		t.Fatal("should not be purgeable within retention")
	}

	// Advance past retention.
	clock.Advance(2 * time.Hour)
	purgeable, _ = s.IsPurgeable(sp, base.UnixMilli())
	if !purgeable {
		t.Fatal("should be purgeable after all trashed + retention expires")
	}
}

func TestIsPurgeableZeroRetention(t *testing.T) {
	s := NewMemorySessionRefStore(WithRetention(0))
	sp := "/sessions/s1.jsonl"
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}

	s.AcquireRef(sp, ow, "r1")
	s.TrashRef(sp, ow, "r2")
	purgeable, _ := s.IsPurgeable(sp, time.Now().UnixMilli())
	if !purgeable {
		t.Fatal("should be purgeable with zero retention")
	}
}

func TestIsPurgeableUnknownSession(t *testing.T) {
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	clock := &fixedClock{t: base}
	s := NewMemorySessionRefStore(WithRetention(1*time.Hour), WithClock(clock.Now))
	purgeable, _ := s.IsPurgeable("/sessions/unknown.jsonl", 0)
	if purgeable {
		t.Fatal("unknown session without a durable trash timestamp should fail closed")
	}
	clock.Advance(2 * time.Hour)
	purgeable, _ = s.IsPurgeable("/sessions/unknown.jsonl", base.UnixMilli())
	if !purgeable {
		t.Fatal("unknown session should be purgeable after trash retention")
	}
}

func TestReleaseRefFullyRemoves(t *testing.T) {
	s := NewMemorySessionRefStore(WithRetention(0))
	sp := "/sessions/s1.jsonl"
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}

	s.AcquireRef(sp, ow, "r1")
	s.ReleaseRef(sp, ow, "r2")
	purgeable, _ := s.IsPurgeable(sp, 0)
	if !purgeable {
		t.Fatal("fully released session should be purgeable")
	}
}

// ── index rebuild tests ────────────────────────────────────────────────────

func TestRebuildFromProjections(t *testing.T) {
	s := NewMemorySessionRefStore()
	works := []WorkProjectionSummary{
		{WorkID: "w1", SessionPaths: []string{"/sessions/a.jsonl", "/sessions/b.jsonl"}},
		{WorkID: "w2", SessionPaths: []string{"/sessions/b.jsonl", "/sessions/c.jsonl"}},
	}
	if err := s.RebuildFromProjections(works); err != nil {
		t.Fatal(err)
	}

	impact, err := s.ForcePurgeImpact("/sessions/b.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.AffectedWorkIDs) != 2 {
		t.Fatalf("shared session b should have 2 work owners, got %v", impact.AffectedWorkIDs)
	}
}

func TestRebuildDropsPreviousState(t *testing.T) {
	s := NewMemorySessionRefStore()
	s.AcquireRef("/sessions/old.jsonl", SessionOwner{OwnerType: OwnerWork, OwnerID: "w-old"}, "r1")

	if err := s.RebuildFromProjections([]WorkProjectionSummary{
		{WorkID: "w-new", SessionPaths: []string{"/sessions/new.jsonl"}},
	}); err != nil {
		t.Fatal(err)
	}
	ref, _ := s.IsReferenced("/sessions/old.jsonl")
	if ref {
		t.Fatal("old session should not be referenced after rebuild")
	}
	ref, _ = s.IsReferenced("/sessions/new.jsonl")
	if !ref {
		t.Fatal("new session should be referenced")
	}
}

func TestRebuildWithBranchRefs(t *testing.T) {
	s := NewMemorySessionRefStore()
	works := []WorkProjectionSummary{
		{
			WorkID:       "w1",
			SessionPaths: []string{"/sessions/s1.jsonl"},
			BranchRefs: []BranchSessionRef{
				{BranchID: "branch-1", SessionPath: "/sessions/s1.jsonl"},
				{BranchID: "branch-2", SessionPath: "/sessions/s1.jsonl"},
			},
		},
	}
	if err := s.RebuildFromProjections(works); err != nil {
		t.Fatal(err)
	}

	impact, _ := s.ForcePurgeImpact("/sessions/s1.jsonl")
	if len(impact.AffectedWorkIDs) != 1 {
		t.Fatalf("s1 work IDs = %v", impact.AffectedWorkIDs)
	}
	if len(impact.AffectedBranchIDs) != 2 {
		t.Fatalf("s1 branch IDs = %v, want [branch-1, branch-2]", impact.AffectedBranchIDs)
	}
}

func TestRebuildBranchRefsOnCorrectSession(t *testing.T) {
	// Branch refs should attach to their SessionPath, not create standalone entries.
	s := NewMemorySessionRefStore()
	works := []WorkProjectionSummary{
		{
			WorkID:       "w1",
			SessionPaths: []string{"/sessions/s1.jsonl"},
			BranchRefs: []BranchSessionRef{
				{BranchID: "br-a", SessionPath: "/sessions/s1.jsonl"},
			},
		},
	}
	s.RebuildFromProjections(works)

	// Branch ID should NOT appear as a standalone session path.
	impact, _ := s.ForcePurgeImpact("br-a")
	if impact != nil {
		t.Fatalf("branch ID should not be a standalone session path, got %+v", impact)
	}
	// But it should appear in the session's branch IDs.
	impact, _ = s.ForcePurgeImpact("/sessions/s1.jsonl")
	if len(impact.AffectedBranchIDs) != 1 || impact.AffectedBranchIDs[0] != "br-a" {
		t.Fatalf("s1 branch IDs = %v", impact.AffectedBranchIDs)
	}
}

func TestRebuildScopePreservesOtherProjectsAndCleanup(t *testing.T) {
	s := NewMemorySessionRefStore()
	if err := s.RebuildScope("scope-a", []WorkProjectionSummary{{WorkID: "a1", SessionPaths: []string{"/sessions/shared.jsonl", "/sessions/a.jsonl"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildScope("scope-b", []WorkProjectionSummary{{WorkID: "b1", SessionPaths: []string{"/sessions/shared.jsonl", "/sessions/b.jsonl"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCleanupPending("/sessions/shared.jsonl", "retry", "cleanup-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildScope("scope-a", []WorkProjectionSummary{{WorkID: "a2", SessionPaths: []string{"/sessions/new-a.jsonl"}}}); err != nil {
		t.Fatal(err)
	}

	if ref, _ := s.IsReferenced("/sessions/a.jsonl"); ref {
		t.Fatal("stale scope-a owner should be removed")
	}
	impact, _ := s.ForcePurgeImpact("/sessions/shared.jsonl")
	if impact == nil || !slices.Equal(impact.AffectedWorkIDs, []string{"b1"}) {
		t.Fatalf("shared impact after scoped rebuild = %+v", impact)
	}
	if ref, _ := s.IsReferenced("/sessions/new-a.jsonl"); !ref {
		t.Fatal("replacement scope-a owner is missing")
	}
	pending, _ := s.ListCleanupPending()
	if len(pending) != 1 || pending[0].RequestID != "cleanup-1" {
		t.Fatalf("cleanup ledger should survive index rebuild: %+v", pending)
	}
}

// ── cleanup-pending tests ──────────────────────────────────────────────────

func TestCleanupPendingRecordAndClear(t *testing.T) {
	s := NewMemorySessionRefStore()

	if err := s.RecordCleanupPending("/sessions/s1.jsonl", "purge failed", "req-purge-1"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListCleanupPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Reason != "purge failed" || pending[0].Stage != "starting" {
		t.Fatalf("pending record = %+v", pending[0])
	}

	s.ClearCleanupPending("/sessions/s1.jsonl", "req-purge-1")
	pending, _ = s.ListCleanupPending()
	if len(pending) != 0 {
		t.Fatal("expected 0 pending after clear")
	}
}

func TestCleanupPendingDuplicateIncrementsAttempts(t *testing.T) {
	s := NewMemorySessionRefStore()
	s.RecordCleanupPending("/sessions/s1.jsonl", "fail", "req-1")
	s.RecordCleanupPending("/sessions/s1.jsonl", "fail-again", "req-1")

	pending, _ := s.ListCleanupPending()
	if len(pending) != 1 {
		t.Fatalf("duplicate should not create second record, got %d", len(pending))
	}
	if pending[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", pending[0].Attempts)
	}
}

func TestFileSessionRefStorePersistsRefsAndCleanupAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-refs.json")
	store, err := NewFileSessionRefStore(path, WithRetention(0))
	if err != nil {
		t.Fatal(err)
	}
	owner := SessionOwner{OwnerType: OwnerBranch, OwnerID: "Branch-A", WorkID: "work-1", ScopeID: "scope-a"}
	if err := store.AcquireRef("/sessions/a.jsonl", owner, "Acquire-A"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCleanupPending("/sessions/a.jsonl", "force purge", "Cleanup-A"); err != nil {
		t.Fatal(err)
	}
	impact := &ForcePurgeImpact{AffectedWorkIDs: []string{"work-1"}}
	if err := store.UpdateCleanupPending("/sessions/a.jsonl", "Cleanup-A", "failed", "injected", impact); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileSessionRefStore(path, WithRetention(0))
	if err != nil {
		t.Fatal(err)
	}
	gotImpact, err := reopened.ForcePurgeImpact("/sessions/a.jsonl")
	if err != nil || gotImpact == nil || !slices.Equal(gotImpact.AffectedWorkIDs, []string{"work-1"}) || !slices.Equal(gotImpact.AffectedBranchIDs, []string{"Branch-A"}) {
		t.Fatalf("reopened impact=%+v err=%v", gotImpact, err)
	}
	pending, err := reopened.ListCleanupPending()
	if err != nil || len(pending) != 1 || pending[0].RequestID != "Cleanup-A" || pending[0].Stage != "failed" {
		t.Fatalf("reopened cleanup=%+v err=%v", pending, err)
	}
}

func TestFileSessionRefStoreRollsBackMemoryWhenPersistFails(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileSessionRefStore(filepath.Join(blocker, "refs.json"), WithRetention(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcquireRef("/sessions/a.jsonl", SessionOwner{OwnerType: OwnerWork, OwnerID: "work-1"}, "req-1"); err == nil {
		t.Fatal("expected persistence failure")
	}
	if ref, _ := store.IsReferenced("/sessions/a.jsonl"); ref {
		t.Fatal("failed persistence must roll back in-memory ref")
	}
}

func TestUpdateCleanupPending(t *testing.T) {
	s := NewMemorySessionRefStore()
	s.RecordCleanupPending("/sessions/s1.jsonl", "purge", "req-1")

	impact := &ForcePurgeImpact{SessionPath: "/sessions/s1.jsonl", AffectedWorkIDs: []string{"w1"}}
	s.UpdateCleanupPending("/sessions/s1.jsonl", "req-1", "failed", "disk full", impact)

	rec, ok, _ := s.GetCleanupPending("/sessions/s1.jsonl", "req-1")
	if !ok {
		t.Fatal("expected record to exist")
	}
	if rec.Stage != "failed" || rec.Error != "disk full" || rec.Impact == nil {
		t.Fatalf("updated record = %+v", rec)
	}
}

func TestGetCleanupPendingNotFound(t *testing.T) {
	s := NewMemorySessionRefStore()
	_, ok, _ := s.GetCleanupPending("/sessions/s1.jsonl", "req-none")
	if ok {
		t.Fatal("should not find non-existent record")
	}
}

// ── coordinator tests ──────────────────────────────────────────────────────

func TestCoordinatorOnWorkLifecycle(t *testing.T) {
	s := NewMemorySessionRefStore()
	c := NewSessionRefCoordinator(s)
	sp := []string{"/sessions/a.jsonl", "/sessions/b.jsonl"}

	c.OnWorkCreated("w1", sp, "req-create")
	for _, p := range sp {
		ref, _ := s.IsReferenced(p)
		if !ref {
			t.Fatalf("%s should be referenced after create", p)
		}
	}

	c.OnWorkArchived("w1", sp, "req-archive")
	for _, p := range sp {
		ref, _ := s.IsReferenced(p)
		if !ref {
			t.Fatalf("%s should still be referenced after archive", p)
		}
	}

	// Delete trashes refs (marks trashed, not removed).
	c.OnWorkDeleted("w1", sp, "req-delete")
	for _, p := range sp {
		ref, _ := s.IsReferenced(p)
		if ref {
			t.Fatalf("%s should not be referenced after delete", p)
		}
	}

	// Restore brings back active.
	c.OnWorkRestored("w1", sp, "req-restore")
	for _, p := range sp {
		ref, _ := s.IsReferenced(p)
		if !ref {
			t.Fatalf("%s should be referenced after restore", p)
		}
	}
}

func TestReconcileDeletedWorkCreatesTrashedOwnersIdempotently(t *testing.T) {
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	clock := &fixedClock{t: base}
	store := NewMemorySessionRefStore(WithClock(clock.Now), WithRetention(time.Hour))
	coord := NewSessionRefCoordinator(store)
	value := &Work{
		ID: "work-1", ArchiveState: ArchiveDeleted, UpdatedAt: base,
		Runs: []WorkflowRun{{Stages: []Stage{{Tasks: []Task{{Attempts: []Attempt{{
			SessionRef: SessionRef{SessionPath: "/sessions/a.jsonl", BranchID: "Branch-A"},
		}}}}}}}},
	}
	if err := coord.ReconcileWork("scope-a", value, "delete-1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Minute)
	if err := coord.ReconcileWork("scope-a", value, "delete-1"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(31 * time.Minute)
	purgeable, err := store.IsPurgeable("/sessions/a.jsonl", base.UnixMilli())
	if err != nil || !purgeable {
		t.Fatalf("repeated delete must not extend retention: purgeable=%v err=%v", purgeable, err)
	}
	impact, _ := store.ForcePurgeImpact("/sessions/a.jsonl")
	if impact == nil || !slices.Equal(impact.AffectedWorkIDs, []string{"work-1"}) || !slices.Equal(impact.AffectedBranchIDs, []string{"Branch-A"}) {
		t.Fatalf("typed deleted owners = %+v", impact)
	}
}

func TestServiceCoordinatesSessionRefsAcrossRunDeleteRestoreAndRebuild(t *testing.T) {
	f := newRunnerFixture(t)
	refs := NewMemorySessionRefStore(WithRetention(0))
	if err := f.svc.SetSessionRefStore(refs, "scope-a"); err != nil {
		t.Fatal(err)
	}
	const sessionPath = "/sessions/service-owned.jsonl"
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		return &Attempt{State: RunCompleted, SessionRef: SessionRef{SessionPath: sessionPath, BranchID: "Branch-Service"}}, nil
	}
	bp := testBlueprint("blueprint:session-ref", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: bp.Version}, "session-ref-create")
	if _, err := f.svc.RunWork(context.Background(), value.ID, "session-ref-run"); err != nil {
		t.Fatal(err)
	}
	impact, _ := refs.ForcePurgeImpact(sessionPath)
	if impact == nil || !slices.Equal(impact.AffectedWorkIDs, []string{value.ID}) || !slices.Equal(impact.AffectedBranchIDs, []string{"Branch-Service"}) {
		t.Fatalf("run did not acquire typed Session owners: %+v", impact)
	}

	if err := f.svc.Delete(context.Background(), value.ID, "session-ref-delete"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(context.Background(), value.ID, "session-ref-delete"); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if referenced, _ := refs.IsReferenced(sessionPath); referenced {
		t.Fatal("deleted Work owners should be trashed")
	}

	rebuilt := NewMemorySessionRefStore(WithRetention(0))
	restarted := NewService(f.store, f.registry, ViewSinkDiscard)
	if err := restarted.SetSessionRefStore(rebuilt, "scope-a"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RebuildSessionRefs(context.Background()); err != nil {
		t.Fatal(err)
	}
	impact, _ = rebuilt.ForcePurgeImpact(sessionPath)
	if impact == nil || len(impact.AffectedOwners) != 2 {
		t.Fatalf("restart rebuild lost deleted owners: %+v", impact)
	}
	if referenced, _ := rebuilt.IsReferenced(sessionPath); referenced {
		t.Fatal("rebuilt deleted owners should remain trashed")
	}

	if _, err := restarted.Restore(context.Background(), value.ID, "session-ref-restore"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Restore(context.Background(), value.ID, "session-ref-restore"); err != nil {
		t.Fatalf("repeat restore: %v", err)
	}
	if referenced, _ := rebuilt.IsReferenced(sessionPath); !referenced {
		t.Fatal("restored Work owners should be active")
	}
}

func TestCoordinatorCheckForcePurge(t *testing.T) {
	s := NewMemorySessionRefStore()
	c := NewSessionRefCoordinator(s)
	s.AcquireRef("/sessions/s1.jsonl", SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}, "r1")

	_, err := c.CheckForcePurge("/sessions/s1.jsonl", false)
	if err == nil {
		t.Fatal("expected error for referenced session without force")
	}

	impact, err := c.CheckForcePurge("/sessions/s1.jsonl", true)
	if err != nil {
		t.Fatalf("force purge check should succeed: %v", err)
	}
	if len(impact.AffectedWorkIDs) != 1 || impact.AffectedWorkIDs[0] != "w1" {
		t.Fatalf("impact = %+v", impact)
	}
}

func TestCoordinatorReconcileFromWorks(t *testing.T) {
	s := NewMemorySessionRefStore()
	c := NewSessionRefCoordinator(s)

	// Seed with stale data.
	s.AcquireRef("/sessions/stale.jsonl", SessionOwner{OwnerType: OwnerWork, OwnerID: "w-old"}, "r1")

	works := []WorkProjectionSummary{
		{WorkID: "w1", SessionPaths: []string{"/sessions/fresh.jsonl"}},
	}
	if err := c.ReconcileFromWorks(works); err != nil {
		t.Fatal(err)
	}

	ref, _ := s.IsReferenced("/sessions/stale.jsonl")
	if ref {
		t.Fatal("stale data should be gone after reconcile")
	}
	ref, _ = s.IsReferenced("/sessions/fresh.jsonl")
	if !ref {
		t.Fatal("fresh data should be present")
	}
}

func TestCoordinatorCleanupPendingRoundTrip(t *testing.T) {
	s := NewMemorySessionRefStore()
	c := NewSessionRefCoordinator(s)

	c.RecordCleanupPending("/sessions/s1.jsonl", "disk full", "req-1")
	pending, _ := c.ListCleanupPending()
	if len(pending) != 1 || pending[0].Reason != "disk full" {
		t.Fatalf("pending = %+v", pending)
	}

	c.UpdateCleanupPending("/sessions/s1.jsonl", "req-1", "purging", "", nil)
	pending, _ = c.ListCleanupPending()
	if pending[0].Stage != "purging" {
		t.Fatalf("stage = %s", pending[0].Stage)
	}

	c.ClearCleanupPending("/sessions/s1.jsonl", "req-1")
	pending, _ = c.ListCleanupPending()
	if len(pending) != 0 {
		t.Fatal("expected empty after clear")
	}
}

// ── snapshot encode/decode tests ───────────────────────────────────────────

func TestEncodeDecodeSessionRefSnapshotRoundTrip(t *testing.T) {
	records := map[string]*SessionRefRecord{
		"/sessions/a.jsonl": {
			SessionPath: "/sessions/a.jsonl",
			Owners:      []SessionOwner{{OwnerType: OwnerWork, OwnerID: "w1", State: OwnerActive}},
			CreatedAt:   1000, UpdatedAt: 2000,
		},
		"/sessions/b.jsonl": {
			SessionPath: "/sessions/b.jsonl",
			Owners: []SessionOwner{
				{OwnerType: OwnerWork, OwnerID: "w1", State: OwnerActive},
				{OwnerType: OwnerWork, OwnerID: "w2", State: OwnerTrashed, TrashedAt: 3000},
			},
			CreatedAt: 3000, UpdatedAt: 4000,
		},
	}

	data, err := EncodeSessionRefSnapshot(records, 5000)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSessionRefSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(records) {
		t.Fatalf("decoded %d records, want %d", len(decoded), len(records))
	}
	for k, want := range records {
		got, ok := decoded[k]
		if !ok {
			t.Fatalf("missing key %s", k)
		}
		if len(got.Owners) != len(want.Owners) {
			t.Fatalf("%s owners = %d, want %d", k, len(got.Owners), len(want.Owners))
		}
		// Verify owner state is preserved.
		for i, wo := range want.Owners {
			if got.Owners[i].State != wo.State {
				t.Fatalf("%s owner[%d] state = %s, want %s", k, i, got.Owners[i].State, wo.State)
			}
		}
	}
}

func TestDecodeSessionRefSnapshotInvalid(t *testing.T) {
	_, err := DecodeSessionRefSnapshot([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ── clock injection tests ──────────────────────────────────────────────────

type fixedClock struct {
	t time.Time
}

func (c *fixedClock) Now() time.Time          { return c.t }
func (c *fixedClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func TestMemoryStoreCustomClock(t *testing.T) {
	frozen := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	s := NewMemorySessionRefStore(WithClock(func() time.Time { return frozen }))
	s.AcquireRef("/sessions/s1.jsonl", SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}, "r1")

	impact, _ := s.ForcePurgeImpact("/sessions/s1.jsonl")
	if impact.AffectedOwners[0].OwnerID != "w1" {
		t.Fatalf("owner = %+v", impact.AffectedOwners[0])
	}
}

// ── concurrency test ───────────────────────────────────────────────────────

func TestMemoryStoreConcurrentAccess(t *testing.T) {
	s := NewMemorySessionRefStore()
	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 50

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				s.AcquireRef("/sessions/concurrent.jsonl", SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}, "req")
				s.IsReferenced("/sessions/concurrent.jsonl")
				s.IsPurgeable("/sessions/concurrent.jsonl", 0)
				s.ForcePurgeImpact("/sessions/concurrent.jsonl")
			}
		}(i)
	}
	wg.Wait()

	ref, err := s.IsReferenced("/sessions/concurrent.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !ref {
		t.Fatal("concurrent acquires should leave session referenced")
	}
}

func TestMemoryStoreReleaseAllRefsThenReacquire(t *testing.T) {
	s := NewMemorySessionRefStore()
	sp := "/sessions/reacquire.jsonl"

	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}, "r1")
	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w2"}, "r2")

	s.TrashRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w1"}, "r3")
	s.ReleaseRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w2"}, "r4")

	ref, _ := s.IsReferenced(sp)
	if ref {
		t.Fatal("should not be referenced after all owners gone/trashed")
	}

	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w3"}, "r5")
	impact, _ := s.ForcePurgeImpact(sp)
	hasW3 := false
	for _, id := range impact.AffectedWorkIDs {
		if id == "w3" {
			hasW3 = true
		}
	}
	if !hasW3 {
		t.Fatalf("after reacquire, should have w3 in %v", impact.AffectedWorkIDs)
	}
}

// ── ForcePurgeImpact includes trashed owners ───────────────────────────────

func TestForcePurgeImpactIncludesTrashedOwners(t *testing.T) {
	s := NewMemorySessionRefStore()
	sp := "/sessions/s1.jsonl"

	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w-active"}, "r1")
	s.AcquireRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w-trashed"}, "r2")
	s.TrashRef(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: "w-trashed"}, "r3")

	impact, _ := s.ForcePurgeImpact(sp)
	if len(impact.AffectedWorkIDs) != 2 {
		t.Fatalf("impact should include trashed owners: got %v", impact.AffectedWorkIDs)
	}
	if len(impact.AffectedOwners) != 2 {
		t.Fatalf("affected owners = %d, want 2", len(impact.AffectedOwners))
	}
}
