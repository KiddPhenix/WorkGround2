package work

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBlockFallbackArchiveRoundTripStructuredEnvelope(t *testing.T) {
	store := newTestStore(t)
	archivedAt := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	workID := "work-block-fallback-roundtrip"
	fallback := BlockFallback{
		Summary: "Renderer unavailable; archived summary remains readable",
		Data: json.RawMessage(`{
			"version":1,
			"display":{"title":"Archived result","lines":["first","second"]},
			"error":{"code":"renderer_unavailable","retryable":false}
		}`),
	}
	record := &WorkRecord{
		ArchiveSchemaVersion: SchemaVersion,
		WorkID:               workID,
		Snapshot: Work{
			SchemaVersion: SchemaVersion,
			ID:            workID,
			Name:          "Fallback archive",
			State:         WorkCompleted,
			ArchiveState:  ArchiveArchived,
			Blocks: []BlockInstance{{
				ID:            "block-future",
				Kind:          "future_summary",
				SchemaVersion: SchemaVersion + 1,
				Revision:      4,
				Status:        BlockFailed,
				Data:          json.RawMessage(`{"futureField":{"version":2}}`),
				Fallback:      fallback,
				CreatedAt:     archivedAt.Add(-time.Hour),
				UpdatedAt:     archivedAt,
			}},
			CreatedWith: RuntimeFingerprint{RendererSetVersion: 37},
			CreatedAt:   archivedAt.Add(-time.Hour),
			UpdatedAt:   archivedAt,
			ArchivedAt:  &archivedAt,
		},
		RendererSetVersion: 37,
		FallbackBlocks:     []BlockFallback{fallback},
		ArchivedAt:         archivedAt,
	}

	if err := store.WriteArchive(workID, record); err != nil {
		t.Fatalf("WriteArchive: %v", err)
	}
	// Mutating caller-owned buffers after the atomic write must not alter the
	// immutable archive loaded from disk.
	record.FallbackBlocks[0].Data[0] = 'X'
	record.Snapshot.Blocks[0].Fallback.Data[1] = 'Y'

	loaded, err := store.LoadArchive(workID)
	if err != nil {
		t.Fatalf("LoadArchive: %v", err)
	}
	if len(loaded.FallbackBlocks) != 1 || len(loaded.Snapshot.Blocks) != 1 {
		t.Fatalf("fallback archive cardinality = fallbacks:%d blocks:%d", len(loaded.FallbackBlocks), len(loaded.Snapshot.Blocks))
	}
	if !IsFutureSchema(loaded.Snapshot.Blocks[0].SchemaVersion) {
		t.Fatalf("block schemaVersion = %d, want future schema retained for read-only fallback", loaded.Snapshot.Blocks[0].SchemaVersion)
	}
	assertBlockFallbackEnvelope(t, loaded.FallbackBlocks[0])
	assertBlockFallbackEnvelope(t, loaded.Snapshot.Blocks[0].Fallback)

	loaded.FallbackBlocks[0].Data[0] = 'Z'
	reloaded, err := store.LoadArchive(workID)
	if err != nil {
		t.Fatalf("second LoadArchive: %v", err)
	}
	assertBlockFallbackEnvelope(t, reloaded.FallbackBlocks[0])
}

func TestBlockFallbackRefreshValidationBoundsAndInjection(t *testing.T) {
	block := &BlockInstance{Kind: "markdown", SchemaVersion: SchemaVersion}
	result := BlockRefreshResult{
		Kind:          block.Kind,
		SchemaVersion: block.SchemaVersion,
		Data:          json.RawMessage(`{"content":"refreshed"}`),
		Status:        BlockReady,
		Fallback: BlockFallback{
			Summary: "readable fallback",
			Data:    blockFallbackAtSize(t, BlockInlineMaxBytes),
		},
	}
	if err := validateRefreshResult(block, result); err != nil {
		t.Fatalf("valid fallback at exact inline limit rejected: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*BlockRefreshResult)
		wantErr string
	}{
		{
			name: "invalid JSON",
			mutate: func(value *BlockRefreshResult) {
				value.Fallback.Data = json.RawMessage(`{"broken"`)
			},
			wantErr: "fallback data is invalid JSON",
		},
		{
			name: "nested UI injection",
			mutate: func(value *BlockRefreshResult) {
				value.Fallback.Data = json.RawMessage(`{"display":{"renderer":{"component":"Injected"}}}`)
			},
			wantErr: "forbidden UI/execution field fallback.data.display.renderer",
		},
		{
			name: "data exceeds inline limit",
			mutate: func(value *BlockRefreshResult) {
				value.Fallback.Data = bytes.Repeat([]byte{'x'}, BlockInlineMaxBytes+1)
			},
			wantErr: "fallback exceeds the safe inline limit",
		},
		{
			name: "summary exceeds inline limit",
			mutate: func(value *BlockRefreshResult) {
				value.Fallback.Summary = strings.Repeat("s", 4097)
			},
			wantErr: "fallback exceeds the safe inline limit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := result
			candidate.Fallback = cloneBlockFallback(result.Fallback)
			tc.mutate(&candidate)
			err := validateRefreshResult(block, candidate)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateRefreshResult error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestBlockFallbackCloneOwnsRawData(t *testing.T) {
	original := BlockFallback{
		Summary: "stable summary",
		Data:    json.RawMessage(`{"error":{"code":"source_unavailable","retryable":true}}`),
	}
	cloned := cloneBlockFallback(original)
	cloned.Data[0] = 'X'
	if !json.Valid(original.Data) {
		t.Fatalf("mutating cloned fallback corrupted original: %s", original.Data)
	}
	if bytes.Equal(cloned.Data, original.Data) {
		t.Fatal("cloneBlockFallback returned aliased raw data")
	}
}

func assertBlockFallbackEnvelope(t *testing.T, fallback BlockFallback) {
	t.Helper()
	if fallback.Summary != "Renderer unavailable; archived summary remains readable" {
		t.Fatalf("fallback summary = %q", fallback.Summary)
	}
	var envelope struct {
		Version int `json:"version"`
		Display struct {
			Title string   `json:"title"`
			Lines []string `json:"lines"`
		} `json:"display"`
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(fallback.Data, &envelope); err != nil {
		t.Fatalf("fallback data is not structured JSON: %v", err)
	}
	if envelope.Version != 1 || envelope.Display.Title != "Archived result" || len(envelope.Display.Lines) != 2 {
		t.Fatalf("fallback display envelope = %+v", envelope)
	}
	if envelope.Error.Code != "renderer_unavailable" || envelope.Error.Retryable {
		t.Fatalf("fallback error metadata = %+v", envelope.Error)
	}
}

func blockFallbackAtSize(t *testing.T, size int) json.RawMessage {
	t.Helper()
	prefix := []byte(`{"payload":"`)
	suffix := []byte(`"}`)
	if size < len(prefix)+len(suffix) {
		t.Fatalf("fallback size %d is too small", size)
	}
	value := make([]byte, 0, size)
	value = append(value, prefix...)
	value = append(value, bytes.Repeat([]byte{'x'}, size-len(prefix)-len(suffix))...)
	value = append(value, suffix...)
	if len(value) != size || !json.Valid(value) {
		t.Fatalf("fallback boundary fixture len=%d valid=%v", len(value), json.Valid(value))
	}
	return value
}
