package work

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestBlockSchemaRegistryMigratesFileListV1ToV2(t *testing.T) {
	registry := NewBlockSchemaRegistry()
	source := json.RawMessage(`{"files":[{"path":"a.txt","status":"modified","desc":"legacy"}]}`)
	before := append([]byte(nil), source...)

	migrated, path, err := registry.Migrate("file_list", 1, 2, source)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !bytes.Equal(source, before) {
		t.Fatal("migration mutated source bytes")
	}
	if len(path) != 2 || path[0] != 1 || path[1] != 2 {
		t.Fatalf("path = %v, want [1 2]", path)
	}
	if err := registry.Validate("file_list", 2, migrated); err != nil {
		t.Fatalf("migrated v2 data rejected: %v", err)
	}
	if bytes.Contains(migrated, []byte(`"desc"`)) ||
		!bytes.Contains(migrated, []byte(`"description":"legacy"`)) {
		t.Fatalf("migration output = %s", migrated)
	}
	if err := registry.Validate("file_list", 1, migrated); err == nil {
		t.Fatal("v2 data was accepted as v1")
	}
}

func TestBlockSchemaRegistryMissingMigrationFailsExplicitly(t *testing.T) {
	registry := NewBlockSchemaRegistry()
	if err := registry.Register("markdown", 2, validateCoreBlockSchema("markdown", 2)); err != nil {
		t.Fatal(err)
	}
	_, path, err := registry.Migrate("markdown", 1, 2, json.RawMessage(`{"content":"x"}`))
	if err == nil || len(path) != 1 {
		t.Fatalf("missing migration err=%v path=%v", err, path)
	}
	var future *ErrFutureBlockSchema
	if _, _, futureErr := registry.Migrate("markdown", 1, 3, json.RawMessage(`{"content":"x"}`)); futureErr == nil {
		t.Fatal("future target unexpectedly migrated")
	} else if !errors.As(futureErr, &future) || future.Got != 3 {
		t.Fatalf("future schema = %+v", future)
	}
}

func TestMigrateRerunBlocksReinitializesEmptyTargetSchema(t *testing.T) {
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	sourceSpecs := []BlockSpec{{
		ID: "files", Kind: "file_list", SchemaVersion: 1, Label: "Old files",
		Placement: BlockPlacement{Slot: "primary"},
	}}
	targetSpecs := []BlockSpec{{
		ID: "files", Kind: "file_list", SchemaVersion: 2, Label: "New files",
		DefaultData: json.RawMessage(`{"files":[]}`),
		Placement:   BlockPlacement{Slot: "result", Span: 6},
	}}
	sourceBlocks, _ := buildInitialBlocks(sourceSpecs, now.Add(-time.Hour))

	blocks, placements, issues, warnings := migrateRerunBlocks(
		sourceBlocks, sourceSpecs, targetSpecs, NewBlockSchemaRegistry(), now,
	)
	if len(issues) != 0 || len(warnings) != 1 || len(blocks) != 1 {
		t.Fatalf("migration result blocks=%+v issues=%+v warnings=%+v", blocks, issues, warnings)
	}
	if blocks[0].SchemaVersion != 2 || blocks[0].Status != BlockEmpty ||
		!bytes.Equal(blocks[0].Data, targetSpecs[0].DefaultData) {
		t.Fatalf("empty target block = %+v", blocks[0])
	}
	if len(placements) != 1 || placements[0].Slot != "result" || placements[0].Span != 6 {
		t.Fatalf("target placements = %+v", placements)
	}
}
