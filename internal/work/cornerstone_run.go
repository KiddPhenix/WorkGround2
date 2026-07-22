package work

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// unavailableCornerstoneResolver makes missing production source adapters an
// observable, retryable preflight failure instead of trusting last-known text.
type unavailableCornerstoneResolver struct{}

func (unavailableCornerstoneResolver) Resolve(context.Context, CornerstoneRef) (ResolveResult, error) {
	return ResolveResult{}, &ResolverError{
		Kind:      ResolveErrorNetwork,
		Retryable: true,
		Err:       ErrCornerstoneResolverUnavailable,
	}
}

// resolveRunCornerstones re-resolves every non-tombstone live reference once
// per Run request. Stable per-cornerstone request IDs make retries and restart
// replay safe while the Work run flight serializes same-process execution.
func (s *Service) resolveRunCornerstones(ctx context.Context, current *Work, requestID string) (*Work, error) {
	if current == nil || s.cornerstones == nil {
		return current, nil
	}
	ids := make([]string, 0, len(current.Cornerstones))
	for _, cs := range activeCornerstonesDeduped(current.Cornerstones) {
		if cs.Mode == CornerstoneLiveRef {
			ids = append(ids, cs.ID)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		for attempts := 0; ; attempts++ {
			if err := checkServiceContext(ctx); err != nil {
				return nil, err
			}
			latest, state, err := s.store.LoadState(current.ID, "")
			if err != nil {
				return nil, err
			}
			cs := findCornerstone(latest, id)
			if cs == nil || cs.Tombstone || cs.Mode != CornerstoneLiveRef {
				current = latest
				break
			}
			result, err := s.cornerstones.Resolve(ctx, current.ID, RefreshCornerstoneInput{
				CornerstoneID:    id,
				ExpectedRevision: state.Revision,
				RequestID:        requestID + "/cornerstone-preflight/" + id,
			})
			if err == nil {
				current = result.WorkView.Work
				break
			}
			var conflict *ErrWorkEventConflict
			if errors.As(err, &conflict) && conflict.Kind == WorkEventRevisionConflict && attempts < 7 {
				continue
			}
			return nil, fmt.Errorf("resolve live_ref %q: %w", id, err)
		}
	}
	latest, _, err := s.store.LoadState(current.ID, "")
	if err != nil {
		return nil, err
	}
	return latest, nil
}
