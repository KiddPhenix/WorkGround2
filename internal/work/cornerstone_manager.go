package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── CornerstoneManager ──────────────────────────────────────────────────────

// CornerstoneManager coordinates Cornerstone lifecycle operations: Pin,
// Refresh, Remove, Undo, blob GC, Resolve, Accept, Freeze, and Repair.
// It owns no state; it reads the authoritative Work event log and writes
// typed mutation events through the WorkStore.
type CornerstoneManager struct {
	store     WorkStore
	blobStore BlobStore
	clock     Clock
	resolver  CornerstoneResolver

	inflightMu sync.Mutex
	inflight   map[string]*resolveCall
}

type resolveCall struct {
	done    chan struct{}
	result  ResolveResult
	err     error
	waiters int
}

// NewCornerstoneManager creates a CornerstoneManager. A nil blobStore disables
// large-content blob storage — content above the inline threshold is rejected.
// A nil resolver disables live_ref resolution for Refresh/Resolve — callers
// must provide one to use those features.
func NewCornerstoneManager(store WorkStore, blobStore BlobStore, clock Clock) *CornerstoneManager {
	if clock == nil {
		clock = RealClock{}
	}
	return &CornerstoneManager{
		store:     store,
		blobStore: blobStore,
		clock:     clock,
		inflight:  make(map[string]*resolveCall),
	}
}

// SetResolver sets the CornerstoneResolver used by Resolve, Accept, Freeze,
// and Repair. Callers configure it before concurrent resolve operations.
func (m *CornerstoneManager) SetResolver(r CornerstoneResolver) {
	m.resolver = r
}

// ── Pin ─────────────────────────────────────────────────────────────────────

// Pin creates or updates a Cornerstone. Same Work + Type + Ref + Content
// produces the same stable ID. Duplicate pins return the existing Cornerstone
// with Duplicate=true.
//
// Content larger than CornerstoneInlineThreshold is written to the BlobStore
// and referenced via Ref.BlobDigest; the Content field is truncated to a
// preview. If no BlobStore is configured, large content is rejected.
func (m *CornerstoneManager) Pin(workID string, input PinCornerstoneInput) (*CornerstoneResult, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, errors.New("cornerstone: Pin: workID is required")
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return nil, errors.New("cornerstone: Pin: requestID is required")
	}
	input.RequestID = requestID
	input.Title = strings.TrimSpace(input.Title)
	input.Content = normalizeCornerstoneContent(input.Content)
	input.Ref = normalizedCornerstoneRef(input.Ref)
	if cornerstoneInputSecretLike(input) {
		return nil, ErrSecretRejected
	}
	if err := validateCornerstoneInput(input); err != nil {
		return nil, err
	}

	// Compute stable ID from identity fields.
	stableID, err := computeStableCornerstoneID(workID, input)
	if err != nil {
		return nil, err
	}

	content := input.Content
	blobDigest := ""
	if len(content) > CornerstoneInlineThreshold {
		if m.blobStore == nil {
			return nil, fmt.Errorf("cornerstone: Pin: content (%d bytes) exceeds inline threshold (%d) but no BlobStore is configured", len(content), CornerstoneInlineThreshold)
		}
		blobDigest = ContentDigest([]byte(content))
		content = truncateContentPreview(content)
	}

	// Compute content digest.
	contentDigest := ContentDigest([]byte(input.Content))

	eventRequestID := requestID + "/cs"
	eventID := cornerstoneEventID("cs", eventRequestID, stableID)
	current, state, err := m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		if err := validateCornerstoneReplay("Pin", workID, requestID, state, EventCornerstoneUpserted, eventID); err != nil {
			return nil, err
		}
		cs := findCornerstone(current, stableID)
		if cs == nil {
			return nil, fmt.Errorf("%w: cornerstone: Pin request %q committed for a different object", ErrWorkRequestIDConflict, requestID)
		}
		if blobDigest != "" {
			return m.finalizeBlobPin(workID, requestID, stableID, input.Content, true)
		}
		return &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("cornerstone: Pin: Work %s is %s", workID, current.ArchiveState)
	}

	if input.ExpectedRevision != state.Revision {
		return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}

	// Check for duplicate by identity (same Work + Type + Ref + Content).
	if existing := findCornerstone(current, stableID); existing != nil {
		if existing.Tombstone {
			return nil, fmt.Errorf("cornerstone: Pin: cornerstone %q is removed; use Undo to restore it", stableID)
		}
		return &CornerstoneResult{Cornerstone: existing, WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
	}

	now := m.clock.Now().UTC()
	ref := input.Ref
	if blobDigest != "" {
		ref.BlobDigest = blobDigest
	}

	status := CornerstoneActive
	statusErr := ""
	if blobDigest != "" {
		// Persist the reference first. A crash or failed blob write leaves an
		// explicit invalid state that the same request can safely resume.
		status = CornerstoneInvalid
		statusErr = "snapshot blob is not materialized"
	}
	cs := Cornerstone{
		ID:         stableID,
		WorkID:     workID,
		Type:       input.Type,
		Title:      input.Title,
		Content:    content,
		Ref:        ref,
		Mode:       input.Mode,
		Digest:     contentDigest,
		Required:   input.Required,
		Status:     status,
		Tags:       cloneTags(input.Tags),
		Provenance: cornerstoneProvenance(workID, input.Ref),
		PinnedAt:   now,
		UpdatedAt:  now,
		Error:      statusErr,
		Tombstone:  false,
	}

	payload, err := json.Marshal(cs)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: Pin: marshal cornerstone: %w", err)
	}

	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            eventID,
		RequestID:     eventRequestID,
		WorkID:        workID,
		Type:          EventCornerstoneUpserted,
		BaseRevision:  state.Revision,
		Revision:      state.Revision + 1,
		Payload:       json.RawMessage(payload),
		WriterID:      WorkWriterID(),
		CreatedAt:     now,
	}

	revision, err := m.store.CommitEvent(workID, event)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: Pin: commit: %w", err)
	}
	if blobDigest != "" {
		return m.finalizeBlobPin(workID, requestID, stableID, input.Content, false)
	}

	view, err := m.loadView(workID)
	if err != nil {
		return nil, committedRecovery("cornerstone-pin-view", workID, requestID, revision, err)
	}

	result := findCornerstone(view.Work, stableID)
	if result == nil {
		return nil, fmt.Errorf("cornerstone: Pin: cornerstone %q disappeared from projection", stableID)
	}
	return &CornerstoneResult{Cornerstone: result, WorkView: view, Revision: view.Revision}, nil
}

func (m *CornerstoneManager) finalizeBlobPin(workID, requestID, stableID, content string, duplicate bool) (*CornerstoneResult, error) {
	finalRequestID := requestID + "/cs-blob"
	finalEventID := cornerstoneEventID("cs-blob", finalRequestID, stableID)
	current, state, err := m.store.LoadState(workID, finalRequestID)
	if err != nil {
		return nil, err
	}
	cs := findCornerstone(current, stableID)
	if cs == nil {
		return nil, fmt.Errorf("cornerstone: Pin: committed cornerstone %q missing before blob recovery", stableID)
	}
	if cs.Tombstone {
		return &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: duplicate, Revision: state.Revision},
			fmt.Errorf("cornerstone: Pin: cornerstone %q was removed before blob recovery", stableID)
	}
	digest := ContentDigest([]byte(content))
	if cs.Ref.BlobDigest != digest || cs.Digest != digest {
		return nil, fmt.Errorf("%w: cornerstone: Pin blob identity changed for %q", ErrWorkRequestIDConflict, stableID)
	}
	storedDigest, putErr := m.blobStore.Put(workID, []byte(content))
	if putErr != nil {
		result := &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: duplicate, Revision: state.Revision}
		return result, committedRecovery("cornerstone-pin-blob", workID, requestID, state.Revision, putErr)
	}
	if storedDigest != digest {
		mismatch := fmt.Errorf("cornerstone: Pin: blob store returned digest %q, want %q", storedDigest, digest)
		result := &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: duplicate, Revision: state.Revision}
		return result, committedRecovery("cornerstone-pin-blob", workID, requestID, state.Revision, mismatch)
	}
	if state.RequestFound {
		if err := validateCornerstoneReplay("Pin", workID, requestID, state, EventCornerstoneUpserted, finalEventID); err != nil {
			return nil, err
		}
		return &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
	}

	// Reload after the side effect so a concurrent remove cannot be undone by
	// the internal finalization event.
	current, state, err = m.store.LoadState(workID, finalRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		if err := validateCornerstoneReplay("Pin", workID, requestID, state, EventCornerstoneUpserted, finalEventID); err != nil {
			return nil, err
		}
		cs = findCornerstone(current, stableID)
		return &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
	}
	cs = findCornerstone(current, stableID)
	if cs == nil || cs.Tombstone {
		return nil, fmt.Errorf("cornerstone: Pin: cornerstone %q unavailable after blob write", stableID)
	}
	updated := *cs
	updated.Status = CornerstoneActive
	updated.Error = ""
	updated.UpdatedAt = m.clock.Now().UTC()
	event, err := cornerstoneUpsertEvent(workID, finalRequestID, state.Revision, updated, updated.UpdatedAt, "cs-blob")
	if err != nil {
		return nil, err
	}
	revision, err := m.store.CommitEvent(workID, event)
	if err != nil {
		latest, latestState, loadErr := m.store.LoadState(workID, finalRequestID)
		if loadErr == nil && latestState.RequestFound {
			if replayErr := validateCornerstoneReplay("Pin", workID, requestID, latestState, EventCornerstoneUpserted, finalEventID); replayErr != nil {
				return nil, replayErr
			}
			result := findCornerstone(latest, stableID)
			return &CornerstoneResult{Cornerstone: result, WorkView: viewFromState(latest, latestState), Duplicate: true, Revision: latestState.Revision}, nil
		}
		return nil, fmt.Errorf("cornerstone: Pin: finalize blob: %w", errors.Join(err, loadErr))
	}
	view, err := m.loadView(workID)
	if err != nil {
		return nil, committedRecovery("cornerstone-pin-blob-view", workID, requestID, revision, err)
	}
	return &CornerstoneResult{Cornerstone: findCornerstone(view.Work, stableID), WorkView: view, Duplicate: duplicate, Revision: view.Revision}, nil
}

// ── Refresh ─────────────────────────────────────────────────────────────────

// Refresh re-resolves live references and verifies snapshot blobs. The ctx is
// propagated to the resolver for cancellation and timeout control.
func (m *CornerstoneManager) Refresh(ctx context.Context, workID string, input RefreshCornerstoneInput) (*CornerstoneResult, error) {
	return m.Resolve(ctx, workID, input)
}

// ── Remove ──────────────────────────────────────────────────────────────────

// Remove writes a tombstone event for a Cornerstone. The cornerstone is not
// deleted from the projection; it is marked tombstoned and can be restored
// via Undo.
func (m *CornerstoneManager) Remove(workID string, input RemoveCornerstoneInput) (*CornerstoneResult, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, errors.New("cornerstone: Remove: workID is required")
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return nil, errors.New("cornerstone: Remove: requestID is required")
	}
	csID := strings.TrimSpace(input.CornerstoneID)
	if csID == "" {
		return nil, errors.New("cornerstone: Remove: cornerstoneId is required")
	}

	eventRequestID := requestID + "/cs-remove"
	eventID := cornerstoneEventID("cs-remove", eventRequestID, csID)
	current, state, err := m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		if err := validateCornerstoneReplay("Remove", workID, requestID, state, EventCornerstoneRemoved, eventID); err != nil {
			return nil, err
		}
		cs := findCornerstone(current, csID)
		if cs != nil {
			return &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
		}
		// Cornerstone already gone — idempotent success.
		return &CornerstoneResult{WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("cornerstone: Remove: Work %s is %s", workID, current.ArchiveState)
	}

	if input.ExpectedRevision != state.Revision {
		return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}

	cs := findCornerstone(current, csID)
	if cs == nil || cs.Tombstone {
		// Already removed — idempotent.
		return &CornerstoneResult{WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
	}

	now := m.clock.Now().UTC()
	payload, err := json.Marshal(struct {
		CornerstoneID string `json:"cornerstoneId"`
	}{CornerstoneID: csID})
	if err != nil {
		return nil, fmt.Errorf("cornerstone: Remove: marshal payload: %w", err)
	}

	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            eventID,
		RequestID:     eventRequestID,
		WorkID:        workID,
		Type:          EventCornerstoneRemoved,
		BaseRevision:  state.Revision,
		Revision:      state.Revision + 1,
		Payload:       json.RawMessage(payload),
		WriterID:      WorkWriterID(),
		CreatedAt:     now,
	}

	revision, err := m.store.CommitEvent(workID, event)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: Remove: commit: %w", err)
	}

	view, err := m.loadView(workID)
	if err != nil {
		return nil, committedRecovery("cornerstone-remove-view", workID, requestID, revision, err)
	}
	result := findCornerstone(view.Work, csID)
	return &CornerstoneResult{Cornerstone: result, WorkView: view, Revision: view.Revision}, nil
}

// ── Undo ────────────────────────────────────────────────────────────────────

// Undo restores a tombstoned Cornerstone by writing a restore event.
func (m *CornerstoneManager) Undo(workID string, input UndoCornerstoneInput) (*CornerstoneResult, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, errors.New("cornerstone: Undo: workID is required")
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return nil, errors.New("cornerstone: Undo: requestID is required")
	}
	csID := strings.TrimSpace(input.CornerstoneID)
	if csID == "" {
		return nil, errors.New("cornerstone: Undo: cornerstoneId is required")
	}

	eventRequestID := requestID + "/cs-undo"
	eventID := cornerstoneEventID("cs-restore", eventRequestID, csID)
	current, state, err := m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		if err := validateCornerstoneReplay("Undo", workID, requestID, state, EventCornerstoneRestored, eventID); err != nil {
			return nil, err
		}
		cs := findCornerstone(current, csID)
		if cs != nil {
			return &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
		}
		return nil, fmt.Errorf("cornerstone: Undo: request %q committed but cornerstone %q missing", requestID, csID)
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("cornerstone: Undo: Work %s is %s", workID, current.ArchiveState)
	}

	if input.ExpectedRevision != state.Revision {
		return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}

	cs := findCornerstone(current, csID)
	if cs == nil {
		return nil, fmt.Errorf("cornerstone: Undo: cornerstone %q not found", csID)
	}
	if !cs.Tombstone {
		// Already active — idempotent.
		return &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}, nil
	}

	now := m.clock.Now().UTC()
	status := cs.Status
	statusErr := cs.Error
	var verifiedAt *time.Time
	if cs.Mode == CornerstoneSnapshot && cs.Ref.BlobDigest != "" {
		verifiedAt = &now
		if err := m.verifyBlobIntegrity(workID, cs.Ref.BlobDigest); err != nil {
			status = CornerstoneInvalid
			statusErr = fmt.Sprintf("blob verification failed: %v", err)
		} else {
			status = CornerstoneActive
			statusErr = ""
		}
	}
	payload, err := json.Marshal(cornerstoneRestorePayload{
		CornerstoneID:  csID,
		Status:         status,
		Error:          statusErr,
		LastVerifiedAt: verifiedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("cornerstone: Undo: marshal payload: %w", err)
	}

	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            eventID,
		RequestID:     eventRequestID,
		WorkID:        workID,
		Type:          EventCornerstoneRestored,
		BaseRevision:  state.Revision,
		Revision:      state.Revision + 1,
		Payload:       json.RawMessage(payload),
		WriterID:      WorkWriterID(),
		CreatedAt:     now,
	}

	revision, err := m.store.CommitEvent(workID, event)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: Undo: commit: %w", err)
	}

	view, err := m.loadView(workID)
	if err != nil {
		return nil, committedRecovery("cornerstone-undo-view", workID, requestID, revision, err)
	}
	result := findCornerstone(view.Work, csID)
	return &CornerstoneResult{Cornerstone: result, WorkView: view, Revision: view.Revision}, nil
}

// ── GC ──────────────────────────────────────────────────────────────────────

// GC walks all Work projections, WorkRecords, Cornerstone refs, and Block refs
// to collect the set of referenced blob digests, then deletes any blob not in
// that set. GC is repeatable and idempotent; partial failures are reported per
// blob and do not block progress on other blobs.
func (m *CornerstoneManager) GC(workID string, input GCInput) (*GCResult, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, errors.New("cornerstone: GC: workID is required")
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return nil, errors.New("cornerstone: GC: requestID is required")
	}

	eventRequestID := requestID + "/cs-gc"
	eventID := cornerstoneEventID("cs-gc", eventRequestID, workID)
	_, state, err := m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	duplicate := state.RequestFound
	if duplicate {
		if err := validateCornerstoneReplay("GC", workID, requestID, state, EventCornerstoneGC, eventID); err != nil {
			return nil, err
		}
	}
	if !duplicate && input.ExpectedRevision != state.Revision {
		return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}

	referenced, err := m.collectReferencedDigests(workID)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: GC: collect references: %w", err)
	}
	allDigests := []string(nil)
	if m.blobStore != nil {
		allDigests, err = m.blobStore.ListDigests(workID)
		if err != nil {
			return nil, fmt.Errorf("cornerstone: GC: list blobs: %w", err)
		}
	}
	refSet := digestSet(referenced)
	targets := make([]string, 0, len(allDigests))
	for _, digest := range allDigests {
		if !refSet[digest] {
			targets = append(targets, digest)
		}
	}
	sort.Strings(targets)

	if !duplicate {
		now := m.clock.Now().UTC()
		payload, marshalErr := json.Marshal(struct {
			Targets []string `json:"targets"`
		}{Targets: targets})
		if marshalErr != nil {
			return nil, fmt.Errorf("cornerstone: GC: marshal intent: %w", marshalErr)
		}
		event := WorkEvent{
			SchemaVersion: WorkEventSchemaVersion,
			ID:            eventID,
			RequestID:     eventRequestID,
			WorkID:        workID,
			Type:          EventCornerstoneGC,
			BaseRevision:  state.Revision,
			Revision:      state.Revision + 1,
			Payload:       payload,
			WriterID:      WorkWriterID(),
			CreatedAt:     now,
		}
		revision, err := m.store.CommitEvent(workID, event)
		if err != nil {
			return nil, fmt.Errorf("cornerstone: GC: persist intent: %w", err)
		}
		state.Revision = revision
	}

	result := &GCResult{
		WorkID:     workID,
		TotalBlobs: len(allDigests),
		Referenced: len(referenced),
		Duplicate:  duplicate,
		Revision:   state.Revision,
	}
	var failures []error
	for _, digest := range targets {
		// Reconcile immediately before each deletion. A pin committed after the
		// GC intent therefore protects its blob on a retry or concurrent pass.
		latestRefs, refErr := m.collectReferencedDigests(workID)
		if refErr != nil {
			failures = append(failures, fmt.Errorf("recheck %s: %w", digest, refErr))
			continue
		}
		if digestSet(latestRefs)[digest] {
			continue
		}
		if err := m.blobStore.Delete(workID, digest); err != nil {
			failures = append(failures, fmt.Errorf("delete %s: %w", digest, err))
			continue
		}
		result.Reclaimed++
		result.ReclaimedKeys = append(result.ReclaimedKeys, digest)
	}
	for _, failure := range failures {
		result.Errors = append(result.Errors, failure.Error())
	}
	sort.Strings(result.ReclaimedKeys)
	return result, errors.Join(failures...)
}

// collectReferencedDigests scans the Work projection and WorkRecord for all
// blob digests referenced by Cornerstones, Blocks, and artifacts.
func (m *CornerstoneManager) collectReferencedDigests(workID string) ([]string, error) {
	seen := make(map[string]bool)

	// Load current projection.
	work, err := m.store.LoadProjection(workID)
	if err != nil {
		if errors.Is(err, ErrWorkNotFound) {
			return nil, nil
		}
		return nil, err
	}

	// Tombstones remain durable, undoable references. GC must preserve their
	// blobs until a future purge removes the owning object from all facts.
	for _, cs := range work.Cornerstones {
		if cs.Ref.BlobDigest != "" {
			seen[cs.Ref.BlobDigest] = true
		}
	}

	// Block blob refs (via ArtifactRef in block data or fallback).
	for _, block := range work.Blocks {
		if err := collectDigestsFromBlock(block, seen); err != nil {
			return nil, fmt.Errorf("work block %s: %w", block.ID, err)
		}
	}

	// WorkRecord archive snapshot.
	record, err := m.store.LoadArchive(workID)
	if err != nil && !errors.Is(err, ErrWorkNotFound) {
		return nil, err
	}
	if record != nil {
		for _, cs := range record.Snapshot.Cornerstones {
			if cs.Ref.BlobDigest != "" {
				seen[cs.Ref.BlobDigest] = true
			}
		}
		for _, block := range record.Snapshot.Blocks {
			if err := collectDigestsFromBlock(block, seen); err != nil {
				return nil, fmt.Errorf("archived block %s: %w", block.ID, err)
			}
		}
	}

	digests := make([]string, 0, len(seen))
	for d := range seen {
		digests = append(digests, d)
	}
	sort.Strings(digests)
	return digests, nil
}

// collectDigestsFromBlock scans a BlockInstance's data and actions for blob
// digest references.
func collectDigestsFromBlock(block BlockInstance, seen map[string]bool) error {
	// Scan raw data for any blobDigest field.
	if err := collectDigestsFromJSON(block.Data, seen); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if block.Fallback.Data != nil {
		if err := collectDigestsFromJSON(block.Fallback.Data, seen); err != nil {
			return fmt.Errorf("fallback: %w", err)
		}
	}
	// Actions may reference artifacts with blob digests.
	for _, action := range block.Actions {
		if err := collectDigestsFromJSON(action.Payload, seen); err != nil {
			return fmt.Errorf("action %s: %w", action.ID, err)
		}
	}
	return nil
}

// collectDigestsFromJSON walks a raw JSON message looking for "blobDigest",
// "digest" (prefixed with sha256:), and "BlobDigest" string fields.
func collectDigestsFromJSON(raw json.RawMessage, seen map[string]bool) error {
	if len(raw) == 0 {
		return nil
	}
	// Use a generic decode to find digest-like fields.
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	extractDigests(v, seen)
	return nil
}

func extractDigests(v any, seen map[string]bool) {
	switch val := v.(type) {
	case map[string]any:
		for k, vv := range val {
			if k == "blobDigest" || k == "digest" || k == "BlobDigest" {
				if s, ok := vv.(string); ok && strings.HasPrefix(s, digestPrefix) {
					seen[s] = true
				}
			}
			extractDigests(vv, seen)
		}
	case []any:
		for _, vv := range val {
			extractDigests(vv, seen)
		}
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// computeStableCornerstoneID produces a stable ID from the identity fields of
// a pin request: WorkID, Type, normalized Ref, and Content. Same input always
// produces the same ID; different input always produces a different ID.
func computeStableCornerstoneID(workID string, input PinCornerstoneInput) (string, error) {
	identity, err := canonicalCornerstoneInput(CornerstoneInput{
		Type:    input.Type,
		Content: input.Content,
		Ref:     input.Ref,
	})
	if err != nil {
		return "", fmt.Errorf("cornerstone: compute stable ID: %w", err)
	}
	h := sha256.Sum256(append([]byte(workID+"\x00"), identity...))
	return "cs-" + fmt.Sprintf("%x", h[:])[:16], nil
}

// findCornerstone finds a cornerstone by ID in a Work projection.
func findCornerstone(w *Work, id string) *Cornerstone {
	if w == nil {
		return nil
	}
	for i := range w.Cornerstones {
		if w.Cornerstones[i].ID == id {
			return &w.Cornerstones[i]
		}
	}
	return nil
}

// verifyBlobIntegrity checks that a blob exists and its content matches its digest.
func (m *CornerstoneManager) verifyBlobIntegrity(workID, digest string) error {
	if m.blobStore == nil {
		return errors.New("no blob store configured")
	}
	exists, err := m.blobStore.Exists(workID, digest)
	if err != nil {
		return fmt.Errorf("blob check failed: %w", err)
	}
	if !exists {
		return errors.New("blob not found")
	}
	return nil
}

// truncateContentPreview returns a short prefix of large content for inline display.
func truncateContentPreview(content string) string {
	const maxPreview = 256
	runes := []rune(content)
	if len(runes) <= maxPreview {
		return content
	}
	return string(runes[:maxPreview]) + "...[truncated]"
}

// cloneTags returns a shallow copy of a string slice.
func cloneTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, len(tags))
	copy(out, tags)
	return out
}

// loadView loads the current Work projection and revision.
func (m *CornerstoneManager) loadView(workID string) (*WorkView, error) {
	w, state, err := m.store.LoadState(workID, "")
	if err != nil {
		return nil, err
	}
	return viewFromState(w, state), nil
}

func normalizedCornerstoneRef(ref CornerstoneRef) CornerstoneRef {
	ref.Kind = strings.TrimSpace(ref.Kind)
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.Path = strings.TrimSpace(ref.Path)
	ref.ArtifactID = strings.TrimSpace(ref.ArtifactID)
	ref.URL = strings.TrimSpace(ref.URL)
	ref.BlobDigest = strings.TrimSpace(ref.BlobDigest)
	if ref.Kind == "workspace_file" && ref.Path != "" {
		ref.Path = normalizeCornerstonePath(ref.Path)
	}
	if ref.Kind == "url" && ref.URL != "" {
		if parsed, err := url.Parse(ref.URL); err == nil {
			parsed.Scheme = strings.ToLower(parsed.Scheme)
			parsed.Host = strings.ToLower(parsed.Host)
			parsed.RawQuery = parsed.Query().Encode()
			ref.URL = parsed.String()
		}
	}
	return ref
}

// normalizeCornerstonePath canonicalizes workspace refs without consulting the
// host OS. Slash-relative and POSIX paths remain case-sensitive; explicit
// Windows drive and UNC paths use Windows case-insensitive identity rules.
func normalizeCornerstonePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, `\`, "/")
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "//?/unc/"):
		value = "//" + value[len("//?/UNC/"):]
	case strings.HasPrefix(lower, "//?/") && isDrivePath(value[len("//?/"):]):
		value = value[len("//?/"):]
	}
	if isDrivePath(value) {
		return normalizeDrivePath(value)
	}
	if strings.HasPrefix(value, "//") && !strings.HasPrefix(value, "///") {
		return normalizeUNCPath(value)
	}
	return path.Clean(value)
}

func isDrivePath(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	c := value[0]
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func normalizeDrivePath(value string) string {
	drive := strings.ToLower(value[:2])
	rest := value[2:]
	if strings.HasPrefix(rest, "/") {
		cleaned := path.Clean("/" + strings.TrimLeft(rest, "/"))
		if cleaned == "/" {
			return drive + "/"
		}
		return strings.ToLower(drive + cleaned)
	}
	cleaned := path.Clean(rest)
	if cleaned == "." {
		return drive
	}
	return strings.ToLower(drive + cleaned)
}

func normalizeUNCPath(value string) string {
	parts := strings.FieldsFunc(strings.TrimLeft(value, "/"), func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return "//"
	}
	rootLen := min(2, len(parts))
	stack := append([]string(nil), parts[:rootLen]...)
	for _, part := range parts[rootLen:] {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) > rootLen {
				stack = stack[:len(stack)-1]
			}
		default:
			stack = append(stack, part)
		}
	}
	return "//" + strings.ToLower(strings.Join(stack, "/"))
}

func validateCornerstoneInput(input PinCornerstoneInput) error {
	if input.Title == "" {
		return errors.New("cornerstone: Pin: title is required")
	}
	switch input.Type {
	case CornerstoneInstruction, CornerstoneFileRef, CornerstoneFileSnapshot,
		CornerstoneDecision, CornerstoneConclusion, CornerstoneSource,
		CornerstonePolicy, CornerstoneParameter:
	default:
		return fmt.Errorf("cornerstone: Pin: unsupported type %q", input.Type)
	}
	switch input.Mode {
	case CornerstoneLiveRef, CornerstoneSnapshot:
	default:
		return fmt.Errorf("cornerstone: Pin: unsupported mode %q", input.Mode)
	}
	if input.Ref.BlobDigest != "" {
		return errors.New("cornerstone: Pin: blobDigest is managed internally")
	}
	if input.Type == CornerstoneFileRef && input.Mode != CornerstoneLiveRef {
		return errors.New("cornerstone: Pin: file_ref requires live_ref mode")
	}
	if input.Type == CornerstoneFileSnapshot && input.Mode != CornerstoneSnapshot {
		return errors.New("cornerstone: Pin: file_snapshot requires snapshot mode")
	}
	if (input.Type == CornerstoneFileRef || input.Type == CornerstoneFileSnapshot) && input.Ref.Kind != "workspace_file" {
		return errors.New("cornerstone: Pin: file cornerstones require a workspace_file ref")
	}
	if input.Mode == CornerstoneLiveRef && input.Ref.Kind == "inline" {
		return errors.New("cornerstone: Pin: inline content cannot use live_ref mode")
	}
	if input.Mode == CornerstoneSnapshot && input.Content == "" {
		return errors.New("cornerstone: Pin: snapshot content is required")
	}
	if input.Mode == CornerstoneLiveRef && len(input.Content) > CornerstoneInlineThreshold {
		return fmt.Errorf("cornerstone: Pin: live_ref last-known content exceeds %d bytes; freeze it as a snapshot", CornerstoneInlineThreshold)
	}
	ref := input.Ref
	switch ref.Kind {
	case "inline":
		if ref.SessionID != "" || ref.Turn != 0 || ref.Path != "" || ref.ArtifactID != "" || ref.URL != "" {
			return errors.New("cornerstone: Pin: inline ref contains source fields")
		}
	case "session_turn":
		if ref.SessionID == "" || ref.Turn < 0 || ref.Path != "" || ref.ArtifactID != "" || ref.URL != "" {
			return errors.New("cornerstone: Pin: session_turn requires sessionId and a non-negative turn only")
		}
	case "workspace_file":
		if ref.Path == "" || ref.SessionID != "" || ref.Turn != 0 || ref.ArtifactID != "" || ref.URL != "" {
			return errors.New("cornerstone: Pin: workspace_file requires path only")
		}
	case "artifact":
		if ref.ArtifactID == "" || ref.SessionID != "" || ref.Turn != 0 || ref.Path != "" || ref.URL != "" {
			return errors.New("cornerstone: Pin: artifact requires artifactId only")
		}
	case "url":
		if ref.URL == "" || ref.SessionID != "" || ref.Turn != 0 || ref.Path != "" || ref.ArtifactID != "" {
			return errors.New("cornerstone: Pin: url requires URL only")
		}
		u, err := url.Parse(ref.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("cornerstone: Pin: URL must be an absolute http or https URL")
		}
	default:
		return fmt.Errorf("cornerstone: Pin: unsupported ref kind %q", ref.Kind)
	}
	return nil
}

func cornerstoneInputSecretLike(input PinCornerstoneInput) bool {
	values := []string{
		string(input.Type), input.Title, input.Content, string(input.Mode), input.RequestID,
		input.Ref.Kind, input.Ref.SessionID, input.Ref.Path, input.Ref.ArtifactID, input.Ref.URL, input.Ref.BlobDigest,
	}
	values = append(values, input.Tags...)
	for _, value := range values {
		if IsSecretLike([]byte(value)) {
			return true
		}
	}
	if input.Ref.Kind == "url" {
		u, err := url.Parse(input.Ref.URL)
		if err == nil && u.User != nil {
			return true
		}
	}
	return false
}

func cornerstoneUpsertEvent(workID, requestID string, revision int64, cs Cornerstone, now time.Time, prefix string) (WorkEvent, error) {
	payload, err := json.Marshal(cs)
	if err != nil {
		return WorkEvent{}, fmt.Errorf("cornerstone: marshal upsert: %w", err)
	}
	timestamp := now.UTC()
	return WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            cornerstoneEventID(prefix, requestID, cs.ID),
		RequestID:     requestID,
		WorkID:        workID,
		Type:          EventCornerstoneUpserted,
		BaseRevision:  revision,
		Revision:      revision + 1,
		Payload:       payload,
		WriterID:      WorkWriterID(),
		CreatedAt:     timestamp,
	}, nil
}

func digestSet(digests []string) map[string]bool {
	set := make(map[string]bool, len(digests))
	for _, digest := range digests {
		set[digest] = true
	}
	return set
}

func cornerstoneEventID(prefix, requestID, objectID string) string {
	digest := sha256.Sum256([]byte(prefix + "\x00" + requestID + "\x00" + objectID))
	return prefix + "-" + fmt.Sprintf("%x", digest[:8])
}

func cornerstoneProvenance(workID string, ref CornerstoneRef) SourceRef {
	switch ref.Kind {
	case "inline":
		return SourceRef{Kind: "work", WorkID: workID}
	case "session_turn":
		return SourceRef{Kind: "session_turn", ObjectID: ref.SessionID}
	case "workspace_file":
		return SourceRef{Kind: "file", Path: ref.Path}
	case "artifact":
		return SourceRef{Kind: "artifact", ObjectID: ref.ArtifactID}
	case "url":
		return SourceRef{Kind: "url", URL: ref.URL}
	default:
		return SourceRef{}
	}
}
