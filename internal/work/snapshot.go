package work

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// ── WorkDefinitionSnapshot ─────────────────────────────────────────────────

// WorkDefinitionSnapshot is a copy-on-write frozen revision of the definition
// that a Work was created with (or edited into). Each WorkflowRun records the
// digest it executed against. Edits produce a new revision; old revisions are
// immutable.
type WorkDefinitionSnapshot struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Revision        int64             `json:"revision"`
	BlueprintRef    BlueprintRef      `json:"blueprintRef"`
	InputSchema     json.RawMessage   `json:"inputSchema,omitempty"`
	PromptTemplate  string            `json:"promptTemplate"`
	Workflow        WorkflowDef       `json:"workflow"`
	BlockSpecs      []BlockSpec       `json:"blockSpecs"`
	CornerstoneReqs []CornerstoneReq  `json:"cornerstoneRequirements,omitempty"`
	ConclusionKinds []ConclusionKind  `json:"conclusionKinds,omitempty"`
	ArtifactKinds   []string          `json:"artifactKinds,omitempty"`
	ToolContracts   []ToolContractRef `json:"toolContracts,omitempty"`
	Digest          string            `json:"digest"`
}

// ── Digest ─────────────────────────────────────────────────────────────────

// digestPrefix is prepended to every SHA-256 hex digest so consumers can
// recognise the algorithm.
const digestPrefix = "sha256:"

// NormalizeDefinitionSnapshot returns a deep copy of s with the Digest field
// cleared, all inner JSON (maps, RawMessage) canonicalised into stable sorted-
// key compact form, and the correct Digest populated. The original s is never
// mutated.
//
// Canonicalisation ensures that the same logical snapshot always produces the
// same Digest regardless of key order or whitespace in raw JSON blobs.
func NormalizeDefinitionSnapshot(s *WorkDefinitionSnapshot) (*WorkDefinitionSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("work: cannot normalise nil DefinitionSnapshot")
	}
	if err := CheckSchemaVersion("DefinitionSnapshot", s.SchemaVersion); err != nil {
		return nil, err
	}
	// Marshal → Unmarshal is the cheapest correct deep copy.
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("work: normalise marshal: %w", err)
	}
	var copy WorkDefinitionSnapshot
	if err := json.Unmarshal(raw, &copy); err != nil {
		return nil, fmt.Errorf("work: normalise unmarshal: %w", err)
	}
	copy.Digest = ""

	// Re-marshal through canonicalJSON to get stable output.
	canon, digest, err := canonicalDigest(&copy)
	if err != nil {
		return nil, err
	}
	// Unmarshal the canonical form back to get a perfectly round-tripped copy.
	var out WorkDefinitionSnapshot
	if err := json.Unmarshal(canon, &out); err != nil {
		return nil, fmt.Errorf("work: normalise final unmarshal: %w", err)
	}
	out.Digest = digestPrefix + digest
	return &out, nil
}

// ComputeDigest returns the stable sha256: digest for a DefinitionSnapshot.
// The Digest field on s is ignored during computation (set to "" internally),
// and s is never mutated.
func ComputeDigest(s *WorkDefinitionSnapshot) (string, error) {
	if s == nil {
		return "", fmt.Errorf("work: cannot compute digest of nil DefinitionSnapshot")
	}
	if err := CheckSchemaVersion("DefinitionSnapshot", s.SchemaVersion); err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("work: compute digest marshal: %w", err)
	}
	var copy WorkDefinitionSnapshot
	if err := json.Unmarshal(raw, &copy); err != nil {
		return "", fmt.Errorf("work: compute digest unmarshal: %w", err)
	}
	copy.Digest = ""
	_, digest, err := canonicalDigest(&copy)
	if err != nil {
		return "", err
	}
	return digestPrefix + digest, nil
}

// canonicalDigest marshals v to stable canonical JSON and returns both the
// bytes and the hex-encoded SHA-256 digest.
func canonicalDigest(v any) ([]byte, string, error) {
	canon, err := canonicalJSON(v)
	if err != nil {
		return nil, "", fmt.Errorf("work: canonical marshal: %w", err)
	}
	h := sha256.Sum256(canon)
	return canon, fmt.Sprintf("%x", h[:]), nil
}

// canonicalJSON marshals v to compact JSON with deterministically sorted
// object keys. It round-trips through a generic structure so any json.RawMessage
// blobs are also normalised.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	normalized := normalizeGeneric(generic)
	return json.Marshal(normalized)
}

// normalizeGeneric walks a generic JSON value and returns a deep copy with
// every object's keys in sorted order. RawMessage values have already become
// nested JSON values during the generic decode.
func normalizeGeneric(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, vv := range val {
			out[k] = normalizeGeneric(vv)
		}
		return sortedMap(out)
	case []any:
		out := make([]any, len(val))
		for i, vv := range val {
			out[i] = normalizeGeneric(vv)
		}
		return out
	default:
		return val
	}
}

// sortedMap wraps a map in a type that json.Marshal serialises with sorted
// keys. Go's encoding/json already sorts map keys by default, but we use an
// explicit wrapper to be explicit about the contract.
type sortedMap map[string]any

func (s sortedMap) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		keyBytes, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf = append(buf, keyBytes...)
		buf = append(buf, ':')
		valBytes, err := json.Marshal(s[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, valBytes...)
	}
	buf = append(buf, '}')
	return buf, nil
}
