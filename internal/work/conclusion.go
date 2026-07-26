package work

import "time"

// ── Conclusion ─────────────────────────────────────────────────────────────

// ConclusionKind classifies a structured conclusion produced by a Work run.
type ConclusionKind string

const (
	ConclusionFact     ConclusionKind = "fact"
	ConclusionFinding  ConclusionKind = "finding"
	ConclusionDecision ConclusionKind = "decision"
	ConclusionOutcome  ConclusionKind = "outcome"
	ConclusionLesson   ConclusionKind = "lesson"
)

// Conclusion is a traceable, confirmable, supersedable structured conclusion.
type Conclusion struct {
	ID          string         `json:"id"`
	Kind        ConclusionKind `json:"kind"`
	Status      string         `json:"status"` // proposed | confirmed | superseded
	Title       string         `json:"title"`
	Summary     string         `json:"summary"`
	Evidence    []SourceRef    `json:"evidence,omitempty"`
	Artifacts   []ArtifactRef  `json:"artifacts,omitempty"`
	NextSteps   []string       `json:"nextSteps,omitempty"`
	Supersedes  string         `json:"supersedes,omitempty"`
	GeneratedAt time.Time      `json:"generatedAt"`
}

// ── ArtifactRef ────────────────────────────────────────────────────────────

const (
	ArtifactRefStatusAvailable = "available"
	ArtifactRefStatusStale     = "stale"
	ArtifactRefStatusMissing   = "missing"
	ArtifactRefStatusFailed    = "failed"
)

func validArtifactRefStatus(status string) bool {
	switch status {
	case ArtifactRefStatusAvailable, ArtifactRefStatusStale, ArtifactRefStatusMissing, ArtifactRefStatusFailed:
		return true
	default:
		return false
	}
}

// ArtifactRef references an artifact produced by a Work run.
type ArtifactRef struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Type           string     `json:"type"`
	Status         string     `json:"status"` // ArtifactRefStatus*
	Path           string     `json:"path,omitempty"`
	RelativePath   string     `json:"relativePath,omitempty"`
	BlobDigest     string     `json:"blobDigest,omitempty"`
	SourceRunID    string     `json:"sourceRunId,omitempty"`
	LastVerifiedAt *time.Time `json:"lastVerifiedAt,omitempty"`
	Error          string     `json:"error,omitempty"`
}

// ── SessionRef ─────────────────────────────────────────────────────────────

// SessionRef is a lightweight reference to a WorkGround2 Session. It does not
// hold the full session content.
type SessionRef struct {
	SessionPath string    `json:"sessionPath"`
	BranchID    string    `json:"branchId"`
	ModelRef    string    `json:"modelRef"`
	TurnCount   int       `json:"turnCount"`
	Preview     string    `json:"preview"`
	StartedAt   time.Time `json:"startedAt"`
}

// ── SourceRef ──────────────────────────────────────────────────────────────

// SourceRef records where a piece of evidence, conclusion, or artifact
// originated.
type SourceRef struct {
	Kind     string `json:"kind"` // work | session_turn | block | artifact | file | url
	WorkID   string `json:"workId,omitempty"`
	ObjectID string `json:"objectId,omitempty"`
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	Digest   string `json:"digest,omitempty"`
}
