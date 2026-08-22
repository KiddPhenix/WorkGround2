package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// ── ArtifactSlot projection helpers ────────────────────────────────────────

// slotProjKey builds the deterministic composite key for an ArtifactSlot from
// its definition revision and slot definition ID.
func slotProjKey(definitionRev int64, slotDefID string) string {
	return fmt.Sprintf("%d/%s", definitionRev, slotDefID)
}

// SlotProjKey builds the deterministic composite key from an ArtifactSlot.
func SlotProjKey(slot ArtifactSlot) string { return slotProjKey(slot.DefinitionRev, slot.ID) }

// ── Slot declaration ──────────────────────────────────────────────────────

// DeclareArtifactSlotsInput carries the parameters for declaring slots from a
// definition's ArtifactSlotDefs.
type DeclareArtifactSlotsInput struct {
	WorkID           string
	DefinitionRev    int64
	RequestID        string
	SlotDefs         []ArtifactSlotDef
	ExpectedRevision int64
}

// DeclareArtifactSlots produces artifact_slot.declared events for every
// ArtifactSlotDef in the definition. Each slot is keyed by definitionRev +
// slotDef.ID. Repeated calls with the same requestID are idempotent.
func DeclareArtifactSlots(defs []ArtifactSlotDef) []ArtifactSlot {
	slots := make([]ArtifactSlot, 0, len(defs))
	for _, d := range defs {
		slots = append(slots, ArtifactSlot{
			ID:            d.ID,
			Title:         d.Title,
			Kind:          d.Kind,
			ExpectedCount: d.ExpectedCount,
			Required:      d.Required,
			State:         SlotReserved,
		})
	}
	return slots
}

// BuildDeclareEvents constructs one EventArtifactSlotDeclared per slot.
// Each slot carries a deterministic composite identity via definitionRev + slot.ID.
func BuildDeclareEvents(workID string, definitionRev int64, requestID string, defs []ArtifactSlotDef, now time.Time) []WorkEvent {
	events := make([]WorkEvent, 0, len(defs))
	for i, d := range defs {
		payload, _ := json.Marshal(ArtifactSlotDeclaredPayload{
			SlotID:        d.ID,
			WorkID:        workID,
			DefinitionRev: definitionRev,
			Title:         d.Title,
			Kind:          d.Kind,
			ExpectedCount: d.ExpectedCount,
			Required:      d.Required,
		})
		ev := newServiceEventV2(workID, fmt.Sprintf("%s/declare-slot/%d", requestID, i),
			EventArtifactSlotDeclared, payload, now)
		ev.Object = ObjectContext{
			Kind: ObjectArtifactSlot, ID: d.ID, WorkID: workID,
			ArtifactSlotID: d.ID, DefinitionRevision: int64Ptr(definitionRev),
		}
		events = append(events, ev)
	}
	return events
}

// ── Slot update ───────────────────────────────────────────────────────────

// UpdateArtifactSlotInput carries parameters for a slot state update.
type UpdateArtifactSlotInput struct {
	WorkID           string
	SlotID           string
	RequestID        string
	State            ArtifactSlotState
	Refs             []ArtifactRef
	UpstreamDigest   string
	Progress         *float64
	Summary          string
	Error            *ArtifactError
	Revision         int64
	ExpectedRevision int64
	DefinitionRev    int64
	// intentDigest lets a coordinating operation keep its caller-level
	// idempotency identity while rebasing this child event onto the latest
	// aggregate Work revision. Direct callers leave it empty.
	intentDigest string
}

// ArtifactSlotResult reports both the updated slot revision and the
// authoritative Work event-log revision.
type ArtifactSlotResult struct {
	Slot         ArtifactSlot `json:"slot"`
	WorkRevision int64        `json:"workRevision"`
	Duplicate    bool         `json:"duplicate"`
}

// ArtifactSlotUpdateReceipt is an additive V2 event payload field. Keeping the
// first result in the authoritative log makes retries independent of mutable
// projections and derived receipt sidecars.
type ArtifactSlotUpdateReceipt struct {
	Slot         ArtifactSlot `json:"slot"`
	WorkRevision int64        `json:"workRevision"`
	IntentDigest string       `json:"intentDigest"`
}

// BuildUpdateEvent constructs an EventArtifactSlotUpdated.
func BuildUpdateEvent(input UpdateArtifactSlotInput, now time.Time) WorkEvent {
	payload, _ := json.Marshal(ArtifactSlotUpdatedPayload{
		SlotID:         input.SlotID,
		WorkID:         input.WorkID,
		State:          input.State,
		Refs:           input.Refs,
		UpstreamDigest: input.UpstreamDigest,
		Progress:       input.Progress,
		Summary:        input.Summary,
		Error:          input.Error,
		Revision:       input.Revision,
	})
	ev := newServiceEventV2(input.WorkID, input.RequestID, EventArtifactSlotUpdated, payload, now)
	ev.Object = ObjectContext{
		Kind: ObjectArtifactSlot, ID: input.SlotID, WorkID: input.WorkID,
		ArtifactSlotID: input.SlotID, DefinitionRevision: int64Ptr(input.DefinitionRev),
		ExpectedRevision: int64Ptr(input.ExpectedRevision),
	}
	return ev
}

// UpdateArtifactSlot is the single production write entry for ArtifactSlot
// state. The FileWorkStore CommitEvent seam owns the writer lease, durable
// request receipt, preflight reducer validation, event append, and projection
// rebuild.
func (s *Service) UpdateArtifactSlot(ctx context.Context, input UpdateArtifactSlotInput) (*ArtifactSlotResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("UpdateArtifactSlot", input.RequestID)
	if err != nil {
		return nil, err
	}
	input.RequestID = artifactSlotRequestID(requestID)
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.SlotID = strings.TrimSpace(input.SlotID)
	input.UpstreamDigest = strings.TrimSpace(input.UpstreamDigest)
	if input.WorkID == "" || input.SlotID == "" {
		return nil, errors.New("work: UpdateArtifactSlot: workID and slotID are required")
	}
	if input.DefinitionRev <= 0 || input.Revision <= 0 {
		return nil, errors.New("work: UpdateArtifactSlot: definitionRev and slot revision must be positive")
	}
	for i, ref := range input.Refs {
		if strings.TrimSpace(ref.ID) == "" {
			return nil, fmt.Errorf("work: UpdateArtifactSlot: refs[%d].id is required", i)
		}
		if !validArtifactRefStatus(ref.Status) {
			return nil, fmt.Errorf("work: UpdateArtifactSlot: refs[%d].status %q is invalid", i, ref.Status)
		}
		if strings.TrimSpace(ref.URL) != "" && !ValidateArtifactURL(ref.URL) {
			return nil, fmt.Errorf("work: UpdateArtifactSlot: refs[%d].url %q is not an absolute http(s) URL", i, ref.URL)
		}
	}

	current, state, err := s.store.LoadState(input.WorkID, input.RequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		store, ok := s.store.(interface {
			LoadArtifactSlotUpdate(string, string) (*ArtifactSlotResult, string, error)
		})
		if !ok {
			return nil, fmt.Errorf("work: UpdateArtifactSlot: store cannot reconstruct authoritative receipt")
		}
		result, intentDigest, loadErr := store.LoadArtifactSlotUpdate(input.WorkID, input.RequestID)
		if loadErr != nil {
			return nil, loadErr
		}
		if intentDigest != effectiveArtifactSlotIntentDigest(input) {
			return nil, &ErrWorkEventConflict{
				WorkID: input.WorkID, RequestID: input.RequestID,
				Kind:   WorkEventRequestConflict,
				Reason: "requestID already used by a different artifact slot update",
			}
		}
		result.Duplicate = true
		return result, nil
	}
	if current.SchemaVersion < SchemaVersionV2 {
		return nil, fmt.Errorf("work: UpdateArtifactSlot: Work %s is schema V1", input.WorkID)
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: UpdateArtifactSlot: Work %s is %s", input.WorkID, current.ArchiveState)
	}
	slot, _ := FindArtifactSlotRevision(current, input.DefinitionRev, input.SlotID)
	if slot == nil {
		return nil, fmt.Errorf("work: UpdateArtifactSlot: slot %q at definition revision %d not found",
			input.SlotID, input.DefinitionRev)
	}

	event := BuildUpdateEvent(input, time.Now().UTC())
	event.BaseRevision = input.ExpectedRevision
	event.Revision = input.ExpectedRevision + 1
	if input.ExpectedRevision != state.Revision {
		return artifactSlotResult(slot, state.Revision),
			revisionConflict(input.WorkID, input.ExpectedRevision, state.Revision)
	}
	var payload ArtifactSlotUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil, fmt.Errorf("work: UpdateArtifactSlot: decode event payload: %w", err)
	}
	updatedSlot, err := projectArtifactSlotUpdate(slot, payload, input.RequestID)
	if err != nil {
		return artifactSlotResult(slot, state.Revision), err
	}
	payload.Receipt = &ArtifactSlotUpdateReceipt{
		Slot:         updatedSlot,
		WorkRevision: event.Revision,
		IntentDigest: effectiveArtifactSlotIntentDigest(input),
	}
	event.Payload, err = json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("work: UpdateArtifactSlot: encode event receipt: %w", err)
	}
	revision, err := s.store.CommitEvent(input.WorkID, event)
	if err != nil {
		return artifactSlotResult(slot, state.Revision), err
	}
	return artifactSlotResult(&updatedSlot, revision), nil
}

func artifactSlotResult(slot *ArtifactSlot, workRevision int64) *ArtifactSlotResult {
	if slot == nil {
		return nil
	}
	copySlot := *slot
	copySlot.ArtifactRefs = append([]ArtifactRef(nil), slot.ArtifactRefs...)
	return &ArtifactSlotResult{Slot: copySlot, WorkRevision: workRevision}
}

// LoadArtifactSlotUpdate rebuilds the first update result from the
// authoritative event log. Receipts projected into compact snapshots keep the
// result available after log compaction; older pre-receipt events fall back to
// replaying through the matching event.
func (s *FileWorkStore) LoadArtifactSlotUpdate(workID, requestID string) (result *ArtifactSlotResult, intentDigest string, retErr error) {
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return nil, "", err
	}
	defer func() { retErr = errors.Join(retErr, done()) }()

	wp, err := s.workPath(workID)
	if err != nil {
		return nil, "", err
	}
	replay, projection, err := ReplayWithReducer(wp, DefaultReducer())
	if err != nil {
		return nil, "", fmt.Errorf("work: replay artifact slot receipt for %s: %w", workID, err)
	}
	if receipt, ok := projection.V2ArtifactReceipts[requestID]; ok {
		return artifactSlotResult(&receipt.Slot, receipt.WorkRevision), receipt.IntentDigest, nil
	}

	var current *Work
	for _, event := range replay.Events {
		if event.Type == eventCompact {
			current, err = decodeCompactProjection(event.Payload)
		} else {
			current, err = DefaultReducer()(event, current)
		}
		if err != nil {
			return nil, "", fmt.Errorf("work: replay artifact slot receipt at revision %d: %w", event.Revision, err)
		}
		if event.RequestID != requestID || event.Type != EventArtifactSlotUpdated {
			continue
		}
		var payload ArtifactSlotUpdatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, "", fmt.Errorf("work: decode artifact slot receipt at revision %d: %w", event.Revision, err)
		}
		slot, _ := FindArtifactSlotRevision(current, *event.Object.DefinitionRevision, payload.SlotID)
		if slot == nil {
			return nil, "", fmt.Errorf("%w: artifact slot receipt %s has no projected slot", ErrWorkNeedsRepair, requestID)
		}
		payload.Receipt = nil
		event.Payload, err = json.Marshal(payload)
		if err != nil {
			return nil, "", err
		}
		digest, err := workEventIdempotentDigest(recordFromEvent(event))
		if err != nil {
			return nil, "", err
		}
		return artifactSlotResult(slot, event.Revision), digest, nil
	}
	return nil, "", fmt.Errorf("%w: artifact slot receipt %s for %s", ErrWorkNotFound, requestID, workID)
}

func artifactSlotIntentDigest(input UpdateArtifactSlotInput) string {
	event := BuildUpdateEvent(input, time.Time{})
	digest, err := workEventIdempotentDigest(recordFromEvent(event))
	if err != nil {
		return ""
	}
	return digest
}

func effectiveArtifactSlotIntentDigest(input UpdateArtifactSlotInput) string {
	if digest := strings.TrimSpace(input.intentDigest); digest != "" {
		return digest
	}
	return artifactSlotIntentDigest(input)
}

func artifactSlotRequestID(requestID string) string {
	return requestID + "/artifact-slot"
}

// ── Projection read helpers ────────────────────────────────────────────────

// FindArtifactSlot locates the newest definition revision for slotID. Writers
// must use FindArtifactSlotRevision so a late event cannot target a newer slot
// that reused the same definition-local ID.
func FindArtifactSlot(w *Work, slotID string) (*ArtifactSlot, int) {
	if w == nil {
		return nil, -1
	}
	best := -1
	for i := range w.V2ArtifactSlots {
		if w.V2ArtifactSlots[i].ID == slotID &&
			(best < 0 || w.V2ArtifactSlots[i].DefinitionRev > w.V2ArtifactSlots[best].DefinitionRev) {
			best = i
		}
	}
	if best < 0 {
		return nil, -1
	}
	return &w.V2ArtifactSlots[best], best
}

// FindArtifactSlotRevision locates a slot by its stable composite projection
// key (definitionRev, slotID).
func FindArtifactSlotRevision(w *Work, definitionRev int64, slotID string) (*ArtifactSlot, int) {
	if w == nil {
		return nil, -1
	}
	for i := range w.V2ArtifactSlots {
		slot := &w.V2ArtifactSlots[i]
		if slot.DefinitionRev == definitionRev && slot.ID == slotID {
			return slot, i
		}
	}
	return nil, -1
}

// ── Dedup helpers ─────────────────────────────────────────────────────────

// DedupArtifactRefs returns a deduplicated copy of refs, keeping the first
// occurrence of each stable ID. An empty ID is treated as distinct and kept
// as-is (callers should not produce empty-ID refs).
func DedupArtifactRefs(refs []ArtifactRef) []ArtifactRef {
	if len(refs) <= 1 {
		return append([]ArtifactRef(nil), refs...)
	}
	seen := make(map[string]bool, len(refs))
	out := make([]ArtifactRef, 0, len(refs))
	for _, r := range refs {
		if r.ID == "" {
			out = append(out, r)
			continue
		}
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	return out
}

// ── Partial calculation ────────────────────────────────────────────────────

// ComputeSlotState derives the correct state from usable ref count vs expected.
// - 0 refs, not generating → reserved
// - ref count ≥ expectedCount → ready
// - ref count > 0 but < expectedCount → partial
// Required controls only the aggregate delivery gate; it never weakens a
// slot's own state facts.
// Does not override generating / failed / stale — those are set explicitly.
func ComputeSlotState(slot *ArtifactSlot) ArtifactSlotState {
	if slot == nil {
		return SlotReserved
	}
	refCount := usableArtifactRefCount(slot.ArtifactRefs)
	if refCount >= slot.ExpectedCount && slot.ExpectedCount > 0 {
		return SlotReady
	}
	if refCount > 0 {
		return SlotPartial
	}
	if slot.State == SlotReady || slot.State == SlotPartial {
		return SlotReserved
	}
	return slot.State // preserve reserved/generating/failed/stale
}

func usableArtifactRefCount(refs []ArtifactRef) int {
	count := 0
	for _, ref := range refs {
		if ref.Status == ArtifactRefStatusAvailable {
			count++
		}
	}
	return count
}

// ── Revision guard ────────────────────────────────────────────────────────

// ValidateSlotRevision returns an error if the event cannot be applied:
//   - revision < current: late event, reject
//   - revision == current: same content → idempotent, different content → conflict
//   - revision > current: apply
func ValidateSlotRevision(current *ArtifactSlot, incoming ArtifactSlot, requestID string) error {
	if current == nil {
		return nil
	}
	if incoming.Revision < current.Revision {
		return fmt.Errorf("work: slot %q late event rev %d < current %d (request %s)",
			current.ID, incoming.Revision, current.Revision, requestID)
	}
	if incoming.Revision == current.Revision {
		// Same revision: must be exact same content (idempotent).
		if !slotContentEqual(current, &incoming) {
			return &ErrWorkEventConflict{
				WorkID: current.WorkID,
				Kind:   WorkEventRevisionConflict,
				Reason: fmt.Sprintf("slot %q revision %d same rev but different content (request %s)",
					current.ID, incoming.Revision, requestID),
			}
		}
	}
	return nil
}

func slotContentEqual(a, b *ArtifactSlot) bool {
	return reflect.DeepEqual(a, b)
}

// ── Digest change / stale detection ────────────────────────────────────────

// SlotUpstreamDigestChanged distinguishes source invalidation from normal
// replacement of a generated ArtifactRef blob.
func SlotUpstreamDigestChanged(slot *ArtifactSlot, incoming string) bool {
	return slot != nil && slot.UpstreamDigest != "" && incoming != "" &&
		slot.UpstreamDigest != incoming
}

// ── Merge helpers ─────────────────────────────────────────────────────────

// mergeSlotRefs merges incoming refs into existing refs, deduplicating by
// stable ID. Incoming refs replace prior values with the same ID; regenerating
// a file at a stable artifact ID is a normal update, not upstream invalidation.
func mergeSlotRefs(existing, incoming []ArtifactRef) []ArtifactRef {
	index := make(map[string]int, len(existing))
	out := append([]ArtifactRef(nil), existing...)
	for i, r := range out {
		if r.ID != "" {
			index[r.ID] = i
		}
	}
	for _, r := range incoming {
		if i, ok := index[r.ID]; r.ID != "" && ok {
			out[i] = r
		} else {
			if r.ID != "" {
				index[r.ID] = len(out)
			}
			out = append(out, r)
		}
	}
	return out
}

// ── Slot projection (used by reducer) ─────────────────────────────────────

// reduceArtifactSlotDeclared handles EventArtifactSlotDeclared in the reducer.
func reduceArtifactSlotDeclared(w *Work, slotID, workID string, definitionRev int64, title, kind string, expectedCount int, required bool) error {
	existing, _ := FindArtifactSlotRevision(w, definitionRev, slotID)
	if existing != nil {
		if existing.WorkID == workID && existing.Title == title && existing.Kind == kind &&
			existing.ExpectedCount == expectedCount && existing.Required == required {
			return nil
		}
		return &ErrWorkEventConflict{
			WorkID: workID, Kind: WorkEventRevisionConflict,
			Reason: fmt.Sprintf("artifact slot %q definition revision %d was redeclared with different content",
				slotID, definitionRev),
		}
	}
	w.V2ArtifactSlots = append(w.V2ArtifactSlots, ArtifactSlot{
		ID:            slotID,
		WorkID:        workID,
		DefinitionRev: definitionRev,
		Title:         title,
		Kind:          kind,
		ExpectedCount: expectedCount,
		Required:      required,
		State:         SlotReserved,
		Revision:      1,
	})
	return nil
}

// reduceArtifactSlotUpdated handles EventArtifactSlotUpdated in the reducer.
// Returns error on revision conflict.
func reduceArtifactSlotUpdated(w *Work, payload ArtifactSlotUpdatedPayload, requestID string) error {
	existing, _ := FindArtifactSlot(w, payload.SlotID)
	if existing == nil {
		return fmt.Errorf("work: slot %q not found for update (request %s)", payload.SlotID, requestID)
	}
	return reduceArtifactSlotUpdatedAt(w, payload, existing.DefinitionRev, requestID)
}

func reduceArtifactSlotUpdatedAt(w *Work, payload ArtifactSlotUpdatedPayload, definitionRev int64, requestID string) error {
	existing, idx := FindArtifactSlotRevision(w, definitionRev, payload.SlotID)
	if existing == nil {
		return fmt.Errorf("work: slot %q at definition revision %d not found for update (request %s)",
			payload.SlotID, definitionRev, requestID)
	}

	updated, err := projectArtifactSlotUpdate(existing, payload, requestID)
	if err != nil {
		return err
	}
	w.V2ArtifactSlots[idx] = updated
	if payload.Receipt != nil {
		if w.V2ArtifactReceipts == nil {
			w.V2ArtifactReceipts = make(map[string]ArtifactSlotUpdateReceipt)
		}
		receipt := *payload.Receipt
		receipt.Slot.ArtifactRefs = append([]ArtifactRef(nil), payload.Receipt.Slot.ArtifactRefs...)
		w.V2ArtifactReceipts[requestID] = receipt
	}
	return nil
}

func projectArtifactSlotUpdate(existing *ArtifactSlot, payload ArtifactSlotUpdatedPayload, requestID string) (ArtifactSlot, error) {
	refs := DedupArtifactRefs(payload.Refs)
	updated := *existing
	if SlotUpstreamDigestChanged(existing, payload.UpstreamDigest) {
		updated.State = SlotStale
		updated.UpstreamDigest = payload.UpstreamDigest
	} else {
		if payload.State != "" {
			updated.State = payload.State
		}
		if payload.UpstreamDigest != "" {
			updated.UpstreamDigest = payload.UpstreamDigest
		}
		updated.ArtifactRefs = mergeSlotRefs(existing.ArtifactRefs, refs)
	}
	if payload.Progress != nil {
		updated.Progress = payload.Progress
	}
	if payload.Summary != "" {
		updated.Summary = payload.Summary
	}
	if payload.Error != nil {
		updated.Error = payload.Error
	} else if payload.State == SlotGenerating || payload.State == SlotReady || payload.State == SlotPartial {
		updated.Error = nil
	}
	if updated.State != SlotStale && updated.State != SlotFailed && updated.State != SlotGenerating {
		updated.State = ComputeSlotState(&updated)
	}
	updated.Revision = payload.Revision

	if err := ValidateSlotRevision(existing, updated, requestID); err != nil {
		return ArtifactSlot{}, err
	}
	if updated.Revision == existing.Revision {
		return updated, nil
	}
	if err := ValidateArtifactSlotTransition(existing.State, updated.State); err != nil {
		return ArtifactSlot{}, err
	}
	return updated, nil
}
