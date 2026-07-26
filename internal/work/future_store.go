package work

import (
	"encoding/json"
	"fmt"
)

// ErrFutureSchemaMutation is the unified typed error returned when any
// mutation is attempted on a future-schema Work or WorkRecord. All mutation
// entry points must return (or wrap) this error for future data.
type ErrFutureSchemaMutation struct {
	WorkID    string
	SchemaVer int
	Operation string
}

func (e *ErrFutureSchemaMutation) Error() string {
	return fmt.Sprintf("work: cannot %s future-schema %s (v%d > binary max v%d); read-only access only",
		e.Operation, e.WorkID, e.SchemaVer, SchemaVersionV2)
}

// FutureAwareReadResult is a tagged union returned by future-aware reads.
type FutureAwareReadResult struct {
	Work         *Work
	Record       *WorkRecord
	FutureWork   *FutureWorkEnvelope
	FutureRecord *FutureWorkRecordEnvelope
}

// IsFuture reports whether this result is a read-only future view.
func (r *FutureAwareReadResult) IsFuture() bool {
	return r.FutureWork != nil || r.FutureRecord != nil
}

// RejectMutation returns ErrFutureSchemaMutation if this result is future.
// All mutation paths must call this before accepting writes.
func (r *FutureAwareReadResult) RejectMutation(op string) error {
	if r.FutureWork != nil {
		return &ErrFutureSchemaMutation{
			WorkID: r.FutureWork.ID, SchemaVer: r.FutureWork.SchemaVersion, Operation: op,
		}
	}
	if r.FutureRecord != nil {
		ver := r.FutureRecord.ArchiveSchemaVersion
		if r.FutureRecord.SnapshotSchemaVersion > ver {
			ver = r.FutureRecord.SnapshotSchemaVersion
		}
		return &ErrFutureSchemaMutation{
			WorkID: r.FutureRecord.WorkID, SchemaVer: ver, Operation: op,
		}
	}
	return nil
}

// ReadFutureAwareWorkFromRaw reads raw Work JSON bytes, checks schema version,
// and returns either a FutureWorkEnvelope or the parsed Work.
func ReadFutureAwareWorkFromRaw(raw json.RawMessage) (*FutureAwareReadResult, error) {
	var header struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("work: ReadFutureAwareWorkFromRaw header: %w", err)
	}
	if IsFutureSchemaV2(header.SchemaVersion) {
		env, err := ReadFutureWorkEnvelope(raw)
		if err != nil {
			return nil, fmt.Errorf("work: ReadFutureAwareWorkFromRaw envelope: %w", err)
		}
		return &FutureAwareReadResult{FutureWork: env}, nil
	}
	var w Work
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("work: ReadFutureAwareWorkFromRaw unmarshal: %w", err)
	}
	return &FutureAwareReadResult{Work: &w}, nil
}

// recordSchemaIsFuture reads minimal headers from raw WorkRecord JSON and
// returns true if either archiveSchemaVersion or snapshot.schemaVersion
// exceeds SchemaVersionV2.
func recordSchemaIsFuture(raw json.RawMessage) bool {
	var h struct {
		ArchiveSchemaVersion *int            `json:"archiveSchemaVersion"`
		Snapshot             json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return false // unreadable — let caller decide.
	}
	if h.ArchiveSchemaVersion != nil && *h.ArchiveSchemaVersion > SchemaVersionV2 {
		return true
	}
	if len(h.Snapshot) > 0 {
		var snap struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		if json.Unmarshal(h.Snapshot, &snap) == nil && snap.SchemaVersion > SchemaVersionV2 {
			return true
		}
	}
	return false
}

// ReadFutureAwareRecordFromRaw reads raw WorkRecord JSON bytes, checks
// archiveSchemaVersion and snapshot schema, returns either a
// FutureWorkRecordEnvelope or the parsed WorkRecord. Errors from
// ReadFutureWorkRecordEnvelope (missing workId, bad snapshot, etc.) are
// propagated for future records; only confirmed-known records are unmarshalled.
func ReadFutureAwareRecordFromRaw(raw json.RawMessage) (*FutureAwareReadResult, error) {
	if recordSchemaIsFuture(raw) {
		env, err := ReadFutureWorkRecordEnvelope(raw)
		if err != nil {
			return nil, fmt.Errorf("work: ReadFutureAwareRecordFromRaw future envelope: %w", err)
		}
		return &FutureAwareReadResult{FutureRecord: env}, nil
	}
	// Confirmed known — parse as WorkRecord.
	var record WorkRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("work: ReadFutureAwareRecordFromRaw: %w", err)
	}
	return &FutureAwareReadResult{Record: &record}, nil
}
