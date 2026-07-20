package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── BlueprintRegistry ──────────────────────────────────────────────────────

// BlueprintRegistry loads, indexes, and validates WorkBlueprints from system
// built-ins, user definitions, and AddOns. It is safe for concurrent use.
type BlueprintRegistry struct {
	mu       sync.RWMutex
	byID     map[string][]*WorkBlueprint // id → versions, sorted by Version asc
	diskKeys map[string]map[blueprintKey]struct{}
}

type blueprintKey struct {
	id      string
	version int
}

// NewBlueprintRegistry returns a registry pre-populated with the four V1
// system built-in blueprints.
func NewBlueprintRegistry() *BlueprintRegistry {
	r := &BlueprintRegistry{
		byID:     make(map[string][]*WorkBlueprint),
		diskKeys: make(map[string]map[blueprintKey]struct{}),
	}
	for _, bp := range builtinBlueprints() {
		if err := r.registerLocked(bp); err != nil {
			panic(fmt.Sprintf("work: builtin blueprint %s v%d is invalid: %v", bp.ID, bp.Version, err))
		}
	}
	return r
}

// ── Registration ───────────────────────────────────────────────────────────

// Register validates and adds a blueprint. Duplicate ID + Version is an error.
func (r *BlueprintRegistry) Register(bp *WorkBlueprint) error {
	if bp == nil {
		return errors.New("work: cannot register nil blueprint")
	}
	if err := ValidateBlueprint(bp); err != nil {
		return fmt.Errorf("work: register blueprint %s v%d: %w", bp.ID, bp.Version, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registerLocked(bp)
}

func (r *BlueprintRegistry) registerLocked(bp *WorkBlueprint) error {
	versions := r.byID[bp.ID]
	for _, v := range versions {
		if v.Version == bp.Version {
			return fmt.Errorf("work: duplicate blueprint id=%s version=%d (existing created at %s)", bp.ID, bp.Version, v.CreatedAt.Format(time.RFC3339))
		}
	}
	cp := deepCopyBlueprint(bp)
	versions = append(versions, cp)
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
	r.byID[bp.ID] = versions
	return nil
}

// ── Lookup ─────────────────────────────────────────────────────────────────

// LookupLatest returns the highest-version blueprint for id.
func (r *BlueprintRegistry) LookupLatest(id string) (*WorkBlueprint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions := r.byID[id]
	if len(versions) == 0 {
		return nil, fmt.Errorf("work: blueprint %q not found", id)
	}
	return deepCopyBlueprint(versions[len(versions)-1]), nil
}

// LookupExact returns the blueprint with the exact id + version.
func (r *BlueprintRegistry) LookupExact(id string, version int) (*WorkBlueprint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, bp := range r.byID[id] {
		if bp.Version == version {
			return deepCopyBlueprint(bp), nil
		}
	}
	return nil, fmt.Errorf("work: blueprint %q version %d not found", id, version)
}

// LookupRef resolves all three stable reference fields. A matching business
// version with a different serialization schema is an explicit compatibility
// error rather than a silent fallback.
func (r *BlueprintRegistry) LookupRef(ref BlueprintRef) (*WorkBlueprint, error) {
	if err := validateBlueprintID(ref.ID); err != nil {
		return nil, fmt.Errorf("work: invalid blueprint ref: %w", err)
	}
	if ref.SchemaVersion <= 0 || ref.Version <= 0 {
		return nil, fmt.Errorf("work: invalid blueprint ref versions: schemaVersion=%d version=%d", ref.SchemaVersion, ref.Version)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, bp := range r.byID[ref.ID] {
		if bp.Version != ref.Version {
			continue
		}
		if bp.SchemaVersion != ref.SchemaVersion {
			return nil, fmt.Errorf("work: blueprint %q version %d schemaVersion mismatch: requested %d, registered %d", ref.ID, ref.Version, ref.SchemaVersion, bp.SchemaVersion)
		}
		return deepCopyBlueprint(bp), nil
	}
	return nil, fmt.Errorf("work: blueprint %q version %d not found", ref.ID, ref.Version)
}

// List returns all registered blueprints sorted by (id, version).
func (r *BlueprintRegistry) List() []*WorkBlueprint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listLocked()
}

func (r *BlueprintRegistry) listLocked() []*WorkBlueprint {
	var all []*WorkBlueprint
	for _, versions := range r.byID {
		for _, bp := range versions {
			all = append(all, deepCopyBlueprint(bp))
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ID != all[j].ID {
			return all[i].ID < all[j].ID
		}
		return all[i].Version < all[j].Version
	})
	return all
}

// ListBySource returns blueprints matching the given source.
func (r *BlueprintRegistry) ListBySource(source BlueprintSource) []*WorkBlueprint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*WorkBlueprint
	for _, versions := range r.byID {
		for _, bp := range versions {
			if bp.Source == source {
				out = append(out, deepCopyBlueprint(bp))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// ── Loading from disk ──────────────────────────────────────────────────────

// LoadFromDir reads blueprints/index.json and the referenced definition files
// from dir. Loading is transactional: on any error the registry is unchanged.
// Repeated calls with the same directory are safe and refresh disk entries
// while preserving non-disk registered items. Duplicate ID+Version within the
// same index is an explicit error. Conflicts between disk entries and
// non-disk registrations are also errors.
func (r *BlueprintRegistry) LoadFromDir(dir string) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("work: resolve blueprint directory %s: %w", dir, err)
	}
	root = filepath.Clean(root)
	indexPath := filepath.Join(root, "index.json")
	data, err := os.ReadFile(indexPath)
	index := blueprintIndex{SchemaVersion: SchemaVersion}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("work: read blueprint index %s: %w", indexPath, err)
		}
	} else if err := json.Unmarshal(data, &index); err != nil {
		return fmt.Errorf("work: parse blueprint index %s: %w", indexPath, err)
	}
	if index.SchemaVersion <= 0 {
		return fmt.Errorf("work: blueprint index %s has invalid schemaVersion %d (must be >= 1)", indexPath, index.SchemaVersion)
	}
	if index.SchemaVersion > SchemaVersion {
		return fmt.Errorf("work: blueprint index %s has future schemaVersion %d > current %d", indexPath, index.SchemaVersion, SchemaVersion)
	}

	// Phase 1: read + validate all entries into a temporary map.
	loaded := make(map[string][]*WorkBlueprint)
	seen := make(map[string]bool) // "id@version" for dup detection within this index.
	for i, entry := range index.Entries {
		if entry.ID == "" {
			return fmt.Errorf("work: blueprint index entry %d: id is empty", i)
		}
		if entry.Version < 1 {
			return fmt.Errorf("work: blueprint index entry %d (%s): version must be >= 1", i, entry.ID)
		}
		if entry.Path == "" {
			return fmt.Errorf("work: blueprint index entry %d (%s): path is empty", i, entry.ID)
		}
		if filepath.IsAbs(entry.Path) {
			return fmt.Errorf("work: blueprint index entry %d (%s): path %q must be relative", i, entry.ID, entry.Path)
		}
		if entry.SchemaVersion <= 0 {
			return fmt.Errorf("work: blueprint index entry %d (%s): schemaVersion must be >= 1", i, entry.ID)
		}
		if entry.SchemaVersion > SchemaVersion {
			return fmt.Errorf("work: blueprint index entry %d (%s): future schemaVersion %d", i, entry.ID, entry.SchemaVersion)
		}

		dupKey := fmt.Sprintf("%s@%d", entry.ID, entry.Version)
		if seen[dupKey] {
			return fmt.Errorf("work: blueprint index entry %d: duplicate id=%s version=%d within index", i, entry.ID, entry.Version)
		}
		seen[dupKey] = true

		defPath, err := resolveBlueprintPath(root, entry.Path)
		if err != nil {
			return fmt.Errorf("work: blueprint index entry %d (%s): %w", i, entry.ID, err)
		}
		defData, err := os.ReadFile(defPath)
		if err != nil {
			return fmt.Errorf("work: blueprint index entry %d (%s): read %s: %w", i, entry.ID, defPath, err)
		}
		var bp WorkBlueprint
		if err := json.Unmarshal(defData, &bp); err != nil {
			return fmt.Errorf("work: blueprint index entry %d (%s): parse %s: %w", i, entry.ID, defPath, err)
		}
		if bp.ID != entry.ID {
			return fmt.Errorf("work: blueprint index entry %d: id mismatch — index says %q, file says %q", i, entry.ID, bp.ID)
		}
		if bp.Version != entry.Version {
			return fmt.Errorf("work: blueprint index entry %d (%s): version mismatch — index says %d, file says %d", i, entry.ID, entry.Version, bp.Version)
		}
		if bp.SchemaVersion != entry.SchemaVersion {
			return fmt.Errorf("work: blueprint index entry %d (%s): schemaVersion mismatch — index says %d, file says %d", i, entry.ID, entry.SchemaVersion, bp.SchemaVersion)
		}
		if err := ValidateBlueprint(&bp); err != nil {
			return fmt.Errorf("work: blueprint index entry %d (%s v%d): %w", i, bp.ID, bp.Version, err)
		}
		if bp.Source == BlueprintSystem {
			return fmt.Errorf("work: blueprint index entry %d (%s v%d): disk definitions cannot claim system source", i, bp.ID, bp.Version)
		}

		loaded[bp.ID] = append(loaded[bp.ID], deepCopyBlueprint(&bp))
	}

	// Phase 2: build a complete replacement under the write lock, then swap it
	// in one assignment. Errors leave the current registry untouched.
	r.mu.Lock()
	defer r.mu.Unlock()
	next := cloneBlueprintMap(r.byID)
	for key := range r.diskKeys[root] {
		removeBlueprint(next, key)
	}
	newKeys := make(map[blueprintKey]struct{}, len(seen))
	for id, bps := range loaded {
		for _, bp := range bps {
			key := blueprintKey{id: id, version: bp.Version}
			if hasBlueprint(next, key) {
				return fmt.Errorf("work: blueprint id=%s v%d conflicts with a registration from another source", id, bp.Version)
			}
			next[id] = append(next[id], deepCopyBlueprint(bp))
			newKeys[key] = struct{}{}
		}
		sort.Slice(next[id], func(i, j int) bool { return next[id][i].Version < next[id][j].Version })
	}
	r.byID = next
	if len(newKeys) == 0 {
		delete(r.diskKeys, root)
	} else {
		r.diskKeys[root] = newKeys
	}
	return nil
}

func resolveBlueprintPath(root, indexedPath string) (string, error) {
	clean := filepath.Clean(indexedPath)
	if clean == "." {
		return "", fmt.Errorf("path %q does not name a definition file", indexedPath)
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes directory", indexedPath)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve directory %s: %w", root, err)
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", indexedPath, err)
	}
	realRel, err := filepath.Rel(realRoot, realFull)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside directory", indexedPath)
	}
	return realFull, nil
}

func cloneBlueprintMap(src map[string][]*WorkBlueprint) map[string][]*WorkBlueprint {
	out := make(map[string][]*WorkBlueprint, len(src))
	for id, versions := range src {
		out[id] = append([]*WorkBlueprint(nil), versions...)
	}
	return out
}

func hasBlueprint(byID map[string][]*WorkBlueprint, key blueprintKey) bool {
	for _, bp := range byID[key.id] {
		if bp.Version == key.version {
			return true
		}
	}
	return false
}

func removeBlueprint(byID map[string][]*WorkBlueprint, key blueprintKey) {
	versions := byID[key.id]
	for i, bp := range versions {
		if bp.Version == key.version {
			versions = append(versions[:i:i], versions[i+1:]...)
			break
		}
	}
	if len(versions) == 0 {
		delete(byID, key.id)
	} else {
		byID[key.id] = versions
	}
}

// blueprintIndex is the on-disk format of blueprints/index.json.
type blueprintIndex struct {
	SchemaVersion int              `json:"schemaVersion"`
	Entries       []blueprintEntry `json:"entries"`
}

type blueprintEntry struct {
	ID            string `json:"id"`
	Version       int    `json:"version"`
	SchemaVersion int    `json:"schemaVersion"`
	Path          string `json:"path"`
}

// ── DefinitionSnapshot creation (from Blueprint) ───────────────────────────

// CreateDefinitionSnapshot produces a deep-copied, normalised, revision=1
// WorkDefinitionSnapshot from a Blueprint. If the blueprint has required
// ToolContracts, a ToolCatalog must be provided via CreateDefinitionSnapshotWithTools
// instead — this two-argument form refuses to proceed when required contracts exist.
//
// The original blueprint is never mutated.
func CreateDefinitionSnapshot(bp *WorkBlueprint, inputs map[string]any) (*WorkDefinitionSnapshot, error) {
	return createDefinitionSnapshot(context.Background(), bp, inputs, nil)
}

// CreateDefinitionSnapshotWithTools is the tool-aware variant of
// CreateDefinitionSnapshot. It resolves each required ToolContract against the
// catalog and returns an explicit per-tool error if unavailable, incompatible,
// or if resolution itself returns an error. Optional contracts that are missing
// are allowed to proceed.
func CreateDefinitionSnapshotWithTools(ctx context.Context, bp *WorkBlueprint, inputs map[string]any, catalog ToolCatalog) (*WorkDefinitionSnapshot, error) {
	if ctx == nil {
		return nil, errors.New("work: create snapshot context is nil")
	}
	return createDefinitionSnapshot(ctx, bp, inputs, catalog)
}

func createDefinitionSnapshot(ctx context.Context, bp *WorkBlueprint, inputs map[string]any, catalog ToolCatalog) (*WorkDefinitionSnapshot, error) {
	if bp == nil {
		return nil, errors.New("work: cannot create snapshot from nil blueprint")
	}
	if err := ValidateBlueprint(bp); err != nil {
		return nil, fmt.Errorf("work: create snapshot: %w", err)
	}

	// Resolve required ToolContracts.
	hasRequired := false
	for _, tc := range bp.ToolContracts {
		if tc.Required {
			hasRequired = true
			break
		}
	}
	if hasRequired && catalog == nil {
		return nil, fmt.Errorf("work: blueprint %s v%d has required ToolContracts but no ToolCatalog provided — use CreateDefinitionSnapshotWithTools", bp.ID, bp.Version)
	}
	if catalog != nil {
		for _, tc := range bp.ToolContracts {
			cap, err := catalog.ResolveTool(ctx, tc)
			if err != nil {
				if tc.Required {
					return nil, fmt.Errorf("work: blueprint %s v%d: required tool %q resolve error: %w", bp.ID, bp.Version, tc.Name, err)
				}
				continue
			}
			if !cap.Available {
				if tc.Required {
					return nil, fmt.Errorf("work: blueprint %s v%d: required tool %q is unavailable: %s", bp.ID, bp.Version, tc.Name, cap.Reason)
				}
				continue
			}
			if !cap.Compatible {
				if tc.Required {
					return nil, fmt.Errorf("work: blueprint %s v%d: required tool %q is incompatible: %s", bp.ID, bp.Version, tc.Name, cap.Reason)
				}
			}
		}
	}

	// Validate inputs.
	if len(bp.InputSchema) > 0 {
		if err := validateInputsAgainstSchema(bp.InputSchema, inputs); err != nil {
			return nil, fmt.Errorf("work: blueprint %s v%d inputs: %w", bp.ID, bp.Version, err)
		}
	}

	snap := &WorkDefinitionSnapshot{
		SchemaVersion: bp.SchemaVersion,
		Revision:      1,
		BlueprintRef: BlueprintRef{
			ID:            bp.ID,
			SchemaVersion: bp.SchemaVersion,
			Version:       bp.Version,
		},
		InputSchema:     copyRaw(bp.InputSchema),
		PromptTemplate:  bp.PromptTemplate,
		Workflow:        deepCopyWorkflow(bp.Workflow),
		BlockSpecs:      deepCopyBlockSpecs(bp.BlockSpecs),
		CornerstoneReqs: deepCopyCornerstoneReqs(bp.CornerstoneReqs),
		ConclusionKinds: copyConclusionKinds(bp.ConclusionKinds),
		ArtifactKinds:   copyStrings(bp.ArtifactKinds),
		ToolContracts:   deepCopyToolContracts(bp.ToolContracts),
	}

	norm, err := NormalizeDefinitionSnapshot(snap)
	if err != nil {
		return nil, fmt.Errorf("work: normalise snapshot: %w", err)
	}
	return norm, nil
}

// ── COW DefinitionSnapshot editing ─────────────────────────────────────────

// DefinitionEdits carries optional fields to change on a DefinitionSnapshot.
type DefinitionEdits struct {
	ExpectedRevision int64             // 0 means no revision guard
	InputSchema      *json.RawMessage  // nil = keep
	PromptTemplate   *string           // nil = keep
	Workflow         *WorkflowDef      // nil = keep
	BlockSpecs       []BlockSpec       // non-nil = replace (empty slice is valid)
	CornerstoneReqs  []CornerstoneReq  // non-nil = replace
	ConclusionKinds  []ConclusionKind  // non-nil = replace
	ArtifactKinds    []string          // non-nil = replace
	ToolContracts    []ToolContractRef // non-nil = replace
}

// EditDefinitionSnapshot produces a new revision+1 snapshot by deep-copying
// prev and applying edits. The previous snapshot is never mutated.
func EditDefinitionSnapshot(prev *WorkDefinitionSnapshot, edits DefinitionEdits) (*WorkDefinitionSnapshot, error) {
	if prev == nil {
		return nil, errors.New("work: cannot edit nil DefinitionSnapshot")
	}
	if err := validatePrevSnapshot(prev); err != nil {
		return nil, fmt.Errorf("work: invalid previous snapshot: %w", err)
	}
	if edits.ExpectedRevision != 0 && edits.ExpectedRevision != prev.Revision {
		return nil, fmt.Errorf("work: edit revision conflict: expected %d, current %d", edits.ExpectedRevision, prev.Revision)
	}
	if isEmptyPatch(edits) {
		return nil, errors.New("work: edit patch is empty — at least one field must change")
	}
	if prev.Revision == math.MaxInt64 {
		return nil, errors.New("work: revision overflow — snapshot has reached max revision")
	}

	next := &WorkDefinitionSnapshot{
		SchemaVersion:   prev.SchemaVersion,
		Revision:        prev.Revision + 1,
		BlueprintRef:    prev.BlueprintRef,
		InputSchema:     copyRaw(prev.InputSchema),
		PromptTemplate:  prev.PromptTemplate,
		Workflow:        deepCopyWorkflow(prev.Workflow),
		BlockSpecs:      deepCopyBlockSpecs(prev.BlockSpecs),
		CornerstoneReqs: deepCopyCornerstoneReqs(prev.CornerstoneReqs),
		ConclusionKinds: copyConclusionKinds(prev.ConclusionKinds),
		ArtifactKinds:   copyStrings(prev.ArtifactKinds),
		ToolContracts:   deepCopyToolContracts(prev.ToolContracts),
	}

	if edits.InputSchema != nil {
		next.InputSchema = copyRaw(*edits.InputSchema)
	}
	if edits.PromptTemplate != nil {
		next.PromptTemplate = *edits.PromptTemplate
	}
	if edits.Workflow != nil {
		next.Workflow = deepCopyWorkflow(*edits.Workflow)
	}
	if edits.BlockSpecs != nil {
		next.BlockSpecs = deepCopyBlockSpecs(edits.BlockSpecs)
	}
	if edits.CornerstoneReqs != nil {
		next.CornerstoneReqs = deepCopyCornerstoneReqs(edits.CornerstoneReqs)
	}
	if edits.ConclusionKinds != nil {
		next.ConclusionKinds = copyConclusionKinds(edits.ConclusionKinds)
	}
	if edits.ArtifactKinds != nil {
		next.ArtifactKinds = copyStrings(edits.ArtifactKinds)
	}
	if edits.ToolContracts != nil {
		next.ToolContracts = deepCopyToolContracts(edits.ToolContracts)
	}

	if err := validateSnapshotFields(next); err != nil {
		return nil, fmt.Errorf("work: edited snapshot validation: %w", err)
	}
	if err := ValidateNoSecretsInSnapshot(next); err != nil {
		return nil, fmt.Errorf("work: edited snapshot secrets: %w", err)
	}

	norm, err := NormalizeDefinitionSnapshot(next)
	if err != nil {
		return nil, fmt.Errorf("work: normalise edited snapshot: %w", err)
	}
	return norm, nil
}

func validatePrevSnapshot(prev *WorkDefinitionSnapshot) error {
	if prev.SchemaVersion <= 0 {
		return fmt.Errorf("schemaVersion must be > 0, got %d", prev.SchemaVersion)
	}
	if prev.SchemaVersion > SchemaVersion {
		return fmt.Errorf("future schemaVersion %d > current %d", prev.SchemaVersion, SchemaVersion)
	}
	if prev.Revision <= 0 {
		return fmt.Errorf("revision must be > 0, got %d", prev.Revision)
	}
	if prev.BlueprintRef.ID == "" {
		return errors.New("blueprintRef.ID is empty")
	}
	if prev.BlueprintRef.SchemaVersion <= 0 {
		return fmt.Errorf("blueprintRef.schemaVersion must be > 0, got %d", prev.BlueprintRef.SchemaVersion)
	}
	if prev.BlueprintRef.Version <= 0 {
		return fmt.Errorf("blueprintRef.version must be > 0, got %d", prev.BlueprintRef.Version)
	}
	if prev.BlueprintRef.SchemaVersion != prev.SchemaVersion {
		return fmt.Errorf("blueprintRef.schemaVersion %d does not match snapshot schemaVersion %d", prev.BlueprintRef.SchemaVersion, prev.SchemaVersion)
	}
	if err := validateBlueprintID(prev.BlueprintRef.ID); err != nil {
		return fmt.Errorf("blueprintRef: %w", err)
	}
	if err := validateSnapshotFields(prev); err != nil {
		return err
	}
	if err := ValidateNoSecretsInSnapshot(prev); err != nil {
		return err
	}
	if prev.Digest == "" {
		return errors.New("digest is empty")
	}
	// Verify stored digest matches recomputed digest.
	recomputed, err := ComputeDigest(prev)
	if err != nil {
		return fmt.Errorf("compute digest: %w", err)
	}
	if recomputed != prev.Digest {
		return fmt.Errorf("digest mismatch: stored %s, computed %s", prev.Digest, recomputed)
	}
	return nil
}

func isEmptyPatch(edits DefinitionEdits) bool {
	return edits.InputSchema == nil &&
		edits.PromptTemplate == nil &&
		edits.Workflow == nil &&
		edits.BlockSpecs == nil &&
		edits.CornerstoneReqs == nil &&
		edits.ConclusionKinds == nil &&
		edits.ArtifactKinds == nil &&
		edits.ToolContracts == nil
}

// ── Validation: Blueprint ──────────────────────────────────────────────────

// ValidateBlueprint performs strict validation of a WorkBlueprint.
func ValidateBlueprint(bp *WorkBlueprint) error {
	if bp == nil {
		return errors.New("work: blueprint is nil")
	}
	if err := CheckSchemaVersion("WorkBlueprint", bp.SchemaVersion); err != nil {
		return err
	}
	if bp.SchemaVersion <= 0 {
		return fmt.Errorf("work: blueprint schemaVersion must be >= 1, got %d", bp.SchemaVersion)
	}
	if bp.ID == "" {
		return errors.New("work: blueprint ID is required")
	}
	if err := validateBlueprintID(bp.ID); err != nil {
		return fmt.Errorf("work: blueprint %s: %w", bp.ID, err)
	}
	if bp.Version < 1 {
		return fmt.Errorf("work: blueprint %s version must be >= 1, got %d", bp.ID, bp.Version)
	}
	if bp.Name == "" {
		return fmt.Errorf("work: blueprint %s v%d name is required", bp.ID, bp.Version)
	}
	if bp.CreatedAt.IsZero() {
		return fmt.Errorf("work: blueprint %s v%d createdAt is required", bp.ID, bp.Version)
	}
	if err := validateBlueprintSource(bp.Source); err != nil {
		return fmt.Errorf("work: blueprint %s v%d: %w", bp.ID, bp.Version, err)
	}
	if len(bp.InputSchema) > 0 {
		if err := validateInputSchema(bp.InputSchema); err != nil {
			return fmt.Errorf("work: blueprint %s v%d inputSchema: %w", bp.ID, bp.Version, err)
		}
	}
	if err := validateWorkflowDef(bp.Workflow); err != nil {
		return fmt.Errorf("work: blueprint %s v%d workflow: %w", bp.ID, bp.Version, err)
	}
	if err := validateBlockSpecs(bp.BlockSpecs); err != nil {
		return fmt.Errorf("work: blueprint %s v%d blockSpecs: %w", bp.ID, bp.Version, err)
	}
	if err := validateToolContracts(bp.ToolContracts); err != nil {
		return fmt.Errorf("work: blueprint %s v%d toolContracts: %w", bp.ID, bp.Version, err)
	}
	if err := validateDefinitionMetadata(bp.CornerstoneReqs, bp.ConclusionKinds, bp.ArtifactKinds); err != nil {
		return fmt.Errorf("work: blueprint %s v%d metadata: %w", bp.ID, bp.Version, err)
	}
	// Cross-validate globally unique IDs across stages, tasks, and blocks.
	if err := validateDefinitionIDs(bp.Workflow, bp.BlockSpecs); err != nil {
		return fmt.Errorf("work: blueprint %s v%d: %w", bp.ID, bp.Version, err)
	}
	if err := ValidateNoSecrets(bp); err != nil {
		return fmt.Errorf("work: blueprint %s v%d: %w", bp.ID, bp.Version, err)
	}
	return nil
}

func validateBlueprintID(id string) error {
	const prefix = "blueprint:"
	if !strings.HasPrefix(id, prefix) {
		return fmt.Errorf("ID must start with %q, got %q", prefix, id)
	}
	name := strings.TrimPrefix(id, prefix)
	if name == "" {
		return fmt.Errorf("ID %q has empty name after prefix", id)
	}
	if len(name) > 96 {
		return fmt.Errorf("ID name too long (%d > 96)", len(name))
	}
	for _, r := range name {
		if !(r == '-' || r == '.' || r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')) {
			return fmt.Errorf("ID name contains invalid character %q", r)
		}
	}
	return nil
}

func validateBlueprintSource(source BlueprintSource) error {
	switch {
	case source == BlueprintSystem:
		return nil
	case source == BlueprintUser:
		return nil
	case strings.HasPrefix(string(source), "addon:"):
		name := strings.TrimPrefix(string(source), "addon:")
		if name == "" {
			return fmt.Errorf("addon source missing name after 'addon:'")
		}
		for _, r := range name {
			if !(r == '-' || r == '.' || r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')) {
				return fmt.Errorf("addon source name contains invalid character %q", r)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown blueprint source %q (must be system, user, or addon:<name>)", source)
	}
}

func validateSnapshotFields(s *WorkDefinitionSnapshot) error {
	if s.SchemaVersion <= 0 {
		return fmt.Errorf("schemaVersion must be >= 1, got %d", s.SchemaVersion)
	}
	if err := CheckSchemaVersion("DefinitionSnapshot", s.SchemaVersion); err != nil {
		return err
	}
	if len(s.InputSchema) > 0 {
		if err := validateInputSchema(s.InputSchema); err != nil {
			return fmt.Errorf("inputSchema: %w", err)
		}
	}
	if err := validateWorkflowDef(s.Workflow); err != nil {
		return fmt.Errorf("workflow: %w", err)
	}
	if err := validateBlockSpecs(s.BlockSpecs); err != nil {
		return fmt.Errorf("blockSpecs: %w", err)
	}
	if err := validateDefinitionIDs(s.Workflow, s.BlockSpecs); err != nil {
		return err
	}
	if err := validateToolContracts(s.ToolContracts); err != nil {
		return fmt.Errorf("toolContracts: %w", err)
	}
	if err := validateDefinitionMetadata(s.CornerstoneReqs, s.ConclusionKinds, s.ArtifactKinds); err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	return nil
}

// ── Validation: InputSchema ────────────────────────────────────────────────

func validateInputSchema(raw json.RawMessage) error {
	if !json.Valid(raw) {
		return errors.New("inputSchema is not valid JSON")
	}
	var schema map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&schema); err != nil {
		return fmt.Errorf("inputSchema parse: %w", err)
	}
	if schema["type"] != "object" {
		return fmt.Errorf("inputSchema type must be 'object' for Blueprint inputs")
	}
	return validateSchemaNode("inputSchema", schema)
}

func validateSchemaNode(path string, schema map[string]any) error {
	typeName, ok := schema["type"].(string)
	if !ok || typeName == "" {
		return fmt.Errorf("%s.type must be a non-empty string", path)
	}
	if err := checkJSONType(path, zeroJSONValue(typeName), typeName); err != nil {
		return fmt.Errorf("%s has unsupported type %q", path, typeName)
	}
	props := map[string]any(nil)
	if rawProps, exists := schema["properties"]; exists {
		if typeName != "object" {
			return fmt.Errorf("%s.properties is only valid for object schemas", path)
		}
		var propsOK bool
		props, propsOK = rawProps.(map[string]any)
		if !propsOK {
			return fmt.Errorf("%s.properties must be an object", path)
		}
		for name, rawProp := range props {
			prop, ok := rawProp.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s must be an object", path, name)
			}
			if err := validateSchemaNode(path+"."+name, prop); err != nil {
				return err
			}
		}
	}
	if rawRequired, exists := schema["required"]; exists {
		if typeName != "object" {
			return fmt.Errorf("%s.required is only valid for object schemas", path)
		}
		required, ok := rawRequired.([]any)
		if !ok {
			return fmt.Errorf("%s.required must be an array", path)
		}
		seen := make(map[string]bool, len(required))
		for i, rawName := range required {
			name, ok := rawName.(string)
			if !ok || name == "" {
				return fmt.Errorf("%s.required[%d] must be a non-empty string", path, i)
			}
			if seen[name] {
				return fmt.Errorf("%s.required contains duplicate field %q", path, name)
			}
			if _, ok := props[name]; !ok {
				return fmt.Errorf("%s.required names unknown property %q", path, name)
			}
			seen[name] = true
		}
	}
	if rawAdditional, exists := schema["additionalProperties"]; exists {
		if _, ok := rawAdditional.(bool); !ok {
			return fmt.Errorf("%s.additionalProperties must be a boolean", path)
		}
	}
	if typeName == "array" {
		rawItems, ok := schema["items"]
		if !ok {
			return fmt.Errorf("%s.items is required for array schemas", path)
		}
		items, ok := rawItems.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items must be an object", path)
		}
		if err := validateSchemaNode(path+"[]", items); err != nil {
			return err
		}
	}
	if rawEnum, exists := schema["enum"]; exists {
		enumVals, ok := rawEnum.([]any)
		if !ok || len(enumVals) == 0 {
			return fmt.Errorf("%s.enum must be a non-empty array", path)
		}
		for i, value := range enumVals {
			if err := checkJSONType(fmt.Sprintf("%s.enum[%d]", path, i), value, typeName); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateInputsAgainstSchema validates inputs (or nil) against a JSON Schema.
// When inputs is nil, required fields are checked for existence.
func validateInputsAgainstSchema(rawSchema json.RawMessage, inputs map[string]any) error {
	var schema map[string]any
	dec := json.NewDecoder(strings.NewReader(string(rawSchema)))
	dec.UseNumber()
	if err := dec.Decode(&schema); err != nil {
		return fmt.Errorf("parse schema: %w", err)
	}
	rawInputs, err := json.Marshal(inputs)
	if err != nil {
		return fmt.Errorf("inputs are not JSON serializable: %w", err)
	}
	var normalized any
	inputDecoder := json.NewDecoder(strings.NewReader(string(rawInputs)))
	inputDecoder.UseNumber()
	if err := inputDecoder.Decode(&normalized); err != nil {
		return fmt.Errorf("decode inputs: %w", err)
	}
	if normalized == nil {
		normalized = map[string]any(nil)
	}
	return validateSchemaValue("inputs", normalized, schema)
}

func validateSchemaValue(path string, value any, schema map[string]any) error {
	typeName, _ := schema["type"].(string)
	if err := checkJSONType(path, value, typeName); err != nil {
		return err
	}
	if enumVals, ok := schema["enum"].([]any); ok && !valueInEnum(value, enumVals) {
		return fmt.Errorf("field %q is not an allowed enum value", path)
	}
	switch typeName {
	case "object":
		object := value.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, rawName := range required {
				name := rawName.(string)
				if _, exists := object[name]; !exists {
					return fmt.Errorf("field %q is required", path+"."+name)
				}
			}
		}
		if allow, ok := schema["additionalProperties"].(bool); ok && !allow {
			for name := range object {
				if _, known := props[name]; !known {
					return fmt.Errorf("field %q is unknown (additionalProperties is false)", path+"."+name)
				}
			}
		}
		for name, rawProp := range props {
			child, exists := object[name]
			if !exists {
				continue
			}
			if err := validateSchemaValue(path+"."+name, child, rawProp.(map[string]any)); err != nil {
				return err
			}
		}
	case "array":
		items := schema["items"].(map[string]any)
		for i, item := range value.([]any) {
			if err := validateSchemaValue(fmt.Sprintf("%s[%d]", path, i), item, items); err != nil {
				return err
			}
		}
	}
	return nil
}

func valueInEnum(value any, values []any) bool {
	for _, candidate := range values {
		if valuesEqual(value, candidate) {
			return true
		}
	}
	return false
}

func valuesEqual(a, b any) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok && bok {
		ar, aok := new(big.Rat).SetString(string(an))
		br, bok := new(big.Rat).SetString(string(bn))
		return aok && bok && ar.Cmp(br) == 0
	}
	aj, aerr := canonicalJSON(a)
	bj, berr := canonicalJSON(b)
	return aerr == nil && berr == nil && string(aj) == string(bj)
}

func zeroJSONValue(typeName string) any {
	switch typeName {
	case "string":
		return ""
	case "integer", "number":
		return json.Number("0")
	case "boolean":
		return false
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	default:
		return nil
	}
}

func checkJSONType(field string, val any, expectedType string) error {
	switch expectedType {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("field %q must be a string (got %T)", field, val)
		}
	case "integer":
		switch v := val.(type) {
		case float64:
			if v != math.Floor(v) {
				return fmt.Errorf("field %q must be an integer (got fractional %v)", field, v)
			}
		case json.Number:
			if _, err := v.Int64(); err != nil {
				return fmt.Errorf("field %q must be an integer (got %v)", field, v)
			}
		case int, int64, int32:
		default:
			return fmt.Errorf("field %q must be an integer (got %T)", field, val)
		}
	case "number":
		switch val.(type) {
		case float64, json.Number, int, int64, int32:
		default:
			return fmt.Errorf("field %q must be a number (got %T)", field, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("field %q must be a boolean (got %T)", field, val)
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return fmt.Errorf("field %q must be an array (got %T)", field, val)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("field %q must be an object (got %T)", field, val)
		}
	default:
		return fmt.Errorf("field %q uses unsupported JSON schema type %q", field, expectedType)
	}
	return nil
}

// ── Validation: Workflow ───────────────────────────────────────────────────

func validateWorkflowDef(wf WorkflowDef) error {
	if len(wf.Stages) == 0 {
		return errors.New("workflow must have at least one stage")
	}
	stageIDs := make(map[string]bool)
	for i, stage := range wf.Stages {
		if stage.ID == "" {
			return fmt.Errorf("stage[%d]: id is required", i)
		}
		if stageIDs[stage.ID] {
			return fmt.Errorf("duplicate stage id %q", stage.ID)
		}
		stageIDs[stage.ID] = true
		if stage.Title == "" {
			return fmt.Errorf("stage[%d] (%s): title is required", i, stage.ID)
		}
		if len(stage.Tasks) == 0 {
			return fmt.Errorf("stage[%d] (%s): must have at least one task", i, stage.ID)
		}
		taskIDs := make(map[string]bool)
		for j, task := range stage.Tasks {
			if task.ID == "" {
				return fmt.Errorf("stage[%d] (%s) task[%d]: id is required", i, stage.ID, j)
			}
			if taskIDs[task.ID] {
				return fmt.Errorf("stage[%d] (%s): duplicate task id %q", i, stage.ID, task.ID)
			}
			taskIDs[task.ID] = true
			if task.Title == "" {
				return fmt.Errorf("stage[%d] (%s) task[%d] (%s): title is required", i, stage.ID, j, task.ID)
			}
		}
		switch stage.Gate {
		case "", "input", "approval":
		default:
			return fmt.Errorf("stage[%d] (%s): invalid gate %q", i, stage.ID, stage.Gate)
		}
	}
	return nil
}

// ── Validation: BlockSpec ──────────────────────────────────────────────────

// coreBlockKinds is the V1 whitelist from §8.3.
var coreBlockKinds = map[string]bool{
	"item": true, "list": true, "checklist": true, "file_list": true,
	"git_status": true, "action_entry": true, "key_value": true, "status": true,
	"progress": true, "timeline": true, "chart": true, "table": true,
	"graph": true, "code": true, "markdown": true, "artifact": true,
	"decision": true, "approval": true, "input": true, "notice": true,
}

func validateBlockSpecs(specs []BlockSpec) error {
	ids := make(map[string]bool)
	for i, bs := range specs {
		if bs.ID == "" {
			return fmt.Errorf("blockSpec[%d]: id is required", i)
		}
		if ids[bs.ID] {
			return fmt.Errorf("duplicate blockSpec id %q", bs.ID)
		}
		ids[bs.ID] = true
		if bs.Kind == "" {
			return fmt.Errorf("blockSpec[%d] (%s): kind is required", i, bs.ID)
		}
		if !coreBlockKinds[bs.Kind] {
			return fmt.Errorf("blockSpec[%d] (%s): unknown block kind %q", i, bs.ID, bs.Kind)
		}
		if bs.SchemaVersion < 1 {
			return fmt.Errorf("blockSpec[%d] (%s): schemaVersion must be >= 1, got %d", i, bs.ID, bs.SchemaVersion)
		}
		if err := CheckSchemaVersion("BlockSpec "+bs.Kind, bs.SchemaVersion); err != nil {
			return fmt.Errorf("blockSpec[%d] (%s): %w", i, bs.ID, err)
		}
		if bs.Label == "" {
			return fmt.Errorf("blockSpec[%d] (%s): label is required", i, bs.ID)
		}
		if bs.Placement.Slot == "" {
			return fmt.Errorf("blockSpec[%d] (%s): placement.slot is required", i, bs.ID)
		}
		validSlots := map[string]bool{"primary": true, "secondary": true, "attention": true, "result": true}
		if !validSlots[bs.Placement.Slot] {
			return fmt.Errorf("blockSpec[%d] (%s): invalid placement.slot %q", i, bs.ID, bs.Placement.Slot)
		}
		if bs.Placement.Order < 0 {
			return fmt.Errorf("blockSpec[%d] (%s): placement.order must be >= 0, got %d", i, bs.ID, bs.Placement.Order)
		}
		if bs.Placement.Span < 0 {
			return fmt.Errorf("blockSpec[%d] (%s): placement.span must be >= 0, got %d", i, bs.ID, bs.Placement.Span)
		}
		if bs.Placement.BlockID != "" && bs.Placement.BlockID != bs.ID {
			return fmt.Errorf("blockSpec[%d] (%s): placement.blockId %q must be empty or match the block ID", i, bs.ID, bs.Placement.BlockID)
		}
		if len(bs.DefaultData) > 0 {
			if !json.Valid(bs.DefaultData) {
				return fmt.Errorf("blockSpec[%d] (%s): defaultData is not valid JSON", i, bs.ID)
			}
			var value any
			if err := json.Unmarshal(bs.DefaultData, &value); err != nil {
				return fmt.Errorf("blockSpec[%d] (%s): parse defaultData: %w", i, bs.ID, err)
			}
			if _, ok := value.(map[string]any); !ok {
				return fmt.Errorf("blockSpec[%d] (%s): defaultData must be a JSON object", i, bs.ID)
			}
		}
	}
	return nil
}

// validateDefinitionIDs checks that stage IDs, task IDs, and blockSpec IDs do not
// collide across the blueprint.
func validateDefinitionIDs(workflow WorkflowDef, blocks []BlockSpec) error {
	global := make(map[string]string) // id → kind
	for _, stage := range workflow.Stages {
		if prev, ok := global[stage.ID]; ok {
			return fmt.Errorf("duplicate global id %q (stage, previously used by %s)", stage.ID, prev)
		}
		global[stage.ID] = "stage"
		for _, task := range stage.Tasks {
			if prev, ok := global[task.ID]; ok {
				return fmt.Errorf("duplicate global id %q (task, previously used by %s)", task.ID, prev)
			}
			global[task.ID] = "task"
		}
	}
	for _, bs := range blocks {
		if prev, ok := global[bs.ID]; ok {
			return fmt.Errorf("duplicate global id %q (blockSpec, previously used by %s)", bs.ID, prev)
		}
		global[bs.ID] = "blockSpec"
	}
	return nil
}

// ── Validation: ToolContract ───────────────────────────────────────────────

func validateToolContracts(contracts []ToolContractRef) error {
	validClasses := map[string]bool{
		"read": true, "workspace_write": true, "external_write": true, "destructive": true,
	}
	seen := make(map[string]bool)
	for i, tc := range contracts {
		if tc.Name == "" {
			return fmt.Errorf("toolContract[%d]: name is required", i)
		}
		if tc.ContractVersion < 1 {
			return fmt.Errorf("toolContract[%d] (%s): contractVersion must be >= 1, got %d", i, tc.Name, tc.ContractVersion)
		}
		if tc.SideEffectClass == "" {
			return fmt.Errorf("toolContract[%d] (%s): sideEffectClass is required", i, tc.Name)
		}
		if !validClasses[tc.SideEffectClass] {
			return fmt.Errorf("toolContract[%d] (%s): invalid sideEffectClass %q", i, tc.Name, tc.SideEffectClass)
		}
		key := tc.Name + "@" + tc.Provider
		if seen[key] {
			return fmt.Errorf("toolContract[%d] (%s): duplicate contract key %q", i, tc.Name, key)
		}
		seen[key] = true
	}
	return nil
}

func validateDefinitionMetadata(reqs []CornerstoneReq, conclusions []ConclusionKind, artifacts []string) error {
	validCornerstones := map[CornerstoneType]bool{
		CornerstoneInstruction: true, CornerstoneFileRef: true, CornerstoneFileSnapshot: true,
		CornerstoneDecision: true, CornerstoneConclusion: true, CornerstoneSource: true,
		CornerstonePolicy: true, CornerstoneParameter: true,
	}
	for i, req := range reqs {
		if !validCornerstones[req.Type] {
			return fmt.Errorf("cornerstoneRequirement[%d] has invalid type %q", i, req.Type)
		}
		if req.Required && strings.TrimSpace(req.Label) == "" {
			return fmt.Errorf("cornerstoneRequirement[%d] label is required", i)
		}
	}
	validConclusions := map[ConclusionKind]bool{
		ConclusionFact: true, ConclusionFinding: true, ConclusionDecision: true,
		ConclusionOutcome: true, ConclusionLesson: true,
	}
	seenConclusions := make(map[ConclusionKind]bool, len(conclusions))
	for i, kind := range conclusions {
		if !validConclusions[kind] {
			return fmt.Errorf("conclusionKinds[%d] is invalid: %q", i, kind)
		}
		if seenConclusions[kind] {
			return fmt.Errorf("conclusionKinds contains duplicate %q", kind)
		}
		seenConclusions[kind] = true
	}
	seenArtifacts := make(map[string]bool, len(artifacts))
	for i, kind := range artifacts {
		if strings.TrimSpace(kind) == "" {
			return fmt.Errorf("artifactKinds[%d] is empty", i)
		}
		if seenArtifacts[kind] {
			return fmt.Errorf("artifactKinds contains duplicate %q", kind)
		}
		seenArtifacts[kind] = true
	}
	return nil
}

// ── Secret detection ───────────────────────────────────────────────────────

var secretFieldPatterns = []string{
	"api_key", "apikey", "api-key",
	"secret", "token",
	"password", "passwd",
	"credential",
	"private_key", "privatekey", "private-key",
	"access_key", "accesskey", "access-key",
}

// secretRefPatterns are patterns that indicate a value is a reference, not a plaintext secret.
var secretRefPatterns = []string{
	"${",                        // env var / template ref
	"{{",                        // Go template
	"vault:",                    // vault ref
	"secretref:", "secret-ref:", // explicit ref
	"$secret.", // env-like ref
	"ref:",     // generic ref
}

// ValidateNoSecrets scans a WorkBlueprint for secret plaintext in all
// persisted definition fields.
func ValidateNoSecrets(bp *WorkBlueprint) error {
	if bp == nil {
		return errors.New("work: cannot scan nil blueprint for secrets")
	}
	var issues []string

	// JSON fields.
	if len(bp.InputSchema) > 0 {
		collectSecretIssues(bp.InputSchema, "inputSchema", &issues)
	}
	for i, bs := range bp.BlockSpecs {
		if len(bs.DefaultData) > 0 {
			collectSecretIssues(bs.DefaultData, fmt.Sprintf("blockSpec[%d].defaultData", i), &issues)
		}
	}

	// All text fields.
	checkTextField(bp.PromptTemplate, "promptTemplate", &issues)
	checkTextField(bp.ID, "id", &issues)
	checkTextField(bp.Name, "name", &issues)
	checkTextField(bp.Description, "description", &issues)
	checkTextField(string(bp.Source), "source", &issues)
	for i, bs := range bp.BlockSpecs {
		checkTextField(bs.Label, fmt.Sprintf("blockSpec[%d].label", i), &issues)
		checkTextField(bs.Description, fmt.Sprintf("blockSpec[%d].description", i), &issues)
	}
	for i, stage := range bp.Workflow.Stages {
		checkTextField(stage.Title, fmt.Sprintf("workflow.stage[%d].title", i), &issues)
		for j, task := range stage.Tasks {
			checkTextField(task.Title, fmt.Sprintf("workflow.stage[%d].task[%d].title", i, j), &issues)
		}
	}
	for i, req := range bp.CornerstoneReqs {
		checkTextField(req.Label, fmt.Sprintf("cornerstoneReq[%d].label", i), &issues)
	}
	for i, tc := range bp.ToolContracts {
		checkTextField(tc.Name, fmt.Sprintf("toolContract[%d].name", i), &issues)
		checkTextField(tc.Provider, fmt.Sprintf("toolContract[%d].provider", i), &issues)
	}
	for i, kind := range bp.ArtifactKinds {
		checkTextField(kind, fmt.Sprintf("artifactKinds[%d]", i), &issues)
	}

	if len(issues) > 0 {
		return fmt.Errorf("secrets detected in blueprint definition: %s", strings.Join(issues, "; "))
	}
	return nil
}

// ValidateNoSecretsInSnapshot scans a DefinitionSnapshot for secrets.
func ValidateNoSecretsInSnapshot(s *WorkDefinitionSnapshot) error {
	if s == nil {
		return errors.New("work: cannot scan nil DefinitionSnapshot for secrets")
	}
	var issues []string
	if len(s.InputSchema) > 0 {
		collectSecretIssues(s.InputSchema, "inputSchema", &issues)
	}
	for i, bs := range s.BlockSpecs {
		if len(bs.DefaultData) > 0 {
			collectSecretIssues(bs.DefaultData, fmt.Sprintf("blockSpec[%d].defaultData", i), &issues)
		}
	}
	checkTextField(s.PromptTemplate, "promptTemplate", &issues)
	for i, bs := range s.BlockSpecs {
		checkTextField(bs.Label, fmt.Sprintf("blockSpec[%d].label", i), &issues)
		checkTextField(bs.Description, fmt.Sprintf("blockSpec[%d].description", i), &issues)
	}
	for i, stage := range s.Workflow.Stages {
		checkTextField(stage.Title, fmt.Sprintf("workflow.stage[%d].title", i), &issues)
		for j, task := range stage.Tasks {
			checkTextField(task.Title, fmt.Sprintf("workflow.stage[%d].task[%d].title", i, j), &issues)
		}
	}
	for i, req := range s.CornerstoneReqs {
		checkTextField(req.Label, fmt.Sprintf("cornerstoneReq[%d].label", i), &issues)
	}
	for i, tc := range s.ToolContracts {
		checkTextField(tc.Name, fmt.Sprintf("toolContract[%d].name", i), &issues)
		checkTextField(tc.Provider, fmt.Sprintf("toolContract[%d].provider", i), &issues)
	}
	for i, kind := range s.ArtifactKinds {
		checkTextField(kind, fmt.Sprintf("artifactKinds[%d]", i), &issues)
	}
	if len(issues) > 0 {
		return fmt.Errorf("secrets detected in definition snapshot: %s", strings.Join(issues, "; "))
	}
	return nil
}

func collectSecretIssues(raw json.RawMessage, path string, issues *[]string) {
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return
	}
	walkSecretFields(generic, path, issues)
}

func walkSecretFields(v any, path string, issues *[]string) {
	switch val := v.(type) {
	case map[string]any:
		for k, vv := range val {
			subPath := path + "." + k
			if isSecretFieldName(k) {
				findStringValues(vv, subPath, issues)
			}
			walkSecretFields(vv, subPath, issues)
		}
	case []any:
		for i, item := range val {
			walkSecretFields(item, fmt.Sprintf("%s[%d]", path, i), issues)
		}
	case string:
		checkTextField(val, path, issues)
	}
}

func findStringValues(v any, path string, issues *[]string) {
	switch val := v.(type) {
	case string:
		if val != "" && !isSecretRef(val) {
			*issues = append(*issues, fmt.Sprintf("%s contains a non-empty secret value", path))
		}
	case map[string]any:
		for k, vv := range val {
			if isSecretValueKey(k) {
				findStringValues(vv, path+"."+k, issues)
			}
		}
	case []any:
		for i, item := range val {
			findStringValues(item, fmt.Sprintf("%s[%d]", path, i), issues)
		}
	}
}

func isSecretValueKey(k string) bool {
	switch strings.ToLower(k) {
	case "default", "const", "example", "examples", "enum", "value":
		return true
	}
	return false
}

func isSecretFieldName(name string) bool {
	lower := strings.ToLower(name)
	// Must contain at least one secret pattern AND not be a common non-secret word.
	matched := false
	for _, pat := range secretFieldPatterns {
		if strings.Contains(lower, pat) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	return true
}

// isSecretRef returns true if s looks like a reference/template rather than a
// plaintext secret.
func isSecretRef(s string) bool {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		return true
	}
	if strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") {
		return true
	}
	for _, pat := range secretRefPatterns[2:] {
		if strings.HasPrefix(lower, strings.ToLower(pat)) && len(s) > len(pat) {
			return true
		}
	}
	// Also allow obvious placeholder values.
	if s == "" || s == "\"\"" || s == "''" || s == "{}" || s == "[]" {
		return true
	}
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		return true
	}
	return false
}

// checkTextField scans a plain string for credential-like value assignments.
func checkTextField(text, path string, issues *[]string) {
	if text == "" {
		return
	}
	if looksLikeSecretLiteral(text) {
		*issues = append(*issues, fmt.Sprintf("%s contains secret-like plaintext", path))
	}
	lower := strings.ToLower(text)
	for _, pat := range secretFieldPatterns {
		start := 0
		for start < len(lower) {
			rel := strings.Index(lower[start:], pat)
			if rel < 0 {
				break
			}
			idx := start + rel
			after := strings.TrimLeft(text[idx+len(pat):], " \t")
			if len(after) > 0 && (after[0] == '=' || after[0] == ':') {
				value := strings.TrimSpace(after[1:])
				if value != "" && !isSecretRef(value) {
					*issues = append(*issues, fmt.Sprintf("%s contains inline secret-like text for %q", path, pat))
					break
				}
			}
			start = idx + len(pat)
		}
	}
}

func looksLikeSecretLiteral(text string) bool {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"sk-", "ghp_", "github_pat_", "xoxb-"} {
		if strings.HasPrefix(lower, prefix) && len(trimmed) >= len(prefix)+12 {
			return true
		}
	}
	if strings.HasPrefix(trimmed, "AKIA") && len(trimmed) == 20 {
		return true
	}
	if strings.Contains(trimmed, "-----BEGIN ") && strings.Contains(trimmed, "PRIVATE KEY-----") {
		return true
	}
	if strings.HasPrefix(trimmed, "eyJ") && len(trimmed) > 40 && strings.Count(trimmed, ".") == 2 {
		return true
	}
	return false
}

// ── Deep copy helpers ──────────────────────────────────────────────────────

func deepCopyBlueprint(bp *WorkBlueprint) *WorkBlueprint {
	cp := *bp
	cp.InputSchema = copyRaw(bp.InputSchema)
	cp.Workflow = deepCopyWorkflow(bp.Workflow)
	cp.BlockSpecs = deepCopyBlockSpecs(bp.BlockSpecs)
	cp.CornerstoneReqs = deepCopyCornerstoneReqs(bp.CornerstoneReqs)
	cp.ConclusionKinds = copyConclusionKinds(bp.ConclusionKinds)
	cp.ArtifactKinds = copyStrings(bp.ArtifactKinds)
	cp.ToolContracts = deepCopyToolContracts(bp.ToolContracts)
	return &cp
}

func deepCopyWorkflow(wf WorkflowDef) WorkflowDef {
	stages := make([]StageSpec, len(wf.Stages))
	for i, s := range wf.Stages {
		tasks := make([]TaskSpec, len(s.Tasks))
		copy(tasks, s.Tasks)
		stages[i] = s
		stages[i].Tasks = tasks
	}
	return WorkflowDef{Stages: stages}
}

func deepCopyBlockSpecs(specs []BlockSpec) []BlockSpec {
	if specs == nil {
		return nil
	}
	out := make([]BlockSpec, len(specs))
	for i, bs := range specs {
		out[i] = bs
		out[i].DefaultData = copyRaw(bs.DefaultData)
	}
	return out
}

func deepCopyCornerstoneReqs(reqs []CornerstoneReq) []CornerstoneReq {
	if reqs == nil {
		return nil
	}
	out := make([]CornerstoneReq, len(reqs))
	copy(out, reqs)
	return out
}

func deepCopyToolContracts(contracts []ToolContractRef) []ToolContractRef {
	if contracts == nil {
		return nil
	}
	out := make([]ToolContractRef, len(contracts))
	copy(out, contracts)
	return out
}

func copyConclusionKinds(kinds []ConclusionKind) []ConclusionKind {
	if kinds == nil {
		return nil
	}
	out := make([]ConclusionKind, len(kinds))
	copy(out, kinds)
	return out
}

func copyStrings(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	copy(out, ss)
	return out
}

func copyRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	out := make(json.RawMessage, len(raw))
	copy(out, raw)
	return out
}

// ── Built-in Blueprints ────────────────────────────────────────────────────

func builtinBlueprints() []*WorkBlueprint {
	return []*WorkBlueprint{
		builtinBlank(),
		builtinInfoOrganize(),
		builtinCodeReview(),
		builtinReport(),
	}
}

func builtinBlank() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion:  SchemaVersion,
		ID:             "blueprint:blank",
		Version:        1,
		Name:           "空白工作",
		Description:    "从零开始，自由定义工作内容和结构。适合临时任务、快速记录和探索性工作。",
		Source:         BlueprintSystem,
		InputSchema:    nil,
		PromptTemplate: "",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "bp-blank-main", Title: "执行", Tasks: []TaskSpec{{ID: "bp-blank-run", Title: "执行任务"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{
				ID:            "bp-blank-notes",
				Kind:          "markdown",
				SchemaVersion: 1,
				Label:         "备注",
				Description:   "自由记录的内容区域",
				Placement:     BlockPlacement{Slot: "primary", Order: 0},
				Editable:      true,
			},
		},
		ToolContracts: []ToolContractRef{},
		CreatedAt:     time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
	}
}

func builtinInfoOrganize() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion: SchemaVersion,
		ID:            "blueprint:info-organize",
		Version:       1,
		Name:          "资料整理",
		Description:   "整理、归纳和分析信息，生成结构化的摘要、分类或知识条目。",
		Source:        BlueprintSystem,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"topic": {"type": "string", "description": "整理主题"},
				"sources": {"type": "array", "items": {"type": "string"}, "description": "信息来源列表"},
				"outputFormat": {"type": "string", "enum": ["summary","outline","table","qa"], "description": "输出格式"}
			},
			"required": ["topic"]
		}`),
		PromptTemplate: "请整理以下信息主题：{{.topic}}\n\n{{if .sources}}参考来源：\n{{range .sources}}- {{.}}\n{{end}}{{end}}\n\n请以{{if .outputFormat}}{{.outputFormat}}{{else}}summary{{end}}格式输出结构化结果。",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "bp-io-collect", Title: "收集信息", Tasks: []TaskSpec{{ID: "bp-io-gather", Title: "获取和确认输入材料"}}},
				{ID: "bp-io-process", Title: "整理分析", Tasks: []TaskSpec{{ID: "bp-io-classify", Title: "分类和归纳"}}},
				{ID: "bp-io-output", Title: "输出结果", Tasks: []TaskSpec{{ID: "bp-io-generate", Title: "生成最终输出"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "bp-io-checklist", Kind: "checklist", SchemaVersion: 1, Label: "整理清单", Description: "待整理的项目", Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true},
			{ID: "bp-io-result", Kind: "markdown", SchemaVersion: 1, Label: "整理结果", Description: "结构化输出", Placement: BlockPlacement{Slot: "result", Order: 1}, Editable: true},
		},
		CornerstoneReqs: []CornerstoneReq{},
		ConclusionKinds: []ConclusionKind{ConclusionFinding},
		ArtifactKinds:   []string{"markdown"},
		ToolContracts:   []ToolContractRef{},
		CreatedAt:       time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
	}
}

func builtinCodeReview() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion: SchemaVersion,
		ID:            "blueprint:code-review",
		Version:       1,
		Name:          "代码审查",
		Description:   "对代码变更、PR 或模块进行系统性审查，检查安全、性能、可维护性和最佳实践。",
		Source:        BlueprintSystem,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"target": {"type": "string", "description": "审查目标"},
				"focus": {"type": "array", "items": {"type": "string"}, "description": "重点关注领域"},
				"language": {"type": "string", "description": "主要编程语言"},
				"context": {"type": "string", "description": "额外上下文说明"}
			},
			"required": ["target"]
		}`),
		PromptTemplate: "请审查以下代码：{{.target}}\n\n{{if .focus}}重点关注：{{range .focus}}- {{.}}\n{{end}}{{end}}{{if .language}}语言：{{.language}}\n{{end}}{{if .context}}上下文：{{.context}}\n{{end}}\n\n请列出发现的问题、严重程度和建议修复方案。",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "bp-cr-scan", Title: "扫描代码", Tasks: []TaskSpec{{ID: "bp-cr-read", Title: "读取目标代码"}}},
				{ID: "bp-cr-analyze", Title: "分析检查", Tasks: []TaskSpec{
					{ID: "bp-cr-security", Title: "安全检查"},
					{ID: "bp-cr-perf", Title: "性能检查"},
					{ID: "bp-cr-style", Title: "风格检查"},
				}},
				{ID: "bp-cr-report", Title: "生成报告", Tasks: []TaskSpec{{ID: "bp-cr-summarize", Title: "汇总发现和修复建议"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "bp-cr-findings", Kind: "checklist", SchemaVersion: 1, Label: "审查发现", Description: "问题清单及严重程度", Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true},
			{ID: "bp-cr-summary", Kind: "markdown", SchemaVersion: 1, Label: "审查摘要", Description: "总体评价和建议", Placement: BlockPlacement{Slot: "result", Order: 1}, Editable: true},
			{ID: "bp-cr-code", Kind: "code", SchemaVersion: 1, Label: "代码片段", Description: "关键代码引用", Placement: BlockPlacement{Slot: "secondary", Order: 2}, Editable: false},
		},
		CornerstoneReqs: []CornerstoneReq{
			{Type: CornerstoneFileRef, Required: true, Label: "待审查的代码文件"},
		},
		ConclusionKinds: []ConclusionKind{ConclusionFinding, ConclusionDecision},
		ArtifactKinds:   []string{"markdown"},
		ToolContracts: []ToolContractRef{
			{Name: "read_file", ContractVersion: 1, SideEffectClass: "read", Required: true},
			{Name: "grep", ContractVersion: 1, SideEffectClass: "read", Required: false},
		},
		CreatedAt: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
	}
}

func builtinReport() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion: SchemaVersion,
		ID:            "blueprint:report",
		Version:       1,
		Name:          "报告生成",
		Description:   "基于输入数据和上下文生成结构化报告，支持多种输出格式。",
		Source:        BlueprintSystem,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {"type": "string", "description": "报告标题"},
				"audience": {"type": "string", "description": "目标读者"},
				"sections": {"type": "array", "items": {"type": "string"}, "description": "报告章节"},
				"data": {"type": "object", "description": "关联数据"}
			},
			"required": ["title"]
		}`),
		PromptTemplate: "请生成报告：{{.title}}\n\n{{if .audience}}目标读者：{{.audience}}\n{{end}}{{if .sections}}章节：{{range .sections}}- {{.}}\n{{end}}{{end}}\n\n请生成结构完整、内容详实的报告。",
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "bp-rpt-outline", Title: "构建大纲", Tasks: []TaskSpec{{ID: "bp-rpt-structure", Title: "确定报告结构"}}},
				{ID: "bp-rpt-draft", Title: "撰写内容", Tasks: []TaskSpec{
					{ID: "bp-rpt-write", Title: "撰写各部分内容"},
					{ID: "bp-rpt-verify", Title: "核对数据和引用"},
				}},
				{ID: "bp-rpt-polish", Title: "润色输出", Tasks: []TaskSpec{{ID: "bp-rpt-format", Title: "格式化和最终检查"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "bp-rpt-outline-blk", Kind: "checklist", SchemaVersion: 1, Label: "报告大纲", Description: "章节结构", Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true},
			{ID: "bp-rpt-content", Kind: "markdown", SchemaVersion: 1, Label: "报告正文", Description: "报告内容", Placement: BlockPlacement{Slot: "result", Order: 1}, Editable: true},
			{ID: "bp-rpt-data", Kind: "table", SchemaVersion: 1, Label: "数据表格", Description: "支撑数据的表格展示", Placement: BlockPlacement{Slot: "secondary", Order: 2}, Editable: true},
		},
		CornerstoneReqs: []CornerstoneReq{},
		ConclusionKinds: []ConclusionKind{ConclusionOutcome, ConclusionFinding},
		ArtifactKinds:   []string{"markdown"},
		ToolContracts:   []ToolContractRef{},
		CreatedAt:       time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
	}
}
