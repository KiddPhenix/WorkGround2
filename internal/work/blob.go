package work

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ── BlobStore ──────────────────────────────────────────────────────────────

// BlobStore is the content-addressed persistent blob storage narrow port.
// Blobs are immutable once written; their storage key is the SHA-256 digest
// of the content prefixed with "sha256:".
type BlobStore interface {
	// Put writes data and returns its content digest. It is idempotent:
	// repeated writes of the same content are safe and return the same digest.
	Put(workID string, data []byte) (digest string, err error)

	// Get reads a blob by digest. It verifies the returned content against the
	// digest; mismatches return ErrWorkDigestMismatch wrapped in ErrWorkNeedsRepair.
	Get(workID, digest string) ([]byte, error)

	// Exists reports whether a blob with the given digest is present and passes
	// integrity verification.
	Exists(workID, digest string) (bool, error)

	// Delete removes a blob by digest. Idempotent: returns nil if already absent.
	Delete(workID, digest string) error

	// ListDigests returns every blob digest stored under a work directory.
	ListDigests(workID string) ([]string, error)
}

// ── Inline threshold ────────────────────────────────────────────────────────

// CornerstoneInlineThreshold is the maximum byte size for a Cornerstone's
// content to be stored inline in the event payload. Larger content is written
// to the BlobStore and referenced via Ref.BlobDigest.
const CornerstoneInlineThreshold = 4096

// ── Digest helpers ──────────────────────────────────────────────────────────

// ContentDigest computes the SHA-256 hex digest of data and returns it with the
// "sha256:" prefix.
func ContentDigest(data []byte) string {
	h := sha256.Sum256(data)
	return digestPrefix + fmt.Sprintf("%x", h[:])
}

// ── Secret detection ────────────────────────────────────────────────────────

// IsSecretLike reports whether the given bytes look like they contain secret
// material. This is a heuristic guard; callers must combine it with other
// checks (structured type markers, source flags) and never log or persist
// confirmed secret content.
func IsSecretLike(data []byte) bool {
	value := string(data)
	if secretAssignmentLike(value) {
		return true
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization: bearer ", "-----begin private key-----", "-----begin rsa private key-----", "ghp_", "github_pat_"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Contains(value, "AKIA") && len(value) >= 20
}

// ErrSecretRejected is returned when an operation attempts to persist, log, or
// otherwise handle content that is flagged as secret-like.
var ErrSecretRejected = errors.New("work: secret-like content rejected — secrets must not be stored as plaintext")

// normalizeCornerstoneContent makes persisted snapshots and their digests
// independent of platform line endings. Invalid UTF-8 is replaced explicitly
// so a stable ID can never depend on bytes that JSON would rewrite later.
func normalizeCornerstoneContent(content string) string {
	content = strings.ToValidUTF8(content, string(utf8.RuneError))
	content = strings.ReplaceAll(content, "\r\n", "\n")
	return strings.ReplaceAll(content, "\r", "\n")
}

// ── Ref normalization ───────────────────────────────────────────────────────

// normalizeCornerstoneRef returns a canonical JSON representation of a
// CornerstoneRef suitable for stable ID computation.
func normalizeCornerstoneRef(ref CornerstoneRef) ([]byte, error) {
	// Zero out BlobDigest — it's a storage detail, not identity.
	r := normalizedCornerstoneRef(ref)
	r.BlobDigest = ""
	return canonicalJSON(r)
}

// ── JSON canonicalization (mirrors snapshot.go but operates on cornerstones) ──

// canonicalCornerstoneInput produces a stable byte representation of the
// identity fields of a CornerstoneInput for stable ID computation.
func canonicalCornerstoneInput(input CornerstoneInput) ([]byte, error) {
	refJSON, err := normalizeCornerstoneRef(input.Ref)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: normalize ref: %w", err)
	}
	// Title, mode, required and tags are mutable presentation/behaviour fields,
	// not object identity. Section 7.4 defines identity as Work + type +
	// normalized ref + normalized content.
	identity := map[string]any{
		"type":    string(input.Type),
		"ref":     json.RawMessage(refJSON),
		"content": normalizeCornerstoneContent(input.Content),
	}
	canon, err := canonicalJSON(identity)
	if err != nil {
		return nil, fmt.Errorf("cornerstone: canonical identity: %w", err)
	}
	return canon, nil
}
