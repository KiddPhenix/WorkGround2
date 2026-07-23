package work

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ── Block digest ────────────────────────────────────────────────────────────

// blockContentDigest computes a stable digest over the semantic fields of a
// BlockInstance, excluding ID (used as the lookup key), Revision,
// CreatedAt, and UpdatedAt.
func blockContentDigest(b *BlockInstance) (string, error) {
	if b == nil {
		return "", fmt.Errorf("work: cannot compute digest of nil BlockInstance")
	}
	content := struct {
		Kind          string            `json:"kind"`
		SchemaVersion int               `json:"schemaVersion"`
		Title         string            `json:"title,omitempty"`
		Status        BlockStatus       `json:"status"`
		Data          json.RawMessage   `json:"data"`
		Actions       []BlockActionSpec `json:"actions,omitempty"`
		Source        BlockSource       `json:"source"`
		Freshness     *BlockFreshness   `json:"freshness,omitempty"`
		Fallback      BlockFallback     `json:"fallback"`
		Tombstone     bool              `json:"tombstone,omitempty"`
	}{
		Kind:          b.Kind,
		SchemaVersion: b.SchemaVersion,
		Title:         b.Title,
		Status:        b.Status,
		Data:          b.Data,
		Actions:       b.Actions,
		Source:        b.Source,
		Freshness:     b.Freshness,
		Fallback:      b.Fallback,
		Tombstone:     b.Tombstone,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("work: marshal block content: %w", err)
	}
	var generic any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return "", fmt.Errorf("work: canonicalise block content: %w", err)
	}
	canon, err := json.Marshal(generic)
	if err != nil {
		return "", fmt.Errorf("work: re-marshal block content: %w", err)
	}
	h := sha256.Sum256(canon)
	return fmt.Sprintf("sha256:%x", h[:]), nil
}

// ErrBlockConflict describes a retryable optimistic-lock or merge conflict.
// Service methods return the latest WorkView alongside this error.
type ErrBlockConflict struct {
	WorkID               string `json:"workId"`
	BlockID              string `json:"blockId,omitempty"`
	Reason               string `json:"reason"`
	IncomingRevision     int64  `json:"incomingRevision,omitempty"`
	CurrentRevision      int64  `json:"currentRevision,omitempty"`
	ExpectedWorkRevision int64  `json:"expectedWorkRevision,omitempty"`
	CurrentWorkRevision  int64  `json:"currentWorkRevision,omitempty"`
	Retryable            bool   `json:"retryable"`
	cause                error
}

func (e *ErrBlockConflict) Error() string {
	if e == nil {
		return "work: block conflict"
	}
	return fmt.Sprintf("work: block conflict on %s/%s: %s (block revision %d, work revision %d)",
		e.WorkID, e.BlockID, e.Reason, e.CurrentRevision, e.CurrentWorkRevision)
}

func (e *ErrBlockConflict) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// inputToBlock builds a BlockInstance from the upsert input fields.
func inputToBlock(input BlockUpsertInput) BlockInstance {
	return BlockInstance{
		ID:            input.BlockID,
		Kind:          input.Kind,
		SchemaVersion: input.SchemaVersion,
		Revision:      input.Revision,
		Title:         input.Title,
		Status:        input.Status,
		Data:          input.Data,
		Actions:       input.Actions,
		Source:        input.Source,
		Freshness:     input.Freshness,
		Fallback:      input.Fallback,
		Tombstone:     input.Tombstone,
	}
}

// ── Block spec matching ────────────────────────────────────────────────────

// blockSpecForWork returns the BlockSpec with the given ID from the Work's
// current DefinitionSnapshot, or nil if not found.
func blockSpecForWork(w *Work, blockID string) *BlockSpec {
	for i := range w.Definition.BlockSpecs {
		if w.Definition.BlockSpecs[i].ID == blockID {
			return &w.Definition.BlockSpecs[i]
		}
	}
	return nil
}

// validateBlockSpecMatch checks that kind and schemaVersion match the blueprint
// spec. Returns an error if the block is part of the definition but the values
// differ — users must not change kind/schema.
func validateBlockSpecMatch(spec *BlockSpec, kind string, schemaVersion int) error {
	if spec == nil {
		return nil // block not in definition; allow with caller-controlled guard
	}
	if spec.Kind != kind {
		return fmt.Errorf("work: block %s kind %q does not match spec kind %q", spec.ID, kind, spec.Kind)
	}
	if spec.SchemaVersion != schemaVersion {
		return fmt.Errorf("work: block %s schemaVersion %d does not match spec schemaVersion %d",
			spec.ID, schemaVersion, spec.SchemaVersion)
	}
	return nil
}

// isUserEditable reports whether the block spec allows user edits.
func isUserEditable(spec *BlockSpec) bool {
	return spec != nil && spec.Editable
}

// ── Block merge logic ──────────────────────────────────────────────────────

// blockMergeResult encodes the outcome of comparing an incoming block against
// the current projection.
type blockMergeResult int

const (
	blockMergeNew        blockMergeResult = iota // block does not exist yet
	blockMergeSkipOlder                          // incoming revision < current
	blockMergeIdempotent                         // same revision + same digest
	blockMergeConflict                           // same revision + different content
	blockMergeApplied                            // incoming revision > current
)

// mergeBlock compares incoming against current and returns the result.
// If result is blockMergeApplied, the merged block is returned.
// For blockMergeIdempotent, current is returned (caller should still validate).
func mergeBlock(current *BlockInstance, incoming *BlockInstance, incomingDigest string) (blockMergeResult, *BlockInstance, error) {
	if current == nil {
		return blockMergeNew, incoming, nil
	}
	if incoming.Revision < current.Revision {
		return blockMergeSkipOlder, current, nil
	}
	if incoming.Revision == current.Revision {
		currentDigest, err := blockContentDigest(current)
		if err != nil {
			return 0, current, err
		}
		if currentDigest == incomingDigest {
			return blockMergeIdempotent, current, nil
		}
		return blockMergeConflict, current, nil
	}
	// incoming.Revision > current.Revision: old events cannot revive a
	// tombstone. A deliberate higher-revision upsert may supersede it.
	return blockMergeApplied, incoming, nil
}

// ── Placement sorting ──────────────────────────────────────────────────────

// sortPlacements returns a sorted copy by (Slot, Order, BlockID). BlockID is a
// deterministic tie-breaker, so equivalent sets converge regardless of input
// delivery order.
func sortPlacements(placements []BlockPlacement) []BlockPlacement {
	if len(placements) <= 1 {
		out := make([]BlockPlacement, len(placements))
		copy(out, placements)
		return out
	}
	out := make([]BlockPlacement, len(placements))
	copy(out, placements)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Slot != out[j].Slot {
			return out[i].Slot < out[j].Slot
		}
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].BlockID < out[j].BlockID
	})
	return out
}

func validatePlacementShape(placements []BlockPlacement) error {
	seen := make(map[string]bool, len(placements))
	for i, placement := range placements {
		placement.BlockID = strings.TrimSpace(placement.BlockID)
		if placement.BlockID == "" {
			return fmt.Errorf("work: placement[%d]: blockId is required", i)
		}
		if seen[placement.BlockID] {
			return fmt.Errorf("work: placement[%d]: duplicate blockId %s", i, placement.BlockID)
		}
		seen[placement.BlockID] = true
		if !validPlacementSlot(placement.Slot) {
			return fmt.Errorf("work: placement[%d]: unsupported slot %q", i, placement.Slot)
		}
		if placement.Order < 0 {
			return fmt.Errorf("work: placement[%d]: order must be non-negative", i)
		}
		// Span zero is the V1 omitted/default value (the field is omitempty);
		// explicit negative spans are always invalid.
		if placement.Span < 0 {
			return fmt.Errorf("work: placement[%d]: span must be non-negative", i)
		}
	}
	return nil
}

// validateBlockPlacements is the single online/replay validation path for a
// complete placement replacement. It returns a normalized, deterministic copy.
func validateBlockPlacements(w *Work, placements []BlockPlacement) ([]BlockPlacement, error) {
	if w == nil {
		return nil, fmt.Errorf("work: placements require a Work projection")
	}
	normalized := normalizeBlockPlacements(placements)
	if err := validatePlacementShape(normalized); err != nil {
		return nil, err
	}
	blocks := make(map[string]BlockInstance, len(w.Blocks))
	for _, block := range w.Blocks {
		blocks[block.ID] = block
	}
	for _, placement := range normalized {
		block, ok := blocks[placement.BlockID]
		if !ok {
			return nil, fmt.Errorf("work: placement references unknown block %s", placement.BlockID)
		}
		if block.Tombstone {
			return nil, fmt.Errorf("work: placement references tombstoned block %s", placement.BlockID)
		}
	}
	current := make(map[string]BlockPlacement, len(w.Placements))
	for _, placement := range normalizeBlockPlacements(w.Placements) {
		current[placement.BlockID] = placement
	}
	next := make(map[string]BlockPlacement, len(normalized))
	for _, placement := range normalized {
		next[placement.BlockID] = placement
	}
	for _, block := range w.Blocks {
		if isUserEditable(blockSpecForWork(w, block.ID)) {
			continue
		}
		before, hadBefore := current[block.ID]
		after, hasAfter := next[block.ID]
		if hadBefore != hasAfter || (hadBefore && !placementEqual(before, after)) {
			return nil, fmt.Errorf("work: block %s is not user-editable", block.ID)
		}
	}
	return sortPlacements(normalized), nil
}

func normalizeBlockPlacements(placements []BlockPlacement) []BlockPlacement {
	normalized := append([]BlockPlacement(nil), placements...)
	for i := range normalized {
		normalized[i].BlockID = strings.TrimSpace(normalized[i].BlockID)
		normalized[i].Slot = strings.TrimSpace(normalized[i].Slot)
	}
	return normalized
}

func validPlacementSlot(slot string) bool {
	switch slot {
	case "primary", "secondary", "attention", "result":
		return true
	default:
		return false
	}
}

func placementEqual(left, right BlockPlacement) bool {
	return left.BlockID == right.BlockID && left.Slot == right.Slot &&
		left.Order == right.Order && left.Span == right.Span && left.Collapsed == right.Collapsed
}

func placementSetsEqual(left, right []BlockPlacement) bool {
	if len(left) != len(right) {
		return false
	}
	left = sortPlacements(left)
	right = sortPlacements(right)
	for i := range left {
		if !placementEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}

// requireWritableBlockSchemas keeps reads available for unknown future block
// schemas while making every Service mutation explicitly read-only.
func requireWritableBlockSchemas(w *Work) error {
	if w == nil {
		return fmt.Errorf("work: writable block schema check requires a Work projection")
	}
	for i := range w.Blocks {
		block := &w.Blocks[i]
		if err := CheckSchemaVersion("BlockInstance", block.SchemaVersion); err != nil {
			return fmt.Errorf("work: Work %s block %s is read-only: %w", w.ID, block.ID, err)
		}
	}
	return nil
}

func revisionChainConflict(err error) bool {
	var conflict *ErrWorkEventConflict
	if !errors.As(err, &conflict) {
		return false
	}
	if conflict.Kind != "" {
		return conflict.Kind == WorkEventRevisionConflict
	}
	// Compatibility for stores that have not populated Kind yet: a same-ID
	// fingerprint conflict is the only conflict that must not be re-merged.
	return conflict.RequestID == "" || !strings.Contains(conflict.Reason, "already used")
}
