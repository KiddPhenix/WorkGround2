package work

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ViewFutureSchemaError marks a transport event that this binary must preserve
// as raw, read-only data. The caller must not merge it or issue writes from it.
type ViewFutureSchemaError struct {
	Got     int
	Current int
	EventID string
}

func (e *ViewFutureSchemaError) Error() string {
	return fmt.Sprintf("work: WorkViewEvent schema version %d exceeds current max %d on event %q; read-only access is required",
		e.Got, e.Current, e.EventID)
}

// ViewParseResult preserves the original bytes for future-schema read-only
// display/export. Event is nil when FutureError is non-nil.
type ViewParseResult struct {
	Event       *WorkViewEvent
	Raw         json.RawMessage
	FutureError *ViewFutureSchemaError
}

// ParseWorkViewEvent decodes a supported transport event. Future schema data is
// preserved without partial interpretation so an old client cannot overwrite
// fields it does not understand.
func ParseWorkViewEvent(raw json.RawMessage) (*ViewParseResult, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("work: decode WorkViewEvent: invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var header struct {
		SchemaVersion *int   `json:"schemaVersion"`
		EventID       string `json:"eventID"`
	}
	if err := decoder.Decode(&header); err != nil {
		return nil, fmt.Errorf("work: decode WorkViewEvent header: %w", err)
	}
	if header.SchemaVersion == nil || *header.SchemaVersion < 1 {
		return nil, fmt.Errorf("work: WorkViewEvent schemaVersion must be a positive integer")
	}

	copyRaw := append(json.RawMessage(nil), raw...)
	if *header.SchemaVersion > WorkViewSchemaVersion {
		return &ViewParseResult{
			Raw: copyRaw,
			FutureError: &ViewFutureSchemaError{
				Got:     *header.SchemaVersion,
				Current: WorkViewSchemaVersion,
				EventID: header.EventID,
			},
		}, nil
	}

	var event WorkViewEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, fmt.Errorf("work: decode WorkViewEvent: %w", err)
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	return &ViewParseResult{Event: &event, Raw: copyRaw}, nil
}

// Validate checks the context and revision fields needed for safe routing and
// idempotent frontend merging.
func (e WorkViewEvent) Validate() error {
	if e.SchemaVersion < 1 || e.SchemaVersion > WorkViewSchemaVersion {
		return fmt.Errorf("work: unsupported WorkViewEvent schemaVersion %d", e.SchemaVersion)
	}
	if !e.Type.Valid() {
		return fmt.Errorf("work: invalid WorkViewEvent type %q", e.Type)
	}
	if e.WorkID == "" || e.EventID == "" || e.RequestID == "" {
		return fmt.Errorf("work: WorkViewEvent requires workID, eventID, and requestID")
	}
	if e.Revision < 0 || e.BaseRevision < 0 {
		return fmt.Errorf("work: WorkViewEvent revisions must be non-negative")
	}
	if e.Type == ViewDelta && e.BaseRevision >= e.Revision {
		return fmt.Errorf("work: delta baseRevision %d must be lower than revision %d", e.BaseRevision, e.Revision)
	}
	if !e.Object.Kind.Valid() || e.Object.ID == "" {
		return fmt.Errorf("work: WorkViewEvent requires valid object kind and id")
	}
	if e.Resync != nil {
		const maxSafeJSONInteger = uint64(1<<53 - 1)
		if e.Type != ViewSnapshot || e.Object.Kind != ObjectWork || e.Object.ID != e.WorkID {
			return fmt.Errorf("work: authoritative resync requires a matching Work snapshot")
		}
		if (e.Resync.Reason != ViewResyncOverflow && e.Resync.Reason != ViewResyncRetry && e.Resync.Reason != ViewResyncHydrate) || !e.Resync.Authoritative {
			return fmt.Errorf("work: unsupported or non-authoritative WorkView resync")
		}
		if e.Resync.Generation == 0 || e.Resync.Generation > maxSafeJSONInteger {
			return fmt.Errorf("work: WorkView resync generation must be a positive JSON-safe integer")
		}
		if want := ResyncEventID(e.WorkID, e.Revision, e.Resync.Reason, e.Resync.Generation); e.EventID != want {
			return fmt.Errorf("work: WorkView resync eventID %q does not match %q", e.EventID, want)
		}
	}
	if e.CreatedAt.IsZero() {
		return fmt.Errorf("work: WorkViewEvent requires createdAt")
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("work: WorkViewEvent requires payload (use null when empty)")
	}
	return nil
}

// Valid reports whether t is a V1 transport event type.
func (t ViewEventType) Valid() bool {
	switch t {
	case ViewSnapshot, ViewDelta, ViewAttention, ViewRemoved:
		return true
	default:
		return false
	}
}

// Valid reports whether k is a V1 object context kind.
func (k ObjectKind) Valid() bool {
	switch k {
	case ObjectWork, ObjectBlock, ObjectRun, ObjectStage, ObjectTask,
		ObjectAttempt, ObjectCornerstone, ObjectConclusion, ObjectArtifact:
		return true
	default:
		return false
	}
}

// RejectWrite returns a future-schema error when this result is read-only.
func (r *ViewParseResult) RejectWrite() error {
	if r == nil {
		return fmt.Errorf("work: nil WorkViewEvent parse result")
	}
	if r.FutureError != nil {
		return r.FutureError
	}
	return nil
}

func (r *ViewParseResult) IsType(t ViewEventType) bool {
	return r != nil && r.Event != nil && r.Event.Type == t
}

// DeltaAppliesTo reports whether this is a contiguous delta for current.
// False means the frontend must request a full snapshot.
func (r *ViewParseResult) DeltaAppliesTo(current int64) bool {
	return r != nil && r.Event != nil && r.Event.Type == ViewDelta &&
		r.Event.BaseRevision == current
}
