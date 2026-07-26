package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── FileWorkStore: DefinitionRevisionStore implementation ──────────────────

// V2DefinitionDir returns the definitions subdirectory path for a work ID (relative to work root).
const v2DefSubDir = "definitions"

// StoreRevision persists a V2 WorkDefinitionRevision as a create-once sidecar
// under <work>/definitions/<revision>.json. Acquires per-work lock to prevent
// TOCTOU. Repeated writes with identical canonicalised bytes are idempotent;
// different bytes for the same revision return a typed conflict.
func (s *FileWorkStore) StoreRevision(workID string, rev *WorkDefinitionRevision) (retErr error) {
	if err := validateStoredRevision(workID, rev); err != nil {
		return err
	}
	done, lockErr := s.beginWorkOp(workID)
	if lockErr != nil {
		return lockErr
	}
	defer func() {
		if closeErr := done(); closeErr != nil {
			if retErr == nil {
				retErr = fmt.Errorf("work: release lock after StoreRevision for %s: %w", workID, closeErr)
			} else {
				retErr = errors.Join(retErr, closeErr)
			}
		}
	}()
	return s.storeRevisionLocked(workID, rev)
}

func validateStoredRevision(workID string, rev *WorkDefinitionRevision) error {
	if rev == nil {
		return fmt.Errorf("%w: definition revision for %s", ErrWorkNilInput, workID)
	}
	if rev.Digest == "" {
		return fmt.Errorf("work: definition revision for %s has empty digest", workID)
	}
	computed, err := ComputeV2RevisionDigest(rev)
	if err != nil {
		return fmt.Errorf("work: compute revision digest for %s: %w", workID, err)
	}
	if computed != rev.Digest {
		return fmt.Errorf("work: definition revision %d for %s: stored digest %q does not match computed %q",
			rev.Revision, workID, rev.Digest, computed)
	}
	return nil
}

func (s *FileWorkStore) storeRevisionLocked(workID string, rev *WorkDefinitionRevision) error {
	wp, err := s.workPath(workID)
	if err != nil {
		return err
	}
	dir := filepath.Join(wp, v2DefSubDir)
	path := filepath.Join(dir, fmt.Sprintf("%d.json", rev.Revision))

	canon, canonErr := canonicalV2Revision(rev)
	if canonErr != nil {
		return fmt.Errorf("work: canonicalise revision %d for %s: %w", rev.Revision, workID, canonErr)
	}
	data, err := json.MarshalIndent(canon, "", "  ")
	if err != nil {
		return fmt.Errorf("work: marshal revision %d for %s: %w", rev.Revision, workID, err)
	}
	data = append(data, '\n')

	existing, existErr := os.ReadFile(path)
	if existErr == nil {
		if string(existing) == string(data) {
			return nil
		}
		return &ErrWorkEventConflict{
			WorkID: workID,
			Reason: fmt.Sprintf("definition revision %d already stored with different content", rev.Revision),
			Kind:   WorkEventRequestConflict,
		}
	}
	if !os.IsNotExist(existErr) {
		return fmt.Errorf("work: read existing revision %d for %s: %w", rev.Revision, workID, existErr)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("work: create definitions dir for %s: %w", workID, err)
	}
	if err := writeDerivedFile(path, data, 0o644); err != nil {
		return fmt.Errorf("work: write revision %d for %s: %w", rev.Revision, workID, err)
	}
	return nil
}

// CommitDefinitionRevision serializes the body-first candidate commit under the
// same per-work lifecycle lock and writer lease. A failed event append may leave
// an unreferenced immutable body, but can never leave an event with no body.
func (s *FileWorkStore) CommitDefinitionRevision(workID string, rev *WorkDefinitionRevision, event WorkEvent) (revision int64, retErr error) {
	if err := validateStoredRevision(workID, rev); err != nil {
		return 0, err
	}
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := done(); closeErr != nil {
			if revision > 0 {
				closeErr = committedRecovery("commit-definition-revision", workID, event.RequestID, revision, closeErr)
			}
			retErr = errors.Join(retErr, closeErr)
		}
	}()
	wp, err := s.workPath(workID)
	if err != nil {
		return 0, err
	}
	if !s.isDirWithData(wp) {
		return 0, fmt.Errorf("%w: %s", ErrWorkNotFound, workID)
	}
	if held, _, writer := probeWorkLease(wp); held {
		return 0, fmt.Errorf("%w: cannot commit %s while writer %q is active", ErrWorkLeaseHeld, workID, writer)
	}
	if err := AcquireWorkLease(wp); err != nil {
		return 0, fmt.Errorf("work: acquire candidate writer lease for %s: %w", workID, err)
	}
	defer func() {
		if releaseErr := releaseStoreLease(wp); releaseErr != nil {
			if revision > 0 {
				releaseErr = committedRecovery("commit-definition-revision", workID, event.RequestID, revision, releaseErr)
			}
			retErr = errors.Join(retErr, releaseErr)
		}
	}()

	replay, err := ReplayWorkEventLog(wp)
	if err != nil {
		return 0, err
	}
	if err := validateV2DefinitionReplay(wp, workID, replay); err != nil {
		return 0, err
	}
	currentRevision := int64(0)
	if replay.Index != nil {
		currentRevision = replay.Index.Revision
	}
	if event.BaseRevision != currentRevision {
		return 0, revisionConflict(workID, event.BaseRevision, currentRevision)
	}
	if err := s.storeRevisionLocked(workID, rev); err != nil {
		return 0, err
	}
	return s.appendLocked(workID, wp, event)
}

// LoadRevision loads a V2 definition revision by number. Returns a deep copy.
func (s *FileWorkStore) LoadRevision(workID string, revision int64) (*WorkDefinitionRevision, error) {
	wp, err := s.workPath(workID)
	if err != nil {
		return nil, err
	}
	return loadRevisionFromDir(wp, workID, revision)
}

func loadRevisionFromDir(workDir, workID string, revision int64) (*WorkDefinitionRevision, error) {
	path := filepath.Join(workDir, v2DefSubDir, fmt.Sprintf("%d.json", revision))
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, fmt.Errorf("work: definition revision %d for %s not found: %w", revision, workID, ErrWorkNotFound)
		}
		return nil, fmt.Errorf("work: read revision %d for %s: %w", revision, workID, readErr)
	}
	var rev WorkDefinitionRevision
	if err := json.Unmarshal(data, &rev); err != nil {
		return nil, fmt.Errorf("work: corrupt revision %d for %s: %w", revision, workID, err)
	}
	// Verify digest.
	if rev.Digest == "" {
		return nil, fmt.Errorf("work: revision %d for %s has empty stored digest", revision, workID)
	}
	computed, err := ComputeV2RevisionDigest(&rev)
	if err != nil {
		return nil, fmt.Errorf("work: verify revision %d digest for %s: %w", revision, workID, err)
	}
	if computed != rev.Digest {
		return nil, fmt.Errorf("work: revision %d for %s: stored digest %q != computed %q",
			revision, workID, rev.Digest, computed)
	}
	// Deep copy via JSON round-trip — never expose internal pointer.
	var out WorkDefinitionRevision
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("work: deep-copy revision %d for %s: %w", revision, workID, err)
	}
	return &out, nil
}

func validateV2DefinitionReplay(workDir, workID string, replay *WorkEventReplay) error {
	if replay == nil {
		return fmt.Errorf("%w: nil replay for %s", ErrWorkNeedsRepair, workID)
	}
	created := make(map[int64]DefRevisionCreatedPayload)
	applyReceipts := make(map[string]*V2IntentReceipt)
	active := int64(0)
	for _, event := range replay.Events {
		switch event.Type {
		case EventRunStarted:
			if !strings.HasSuffix(event.RequestID, "/run") {
				continue
			}
			var payload runEventPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("%w: decode run receipt for %s: %v", ErrWorkNeedsRepair, workID, err)
			}
			if payload.V2Receipt != nil {
				applyReceipts[strings.TrimSuffix(event.RequestID, "/run")] = payload.V2Receipt
			}

		case EventDefRevisionCreated:
			var payload DefRevisionCreatedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("%w: decode revision_created for %s: %v", ErrWorkNeedsRepair, workID, err)
			}
			body, err := loadRevisionFromDir(workDir, workID, payload.Revision)
			if err != nil {
				return fmt.Errorf("%w: revision_created %d has no valid body for %s: %v", ErrWorkNeedsRepair, payload.Revision, workID, err)
			}
			if payload.WorkID != workID || body.WorkID != workID ||
				body.Revision != payload.Revision ||
				body.ParentRevision != payload.ParentRevision ||
				body.Digest != payload.Digest {
				return fmt.Errorf("%w: revision_created %d does not match immutable body for %s", ErrWorkNeedsRepair, payload.Revision, workID)
			}
			if _, duplicate := created[payload.Revision]; duplicate {
				return fmt.Errorf("%w: duplicate revision_created %d for %s", ErrWorkNeedsRepair, payload.Revision, workID)
			}
			if payload.Revision == 1 {
				if payload.ParentRevision != 0 {
					return fmt.Errorf("%w: initial revision_created for %s has parent %d", ErrWorkNeedsRepair, workID, payload.ParentRevision)
				}
			} else if payload.ParentRevision > 0 {
				parent, ok := created[payload.ParentRevision]
				if !ok {
					return fmt.Errorf("%w: revision_created %d has no committed parent event %d for %s",
						ErrWorkNeedsRepair, payload.Revision, payload.ParentRevision, workID)
				}
				parentBody, err := loadRevisionFromDir(workDir, workID, payload.ParentRevision)
				if err != nil || parentBody.Digest != parent.Digest {
					return fmt.Errorf("%w: revision_created %d has invalid parent body %d for %s: %v",
						ErrWorkNeedsRepair, payload.Revision, payload.ParentRevision, workID, err)
				}
			}
			created[payload.Revision] = payload

		case EventDefRevisionApplied:
			var payload DefRevisionAppliedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("%w: decode revision_applied for %s: %v", ErrWorkNeedsRepair, workID, err)
			}
			createdPayload, ok := created[payload.Revision]
			if !ok {
				return fmt.Errorf("%w: revision_applied %d has no revision_created event for %s", ErrWorkNeedsRepair, payload.Revision, workID)
			}
			body, err := loadRevisionFromDir(workDir, workID, payload.Revision)
			if err != nil {
				return fmt.Errorf("%w: revision_applied %d has no valid body for %s: %v", ErrWorkNeedsRepair, payload.Revision, workID, err)
			}
			if err := ValidateDefinitionRevision(body); err != nil {
				return fmt.Errorf("%w: revision_applied %d is invalid for %s: %v", ErrWorkNeedsRepair, payload.Revision, workID, err)
			}
			if payload.WorkID != workID ||
				payload.PreviousRevision != body.ParentRevision ||
				createdPayload.Digest != body.Digest {
				return fmt.Errorf("%w: revision_applied %d lineage mismatch for %s", ErrWorkNeedsRepair, payload.Revision, workID)
			}
			if active != 0 && payload.PreviousRevision != active {
				return fmt.Errorf("%w: revision_applied %d expected active parent %d, got %d for %s",
					ErrWorkNeedsRepair, payload.Revision, active, payload.PreviousRevision, workID)
			}
			if event.Object.DefinitionRevision == nil || *event.Object.DefinitionRevision != payload.Revision ||
				event.Object.ExpectedRevision == nil || *event.Object.ExpectedRevision != payload.ExpectedRevision {
				return fmt.Errorf("%w: revision_applied %d object context mismatch for %s", ErrWorkNeedsRepair, payload.Revision, workID)
			}
			requestID := strings.TrimSuffix(event.RequestID, "/apply")
			receipt := applyReceipts[requestID]
			if receipt == nil || receipt.Operation != "ApplyDefinition" ||
				receipt.RequestID != requestID ||
				receipt.ResultRevision != payload.Revision {
				return fmt.Errorf("%w: revision_applied %d has no matching atomic apply receipt for %s", ErrWorkNeedsRepair, payload.Revision, workID)
			}
			var expectedImpact *RunImpact
			if body.ParentRevision > 0 {
				parent, err := loadRevisionFromDir(workDir, workID, body.ParentRevision)
				if err != nil {
					return fmt.Errorf("%w: load apply parent %d for %s: %v", ErrWorkNeedsRepair, body.ParentRevision, workID, err)
				}
				expectedImpact = ClassifyRunImpact(parent, body)
			} else {
				expectedImpact = ClassifyRunImpact(&WorkDefinitionRevision{}, body)
			}
			gotImpact, _ := json.Marshal(receipt.Impact)
			wantImpact, _ := json.Marshal(impactToJSON(expectedImpact))
			if string(gotImpact) != string(wantImpact) {
				return fmt.Errorf("%w: revision_applied %d impact receipt mismatch for %s", ErrWorkNeedsRepair, payload.Revision, workID)
			}
			active = payload.Revision
		}
	}
	return nil
}

// LoadLatestRevision finds the highest-numbered revision file. Returns nil if
// none exist.
func (s *FileWorkStore) LoadLatestRevision(workID string) (*WorkDefinitionRevision, error) {
	wp, err := s.workPath(workID)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(wp, v2DefSubDir)
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("work: read definitions dir for %s: %w", workID, readErr)
	}
	var latest int64 = -1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		var rev int64
		if _, err := fmt.Sscanf(name, "%d.json", &rev); err != nil || rev < 1 {
			continue
		}
		if rev > latest {
			latest = rev
		}
	}
	if latest < 0 {
		return nil, nil
	}
	return s.LoadRevision(workID, latest)
}

// ── Helpers ────────────────────────────────────────────────────────────────

func canonicalV2Revision(rev *WorkDefinitionRevision) (*WorkDefinitionRevision, error) {
	raw, err := json.Marshal(rev)
	if err != nil {
		return nil, err
	}
	var out WorkDefinitionRevision
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Ensure interface satisfaction ──────────────────────────────────────────

var _ DefinitionRevisionStore = (*FileWorkStore)(nil)
