package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── UpsertBlock basics ──────────────────────────────────────────────────────

func TestBlockUpsertBasic(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-upsert-basic")

	input := BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         2,
		Title:            "Updated Notes",
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"hello world"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2,
		RequestID:        "block-upsert-basic-req",
	}

	view, err := f.svc.UpsertBlock(context.Background(), input)
	if err != nil {
		t.Fatalf("UpsertBlock: %v", err)
	}
	if view.Revision != 3 {
		t.Fatalf("revision = %d, want 3", view.Revision)
	}

	var updated *BlockInstance
	for i := range view.Work.Blocks {
		if view.Work.Blocks[i].ID == "bp-blank-notes" {
			updated = &view.Work.Blocks[i]
			break
		}
	}
	if updated == nil {
		t.Fatal("block not found after upsert")
	}
	if updated.Revision != 2 {
		t.Fatalf("block revision = %d, want 2", updated.Revision)
	}
	if updated.Title != "Updated Notes" {
		t.Fatalf("block title = %q, want %q", updated.Title, "Updated Notes")
	}
	if updated.Status != BlockReady {
		t.Fatalf("block status = %s, want ready", updated.Status)
	}
}

// ── UpsertBlock idempotent ──────────────────────────────────────────────────

func TestBlockUpsertIdempotent(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-idem-create")

	input := BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         2,
		Title:            "Once",
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"test"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2,
		RequestID:        "block-idem",
	}

	first, err := f.svc.UpsertBlock(context.Background(), input)
	if err != nil {
		t.Fatalf("first UpsertBlock: %v", err)
	}
	if first.Revision != 3 {
		t.Fatalf("first revision = %d, want 3", first.Revision)
	}

	// Restart and retry — same requestID, same content.
	restarted := f.restart(t)
	second, err := restarted.UpsertBlock(context.Background(), input)
	if err != nil {
		t.Fatalf("idempotent UpsertBlock after restart: %v", err)
	}
	if second.Revision != 3 {
		t.Fatalf("idempotent revision = %d, want 3", second.Revision)
	}
	for i := range second.Work.Blocks {
		if second.Work.Blocks[i].ID == "bp-blank-notes" && second.Work.Blocks[i].Title != "Once" {
			t.Fatal("block title changed on idempotent retry")
		}
	}

	// Same requestID, different content → conflict.
	changed := input
	changed.Title = "Different"
	conflict, err := restarted.UpsertBlock(context.Background(), changed)
	if err == nil {
		t.Fatal("expected conflict error for different content with same requestID")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("conflict error = %v, want event conflict", err)
	}
	if conflict == nil || conflict.Revision != 3 {
		t.Fatalf("conflict view = %+v, want revision 3", conflict)
	}
}

// ── UpsertBlock revision merge ──────────────────────────────────────────────

func TestBlockUpsertRevisionMerge(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-merge-create")

	upsert := func(rev, workRev int64, title, reqID string) (*WorkView, error) {
		return f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
			WorkID:           value.ID,
			BlockID:          "bp-blank-notes",
			Kind:             "markdown",
			SchemaVersion:    1,
			Revision:         rev,
			Title:            title,
			Status:           BlockReady,
			Data:             json.RawMessage(`{"content":"merge"}`),
			Source:           BlockSource{Provider: "user", Mode: "snapshot"},
			ExpectedRevision: workRev,
			RequestID:        reqID,
		})
	}

	// Apply revision 2.
	v1, err := upsert(2, 2, "v2", "merge-2")
	if err != nil {
		t.Fatalf("upsert rev 2: %v", err)
	}
	if v1.Revision != 3 {
		t.Fatalf("work revision = %d after rev 2", v1.Revision)
	}

	// Revision 3 supersedes.
	v2, err := upsert(3, 3, "v3", "merge-3")
	if err != nil {
		t.Fatalf("upsert rev 3: %v", err)
	}
	for i := range v2.Work.Blocks {
		if v2.Work.Blocks[i].ID == "bp-blank-notes" && v2.Work.Blocks[i].Title != "v3" {
			t.Fatal("rev 3 did not supersede")
		}
	}

	// Revision 1 (lower than current 3) is a successful late-event no-op.
	v3, err := upsert(1, 4, "v1-late", "merge-late")
	if err != nil {
		t.Fatalf("late revision should be ignored: %v", err)
	}
	if v3.Revision != 4 {
		t.Fatalf("late event advanced work revision to %d, want 4", v3.Revision)
	}

	// Revision 3 with same content is idempotent even under a new request ID.
	v4, err := upsert(3, 4, "v3", "merge-3-dup")
	if err != nil {
		t.Fatalf("idempotent rev 3: %v", err)
	}
	if v4.Revision != 4 {
		t.Fatalf("idempotent block upsert advanced work revision to %d, want 4", v4.Revision)
	}
	// Check block is still at rev 3.
	for i := range v4.Work.Blocks {
		if v4.Work.Blocks[i].ID == "bp-blank-notes" && v4.Work.Blocks[i].Revision != 3 {
			t.Fatalf("block revision = %d after idempotent rev 3", v4.Work.Blocks[i].Revision)
		}
	}

	// Revision 3 with different content → conflict.
	conflictView, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         3,
		Title:            "conflict-v3",
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"different"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 4,
		RequestID:        "merge-conflict",
	})
	if err == nil {
		t.Fatal("expected conflict for same revision different content")
	}
	var conflict *ErrBlockConflict
	if !errors.As(err, &conflict) || !conflict.Retryable || conflict.CurrentRevision != 3 || conflict.CurrentWorkRevision != 4 {
		t.Fatalf("conflict error = %#v, want retryable block/work revisions 3/4", err)
	}
	if conflictView == nil {
		t.Fatal("conflict must return latest view")
	}
}

// ── Tombstone anti-revival ─────────────────────────────────────────────────

func TestBlockTombstoneAntiRevival(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-tomb-create")

	// Remove the block (tombstone at rev 2).
	_, err := f.svc.RemoveBlock(context.Background(), BlockRemoveInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Revision:         2,
		ExpectedRevision: 2,
		RequestID:        "tomb-remove",
	})
	if err != nil {
		t.Fatalf("RemoveBlock: %v", err)
	}

	view := mustServiceView(t, f.svc, value.ID)
	var block *BlockInstance
	for i := range view.Work.Blocks {
		if view.Work.Blocks[i].ID == "bp-blank-notes" {
			block = &view.Work.Blocks[i]
			break
		}
	}
	if block == nil {
		t.Fatal("tombstoned block is still present in projection (correct)")
	}
	if !block.Tombstone {
		t.Fatal("block should be tombstoned but is not")
	}

	// An event older than the tombstone is ignored and cannot revive it.
	late, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         1, // lower than tombstone's 2
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"old"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 3,
		RequestID:        "tomb-old-event",
	})
	if err != nil {
		t.Fatalf("late event should be ignored: %v", err)
	}
	if late.Revision != 3 {
		t.Fatalf("late event advanced work revision to %d, want 3", late.Revision)
	}
	for i := range late.Work.Blocks {
		if late.Work.Blocks[i].ID == "bp-blank-notes" && !late.Work.Blocks[i].Tombstone {
			t.Fatal("late event revived tombstoned block")
		}
	}

	// Higher revision can set new content and un-tombstone.
	_, err = f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         3,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"revived"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 3,
		RequestID:        "tomb-revive",
	})
	if err != nil {
		t.Fatalf("higher revision revive: %v", err)
	}
	view = mustServiceView(t, f.svc, value.ID)
	for i := range view.Work.Blocks {
		if view.Work.Blocks[i].ID == "bp-blank-notes" {
			block = &view.Work.Blocks[i]
			break
		}
	}
	if block.Tombstone {
		t.Fatal("block should be revived (tombstone=false)")
	}
	if block.Revision != 3 {
		t.Fatalf("block revision = %d after revive, want 3", block.Revision)
	}
}

// ── RemoveBlock idempotent ──────────────────────────────────────────────────

func TestBlockRemoveIdempotent(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-rem-idem-create")

	input := BlockRemoveInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Revision:         2,
		ExpectedRevision: 2,
		RequestID:        "rem-idem",
	}

	first, err := f.svc.RemoveBlock(context.Background(), input)
	if err != nil {
		t.Fatalf("first RemoveBlock: %v", err)
	}
	if first.Revision != 3 {
		t.Fatalf("first revision = %d, want 3", first.Revision)
	}

	restarted := f.restart(t)
	second, err := restarted.RemoveBlock(context.Background(), input)
	if err != nil {
		t.Fatalf("idempotent RemoveBlock: %v", err)
	}
	if second.Revision != 3 {
		t.Fatalf("idempotent revision = %d, want 3", second.Revision)
	}

	// Verify block is still present but tombstoned.
	for i := range second.Work.Blocks {
		if second.Work.Blocks[i].ID == "bp-blank-notes" {
			if !second.Work.Blocks[i].Tombstone {
				t.Fatal("block should be tombstoned")
			}
			return
		}
	}
	t.Fatal("tombstoned block not found in projection — should not be physically removed")
}

// ── Placement stable sort ───────────────────────────────────────────────────

func TestBlockPlacementStableSort(t *testing.T) {
	f := newServiceFixture(t)
	value, err := f.svc.Create(context.Background(), CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: "blueprint:info-organize", SchemaVersion: SchemaVersion, Version: 1},
		Name:         "Placement Sort",
		Inputs:       map[string]any{"topic": "stable sort"},
		RequestID:    "block-plc-sort",
	})
	if err != nil {
		t.Fatalf("Create info-organize: %v", err)
	}

	// Equal slot/order values use BlockID as a deterministic tie-breaker.
	view, err := f.svc.UpdatePlacements(context.Background(), BlockPlacementInput{
		WorkID: value.ID,
		Placements: []BlockPlacement{
			{BlockID: "bp-io-result", Slot: "primary", Order: 1},
			{BlockID: "bp-io-checklist", Slot: "primary", Order: 1},
		},
		ExpectedRevision: 2,
		RequestID:        "plc-sort-req",
	})
	if err != nil {
		t.Fatalf("UpdatePlacements: %v", err)
	}

	if len(view.Work.Placements) != 2 {
		t.Fatalf("placements count = %d, want 2", len(view.Work.Placements))
	}
	want := []string{"bp-io-checklist", "bp-io-result"}
	for i, p := range view.Work.Placements {
		if p.BlockID != want[i] || p.Slot != "primary" || p.Order != 1 {
			t.Fatalf("placement[%d] = %+v, want block %s at primary:1", i, p, want[i])
		}
	}
}

// ── Placement validates block references ────────────────────────────────────

func TestBlockPlacementValidatesReferences(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-plc-refs")

	_, err := f.svc.UpdatePlacements(context.Background(), BlockPlacementInput{
		WorkID:           value.ID,
		Placements:       []BlockPlacement{{BlockID: "nonexistent", Slot: "primary", Order: 0}},
		ExpectedRevision: 2,
		RequestID:        "plc-bad-ref",
	})
	if err == nil {
		t.Fatal("expected error for unknown block reference")
	}
	if !strings.Contains(err.Error(), "unknown block") {
		t.Fatalf("error = %v, want 'unknown block'", err)
	}

	// Empty slot should also be rejected.
	_, err = f.svc.UpdatePlacements(context.Background(), BlockPlacementInput{
		WorkID:           value.ID,
		Placements:       []BlockPlacement{{BlockID: "bp-blank-notes", Slot: "", Order: 0}},
		ExpectedRevision: 2,
		RequestID:        "plc-empty-slot",
	})
	if err == nil {
		t.Fatal("expected error for empty slot")
	}

	invalid := []struct {
		name       string
		placements []BlockPlacement
	}{
		{"duplicate", []BlockPlacement{{BlockID: "bp-blank-notes", Slot: "primary"}, {BlockID: "bp-blank-notes", Slot: "result"}}},
		{"unsupported-slot", []BlockPlacement{{BlockID: "bp-blank-notes", Slot: "sidebar"}}},
		{"negative-order", []BlockPlacement{{BlockID: "bp-blank-notes", Slot: "primary", Order: -1}}},
		{"negative-span", []BlockPlacement{{BlockID: "bp-blank-notes", Slot: "primary", Span: -1}}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, shapeErr := f.svc.UpdatePlacements(context.Background(), BlockPlacementInput{
				WorkID: value.ID, Placements: tc.placements, ExpectedRevision: 2, RequestID: "plc-shape-" + tc.name,
			})
			if shapeErr == nil {
				t.Fatalf("expected placement shape error for %+v", tc.placements)
			}
		})
	}
}

// ── Editable guard ──────────────────────────────────────────────────────────

func TestBlockEditableGuard(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-editable-create")

	// bp-blank-notes is editable — user can upsert.
	_, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         2,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"ok"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2,
		RequestID:        "editable-ok",
	})
	if err != nil {
		t.Fatalf("editable block upsert should succeed: %v", err)
	}

	// The producer label does not bypass the Blueprint editable flag.
	_, err = f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         3,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"ai write"}`),
		Source:           BlockSource{Provider: "ai", Mode: "snapshot"},
		ExpectedRevision: 3,
		RequestID:        "editable-ai",
	})
	if err != nil {
		t.Fatalf("AI upsert should not be blocked by editable: %v", err)
	}

	// RemoveBlock is always user-initiated and checks editable.
	_, err = f.svc.RemoveBlock(context.Background(), BlockRemoveInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Revision:         4,
		ExpectedRevision: 4,
		RequestID:        "editable-remove",
	})
	if err != nil {
		t.Fatalf("editable block remove should succeed: %v", err)
	}

	f.svc = NewServiceWithTools(f.store, NewBlueprintRegistry(), &fakeToolCatalog{tools: map[string]ToolCapability{
		"read_file": availableTool(),
	}}, f.sink)
	readOnly, err := f.svc.Create(context.Background(), CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: "blueprint:code-review", SchemaVersion: SchemaVersion, Version: 1},
		Name:         "Read-only Block",
		Inputs:       map[string]any{"target": "internal/work"},
		RequestID:    "editable-readonly-work",
	})
	if err != nil {
		t.Fatalf("Create code-review: %v", err)
	}

	_, err = f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           readOnly.ID,
		BlockID:          "bp-cr-code",
		Kind:             "code",
		SchemaVersion:    1,
		Revision:         2,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"language":"go","content":"package work"}`),
		Source:           BlockSource{Provider: "ai", Mode: "snapshot"},
		ExpectedRevision: 2,
		RequestID:        "editable-readonly-upsert",
	})
	if err == nil || !strings.Contains(err.Error(), "not user-editable") {
		t.Fatalf("read-only Blueprint block upsert error = %v", err)
	}

	_, err = f.svc.UpdatePlacements(context.Background(), BlockPlacementInput{
		WorkID: readOnly.ID,
		Placements: []BlockPlacement{
			{BlockID: "bp-cr-findings", Slot: "primary", Order: 0},
			{BlockID: "bp-cr-summary", Slot: "result", Order: 1},
			{BlockID: "bp-cr-code", Slot: "primary", Order: 2},
		},
		ExpectedRevision: 2,
		RequestID:        "editable-readonly-placement",
	})
	if err == nil || !strings.Contains(err.Error(), "not user-editable") {
		t.Fatalf("read-only Blueprint placement error = %v", err)
	}
}

// ── Kind/schema validation ─────────────────────────────────────────────────

func TestBlockKindSchemaValidation(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-kind-create")

	// Wrong kind for spec.
	_, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "checklist", // spec says "markdown"
		SchemaVersion:    1,
		Revision:         2,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"items":[]}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2,
		RequestID:        "kind-wrong",
	})
	if err == nil {
		t.Fatal("expected error for kind mismatch")
	}
	if !strings.Contains(err.Error(), "does not match spec kind") {
		t.Fatalf("error = %v, want kind mismatch message", err)
	}
}

// ── Future schema protection ────────────────────────────────────────────────

func TestBlockFutureSchemaRejected(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-future-create")

	_, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    SchemaVersion + 1, // future
		Revision:         2,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"future"}`),
		Source:           BlockSource{Provider: "ai", Mode: "snapshot"},
		ExpectedRevision: 2,
		RequestID:        "future-schema",
	})
	if err == nil {
		t.Fatal("expected future schema rejection")
	}
	var future *ErrFutureSchema
	if !errors.As(err, &future) {
		t.Fatalf("error = %v, want ErrFutureSchema", err)
	}
}

// ── State guard ─────────────────────────────────────────────────────────────

func TestBlockStateGuard(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-state-create")

	// Put work into running state via event injection.
	store, err := NewFileWorkStore(f.root, 0)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	workDir, err := store.workPath(value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workDir); err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(value.ID, WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "ev-running",
		RequestID:     "ev-running",
		WorkID:        value.ID,
		Type:          EventDraftUpdated,
		Revision:      3,
		BaseRevision:  2,
		Payload:       json.RawMessage(`{"state":"running"}`),
		CreatedAt:     value.UpdatedAt,
	})
	if err != nil {
		t.Fatalf("append running event: %v", err)
	}
	if err := ReleaseWorkLease(workDir); err != nil {
		t.Fatal(err)
	}

	// Restart service so projection picks up running state.
	svc2 := f.restart(t)
	_, err = svc2.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         2,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"nope"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 3,
		RequestID:        "state-guard",
	})
	if err == nil {
		t.Fatal("expected error when work is running")
	}
	if !strings.Contains(err.Error(), "blocks are immutable") {
		t.Fatalf("error = %v, want immutable message", err)
	}
}

// ── Conflict returns latest snapshot ────────────────────────────────────────

func TestBlockConflictReturnsLatestSnapshot(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-conflict-snap")

	// First upsert at revision 3.
	_, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         2,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"A"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2,
		RequestID:        "conflict-a",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Now conflicting upsert at same block revision but different content.
	view, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         2,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"B"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 3,
		RequestID:        "conflict-b",
	})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if view == nil || view.Work == nil {
		t.Fatal("conflict must return latest view with current state")
	}
	if view.Revision != 3 {
		t.Fatalf("conflict view revision = %d, want 3", view.Revision)
	}
	var conflict *ErrBlockConflict
	if !errors.As(err, &conflict) || !conflict.Retryable || conflict.CurrentRevision != 2 || conflict.CurrentWorkRevision != 3 {
		t.Fatalf("conflict metadata = %#v", err)
	}
	// The latest view should have content "A", not "B".
	for i := range view.Work.Blocks {
		if view.Work.Blocks[i].ID == "bp-blank-notes" {
			if !strings.Contains(string(view.Work.Blocks[i].Data), `"A"`) {
				t.Fatalf("latest block data = %s, want content A", view.Work.Blocks[i].Data)
			}
			break
		}
	}

	view, err = f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID: value.ID, BlockID: "bp-blank-notes", Kind: "markdown", SchemaVersion: 1,
		Revision: 3, Status: BlockReady, Data: json.RawMessage(`{"content":"C"}`),
		Source: BlockSource{Provider: "user", Mode: "snapshot"}, ExpectedRevision: 2, RequestID: "conflict-work-revision",
	})
	if !errors.As(err, &conflict) || conflict.CurrentWorkRevision != 3 || conflict.ExpectedWorkRevision != 2 {
		t.Fatalf("work revision conflict = %#v", err)
	}
	if view == nil || view.Revision != 3 {
		t.Fatalf("work revision conflict view = %+v", view)
	}
}

// ── Restart recovery ────────────────────────────────────────────────────────

func TestBlockUpsertRestartRecovery(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-recover-create")

	// Simulate crash after event append but before projection write.
	original := writeDerivedFile
	failed := false
	writeDerivedFile = func(path string, data []byte, mode fs.FileMode) error {
		if !failed && filepath.Base(path) == "projection.json" {
			failed = true
			return errors.New("injected projection failure")
		}
		return original(path, data, mode)
	}
	t.Cleanup(func() { writeDerivedFile = original })

	input := BlockUpsertInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Kind:             "markdown",
		SchemaVersion:    1,
		Revision:         2,
		Status:           BlockReady,
		Data:             json.RawMessage(`{"content":"recover"}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2,
		RequestID:        "block-recover-req",
	}

	_, err := f.svc.UpsertBlock(context.Background(), input)
	if err == nil {
		t.Fatal("expected projection write failure")
	}
	if !strings.Contains(err.Error(), "injected projection failure") {
		t.Fatalf("error = %v, want projection failure", err)
	}
	writeDerivedFile = original

	// Retry after restart recovers.
	restarted := f.restart(t)
	view, err := restarted.UpsertBlock(context.Background(), input)
	if err != nil {
		t.Fatalf("retry UpsertBlock after restart: %v", err)
	}
	if view.Revision != 3 {
		t.Fatalf("recovery revision = %d, want 3", view.Revision)
	}
}

// ── Placement idempotent ────────────────────────────────────────────────────

func TestBlockPlacementIdempotent(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-plc-idem")

	placements := []BlockPlacement{
		{BlockID: "bp-blank-notes", Slot: "primary", Order: 1},
	}

	first, err := f.svc.UpdatePlacements(context.Background(), BlockPlacementInput{
		WorkID:           value.ID,
		Placements:       placements,
		ExpectedRevision: 2,
		RequestID:        "plc-idem-req",
	})
	if err != nil {
		t.Fatalf("first UpdatePlacements: %v", err)
	}
	if first.Revision != 3 {
		t.Fatalf("first revision = %d, want 3", first.Revision)
	}

	restarted := f.restart(t)
	second, err := restarted.UpdatePlacements(context.Background(), BlockPlacementInput{
		WorkID:           value.ID,
		Placements:       placements,
		ExpectedRevision: 2,
		RequestID:        "plc-idem-req",
	})
	if err != nil {
		t.Fatalf("idempotent UpdatePlacements: %v", err)
	}
	if second.Revision != 3 {
		t.Fatalf("idempotent revision = %d, want 3", second.Revision)
	}
	third, err := restarted.UpdatePlacements(context.Background(), BlockPlacementInput{
		WorkID:           value.ID,
		Placements:       placements,
		ExpectedRevision: 3,
		RequestID:        "plc-idem-new-request",
	})
	if err != nil || third.Revision != 3 {
		t.Fatalf("semantic placement no-op = %+v, err %v", third, err)
	}
}

// ── Four-state acceptance ───────────────────────────────────────────────────

func TestBlockMultiUpsertSequence(t *testing.T) {
	// Tests sequential upsert + tombstone over the same block across
	// multiple revisions — the core revision merge pattern.
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-multi-create")

	makeInput := func(rev, workRev int64, content, reqID string) BlockUpsertInput {
		return BlockUpsertInput{
			WorkID:           value.ID,
			BlockID:          "bp-blank-notes",
			Kind:             "markdown",
			SchemaVersion:    1,
			Revision:         rev,
			Status:           BlockReady,
			Data:             json.RawMessage(`{"content":"` + content + `"}`),
			Source:           BlockSource{Provider: "user", Mode: "snapshot"},
			ExpectedRevision: workRev,
			RequestID:        reqID,
		}
	}

	// Revision 2.
	_, err := f.svc.UpsertBlock(context.Background(), makeInput(2, 2, "first", "multi-1"))
	if err != nil {
		t.Fatalf("rev 2: %v", err)
	}

	// Revision 3.
	_, err = f.svc.UpsertBlock(context.Background(), makeInput(3, 3, "second", "multi-2"))
	if err != nil {
		t.Fatalf("rev 3: %v", err)
	}

	// Revision 4.S
	_, err = f.svc.UpsertBlock(context.Background(), makeInput(4, 4, "third", "multi-3"))
	if err != nil {
		t.Fatalf("rev 4: %v", err)
	}

	// Tombstone at rev 5.
	_, err = f.svc.RemoveBlock(context.Background(), BlockRemoveInput{
		WorkID:           value.ID,
		BlockID:          "bp-blank-notes",
		Revision:         5,
		ExpectedRevision: 5,
		RequestID:        "multi-tomb",
	})
	if err != nil {
		t.Fatalf("tombstone rev 5: %v", err)
	}

	view := mustServiceView(t, f.svc, value.ID)
	for i := range view.Work.Blocks {
		if view.Work.Blocks[i].ID == "bp-blank-notes" {
			if !view.Work.Blocks[i].Tombstone {
				t.Fatal("block should be tombstoned")
			}
			if view.Work.Blocks[i].Revision != 5 {
				t.Fatalf("tombstone revision = %d, want 5", view.Work.Blocks[i].Revision)
			}
			return
		}
	}
	t.Fatal("block not found after tombstone")
}

// ── Future schema: load existing as read-only ───────────────────────────────

func TestBlockFutureSchemaReadOnly(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-future-readonly")
	view := appendFutureBlock(t, f, value.ID)
	restarted := f.restart(t)
	block := view.Work.Blocks[0]
	if block.SchemaVersion != SchemaVersion+1 || block.Fallback.Summary != "future block fallback" {
		t.Fatalf("future block read fallback lost: %+v", block)
	}
	if !strings.Contains(string(block.Fallback.Data), `"readable"`) {
		t.Fatalf("future fallback data = %s", block.Fallback.Data)
	}

	name := "blocked update"
	writes := []struct {
		name string
		run  func() error
	}{
		{"upsert", func() error {
			_, err := restarted.UpsertBlock(context.Background(), BlockUpsertInput{
				WorkID: value.ID, BlockID: block.ID, Kind: block.Kind, SchemaVersion: SchemaVersion,
				Revision: 3, Status: BlockReady, Data: json.RawMessage(`{"content":"overwrite"}`),
				Source: BlockSource{Provider: "user", Mode: "snapshot"}, ExpectedRevision: view.Revision, RequestID: "future-block-overwrite",
			})
			return err
		}},
		{"remove", func() error {
			_, err := restarted.RemoveBlock(context.Background(), BlockRemoveInput{
				WorkID: value.ID, BlockID: block.ID, Revision: 3, ExpectedRevision: view.Revision, RequestID: "future-block-remove",
			})
			return err
		}},
		{"placements", func() error {
			_, err := restarted.UpdatePlacements(context.Background(), BlockPlacementInput{
				WorkID: value.ID, Placements: view.Work.Placements, ExpectedRevision: view.Revision, RequestID: "future-block-placement",
			})
			return err
		}},
		{"draft", func() error {
			_, err := restarted.UpdateDraft(context.Background(), UpdateDraftInput{
				WorkID: value.ID, Name: &name, ExpectedRevision: view.Revision, RequestID: "future-block-draft",
			})
			return err
		}},
		{"archive", func() error {
			_, err := restarted.Archive(context.Background(), value.ID, "future-block-archive")
			return err
		}},
		{"delete", func() error {
			return restarted.Delete(context.Background(), value.ID, "future-block-delete")
		}},
	}
	for _, tc := range writes {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			var futureErr *ErrFutureSchema
			if !errors.As(err, &futureErr) || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("future block %s error = %v, want typed read-only ErrFutureSchema", tc.name, err)
			}
			latest := mustServiceView(t, restarted, value.ID)
			if latest.Revision != view.Revision || latest.Work.Blocks[0].SchemaVersion != SchemaVersion+1 {
				t.Fatalf("future block %s wrote state: revision=%d block=%+v", tc.name, latest.Revision, latest.Work.Blocks[0])
			}
		})
	}
}

func TestBlockFutureSchemaRestoreReadOnlyAfterRestart(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-future-restore")
	if _, err := f.svc.Archive(context.Background(), value.ID, "block-future-restore-archive"); err != nil {
		t.Fatal(err)
	}
	view := appendFutureBlock(t, f, value.ID)
	restarted := f.restart(t)
	_, err := restarted.Restore(context.Background(), value.ID, "block-future-restore-attempt")
	var futureErr *ErrFutureSchema
	if !errors.As(err, &futureErr) || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("future block restore error = %v, want typed read-only ErrFutureSchema", err)
	}
	latest := mustServiceView(t, restarted, value.ID)
	if latest.Revision != view.Revision || latest.Work.ArchiveState != ArchiveArchived {
		t.Fatalf("future restore wrote state: revision=%d archive=%s", latest.Revision, latest.Work.ArchiveState)
	}
}

func TestBlockReducerRevisionMerge(t *testing.T) {
	now := time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC)
	baseBlock := BlockInstance{
		ID: "block-a", Kind: "markdown", SchemaVersion: 1, Revision: 3,
		Status: BlockReady, Data: json.RawMessage(`{"content":"v3"}`),
		Source: BlockSource{Provider: "user", Mode: "snapshot"}, CreatedAt: now, UpdatedAt: now,
	}
	newWork := func() *Work {
		return &Work{SchemaVersion: SchemaVersion, ID: "work-reducer", Blocks: []BlockInstance{baseBlock}}
	}
	upsertEvent := func(block BlockInstance, eventRevision int64) WorkEvent {
		payload, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		return WorkEvent{WorkID: "work-reducer", Type: EventBlockUpserted, Revision: eventRevision, Payload: payload, CreatedAt: now.Add(time.Duration(eventRevision) * time.Minute)}
	}
	reduce := DefaultReducer()

	older := baseBlock
	older.Revision = 2
	older.Data = json.RawMessage(`{"content":"late"}`)
	got, err := reduce(upsertEvent(older, 4), newWork())
	if err != nil || got.Blocks[0].Revision != 3 || strings.Contains(string(got.Blocks[0].Data), "late") {
		t.Fatalf("older event merge = block %+v, err %v", got.Blocks[0], err)
	}

	got, err = reduce(upsertEvent(baseBlock, 4), newWork())
	if err != nil || got.Blocks[0].Revision != 3 {
		t.Fatalf("same revision/digest merge = block %+v, err %v", got.Blocks[0], err)
	}

	different := baseBlock
	different.Data = json.RawMessage(`{"content":"conflict"}`)
	_, err = reduce(upsertEvent(different, 4), newWork())
	var conflict *ErrBlockConflict
	if !errors.As(err, &conflict) || conflict.CurrentRevision != 3 {
		t.Fatalf("same revision conflict = %#v", err)
	}

	removePayload, err := json.Marshal(blockRemovedPayload{BlockID: baseBlock.ID, Revision: 4})
	if err != nil {
		t.Fatal(err)
	}
	tombstoned, err := reduce(WorkEvent{
		WorkID: "work-reducer", Type: EventBlockRemoved, Revision: 4,
		Payload: removePayload, CreatedAt: now.Add(4 * time.Minute),
	}, newWork())
	if err != nil || !tombstoned.Blocks[0].Tombstone || tombstoned.Blocks[0].Revision != 4 {
		t.Fatalf("remove merge = block %+v, err %v", tombstoned.Blocks[0], err)
	}
	got, err = reduce(upsertEvent(baseBlock, 5), tombstoned)
	if err != nil || !got.Blocks[0].Tombstone || got.Blocks[0].Revision != 4 {
		t.Fatalf("late upsert revived tombstone: block %+v, err %v", got.Blocks[0], err)
	}
}

func TestBlockReducerRejectsInvalidPlacements(t *testing.T) {
	base := Work{
		SchemaVersion: SchemaVersion,
		ID:            "work-placement-reducer",
		Name:          "original",
		Definition: WorkDefinitionSnapshot{BlockSpecs: []BlockSpec{
			{ID: "editable", Kind: "markdown", SchemaVersion: 1, Editable: true},
			{ID: "readonly", Kind: "code", SchemaVersion: 1, Editable: false},
			{ID: "removed", Kind: "markdown", SchemaVersion: 1, Editable: true},
		}},
		Blocks: []BlockInstance{
			{ID: "editable", Kind: "markdown", SchemaVersion: 1, Revision: 1},
			{ID: "readonly", Kind: "code", SchemaVersion: 1, Revision: 1},
			{ID: "removed", Kind: "markdown", SchemaVersion: 1, Revision: 2, Tombstone: true},
		},
		Placements: []BlockPlacement{
			{BlockID: "editable", Slot: "primary", Order: 0},
			{BlockID: "readonly", Slot: "secondary", Order: 1},
		},
	}
	validReadonly := BlockPlacement{BlockID: "readonly", Slot: "secondary", Order: 1}
	tests := []struct {
		name       string
		placements []BlockPlacement
		want       string
	}{
		{"duplicate", []BlockPlacement{{BlockID: "editable", Slot: "primary"}, {BlockID: "editable", Slot: "result"}, validReadonly}, "duplicate"},
		{"unknown", []BlockPlacement{{BlockID: "missing", Slot: "primary"}, validReadonly}, "unknown block"},
		{"empty-slot", []BlockPlacement{{BlockID: "editable", Slot: ""}, validReadonly}, "unsupported slot"},
		{"illegal-slot", []BlockPlacement{{BlockID: "editable", Slot: "sidebar"}, validReadonly}, "unsupported slot"},
		{"negative-order", []BlockPlacement{{BlockID: "editable", Slot: "primary", Order: -1}, validReadonly}, "order must be non-negative"},
		{"negative-span", []BlockPlacement{{BlockID: "editable", Slot: "primary", Span: -1}, validReadonly}, "span must be non-negative"},
		{"tombstone", []BlockPlacement{{BlockID: "removed", Slot: "primary"}, validReadonly}, "tombstoned block"},
		{"readonly", []BlockPlacement{{BlockID: "editable", Slot: "primary"}, {BlockID: "readonly", Slot: "primary", Order: 1}}, "not user-editable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := base
			current.Blocks = append([]BlockInstance(nil), base.Blocks...)
			current.Placements = append([]BlockPlacement(nil), base.Placements...)
			before := append([]BlockPlacement(nil), current.Placements...)
			beforeUpdatedAt := current.UpdatedAt
			payload, err := json.Marshal(map[string]any{"name": "must-not-apply", "placements": tc.placements})
			if err != nil {
				t.Fatal(err)
			}
			_, err = DefaultReducer()(WorkEvent{
				WorkID: current.ID, Type: EventDraftUpdated, Revision: 3, BaseRevision: 2,
				Payload: payload, CreatedAt: time.Now().UTC(),
			}, &current)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("reducer error = %v, want %q", err, tc.want)
			}
			if !reflect.DeepEqual(current.Placements, before) {
				t.Fatalf("reducer mutated placements on error: got %+v want %+v", current.Placements, before)
			}
			if current.Name != "original" || current.UpdatedAt != beforeUpdatedAt {
				t.Fatalf("reducer partially mutated projection on placement error: name=%q updatedAt=%s", current.Name, current.UpdatedAt)
			}
		})
	}
}

func TestBlockDigestCanonicalAndPrecise(t *testing.T) {
	base := BlockInstance{
		Kind: "table", SchemaVersion: 1, Status: BlockReady,
		Data:   json.RawMessage(`{"b":1,"a":9007199254740992}`),
		Source: BlockSource{Provider: "user", Mode: "snapshot"},
	}
	left, err := blockContentDigest(&base)
	if err != nil {
		t.Fatal(err)
	}
	reordered := base
	reordered.Data = json.RawMessage(`{"a":9007199254740992,"b":1}`)
	right, err := blockContentDigest(&reordered)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("canonical digest mismatch: %s != %s", left, right)
	}
	different := base
	different.Data = json.RawMessage(`{"b":1,"a":9007199254740993}`)
	changed, err := blockContentDigest(&different)
	if err != nil {
		t.Fatal(err)
	}
	if left == changed {
		t.Fatalf("digest lost integer precision: %s", left)
	}
}

func TestBlockReducerOutOfOrderRemove(t *testing.T) {
	now := time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(blockRemovedPayload{BlockID: "block-late", Revision: 5})
	if err != nil {
		t.Fatal(err)
	}
	reduce := DefaultReducer()
	work, err := reduce(WorkEvent{
		WorkID: "work-out-of-order", Type: EventBlockRemoved, Revision: 3,
		Payload: payload, CreatedAt: now,
	}, &Work{SchemaVersion: SchemaVersion, ID: "work-out-of-order"})
	if err != nil || len(work.Blocks) != 1 || !work.Blocks[0].Tombstone || work.Blocks[0].Revision != 5 {
		t.Fatalf("out-of-order tombstone marker = %+v, err %v", work.Blocks, err)
	}
	late := BlockInstance{
		ID: "block-late", Kind: "markdown", SchemaVersion: 1, Revision: 4,
		Status: BlockReady, Data: json.RawMessage(`{"content":"late"}`), Source: BlockSource{Provider: "user", Mode: "snapshot"},
	}
	latePayload, err := json.Marshal(late)
	if err != nil {
		t.Fatal(err)
	}
	work, err = reduce(WorkEvent{
		WorkID: "work-out-of-order", Type: EventBlockUpserted, Revision: 4,
		Payload: latePayload, CreatedAt: now.Add(time.Minute),
	}, work)
	if err != nil || !work.Blocks[0].Tombstone || work.Blocks[0].Revision != 5 {
		t.Fatalf("late upsert revived missing-block tombstone: %+v, err %v", work.Blocks[0], err)
	}
}

func TestBlockLateRequestRetryReturnsLatest(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-late-request")
	first := BlockUpsertInput{
		WorkID: value.ID, BlockID: "bp-blank-notes", Kind: "markdown", SchemaVersion: 1,
		Revision: 2, Status: BlockReady, Data: json.RawMessage(`{"content":"first"}`),
		Source: BlockSource{Provider: "user", Mode: "snapshot"}, ExpectedRevision: 2, RequestID: "late-request-first",
	}
	if _, err := f.svc.UpsertBlock(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Revision = 3
	second.Data = json.RawMessage(`{"content":"second"}`)
	second.ExpectedRevision = 3
	second.RequestID = "late-request-second"
	if _, err := f.svc.UpsertBlock(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	retried, err := f.restart(t).UpsertBlock(context.Background(), first)
	if err != nil || retried.Revision != 4 || retried.Work.Blocks[0].Revision != 3 || !strings.Contains(string(retried.Work.Blocks[0].Data), "second") {
		t.Fatalf("late request retry = %+v, err %v", retried, err)
	}
}

func TestBlockLateRemoveRetryDoesNotRetombstone(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-late-remove")
	remove := BlockRemoveInput{
		WorkID: value.ID, BlockID: "bp-blank-notes", Revision: 2,
		ExpectedRevision: 2, RequestID: "late-remove-first",
	}
	if _, err := f.svc.RemoveBlock(context.Background(), remove); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID: value.ID, BlockID: "bp-blank-notes", Kind: "markdown", SchemaVersion: 1,
		Revision: 3, Status: BlockReady, Data: json.RawMessage(`{"content":"revived"}`),
		Source: BlockSource{Provider: "user", Mode: "snapshot"}, ExpectedRevision: 3, RequestID: "late-remove-revive",
	}); err != nil {
		t.Fatal(err)
	}

	retried, err := f.restart(t).RemoveBlock(context.Background(), remove)
	if err != nil || retried.Revision != 4 || retried.Work.Blocks[0].Tombstone || retried.Work.Blocks[0].Revision != 3 {
		t.Fatalf("late remove retry = %+v, err %v", retried, err)
	}
}

func TestBlockConcurrentUpsertConverges(t *testing.T) {
	tests := []struct {
		name         string
		sameDigest   bool
		sameRequest  bool
		wantErrors   int
		wantRevision int64
	}{
		{"same-digest-different-request", true, false, 0, 4},
		{"same-digest-same-request", true, true, 0, 3},
		{"different-digest-different-request", false, false, 1, 3},
		{"different-digest-same-request", false, true, 1, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newServiceFixture(t)
			value := mustServiceCreate(t, f.svc, "block-concurrent-"+tc.name)
			barrier := newBlockCommitBarrier(f.store, EventBlockUpserted, 2)
			svc := NewService(barrier, NewBlueprintRegistry(), f.sink)
			left := BlockUpsertInput{
				WorkID: value.ID, BlockID: "bp-blank-notes", Kind: "markdown", SchemaVersion: 1,
				Revision: 2, Status: BlockReady, Data: json.RawMessage(`{"content":"left"}`),
				Source: BlockSource{Provider: "user", Mode: "snapshot"}, ExpectedRevision: 2, RequestID: "concurrent-left",
			}
			right := left
			right.RequestID = "concurrent-right"
			if !tc.sameDigest {
				right.Data = json.RawMessage(`{"content":"right"}`)
			}
			if tc.sameRequest {
				right.RequestID = left.RequestID
			}
			type result struct {
				view *WorkView
				err  error
			}
			results := make(chan result, 2)
			for _, input := range []BlockUpsertInput{left, right} {
				input := input
				go func() {
					view, err := svc.UpsertBlock(context.Background(), input)
					results <- result{view: view, err: err}
				}()
			}
			errorCount := 0
			for range 2 {
				result := <-results
				if result.err == nil {
					if result.view == nil || result.view.Revision < 3 || result.view.Revision > tc.wantRevision {
						t.Fatalf("successful concurrent view = %+v", result.view)
					}
					continue
				}
				errorCount++
				var conflict *ErrBlockConflict
				if !errors.As(result.err, &conflict) || result.view == nil || result.view.Revision != 3 {
					t.Fatalf("concurrent conflict = %v, view=%+v", result.err, result.view)
				}
				if tc.sameRequest && conflict.Retryable {
					t.Fatalf("request fingerprint conflict must not be retryable: %+v", conflict)
				}
			}
			if errorCount != tc.wantErrors {
				t.Fatalf("concurrent errors = %d, want %d", errorCount, tc.wantErrors)
			}
			latest := mustServiceView(t, svc, value.ID)
			if latest.Revision != tc.wantRevision || latest.Work.Blocks[0].Revision != 2 {
				t.Fatalf("concurrent upsert final state: revision=%d block=%+v", latest.Revision, latest.Work.Blocks[0])
			}
		})
	}
}

func TestBlockRevisionConflictKindIgnoresRequestIDShape(t *testing.T) {
	for _, requestID := range []string{"", "different-request", "same-request"} {
		err := &ErrWorkEventConflict{
			Kind: WorkEventRevisionConflict, Reason: "forced revision race", RequestID: requestID,
		}
		if !revisionChainConflict(err) {
			t.Fatalf("revision conflict with requestID %q was not retryable", requestID)
		}
	}
	if revisionChainConflict(&ErrWorkEventConflict{
		Kind: WorkEventRequestConflict, Reason: "request already used", RequestID: "same-request",
	}) {
		t.Fatal("request fingerprint conflict must not enter semantic re-merge")
	}
}

func TestBlockCommitStorageErrorStaysExplicit(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-storage-error")
	sentinel := errors.New("disk write failed")
	svc := NewService(&blockFailCommitStore{WorkStore: f.store, err: sentinel}, NewBlueprintRegistry(), f.sink)
	_, err := svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID: value.ID, BlockID: "bp-blank-notes", Kind: "markdown", SchemaVersion: 1,
		Revision: 2, Status: BlockReady, Data: json.RawMessage(`{"content":"write"}`),
		Source: BlockSource{Provider: "user", Mode: "snapshot"}, ExpectedRevision: 2, RequestID: "block-storage-error-upsert",
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("storage error = %v, want sentinel", err)
	}
	var conflict *ErrBlockConflict
	if errors.As(err, &conflict) {
		t.Fatalf("storage error was hidden as semantic conflict: %+v", conflict)
	}
	if latest := mustServiceView(t, svc, value.ID); latest.Revision != 2 {
		t.Fatalf("storage error advanced revision to %d", latest.Revision)
	}
}

func TestBlockRevisionRaceHonorsCancellation(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-race-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	store := &blockCancelConflictStore{WorkStore: f.store, cancel: cancel}
	svc := NewService(store, NewBlueprintRegistry(), f.sink)
	_, err := svc.UpsertBlock(ctx, BlockUpsertInput{
		WorkID: value.ID, BlockID: "bp-blank-notes", Kind: "markdown", SchemaVersion: 1,
		Revision: 2, Status: BlockReady, Data: json.RawMessage(`{"content":"cancel"}`),
		Source: BlockSource{Provider: "user", Mode: "snapshot"}, ExpectedRevision: 2, RequestID: "block-race-cancel-upsert",
	})
	if !errors.Is(err, context.Canceled) || store.calls != 1 {
		t.Fatalf("cancelled race error=%v commitCalls=%d", err, store.calls)
	}
	if latest := mustServiceView(t, svc, value.ID); latest.Revision != 2 {
		t.Fatalf("cancelled race advanced revision to %d", latest.Revision)
	}
}

func TestBlockConcurrentRemoveConverges(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-concurrent-remove")
	svc := NewService(newBlockCommitBarrier(f.store, EventBlockRemoved, 2), NewBlueprintRegistry(), f.sink)
	results := make(chan error, 2)
	for _, requestID := range []string{"concurrent-remove-left", "concurrent-remove-right"} {
		requestID := requestID
		go func() {
			_, err := svc.RemoveBlock(context.Background(), BlockRemoveInput{
				WorkID: value.ID, BlockID: "bp-blank-notes", Revision: 2,
				ExpectedRevision: 2, RequestID: requestID,
			})
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent remove: %v", err)
		}
	}
	latest := mustServiceView(t, svc, value.ID)
	if latest.Revision != 4 || !latest.Work.Blocks[0].Tombstone || latest.Work.Blocks[0].Revision != 2 {
		t.Fatalf("concurrent remove did not converge: revision=%d block=%+v", latest.Revision, latest.Work.Blocks[0])
	}
}

func TestBlockConcurrentPlacementsConverge(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "block-concurrent-placement")
	svc := NewService(newBlockCommitBarrier(f.store, EventDraftUpdated, 2), NewBlueprintRegistry(), f.sink)
	placements := []BlockPlacement{{BlockID: "bp-blank-notes", Slot: "primary", Order: 1}}
	results := make(chan error, 2)
	for _, requestID := range []string{"concurrent-placement-left", "concurrent-placement-right"} {
		requestID := requestID
		go func() {
			_, err := svc.UpdatePlacements(context.Background(), BlockPlacementInput{
				WorkID: value.ID, Placements: placements, ExpectedRevision: 2, RequestID: requestID,
			})
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent placements: %v", err)
		}
	}
	latest := mustServiceView(t, svc, value.ID)
	if latest.Revision != 3 || len(latest.Work.Placements) != 1 || latest.Work.Placements[0].Order != 1 {
		t.Fatalf("concurrent placements did not converge: revision=%d placements=%+v", latest.Revision, latest.Work.Placements)
	}
}

type blockCommitBarrier struct {
	WorkStore
	eventType WorkEventType
	mu        sync.Mutex
	remaining int
	release   chan struct{}
}

type blockFailCommitStore struct {
	WorkStore
	err error
}

func (s *blockFailCommitStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	if event.Type == EventBlockUpserted {
		return 0, s.err
	}
	return s.WorkStore.CommitEvent(workID, event)
}

type blockCancelConflictStore struct {
	WorkStore
	cancel context.CancelFunc
	calls  int
}

func (s *blockCancelConflictStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	s.calls++
	s.cancel()
	return 0, &ErrWorkEventConflict{
		Kind: WorkEventRevisionConflict, WorkID: workID, RequestID: event.RequestID, Reason: "forced revision race",
	}
}

func newBlockCommitBarrier(store WorkStore, eventType WorkEventType, count int) *blockCommitBarrier {
	return &blockCommitBarrier{WorkStore: store, eventType: eventType, remaining: count, release: make(chan struct{})}
}

func (b *blockCommitBarrier) CommitEvent(workID string, event WorkEvent) (int64, error) {
	if event.Type != b.eventType {
		return b.WorkStore.CommitEvent(workID, event)
	}
	b.mu.Lock()
	if b.remaining <= 0 {
		b.mu.Unlock()
		return b.WorkStore.CommitEvent(workID, event)
	}
	b.remaining--
	release := b.release
	if b.remaining == 0 {
		close(release)
	}
	b.mu.Unlock()
	select {
	case <-release:
		return b.WorkStore.CommitEvent(workID, event)
	case <-time.After(5 * time.Second):
		return 0, errors.New("block commit barrier timed out")
	}
}

func appendFutureBlock(t *testing.T, f *serviceFixture, workID string) *WorkView {
	t.Helper()
	current, state, err := f.store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	future := current.Blocks[0]
	future.SchemaVersion = SchemaVersion + 1
	future.Revision++
	future.Data = json.RawMessage(`{"futureField":{"version":2}}`)
	future.Fallback = BlockFallback{
		Summary: "future block fallback",
		Data:    json.RawMessage(`{"plain":"readable"}`),
	}
	payload, err := json.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	workDir, err := f.store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workDir); err != nil {
		t.Fatal(err)
	}
	_, appendErr := f.store.Append(workID, WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            fmt.Sprintf("future-block-event-%d", state.Revision+1),
		RequestID:     fmt.Sprintf("future-block-event-%d", state.Revision+1),
		WorkID:        workID,
		Type:          EventBlockUpserted,
		Revision:      state.Revision + 1,
		BaseRevision:  state.Revision,
		Payload:       payload,
		CreatedAt:     time.Now().UTC(),
	})
	if releaseErr := ReleaseWorkLease(workDir); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if appendErr != nil {
		t.Fatalf("append future block event: %v", appendErr)
	}
	return mustServiceView(t, f.svc, workID)
}
