package work

import (
	"fmt"
	"strings"
)

// ── Typed patch path compiler ──────────────────────────────────────────────

// PatchPathKind classifies the root segment of a typed patch path.
type PatchPathKind string

const (
	PathRoot   PatchPathKind = "root"          // goal
	PathNodes  PatchPathKind = "nodes"         // nodes/<id>/...
	PathSlots  PatchPathKind = "artifactSlots" // artifactSlots/<id>/...
	PathSpecs  PatchPathKind = "inputSpecs"    // inputSpecs/<id>/...
	PathBlocks PatchPathKind = "blocks"        // blocks/<id>/...
)

// PatchPath is a compiled, validated, typed patch path. It can only be
// produced by CompilePatchPath; there is no zero-value constructor.
type PatchPath struct {
	Kind     PatchPathKind
	Segments []string // root → leaf, e.g. ["nodes", "n1", "title"]
	Leaf     string   // last segment — the field being patched
}

// CompilePatchPath parses and validates a string path against the whitelist.
// Unknown roots, forbidden zones, and malformed paths are rejected.
func CompilePatchPath(raw string) (PatchPath, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return PatchPath{}, fmt.Errorf("work: patch path is empty")
	}
	// Path must not contain ".." or start with "/".
	if strings.Contains(raw, "..") {
		return PatchPath{}, fmt.Errorf("work: patch path %q contains disallowed traversal", raw)
	}
	raw = strings.TrimPrefix(raw, "/")
	if raw == "" {
		return PatchPath{}, fmt.Errorf("work: patch path is empty after trimming")
	}

	segs := strings.Split(raw, "/")
	for _, s := range segs {
		if s == "" {
			return PatchPath{}, fmt.Errorf("work: patch path %q has empty segment", raw)
		}
	}

	root := segs[0]
	if err := checkForbiddenRoot(root); err != nil {
		return PatchPath{}, err
	}

	var kind PatchPathKind
	switch root {
	case "goal":
		if len(segs) != 1 {
			return PatchPath{}, fmt.Errorf("work: patch path %q: 'goal' takes no sub-segments", raw)
		}
		return PatchPath{Kind: PathRoot, Segments: segs, Leaf: "goal"}, nil
	case "nodes":
		kind = PathNodes
	case "artifactSlots":
		kind = PathSlots
	case "inputSpecs":
		kind = PathSpecs
	case "blocks":
		kind = PathBlocks
	default:
		return PatchPath{}, fmt.Errorf("work: patch path %q: unknown root segment %q", raw, root)
	}

	if len(segs) < 2 {
		return PatchPath{}, fmt.Errorf("work: patch path %q: root %q requires an id segment", raw, root)
	}
	// The second segment is the object ID.
	id := segs[1]
	if id == "" {
		return PatchPath{}, fmt.Errorf("work: patch path %q: id segment is empty", raw)
	}

	// For nodes/artifactSlots/inputSpecs: path is <root>/<id>/<field>.
	// For blocks: path is blocks/<id>/<field> (or deeper for block data).
	// For runs: path is runs/<runID>/tasks/<taskID>/<field>.

	leafIdx := len(segs) - 1
	leaf := segs[leafIdx]

	if err := validateLeafField(kind, leaf, segs); err != nil {
		return PatchPath{}, err
	}

	return PatchPath{Kind: kind, Segments: segs, Leaf: leaf}, nil
}

// ── Whitelists ─────────────────────────────────────────────────────────────

// allowedNodeFields is the exhaustive set of mutable fields on NodeDef.
var allowedNodeFields = map[string]bool{
	"title":           true,
	"description":     true,
	"dependsOn":       true,
	"inputSpecIds":    true,
	"toolHints":       true,
	"blockIds":        true,
	"producesSlotIds": true,
	"consumesSlotIds": true,
}

// allowedSlotFields is the exhaustive set of mutable fields on ArtifactSlotDef.
var allowedSlotFields = map[string]bool{
	"title":         true,
	"kind":          true,
	"expectedCount": true,
	"required":      true,
}

// allowedSpecFields is the exhaustive set of mutable fields on InputSpec.
var allowedSpecFields = map[string]bool{
	"label":        true,
	"description":  true,
	"kind":         true,
	"required":     true,
	"valueSchema":  true,
	"defaultValue": true,
	"pinEligible":  true,
}

// allowedBlockFields is the exhaustive set of mutable fields for block scope patches.
var allowedBlockFields = map[string]bool{
	"title": true,
	"data":  true,
}

// forbiddenRoots are root segments that must never be patched.
var forbiddenRoots = map[string]string{
	"permission":        "permission is managed by the permission system",
	"permissions":       "permissions are managed by the permission system",
	"permissionPolicy":  "permission policy is managed by the permission system",
	"permission_policy": "permission policy is managed by the permission system",
	"actionReceipt":     "action receipts are immutable execution records",
	"actionReceipts":    "action receipts are immutable execution records",
	"action_receipt":    "action receipts are immutable execution records",
	"action_receipts":   "action receipts are immutable execution records",
	"action":            "actions must go through Action Intent",
	"runtime":           "runtime state is managed by the scheduler",
	"runtimeState":      "runtime state is managed by the scheduler",
	"runtime_state":     "runtime state is managed by the scheduler",
	"source":            "source is immutable block registration metadata",
	"schema":            "schema is a frozen contract",
	"schemaVersion":     "schema version is a frozen contract",
	"secret":            "secrets are managed by the credential store",
	"secrets":           "secrets are managed by the credential store",
	"credentials":       "credentials are managed by the credential store",
	"runs":              "runtime task state is managed by the scheduler",
}

func checkForbiddenRoot(root string) error {
	for forbidden, reason := range forbiddenRoots {
		if strings.EqualFold(root, forbidden) {
			return fmt.Errorf("work: patch path root %q is forbidden: %s", root, reason)
		}
	}
	return nil
}

func validateLeafField(kind PatchPathKind, leaf string, segs []string) error {
	switch kind {
	case PathNodes:
		if !allowedNodeFields[leaf] {
			return fmt.Errorf("work: patch path leaf %q is not an allowed NodeDef field", leaf)
		}
		if len(segs) != 3 {
			return fmt.Errorf("work: patch path %q: nodes path must be nodes/<id>/<field>", strings.Join(segs, "/"))
		}
	case PathSlots:
		if !allowedSlotFields[leaf] {
			return fmt.Errorf("work: patch path leaf %q is not an allowed ArtifactSlotDef field", leaf)
		}
		if len(segs) != 3 {
			return fmt.Errorf("work: patch path %q: artifactSlots path must be artifactSlots/<id>/<field>", strings.Join(segs, "/"))
		}
	case PathSpecs:
		if !allowedSpecFields[leaf] {
			return fmt.Errorf("work: patch path leaf %q is not an allowed InputSpec field", leaf)
		}
		if len(segs) != 3 {
			return fmt.Errorf("work: patch path %q: inputSpecs path must be inputSpecs/<id>/<field>", strings.Join(segs, "/"))
		}
	case PathBlocks:
		if !allowedBlockFields[leaf] {
			return fmt.Errorf("work: patch path leaf %q is not an allowed Block field", leaf)
		}
		if len(segs) != 3 {
			return fmt.Errorf("work: patch path %q: blocks path must be blocks/<id>/<field>", strings.Join(segs, "/"))
		}
	}
	return nil
}

// ── Validation helpers ────────────────────────────────────────────────────

// ValidatePatchOps checks every operation in a preview against the path
// compiler. Returns the first invalid path error.
func ValidatePatchOps(ops []PatchOp) error {
	for i, op := range ops {
		if op.Op == "" {
			return fmt.Errorf("work: patch op[%d]: op is required", i)
		}
		if op.Path == "" {
			return fmt.Errorf("work: patch op[%d]: path is required", i)
		}
		if err := ValidatePatchOpVerb(op.Op); err != nil {
			return fmt.Errorf("work: patch op[%d]: %w", i, err)
		}
		if _, err := CompilePatchPath(op.Path); err != nil {
			return fmt.Errorf("work: patch op[%d] %q: %w", i, op.Path, err)
		}
	}
	return nil
}

// AllowedPatchOps is the set of operation verbs.
var AllowedPatchOps = map[string]bool{
	"replace": true,
	"add":     true,
	"remove":  true,
}

// ValidatePatchOpVerb returns an error if the operation verb is unknown.
func ValidatePatchOpVerb(op string) error {
	if !AllowedPatchOps[op] {
		return fmt.Errorf("work: unknown patch op %q; allowed: replace, add, remove", op)
	}
	return nil
}
