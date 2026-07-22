package work

import (
	"encoding/json"
	"fmt"
	"time"
)

// WorkEventSchemaVersion is the persisted event contract version.
const WorkEventSchemaVersion = SchemaVersion

// WorkViewSchemaVersion is the frontend transport contract version. It evolves
// independently from the persisted Work schema.
const WorkViewSchemaVersion = 1

// WorkEvent is an append-only persisted domain fact. It is accepted only by
// WorkStore.Append and is never sent directly to a frontend.
type WorkEvent struct {
	SchemaVersion int             `json:"schemaVersion"`
	ID            string          `json:"id"`
	RequestID     string          `json:"requestId"`
	WorkID        string          `json:"workId"`
	Type          WorkEventType   `json:"type"`
	Revision      int64           `json:"revision"`
	BaseRevision  int64           `json:"baseRevision"`
	Payload       json.RawMessage `json:"payload"`
	ContentDigest string          `json:"contentDigest"`
	WriterID      string          `json:"writerId"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// WorkEventType identifies a persisted domain fact.
type WorkEventType string

type cornerstoneRestorePayload struct {
	CornerstoneID  string            `json:"cornerstoneId"`
	Status         CornerstoneStatus `json:"status,omitempty"`
	Error          string            `json:"error,omitempty"`
	LastVerifiedAt *time.Time        `json:"lastVerifiedAt,omitempty"`
}

const (
	EventWorkCreated         WorkEventType = "work.created"
	EventDefinitionFrozen    WorkEventType = "definition.frozen"
	EventDraftUpdated        WorkEventType = "draft.updated"
	EventRunStarted          WorkEventType = "run.started"
	EventRunChanged          WorkEventType = "run.changed"
	EventStageChanged        WorkEventType = "stage.changed"
	EventTaskChanged         WorkEventType = "task.changed"
	EventAttemptChanged      WorkEventType = "attempt.changed"
	EventBlockUpserted       WorkEventType = "block.upserted"
	EventBlockRemoved        WorkEventType = "block.removed"
	EventCornerstoneUpserted WorkEventType = "cornerstone.upserted"
	EventCornerstoneRemoved  WorkEventType = "cornerstone.removed"
	EventCornerstoneRestored WorkEventType = "cornerstone.restored"
	EventCornerstoneGC       WorkEventType = "cornerstone.gc.requested"
	EventConclusionUpserted  WorkEventType = "conclusion.upserted"
	EventArtifactLinked      WorkEventType = "artifact.linked"
	EventWorkArchived        WorkEventType = "work.archived"
	EventWorkRestored        WorkEventType = "work.restored"
	EventWorkDeleted         WorkEventType = "work.deleted"
	EventBlockActionReserved WorkEventType = "block.action.reserved"
	EventBlockActionChanged  WorkEventType = "block.action.changed"
)

// ViewEventType identifies a transport projection update.
type ViewEventType string

const (
	// ViewSnapshot replaces the frontend projection in full.
	ViewSnapshot ViewEventType = "snapshot"
	// ViewDelta applies only when BaseRevision matches the local revision.
	ViewDelta ViewEventType = "delta"
	// ViewAttention announces a user-visible condition without changing state.
	ViewAttention ViewEventType = "attention"
	// ViewRemoved removes the projection from the current frontend view.
	ViewRemoved ViewEventType = "removed"
)

// ObjectKind identifies the object affected by a WorkViewEvent.
type ObjectKind string

const (
	ObjectWork        ObjectKind = "work"
	ObjectBlock       ObjectKind = "block"
	ObjectRun         ObjectKind = "run"
	ObjectStage       ObjectKind = "stage"
	ObjectTask        ObjectKind = "task"
	ObjectAttempt     ObjectKind = "attempt"
	ObjectCornerstone ObjectKind = "cornerstone"
	ObjectConclusion  ObjectKind = "conclusion"
	ObjectArtifact    ObjectKind = "artifact"
)

// ObjectContext locates the affected object without relying on the active tab,
// face, page, or currently selected Work.
type ObjectContext struct {
	Kind     ObjectKind `json:"kind"`
	ID       string     `json:"id"`
	ParentID string     `json:"parentID,omitempty"`
}

// ViewResyncReason identifies the narrowly-scoped transport recovery that may
// replace a frontend projection without a persisted revision change.
type ViewResyncReason string

const (
	// ViewResyncOverflow follows a WorkViewBroadcaster overflow signal. The
	// payload must be a fresh authoritative GetWork snapshot.
	ViewResyncOverflow ViewResyncReason = "overflow"
	// ViewResyncRetry follows an explicit frontend re-subscribe. Desktop first
	// installs the new Watch, then returns a fresh authoritative GetWork event.
	ViewResyncRetry ViewResyncReason = "retry"
)

// ViewResync marks an authoritative transport resynchronization. Generation
// orders overflow recovery attempts independently of the persisted Work
// revision because I/O-backed assessment may change at the same revision.
type ViewResync struct {
	Reason        ViewResyncReason `json:"reason"`
	Authoritative bool             `json:"authoritative"`
	Generation    uint64           `json:"generation"`
}

// ViewRecoveryIntent is sent by the frontend only for an explicit retry after
// installing a fresh Watch generation. Desktop returns a validated ViewResync.
type ViewRecoveryIntent struct {
	Reason     ViewResyncReason `json:"reason"`
	Generation uint64           `json:"generation"`
}

// ResyncEventID returns the stable event identity for one backend-issued
// authoritative resynchronization.
func ResyncEventID(workID string, revision int64, reason ViewResyncReason, generation uint64) string {
	return fmt.Sprintf("wv-resync-%s-rev-%d-%s-%d", workID, revision, reason, generation)
}

// OverflowResyncEventID returns the stable event identity shared by backend
// validation and Desktop recovery emission.
func OverflowResyncEventID(workID string, revision int64, generation uint64) string {
	return ResyncEventID(workID, revision, ViewResyncOverflow, generation)
}

// WorkViewEvent is a transport projection event emitted only through ViewSink.
// It is never appended to the persisted Work event log.
//
// A delta is applicable only when BaseRevision equals the frontend's current
// revision. A mismatch means a gap or reordering and requires a new snapshot.
type WorkViewEvent struct {
	SchemaVersion int             `json:"schemaVersion"`
	Type          ViewEventType   `json:"type"`
	WorkID        string          `json:"workID"`
	EventID       string          `json:"eventID"`
	Revision      int64           `json:"revision"`
	BaseRevision  int64           `json:"baseRevision"`
	RequestID     string          `json:"requestID"`
	Object        ObjectContext   `json:"object"`
	Resync        *ViewResync     `json:"resync,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"createdAt"`
}
