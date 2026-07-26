package work

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ── DefinitionRevisionStore ────────────────────────────────────────────────

// DefinitionRevisionStore persists and loads V2 WorkDefinitionRevision objects.
// The event log carries only the digest; full revisions are stored separately
// so copy-on-write history is independently preserved.
type DefinitionRevisionStore interface {
	// StoreRevision persists a revision. The caller must hold the Work write lock.
	StoreRevision(workID string, rev *WorkDefinitionRevision) error
	// LoadRevision loads a specific revision by number.
	LoadRevision(workID string, revision int64) (*WorkDefinitionRevision, error)
	// LoadLatestRevision loads the highest-numbered revision for a Work.
	// Returns nil if no revision exists yet.
	LoadLatestRevision(workID string) (*WorkDefinitionRevision, error)
}

// ── Digest ─────────────────────────────────────────────────────────────────

// ComputeV2RevisionDigest returns a deterministic sha256: digest for a
// WorkDefinitionRevision. The Digest field itself is cleared before hashing.
func ComputeV2RevisionDigest(rev *WorkDefinitionRevision) (string, error) {
	if rev == nil {
		return "", fmt.Errorf("work: cannot compute digest of nil WorkDefinitionRevision")
	}
	raw, err := json.Marshal(rev)
	if err != nil {
		return "", fmt.Errorf("work: compute revision digest marshal: %w", err)
	}
	var copy WorkDefinitionRevision
	if err := json.Unmarshal(raw, &copy); err != nil {
		return "", fmt.Errorf("work: compute revision digest unmarshal: %w", err)
	}
	copy.Digest = ""
	canon, digest, err := canonicalDigest(&copy)
	if err != nil {
		return "", err
	}
	_ = canon
	return digestPrefix + digest, nil
}

// ── Validation ─────────────────────────────────────────────────────────────

// ValidateDefinitionRevision checks that a WorkDefinitionRevision meets the
// minimum structural requirements for activation. It does not check
// application-level semantics (e.g. whether every InputSpec is referenced by a
// NodeDef) — those are checked during ApplyDefinition.
func ValidateDefinitionRevision(rev *WorkDefinitionRevision) error {
	if rev == nil {
		return fmt.Errorf("work: definition revision is nil")
	}
	if rev.WorkID == "" {
		return fmt.Errorf("work: definition revision: workId required")
	}
	if rev.Goal == "" {
		return fmt.Errorf("work: definition revision: goal required")
	}
	if rev.Revision <= 0 {
		return fmt.Errorf("work: definition revision: revision must be positive, got %d", rev.Revision)
	}
	if rev.ParentRevision < 0 {
		return fmt.Errorf("work: definition revision: parentRevision cannot be negative, got %d", rev.ParentRevision)
	}
	if rev.Revision <= rev.ParentRevision {
		return fmt.Errorf("work: definition revision: revision %d must be greater than parentRevision %d", rev.Revision, rev.ParentRevision)
	}
	if len(rev.Nodes) == 0 {
		return fmt.Errorf("work: definition revision: at least one node required")
	}
	nodeIDs := make(map[string]bool, len(rev.Nodes))
	for i, n := range rev.Nodes {
		if n.ID == "" {
			return fmt.Errorf("work: definition revision: nodes[%d].id required", i)
		}
		if n.Title == "" {
			return fmt.Errorf("work: definition revision: nodes[%d].title required", i)
		}
		if nodeIDs[n.ID] {
			return fmt.Errorf("work: definition revision: duplicate node id %q", n.ID)
		}
		nodeIDs[n.ID] = true
	}
	// Validate dependsOn references exist and no self-dependency.
	for _, n := range rev.Nodes {
		if err := validateUniqueIDs("node "+n.ID+" blockIds", n.BlockIDs); err != nil {
			return err
		}
		if err := validateUniqueIDs("node "+n.ID+" producesSlotIds", n.ProducesSlotIDs); err != nil {
			return err
		}
		if err := validateUniqueIDs("node "+n.ID+" consumesSlotIds", n.ConsumesSlotIDs); err != nil {
			return err
		}
		for _, dep := range n.DependsOn {
			if dep == n.ID {
				return fmt.Errorf("work: definition revision: node %q depends on itself", n.ID)
			}
			if !nodeIDs[dep] {
				return fmt.Errorf("work: definition revision: node %q depends on unknown node %q", n.ID, dep)
			}
		}
	}
	// DAG cycle detection via Kahn's algorithm.
	if err := checkDAGNoCycles(rev.Nodes, nodeIDs); err != nil {
		return err
	}
	// Validate input spec references exist.
	for _, n := range rev.Nodes {
		for _, isID := range n.InputSpecIDs {
			found := false
			for _, is := range rev.InputSpecs {
				if is.ID == isID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("work: definition revision: node %q references unknown input spec %q", n.ID, isID)
			}
		}
	}
	slotIDs := make(map[string]bool, len(rev.ArtifactSlots))
	for i, s := range rev.ArtifactSlots {
		if s.ID == "" {
			return fmt.Errorf("work: definition revision: artifactSlots[%d].id required", i)
		}
		if s.Title == "" {
			return fmt.Errorf("work: definition revision: artifactSlots[%d].title required", i)
		}
		if s.Kind == "" {
			return fmt.Errorf("work: definition revision: artifactSlots[%d].kind required", i)
		}
		if s.ExpectedCount <= 0 {
			return fmt.Errorf("work: definition revision: artifactSlots[%d].expectedCount must be positive", i)
		}
		if slotIDs[s.ID] {
			return fmt.Errorf("work: definition revision: duplicate artifact slot id %q", s.ID)
		}
		slotIDs[s.ID] = true
	}
	for _, n := range rev.Nodes {
		for _, slotID := range append(append([]string(nil), n.ProducesSlotIDs...), n.ConsumesSlotIDs...) {
			if !slotIDs[slotID] {
				return fmt.Errorf("work: definition revision: node %q references unknown artifact slot %q", n.ID, slotID)
			}
		}
	}
	specIDs := make(map[string]bool, len(rev.InputSpecs))
	for i, is := range rev.InputSpecs {
		if is.ID == "" {
			return fmt.Errorf("work: definition revision: inputSpecs[%d].id required", i)
		}
		if is.Label == "" {
			return fmt.Errorf("work: definition revision: inputSpecs[%d].label required", i)
		}
		if !isValidInputKind(is.Kind) {
			return fmt.Errorf("work: definition revision: inputSpecs[%d].kind %q is invalid", i, is.Kind)
		}
		if specIDs[is.ID] {
			return fmt.Errorf("work: definition revision: duplicate input spec id %q", is.ID)
		}
		specIDs[is.ID] = true
	}
	return nil
}

func isValidInputKind(k InputKind) bool {
	switch k {
	case InputText, InputNumber, InputDate, InputChoice, InputMultiChoice,
		InputFile, InputRoster, InputForm, InputApproval:
		return true
	}
	return false
}

func validateUniqueIDs(label string, values []string) error {
	seen := make(map[string]bool, len(values))
	for i, value := range values {
		if value == "" {
			return fmt.Errorf("work: definition revision: %s[%d] is empty", label, i)
		}
		if seen[value] {
			return fmt.Errorf("work: definition revision: %s contains duplicate %q", label, value)
		}
		seen[value] = true
	}
	return nil
}

// ── Definition diff ────────────────────────────────────────────────────────

// DefinitionDiffResult captures what changed between two definition revisions.
// It is used to produce the impact preview before a user applies a new revision.
type DefinitionDiffResult struct {
	// Nodes
	NodesAdded   []NodeDef
	NodesRemoved []NodeDef
	NodesChanged []NodeDefChange

	// Artifact slots
	SlotsAdded   []ArtifactSlotDef
	SlotsRemoved []ArtifactSlotDef
	SlotsChanged []ArtifactSlotDefChange

	// Input specs
	InputSpecsAdded   []InputSpec
	InputSpecsRemoved []InputSpec
	InputSpecsChanged []InputSpecChange

	// Summary
	GoalChanged  bool
	NewGoal      string
	PreviousGoal string
}

// NodeDefChange records a node that exists in both revisions but differs.
type NodeDefChange struct {
	ID            string
	PreviousTitle string
	CurrentTitle  string
	// DepAdded lists dependency IDs that were not present in the base revision.
	DepAdded []string
	// DepRemoved lists dependency IDs that were removed.
	DepRemoved []string
	// InputAdded lists input spec IDs that were not present in the base.
	InputAdded []string
	// InputRemoved lists input spec IDs that were removed.
	InputRemoved []string
	// ToolHintsChanged is true when tool hints differ.
	ToolHintsChanged bool
}

// ArtifactSlotDefChange records a slot that exists in both but differs.
type ArtifactSlotDefChange struct {
	ID           string
	PrevTitle    string
	CurrTitle    string
	PrevKind     string
	CurrKind     string
	CountChanged bool
	ReqChanged   bool
}

// InputSpecChange records an input spec that exists in both but differs.
type InputSpecChange struct {
	ID            string
	PrevLbl       string
	CurrLbl       string
	KindChanged   bool
	ReqChanged    bool
	SchemaChanged bool
}

// ComputeDefinitionDiff compares two revisions and returns what changed.
func ComputeDefinitionDiff(base, candidate *WorkDefinitionRevision) *DefinitionDiffResult {
	d := &DefinitionDiffResult{}
	if base.Goal != candidate.Goal {
		d.GoalChanged = true
		d.PreviousGoal = base.Goal
		d.NewGoal = candidate.Goal
	}
	prevNodes := indexNodes(base.Nodes)
	currNodes := indexNodes(candidate.Nodes)
	for id, cn := range currNodes {
		pn, exists := prevNodes[id]
		if !exists {
			d.NodesAdded = append(d.NodesAdded, cn)
			continue
		}
		chg := NodeDefChange{ID: id}
		hasChange := false
		if pn.Title != cn.Title {
			chg.PreviousTitle = pn.Title
			chg.CurrentTitle = cn.Title
			hasChange = true
		}
		if depDiff := stringSetDiff(pn.DependsOn, cn.DependsOn); len(depDiff.added) > 0 || len(depDiff.removed) > 0 {
			chg.DepAdded = depDiff.added
			chg.DepRemoved = depDiff.removed
			hasChange = true
		}
		if inpDiff := stringSetDiff(pn.InputSpecIDs, cn.InputSpecIDs); len(inpDiff.added) > 0 || len(inpDiff.removed) > 0 {
			chg.InputAdded = inpDiff.added
			chg.InputRemoved = inpDiff.removed
			hasChange = true
		}
		if !stringSliceEq(pn.ToolHints, cn.ToolHints) {
			chg.ToolHintsChanged = true
			hasChange = true
		}
		if hasChange {
			d.NodesChanged = append(d.NodesChanged, chg)
		}
	}
	for id, pn := range prevNodes {
		if _, exists := currNodes[id]; !exists {
			d.NodesRemoved = append(d.NodesRemoved, pn)
		}
	}
	// Artifact slots.
	prevSlots := indexSlots(base.ArtifactSlots)
	currSlots := indexSlots(candidate.ArtifactSlots)
	for id, cs := range currSlots {
		ps, exists := prevSlots[id]
		if !exists {
			d.SlotsAdded = append(d.SlotsAdded, cs)
			continue
		}
		changed := false
		sc := ArtifactSlotDefChange{ID: id, PrevTitle: ps.Title, CurrTitle: cs.Title,
			PrevKind: ps.Kind, CurrKind: cs.Kind}
		if ps.Title != cs.Title || ps.Kind != cs.Kind {
			changed = true
		}
		if ps.ExpectedCount != cs.ExpectedCount {
			sc.CountChanged = true
			changed = true
		}
		if ps.Required != cs.Required {
			sc.ReqChanged = true
			changed = true
		}
		if changed {
			d.SlotsChanged = append(d.SlotsChanged, sc)
		}
	}
	for id, ps := range prevSlots {
		if _, exists := currSlots[id]; !exists {
			d.SlotsRemoved = append(d.SlotsRemoved, ps)
		}
	}
	// Input specs.
	prevSpecs := indexSpecs(base.InputSpecs)
	currSpecs := indexSpecs(candidate.InputSpecs)
	for id, cs := range currSpecs {
		ps, exists := prevSpecs[id]
		if !exists {
			d.InputSpecsAdded = append(d.InputSpecsAdded, cs)
			continue
		}
		changed := false
		sc := InputSpecChange{ID: id, PrevLbl: ps.Label, CurrLbl: cs.Label}
		if ps.Label != cs.Label {
			changed = true
		}
		if ps.Kind != cs.Kind {
			sc.KindChanged = true
			changed = true
		}
		if ps.Required != cs.Required {
			sc.ReqChanged = true
			changed = true
		}
		if !rawMessageEq(ps.ValueSchema, cs.ValueSchema) {
			sc.SchemaChanged = true
			changed = true
		}
		if changed {
			d.InputSpecsChanged = append(d.InputSpecsChanged, sc)
		}
	}
	for id, ps := range prevSpecs {
		if _, exists := currSpecs[id]; !exists {
			d.InputSpecsRemoved = append(d.InputSpecsRemoved, ps)
		}
	}
	return d
}

// HasStructuralChange reports whether the diff involves any node, slot, or
// input spec changes (add/remove/change) that would require re-evaluating
// running tasks.
func (d *DefinitionDiffResult) HasStructuralChange() bool {
	return len(d.NodesAdded)+len(d.NodesRemoved)+len(d.NodesChanged)+
		len(d.SlotsAdded)+len(d.SlotsRemoved)+len(d.SlotsChanged)+
		len(d.InputSpecsAdded)+len(d.InputSpecsRemoved)+len(d.InputSpecsChanged) > 0 ||
		d.GoalChanged
}

// ── Run impact classification ──────────────────────────────────────────────

// RunImpact describes how each node from the old definition is affected when
// a new definition revision is applied.
type RunImpact struct {
	// KeptNodeIDs are node IDs that are semantically unchanged and can
	// continue running or keep their completed status.
	KeptNodeIDs []string `json:"keptNodeIds"`
	// InvalidatedNodeIDs are nodes whose dependencies, inputs, or tool hints
	// changed — any completed task for these nodes must be re-run.
	InvalidatedNodeIDs []string `json:"invalidatedNodeIds"`
	// NewNodeIDs are nodes that exist only in the new revision.
	NewNodeIDs []string `json:"newNodeIds"`
	// RemovedNodeIDs are nodes that were in the old revision but not the new.
	RemovedNodeIDs []string `json:"removedNodeIds"`
	// RequiresRerun is true when at least one completed task must re-run.
	RequiresRerun bool `json:"requiresRerun"`
}

// ClassifyRunImpact determines how an existing run's tasks are affected by
// switching from oldRev to newRev. Results are deterministically sorted.
func ClassifyRunImpact(oldRev, newRev *WorkDefinitionRevision) *RunImpact {
	ri := &RunImpact{}
	oldNodes := indexNodes(oldRev.Nodes)
	newNodes := indexNodes(newRev.Nodes)
	oldSpecs := indexSpecs(oldRev.InputSpecs)
	newSpecs := indexSpecs(newRev.InputSpecs)
	invalidated := make(map[string]bool)
	for id := range newNodes {
		if _, exists := oldNodes[id]; !exists {
			ri.NewNodeIDs = append(ri.NewNodeIDs, id)
		}
	}
	sort.Strings(ri.NewNodeIDs)
	for id, on := range oldNodes {
		nn, exists := newNodes[id]
		if !exists {
			ri.RemovedNodeIDs = append(ri.RemovedNodeIDs, id)
			continue
		}
		if nodeExecutionDigest(on, oldSpecs) != nodeExecutionDigest(nn, newSpecs) {
			invalidated[id] = true
		}
	}

	// Propagate upstream semantic changes through the new DAG. Existing
	// descendants must rerun even when their own definition bytes are stable.
	descendants := make(map[string][]string, len(newNodes))
	for _, node := range newRev.Nodes {
		for _, dependency := range node.DependsOn {
			descendants[dependency] = append(descendants[dependency], node.ID)
		}
	}
	queue := make([]string, 0, len(invalidated))
	for id := range invalidated {
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, descendant := range descendants[id] {
			if invalidated[descendant] {
				continue
			}
			if _, existed := oldNodes[descendant]; !existed {
				continue
			}
			invalidated[descendant] = true
			queue = append(queue, descendant)
		}
	}
	for id := range oldNodes {
		if _, exists := newNodes[id]; !exists {
			continue
		}
		if invalidated[id] {
			ri.InvalidatedNodeIDs = append(ri.InvalidatedNodeIDs, id)
		} else {
			ri.KeptNodeIDs = append(ri.KeptNodeIDs, id)
		}
	}
	sort.Strings(ri.RemovedNodeIDs)
	sort.Strings(ri.InvalidatedNodeIDs)
	sort.Strings(ri.KeptNodeIDs)
	ri.RequiresRerun = len(ri.InvalidatedNodeIDs) > 0
	return ri
}

func nodeExecutionDigest(node NodeDef, specs map[string]InputSpec) string {
	normalized := node
	normalized.DependsOn = append([]string(nil), node.DependsOn...)
	normalized.InputSpecIDs = append([]string(nil), node.InputSpecIDs...)
	normalized.ToolHints = append([]string(nil), node.ToolHints...)
	normalized.BlockIDs = append([]string(nil), node.BlockIDs...)
	normalized.ProducesSlotIDs = append([]string(nil), node.ProducesSlotIDs...)
	normalized.ConsumesSlotIDs = append([]string(nil), node.ConsumesSlotIDs...)
	sort.Strings(normalized.DependsOn)
	sort.Strings(normalized.InputSpecIDs)
	sort.Strings(normalized.ToolHints)
	sort.Strings(normalized.BlockIDs)
	sort.Strings(normalized.ProducesSlotIDs)
	sort.Strings(normalized.ConsumesSlotIDs)

	type inputSemantics struct {
		ID           string
		Kind         InputKind
		Required     bool
		ValueSchema  json.RawMessage
		DefaultValue json.RawMessage
	}
	inputs := make([]inputSemantics, 0, len(normalized.InputSpecIDs))
	for _, id := range normalized.InputSpecIDs {
		spec, ok := specs[id]
		if !ok {
			inputs = append(inputs, inputSemantics{ID: id})
			continue
		}
		inputs = append(inputs, inputSemantics{
			ID: id, Kind: spec.Kind, Required: spec.Required,
			ValueSchema: spec.ValueSchema, DefaultValue: spec.DefaultValue,
		})
	}
	_, digest, err := canonicalDigest(struct {
		Node   NodeDef
		Inputs []inputSemantics
	}{Node: normalized, Inputs: inputs})
	if err != nil {
		return "invalid:" + err.Error()
	}
	return digest
}

// ── Helpers ────────────────────────────────────────────────────────────────

func int64Ptr(v int64) *int64 { return &v }

func indexNodes(nodes []NodeDef) map[string]NodeDef {
	m := make(map[string]NodeDef, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n
	}
	return m
}

func indexSlots(slots []ArtifactSlotDef) map[string]ArtifactSlotDef {
	m := make(map[string]ArtifactSlotDef, len(slots))
	for _, s := range slots {
		m[s.ID] = s
	}
	return m
}

func indexSpecs(specs []InputSpec) map[string]InputSpec {
	m := make(map[string]InputSpec, len(specs))
	for _, s := range specs {
		m[s.ID] = s
	}
	return m
}

type setDiff struct {
	added   []string
	removed []string
}

func stringSetDiff(old, new []string) setDiff {
	oldSet := make(map[string]bool, len(old))
	for _, v := range old {
		oldSet[v] = true
	}
	newSet := make(map[string]bool, len(new))
	for _, v := range new {
		newSet[v] = true
	}
	var d setDiff
	for v := range newSet {
		if !oldSet[v] {
			d.added = append(d.added, v)
		}
	}
	for v := range oldSet {
		if !newSet[v] {
			d.removed = append(d.removed, v)
		}
	}
	return d
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	// Both sorted in canonical form; order matters.
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func rawMessageEq(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return string(a) == string(b)
}

// checkDAGNoCycles uses Kahn's algorithm to detect cycles in the node DAG.
func checkDAGNoCycles(nodes []NodeDef, nodeIDs map[string]bool) error {
	inDegree := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		if _, ok := inDegree[n.ID]; !ok {
			inDegree[n.ID] = 0
		}
		for _, dep := range n.DependsOn {
			adj[dep] = append(adj[dep], n.ID)
			inDegree[n.ID]++
		}
	}
	queue := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if inDegree[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adj[id] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(nodes) {
		return fmt.Errorf("work: definition revision: DAG contains a cycle")
	}
	return nil
}

// CopyOnWriteRevision creates a new revision from a parent with modifications
// supplied by the caller. The returned revision has Revision = parent.Revision + 1,
// ParentRevision = parent.Revision, Status = draft, and Digest cleared.
// The caller must populate Nodes, ArtifactSlots, InputSpecs, Goal, etc.
func CopyOnWriteRevision(parent *WorkDefinitionRevision) *WorkDefinitionRevision {
	rev := *parent
	rev.Nodes = make([]NodeDef, len(parent.Nodes))
	for i := range parent.Nodes {
		rev.Nodes[i] = parent.Nodes[i]
		rev.Nodes[i].DependsOn = append([]string(nil), parent.Nodes[i].DependsOn...)
		rev.Nodes[i].InputSpecIDs = append([]string(nil), parent.Nodes[i].InputSpecIDs...)
		rev.Nodes[i].ToolHints = append([]string(nil), parent.Nodes[i].ToolHints...)
		rev.Nodes[i].BlockIDs = append([]string(nil), parent.Nodes[i].BlockIDs...)
		rev.Nodes[i].ProducesSlotIDs = append([]string(nil), parent.Nodes[i].ProducesSlotIDs...)
		rev.Nodes[i].ConsumesSlotIDs = append([]string(nil), parent.Nodes[i].ConsumesSlotIDs...)
	}
	rev.ArtifactSlots = append([]ArtifactSlotDef(nil), parent.ArtifactSlots...)
	rev.InputSpecs = make([]InputSpec, len(parent.InputSpecs))
	for i := range parent.InputSpecs {
		rev.InputSpecs[i] = parent.InputSpecs[i]
		rev.InputSpecs[i].ValueSchema = append(json.RawMessage(nil), parent.InputSpecs[i].ValueSchema...)
		rev.InputSpecs[i].DefaultValue = append(json.RawMessage(nil), parent.InputSpecs[i].DefaultValue...)
	}
	rev.ParentRevision = parent.Revision
	rev.Revision = parent.Revision + 1
	rev.Status = DefDraft
	rev.Digest = ""
	rev.CreatedAt = time.Time{}
	return &rev
}

// ── Transport intent ───────────────────────────────────────────────────────

// AutoSwitchFaceIntent is a transport-layer intent emitted when the domain
// layer determines the frontend should switch to the execution face. The
// Controller/fe interprets this; the domain layer never references UI terms.
// Payload is carried as a ViewAttention event.
type AutoSwitchFaceIntent struct {
	WorkID        string `json:"workId"`
	RunID         string `json:"runId"`
	DefinitionRev int64  `json:"definitionRev"`
	Reason        string `json:"reason"`
}

// ── In-memory fallback DefinitionRevisionStore ───────────────────────────

// mapDefinitionRevisionStore provides an in-memory fallback when no file
// store is configured. It is only for testing; production must use
// FileWorkStore. Load returns a deep copy.
type mapDefinitionRevisionStore struct {
	mu   sync.Mutex
	revs map[string]map[int64][]byte // workID → revision → canonical JSON
}

func newMapDefinitionRevisionStore() *mapDefinitionRevisionStore {
	return &mapDefinitionRevisionStore{
		revs: make(map[string]map[int64][]byte),
	}
}

func (s *mapDefinitionRevisionStore) StoreRevision(workID string, rev *WorkDefinitionRevision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.revs[workID]
	if !ok {
		m = make(map[int64][]byte)
		s.revs[workID] = m
	}
	data, err := json.Marshal(rev)
	if err != nil {
		return err
	}
	// Idempotent: same bytes OK.
	if existing, exists := m[rev.Revision]; exists {
		if string(existing) == string(data) {
			return nil
		}
		return &ErrWorkEventConflict{
			WorkID: workID,
			Reason: fmt.Sprintf("definition revision %d already stored with different content", rev.Revision),
			Kind:   WorkEventRequestConflict,
		}
	}
	m[rev.Revision] = data
	return nil
}

func (s *mapDefinitionRevisionStore) LoadRevision(workID string, revision int64) (*WorkDefinitionRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.revs[workID]
	if m == nil {
		return nil, fmt.Errorf("work: definition revision %d for Work %s not found", revision, workID)
	}
	data := m[revision]
	if data == nil {
		return nil, fmt.Errorf("work: definition revision %d for Work %s not found", revision, workID)
	}
	var rev WorkDefinitionRevision
	if err := json.Unmarshal(data, &rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

func (s *mapDefinitionRevisionStore) LoadLatestRevision(workID string) (*WorkDefinitionRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.revs[workID]
	if m == nil || len(m) == 0 {
		return nil, nil
	}
	var latest int64
	for rev := range m {
		if rev > latest {
			latest = rev
		}
	}
	data := m[latest]
	var rev WorkDefinitionRevision
	if err := json.Unmarshal(data, &rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

var _ DefinitionRevisionStore = (*mapDefinitionRevisionStore)(nil)
