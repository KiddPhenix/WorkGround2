package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Fixture with resolver ───────────────────────────────────────────────────

type cmResolverFixture struct {
	*cmFixture
	resolver *FakeCornerstoneResolver
}

func newCMResolverFixture(t *testing.T) *cmResolverFixture {
	t.Helper()
	requireFileStoreIntegration(t)
	root := filepath.Join(t.TempDir(), "works")
	store, err := NewFileWorkStore(root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	clk := &staticClock{now: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}
	svc := NewService(store, NewBlueprintRegistry(), ViewSinkDiscard)
	mgr := NewCornerstoneManager(store, store, clk)
	resolver := NewFakeCornerstoneResolver()
	mgr.SetResolver(resolver)

	input := CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: "blueprint:blank", SchemaVersion: SchemaVersion, Version: 1},
		Name:         "CM Resolver Test Work",
		RequestID:    "req-create-cm-resolve",
	}
	work, createErr := svc.Create(t.Context(), input)
	if createErr != nil {
		t.Fatalf("Create Work: %v", createErr)
	}

	return &cmResolverFixture{
		cmFixture: &cmFixture{
			root:   root,
			store:  store,
			svc:    svc,
			mgr:    mgr,
			workID: work.ID,
			clock:  clk,
		},
		resolver: resolver,
	}
}

func (f *cmResolverFixture) restart(t *testing.T) {
	t.Helper()
	store, err := NewFileWorkStore(f.root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("restart NewFileWorkStore: %v", err)
	}
	f.store = store
	f.svc = NewService(store, NewBlueprintRegistry(), ViewSinkDiscard)
	f.mgr = NewCornerstoneManager(store, store, f.clock)
	f.mgr.SetResolver(f.resolver)
}

// helper: pin a live_ref cornerstone with a specific ref and content.
func (f *cmResolverFixture) pinLiveRef(t *testing.T, ref CornerstoneRef, content, reqID string) *Cornerstone {
	return f.pinLiveRefRequired(t, ref, content, reqID, true)
}

func (f *cmResolverFixture) pinLiveRefRequired(t *testing.T, ref CornerstoneRef, content, reqID string, required bool) *Cornerstone {
	t.Helper()
	cType := CornerstoneSource // default for session_turn, artifact, url
	if ref.Kind == "workspace_file" {
		cType = CornerstoneFileRef
	}
	view, _ := f.svc.Get(t.Context(), f.workID)
	input := PinCornerstoneInput{
		Type:             cType,
		Title:            "Live Ref Test",
		Content:          content,
		Ref:              ref,
		Mode:             CornerstoneLiveRef,
		Required:         required,
		ExpectedRevision: view.Revision,
		RequestID:        reqID,
	}
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("pinLiveRef: %v", err)
	}
	return result.Cornerstone
}

// helper: pin a snapshot cornerstone.
func (f *cmResolverFixture) pinSnapshot(t *testing.T, content, reqID string) *Cornerstone {
	t.Helper()
	view, _ := f.svc.Get(t.Context(), f.workID)
	input := PinCornerstoneInput{
		Type:             CornerstoneInstruction,
		Title:            "Snapshot Test",
		Content:          content,
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         true,
		ExpectedRevision: view.Revision,
		RequestID:        reqID,
	}
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("pinSnapshot: %v", err)
	}
	return result.Cornerstone
}

func currentRevision(t *testing.T, f *cmResolverFixture) int64 {
	t.Helper()
	view, _ := f.svc.Get(t.Context(), f.workID)
	return view.Revision
}

// ── Resolver type tests ─────────────────────────────────────────────────────

func TestCornerstoneResolve_SessionTurn(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "session_turn", SessionID: "sess-1", Turn: 3}
	f.resolver.SetContent(ref, "turn content v1")
	cs := f.pinLiveRef(t, ref, "turn content v1", "req-st-1")

	// Resolve — should be active (same content).
	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-st-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active, got %s: %s", result.Cornerstone.Status, result.Cornerstone.Error)
	}
}

func TestCornerstoneResolve_WorkspaceFile(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/src/main.go"}
	f.resolver.SetContent(ref, "package main")
	cs := f.pinLiveRef(t, ref, "package main", "req-wf-1")

	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-wf-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active, got %s", result.Cornerstone.Status)
	}
}

func TestCornerstoneResolve_Artifact(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "artifact", ArtifactID: "art-42"}
	f.resolver.SetContent(ref, "artifact data")
	cs := f.pinLiveRef(t, ref, "artifact data", "req-art-1")

	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-art-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active, got %s", result.Cornerstone.Status)
	}
}

func TestCornerstoneResolve_URL(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.com/doc"}
	f.resolver.SetContent(ref, "web content")
	cs := f.pinLiveRef(t, ref, "web content", "req-url-1")

	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-url-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active, got %s", result.Cornerstone.Status)
	}
}

func TestCornerstoneRefresh_LiveRefUsesResolver(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.com/refresh"}
	f.resolver.SetContent(ref, "accepted")
	cs := f.pinLiveRef(t, ref, "accepted", "req-refresh-live-pin")
	f.resolver.SetContent(ref, "changed")

	result, err := f.mgr.Refresh(context.Background(), f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: currentRevision(t, f),
		RequestID:        "req-refresh-live",
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneStale || result.Resolution == nil {
		t.Fatalf("Refresh result = %#v", result)
	}
	if result.Resolution.CandidateContent != "changed" || result.Resolution.Diff == "" {
		t.Fatalf("resolution = %#v", result.Resolution)
	}
}

// ── Stale detection + Accept ────────────────────────────────────────────────

func TestCornerstoneResolve_StaleDetectAndAccept(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/src/config.go"}
	f.resolver.SetContent(ref, "v1 content")
	cs := f.pinLiveRef(t, ref, "v1 content", "req-stale-1")

	oldContent := cs.Content
	oldDigest := cs.Digest

	// Change content in resolver.
	f.resolver.SetContent(ref, "v2 content changed")

	// Resolve — should detect stale.
	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-stale-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneStale {
		t.Fatalf("expected stale, got %s", result.Cornerstone.Status)
	}
	if result.Cornerstone.CandidateContent != "v2 content changed" {
		t.Fatalf("expected candidate 'v2 content changed', got %q", result.Cornerstone.CandidateContent)
	}
	if result.Resolution == nil || result.Resolution.Diff == "" || result.Resolution.CandidateDigest == "" {
		t.Fatalf("missing reviewable resolution: %#v", result.Resolution)
	}
	// Old content must be preserved.
	if result.Cornerstone.Content != oldContent {
		t.Fatalf("old content should be preserved, got %q", result.Cornerstone.Content)
	}
	if result.Cornerstone.Digest != oldDigest {
		t.Fatalf("old digest should be preserved")
	}

	// Accept the new version.
	rev2 := currentRevision(t, f)
	acceptResult, err := f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev2,
		RequestID:        "req-stale-accept",
	})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if acceptResult.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active after accept, got %s", acceptResult.Cornerstone.Status)
	}
	if acceptResult.Cornerstone.Content != "v2 content changed" {
		t.Fatalf("expected new content after accept, got %q", acceptResult.Cornerstone.Content)
	}
	if acceptResult.Cornerstone.CandidateContent != "" {
		t.Fatal("candidate should be cleared after accept")
	}
}

func TestCornerstoneResolve_OldContentUnchangedOnStale(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "session_turn", SessionID: "sess-old", Turn: 1}
	f.resolver.SetContent(ref, "original")
	cs := f.pinLiveRef(t, ref, "original", "req-old-1")

	originalContent := cs.Content
	originalDigest := cs.Digest

	// Change content.
	f.resolver.SetContent(ref, "changed")

	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-old-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Content != originalContent {
		t.Fatal("Content was silently overwritten on stale detection")
	}
	if result.Cornerstone.Digest != originalDigest {
		t.Fatal("Digest was silently overwritten on stale detection")
	}
}

// ── Fault injection tests ───────────────────────────────────────────────────

func TestCornerstoneResolve_FaultMissing(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/missing/file.txt"}
	f.resolver.SetContent(ref, "content")
	cs := f.pinLiveRef(t, ref, "content", "req-miss-1")

	// Inject missing fault.
	f.resolver.ClearFault(ref)
	f.resolver.SetFault(ref, ResolveErrorMissing, "file not found", 1)

	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-miss-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneMissing {
		t.Fatalf("expected missing, got %s", result.Cornerstone.Status)
	}
}

func TestCornerstoneResolve_FaultDenied(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/secret/file.txt"}
	f.resolver.SetContent(ref, "secret")
	cs := f.pinLiveRef(t, ref, "secret", "req-denied-1")

	f.resolver.SetFault(ref, ResolveErrorDenied, "permission denied", 1)

	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-denied-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneDenied {
		t.Fatalf("expected denied, got %s", result.Cornerstone.Status)
	}
}

func TestCornerstoneResolve_FaultNetwork(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.com/data"}
	f.resolver.SetContent(ref, "data")
	cs := f.pinLiveRef(t, ref, "data", "req-net-1")

	f.resolver.SetFault(ref, ResolveErrorNetwork, "connection refused", 1)

	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-net-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	// Network error should mark stale (retryable).
	if result.Cornerstone.Status != CornerstoneStale {
		t.Fatalf("expected stale for network error, got %s", result.Cornerstone.Status)
	}
	if result.Cornerstone.ResolveErrorKind != ResolveErrorNetwork || result.Resolution == nil || !result.Resolution.Retryable {
		t.Fatalf("network classification = (%q, %#v), want retryable network", result.Cornerstone.ResolveErrorKind, result.Resolution)
	}
	if strings.Contains(result.Cornerstone.Error, "connection refused") {
		t.Fatalf("resolver detail leaked into persisted error: %q", result.Cornerstone.Error)
	}
}

// ── Required blocking / optional degraded ──────────────────────────────────

func TestCornerstoneResolve_RequiredBlocking(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/required/config.yaml"}
	f.resolver.SetContent(ref, "required config")
	cs := f.pinLiveRef(t, ref, "required config", "req-block-1")
	if !cs.Required {
		t.Fatal("cornerstone should be required")
	}

	// Inject missing — required cornerstone should still show missing.
	f.resolver.SetFault(ref, ResolveErrorMissing, "not found", 1)

	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-block-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneMissing {
		t.Fatalf("required cornerstone should be missing, got %s", result.Cornerstone.Status)
	}
	if !result.Cornerstone.Required {
		t.Fatal("required flag should be preserved")
	}
	if result.Assessment.State != CornerstoneUseBlocked || !result.Assessment.Blocking {
		t.Fatalf("required assessment = %#v, want blocked", result.Assessment)
	}
}

// ── Freeze ──────────────────────────────────────────────────────────────────

func TestCornerstoneResolve_FreezeLiveRef(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/src/main.go"}
	f.resolver.SetContent(ref, "package main\n\nfunc main() {}")
	cs := f.pinLiveRef(t, ref, "package main\n\nfunc main() {}", "req-freeze-1")

	if cs.Mode != CornerstoneLiveRef {
		t.Fatal("should start as live_ref")
	}

	rev := currentRevision(t, f)
	result, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-freeze-action",
	})
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if result.Cornerstone.Mode != CornerstoneSnapshot {
		t.Fatalf("expected snapshot after freeze, got %s", result.Cornerstone.Mode)
	}
	if result.Cornerstone.Type != CornerstoneFileSnapshot {
		t.Fatalf("file type after freeze = %s, want file_snapshot", result.Cornerstone.Type)
	}
	if result.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active after freeze, got %s", result.Cornerstone.Status)
	}
}

func TestCornerstoneResolve_FreezeStaleThenFreeze(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "artifact", ArtifactID: "art-freeze"}
	f.resolver.SetContent(ref, "v1")
	cs := f.pinLiveRef(t, ref, "v1", "req-freeze-stale-1")

	// Change content to make it stale first.
	f.resolver.SetContent(ref, "v2 updated")

	rev := currentRevision(t, f)
	_, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-freeze-stale-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}

	// Now freeze — should use candidate content.
	rev2 := currentRevision(t, f)
	result, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev2,
		RequestID:        "req-freeze-stale-action",
	})
	if err != nil {
		t.Fatalf("Freeze after stale: %v", err)
	}
	if result.Cornerstone.Mode != CornerstoneSnapshot {
		t.Fatalf("expected snapshot, got %s", result.Cornerstone.Mode)
	}
	if result.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active, got %s", result.Cornerstone.Status)
	}
}

// ── Repair ──────────────────────────────────────────────────────────────────

func TestCornerstoneRepair_MissingLiveRef(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "session_turn", SessionID: "sess-repair", Turn: 2}
	f.resolver.SetContent(ref, "repairable content")
	cs := f.pinLiveRef(t, ref, "repairable content", "req-repair-1")

	// Cause missing.
	f.resolver.SetFault(ref, ResolveErrorMissing, "gone", 1)
	rev := currentRevision(t, f)
	_, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-repair-broken",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}

	// Now repair — fault was exhausted so resolve should succeed.
	rev2 := currentRevision(t, f)
	repairResult, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev2,
		RequestID:        "req-repair-fix",
	})
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if !repairResult.Repaired {
		t.Fatal("expected repair to succeed")
	}
	if repairResult.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active after repair, got %s", repairResult.Cornerstone.Status)
	}
}

func TestCornerstoneRepair_ActiveInlineSnapshot(t *testing.T) {
	f := newCMResolverFixture(t)
	cs := f.pinSnapshot(t, "# Config", "req-repair-snap-1")

	// Repair also verifies an inline snapshot that is already healthy.
	rev := currentRevision(t, f)
	repairResult, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-repair-snap",
	})
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if !repairResult.Repaired {
		t.Fatal("active snapshot should report repaired")
	}
}

func TestCornerstoneRepair_PartialFailureRetryable(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/stubborn/file.txt"}
	f.resolver.SetContent(ref, "content")
	cs := f.pinLiveRef(t, ref, "content", "req-repair-partial-1")

	// First, break it by resolving with a permanent missing fault.
	f.resolver.SetFault(ref, ResolveErrorMissing, "still gone", -1) // forever
	rev := currentRevision(t, f)
	_, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-repair-partial-stale",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}

	// Now try to repair — fault is permanent so repair should fail.
	rev2 := currentRevision(t, f)
	repairResult, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev2,
		RequestID:        "req-repair-partial",
	})
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if repairResult.Repaired {
		t.Fatal("repair should fail with persistent missing fault")
	}
	if len(repairResult.FailedRefs) == 0 {
		t.Fatal("should have failed refs")
	}
	if repairResult.Cornerstone.Status == CornerstoneActive {
		t.Fatal("status should not be active after failed repair")
	}
}

// ── Idempotency and conflict ────────────────────────────────────────────────

func TestCornerstoneResolve_RequestIDReplay(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.com/api"}
	f.resolver.SetContent(ref, "api data")
	cs := f.pinLiveRef(t, ref, "api data", "req-replay-1")

	rev := currentRevision(t, f)
	input := RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-replay-resolve",
	}

	result1, err := f.mgr.ResolveAndRefresh(f.workID, input)
	if err != nil {
		t.Fatalf("first ResolveAndRefresh: %v", err)
	}

	// Same requestID — should be idempotent, return same revision.
	view, _ := f.svc.Get(t.Context(), f.workID)
	input.ExpectedRevision = view.Revision
	result2, err := f.mgr.ResolveAndRefresh(f.workID, input)
	if err != nil {
		t.Fatalf("second ResolveAndRefresh: %v", err)
	}
	if !result2.Duplicate {
		t.Fatal("should be duplicate on requestID replay")
	}
	if result2.Revision != result1.Revision {
		t.Fatalf("revision changed on replay: %d → %d", result1.Revision, result2.Revision)
	}
}

func TestCornerstoneResolve_RequestIDRejectsObjectRepoint(t *testing.T) {
	f := newCMResolverFixture(t)
	refA := CornerstoneRef{Kind: "url", URL: "https://example.test/replay-a"}
	refB := CornerstoneRef{Kind: "url", URL: "https://example.test/replay-b"}
	f.resolver.SetContent(refA, "a")
	f.resolver.SetContent(refB, "b")
	csA := f.pinLiveRef(t, refA, "a", "req-repoint-pin-a")
	csB := f.pinLiveRef(t, refB, "b", "req-repoint-pin-b")
	requestID := "req-resolve-repoint"

	if _, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: csA.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
	}); err != nil {
		t.Fatalf("Resolve A: %v", err)
	}
	_, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: csB.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
	})
	if !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("Resolve replay error = %v, want ErrWorkRequestIDConflict", err)
	}
}

func requireCornerstoneRequestConflict(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("replay error = %v, want ErrWorkRequestIDConflict", err)
	}
}

func TestCornerstoneMutationReplay_RejectsObjectRepoint(t *testing.T) {
	t.Run("Accept", func(t *testing.T) {
		f := newCMResolverFixture(t)
		refA := CornerstoneRef{Kind: "url", URL: "https://example.test/accept-a"}
		refB := CornerstoneRef{Kind: "url", URL: "https://example.test/accept-b"}
		f.resolver.SetContent(refA, "a-v1")
		f.resolver.SetContent(refB, "b-v1")
		csA := f.pinLiveRef(t, refA, "a-v1", "req-accept-repoint-pin-a")
		csB := f.pinLiveRef(t, refB, "b-v1", "req-accept-repoint-pin-b")
		f.resolver.SetContent(refA, "a-v2")
		f.resolver.SetContent(refB, "b-v2")
		for i, cs := range []*Cornerstone{csA, csB} {
			if _, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
				CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: fmt.Sprintf("req-accept-repoint-refresh-%d", i),
			}); err != nil {
				t.Fatalf("make stale %d: %v", i, err)
			}
		}
		requestID := "req-accept-repoint"
		if _, err := f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
			CornerstoneID: csA.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		}); err != nil {
			t.Fatalf("Accept A: %v", err)
		}
		_, err := f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
			CornerstoneID: csB.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		})
		requireCornerstoneRequestConflict(t, err)
	})

	t.Run("Freeze", func(t *testing.T) {
		f := newCMResolverFixture(t)
		refA := CornerstoneRef{Kind: "artifact", ArtifactID: "freeze-a"}
		refB := CornerstoneRef{Kind: "artifact", ArtifactID: "freeze-b"}
		f.resolver.SetContent(refA, "a")
		f.resolver.SetContent(refB, "b")
		csA := f.pinLiveRef(t, refA, "a", "req-freeze-repoint-pin-a")
		csB := f.pinLiveRef(t, refB, "b", "req-freeze-repoint-pin-b")
		requestID := "req-freeze-repoint"
		if _, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
			CornerstoneID: csA.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		}); err != nil {
			t.Fatalf("Freeze A: %v", err)
		}
		_, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
			CornerstoneID: csB.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		})
		requireCornerstoneRequestConflict(t, err)
	})

	t.Run("Repair", func(t *testing.T) {
		f := newCMResolverFixture(t)
		refA := CornerstoneRef{Kind: "workspace_file", Path: "/repair-a"}
		refB := CornerstoneRef{Kind: "workspace_file", Path: "/repair-b"}
		f.resolver.SetContent(refA, "a")
		f.resolver.SetContent(refB, "b")
		csA := f.pinLiveRef(t, refA, "a", "req-repair-repoint-pin-a")
		csB := f.pinLiveRef(t, refB, "b", "req-repair-repoint-pin-b")
		f.resolver.SetFault(refA, ResolveErrorMissing, "missing", 1)
		f.resolver.SetFault(refB, ResolveErrorMissing, "missing", 1)
		for i, cs := range []*Cornerstone{csA, csB} {
			if _, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
				CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: fmt.Sprintf("req-repair-repoint-refresh-%d", i),
			}); err != nil {
				t.Fatalf("mark missing %d: %v", i, err)
			}
		}
		requestID := "req-repair-repoint"
		if _, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
			CornerstoneID: csA.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		}); err != nil {
			t.Fatalf("Repair A: %v", err)
		}
		_, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
			CornerstoneID: csB.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		})
		requireCornerstoneRequestConflict(t, err)
	})
}

func TestCornerstoneMutationReplay_RejectsOperationChange(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.test/operation"}
	f.resolver.SetContent(ref, "content")
	cs := f.pinLiveRef(t, ref, "content", "req-operation-pin")
	requestID := "req-operation-reuse"
	if _, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
	})
	requireCornerstoneRequestConflict(t, err)
}

func TestCornerstoneMutationReplay_RejectsIntentChange(t *testing.T) {
	t.Run("Freeze", func(t *testing.T) {
		f := newCMResolverFixture(t)
		ref := CornerstoneRef{Kind: "url", URL: "https://example.test/freeze-intent"}
		f.resolver.SetContent(ref, "content")
		cs := f.pinLiveRef(t, ref, "content", "req-freeze-intent-pin")
		requestID := "req-freeze-intent"
		if _, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
			CornerstoneID: cs.ID, UseLastKnown: true, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		}); err != nil {
			t.Fatalf("Freeze: %v", err)
		}
		_, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
			CornerstoneID: cs.ID, UseLastKnown: false, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		})
		requireCornerstoneRequestConflict(t, err)
	})

	t.Run("RepairRef", func(t *testing.T) {
		f := newCMResolverFixture(t)
		oldRef := CornerstoneRef{Kind: "workspace_file", Path: "/old"}
		newRefA := CornerstoneRef{Kind: "workspace_file", Path: "/new-a"}
		newRefB := CornerstoneRef{Kind: "workspace_file", Path: "/new-b"}
		f.resolver.SetContent(oldRef, "content")
		f.resolver.SetContent(newRefA, "content")
		f.resolver.SetContent(newRefB, "content")
		cs := f.pinLiveRef(t, oldRef, "content", "req-repair-intent-pin")
		f.resolver.SetFault(oldRef, ResolveErrorMissing, "missing", 1)
		if _, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
			CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-repair-intent-refresh",
		}); err != nil {
			t.Fatalf("mark missing: %v", err)
		}
		requestID := "req-repair-intent-ref"
		if _, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
			CornerstoneID: cs.ID, Ref: &newRefA, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		}); err != nil {
			t.Fatalf("Repair ref A: %v", err)
		}
		_, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
			CornerstoneID: cs.ID, Ref: &newRefB, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		})
		requireCornerstoneRequestConflict(t, err)
	})

	t.Run("RepairContent", func(t *testing.T) {
		f := newCMResolverFixture(t)
		content := strings.Repeat("replay-blob-", 500)
		cs := f.pinSnapshot(t, content, "req-repair-content-pin")
		if err := f.store.Delete(f.workID, cs.Ref.BlobDigest); err != nil {
			t.Fatalf("delete blob: %v", err)
		}
		requestID := "req-repair-intent-content"
		if _, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
			CornerstoneID: cs.ID, Content: &content, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		}); err != nil {
			t.Fatalf("Repair content: %v", err)
		}
		changed := content + "changed"
		_, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
			CornerstoneID: cs.ID, Content: &changed, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
		})
		requireCornerstoneRequestConflict(t, err)
	})
}

type barrierCommitStore struct {
	WorkStore
	target  string
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	count   int
}

func (s *barrierCommitStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	if event.RequestID == s.target {
		s.mu.Lock()
		s.count++
		if s.count == 2 {
			close(s.started)
		}
		s.mu.Unlock()
		<-s.release
	}
	return s.WorkStore.CommitEvent(workID, event)
}

func TestCornerstoneMutationReplay_ConcurrentIntentRace(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.test/concurrent-intent"}
	f.resolver.SetContent(ref, "content")
	cs := f.pinLiveRef(t, ref, "content", "req-concurrent-intent-pin")
	requestID := "req-concurrent-intent"
	barrier := &barrierCommitStore{
		WorkStore: f.store,
		target:    cornerstoneMutationRequestID(requestID),
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	f.mgr = NewCornerstoneManager(barrier, f.store, f.clock)
	f.mgr.SetResolver(f.resolver)
	revision := currentRevision(t, f)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, useLastKnown := range []bool{false, true} {
		wg.Add(1)
		go func(index int, lastKnown bool) {
			defer wg.Done()
			_, errs[index] = f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
				CornerstoneID: cs.ID, UseLastKnown: lastKnown, ExpectedRevision: revision, RequestID: requestID,
			})
		}(i, useLastKnown)
	}
	select {
	case <-barrier.started:
		close(barrier.release)
	case <-time.After(10 * time.Second):
		close(barrier.release)
		t.Fatal("both intents did not reach CommitEvent")
	}
	wg.Wait()
	succeeded, conflicted := 0, 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		if errors.Is(err, ErrWorkRequestIDConflict) {
			conflicted++
			continue
		}
		t.Fatalf("Freeze error = %v, want request conflict", err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("outcomes success=%d conflict=%d", succeeded, conflicted)
	}
}

func TestCornerstoneMutationReplay_ObjectBindingSurvivesRestartAndCompact(t *testing.T) {
	f := newCMResolverFixture(t)
	refA := CornerstoneRef{Kind: "artifact", ArtifactID: "compact-a"}
	refB := CornerstoneRef{Kind: "artifact", ArtifactID: "compact-b"}
	f.resolver.SetContent(refA, "a")
	f.resolver.SetContent(refB, "b")
	csA := f.pinLiveRef(t, refA, "a", "req-compact-pin-a")
	csB := f.pinLiveRef(t, refB, "b", "req-compact-pin-b")
	freezeRef := CornerstoneRef{Kind: "artifact", ArtifactID: "compact-freeze"}
	f.resolver.SetContent(freezeRef, "freeze")
	freezeCS := f.pinLiveRef(t, freezeRef, "freeze", "req-compact-freeze-pin")
	requestID := "req-compact-resolve"
	if _, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: csA.ID, ExpectedRevision: currentRevision(t, f), RequestID: requestID,
	}); err != nil {
		t.Fatalf("Resolve A: %v", err)
	}
	freezeRequestID := "req-compact-freeze"
	if _, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
		CornerstoneID: freezeCS.ID, UseLastKnown: true, ExpectedRevision: currentRevision(t, f), RequestID: freezeRequestID,
	}); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	view, err := f.svc.Get(t.Context(), f.workID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	workDir, err := f.store.workPath(f.workID)
	if err != nil {
		t.Fatalf("workPath: %v", err)
	}
	if err := AcquireWorkLease(workDir); err != nil {
		t.Fatalf("AcquireWorkLease: %v", err)
	}
	compactErr := CompactWorkEventLog(workDir, view.Work, DefaultReducer())
	releaseErr := ReleaseWorkLease(workDir)
	if err := errors.Join(compactErr, releaseErr); err != nil {
		t.Fatalf("compact: %v", err)
	}
	f.restart(t)
	replay, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: csA.ID, ExpectedRevision: view.Revision, RequestID: requestID,
	})
	if err != nil || !replay.Duplicate {
		t.Fatalf("same replay after compact = (%#v, %v)", replay, err)
	}
	_, err = f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: csB.ID, ExpectedRevision: view.Revision, RequestID: requestID,
	})
	requireCornerstoneRequestConflict(t, err)
	freezeReplay, err := f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
		CornerstoneID: freezeCS.ID, UseLastKnown: true, ExpectedRevision: view.Revision, RequestID: freezeRequestID,
	})
	if err != nil || !freezeReplay.Duplicate {
		t.Fatalf("same Freeze replay after compact = (%#v, %v)", freezeReplay, err)
	}
	_, err = f.mgr.Freeze(context.Background(), f.workID, FreezeCornerstoneInput{
		CornerstoneID: freezeCS.ID, UseLastKnown: false, ExpectedRevision: view.Revision, RequestID: freezeRequestID,
	})
	requireCornerstoneRequestConflict(t, err)
}

func TestCornerstoneMutationReplay_RemoveAndUndoRejectObjectRepoint(t *testing.T) {
	f := newCMResolverFixture(t)
	csA := f.pinSnapshot(t, "a", "req-remove-repoint-pin-a")
	csB := f.pinSnapshot(t, "b", "req-remove-repoint-pin-b")
	removeRequestID := "req-remove-repoint"
	if _, err := f.mgr.Remove(f.workID, RemoveCornerstoneInput{
		CornerstoneID: csA.ID, ExpectedRevision: currentRevision(t, f), RequestID: removeRequestID,
	}); err != nil {
		t.Fatalf("Remove A: %v", err)
	}
	_, err := f.mgr.Remove(f.workID, RemoveCornerstoneInput{
		CornerstoneID: csB.ID, ExpectedRevision: currentRevision(t, f), RequestID: removeRequestID,
	})
	requireCornerstoneRequestConflict(t, err)
	if _, err := f.mgr.Remove(f.workID, RemoveCornerstoneInput{
		CornerstoneID: csB.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-remove-repoint-b",
	}); err != nil {
		t.Fatalf("Remove B: %v", err)
	}
	undoRequestID := "req-undo-repoint"
	if _, err := f.mgr.Undo(f.workID, UndoCornerstoneInput{
		CornerstoneID: csA.ID, ExpectedRevision: currentRevision(t, f), RequestID: undoRequestID,
	}); err != nil {
		t.Fatalf("Undo A: %v", err)
	}
	_, err = f.mgr.Undo(f.workID, UndoCornerstoneInput{
		CornerstoneID: csB.ID, ExpectedRevision: currentRevision(t, f), RequestID: undoRequestID,
	})
	requireCornerstoneRequestConflict(t, err)
}

func TestCornerstoneResolve_ExpectedRevisionConflict(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "artifact", ArtifactID: "art-conflict"}
	f.resolver.SetContent(ref, "data")
	cs := f.pinLiveRef(t, ref, "data", "req-conflict-1")

	// Use wrong expected revision.
	rev := currentRevision(t, f)
	_, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev + 99, // wrong
		RequestID:        "req-conflict-bad",
	})
	if err == nil {
		t.Fatal("expected revision conflict error")
	}
	var conflictErr *ErrWorkEventConflict
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *ErrWorkEventConflict, got %T: %v", err, err)
	}
}

func TestCornerstoneAccept_ExpectedRevisionConflict(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "session_turn", SessionID: "sess-accept-conflict", Turn: 1}
	f.resolver.SetContent(ref, "v1")
	cs := f.pinLiveRef(t, ref, "v1", "req-accept-conflict-1")

	// Make stale.
	f.resolver.SetContent(ref, "v2")
	rev := currentRevision(t, f)
	f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-accept-conflict-stale",
	})

	// Accept with wrong revision.
	_, err := f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: 999,
		RequestID:        "req-accept-conflict-bad",
	})
	if err == nil {
		t.Fatal("expected revision conflict error for accept")
	}
}

// ── Single-flight ───────────────────────────────────────────────────────────

func TestCornerstoneResolve_SingleFlight(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/shared/data.txt"}
	f.resolver.SetContent(ref, "shared")
	cs := f.pinLiveRef(t, ref, "shared", "req-sf-1")

	gate := &gatedResolver{inner: f.resolver, started: make(chan struct{}), release: make(chan struct{})}
	f.mgr.SetResolver(gate)
	var wg sync.WaitGroup
	results := make([]*CornerstoneResult, 3)
	errs := make([]error, 3)
	start := make(chan struct{})

	rev := currentRevision(t, f)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx], errs[idx] = f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
				CornerstoneID:    cs.ID,
				ExpectedRevision: rev,
				RequestID:        fmt.Sprintf("req-sf-concurrent-%d", idx),
			})
		}(i)
	}
	close(start)
	<-gate.started
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mgr.inflightMu.Lock()
		call := f.mgr.inflight[f.workID+"\x00"+refIdentity(ref)]
		waiters := 0
		if call != nil {
			waiters = call.waiters
		}
		f.mgr.inflightMu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("single-flight waiters = %d, want 2", waiters)
		}
		time.Sleep(time.Millisecond)
	}
	close(gate.release)
	wg.Wait()

	succeeded, conflicted := 0, 0
	for i := 0; i < 3; i++ {
		if errs[i] == nil {
			succeeded++
			if results[i] == nil {
				t.Fatalf("goroutine %d returned nil success", i)
			}
			continue
		}
		var conflict *ErrWorkEventConflict
		if !errors.As(errs[i], &conflict) {
			t.Fatalf("goroutine %d error = %v, want revision conflict", i, errs[i])
		}
		conflicted++
	}
	if succeeded != 1 || conflicted != 2 {
		t.Fatalf("single-flight outcomes success=%d conflict=%d", succeeded, conflicted)
	}
	if calls := f.resolver.CallCount(ref); calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls)
	}
}

type gatedResolver struct {
	inner   CornerstoneResolver
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gatedResolver) Resolve(ctx context.Context, ref CornerstoneRef) (ResolveResult, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return r.inner.Resolve(ctx, ref)
	case <-ctx.Done():
		return ResolveResult{}, ctx.Err()
	}
}

func TestCornerstoneResolve_SingleFlightDifferentCS(t *testing.T) {
	f := newCMResolverFixture(t)
	ref1 := CornerstoneRef{Kind: "url", URL: "https://a.example.com"}
	ref2 := CornerstoneRef{Kind: "url", URL: "https://b.example.com"}
	f.resolver.SetContent(ref1, "a-data")
	f.resolver.SetContent(ref2, "b-data")
	cs1 := f.pinLiveRef(t, ref1, "a-data", "req-sf-a")
	cs2 := f.pinLiveRef(t, ref2, "b-data", "req-sf-b")

	rev := currentRevision(t, f)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
			CornerstoneID: cs1.ID, ExpectedRevision: rev, RequestID: "req-sf-a-resolve",
		})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
			CornerstoneID: cs2.ID, ExpectedRevision: rev, RequestID: "req-sf-b-resolve",
		})
	}()
	wg.Wait()

	succeeded, conflicted := 0, 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		var conflict *ErrWorkEventConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("resolve error = %v, want revision conflict", err)
		}
		conflicted++
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("different-ref outcomes success=%d conflict=%d", succeeded, conflicted)
	}
	if calls := f.resolver.CallCount(ref1); calls != 1 {
		t.Fatalf("resolver calls for ref1 = %d, want 1", calls)
	}
	if calls := f.resolver.CallCount(ref2); calls != 1 {
		t.Fatalf("resolver calls for ref2 = %d, want 1", calls)
	}
}

// ── Restart / recovery ──────────────────────────────────────────────────────

func TestCornerstoneResolve_RestartRecovery(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "session_turn", SessionID: "sess-restart", Turn: 1}
	f.resolver.SetContent(ref, "before restart")
	cs := f.pinLiveRef(t, ref, "before restart", "req-restart-1")

	// Change content.
	f.resolver.SetContent(ref, "after restart change")
	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-restart-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh before restart: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneStale {
		t.Fatalf("expected stale before restart, got %s", result.Cornerstone.Status)
	}

	// Restart — reload from disk.
	f.restart(t)

	// Verify the cornerstone is still stale with candidate.
	view, _ := f.svc.Get(t.Context(), f.workID)
	reloaded := findCornerstone(view.Work, cs.ID)
	if reloaded == nil {
		t.Fatal("cornerstone missing after restart")
	}
	if reloaded.Status != CornerstoneStale {
		t.Fatalf("expected stale after restart, got %s", reloaded.Status)
	}
	if reloaded.CandidateDigest == "" {
		t.Fatal("candidate digest lost after restart")
	}
	if reloaded.CandidateContent != "" {
		// CandidateContent is transient (json:"-"); it should not survive a restart.
		t.Log("candidate content is transient and correctly empty after restart")
	}

	// Accept after restart.
	rev2 := currentRevision(t, f)
	acceptResult, err := f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev2,
		RequestID:        "req-restart-accept",
	})
	if err != nil {
		t.Fatalf("Accept after restart: %v", err)
	}
	if acceptResult.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active after accept, got %s", acceptResult.Cornerstone.Status)
	}
}

func TestCornerstoneResolve_CommitViewRecovery(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "artifact", ArtifactID: "art-commit-view"}
	f.resolver.SetContent(ref, "commit data")
	cs := f.pinLiveRef(t, ref, "commit data", "req-commit-1")

	// Resolve successfully.
	rev := currentRevision(t, f)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-commit-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active, got %s", result.Cornerstone.Status)
	}
	if result.WorkView == nil {
		t.Fatal("WorkView should not be nil")
	}
	if result.WorkView.Work == nil {
		t.Fatal("Work in WorkView should not be nil")
	}
}

// ── Edge cases ──────────────────────────────────────────────────────────────

func TestCornerstoneResolve_ArchivedWorkRejected(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.com"}
	f.resolver.SetContent(ref, "content")
	cs := f.pinLiveRef(t, ref, "content", "req-arch-1")

	// Archive the work.
	_, err := f.svc.Archive(t.Context(), f.workID, "req-arch-action")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}

	rev := currentRevision(t, f)
	_, err = f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-arch-resolve",
	})
	if err == nil {
		t.Fatal("expected error on archived work")
	}
	if !strings.Contains(err.Error(), "archived") {
		t.Fatalf("error should mention archived, got %v", err)
	}
}

func TestCornerstoneResolve_NonExistentCornerstone(t *testing.T) {
	f := newCMResolverFixture(t)
	rev := currentRevision(t, f)
	_, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    "cs-nonexistent",
		ExpectedRevision: rev,
		RequestID:        "req-nonexist",
	})
	if err == nil {
		t.Fatal("expected error for non-existent cornerstone")
	}
}

func TestCornerstoneRepair_AlreadyActive(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "session_turn", SessionID: "sess-active", Turn: 0}
	f.resolver.SetContent(ref, "content")
	cs := f.pinLiveRef(t, ref, "content", "req-active-1")

	rev := currentRevision(t, f)
	result, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-active-repair",
	})
	if err != nil {
		t.Fatalf("Repair on active: %v", err)
	}
	if !result.Repaired {
		t.Fatal("active cornerstone should report repaired")
	}
	if result.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("active cornerstone should stay active, got %s", result.Cornerstone.Status)
	}
}

func TestCornerstoneAccept_NotStale(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.com/ok"}
	f.resolver.SetContent(ref, "ok")
	cs := f.pinLiveRef(t, ref, "ok", "req-notstale-1")

	// Cornerstone is active — Accept should fail.
	rev := currentRevision(t, f)
	_, err := f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-notstale-accept",
	})
	if err == nil {
		t.Fatal("expected error accepting non-stale cornerstone")
	}
}

func TestCornerstoneResolve_ResolverNotSet(t *testing.T) {
	// Manager without resolver returns ErrCornerstoneResolverUnavailable.
	f := newCMFixture(t)
	view, _ := f.svc.Get(t.Context(), f.workID)
	input := PinCornerstoneInput{
		Type:             CornerstoneSource,
		Title:            "No Resolver",
		Content:          "content",
		Ref:              CornerstoneRef{Kind: "url", URL: "https://example.com"},
		Mode:             CornerstoneLiveRef,
		Required:         false,
		ExpectedRevision: view.Revision,
		RequestID:        "req-nores-1",
	}
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	cs := result.Cornerstone
	if cs == nil {
		t.Fatal("pin failed")
	}
	// ResolveAndRefresh without resolver returns a sentinel error.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	_, err = f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: view2.Revision,
		RequestID:        "req-nores-resolve",
	})
	if !errors.Is(err, ErrCornerstoneResolverUnavailable) {
		t.Fatalf("expected ErrCornerstoneResolverUnavailable, got %v", err)
	}
}

func TestCornerstoneResolve_WithoutResolverFallsBack(t *testing.T) {
	// Snapshot Refresh (without resolver) still works via the base Refresh path.
	// For live_ref without resolver, the sentinel error is returned.
	// This test verifies snapshot cornerstones work without a resolver.
	f := newCMFixture(t)
	view, _ := f.svc.Get(t.Context(), f.workID)
	input := PinCornerstoneInput{
		Type:             CornerstoneInstruction,
		Title:            "Fallback Snapshot",
		Content:          "snapshot content",
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         false,
		ExpectedRevision: view.Revision,
		RequestID:        "req-nor-1",
	}
	pinResult, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	cs := pinResult.Cornerstone

	view, _ = f.svc.Get(t.Context(), f.workID)
	result, err := f.mgr.Refresh(context.Background(), f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: view.Revision,
		RequestID:        "req-nor-refresh",
	})
	if err != nil {
		t.Fatalf("Refresh without resolver: %v", err)
	}
	if result.Cornerstone == nil {
		t.Fatal("expected cornerstone in result")
	}
}

// ── Concurrent accept/freeze conflict ──────────────────────────────────────

func TestCornerstoneResolve_ConcurrentAcceptRace(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "artifact", ArtifactID: "art-race"}
	f.resolver.SetContent(ref, "initial")
	cs := f.pinLiveRef(t, ref, "initial", "req-race-1")

	// Make it stale.
	f.resolver.SetContent(ref, "changed")
	rev := currentRevision(t, f)
	f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: rev,
		RequestID:        "req-race-stale",
	})

	// Two concurrent Accept calls with different request IDs — one should win.
	rev2 := currentRevision(t, f)
	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err1 = f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
			CornerstoneID:    cs.ID,
			ExpectedRevision: rev2,
			RequestID:        "req-race-accept-1",
		})
	}()
	go func() {
		defer wg.Done()
		_, err2 = f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
			CornerstoneID:    cs.ID,
			ExpectedRevision: rev2,
			RequestID:        "req-race-accept-2",
		})
	}()
	wg.Wait()

	succeeded, conflicted := 0, 0
	for _, err := range []error{err1, err2} {
		if err == nil {
			succeeded++
			continue
		}
		var conflict *ErrWorkEventConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("accept error = %v, want revision conflict", err)
		}
		conflicted++
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("accept outcomes success=%d conflict=%d", succeeded, conflicted)
	}
}

// ── Benchmark single-flight resolve ─────────────────────────────────────────

func TestCornerstoneResolve_BulkStaleDetection(t *testing.T) {
	f := newCMResolverFixture(t)

	// Pin multiple live_ref cornerstones.
	n := 10
	refs := make([]CornerstoneRef, n)
	csIDs := make([]string, n)
	for i := 0; i < n; i++ {
		ref := CornerstoneRef{Kind: "workspace_file", Path: fmt.Sprintf("/file-%d.txt", i)}
		f.resolver.SetContent(ref, fmt.Sprintf("content-v1-%d", i))
		cs := f.pinLiveRef(t, ref, fmt.Sprintf("content-v1-%d", i), fmt.Sprintf("req-bulk-pin-%d", i))
		refs[i] = ref
		csIDs[i] = cs.ID
	}

	// Change half the files.
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			f.resolver.SetContent(refs[i], fmt.Sprintf("content-v2-%d", i))
		}
	}

	// Resolve all.
	staleCount := 0
	for i := 0; i < n; i++ {
		rev := currentRevision(t, f)
		result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
			CornerstoneID:    csIDs[i],
			ExpectedRevision: rev,
			RequestID:        fmt.Sprintf("req-bulk-resolve-%d", i),
		})
		if err != nil {
			t.Fatalf("ResolveAndRefresh[%d]: %v", i, err)
		}
		if result.Cornerstone.Status == CornerstoneStale {
			staleCount++
		}
	}
	if staleCount != n/2 {
		t.Fatalf("expected %d stale, got %d", n/2, staleCount)
	}
}

func TestCornerstoneResolve_OptionalFailureIsExplicitlyDegraded(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "artifact", ArtifactID: "optional-artifact"}
	f.resolver.SetContent(ref, "optional content")
	cs := f.pinLiveRefRequired(t, ref, "optional content", "req-optional-pin", false)
	f.resolver.SetFault(ref, ResolveErrorDenied, "private path detail", 1)

	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-optional-refresh",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Assessment.State != CornerstoneUseDegraded || result.Assessment.Blocking || !result.Assessment.Degraded {
		t.Fatalf("optional assessment = %#v, want degraded", result.Assessment)
	}
	if len(result.Assessment.Issues) != 1 || result.Assessment.Issues[0].Blocking {
		t.Fatalf("optional issues = %#v", result.Assessment.Issues)
	}
}

func TestCornerstoneAccept_RejectsCandidateThatChangedAfterReview(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.test/changing"}
	f.resolver.SetContent(ref, "v1")
	cs := f.pinLiveRef(t, ref, "v1", "req-toctou-pin")
	f.resolver.SetContent(ref, "v2")
	refresh, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-toctou-refresh",
	})
	if err != nil || refresh.Cornerstone.Status != CornerstoneStale {
		t.Fatalf("stale refresh = (%#v, %v)", refresh, err)
	}
	before := currentRevision(t, f)
	f.resolver.SetContent(ref, "v3")
	_, err = f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: before, RequestID: "req-toctou-accept",
	})
	if !errors.Is(err, ErrCornerstoneCandidateChanged) {
		t.Fatalf("Accept error = %v, want candidate changed", err)
	}
	if after := currentRevision(t, f); after != before {
		t.Fatalf("candidate race mutated revision: %d -> %d", before, after)
	}
}

func TestCornerstoneAccept_LargeCandidateUsesBlobAndDoesNotPersistPendingBody(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "workspace_file", Path: "/large/generated.txt"}
	f.resolver.SetContent(ref, "v1")
	cs := f.pinLiveRef(t, ref, "v1", "req-large-accept-pin")
	large := strings.Repeat("reviewed-large-content-", 400) + "END-UNIQUE-CANDIDATE"
	f.resolver.SetContent(ref, large)
	refresh, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-large-accept-refresh",
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refresh.Resolution == nil || refresh.Resolution.CandidateContent != large || refresh.Cornerstone.Content != "v1" {
		t.Fatalf("large candidate review = %#v", refresh.Resolution)
	}
	workDir, _ := f.store.workPath(f.workID)
	events, err := os.ReadFile(WorkEventLogPath(workDir))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if strings.Contains(string(events), "END-UNIQUE-CANDIDATE") {
		t.Fatal("unaccepted candidate body entered the event log")
	}
	accepted, err := f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-large-accept",
	})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.Cornerstone.Ref.BlobDigest == "" || len(accepted.Cornerstone.Content) >= len(large) {
		t.Fatalf("large accepted storage = digest %q, inline bytes %d", accepted.Cornerstone.Ref.BlobDigest, len(accepted.Cornerstone.Content))
	}
	blob, err := f.store.Get(f.workID, accepted.Cornerstone.Ref.BlobDigest)
	if err != nil || string(blob) != large {
		t.Fatalf("accepted blob = (%d bytes, %v)", len(blob), err)
	}
}

func TestCornerstoneResolve_SecretCandidateNeverPersists(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "session_turn", SessionID: "session-safe", Turn: 4}
	f.resolver.SetContent(ref, "safe")
	cs := f.pinLiveRef(t, ref, "safe", "req-secret-resolve-pin")
	const secret = "sk-proj-abcdefghijklmnopqrstuvwxyz"
	f.resolver.SetContent(ref, secret)
	result, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-secret-resolve",
	})
	if err != nil {
		t.Fatalf("ResolveAndRefresh: %v", err)
	}
	if result.Cornerstone.Status != CornerstoneInvalid || result.Cornerstone.CandidateDigest != "" || result.Resolution.CandidateContent != "" {
		t.Fatalf("secret resolution result = %#v / %#v", result.Cornerstone, result.Resolution)
	}
	workDir, _ := f.store.workPath(f.workID)
	events, _ := os.ReadFile(WorkEventLogPath(workDir))
	if strings.Contains(string(events), secret) {
		t.Fatal("resolver secret entered Work events")
	}
}

func TestCornerstoneRepair_RefChangePreservesAcceptedContentUntilAccept(t *testing.T) {
	f := newCMResolverFixture(t)
	oldRef := CornerstoneRef{Kind: "workspace_file", Path: "/old/config.txt"}
	newRef := CornerstoneRef{Kind: "workspace_file", Path: "/new/config.txt"}
	f.resolver.SetContent(oldRef, "accepted-v1")
	f.resolver.SetContent(newRef, "candidate-v2")
	cs := f.pinLiveRef(t, oldRef, "accepted-v1", "req-ref-repair-pin")
	f.resolver.SetFault(oldRef, ResolveErrorMissing, "gone", 1)
	_, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-ref-repair-missing",
	})
	if err != nil {
		t.Fatalf("mark missing: %v", err)
	}
	repaired, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
		CornerstoneID: cs.ID, Ref: &newRef, ExpectedRevision: currentRevision(t, f), RequestID: "req-ref-repair",
	})
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if repaired.Repaired || repaired.Cornerstone.Status != CornerstoneStale || repaired.Cornerstone.Content != "accepted-v1" {
		t.Fatalf("repair silently accepted content: %#v", repaired.Cornerstone)
	}
	if repaired.Cornerstone.Ref.Path != normalizeCornerstonePath(newRef.Path) || repaired.Resolution == nil || repaired.Resolution.Diff == "" {
		t.Fatalf("repair ref/resolution = (%#v, %#v)", repaired.Cornerstone.Ref, repaired.Resolution)
	}
	accepted, err := f.mgr.Accept(context.Background(), f.workID, AcceptCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-ref-repair-accept",
	})
	if err != nil || accepted.Cornerstone.Content != "candidate-v2" {
		t.Fatalf("Accept repaired ref = (%#v, %v)", accepted, err)
	}
}

func TestCornerstoneRepair_MissingBlobRequiresMatchingReplacementAndSurvivesRestart(t *testing.T) {
	f := newCMResolverFixture(t)
	content := strings.Repeat("snapshot-body-", 500)
	cs := f.pinSnapshot(t, content, "req-blob-repair-pin")
	if cs.Ref.BlobDigest == "" {
		t.Fatal("fixture did not create a blob snapshot")
	}
	if err := f.store.Delete(f.workID, cs.Ref.BlobDigest); err != nil {
		t.Fatalf("delete blob: %v", err)
	}
	invalid, err := f.mgr.ResolveAndRefresh(f.workID, RefreshCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-blob-repair-detect",
	})
	if err != nil || invalid.Cornerstone.Status != CornerstoneInvalid {
		t.Fatalf("detect missing blob = (%#v, %v)", invalid, err)
	}
	failed, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
		CornerstoneID: cs.ID, ExpectedRevision: currentRevision(t, f), RequestID: "req-blob-repair-empty",
	})
	if err != nil || failed.Repaired || len(failed.FailedRefs) == 0 {
		t.Fatalf("empty repair = (%#v, %v)", failed, err)
	}
	wrong := content + "changed"
	failed, err = f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
		CornerstoneID: cs.ID, Content: &wrong, ExpectedRevision: currentRevision(t, f), RequestID: "req-blob-repair-wrong",
	})
	if err != nil || failed.Repaired {
		t.Fatalf("mismatched repair = (%#v, %v)", failed, err)
	}
	repaired, err := f.mgr.Repair(context.Background(), f.workID, RepairCornerstoneInput{
		CornerstoneID: cs.ID, Content: &content, ExpectedRevision: currentRevision(t, f), RequestID: "req-blob-repair-good",
	})
	if err != nil || !repaired.Repaired || repaired.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("good repair = (%#v, %v)", repaired, err)
	}
	f.restart(t)
	view, err := f.svc.Get(t.Context(), f.workID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	reloaded := findCornerstone(view.Work, cs.ID)
	if reloaded == nil || reloaded.Status != CornerstoneActive {
		t.Fatalf("reloaded cornerstone = %#v", reloaded)
	}
	if _, err := f.store.Get(f.workID, reloaded.Ref.BlobDigest); err != nil {
		t.Fatalf("repaired blob after restart: %v", err)
	}
}

// ── Context cancellation ────────────────────────────────────────────────────

type contextResolver struct {
	content string
}

func (r *contextResolver) Resolve(ctx context.Context, ref CornerstoneRef) (ResolveResult, error) {
	// Check context before returning.
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ResolveResult{}, ctx.Err()
		default:
		}
	}
	digest := ContentDigest([]byte(r.content))
	return ResolveResult{
		Content:    r.content,
		Digest:     digest,
		Found:      true,
		Accessible: true,
	}, nil
}

func TestCornerstoneResolve_ContextCancellation(t *testing.T) {
	f := newCMResolverFixture(t)
	ref := CornerstoneRef{Kind: "url", URL: "https://example.com/ctx"}
	cs := f.pinLiveRef(t, ref, "test", "req-context-pin")
	f.mgr.SetResolver(&contextResolver{content: "test"})
	revision := currentRevision(t, f)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.mgr.Resolve(ctx, f.workID, RefreshCornerstoneInput{
		CornerstoneID:    cs.ID,
		ExpectedRevision: revision,
		RequestID:        "req-context-resolve",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve error = %v, want context.Canceled", err)
	}
	if got := currentRevision(t, f); got != revision {
		t.Fatalf("revision = %d, want unchanged %d", got, revision)
	}
}
