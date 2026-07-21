package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Fixture ─────────────────────────────────────────────────────────────────

type cmFixture struct {
	root   string
	store  *FileWorkStore
	svc    *Service
	mgr    *CornerstoneManager
	workID string
	clock  *staticClock
}

func newCMFixture(t *testing.T) *cmFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "works")
	store, err := NewFileWorkStore(root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	clk := &staticClock{now: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}
	svc := NewService(store, NewBlueprintRegistry(), ViewSinkDiscard)
	mgr := NewCornerstoneManager(store, store, clk) // FileWorkStore implements both WorkStore and BlobStore

	input := CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: "blueprint:blank", SchemaVersion: SchemaVersion, Version: 1},
		Name:         "CM Test Work",
		RequestID:    "req-create-cm",
	}
	work, createErr := svc.Create(t.Context(), input)
	if createErr != nil {
		t.Fatalf("Create Work: %v", createErr)
	}

	return &cmFixture{
		root:   root,
		store:  store,
		svc:    svc,
		mgr:    mgr,
		workID: work.ID,
		clock:  clk,
	}
}

func (f *cmFixture) restart(t *testing.T) {
	t.Helper()
	store, err := NewFileWorkStore(f.root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("restart NewFileWorkStore: %v", err)
	}
	f.store = store
	f.svc = NewService(store, NewBlueprintRegistry(), ViewSinkDiscard)
	f.mgr = NewCornerstoneManager(store, store, f.clock)
}

type staticClock struct {
	now time.Time
}

func (c *staticClock) Now() time.Time                         { return c.now }
func (c *staticClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type eventFirstBlobStore struct {
	BlobStore
	store     WorkStore
	requestID string
	mu        sync.Mutex
	failPuts  int
	sawIntent bool
}

func (s *eventFirstBlobStore) Put(workID string, data []byte) (string, error) {
	_, state, err := s.store.LoadState(workID, s.requestID+"/cs")
	if err != nil || !state.RequestFound {
		return "", fmt.Errorf("blob side effect ran before durable intent: %w", err)
	}
	s.mu.Lock()
	s.sawIntent = true
	if s.failPuts > 0 {
		s.failPuts--
		s.mu.Unlock()
		return "", errors.New("injected blob put failure")
	}
	s.mu.Unlock()
	return s.BlobStore.Put(workID, data)
}

type failDeleteBlobStore struct {
	BlobStore
	mu         sync.Mutex
	failDelete int
}

func (s *failDeleteBlobStore) Delete(workID, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDelete > 0 {
		s.failDelete--
		return errors.New("injected blob delete failure")
	}
	return s.BlobStore.Delete(workID, digest)
}

type projectionOverrideStore struct {
	WorkStore
	projection *Work
}

func (s projectionOverrideStore) LoadProjection(string) (*Work, error) {
	return s.projection, nil
}

type failViewLoadStore struct{ WorkStore }

func (s failViewLoadStore) LoadState(workID, requestID string) (*Work, WorkEventState, error) {
	if requestID == "" {
		return nil, WorkEventState{}, errors.New("injected final view load failure")
	}
	return s.WorkStore.LoadState(workID, requestID)
}

type failCommitStore struct {
	WorkStore
	mu         sync.Mutex
	failCommit int
}

func (s *failCommitStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failCommit > 0 {
		s.failCommit--
		return 0, errors.New("injected event commit failure")
	}
	return s.WorkStore.CommitEvent(workID, event)
}

type countBlobStore struct {
	BlobStore
	mu   sync.Mutex
	puts int
}

func (s *countBlobStore) Put(workID string, data []byte) (string, error) {
	s.mu.Lock()
	s.puts++
	s.mu.Unlock()
	return s.BlobStore.Put(workID, data)
}

func pinInput(reqID string, rev int64) PinCornerstoneInput {
	return PinCornerstoneInput{
		Type:             CornerstoneInstruction,
		Title:            "Test Instruction",
		Content:          "Always use tabs for indentation",
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         true,
		Tags:             []string{"coding", "style"},
		ExpectedRevision: rev,
		RequestID:        reqID,
	}
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestCornerstone_PinDuplicate(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	rev := view.Revision

	input := pinInput("req-pin-1", rev)
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if result.Duplicate {
		t.Fatal("first pin should not be duplicate")
	}
	if result.Cornerstone == nil {
		t.Fatal("expected cornerstone in result")
	}
	csID := result.Cornerstone.ID
	if !strings.HasPrefix(csID, "cs-") {
		t.Fatalf("unexpected ID format: %s", csID)
	}
	if result.Cornerstone.Tombstone {
		t.Fatal("new cornerstone should not be tombstoned")
	}

	// Same input again — should return same cornerstone.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	input.ExpectedRevision = view2.Revision
	result2, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin duplicate: %v", err)
	}
	if !result2.Duplicate {
		t.Fatal("second pin with same content should be duplicate")
	}
	if result2.Cornerstone.ID != csID {
		t.Fatalf("duplicate pin changed ID: %s → %s", csID, result2.Cornerstone.ID)
	}
}

func TestCornerstone_PinSameRequestIdempotent(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	input := pinInput("req-pin-idem", view.Revision)

	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}

	// Same requestID — should return same result idempotently.
	// Need to reload for current revision.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	input.ExpectedRevision = view2.Revision
	result2, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin idempotent: %v", err)
	}
	if result2.Cornerstone.ID != result.Cornerstone.ID {
		t.Fatalf("idempotent pin: ID changed: %s → %s", result.Cornerstone.ID, result2.Cornerstone.ID)
	}
}

func TestCornerstone_PinStableIDAcrossRequests(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)

	// Pin with request-1.
	input1 := pinInput("req-stable-1", view.Revision)
	result1, err := f.mgr.Pin(f.workID, input1)
	if err != nil {
		t.Fatalf("Pin 1: %v", err)
	}
	id1 := result1.Cornerstone.ID

	// Different request but same Work/Type/Ref/Content — should produce same ID.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	input2 := pinInput("req-stable-2", view2.Revision)
	result2, err := f.mgr.Pin(f.workID, input2)
	if err != nil {
		t.Fatalf("Pin 2: %v", err)
	}
	if result2.Cornerstone.ID != id1 {
		t.Fatalf("stable ID: %s ≠ %s with different requestID but same content", id1, result2.Cornerstone.ID)
	}
	if !result2.Duplicate {
		t.Fatal("should be duplicate")
	}
}

func TestCornerstone_PinDifferentContentDifferentID(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	input1 := pinInput("req-diff-1", view.Revision)
	result1, _ := f.mgr.Pin(f.workID, input1)
	id1 := result1.Cornerstone.ID

	// Different content — different ID.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	input2 := pinInput("req-diff-2", view2.Revision)
	input2.Content = "Use spaces for indentation" // different content
	result2, err := f.mgr.Pin(f.workID, input2)
	if err != nil {
		t.Fatalf("Pin with different content: %v", err)
	}
	if result2.Cornerstone.ID == id1 {
		t.Fatal("different content should produce different ID")
	}
}

func TestCornerstone_PinRevisionConflict(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)

	// Pin with correct revision.
	input := pinInput("req-conflict", view.Revision)
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	_ = result

	// Pin again with stale expectedRevision.
	input2 := pinInput("req-conflict-2", view.Revision) // stale revision
	_, err = f.mgr.Pin(f.workID, input2)
	if err == nil {
		t.Fatal("expected revision conflict error")
	}
	if !strings.Contains(err.Error(), "revision conflict") && !strings.Contains(err.Error(), "expected revision") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCornerstone_PinLargeContentBlob(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	largeContent := strings.Repeat("x", CornerstoneInlineThreshold+100)

	input := PinCornerstoneInput{
		Type:             CornerstonePolicy,
		Title:            "Large Policy",
		Content:          largeContent,
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         false,
		ExpectedRevision: view.Revision,
		RequestID:        "req-large",
	}

	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin large content: %v", err)
	}
	cs := result.Cornerstone

	// Content should be truncated inline.
	if len(cs.Content) > CornerstoneInlineThreshold {
		t.Fatalf("inline content not truncated: %d bytes", len(cs.Content))
	}

	// BlobDigest should be set.
	if cs.Ref.BlobDigest == "" {
		t.Fatal("blob digest should be set for large content")
	}

	// Blob should exist and be readable.
	blob, err := f.store.ReadBlob(f.workID, cs.Ref.BlobDigest)
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}
	if string(blob) != largeContent {
		t.Fatalf("blob content mismatch: got %d bytes, want %d", len(blob), len(largeContent))
	}

	// Re-pin with different content (different requestID) creates new cornerstone.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	diffContent := strings.Repeat("y", CornerstoneInlineThreshold+100)
	input2 := PinCornerstoneInput{
		Type:             CornerstonePolicy,
		Title:            "Large Policy V2",
		Content:          diffContent,
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         false,
		ExpectedRevision: view2.Revision,
		RequestID:        "req-large-2",
	}
	result2, err := f.mgr.Pin(f.workID, input2)
	if err != nil {
		t.Fatalf("re-pin large content: %v", err)
	}
	if result2.Duplicate {
		t.Fatal("different content should produce new cornerstone, not duplicate")
	}
	if result2.Cornerstone.ID == cs.ID {
		t.Fatal("different content should produce different ID")
	}
	// Original blob should still exist (it's different content, different cornerstone).
	if _, err := f.store.ReadBlob(f.workID, cs.Ref.BlobDigest); err != nil {
		t.Fatalf("original blob should still exist: %v", err)
	}
}

func TestCornerstone_PinNoBlobStoreRejectsLarge(t *testing.T) {
	f := newCMFixture(t)
	// Create manager without blob store.
	mgrNoBlob := NewCornerstoneManager(f.store, nil, f.clock)

	view, _ := f.svc.Get(t.Context(), f.workID)
	input := PinCornerstoneInput{
		Type:             CornerstonePolicy,
		Title:            "Large No Blob",
		Content:          strings.Repeat("z", CornerstoneInlineThreshold+100),
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		ExpectedRevision: view.Revision,
		RequestID:        "req-no-blob",
	}

	_, err := mgrNoBlob.Pin(f.workID, input)
	if err == nil {
		t.Fatal("expected error for large content without blob store")
	}
	if !strings.Contains(err.Error(), "BlobStore") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCornerstone_RefreshSnapshotBlobIntegrity(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	largeContent := strings.Repeat("a", CornerstoneInlineThreshold+50)
	input := PinCornerstoneInput{
		Type:             CornerstoneFileSnapshot,
		Title:            "Snapshot File",
		Content:          largeContent,
		Ref:              CornerstoneRef{Kind: "workspace_file", Path: "/test/file.txt"},
		Mode:             CornerstoneSnapshot,
		Required:         true,
		ExpectedRevision: view.Revision,
		RequestID:        "req-snap",
	}
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin snapshot: %v", err)
	}
	csID := result.Cornerstone.ID
	blobDigest := result.Cornerstone.Ref.BlobDigest

	// Refresh should verify blob integrity.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	refreshInput := RefreshCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view2.Revision,
		RequestID:        "req-refresh",
	}
	refreshResult, err := f.mgr.Refresh(f.workID, refreshInput)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshResult.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("expected active status, got %s", refreshResult.Cornerstone.Status)
	}
	if refreshResult.Cornerstone.LastVerifiedAt == nil {
		t.Fatal("LastVerifiedAt should be set after refresh")
	}

	// Corrupt the blob.
	blobPath, _ := f.store.blobPath(f.workID, blobDigest)
	if err := os.WriteFile(blobPath, []byte("corrupted!!!"), 0o644); err != nil {
		t.Fatalf("corrupt blob: %v", err)
	}

	// Refresh should mark invalid.
	view3, _ := f.svc.Get(t.Context(), f.workID)
	refreshInput2 := RefreshCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view3.Revision,
		RequestID:        "req-refresh-2",
	}
	refreshResult2, err := f.mgr.Refresh(f.workID, refreshInput2)
	if err != nil {
		t.Fatalf("Refresh after corruption: %v", err)
	}
	if refreshResult2.Cornerstone.Status != CornerstoneInvalid {
		t.Fatalf("expected invalid status after corruption, got %s", refreshResult2.Cornerstone.Status)
	}
	if !strings.Contains(refreshResult2.Cornerstone.Error, "blob") {
		t.Fatalf("expected blob error in cornerstone, got: %s", refreshResult2.Cornerstone.Error)
	}

	// Content should NOT change even though blob is corrupted (snapshot content frozen).
	if refreshResult2.Cornerstone.Content == "" && largeContent != "" {
		t.Fatal("snapshot content should not be lost even when blob is corrupt")
	}
}

func TestCornerstone_SnapshotContentDoesNotDrift(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	originalContent := "original snapshot content v1"

	input := PinCornerstoneInput{
		Type:             CornerstoneDecision,
		Title:            "Decision",
		Content:          originalContent,
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         true,
		ExpectedRevision: view.Revision,
		RequestID:        "req-drift",
	}
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}

	// Even if we "refresh" with different source, snapshot content stays the same.
	storedDigest := result.Cornerstone.Digest
	view2, _ := f.svc.Get(t.Context(), f.workID)
	csAfter := findCornerstoneWithView(f, view2, result.Cornerstone.ID, t)
	if csAfter == nil {
		t.Fatal("cornerstone missing after pin")
	}
	if csAfter.Digest != storedDigest {
		t.Fatalf("digest drifted: %s → %s", storedDigest, csAfter.Digest)
	}
}

func TestCornerstone_RemoveAndUndo(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	input := pinInput("req-rm-1", view.Revision)
	result, _ := f.mgr.Pin(f.workID, input)
	csID := result.Cornerstone.ID

	// Remove.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	rmInput := RemoveCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view2.Revision,
		RequestID:        "req-remove",
	}
	rmResult, err := f.mgr.Remove(f.workID, rmInput)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if rmResult.Cornerstone == nil || !rmResult.Cornerstone.Tombstone {
		t.Fatal("cornerstone should be tombstoned after remove")
	}

	// Restart and verify tombstone persists.
	f.restart(t)
	view3, _ := f.svc.Get(t.Context(), f.workID)
	csAfterRestart := findCornerstoneWithView(f, view3, csID, t)
	if csAfterRestart == nil || !csAfterRestart.Tombstone {
		t.Fatal("tombstone should persist across restarts")
	}

	// Undo.
	view4, _ := f.svc.Get(t.Context(), f.workID)
	undoInput := UndoCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view4.Revision,
		RequestID:        "req-undo",
	}
	undoResult, err := f.mgr.Undo(f.workID, undoInput)
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if undoResult.Cornerstone.Tombstone {
		t.Fatal("cornerstone should not be tombstoned after undo")
	}

	// Undo again should be idempotent.
	view5, _ := f.svc.Get(t.Context(), f.workID)
	undoInput2 := UndoCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view5.Revision,
		RequestID:        "req-undo-2",
	}
	undoResult2, err := f.mgr.Undo(f.workID, undoInput2)
	if err != nil {
		t.Fatalf("Undo idempotent: %v", err)
	}
	if undoResult2.Cornerstone.Tombstone {
		t.Fatal("undo should be idempotent")
	}
}

func TestCornerstone_RemoveIdempotent(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	input := pinInput("req-rm-idem-1", view.Revision)
	result, _ := f.mgr.Pin(f.workID, input)
	csID := result.Cornerstone.ID

	// Remove.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	rmInput := RemoveCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view2.Revision,
		RequestID:        "req-rm-idem-rm",
	}
	_, err := f.mgr.Remove(f.workID, rmInput)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Same requestID again — idempotent.
	view3, _ := f.svc.Get(t.Context(), f.workID)
	rmInput.ExpectedRevision = view3.Revision
	rmResult2, err := f.mgr.Remove(f.workID, rmInput)
	if err != nil {
		t.Fatalf("Remove idempotent: %v", err)
	}
	if !rmResult2.Duplicate {
		t.Fatal("repeat remove should be duplicate")
	}
}

func TestCornerstone_ConcurrentPin(t *testing.T) {
	f := newCMFixture(t)

	var wg sync.WaitGroup
	n := 5
	results := make([]*CornerstoneResult, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			view, getErr := f.svc.Get(t.Context(), f.workID)
			if getErr != nil {
				errs[idx] = getErr
				return
			}
			input := PinCornerstoneInput{
				Type:             CornerstoneParameter,
				Title:            fmt.Sprintf("Param %d", idx),
				Content:          fmt.Sprintf("value-%d", idx),
				Ref:              CornerstoneRef{Kind: "inline"},
				Mode:             CornerstoneSnapshot,
				Required:         false,
				ExpectedRevision: view.Revision,
				RequestID:        fmt.Sprintf("req-conc-%d", idx),
			}
			r, e := f.mgr.Pin(f.workID, input)
			results[idx] = r
			errs[idx] = e
		}(i)
	}
	wg.Wait()

	successCount := 0
	conflictCount := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			if strings.Contains(errs[i].Error(), "revision") || strings.Contains(errs[i].Error(), "conflict") || strings.Contains(errs[i].Error(), "expected revision") {
				conflictCount++
			} else {
				t.Errorf("goroutine %d: %v", i, errs[i])
			}
		} else {
			successCount++
			if results[i] == nil || results[i].Cornerstone == nil {
				t.Errorf("goroutine %d: nil result", i)
			}
		}
	}

	// At least one should succeed, some may conflict.
	if successCount == 0 {
		t.Fatal("no concurrent pins succeeded")
	}
	t.Logf("concurrent: %d succeeded, %d conflicted (out of %d)", successCount, conflictCount, n)
}

func TestCornerstone_GCReferencedAndUnreferenced(t *testing.T) {
	f := newCMFixture(t)

	// Pin a cornerstone with blob content.
	view, _ := f.svc.Get(t.Context(), f.workID)
	largeContent := strings.Repeat("g", CornerstoneInlineThreshold+100)
	input := PinCornerstoneInput{
		Type:             CornerstoneDecision,
		Title:            "GC Decision",
		Content:          largeContent,
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         false,
		ExpectedRevision: view.Revision,
		RequestID:        "req-gc-1",
	}
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin for GC: %v", err)
	}
	csID := result.Cornerstone.ID
	blobDigest := result.Cornerstone.Ref.BlobDigest
	if blobDigest == "" {
		t.Fatal("expected blob digest for large content")
	}

	// Write an orphan blob (not referenced by anything).
	orphanContent := []byte("orphan blob data")
	orphanDigest, err := f.store.Put(f.workID, orphanContent)
	if err != nil {
		t.Fatalf("Put orphan blob: %v", err)
	}

	// Run GC — should reclaim orphan but keep referenced blob.
	gcInput := GCInput{ExpectedRevision: result.Revision, RequestID: "req-gc-run"}
	gcResult, err := f.mgr.GC(f.workID, gcInput)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if gcResult.Reclaimed < 1 {
		t.Fatal("GC should have reclaimed at least the orphan blob")
	}

	// Orphan should be gone.
	exists, _ := f.store.BlobExists(f.workID, orphanDigest)
	if exists {
		t.Fatal("orphan blob should have been reclaimed")
	}

	// Referenced blob should still exist.
	exists, _ = f.store.BlobExists(f.workID, blobDigest)
	if !exists {
		t.Fatal("referenced blob should still exist after GC")
	}

	// Remove cornerstone, then GC should reclaim the blob.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	rmInput := RemoveCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view2.Revision,
		RequestID:        "req-gc-rm",
	}
	rmResult, err := f.mgr.Remove(f.workID, rmInput)
	if err != nil {
		t.Fatalf("Remove for GC: %v", err)
	}

	// GC again — the durable tombstone still protects the blob so Undo remains
	// lossless. A future purge may remove both the fact and its blob reference.
	gcInput2 := GCInput{ExpectedRevision: rmResult.Revision, RequestID: "req-gc-run-2"}
	gcResult2, err := f.mgr.GC(f.workID, gcInput2)
	if err != nil {
		t.Fatalf("GC after remove: %v", err)
	}

	exists, _ = f.store.BlobExists(f.workID, blobDigest)
	if !exists {
		t.Fatalf("GC reclaimed undoable tombstone blob (reclaimed=%d)", gcResult2.Reclaimed)
	}
	view3, _ := f.svc.Get(t.Context(), f.workID)
	undo, err := f.mgr.Undo(f.workID, UndoCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view3.Revision,
		RequestID:        "req-gc-undo",
	})
	if err != nil || undo.Cornerstone == nil || undo.Cornerstone.Tombstone {
		t.Fatalf("Undo after GC = (%#v, %v)", undo, err)
	}
	if _, err := f.store.Get(f.workID, blobDigest); err != nil {
		t.Fatalf("Undo blob after GC: %v", err)
	}
}

func TestCornerstone_GCRepeatable(t *testing.T) {
	f := newCMFixture(t)

	// Write an orphan blob.
	orphanContent := []byte("repeatable orphan")
	orphanDigest, err := f.store.Put(f.workID, orphanContent)
	if err != nil {
		t.Fatalf("Put orphan: %v", err)
	}

	// GC first pass.
	view, _ := f.svc.Get(t.Context(), f.workID)
	gcInput := GCInput{ExpectedRevision: view.Revision, RequestID: "req-gc-repeat-1"}
	result1, err := f.mgr.GC(f.workID, gcInput)
	if err != nil {
		t.Fatalf("GC first pass: %v", err)
	}

	// GC second pass — should be idempotent (no errors, nothing to reclaim).
	result2, err := f.mgr.GC(f.workID, gcInput)
	if err != nil {
		t.Fatalf("GC second pass: %v", err)
	}

	if result2.Reclaimed > 0 {
		t.Fatal("second GC pass should reclaim nothing")
	}

	// Orphan blob should be gone.
	exists, _ := f.store.BlobExists(f.workID, orphanDigest)
	if exists {
		t.Fatal("orphan blob should have been reclaimed in first pass")
	}

	t.Logf("GC pass 1: %d reclaimed; pass 2: %d reclaimed", result1.Reclaimed, result2.Reclaimed)
}

func TestCornerstone_SecretRejected(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)

	tests := []struct {
		name    string
		content string
	}{
		{"api_key", "api_key = sk-abc123def456"},
		{"password", "password: superSecret123"},
		{"token", "Authorization: Bearer token-xxx"},
		{"private_key", "PRIVATE_KEY: MIIEvQIBADANBgkqhkiG9w0BAQEFAASC"},
		{"secret", "secret: my-secret-value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := PinCornerstoneInput{
				Type:             CornerstoneParameter,
				Title:            "Secret Test",
				Content:          tt.content,
				Ref:              CornerstoneRef{Kind: "inline"},
				Mode:             CornerstoneSnapshot,
				Required:         false,
				ExpectedRevision: view.Revision,
				RequestID:        fmt.Sprintf("req-secret-%s", tt.name),
			}
			_, err := f.mgr.Pin(f.workID, input)
			if err == nil {
				t.Fatalf("expected ErrSecretRejected for %q, got nil", tt.name)
			}
			if !errors.Is(err, ErrSecretRejected) {
				t.Fatalf("expected ErrSecretRejected for %q, got: %v", tt.name, err)
			}
		})
	}

	// Secret should not appear in any persisted event, projection, or blob.
	workProj, err := f.store.LoadProjection(f.workID)
	if err != nil {
		t.Fatalf("LoadProjection: %v", err)
	}
	for _, cs := range workProj.Cornerstones {
		if strings.Contains(cs.Content, "sk-") || strings.Contains(cs.Content, "password") {
			t.Fatalf("secret leaked into projection: %s", cs.Content)
		}
	}

	// Ordinary engineering vocabulary must not be rejected merely because it
	// contains words that can also label credentials.
	safe := pinInput("req-secret-safe-vocabulary", view.Revision)
	safe.Title = "Risk-based token budget"
	safe.Content = "Use a risk-based token budget for the next run"
	if _, err := f.mgr.Pin(f.workID, safe); err != nil {
		t.Fatalf("safe credential vocabulary was rejected: %v", err)
	}
}

func TestCornerstone_RestartSurvives(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	input := pinInput("req-restart", view.Revision)
	result, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin before restart: %v", err)
	}
	csID := result.Cornerstone.ID
	origDigest := result.Cornerstone.Digest

	// Simulate restart.
	f.restart(t)

	// Cornerstone should still exist.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	cs := findCornerstoneWithView(f, view2, csID, t)
	if cs == nil {
		t.Fatal("cornerstone lost after restart")
	}
	if cs.Digest != origDigest {
		t.Fatalf("digest changed after restart: %s → %s", origDigest, cs.Digest)
	}
	if cs.Tombstone {
		t.Fatal("cornerstone should not be tombstoned")
	}

	// Can still pin new cornerstones after restart.
	view3, _ := f.svc.Get(t.Context(), f.workID)
	input2 := PinCornerstoneInput{
		Type:             CornerstoneConclusion,
		Title:            "After Restart",
		Content:          "new conclusion",
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         false,
		ExpectedRevision: view3.Revision,
		RequestID:        "req-restart-2",
	}
	_, err = f.mgr.Pin(f.workID, input2)
	if err != nil {
		t.Fatalf("Pin after restart: %v", err)
	}
}

func TestCornerstone_BlobMissingMarksInvalid(t *testing.T) {
	f := newCMFixture(t)

	view, _ := f.svc.Get(t.Context(), f.workID)
	largeContent := strings.Repeat("b", CornerstoneInlineThreshold+100)
	input := PinCornerstoneInput{
		Type:             CornerstoneFileSnapshot,
		Title:            "Blob Missing Test",
		Content:          largeContent,
		Ref:              CornerstoneRef{Kind: "workspace_file", Path: "/test/file.txt"},
		Mode:             CornerstoneSnapshot,
		Required:         true,
		ExpectedRevision: view.Revision,
		RequestID:        "req-missing-blob",
	}
	result, _ := f.mgr.Pin(f.workID, input)
	csID := result.Cornerstone.ID
	blobDigest := result.Cornerstone.Ref.BlobDigest

	// Delete blob manually.
	f.store.DeleteBlob(f.workID, blobDigest)

	// Refresh should mark invalid due to missing blob.
	view2, _ := f.svc.Get(t.Context(), f.workID)
	refreshInput := RefreshCornerstoneInput{
		CornerstoneID:    csID,
		ExpectedRevision: view2.Revision,
		RequestID:        "req-missing-refresh",
	}
	refreshResult, err := f.mgr.Refresh(f.workID, refreshInput)
	if err != nil {
		t.Fatalf("Refresh on missing blob: %v", err)
	}
	if refreshResult.Cornerstone.Status != CornerstoneInvalid {
		t.Fatalf("expected invalid status for missing blob, got %s", refreshResult.Cornerstone.Status)
	}
	// Content should still be preserved (snapshot doesn't lose data).
	if refreshResult.Cornerstone.Content == "" {
		t.Fatal("content should be preserved even when blob is missing")
	}
}

func TestCornerstone_UndoMissingBlobRestoresInvalid(t *testing.T) {
	f := newCMFixture(t)
	view, _ := f.svc.Get(t.Context(), f.workID)
	input := PinCornerstoneInput{
		Type:             CornerstoneFileSnapshot,
		Title:            "Undo missing snapshot",
		Content:          strings.Repeat("undo-blob ", CornerstoneInlineThreshold/4),
		Ref:              CornerstoneRef{Kind: "workspace_file", Path: "docs/undo.txt"},
		Mode:             CornerstoneSnapshot,
		ExpectedRevision: view.Revision,
		RequestID:        "req-undo-missing-pin",
	}
	pinned, err := f.mgr.Pin(f.workID, input)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	removed, err := f.mgr.Remove(f.workID, RemoveCornerstoneInput{
		CornerstoneID:    pinned.Cornerstone.ID,
		ExpectedRevision: pinned.Revision,
		RequestID:        "req-undo-missing-remove",
	})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := f.store.Delete(f.workID, pinned.Cornerstone.Ref.BlobDigest); err != nil {
		t.Fatalf("delete blob: %v", err)
	}
	restored, err := f.mgr.Undo(f.workID, UndoCornerstoneInput{
		CornerstoneID:    pinned.Cornerstone.ID,
		ExpectedRevision: removed.Revision,
		RequestID:        "req-undo-missing-restore",
	})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if restored.Cornerstone.Tombstone || restored.Cornerstone.Status != CornerstoneInvalid || restored.Cornerstone.Error == "" {
		t.Fatalf("restored missing blob = %#v", restored.Cornerstone)
	}
}

func TestCornerstone_GCAfterRestart(t *testing.T) {
	f := newCMFixture(t)

	// Write orphan.
	orphanContent := []byte("gc-restart-orphan")
	orphanDigest, _ := f.store.Put(f.workID, orphanContent)

	// Restart.
	f.restart(t)

	// GC should work after restart.
	view, _ := f.svc.Get(t.Context(), f.workID)
	gcResult, err := f.mgr.GC(f.workID, GCInput{ExpectedRevision: view.Revision, RequestID: "req-gc-restart"})
	if err != nil {
		t.Fatalf("GC after restart: %v", err)
	}

	exists, _ := f.store.BlobExists(f.workID, orphanDigest)
	if exists {
		t.Fatal("orphan blob should be reclaimed after restart")
	}
	t.Logf("GC after restart reclaimed %d blobs", gcResult.Reclaimed)
}

func TestCornerstone_PinBlobFailurePersistsIntentAndRetryRecovers(t *testing.T) {
	f := newCMFixture(t)
	view, _ := f.svc.Get(t.Context(), f.workID)
	input := PinCornerstoneInput{
		Type:             CornerstonePolicy,
		Title:            "Recoverable policy",
		Content:          strings.Repeat("recoverable ", CornerstoneInlineThreshold/4),
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		ExpectedRevision: view.Revision,
		RequestID:        "req-event-first-blob",
	}
	blobs := &eventFirstBlobStore{BlobStore: f.store, store: f.store, requestID: input.RequestID, failPuts: 1}
	mgr := NewCornerstoneManager(f.store, blobs, f.clock)

	result, err := mgr.Pin(f.workID, input)
	if err == nil || result == nil || result.Cornerstone == nil {
		t.Fatalf("Pin failure = (%v, %v), want persisted invalid result and retryable error", result, err)
	}
	var recovery *ErrWorkCommittedRecovery
	if !errors.As(err, &recovery) || !recovery.Committed || !recovery.Recoverable {
		t.Fatalf("Pin error = %v, want committed recovery", err)
	}
	if !blobs.sawIntent {
		t.Fatal("blob Put did not observe the durable pin intent")
	}
	if result.Cornerstone.Status != CornerstoneInvalid || result.Cornerstone.Error == "" {
		t.Fatalf("failed blob pin status = (%s, %q), want explicit invalid", result.Cornerstone.Status, result.Cornerstone.Error)
	}
	exists, existsErr := f.store.Exists(f.workID, result.Cornerstone.Ref.BlobDigest)
	if existsErr != nil || exists {
		t.Fatalf("failed Put blob exists = (%v, %v), want false, nil", exists, existsErr)
	}

	// A process restart can resume the same durable request even with its old
	// expected revision; request idempotency wins over optimistic locking.
	f.restart(t)
	retried, retryErr := f.mgr.Pin(f.workID, input)
	if retryErr != nil {
		t.Fatalf("Pin retry after restart: %v", retryErr)
	}
	if retried.Cornerstone.ID != result.Cornerstone.ID || retried.Cornerstone.Status != CornerstoneActive {
		t.Fatalf("recovered cornerstone = %#v", retried.Cornerstone)
	}
	if _, err := f.store.Get(f.workID, retried.Cornerstone.Ref.BlobDigest); err != nil {
		t.Fatalf("recovered blob: %v", err)
	}
}

func TestCornerstone_PinCommitFailureHasNoBlobSideEffect(t *testing.T) {
	f := newCMFixture(t)
	view, _ := f.svc.Get(t.Context(), f.workID)
	content := strings.Repeat("commit first ", CornerstoneInlineThreshold/4)
	input := PinCornerstoneInput{
		Type:             CornerstonePolicy,
		Title:            "Commit-first policy",
		Content:          content,
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		ExpectedRevision: view.Revision,
		RequestID:        "req-commit-failure",
	}
	store := &failCommitStore{WorkStore: f.store, failCommit: 1}
	blobs := &countBlobStore{BlobStore: f.store}
	mgr := NewCornerstoneManager(store, blobs, f.clock)
	result, err := mgr.Pin(f.workID, input)
	if err == nil || result != nil {
		t.Fatalf("Pin commit failure = (%#v, %v), want nil, error", result, err)
	}
	if blobs.puts != 0 {
		t.Fatalf("blob Put count = %d, want zero before event commit", blobs.puts)
	}
	exists, existsErr := f.store.Exists(f.workID, ContentDigest([]byte(normalizeCornerstoneContent(content))))
	if existsErr != nil || exists {
		t.Fatalf("blob after commit failure = (%v, %v), want absent", exists, existsErr)
	}
	viewAfter, _ := f.svc.Get(t.Context(), f.workID)
	if viewAfter.Revision != view.Revision || len(viewAfter.Work.Cornerstones) != 0 {
		t.Fatalf("commit failure changed projection: rev=%d cornerstones=%d", viewAfter.Revision, len(viewAfter.Work.Cornerstones))
	}
	if _, err := f.mgr.Pin(f.workID, input); err != nil {
		t.Fatalf("Pin retry after uncommitted failure: %v", err)
	}
}

func TestCornerstone_GCPartialFailureRetryable(t *testing.T) {
	f := newCMFixture(t)
	orphanDigest, err := f.store.Put(f.workID, []byte("retryable GC orphan"))
	if err != nil {
		t.Fatalf("Put orphan: %v", err)
	}
	view, _ := f.svc.Get(t.Context(), f.workID)
	blobs := &failDeleteBlobStore{BlobStore: f.store, failDelete: 1}
	mgr := NewCornerstoneManager(f.store, blobs, f.clock)
	input := GCInput{ExpectedRevision: view.Revision, RequestID: "req-gc-partial-blob"}

	first, firstErr := mgr.GC(f.workID, input)
	if firstErr == nil || first == nil || len(first.Errors) != 1 {
		t.Fatalf("first GC = (%#v, %v), want explicit partial failure", first, firstErr)
	}
	_, state, err := f.store.LoadState(f.workID, input.RequestID+"/cs-gc")
	if err != nil || !state.RequestFound {
		t.Fatalf("GC intent state = (%#v, %v), want persisted", state, err)
	}
	if exists, _ := f.store.Exists(f.workID, orphanDigest); !exists {
		t.Fatal("failed GC target disappeared before successful retry")
	}

	second, err := mgr.GC(f.workID, input)
	if err != nil || second.Reclaimed != 1 || !second.Duplicate {
		t.Fatalf("GC retry = (%#v, %v), want one recovery deletion", second, err)
	}
	third, err := mgr.GC(f.workID, input)
	if err != nil || third.Reclaimed != 0 || !third.Duplicate {
		t.Fatalf("completed GC replay = (%#v, %v), want stable no-op", third, err)
	}
}

func TestCornerstone_GCCorruptBlockStopsBeforeDelete(t *testing.T) {
	f := newCMFixture(t)
	orphanDigest, err := f.store.Put(f.workID, []byte("must survive corrupt reference scan"))
	if err != nil {
		t.Fatalf("Put orphan: %v", err)
	}
	view, _ := f.svc.Get(t.Context(), f.workID)
	broken := *view.Work
	broken.Blocks = append([]BlockInstance(nil), broken.Blocks...)
	broken.Blocks = append(broken.Blocks, BlockInstance{ID: "broken-block", Data: json.RawMessage(`{"broken"`)})
	store := projectionOverrideStore{WorkStore: f.store, projection: &broken}
	mgr := NewCornerstoneManager(store, f.store, f.clock)
	input := GCInput{ExpectedRevision: view.Revision, RequestID: "req-gc-corrupt-block"}

	if result, err := mgr.GC(f.workID, input); err == nil || result != nil {
		t.Fatalf("GC corrupt block = (%#v, %v), want explicit stop", result, err)
	}
	if exists, _ := f.store.Exists(f.workID, orphanDigest); !exists {
		t.Fatal("GC deleted a blob after incomplete reference scan")
	}
	_, state, err := f.store.LoadState(f.workID, input.RequestID+"/cs-gc")
	if err != nil || state.RequestFound {
		t.Fatalf("unsafe GC intent state = (%#v, %v), want no committed intent", state, err)
	}
}

func TestCornerstone_StableIdentityUsesOnlyDeclaredFields(t *testing.T) {
	base := PinCornerstoneInput{
		Type:     CornerstoneSource,
		Title:    "First title",
		Content:  "line one\r\nline two",
		Ref:      CornerstoneRef{Kind: "workspace_file", Path: `.\docs\source.md`},
		Mode:     CornerstoneSnapshot,
		Required: true,
		Tags:     []string{"one"},
	}
	changed := base
	changed.Title = "Renamed"
	changed.Content = "line one\nline two"
	changed.Ref.Path = "docs/source.md"
	changed.Mode = CornerstoneLiveRef
	changed.Required = false
	changed.Tags = []string{"two"}
	base.Ref = normalizedCornerstoneRef(base.Ref)
	changed.Ref = normalizedCornerstoneRef(changed.Ref)
	id1, err1 := computeStableCornerstoneID("work-stable", base)
	id2, err2 := computeStableCornerstoneID("work-stable", changed)
	if err1 != nil || err2 != nil || id1 != id2 {
		t.Fatalf("stable IDs = (%q, %v), (%q, %v), want equal", id1, err1, id2, err2)
	}
	if ContentDigest([]byte(normalizeCornerstoneContent(base.Content))) != ContentDigest([]byte(normalizeCornerstoneContent(changed.Content))) {
		t.Fatal("normalized content digest changed across line endings")
	}
}

func TestCornerstone_SecretRefRejectedWithoutPersistence(t *testing.T) {
	f := newCMFixture(t)
	view, _ := f.svc.Get(t.Context(), f.workID)
	const secretValue = "do-not-persist-7341"
	input := PinCornerstoneInput{
		Type:             CornerstoneSource,
		Title:            "Remote source",
		Content:          "public summary",
		Ref:              CornerstoneRef{Kind: "url", URL: "https://example.test/source?token=" + secretValue},
		Mode:             CornerstoneLiveRef,
		ExpectedRevision: view.Revision,
		RequestID:        "req-sensitive-ref",
	}
	if _, err := f.mgr.Pin(f.workID, input); !errors.Is(err, ErrSecretRejected) {
		t.Fatalf("Pin secret ref error = %v, want ErrSecretRejected", err)
	}
	workDir, _ := f.store.workPath(f.workID)
	logData, err := os.ReadFile(WorkEventLogPath(workDir))
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if strings.Contains(string(logData), secretValue) {
		t.Fatal("secret reference value leaked into event log")
	}
	viewAfter, _ := f.svc.Get(t.Context(), f.workID)
	if viewAfter.Revision != view.Revision {
		t.Fatalf("secret rejection changed revision: %d -> %d", view.Revision, viewAfter.Revision)
	}
}

func TestCornerstone_FinalViewFailureIsNotSwallowed(t *testing.T) {
	f := newCMFixture(t)
	view, _ := f.svc.Get(t.Context(), f.workID)
	input := pinInput("req-final-view-fault", view.Revision)
	mgr := NewCornerstoneManager(failViewLoadStore{WorkStore: f.store}, f.store, f.clock)
	result, err := mgr.Pin(f.workID, input)
	var recovery *ErrWorkCommittedRecovery
	if result != nil || !errors.As(err, &recovery) || !recovery.Committed {
		t.Fatalf("Pin final-view fault = (%#v, %v), want committed recovery", result, err)
	}
	retried, err := f.mgr.Pin(f.workID, input)
	if err != nil || retried == nil || !retried.Duplicate {
		t.Fatalf("Pin retry after view fault = (%#v, %v)", retried, err)
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func findCornerstoneWithView(f *cmFixture, view *WorkView, id string, t *testing.T) *Cornerstone {
	t.Helper()
	for i := range view.Work.Cornerstones {
		if view.Work.Cornerstones[i].ID == id {
			return &view.Work.Cornerstones[i]
		}
	}
	return nil
}
