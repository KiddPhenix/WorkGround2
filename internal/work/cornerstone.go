package work

import "time"

// ── Cornerstone ────────────────────────────────────────────────────────────

// Cornerstone is a typed long-term memory fragment explicitly pinned by the
// user. Unlike PinnedMemory (session-scoped), a Cornerstone is bound to a Work
// and survives archival as well as regular memory cleanup/compaction.
type Cornerstone struct {
	ID             string            `json:"id"`
	WorkID         string            `json:"workId"`
	Type           CornerstoneType   `json:"type"`
	Title          string            `json:"title"`
	Content        string            `json:"content,omitempty"`
	Ref            CornerstoneRef    `json:"ref"`
	Mode           CornerstoneMode   `json:"mode"`
	Digest         string            `json:"digest"`
	Required       bool              `json:"required"`
	Status         CornerstoneStatus `json:"status"`
	Tags           []string          `json:"tags,omitempty"`
	Provenance     SourceRef         `json:"provenance"`
	LastVerifiedAt *time.Time        `json:"lastVerifiedAt,omitempty"`
	PinnedAt       time.Time         `json:"pinnedAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	Error          string            `json:"error,omitempty"`
	// Tombstone marks a logically removed Cornerstone. Removed cornerstones
	// retain their event history and can be restored via Undo.
	Tombstone bool `json:"tombstone,omitempty"`
}

// CornerstoneType classifies what a Cornerstone represents.
type CornerstoneType string

const (
	CornerstoneInstruction  CornerstoneType = "instruction"
	CornerstoneFileRef      CornerstoneType = "file_ref"
	CornerstoneFileSnapshot CornerstoneType = "file_snapshot"
	CornerstoneDecision     CornerstoneType = "decision"
	CornerstoneConclusion   CornerstoneType = "conclusion"
	CornerstoneSource       CornerstoneType = "source"
	CornerstonePolicy       CornerstoneType = "policy"
	CornerstoneParameter    CornerstoneType = "parameter"
)

// CornerstoneRef describes where a live_ref Cornerstone fetches its content
// from, or where a snapshot Cornerstone was originally sourced.
type CornerstoneRef struct {
	Kind       string `json:"kind"` // inline | session_turn | workspace_file | artifact | url
	SessionID  string `json:"sessionId,omitempty"`
	Turn       int    `json:"turn,omitempty"`
	Path       string `json:"path,omitempty"`
	ArtifactID string `json:"artifactId,omitempty"`
	URL        string `json:"url,omitempty"`
	BlobDigest string `json:"blobDigest,omitempty"`
}

// CornerstoneMode controls whether the content is a live reference or a frozen
// snapshot.
type CornerstoneMode string

const (
	CornerstoneLiveRef  CornerstoneMode = "live_ref"
	CornerstoneSnapshot CornerstoneMode = "snapshot"
)

// CornerstoneStatus is the resolved state of a Cornerstone after the last
// verification.
type CornerstoneStatus string

const (
	CornerstoneActive  CornerstoneStatus = "active"
	CornerstoneStale   CornerstoneStatus = "stale"
	CornerstoneMissing CornerstoneStatus = "missing"
	CornerstoneDenied  CornerstoneStatus = "denied"
	CornerstoneInvalid CornerstoneStatus = "invalid"
)

// CornerstoneReq describes a Cornerstone that a Blueprint expects.
type CornerstoneReq struct {
	Type     CornerstoneType `json:"type"`
	Required bool            `json:"required"`
	Label    string          `json:"label,omitempty"`
}
