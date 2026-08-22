package work

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── fakeToolCatalog ────────────────────────────────────────────────────────

type fakeToolCatalog struct {
	tools   map[string]ToolCapability
	err     map[string]error
	lastCtx context.Context
}

func (f *fakeToolCatalog) ResolveTool(ctx context.Context, ref ToolContractRef) (ToolCapability, error) {
	f.lastCtx = ctx
	if f.err != nil {
		if e, ok := f.err[ref.Name]; ok {
			return ToolCapability{}, e
		}
	}
	if c, ok := f.tools[ref.Name]; ok {
		return c, nil
	}
	return ToolCapability{Available: false, Reason: "not found"}, nil
}

func availableTool() ToolCapability {
	return ToolCapability{Available: true, Compatible: true}
}

// ── Registry: builtins ─────────────────────────────────────────────────────

func TestNewBlueprintRegistry_HasNineBuiltins(t *testing.T) {
	r := NewBlueprintRegistry()
	all := r.List()
	if len(all) != 9 {
		t.Fatalf("expected 9 built-in blueprints, got %d", len(all))
	}
	ids := make(map[string]bool)
	for _, bp := range all {
		ids[bp.ID] = true
		if bp.Source != BlueprintSystem {
			t.Errorf("builtin %s source = %s, want system", bp.ID, bp.Source)
		}
	}
	wantIDs := []string{"blueprint:blank", "blueprint:code-review", "blueprint:info-organize", "blueprint:report", "blueprint:image-compile", "blueprint:script-writing", "blueprint:financial-budget", "blueprint:git-release", "blueprint:annual-event"}
	for _, id := range wantIDs {
		if !ids[id] {
			t.Errorf("missing builtin blueprint %s", id)
		}
	}
}

func TestBuiltinBlueprints_Valid(t *testing.T) {
	for _, bp := range builtinBlueprints() {
		t.Run(bp.ID, func(t *testing.T) {
			if err := ValidateBlueprint(bp); err != nil {
				t.Fatalf("builtin validation failed: %v", err)
			}
		})
	}
}

func TestBuiltinBlueprints_LookupLatest(t *testing.T) {
	r := NewBlueprintRegistry()
	for _, id := range []string{"blueprint:blank", "blueprint:code-review", "blueprint:info-organize", "blueprint:report"} {
		bp, err := r.LookupLatest(id)
		if err != nil {
			t.Fatalf("LookupLatest(%s): %v", id, err)
		}
		if bp.Version != 1 {
			t.Errorf("%s version = %d, want 1", id, bp.Version)
		}
	}
}

func TestLookupLatest_NotFound(t *testing.T) {
	r := NewBlueprintRegistry()
	_, err := r.LookupLatest("blueprint:nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown blueprint")
	}
}

func TestLookupExact_NotFound(t *testing.T) {
	r := NewBlueprintRegistry()
	_, err := r.LookupExact("blueprint:blank", 999)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
}

func TestLookupLatest_ReturnsCopy(t *testing.T) {
	r := NewBlueprintRegistry()
	bp1, _ := r.LookupLatest("blueprint:blank")
	bp2, _ := r.LookupLatest("blueprint:blank")
	bp1.Name = "mutated"
	if bp2.Name == "mutated" {
		t.Fatal("LookupLatest returned same pointer — mutation leaked")
	}
}

// ── Version management ─────────────────────────────────────────────────────

func TestRegister_DuplicateVersionFails(t *testing.T) {
	r := NewBlueprintRegistry()
	bp := validTestBlueprint()
	if err := r.Register(bp); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := r.Register(bp)
	if err == nil {
		t.Fatal("expected duplicate version error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention 'duplicate': %v", err)
	}
}

func TestMultiVersionLookup(t *testing.T) {
	r := NewBlueprintRegistry()

	v1 := validTestBlueprint()
	v1.ID = "blueprint:multi"
	v1.Version = 1
	v2 := validTestBlueprint()
	v2.ID = "blueprint:multi"
	v2.Version = 2
	v2.Name = "Multi v2"

	if err := r.Register(v1); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if err := r.Register(v2); err != nil {
		t.Fatalf("register v2: %v", err)
	}

	latest, err := r.LookupLatest("blueprint:multi")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != 2 {
		t.Errorf("latest version = %d, want 2", latest.Version)
	}

	exact, err := r.LookupExact("blueprint:multi", 1)
	if err != nil {
		t.Fatal(err)
	}
	if exact.Version != 1 {
		t.Errorf("exact version = %d, want 1", exact.Version)
	}
}

func TestListBySource(t *testing.T) {
	r := NewBlueprintRegistry()
	sys := r.ListBySource(BlueprintSystem)
	if len(sys) != 9 {
		t.Fatalf("system blueprints = %d, want 9", len(sys))
	}
	usr := r.ListBySource(BlueprintUser)
	if len(usr) != 0 {
		t.Fatalf("user blueprints = %d, want 0", len(usr))
	}
}

// ── Loading from disk ──────────────────────────────────────────────────────

func TestLoadFromDir_ValidIndex(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := filepath.Join("testdata", "blueprints")
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	all := r.List()
	if len(all) != 11 {
		t.Fatalf("expected 11 blueprints after load, got %d", len(all))
	}

	bp, err := r.LookupLatest("blueprint:my-review")
	if err != nil {
		t.Fatal(err)
	}
	if bp.Version != 2 {
		t.Errorf("latest my-review version = %d, want 2", bp.Version)
	}
	if bp.Name != "我的审查 v2" {
		t.Errorf("name = %q, want '我的审查 v2'", bp.Name)
	}

	v1, err := r.LookupExact("blueprint:my-review", 1)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Name != "我的审查" {
		t.Errorf("v1 name = %q, want '我的审查'", v1.Name)
	}
}

func TestLoadFromDir_NoIndex(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("missing index should not error: %v", err)
	}
}

func TestLoadFromDir_CorruptIndex(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	badData, err := os.ReadFile(filepath.Join("testdata", "blueprints", "corrupt", "bad-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "index.json"), badData)
	loadErr := r.LoadFromDir(dir)
	if loadErr == nil {
		t.Fatal("expected error for corrupt JSON index")
	}
	if !strings.Contains(loadErr.Error(), "parse") {
		t.Errorf("error should mention 'parse': %v", loadErr)
	}
}

func TestLoadFromDir_BadJSON(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.json"), []byte("{bad"))
	err := r.LoadFromDir(dir)
	if err == nil {
		t.Fatal("expected error for bad JSON index")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention 'parse': %v", err)
	}
}

func TestLoadFromDir_MissingDefFile(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	index := filepath.Join(dir, "index.json")
	writeTestFile(t, index, readFixtureBytes(t, filepath.Join("testdata", "blueprints", "corrupt", "missing-file-index.json")))
	err := r.LoadFromDir(dir)
	if err == nil {
		t.Fatal("expected error for missing definition file")
	}
	if !strings.Contains(err.Error(), "nonexistent.json") {
		t.Errorf("error should mention the missing file: %v", err)
	}
}

func TestLoadFromDir_Idempotent(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := filepath.Join("testdata", "blueprints")
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}
	all1 := r.List()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}
	all2 := r.List()
	if len(all1) != len(all2) {
		t.Fatalf("LoadFromDir not idempotent: %d → %d", len(all1), len(all2))
	}
}

func TestLoadFromDir_InvalidSchemaVersion(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.json"), []byte(`{"schemaVersion":0,"entries":[]}`))
	err := r.LoadFromDir(dir)
	if err == nil {
		t.Fatal("expected error for schemaVersion 0")
	}
}

func TestLoadFromDir_SimulatedRestart(t *testing.T) {
	// Simulate process restart: fresh registry loads same directory.
	dir := filepath.Join("testdata", "blueprints")
	r1 := NewBlueprintRegistry()
	if err := r1.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}
	all1 := r1.List()

	r2 := NewBlueprintRegistry()
	if err := r2.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}
	all2 := r2.List()

	if len(all1) != len(all2) {
		t.Fatalf("restart mismatch: first=%d, second=%d", len(all1), len(all2))
	}
	for i := range all1 {
		if all1[i].ID != all2[i].ID || all1[i].Version != all2[i].Version {
			t.Fatalf("restart mismatch at %d: %s v%d vs %s v%d", i, all1[i].ID, all1[i].Version, all2[i].ID, all2[i].Version)
		}
	}
}

func TestLoadFromDir_EmptyPath(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.json"), []byte(`{"schemaVersion":1,"entries":[{"id":"blueprint:x","version":1,"schemaVersion":1,"path":""}]}`))
	err := r.LoadFromDir(dir)
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("expected path error: %v", err)
	}
}

func TestLoadFromDir_PathEscapes(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.json"), []byte(`{"schemaVersion":1,"entries":[{"id":"blueprint:x","version":1,"schemaVersion":1,"path":"../escape.json"}]}`))
	err := r.LoadFromDir(dir)
	if err == nil || !strings.Contains(err.Error(), "escape") {
		t.Errorf("expected escape error: %v", err)
	}
}

func TestLoadFromDir_DuplicateWithinIndex(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "bp", "1.json"), []byte(`{"schemaVersion":1,"id":"blueprint:x","version":1,"name":"X","source":"user","workflow":{"stages":[{"id":"s1","title":"S1","tasks":[{"id":"t1","title":"T1"}]}]},"blockSpecs":[],"createdAt":"2026-07-20T00:00:00Z"}`))
	writeTestFile(t, filepath.Join(dir, "index.json"), []byte(`{"schemaVersion":1,"entries":[{"id":"blueprint:x","version":1,"schemaVersion":1,"path":"bp/1.json"},{"id":"blueprint:x","version":1,"schemaVersion":1,"path":"bp/1.json"}]}`))
	err := r.LoadFromDir(dir)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate within index error: %v", err)
	}
}

func TestLoadFromDir_FutureSchemaVersion(t *testing.T) {
	r := NewBlueprintRegistry()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.json"), []byte(fmt.Sprintf(`{"schemaVersion":%d,"entries":[]}`, SchemaVersion+1)))
	err := r.LoadFromDir(dir)
	if err == nil || !strings.Contains(err.Error(), "future") {
		t.Errorf("expected future schema error: %v", err)
	}
}

// ── LoadFromDir transactional ─────────────────────────────────────────────

func TestLoadFromDir_TransactionalFailurePreservesRegistry(t *testing.T) {
	r := NewBlueprintRegistry()
	before := r.List()
	if len(before) != 9 {
		t.Fatalf("expected 9 builtins, got %d", len(before))
	}

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "index.json"), []byte("{bad"))
	r.LoadFromDir(dir) // should fail

	after := r.List()
	if len(after) != 9 {
		t.Fatalf("failed load mutated registry: %d → %d", len(before), len(after))
	}
}

// ── Validation: ID / Source ────────────────────────────────────────────────

func TestValidateBlueprint_EmptyID(t *testing.T) {
	bp := validTestBlueprint()
	bp.ID = ""
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "ID") {
		t.Errorf("expected ID error, got: %v", err)
	}
}

func TestValidateBlueprint_BadIDPrefix(t *testing.T) {
	bp := validTestBlueprint()
	bp.ID = "system:bad"
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "blueprint:") {
		t.Errorf("expected prefix error, got: %v", err)
	}
}

func TestValidateBlueprint_BadIDEmptyName(t *testing.T) {
	bp := validTestBlueprint()
	bp.ID = "blueprint:"
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Errorf("expected empty name error, got: %v", err)
	}
}

func TestValidateBlueprint_BadIDInvalidChar(t *testing.T) {
	bp := validTestBlueprint()
	bp.ID = "blueprint:bad name"
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("expected invalid char error, got: %v", err)
	}
}

func TestValidateBlueprint_BadSource(t *testing.T) {
	bp := validTestBlueprint()
	bp.Source = "unknown"
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected unknown source error, got: %v", err)
	}
}

func TestValidateBlueprint_AddonEmptyName(t *testing.T) {
	bp := validTestBlueprint()
	bp.Source = "addon:"
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "missing name") {
		t.Errorf("expected addon empty name error, got: %v", err)
	}
}

func TestValidateBlueprint_BadVersion(t *testing.T) {
	bp := validTestBlueprint()
	bp.Version = 0
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("expected version error, got: %v", err)
	}
}

func TestValidateBlueprint_BadSchemaVersion(t *testing.T) {
	bp := validTestBlueprint()
	bp.SchemaVersion = SchemaVersion + 99
	err := ValidateBlueprint(bp)
	if err == nil {
		t.Fatal("expected future schema error")
	}
}

// ── Validation: InputSchema ────────────────────────────────────────────────

func TestValidateInputSchema_ValidJSONSchema(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	if err := validateInputSchema(raw); err != nil {
		t.Fatalf("valid schema rejected: %v", err)
	}
}

func TestValidateInputSchema_InvalidJSON(t *testing.T) {
	err := validateInputSchema(json.RawMessage(`{bad`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidateInputSchema_NonObjectType(t *testing.T) {
	err := validateInputSchema(json.RawMessage(`{"type":"string"}`))
	if err == nil || !strings.Contains(err.Error(), "object") {
		t.Errorf("expected 'object' type error: %v", err)
	}
}

func TestValidateInputsAgainstSchema_InputsNilMissingRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	err := validateInputsAgainstSchema(schema, nil)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("expected missing required error with nil inputs, got: %v", err)
	}
}

func TestValidateInputsAgainstSchema_MissingRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	inputs := map[string]any{"other": "value"}
	err := validateInputsAgainstSchema(schema, inputs)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("expected missing required error: %v", err)
	}
}

func TestValidateInputsAgainstSchema_IntegerRejectsFloat(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}}}`)
	inputs := map[string]any{"count": 3.14}
	err := validateInputsAgainstSchema(schema, inputs)
	if err == nil || !strings.Contains(err.Error(), "integer") {
		t.Errorf("expected integer/fractional error, got: %v", err)
	}
}

func TestValidateInputsAgainstSchema_IntegerAcceptsInt(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}}}`)
	inputs := map[string]any{"count": float64(42)} // 42.0
	inputs2 := map[string]any{"count": json.Number("7")}
	if err := validateInputsAgainstSchema(schema, inputs); err != nil {
		t.Errorf("integer 42.0 rejected: %v", err)
	}
	if err := validateInputsAgainstSchema(schema, inputs2); err != nil {
		t.Errorf("integer '7' rejected: %v", err)
	}
}

func TestValidateInputsAgainstSchema_Enum(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"color":{"type":"string","enum":["red","green","blue"]}}}`)
	if err := validateInputsAgainstSchema(schema, map[string]any{"color": "red"}); err != nil {
		t.Errorf("valid enum rejected: %v", err)
	}
	if err := validateInputsAgainstSchema(schema, map[string]any{"color": "yellow"}); err == nil {
		t.Fatal("invalid enum accepted")
	}
}

func TestValidateInputsAgainstSchema_AdditionalPropertiesFalse(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`)
	err := validateInputsAgainstSchema(schema, map[string]any{"unknown": "value"})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected additionalProperties error, got: %v", err)
	}
}

func TestValidateInputsAgainstSchema_RecursiveObject(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"config":{"type":"object","properties":{"host":{"type":"string"}},"required":["host"]}}}`)
	if err := validateInputsAgainstSchema(schema, map[string]any{"config": map[string]any{"host": "localhost"}}); err != nil {
		t.Errorf("valid nested object rejected: %v", err)
	}
	if err := validateInputsAgainstSchema(schema, map[string]any{"config": map[string]any{}}); err == nil {
		t.Fatal("missing nested required accepted")
	}
}

// ── Validation: BlockSpec ──────────────────────────────────────────────────

func TestValidateBlockSpecs_UnknownKind(t *testing.T) {
	err := validateBlockSpecs([]BlockSpec{
		{ID: "b1", Kind: "unknown_kind", SchemaVersion: 1, Label: "A", Placement: BlockPlacement{Slot: "primary", Order: 0}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected unknown kind error: %v", err)
	}
}

func TestValidateBlockSpecs_AllCoreKinds(t *testing.T) {
	coreKinds := []string{"item", "list", "checklist", "file_list", "git_status", "action_entry", "key_value", "status", "progress", "timeline", "chart", "table", "graph", "code", "markdown", "artifact", "decision", "approval", "input", "notice"}
	for i, k := range coreKinds {
		specs := []BlockSpec{{ID: fmt.Sprintf("b%d", i), Kind: k, SchemaVersion: 1, Label: "X", Placement: BlockPlacement{Slot: "primary", Order: 0}}}
		if err := validateBlockSpecs(specs); err != nil {
			t.Errorf("core kind %q rejected: %v", k, err)
		}
	}
}

func TestValidateBlockSpecs_DuplicateID(t *testing.T) {
	err := validateBlockSpecs([]BlockSpec{
		{ID: "b1", Kind: "markdown", SchemaVersion: 1, Label: "A", Placement: BlockPlacement{Slot: "primary", Order: 0}},
		{ID: "b1", Kind: "checklist", SchemaVersion: 1, Label: "B", Placement: BlockPlacement{Slot: "secondary", Order: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate blockSpec error: %v", err)
	}
}

func TestValidateBlockSpecs_NegativeOrder(t *testing.T) {
	err := validateBlockSpecs([]BlockSpec{
		{ID: "b1", Kind: "markdown", SchemaVersion: 1, Label: "A", Placement: BlockPlacement{Slot: "primary", Order: -1}},
	})
	if err == nil || !strings.Contains(err.Error(), "order") {
		t.Errorf("expected order error: %v", err)
	}
}

// ── Validation: Global IDs ─────────────────────────────────────────────────

func TestValidateGlobalIDs_StageBlockCollision(t *testing.T) {
	bp := validTestBlueprint()
	bp.Workflow.Stages[0].ID = "b1" // collides with blockSpec ID
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "global") {
		t.Errorf("expected global ID collision error: %v", err)
	}
}

func TestValidateGlobalIDs_TaskBlockCollision(t *testing.T) {
	bp := validTestBlueprint()
	bp.Workflow.Stages[0].Tasks[0].ID = "b1" // collides with blockSpec ID
	err := ValidateBlueprint(bp)
	if err == nil || !strings.Contains(err.Error(), "global") {
		t.Errorf("expected global ID collision error: %v", err)
	}
}

// ── Validation: ToolContract duplicates ────────────────────────────────────

func TestValidateToolContracts_DuplicateKey(t *testing.T) {
	err := validateToolContracts([]ToolContractRef{
		{Name: "test", ContractVersion: 1, SideEffectClass: "read"},
		{Name: "test", ContractVersion: 1, SideEffectClass: "read"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate contract key error: %v", err)
	}
}

func TestValidateToolContracts_EmptyName(t *testing.T) {
	err := validateToolContracts([]ToolContractRef{{Name: "", ContractVersion: 1, SideEffectClass: "read"}})
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("expected name error: %v", err)
	}
}

func TestValidateToolContracts_BadVersion(t *testing.T) {
	err := validateToolContracts([]ToolContractRef{{Name: "test", ContractVersion: 0, SideEffectClass: "read"}})
	if err == nil || !strings.Contains(err.Error(), "contractVersion") {
		t.Errorf("expected contractVersion error: %v", err)
	}
}

func TestValidateToolContracts_InvalidSideEffect(t *testing.T) {
	err := validateToolContracts([]ToolContractRef{{Name: "test", ContractVersion: 1, SideEffectClass: "unsafe"}})
	if err == nil || !strings.Contains(err.Error(), "sideEffectClass") {
		t.Errorf("expected sideEffectClass error: %v", err)
	}
}

func TestValidateToolContracts_MissingSideEffect(t *testing.T) {
	err := validateToolContracts([]ToolContractRef{{Name: "test", ContractVersion: 1}})
	if err == nil || !strings.Contains(err.Error(), "sideEffectClass") {
		t.Errorf("expected sideEffectClass error: %v", err)
	}
}

// ── Secret detection ───────────────────────────────────────────────────────

func TestValidateNoSecrets_InputSchemaDefault(t *testing.T) {
	bp := validTestBlueprint()
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"api_key":{"type":"string","default":"sk-abc123"}}}`)
	err := ValidateNoSecrets(bp)
	if err == nil {
		t.Fatal("expected secret detection in schema default")
	}
}

func TestValidateNoSecrets_BlockDefaultData(t *testing.T) {
	bp := validTestBlueprint()
	bp.BlockSpecs[0].DefaultData = json.RawMessage(`{"token":"ghp_1234567890abcdef"}`)
	err := ValidateNoSecrets(bp)
	if err == nil {
		t.Fatal("expected secret detection in BlockSpec DefaultData")
	}
}

func TestValidateNoSecrets_EmptySecretFieldAllowed(t *testing.T) {
	bp := validTestBlueprint()
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"api_key":{"type":"string"}}}`)
	if err := ValidateNoSecrets(bp); err != nil {
		t.Fatalf("empty secret field name should be allowed: %v", err)
	}
}

func TestValidateNoSecrets_SecretRefAllowed(t *testing.T) {
	// Explicit references like ${VAR}, vault:, {{template}} must be allowed.
	bp := validTestBlueprint()
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"token":{"type":"string","default":"${GITHUB_TOKEN}"}}}`)
	if err := ValidateNoSecrets(bp); err != nil {
		t.Fatalf("secret ref ${GITHUB_TOKEN} should be allowed: %v", err)
	}
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"api_key":{"type":"string","default":"{{.apiKey}}"}}}`)
	if err := ValidateNoSecrets(bp); err != nil {
		t.Fatalf("secret ref {{.apiKey}} should be allowed: %v", err)
	}
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"password":{"type":"string","default":"vault:secret/myapp"}}}`)
	if err := ValidateNoSecrets(bp); err != nil {
		t.Fatalf("secret ref vault:... should be allowed: %v", err)
	}
}

func TestValidateNoSecrets_PromptTemplate(t *testing.T) {
	bp := validTestBlueprint()
	bp.PromptTemplate = "Please use api_key=\"sk-my-key\" for this task"
	err := ValidateNoSecrets(bp)
	if err == nil {
		t.Fatal("expected secret detection in prompt template")
	}
}

func TestValidateNoSecrets_WorkflowTitle(t *testing.T) {
	bp := validTestBlueprint()
	bp.Workflow.Stages[0].Title = "Enter password: hunter2"
	err := ValidateNoSecrets(bp)
	if err == nil {
		t.Fatal("expected secret detection in stage title")
	}
}

func TestValidateNoSecrets_TaskTitle(t *testing.T) {
	bp := validTestBlueprint()
	bp.Workflow.Stages[0].Tasks[0].Title = "Set api_key=abc"
	err := ValidateNoSecrets(bp)
	if err == nil {
		t.Fatal("expected secret detection in task title")
	}
}

func TestValidateNoSecrets_CornerstoneLabel(t *testing.T) {
	bp := validTestBlueprint()
	bp.CornerstoneReqs = []CornerstoneReq{{Type: CornerstoneFileRef, Required: true, Label: "token=ghp_123"}}
	err := ValidateNoSecrets(bp)
	if err == nil {
		t.Fatal("expected secret detection in cornerstone label")
	}
}

func TestValidateNoSecrets_ToolProvider(t *testing.T) {
	bp := validTestBlueprint()
	bp.ToolContracts = []ToolContractRef{{Name: "test", ContractVersion: 1, SideEffectClass: "read", Provider: "password=secret"}}
	err := ValidateNoSecrets(bp)
	if err == nil {
		t.Fatal("expected secret detection in tool provider")
	}
}

func TestValidateNoSecrets_NoFalsePositiveOnNormalWords(t *testing.T) {
	// "passwords" without an assignment should not trigger.
	bp := validTestBlueprint()
	bp.Description = "Manage passwords securely"
	if err := ValidateNoSecrets(bp); err != nil {
		t.Fatalf("normal word 'passwords' should not trigger: %v", err)
	}
	// "token" alone without assignment should not trigger.
	bp.Description = "Token management"
	if err := ValidateNoSecrets(bp); err != nil {
		t.Fatalf("normal word 'token' should not trigger: %v", err)
	}
}

func TestValidateNoSecrets_NestedSecret(t *testing.T) {
	bp := validTestBlueprint()
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"config":{"type":"object","properties":{"secret_token":{"type":"string","default":"abc"}}}}}`)
	err := ValidateNoSecrets(bp)
	if err == nil {
		t.Fatal("expected nested secret detection")
	}
	if !strings.Contains(err.Error(), "secret_token") {
		t.Errorf("error should mention 'secret_token': %v", err)
	}
}

func TestValidateNoSecrets_DescriptionTextIsNotStructuredSecret(t *testing.T) {
	bp := validTestBlueprint()
	bp.Description = "Token: budget unit"
	bp.InputSchema = json.RawMessage(`{
		"type":"object",
		"properties":{"token":{"type":"string","description":"Token: budget unit"}}
	}`)
	if err := ValidateNoSecrets(bp); err != nil {
		t.Fatalf("normal token explanation should be allowed: %v", err)
	}

	bp.InputSchema = json.RawMessage(`{
		"type":"object",
		"properties":{"token":{"type":"string","default":"Token: budget unit"}}
	}`)
	if err := ValidateNoSecrets(bp); err == nil || !strings.Contains(err.Error(), "inputSchema.properties.token.default") {
		t.Fatalf("structured token default must still be rejected, got %v", err)
	}
}

func TestValidateNoSecrets_EmbeddedCredentialLiterals(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "authorization header", text: "Request header: Authorization: Bearer opaque-credential-value"},
		{name: "github token", text: "A leaked URL contains ?auth=ghp_0123456789abcdefghijklmnopqrstuv; remove it"},
		{name: "private key", text: "Unexpected material: -----BEGIN PRIVATE KEY----- redacted"},
		{name: "literal after explanation", text: "Token: budget unit; password: hunter2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bp := validTestBlueprint()
			bp.Description = tt.text
			if err := ValidateNoSecrets(bp); err == nil {
				t.Fatalf("embedded credential %q was not rejected", tt.text)
			}
		})
	}
}

func TestValidateNoSecrets_FreeTextReferencesAllowed(t *testing.T) {
	bp := validTestBlueprint()
	bp.Description = "Token: budget unit. Send Authorization: Bearer ${GITHUB_TOKEN}."
	if err := ValidateNoSecrets(bp); err != nil {
		t.Fatalf("normal explanation and authorization reference should be allowed: %v", err)
	}
}

// ── CreateDefinitionSnapshot ───────────────────────────────────────────────

func TestCreateDefinitionSnapshot_Basic(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")

	snap, err := CreateDefinitionSnapshot(bp, nil)
	if err != nil {
		t.Fatalf("CreateDefinitionSnapshot: %v", err)
	}
	if snap.Revision != 1 {
		t.Errorf("revision = %d, want 1", snap.Revision)
	}
	if snap.Digest == "" {
		t.Fatal("digest is empty")
	}
	if snap.BlueprintRef.ID != "blueprint:blank" {
		t.Errorf("blueprintRef ID = %s", snap.BlueprintRef.ID)
	}
}

func TestCreateDefinitionSnapshot_WithInputs(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:info-organize")

	inputs := map[string]any{"topic": "test topic"}
	snap, err := CreateDefinitionSnapshot(bp, inputs)
	if err != nil {
		t.Fatalf("CreateDefinitionSnapshot with valid inputs: %v", err)
	}
	if snap.Revision != 1 {
		t.Errorf("revision = %d, want 1", snap.Revision)
	}
}

func TestCreateDefinitionSnapshot_InputsMissingRequired(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:info-organize")

	_, err := CreateDefinitionSnapshot(bp, map[string]any{"wrong": "value"})
	if err == nil {
		t.Fatal("expected error for missing required input 'topic'")
	}
	if !strings.Contains(err.Error(), "topic") {
		t.Errorf("error should mention 'topic': %v", err)
	}
}

func TestCreateDefinitionSnapshot_InputsNilWithSchema(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:info-organize")
	// blueprint:info-organize has required "topic", nil inputs should fail.
	_, err := CreateDefinitionSnapshot(bp, nil)
	if err == nil {
		t.Fatal("expected error for nil inputs with required field")
	}
}

func TestCreateDefinitionSnapshot_NilBlueprint(t *testing.T) {
	_, err := CreateDefinitionSnapshot(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil blueprint")
	}
}

func TestCreateDefinitionSnapshot_DeepCopy(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)
	bp.Name = "CHANGED"
	if snap.PromptTemplate == "CHANGED" {
		t.Fatal("snapshot mutated via original blueprint")
	}
}

func TestCreateDefinitionSnapshot_DigestStability(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	s1, _ := CreateDefinitionSnapshot(bp, nil)
	s2, _ := CreateDefinitionSnapshot(bp, nil)
	if s1.Digest != s2.Digest {
		t.Fatalf("digest mismatch: %s vs %s", s1.Digest, s2.Digest)
	}
}

// ── CreateDefinitionSnapshotWithTools ──────────────────────────────────────

func TestCreateDefinitionSnapshotWithTools_RequiredMissingCatalog(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:code-review")
	_, err := CreateDefinitionSnapshot(bp, map[string]any{"target": "main.go"})
	if err == nil {
		t.Fatal("expected error — code-review has required ToolContracts but no catalog")
	}
	if !strings.Contains(err.Error(), "ToolCatalog") {
		t.Errorf("error should mention ToolCatalog: %v", err)
	}
}

func TestCreateDefinitionSnapshotWithTools_ResolveSuccess(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:code-review")
	cat := &fakeToolCatalog{tools: map[string]ToolCapability{
		"read_file": availableTool(),
		"grep":      availableTool(),
	}}
	snap, err := CreateDefinitionSnapshotWithTools(context.Background(), bp, map[string]any{"target": "main.go"}, cat)
	if err != nil {
		t.Fatalf("CreateDefinitionSnapshotWithTools: %v", err)
	}
	if snap.Revision != 1 {
		t.Errorf("revision = %d, want 1", snap.Revision)
	}
}

func TestCreateDefinitionSnapshotWithTools_RequiredUnavailable(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:code-review")
	cat := &fakeToolCatalog{tools: map[string]ToolCapability{
		"read_file": {Available: false, Compatible: false, Reason: "not installed"},
		"grep":      availableTool(),
	}}
	_, err := CreateDefinitionSnapshotWithTools(context.Background(), bp, map[string]any{"target": "main.go"}, cat)
	if err == nil {
		t.Fatal("expected error for unavailable required tool")
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Errorf("error should mention 'read_file': %v", err)
	}
}

func TestCreateDefinitionSnapshotWithTools_OptionalMissingOK(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:code-review")
	cat := &fakeToolCatalog{tools: map[string]ToolCapability{
		"read_file": availableTool(),
	}}
	// grep is optional — missing should be fine.
	_, err := CreateDefinitionSnapshotWithTools(context.Background(), bp, map[string]any{"target": "main.go"}, cat)
	if err != nil {
		t.Fatalf("optional tool missing should not fail: %v", err)
	}
}

func TestCreateDefinitionSnapshotWithTools_ResolveError(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:code-review")
	cat := &fakeToolCatalog{
		tools: map[string]ToolCapability{"grep": availableTool()},
		err:   map[string]error{"read_file": fmt.Errorf("network error")},
	}
	_, err := CreateDefinitionSnapshotWithTools(context.Background(), bp, map[string]any{"target": "main.go"}, cat)
	if err == nil {
		t.Fatal("expected error for resolve failure on required tool")
	}
	if !strings.Contains(err.Error(), "read_file") || !strings.Contains(err.Error(), "network error") {
		t.Errorf("error should mention tool name and cause: %v", err)
	}
}

func TestCreateDefinitionSnapshotWithTools_NilCatalog(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:code-review")
	_, err := CreateDefinitionSnapshotWithTools(context.Background(), bp, map[string]any{"target": "main.go"}, nil)
	if err == nil {
		t.Fatal("expected error for nil catalog")
	}
}

// ── EditDefinitionSnapshot (COW) ───────────────────────────────────────────

func TestEditDefinitionSnapshot_Basic(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)

	newPrompt := "Updated prompt"
	next, err := EditDefinitionSnapshot(snap, DefinitionEdits{PromptTemplate: &newPrompt})
	if err != nil {
		t.Fatalf("EditDefinitionSnapshot: %v", err)
	}
	if next.Revision != snap.Revision+1 {
		t.Errorf("revision = %d, want %d", next.Revision, snap.Revision+1)
	}
	if next.PromptTemplate != newPrompt {
		t.Errorf("PromptTemplate = %q", next.PromptTemplate)
	}
	if next.Digest == snap.Digest {
		t.Fatal("digest should change after edit")
	}
}

func TestEditDefinitionSnapshot_PrevNotMutated(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)

	origDigest := snap.Digest
	origRev := snap.Revision

	newPrompt := "Changed"
	EditDefinitionSnapshot(snap, DefinitionEdits{PromptTemplate: &newPrompt})

	if snap.Digest != origDigest {
		t.Fatal("original snapshot digest mutated")
	}
	if snap.Revision != origRev {
		t.Fatal("original snapshot revision mutated")
	}
}

func TestEditDefinitionSnapshot_ExpectedRevisionGuard(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)

	_, err := EditDefinitionSnapshot(snap, DefinitionEdits{ExpectedRevision: 99, PromptTemplate: strPtr("X")})
	if err == nil || !strings.Contains(err.Error(), "revision") {
		t.Errorf("expected revision conflict error: %v", err)
	}
}

func TestEditDefinitionSnapshot_EmptyPatch(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)

	_, err := EditDefinitionSnapshot(snap, DefinitionEdits{})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected empty patch error: %v", err)
	}
}

func TestEditDefinitionSnapshot_NilPrev(t *testing.T) {
	_, err := EditDefinitionSnapshot(nil, DefinitionEdits{PromptTemplate: strPtr("x")})
	if err == nil {
		t.Fatal("expected error for nil snapshot")
	}
}

func TestEditDefinitionSnapshot_InvalidPrevSchemaVersion(t *testing.T) {
	snap := &WorkDefinitionSnapshot{SchemaVersion: 0, Revision: 1, BlueprintRef: BlueprintRef{ID: "blueprint:x", SchemaVersion: 1, Version: 1}, Workflow: WorkflowDef{Stages: []StageSpec{{ID: "s1", Title: "S1", Tasks: []TaskSpec{{ID: "t1", Title: "T1"}}}}}, BlockSpecs: []BlockSpec{}, Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
	norm, _ := NormalizeDefinitionSnapshot(snap)
	_, err := EditDefinitionSnapshot(norm, DefinitionEdits{PromptTemplate: strPtr("x")})
	if err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Errorf("expected schemaVersion error: %v", err)
	}
}

func TestEditDefinitionSnapshot_InvalidPrevDigest(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)
	snap.Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	_, err := EditDefinitionSnapshot(snap, DefinitionEdits{PromptTemplate: strPtr("x")})
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("expected digest mismatch error: %v", err)
	}
}

func TestEditDefinitionSnapshot_RevisionChain(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)
	for i := int64(2); i <= 5; i++ {
		p := fmt.Sprintf("prompt-v%d", i)
		next, err := EditDefinitionSnapshot(snap, DefinitionEdits{ExpectedRevision: i - 1, PromptTemplate: &p})
		if err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
		if next.Revision != i {
			t.Errorf("revision = %d, want %d", next.Revision, i)
		}
		snap = next
	}
	if snap.Revision != 5 {
		t.Errorf("final revision = %d, want 5", snap.Revision)
	}
}

func TestEditDefinitionSnapshot_WorkflowEdit(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)

	newWF := WorkflowDef{Stages: []StageSpec{{ID: "newstage", Title: "New Stage", Tasks: []TaskSpec{{ID: "t1", Title: "Task 1"}}}}}
	next, err := EditDefinitionSnapshot(snap, DefinitionEdits{Workflow: &newWF})
	if err != nil {
		t.Fatalf("EditDefinitionSnapshot workflow: %v", err)
	}
	if len(next.Workflow.Stages) != 1 || next.Workflow.Stages[0].ID != "newstage" {
		t.Fatal("workflow edit not applied")
	}
}

func TestEditDefinitionSnapshot_CannotInjectSecret(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, _ := CreateDefinitionSnapshot(bp, nil)

	secretSchema := json.RawMessage(`{"type":"object","properties":{"password":{"type":"string","default":"hunter2"}}}`)
	_, err := EditDefinitionSnapshot(snap, DefinitionEdits{InputSchema: &secretSchema})
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("expected secret detection error: %v", err)
	}
}

// ── Builtin ID stability ───────────────────────────────────────────────────

func TestBuiltinIDs_Stable(t *testing.T) {
	expected := map[string]string{
		"blueprint:blank":         "空白工作",
		"blueprint:info-organize": "资料整理",
		"blueprint:code-review":   "代码审查",
		"blueprint:report":        "报告生成",
	}
	r := NewBlueprintRegistry()
	for id, wantName := range expected {
		bp, err := r.LookupLatest(id)
		if err != nil {
			t.Errorf("builtin %s missing: %v", id, err)
			continue
		}
		if bp.Name != wantName {
			t.Errorf("builtin %s name = %q, want %q", id, bp.Name, wantName)
		}
	}
}

// ── Review regressions ─────────────────────────────────────────────────────

func TestLoadFromDir_ReloadRefreshesAndRemovesDiskEntries(t *testing.T) {
	dir := copyBlueprintFixture(t)
	r := NewBlueprintRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}

	v1Path := filepath.Join(dir, "my-review", "1.json")
	var v1 WorkBlueprint
	if err := json.Unmarshal(readFixtureBytes(t, v1Path), &v1); err != nil {
		t.Fatal(err)
	}
	v1.Name = "磁盘刷新后的名称"
	writeJSON(t, v1Path, v1)
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("reload updated definition: %v", err)
	}
	got, err := r.LookupExact(v1.ID, v1.Version)
	if err != nil || got.Name != v1.Name {
		t.Fatalf("disk refresh not visible: got=%v err=%v", got, err)
	}

	var index blueprintIndex
	indexPath := filepath.Join(dir, "index.json")
	if err := json.Unmarshal(readFixtureBytes(t, indexPath), &index); err != nil {
		t.Fatal(err)
	}
	index.Entries = index.Entries[1:]
	writeJSON(t, indexPath, index)
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("reload removed definition: %v", err)
	}
	if _, err := r.LookupExact(v1.ID, v1.Version); err == nil {
		t.Fatal("definition removed from index remained registered")
	}
}

func TestLoadFromDir_RejectsNonObjectIndexWithoutMutation(t *testing.T) {
	dir := copyBlueprintFixture(t)
	r := NewBlueprintRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}
	before := r.List()
	indexPath := filepath.Join(dir, "index.json")

	tests := []struct {
		name string
		data string
	}{
		{name: "null", data: `null`},
		{name: "array", data: `[]`},
		{name: "string", data: `"index"`},
		{name: "number", data: `1`},
		{name: "boolean", data: `true`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTestFile(t, indexPath, []byte(tt.data))
			err := r.LoadFromDir(dir)
			if err == nil || !strings.Contains(err.Error(), "JSON object") {
				t.Fatalf("expected explicit object-shape error, got %v", err)
			}
			if after := r.List(); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed reload mutated registry\nbefore: %#v\nafter:  %#v", before, after)
			}
		})
	}
}

func TestLoadFromDir_ManualConflictIsTransactional(t *testing.T) {
	dir := copyBlueprintFixture(t)
	r := NewBlueprintRegistry()
	manual := validTestBlueprint()
	manual.ID = "blueprint:my-review"
	if err := r.Register(manual); err != nil {
		t.Fatal(err)
	}
	before := len(r.List())
	if err := r.LoadFromDir(dir); err == nil {
		t.Fatal("expected disk/manual duplicate version conflict")
	}
	if got := len(r.List()); got != before {
		t.Fatalf("failed load mutated registry: got %d entries, want %d", got, before)
	}
	if _, err := r.LookupExact("blueprint:my-review", 2); err == nil {
		t.Fatal("failed transactional load leaked version 2")
	}
}

func TestValidateBlueprint_ZeroSchemaAndBadAddonName(t *testing.T) {
	bp := validTestBlueprint()
	bp.SchemaVersion = 0
	if err := ValidateBlueprint(bp); err == nil {
		t.Fatal("expected zero schemaVersion error")
	}
	bp = validTestBlueprint()
	bp.Source = "addon:bad name"
	if err := ValidateBlueprint(bp); err == nil {
		t.Fatal("expected invalid addon source name error")
	}
}

func TestBlueprintRegistry_LookupRefChecksBothVersions(t *testing.T) {
	r := NewBlueprintRegistry()
	ref := BlueprintRef{ID: "blueprint:blank", SchemaVersion: SchemaVersion, Version: 1}
	if _, err := r.LookupRef(ref); err != nil {
		t.Fatal(err)
	}
	ref.SchemaVersion++
	if _, err := r.LookupRef(ref); err == nil || !strings.Contains(err.Error(), "schemaVersion mismatch") {
		t.Fatalf("expected schema mismatch, got %v", err)
	}
}

func TestValidateBlockSpecs_FutureSchemaAndDefaultShape(t *testing.T) {
	base := BlockSpec{ID: "b1", Kind: "markdown", SchemaVersion: SchemaVersion + 1, Label: "B", Placement: BlockPlacement{Slot: "primary"}}
	if err := validateBlockSpecs([]BlockSpec{base}); err == nil {
		t.Fatal("expected future block schema error")
	}
	base.SchemaVersion = SchemaVersion
	base.DefaultData = json.RawMessage(`[]`)
	if err := validateBlockSpecs([]BlockSpec{base}); err == nil {
		t.Fatal("expected non-object defaultData error")
	}
	base.DefaultData = json.RawMessage(`{}`)
	base.Placement.BlockID = "other"
	if err := validateBlockSpecs([]BlockSpec{base}); err == nil {
		t.Fatal("expected placement block ID mismatch")
	}
}

func TestValidateInputsAgainstSchema_ArrayItemsAndNestedAdditionalProperties(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"items":{"type":"array","items":{"type":"integer"}},
			"options":{"type":"object","properties":{"enabled":{"type":"boolean"}},"additionalProperties":false}
		},
		"required":["items","options"]
	}`)
	if err := validateInputSchema(schema); err != nil {
		t.Fatal(err)
	}
	if err := validateInputsAgainstSchema(schema, map[string]any{
		"items": []any{1, 2.5}, "options": map[string]any{"enabled": true},
	}); err == nil || !strings.Contains(err.Error(), "items[1]") {
		t.Fatalf("expected array item path error, got %v", err)
	}
	if err := validateInputsAgainstSchema(schema, map[string]any{
		"items": []int{1, 2}, "options": map[string]any{"enabled": true, "extra": true},
	}); err == nil || !strings.Contains(err.Error(), "options.extra") {
		t.Fatalf("expected nested additional property error, got %v", err)
	}
}

func TestValidateInputSchema_RejectsMalformedNestedSchema(t *testing.T) {
	badSchemas := []json.RawMessage{
		json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`),
		json.RawMessage(`{"type":"object","properties":{"child":{"type":"mystery"}}}`),
		json.RawMessage(`{"type":"object","properties":{"child":"not-an-object"}}`),
	}
	for _, schema := range badSchemas {
		if err := validateInputSchema(schema); err == nil {
			t.Fatalf("expected invalid nested schema error for %s", schema)
		}
	}
}

func TestValidateNoSecrets_ReferenceMustCoverWholeValue(t *testing.T) {
	bp := validTestBlueprint()
	bp.PromptTemplate = "token=plaintext-before-vault:ref"
	if err := ValidateNoSecrets(bp); err == nil {
		t.Fatal("plaintext containing a reference marker bypassed secret validation")
	}
	bp = validTestBlueprint()
	bp.ArtifactKinds = []string{"token=plaintext"}
	if err := ValidateNoSecrets(bp); err == nil || !strings.Contains(err.Error(), "artifactKinds") {
		t.Fatalf("expected artifact kind secret error, got %v", err)
	}
}

func TestCreateDefinitionSnapshotWithTools_PreservesContext(t *testing.T) {
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "req-1")
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:code-review")
	cat := &fakeToolCatalog{tools: map[string]ToolCapability{
		"read_file": availableTool(),
		"grep":      availableTool(),
	}}
	if _, err := CreateDefinitionSnapshotWithTools(ctx, bp, map[string]any{"target": "main.go"}, cat); err != nil {
		t.Fatal(err)
	}
	if cat.lastCtx == nil || cat.lastCtx.Value(contextKey("request")) != "req-1" {
		t.Fatal("ToolCatalog did not receive caller context")
	}
}

func TestEditDefinitionSnapshot_BlueprintSchemaMismatch(t *testing.T) {
	r := NewBlueprintRegistry()
	bp, _ := r.LookupLatest("blueprint:blank")
	snap, err := CreateDefinitionSnapshot(bp, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap.BlueprintRef.SchemaVersion++
	snap.Digest, err = ComputeDigest(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EditDefinitionSnapshot(snap, DefinitionEdits{PromptTemplate: strPtr("changed")}); err == nil {
		t.Fatal("expected snapshot/blueprint schema mismatch error")
	}
}

// ── Concurrency ────────────────────────────────────────────────────────────

func TestBlueprintRegistry_ConcurrentReads(t *testing.T) {
	r := NewBlueprintRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bp, err := r.LookupLatest("blueprint:blank")
			if err != nil || bp == nil {
				t.Errorf("concurrent lookup failed: %v", err)
			}
			r.List()
			r.ListBySource(BlueprintSystem)
		}()
	}
	wg.Wait()
}

func TestBlueprintRegistry_ConcurrentRegisterAndRead(t *testing.T) {
	r := NewBlueprintRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bp := validTestBlueprint()
			bp.ID = fmt.Sprintf("blueprint:concurrent-%d", idx)
			bp.Version = 1
			_ = r.Register(bp) // some will succeed, dupes will fail — that's fine
		}(i)
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.List()
			r.LookupLatest("blueprint:blank")
		}()
	}
	wg.Wait()
}

// ── Helpers ────────────────────────────────────────────────────────────────

func validTestBlueprint() *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion: SchemaVersion,
		ID:            "blueprint:test-bp",
		Version:       1,
		Name:          "Test Blueprint",
		Source:        BlueprintUser,
		Workflow: WorkflowDef{
			Stages: []StageSpec{
				{ID: "s1", Title: "Stage 1", Tasks: []TaskSpec{{ID: "t1", Title: "Task 1"}}},
			},
		},
		BlockSpecs: []BlockSpec{
			{ID: "b1", Kind: "markdown", SchemaVersion: 1, Label: "Block 1", Placement: BlockPlacement{Slot: "primary", Order: 0}},
		},
		CreatedAt: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
	}
}

func readFixtureBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func copyBlueprintFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	paths := []string{"index.json", filepath.Join("my-review", "1.json"), filepath.Join("my-review", "v2", "2.json")}
	for _, rel := range paths {
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, readFixtureBytes(t, filepath.Join("testdata", "blueprints", rel)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func strPtr(s string) *string { return &s }
