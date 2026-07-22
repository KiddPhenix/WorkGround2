package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrCornerstoneResolverUnavailable is retryable after an adapter is set.
	ErrCornerstoneResolverUnavailable = errors.New("cornerstone: resolver unavailable")
	// ErrCornerstoneCandidateChanged forces a new review when the source moved
	// after Refresh and before Accept or Freeze.
	ErrCornerstoneCandidateChanged = errors.New("cornerstone: reviewed candidate changed; refresh and review again")
)

// Resolve is the context-aware typed Refresh entry point.
func (m *CornerstoneManager) Resolve(ctx context.Context, workID string, input RefreshCornerstoneInput) (*CornerstoneResult, error) {
	return m.resolveAndRefresh(ctx, workID, input)
}

// ResolveAndRefresh keeps the intent-level API convenient for callers that do
// not carry a context. Context-aware callers should use Resolve.
func (m *CornerstoneManager) ResolveAndRefresh(workID string, input RefreshCornerstoneInput) (*CornerstoneResult, error) {
	return m.resolveAndRefresh(context.Background(), workID, input)
}

func (m *CornerstoneManager) resolveRef(ctx context.Context, workID string, ref CornerstoneRef) (ResolveResult, error) {
	key := workID + "\x00" + refIdentity(ref)
	m.inflightMu.Lock()
	if call, ok := m.inflight[key]; ok {
		call.waiters++
		m.inflightMu.Unlock()
		select {
		case <-call.done:
			return call.result, call.err
		case <-ctx.Done():
			return ResolveResult{}, ctx.Err()
		}
	}
	call := &resolveCall{done: make(chan struct{})}
	m.inflight[key] = call
	m.inflightMu.Unlock()

	defer func() {
		m.inflightMu.Lock()
		delete(m.inflight, key)
		close(call.done)
		m.inflightMu.Unlock()
	}()
	if scoped, ok := m.resolver.(ScopedCornerstoneResolver); ok {
		call.result, call.err = scoped.ResolveForWork(ctx, workID, ref)
	} else {
		call.result, call.err = m.resolver.Resolve(ctx, ref)
	}
	call.result, call.err = classifyResolveResult(call.result, call.err)
	return call.result, call.err
}

func classifyResolveResult(result ResolveResult, err error) (ResolveResult, error) {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ResolveResult{}, err
		}
		var resolverErr *ResolverError
		if errors.As(err, &resolverErr) {
			kind := resolverErr.Kind
			if kind == "" {
				kind = ResolveErrorNetwork
			}
			return ResolveResult{ErrorKind: kind, Error: "source resolution failed"}, nil
		}
		return ResolveResult{ErrorKind: ResolveErrorNetwork, Error: "source resolution failed"}, nil
	}
	if result.ErrorKind == "" {
		switch {
		case !result.Found:
			result.ErrorKind = ResolveErrorMissing
		case !result.Accessible:
			result.ErrorKind = ResolveErrorDenied
		}
	}
	if result.ErrorKind != "" {
		result.Content = ""
		result.Digest = ""
		return result, nil
	}
	normalized := normalizeCornerstoneContent(result.Content)
	digest := ContentDigest([]byte(normalized))
	if result.Digest != "" && result.Digest != digest {
		return ResolveResult{ErrorKind: ResolveErrorInvalid, Error: "resolver digest mismatch"}, nil
	}
	if IsSecretLike([]byte(normalized)) {
		return ResolveResult{ErrorKind: ResolveErrorInvalid, Error: "resolved content was rejected"}, nil
	}
	result.Content = normalized
	result.Digest = digest
	result.Found = true
	result.Accessible = true
	result.Error = ""
	return result, nil
}

func (m *CornerstoneManager) resolveAndRefresh(ctx context.Context, workID string, input RefreshCornerstoneInput) (*CornerstoneResult, error) {
	workID, csID, requestID, err := normalizeCornerstoneMutation("Resolve", workID, input.CornerstoneID, input.RequestID)
	if err != nil {
		return nil, err
	}
	return m.resolveAndRefreshLocked(ctx, workID, csID, requestID, input.ExpectedRevision)
}

func (m *CornerstoneManager) resolveAndRefreshLocked(ctx context.Context, workID, csID, requestID string, expectedRevision int64) (*CornerstoneResult, error) {
	eventRequestID := cornerstoneMutationRequestID(requestID)
	eventID := cornerstoneEventID("cs-refresh", eventRequestID, csID)
	current, state, err := m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		return mutationReplay("Resolve", current, state, csID, requestID, eventID)
	}
	if err := validateCornerstoneMutation("Resolve", current, state, csID, expectedRevision); err != nil {
		return nil, err
	}
	cs := findCornerstone(current, csID)
	now := m.clock.Now().UTC()
	updated := *cs
	var resolution *CornerstoneResolution

	if cs.Mode == CornerstoneSnapshot {
		applySnapshotVerification(m, workID, &updated)
	} else {
		if m.resolver == nil {
			return nil, ErrCornerstoneResolverUnavailable
		}
		identity := refIdentity(cs.Ref)
		resolved, err := m.resolveRef(ctx, workID, cs.Ref)
		if err != nil {
			return nil, err
		}
		current, state, err = m.store.LoadState(workID, eventRequestID)
		if err != nil {
			return nil, err
		}
		if state.RequestFound {
			return mutationReplay("Resolve", current, state, csID, requestID, eventID)
		}
		if expectedRevision != state.Revision {
			return nil, revisionConflict(workID, expectedRevision, state.Revision)
		}
		cs = findCornerstone(current, csID)
		if cs == nil || cs.Tombstone || refIdentity(cs.Ref) != identity {
			return nil, revisionConflict(workID, expectedRevision, state.Revision)
		}
		updated = *cs
		resolution = m.applyResolved(workID, &updated, resolved)
	}

	updated.LastVerifiedAt = &now
	updated.UpdatedAt = now
	result, err := m.commitCornerstoneUpdate(workID, csID, eventRequestID, "Resolve", "cs-refresh", eventID, state.Revision, updated, now)
	if err != nil {
		return nil, err
	}
	attachCornerstoneResult(result, resolution)
	return result, nil
}

func applySnapshotVerification(m *CornerstoneManager, workID string, updated *Cornerstone) {
	updated.CandidateContent = ""
	updated.CandidateDigest = ""
	if updated.Ref.BlobDigest != "" {
		if err := m.verifyBlobIntegrity(workID, updated.Ref.BlobDigest); err != nil {
			updated.Status = CornerstoneInvalid
			updated.ResolveErrorKind = ResolveErrorInvalid
			updated.Error = "snapshot blob is missing or corrupt; repair is available"
			return
		}
	} else if ContentDigest([]byte(normalizeCornerstoneContent(updated.Content))) != updated.Digest {
		updated.Status = CornerstoneInvalid
		updated.ResolveErrorKind = ResolveErrorInvalid
		updated.Error = "snapshot content digest is invalid; repair is available"
		return
	}
	updated.Status = CornerstoneActive
	updated.ResolveErrorKind = ""
	updated.Error = ""
}

func (m *CornerstoneManager) applyResolved(workID string, updated *Cornerstone, resolved ResolveResult) *CornerstoneResolution {
	updated.CandidateContent = ""
	resolution := &CornerstoneResolution{ErrorKind: resolved.ErrorKind, Retryable: resolved.ErrorKind == ResolveErrorNetwork}
	switch resolved.ErrorKind {
	case "":
		if resolved.Digest == updated.Digest {
			updated.Status = CornerstoneActive
			updated.ResolveErrorKind = ""
			updated.CandidateDigest = ""
			updated.Error = ""
			return resolution
		}
		updated.Status = CornerstoneStale
		updated.ResolveErrorKind = ResolveErrorChanged
		updated.CandidateDigest = resolved.Digest
		updated.Error = "source content changed; review before accept or freeze"
		accepted, err := m.cornerstoneContent(workID, *updated)
		if err != nil {
			accepted = updated.Content
		}
		resolution.ErrorKind = ResolveErrorChanged
		resolution.CandidateContent = resolved.Content
		resolution.CandidateDigest = resolved.Digest
		resolution.Diff = cornerstoneDiff(accepted, resolved.Content)
	case ResolveErrorMissing:
		updated.Status = CornerstoneMissing
		updated.ResolveErrorKind = ResolveErrorMissing
		updated.CandidateDigest = ""
		updated.Error = "source is missing; repair or retry"
	case ResolveErrorDenied:
		updated.Status = CornerstoneDenied
		updated.ResolveErrorKind = ResolveErrorDenied
		updated.CandidateDigest = ""
		updated.Error = "source access was denied; repair or retry"
	case ResolveErrorNetwork:
		updated.Status = CornerstoneStale
		updated.ResolveErrorKind = ResolveErrorNetwork
		updated.CandidateDigest = ""
		updated.Error = "source could not be verified due to a network failure; retry"
	default:
		updated.Status = CornerstoneInvalid
		updated.ResolveErrorKind = ResolveErrorInvalid
		updated.CandidateDigest = ""
		updated.Error = "source resolution returned invalid content; repair or retry"
	}
	return resolution
}

func cornerstoneDiff(before, after string) string {
	if before == after {
		return ""
	}
	const limit = 2000
	trim := func(value string) string {
		if len(value) <= limit {
			return value
		}
		return value[:limit] + "\n…(truncated)"
	}
	return "--- accepted\n+++ resolved\n-" + strings.ReplaceAll(trim(before), "\n", "\n-") + "\n+" + strings.ReplaceAll(trim(after), "\n", "\n+")
}

// Accept re-resolves the source and accepts only the exact candidate digest
// previously persisted by Refresh. This closes the review/commit TOCTOU gap.
func (m *CornerstoneManager) Accept(ctx context.Context, workID string, input AcceptCornerstoneInput) (*CornerstoneResult, error) {
	return m.accept(ctx, workID, input)
}

func (m *CornerstoneManager) accept(ctx context.Context, workID string, input AcceptCornerstoneInput) (*CornerstoneResult, error) {
	workID, csID, requestID, err := normalizeCornerstoneMutation("Accept", workID, input.CornerstoneID, input.RequestID)
	if err != nil {
		return nil, err
	}
	eventRequestID := cornerstoneMutationRequestID(requestID)
	eventID := cornerstoneEventID("cs-accept", eventRequestID, csID)
	current, state, err := m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		return mutationReplay("Accept", current, state, csID, requestID, eventID)
	}
	if err := validateCornerstoneMutation("Accept", current, state, csID, input.ExpectedRevision); err != nil {
		return nil, err
	}
	cs := findCornerstone(current, csID)
	if cs.Mode != CornerstoneLiveRef || cs.Status != CornerstoneStale || cs.ResolveErrorKind != ResolveErrorChanged || cs.CandidateDigest == "" {
		return nil, fmt.Errorf("cornerstone: Accept: cornerstone %q has no reviewed candidate", csID)
	}
	if m.resolver == nil {
		return nil, ErrCornerstoneResolverUnavailable
	}
	identity, candidateDigest := refIdentity(cs.Ref), cs.CandidateDigest
	resolved, err := m.resolveRef(ctx, workID, cs.Ref)
	if err != nil {
		return nil, err
	}
	if resolved.ErrorKind != "" || resolved.Digest != candidateDigest {
		return nil, ErrCornerstoneCandidateChanged
	}
	current, state, err = m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		return mutationReplay("Accept", current, state, csID, requestID, eventID)
	}
	if input.ExpectedRevision != state.Revision {
		return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}
	cs = findCornerstone(current, csID)
	if cs == nil || cs.Tombstone || refIdentity(cs.Ref) != identity || cs.CandidateDigest != candidateDigest {
		return nil, ErrCornerstoneCandidateChanged
	}
	display, blobDigest, err := m.materializeCornerstoneContent(workID, resolved.Content)
	if err != nil {
		return nil, err
	}
	now := m.clock.Now().UTC()
	updated := *cs
	updated.Content = display
	updated.Digest = resolved.Digest
	updated.Ref.BlobDigest = blobDigest
	updated.Status = CornerstoneActive
	updated.ResolveErrorKind = ""
	updated.Error = ""
	updated.CandidateContent = ""
	updated.CandidateDigest = ""
	updated.LastVerifiedAt = &now
	updated.UpdatedAt = now
	result, err := m.commitCornerstoneUpdate(workID, csID, eventRequestID, "Accept", "cs-accept", eventID, state.Revision, updated, now)
	if err != nil {
		return nil, err
	}
	attachCornerstoneResult(result, nil)
	return result, nil
}

// Freeze converts a live reference to a snapshot. A changed source must match
// the reviewed CandidateDigest unless UseLastKnown is explicit.
func (m *CornerstoneManager) Freeze(ctx context.Context, workID string, input FreezeCornerstoneInput) (*CornerstoneResult, error) {
	workID, csID, requestID, err := normalizeCornerstoneMutation("Freeze", workID, input.CornerstoneID, input.RequestID)
	if err != nil {
		return nil, err
	}
	eventRequestID := cornerstoneMutationRequestID(requestID)
	eventID, err := cornerstoneMutationEventID("cs-freeze", eventRequestID, csID, struct {
		UseLastKnown bool `json:"useLastKnown"`
	}{UseLastKnown: input.UseLastKnown})
	if err != nil {
		return nil, fmt.Errorf("cornerstone: Freeze: fingerprint intent: %w", err)
	}
	current, state, err := m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		return mutationReplay("Freeze", current, state, csID, requestID, eventID)
	}
	if err := validateCornerstoneMutation("Freeze", current, state, csID, input.ExpectedRevision); err != nil {
		return nil, err
	}
	cs := findCornerstone(current, csID)
	if cs.Mode != CornerstoneLiveRef {
		return nil, fmt.Errorf("cornerstone: Freeze: cornerstone %q is not live_ref", csID)
	}

	var content string
	if input.UseLastKnown {
		content, err = m.cornerstoneContent(workID, *cs)
		if err != nil {
			return nil, fmt.Errorf("cornerstone: Freeze: last-known content unavailable; repair first: %w", err)
		}
	} else {
		if m.resolver == nil {
			return nil, ErrCornerstoneResolverUnavailable
		}
		resolved, resolveErr := m.resolveRef(ctx, workID, cs.Ref)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if resolved.ErrorKind != "" {
			return nil, fmt.Errorf("cornerstone: Freeze: source is %s", resolved.ErrorKind)
		}
		expected := cs.Digest
		if cs.ResolveErrorKind == ResolveErrorChanged && cs.CandidateDigest != "" {
			expected = cs.CandidateDigest
		}
		if resolved.Digest != expected {
			return nil, ErrCornerstoneCandidateChanged
		}
		content = resolved.Content
	}
	display, blobDigest, err := m.materializeCornerstoneContent(workID, content)
	if err != nil {
		return nil, err
	}
	current, state, err = m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		return mutationReplay("Freeze", current, state, csID, requestID, eventID)
	}
	if input.ExpectedRevision != state.Revision {
		return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}
	cs = findCornerstone(current, csID)
	if cs == nil || cs.Tombstone || cs.Mode != CornerstoneLiveRef {
		return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}
	now := m.clock.Now().UTC()
	updated := *cs
	if updated.Type == CornerstoneFileRef {
		updated.Type = CornerstoneFileSnapshot
	}
	updated.Mode = CornerstoneSnapshot
	updated.Content = display
	updated.Digest = ContentDigest([]byte(normalizeCornerstoneContent(content)))
	updated.Ref.BlobDigest = blobDigest
	updated.Status = CornerstoneActive
	updated.ResolveErrorKind = ""
	updated.Error = ""
	updated.CandidateContent = ""
	updated.CandidateDigest = ""
	updated.LastVerifiedAt = &now
	updated.UpdatedAt = now
	result, err := m.commitCornerstoneUpdate(workID, csID, eventRequestID, "Freeze", "cs-freeze", eventID, state.Revision, updated, now)
	if err != nil {
		return nil, err
	}
	attachCornerstoneResult(result, nil)
	return result, nil
}

// Repair retries a source, replaces a broken live Ref, or rematerializes a
// snapshot blob. Changed live content remains stale and still requires Accept.
func (m *CornerstoneManager) Repair(ctx context.Context, workID string, input RepairCornerstoneInput) (*RepairResult, error) {
	workID, csID, requestID, err := normalizeCornerstoneMutation("Repair", workID, input.CornerstoneID, input.RequestID)
	if err != nil {
		return nil, err
	}
	if repairInputSecretLike(input) {
		return nil, ErrSecretRejected
	}
	input, intent := normalizeRepairIntent(input)
	eventRequestID := cornerstoneMutationRequestID(requestID)
	eventID, err := cornerstoneMutationEventID("cs-repair", eventRequestID, csID, intent)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: Repair: fingerprint intent: %w", err)
	}
	current, state, err := m.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		return repairReplay(current, state, csID, requestID, eventID)
	}
	if err := validateCornerstoneMutation("Repair", current, state, csID, input.ExpectedRevision); err != nil {
		return nil, err
	}
	cs := findCornerstone(current, csID)
	now := m.clock.Now().UTC()
	updated := *cs
	var resolution *CornerstoneResolution
	failed := []string(nil)

	if cs.Mode == CornerstoneLiveRef {
		if input.Content != nil {
			return nil, errors.New("cornerstone: Repair: content is only valid for snapshot blob repair")
		}
		if m.resolver == nil {
			return nil, ErrCornerstoneResolverUnavailable
		}
		ref := cs.Ref
		if input.Ref != nil {
			ref = normalizedCornerstoneRef(*input.Ref)
			ref.BlobDigest = cs.Ref.BlobDigest
			if err := validateRepairRef(*cs, ref); err != nil {
				return nil, err
			}
		}
		resolved, resolveErr := m.resolveRef(ctx, workID, ref)
		if resolveErr != nil {
			return nil, resolveErr
		}
		current, state, err = m.store.LoadState(workID, eventRequestID)
		if err != nil {
			return nil, err
		}
		if state.RequestFound {
			return repairReplay(current, state, csID, requestID, eventID)
		}
		if input.ExpectedRevision != state.Revision {
			return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
		}
		cs = findCornerstone(current, csID)
		if cs == nil || cs.Tombstone {
			return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
		}
		updated = *cs
		updated.Ref = ref
		resolution = m.applyResolved(workID, &updated, resolved)
		if updated.Status != CornerstoneActive {
			failed = append(failed, string(updated.ResolveErrorKind))
		}
	} else {
		if input.Ref != nil {
			return nil, errors.New("cornerstone: Repair: snapshot Ref is immutable")
		}
		if cs.Ref.BlobDigest == "" {
			applySnapshotVerification(m, workID, &updated)
		} else if input.Content != nil {
			content := normalizeCornerstoneContent(*input.Content)
			if IsSecretLike([]byte(content)) {
				return nil, ErrSecretRejected
			}
			if ContentDigest([]byte(content)) != cs.Digest {
				updated.Status = CornerstoneInvalid
				updated.ResolveErrorKind = ResolveErrorInvalid
				updated.Error = "replacement content does not match the accepted snapshot digest; retry"
				failed = append(failed, "blob:digest")
			} else if m.blobStore == nil {
				updated.Status = CornerstoneInvalid
				updated.ResolveErrorKind = ResolveErrorInvalid
				updated.Error = "blob store is unavailable; retry"
				failed = append(failed, "blob:store")
			} else if digest, putErr := m.blobStore.Put(workID, []byte(content)); putErr != nil || digest != cs.Ref.BlobDigest {
				updated.Status = CornerstoneInvalid
				updated.ResolveErrorKind = ResolveErrorInvalid
				updated.Error = "snapshot blob repair failed; retry"
				failed = append(failed, "blob:write")
			} else {
				updated.Status = CornerstoneActive
				updated.ResolveErrorKind = ""
				updated.Error = ""
			}
		} else {
			applySnapshotVerification(m, workID, &updated)
			if updated.Status != CornerstoneActive {
				failed = append(failed, "blob:missing")
			}
		}
	}

	updated.LastVerifiedAt = &now
	updated.UpdatedAt = now
	result, err := m.commitCornerstoneUpdate(workID, csID, eventRequestID, "Repair", "cs-repair", eventID, state.Revision, updated, now)
	if err != nil {
		return nil, err
	}
	attachCornerstoneResult(result, resolution)
	return &RepairResult{
		Cornerstone: result.Cornerstone,
		WorkView:    result.WorkView,
		Repaired:    result.Cornerstone.Status == CornerstoneActive,
		Revision:    result.Revision,
		FailedRefs:  failed,
		Resolution:  resolution,
		Assessment:  result.Assessment,
	}, nil
}

func normalizeCornerstoneMutation(op, workID, csID, requestID string) (string, string, string, error) {
	workID = strings.TrimSpace(workID)
	csID = strings.TrimSpace(csID)
	requestID = strings.TrimSpace(requestID)
	if workID == "" {
		return "", "", "", fmt.Errorf("cornerstone: %s: workID is required", op)
	}
	if requestID == "" {
		return "", "", "", fmt.Errorf("cornerstone: %s: requestID is required", op)
	}
	if csID == "" {
		return "", "", "", fmt.Errorf("cornerstone: %s: cornerstoneId is required", op)
	}
	return workID, csID, requestID, nil
}

// cornerstoneMutationRequestID deliberately keeps the original Refresh
// namespace. It preserves upgrade replay compatibility while making a caller
// request ID single-use across Resolve, Accept, Freeze, and Repair; the event
// ID below distinguishes the exact operation, object, and intent.
func cornerstoneMutationRequestID(requestID string) string {
	return requestID + "/cs-refresh"
}

func cornerstoneMutationEventID(prefix, requestID, csID string, intent any) (string, error) {
	fingerprint, err := hashCanonical(intent)
	if err != nil {
		return "", err
	}
	return cornerstoneEventID(prefix, requestID, csID+"\x00"+fingerprint), nil
}

type repairIntent struct {
	HasRef        bool           `json:"hasRef"`
	Ref           CornerstoneRef `json:"ref,omitempty"`
	HasContent    bool           `json:"hasContent"`
	ContentDigest string         `json:"contentDigest,omitempty"`
}

func normalizeRepairIntent(input RepairCornerstoneInput) (RepairCornerstoneInput, repairIntent) {
	intent := repairIntent{}
	if input.Ref != nil {
		ref := normalizedCornerstoneRef(*input.Ref)
		// BlobDigest belongs to the accepted snapshot/content state. A live-ref
		// repair cannot replace it through caller input.
		ref.BlobDigest = ""
		input.Ref = &ref
		intent.HasRef = true
		intent.Ref = ref
	}
	if input.Content != nil {
		content := normalizeCornerstoneContent(*input.Content)
		input.Content = &content
		intent.HasContent = true
		intent.ContentDigest = ContentDigest([]byte(content))
	}
	return input, intent
}

func validateCornerstoneMutation(op string, current *Work, state WorkEventState, csID string, expectedRevision int64) error {
	if current.ArchiveState != ArchiveActive {
		return fmt.Errorf("cornerstone: %s: Work %s is %s", op, current.ID, current.ArchiveState)
	}
	if expectedRevision != state.Revision {
		return revisionConflict(current.ID, expectedRevision, state.Revision)
	}
	cs := findCornerstone(current, csID)
	if cs == nil || cs.Tombstone {
		return fmt.Errorf("cornerstone: %s: cornerstone %q not found or is removed", op, csID)
	}
	return nil
}

func mutationReplay(op string, current *Work, state WorkEventState, csID, requestID, eventID string) (*CornerstoneResult, error) {
	if err := validateCornerstoneReplay(op, current.ID, requestID, state, EventCornerstoneUpserted, eventID); err != nil {
		return nil, err
	}
	cs := findCornerstone(current, csID)
	if cs == nil {
		return nil, fmt.Errorf("cornerstone: %s: request %q committed but cornerstone %q is missing", op, requestID, csID)
	}
	result := &CornerstoneResult{Cornerstone: cs, WorkView: viewFromState(current, state), Duplicate: true, Revision: state.Revision}
	attachCornerstoneResult(result, nil)
	return result, nil
}

func repairReplay(current *Work, state WorkEventState, csID, requestID, eventID string) (*RepairResult, error) {
	result, err := mutationReplay("Repair", current, state, csID, requestID, eventID)
	if err != nil {
		return nil, err
	}
	return &RepairResult{
		Cornerstone: result.Cornerstone,
		WorkView:    result.WorkView,
		Repaired:    result.Cornerstone.Status == CornerstoneActive,
		Duplicate:   true,
		Revision:    result.Revision,
		Assessment:  result.Assessment,
	}, nil
}

func validateCornerstoneReplay(op, workID, requestID string, state WorkEventState, eventType WorkEventType, eventID string) error {
	if state.RequestType == eventType && state.RequestEventID == eventID {
		return nil
	}
	return fmt.Errorf(
		"%w: cornerstone: %s request %q was already committed for a different operation, object, or intent",
		ErrWorkRequestIDConflict, op, requestID,
	)
}

func (m *CornerstoneManager) commitCornerstoneUpdate(workID, csID, requestID, op, prefix, eventID string, baseRevision int64, cs Cornerstone, now time.Time) (*CornerstoneResult, error) {
	callerRequestID := strings.TrimSuffix(requestID, "/cs-refresh")
	cs.CandidateContent = ""
	payload, err := json.Marshal(cs)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: marshal update: %w", err)
	}
	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            eventID,
		RequestID:     requestID,
		WorkID:        workID,
		Type:          EventCornerstoneUpserted,
		BaseRevision:  baseRevision,
		Revision:      baseRevision + 1,
		Payload:       json.RawMessage(payload),
		WriterID:      WorkWriterID(),
		CreatedAt:     now,
	}
	committedRevision, err := m.store.CommitEvent(workID, event)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: commit update: %w", err)
	}
	current, state, err := m.store.LoadState(workID, requestID)
	if err != nil {
		return nil, committedRecovery("cornerstone-"+prefix+"-view", workID, callerRequestID, committedRevision, err)
	}
	if !state.RequestFound {
		return nil, committedRecovery(
			"cornerstone-"+prefix+"-request",
			workID,
			callerRequestID,
			committedRevision,
			errors.New("committed request is missing from the authoritative request index"),
		)
	}
	if err := validateCornerstoneReplay(op, workID, callerRequestID, state, EventCornerstoneUpserted, eventID); err != nil {
		return nil, err
	}
	result := findCornerstone(current, csID)
	if result == nil {
		return nil, fmt.Errorf("cornerstone: cornerstone %q disappeared after update", csID)
	}
	view := viewFromState(current, state)
	return &CornerstoneResult{Cornerstone: result, WorkView: view, Revision: view.Revision}, nil
}

func attachCornerstoneResult(result *CornerstoneResult, resolution *CornerstoneResolution) {
	if result == nil || result.Cornerstone == nil {
		return
	}
	result.Resolution = resolution
	if resolution != nil {
		result.Cornerstone.CandidateContent = resolution.CandidateContent
	}
	result.Assessment = AssessCornerstones([]Cornerstone{*result.Cornerstone})
}

// AssessCornerstones converts persisted statuses into the policy consumed by
// open/run/preflight callers. It performs no I/O and owns no state.
func AssessCornerstones(items []Cornerstone) CornerstoneAssessment {
	assessment := CornerstoneAssessment{State: CornerstoneUseReady}
	for _, cs := range items {
		if cs.Tombstone || cs.Status == CornerstoneActive {
			continue
		}
		issue := CornerstoneIssue{
			CornerstoneID: cs.ID,
			Title:         cs.Title,
			Problem:       string(cs.Status),
			Blocking:      cs.Required,
		}
		if cs.ResolveErrorKind != "" {
			issue.Problem += ":" + string(cs.ResolveErrorKind)
		}
		assessment.Issues = append(assessment.Issues, issue)
		if cs.Required {
			assessment.Blocking = true
		} else {
			assessment.Degraded = true
		}
	}
	switch {
	case assessment.Blocking:
		assessment.State = CornerstoneUseBlocked
	case assessment.Degraded:
		assessment.State = CornerstoneUseDegraded
	}
	return assessment
}

func (m *CornerstoneManager) materializeCornerstoneContent(workID, content string) (string, string, error) {
	content = normalizeCornerstoneContent(content)
	if IsSecretLike([]byte(content)) {
		return "", "", ErrSecretRejected
	}
	if len(content) <= CornerstoneInlineThreshold {
		return content, "", nil
	}
	if m.blobStore == nil {
		return "", "", errors.New("cornerstone: blob store is required for large content")
	}
	digest := ContentDigest([]byte(content))
	stored, err := m.blobStore.Put(workID, []byte(content))
	if err != nil {
		return "", "", fmt.Errorf("cornerstone: blob write failed: %w", err)
	}
	if stored != digest {
		return "", "", fmt.Errorf("cornerstone: blob digest mismatch: got %q, want %q", stored, digest)
	}
	return truncateContentPreview(content), digest, nil
}

func (m *CornerstoneManager) cornerstoneContent(workID string, cs Cornerstone) (string, error) {
	if cs.Ref.BlobDigest == "" {
		return normalizeCornerstoneContent(cs.Content), nil
	}
	if m.blobStore == nil {
		return "", errors.New("blob store unavailable")
	}
	content, err := m.blobStore.Get(workID, cs.Ref.BlobDigest)
	if err != nil {
		return "", err
	}
	return normalizeCornerstoneContent(string(content)), nil
}

func validateRepairRef(cs Cornerstone, ref CornerstoneRef) error {
	probe := PinCornerstoneInput{
		Type:    cs.Type,
		Title:   cs.Title,
		Content: cs.Content,
		Ref:     ref,
		Mode:    cs.Mode,
	}
	probe.Ref.BlobDigest = ""
	return validateCornerstoneInput(probe)
}

func repairInputSecretLike(input RepairCornerstoneInput) bool {
	if input.Ref != nil {
		ref := *input.Ref
		for _, value := range []string{ref.Kind, ref.SessionID, ref.Path, ref.ArtifactID, ref.URL, ref.BlobDigest} {
			if IsSecretLike([]byte(value)) {
				return true
			}
		}
	}
	return input.Content != nil && IsSecretLike([]byte(*input.Content))
}
