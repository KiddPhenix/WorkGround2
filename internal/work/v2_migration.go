package work

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MigrateV1FileToV2 reads a V1 work file at srcPath, validates the migration
// decision, applies transform to produce V2 content, and writes the result
// atomically to dstPath. The source file is never modified.
//
// Retry safety: if dstPath already exists, its content is compared with the
// expected result. If identical, the call succeeds (idempotent). If different,
// an error is returned (conflict). Future/invalid/current schema versions are
// rejected explicitly with no writes.
//
// transform receives the raw source bytes and must return V2 JSON bytes where
// the top-level schemaVersion equals SchemaVersionV2.
func MigrateV1FileToV2(srcPath, dstPath, requestID string, transform func([]byte) ([]byte, error)) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fmt.Errorf("work: MigrateV1FileToV2: requestID must be non-empty")
	}
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("work: MigrateV1FileToV2: srcPath and dstPath must be non-empty")
	}
	if transform == nil {
		return fmt.Errorf("work: MigrateV1FileToV2: transform must be non-nil")
	}

	// Read source.
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("work: MigrateV1FileToV2 read source %s: %w", srcPath, err)
	}

	// Parse schema version from header.
	var header struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(src, &header); err != nil {
		return fmt.Errorf("work: MigrateV1FileToV2 parse source header: %w", err)
	}

	dec := DecideV2Migration(header.SchemaVersion)
	switch dec {
	case MigrateV1ToV2:
		// Proceed.
	case MigrateCurrent:
		return fmt.Errorf("work: MigrateV1FileToV2: source is already V2 (current)")
	case MigrateFutureReadOnly:
		return fmt.Errorf("work: MigrateV1FileToV2: source schema v%d is future, read-only; no migration", header.SchemaVersion)
	case MigrateInvalid:
		return fmt.Errorf("work: MigrateV1FileToV2: source schema v%d is invalid, cannot migrate", header.SchemaVersion)
	default:
		return fmt.Errorf("work: MigrateV1FileToV2: unknown migration decision for v%d", header.SchemaVersion)
	}

	// Apply transform.
	result, err := transform(src)
	if err != nil {
		return fmt.Errorf("work: MigrateV1FileToV2 transform: %w", err)
	}

	// Validate output schema.
	var outHeader struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(result, &outHeader); err != nil {
		return fmt.Errorf("work: MigrateV1FileToV2: transform output is not valid JSON: %w", err)
	}
	if outHeader.SchemaVersion != SchemaVersionV2 {
		return fmt.Errorf("work: MigrateV1FileToV2: transform output schemaVersion=%d, want %d", outHeader.SchemaVersion, SchemaVersionV2)
	}

	// Check existing dst.
	if existing, err := os.ReadFile(dstPath); err == nil {
		if bytes.Equal(existing, result) {
			return nil // idempotent: same content already present.
		}
		return fmt.Errorf("work: MigrateV1FileToV2: dst %s exists with different content; conflict", dstPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("work: MigrateV1FileToV2 read destination %s: %w", dstPath, err)
	}

	// Atomic write: temp file in same dir, then rename.
	dstDir := filepath.Dir(dstPath)
	tmp, err := os.CreateTemp(dstDir, ".migrate-v2-*")
	if err != nil {
		return fmt.Errorf("work: MigrateV1FileToV2 create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { os.Remove(tmpPath) }

	if _, err := tmp.Write(result); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("work: MigrateV1FileToV2 write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("work: MigrateV1FileToV2 sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("work: MigrateV1FileToV2 close temp: %w", err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		cleanup()
		return fmt.Errorf("work: MigrateV1FileToV2 rename temp→dst: %w", err)
	}
	return nil
}
