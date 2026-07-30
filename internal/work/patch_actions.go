package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// reconcilePatchArtifacts carries the outputs of coordinator-reused completed
// nodes into the new definition revision. A reformat action converts only the
// target slot through the executor's local materialiser; it never starts a
// model turn or repeats node capabilities.
func (c *V2Coordinator) reconcilePatchArtifacts(
	ctx context.Context,
	workID, runID, requestID string,
	preview WorkPatchPreview,
	definition *WorkDefinitionRevision,
) error {
	if c == nil || definition == nil || strings.TrimSpace(runID) == "" {
		return nil
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	runtimes := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, runID)
	reformat := make(map[string]bool)
	for _, action := range preview.Actions {
		if action.Action == PatchActionReformat {
			reformat[action.ArtifactSlotID] = true
		}
	}
	for _, node := range definition.Nodes {
		runtime := runtimes[node.ID]
		if runtime == nil || runtime.State != TaskCompleted {
			continue
		}
		for _, slotID := range node.ProducesSlotIDs {
			target, _ := FindArtifactSlotRevision(projection, definition.Revision, slotID)
			if target == nil || v2ArtifactDelivered(target) {
				continue
			}
			source, _ := FindArtifactSlotRevision(projection, preview.BaseDefinitionRev, slotID)
			if !v2ArtifactDelivered(source) {
				return fmt.Errorf("coordinator reused node %q but source artifact %q is unavailable",
					node.ID, slotID)
			}
			refs := append([]ArtifactRef(nil), source.ArtifactRefs...)
			summary := source.Summary
			operation := "reuse"
			if reformat[slotID] {
				formatter := c.patchArtifactReformatter()
				if formatter == nil {
					return fmt.Errorf("artifact %q requires reformat but the executor has no local reformatter", slotID)
				}
				refs, err = formatter.ReformatTaskArtifacts(ctx, ArtifactReformatInput{
					WorkID:     workID,
					RequestID:  requestID + "/reformat/" + slotID,
					SourceRefs: append([]ArtifactRef(nil), source.ArtifactRefs...),
					Target:     *target,
				})
				if err != nil {
					return fmt.Errorf("reformat artifact %q: %w", slotID, err)
				}
				summary = "Reformatted from the previous definition without rerunning its producer."
				operation = "reformat"
			} else if source.Kind != target.Kind {
				return fmt.Errorf("coordinator reused node %q but artifact %q changed kind without reformat action",
					node.ID, slotID)
			}
			if err := c.commitCarriedArtifact(
				ctx,
				workID,
				definition.Revision,
				requestID+"/"+operation+"/"+slotID,
				target,
				refs,
				source.UpstreamDigest,
				summary,
			); err != nil {
				return err
			}
			projection, err = c.store.LoadProjection(workID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *V2Coordinator) recoverPatchArtifacts(
	ctx context.Context,
	workID, runID string,
	projection *Work,
	definition *WorkDefinitionRevision,
) error {
	if projection == nil || definition == nil {
		return nil
	}
	createdBy := strings.TrimPrefix(strings.TrimSpace(definition.CreatedBy), "patch:")
	patchID, requestID, ok := strings.Cut(createdBy, "/request:")
	if !ok || strings.TrimSpace(patchID) == "" || strings.TrimSpace(requestID) == "" {
		return nil
	}
	preview, ok := projection.V2PatchPreviews[patchID]
	if !ok {
		return fmt.Errorf("recover patch artifacts: preview %q is unavailable", patchID)
	}
	return c.reconcilePatchArtifacts(ctx, workID, runID, requestID, preview, definition)
}

func (c *V2Coordinator) patchArtifactReformatter() TaskArtifactReformatter {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.scheduler == nil {
		return nil
	}
	formatter, _ := c.scheduler.executor.(TaskArtifactReformatter)
	return formatter
}

func (c *V2Coordinator) commitCarriedArtifact(
	ctx context.Context,
	workID string,
	definitionRev int64,
	requestID string,
	target *ArtifactSlot,
	refs []ArtifactRef,
	upstreamDigest, summary string,
) error {
	if target == nil {
		return errors.New("carried artifact target is unavailable")
	}
	service := &Service{store: c.store}
	for tries := 0; tries < 4; tries++ {
		projection, state, err := c.store.LoadState(workID, artifactSlotRequestID(requestID))
		if err != nil {
			return err
		}
		if state.RequestFound {
			return nil
		}
		current, _ := FindArtifactSlotRevision(projection, definitionRev, target.ID)
		if current == nil {
			return fmt.Errorf("carried artifact target %q at definition revision %d is unavailable",
				target.ID, definitionRev)
		}
		if v2ArtifactDelivered(current) {
			return nil
		}
		_, err = service.UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
			WorkID:           workID,
			SlotID:           target.ID,
			RequestID:        requestID,
			State:            SlotReady,
			Refs:             append([]ArtifactRef(nil), refs...),
			UpstreamDigest:   upstreamDigest,
			Summary:          summary,
			Revision:         current.Revision + 1,
			ExpectedRevision: state.Revision,
			DefinitionRev:    definitionRev,
		})
		if err == nil {
			return nil
		}
		var conflict *ErrWorkEventConflict
		if !errors.As(err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
			return err
		}
	}
	return fmt.Errorf("carried artifact %q exceeded conflict retries", target.ID)
}
