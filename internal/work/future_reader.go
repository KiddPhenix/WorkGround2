package work

import (
	"encoding/json"
	"fmt"
	"time"
)

// FutureWorkEnvelope is a read-only view of a Work whose schema version
// exceeds the binary's understanding. It exposes only safe metadata,
// fallback blocks (extracted from blocks[].fallback), and raw export.
// No mutable operations are permitted.
type FutureWorkEnvelope struct {
	SchemaVersion int              `json:"schemaVersion"`
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	ArchiveState  WorkArchiveState `json:"archiveState"`
	CreatedAt     time.Time        `json:"createdAt"`
	UpdatedAt     time.Time        `json:"updatedAt"`

	// FallbackBlocks are extracted from blocks[].fallback. Each block's
	// fallback is lifted independently; a single broken fallback is skipped.
	FallbackBlocks []BlockFallback `json:"fallbackBlocks"`
	// Raw is the complete original JSON bytes for export.
	Raw json.RawMessage `json:"-"`
}

// FutureWorkEnvelopeError is returned when an operation attempts to mutate a
// future-schema Work or WorkRecord.
type FutureWorkEnvelopeError struct {
	WorkID       string
	SchemaVer    int
	BinaryMaxVer int
	Operation    string
}

func (e *FutureWorkEnvelopeError) Error() string {
	return fmt.Sprintf("work: %s cannot process %s at schema v%d (binary max v%d); read-only access only",
		e.Operation, e.WorkID, e.SchemaVer, e.BinaryMaxVer)
}

// ReadFutureWorkEnvelope extracts a read-only FutureWorkEnvelope from raw
// Work JSON. It parses blocks[].fallback (the real structure) and tolerates
// a top-level fallbackBlocks for compatibility. A single broken block fallback
// is skipped; remaining valid fallbacks are preserved.
func ReadFutureWorkEnvelope(raw json.RawMessage) (*FutureWorkEnvelope, error) {
	var header struct {
		SchemaVersion int              `json:"schemaVersion"`
		ID            string           `json:"id"`
		Name          string           `json:"name"`
		ArchiveState  WorkArchiveState `json:"archiveState"`
		CreatedAt     time.Time        `json:"createdAt"`
		UpdatedAt     time.Time        `json:"updatedAt"`
		Blocks        json.RawMessage  `json:"blocks"`
		// Compat: also accept top-level fallbackBlocks if present.
		FallbackBlocksCompat json.RawMessage `json:"fallbackBlocks"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("work: ReadFutureWorkEnvelope: %w", err)
	}
	if header.SchemaVersion <= SchemaVersionV2 {
		return nil, fmt.Errorf("work: ReadFutureWorkEnvelope: schema v%d is within binary capability (max %d); use the full reader",
			header.SchemaVersion, SchemaVersionV2)
	}
	if header.ID == "" {
		return nil, fmt.Errorf("work: ReadFutureWorkEnvelope: missing id")
	}

	env := &FutureWorkEnvelope{
		SchemaVersion: header.SchemaVersion,
		ID:            header.ID,
		Name:          header.Name,
		ArchiveState:  header.ArchiveState,
		CreatedAt:     header.CreatedAt,
		UpdatedAt:     header.UpdatedAt,
		Raw:           append(json.RawMessage(nil), raw...),
	}

	// Primary: extract fallbacks from blocks[].fallback. Each block is parsed
	// independently so a broken fallback in one block doesn't lose the rest.
	if len(header.Blocks) > 0 {
		var rawBlocks []json.RawMessage
		if err := json.Unmarshal(header.Blocks, &rawBlocks); err == nil {
			for _, rb := range rawBlocks {
				var b struct {
					Fallback *BlockFallback `json:"fallback"`
				}
				if err := json.Unmarshal(rb, &b); err != nil {
					continue // skip broken block entirely
				}
				if b.Fallback != nil {
					env.FallbackBlocks = append(env.FallbackBlocks, *b.Fallback)
				}
			}
		}
		// On unmarshal failure degrade to empty — raw is still preserved.
	}

	// Compat fallback: if no blocks fallback found, try top-level.
	if len(env.FallbackBlocks) == 0 && len(header.FallbackBlocksCompat) > 0 {
		json.Unmarshal(header.FallbackBlocksCompat, &env.FallbackBlocks)
		// Degrade silently on failure.
	}

	return env, nil
}

// RejectWrite returns a FutureWorkEnvelopeError for any attempted mutation.
func (e *FutureWorkEnvelope) RejectWrite(operation string) error {
	return &FutureWorkEnvelopeError{
		WorkID:       e.ID,
		SchemaVer:    e.SchemaVersion,
		BinaryMaxVer: SchemaVersionV2,
		Operation:    operation,
	}
}

// ── Future WorkRecord reader ───────────────────────────────────────────────

// FutureWorkRecordEnvelope is a read-only view of a WorkRecord whose
// archiveSchemaVersion or snapshot schema version exceeds the binary's
// understanding. Only metadata, fallback blocks, and raw export are exposed.
type FutureWorkRecordEnvelope struct {
	ArchiveSchemaVersion  int             `json:"archiveSchemaVersion"`
	SnapshotSchemaVersion int             `json:"snapshotSchemaVersion"`
	WorkID                string          `json:"workId"`
	ArchivedAt            time.Time       `json:"archivedAt"`
	FallbackBlocks        []BlockFallback `json:"fallbackBlocks"`
	Raw                   json.RawMessage `json:"-"`
}

// effectiveFutureVersion returns the schema version that makes this record
// future. If the archive version is future it wins; otherwise the snapshot's.
func (e *FutureWorkRecordEnvelope) effectiveFutureVersion() int {
	if e.ArchiveSchemaVersion > SchemaVersionV2 {
		return e.ArchiveSchemaVersion
	}
	if e.SnapshotSchemaVersion > SchemaVersionV2 {
		return e.SnapshotSchemaVersion
	}
	return e.ArchiveSchemaVersion // fallback — shouldn't happen if reader worked
}

// ReadFutureWorkRecordEnvelope extracts a read-only view from raw WorkRecord
// JSON. It checks both archiveSchemaVersion and the embedded snapshot's
// schemaVersion. Returns an error only when both are within binary capability.
func ReadFutureWorkRecordEnvelope(raw json.RawMessage) (*FutureWorkRecordEnvelope, error) {
	var header struct {
		ArchiveSchemaVersion int             `json:"archiveSchemaVersion"`
		WorkID               string          `json:"workId"`
		ArchivedAt           time.Time       `json:"archivedAt"`
		FallbackBlocks       json.RawMessage `json:"fallbackBlocks"`
		Snapshot             json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, fmt.Errorf("work: ReadFutureWorkRecordEnvelope: %w", err)
	}

	// Determine effective future-ness.
	var snapSchema int
	if len(header.Snapshot) > 0 {
		var snap struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		if err := json.Unmarshal(header.Snapshot, &snap); err != nil {
			return nil, fmt.Errorf("work: ReadFutureWorkRecordEnvelope: snapshot parse: %w", err)
		}
		snapSchema = snap.SchemaVersion
	}

	archiveFuture := header.ArchiveSchemaVersion > SchemaVersionV2
	snapFuture := snapSchema > SchemaVersionV2
	if !archiveFuture && !snapFuture {
		return nil, fmt.Errorf("work: ReadFutureWorkRecordEnvelope: archive v%d / snapshot v%d is within binary capability (max %d); use the full reader",
			header.ArchiveSchemaVersion, snapSchema, SchemaVersionV2)
	}
	if header.WorkID == "" {
		return nil, fmt.Errorf("work: ReadFutureWorkRecordEnvelope: missing workId")
	}

	env := &FutureWorkRecordEnvelope{
		ArchiveSchemaVersion:  header.ArchiveSchemaVersion,
		SnapshotSchemaVersion: snapSchema,
		WorkID:                header.WorkID,
		ArchivedAt:            header.ArchivedAt,
		Raw:                   append(json.RawMessage(nil), raw...),
	}
	if header.FallbackBlocks != nil {
		if err := json.Unmarshal(header.FallbackBlocks, &env.FallbackBlocks); err != nil {
			env.FallbackBlocks = nil // degrade
		}
	}
	return env, nil
}

// RejectWrite returns a FutureWorkEnvelopeError using the effective future
// version — i.e., the snapshot's schema when that is what makes the record
// future, not the (potentially V1) archive schema.
func (e *FutureWorkRecordEnvelope) RejectWrite(operation string) error {
	return &FutureWorkEnvelopeError{
		WorkID:       e.WorkID,
		SchemaVer:    e.effectiveFutureVersion(),
		BinaryMaxVer: SchemaVersionV2,
		Operation:    operation,
	}
}
