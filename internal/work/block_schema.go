package work

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// BlockDataValidator validates one kind-specific data contract version.
// Implementations must be deterministic and side-effect free.
type BlockDataValidator func(json.RawMessage) error

type blockMigrationKey struct {
	kind string
	from int
}

// BlockSchemaRegistry owns the writable schema versions and adjacent migration
// steps for every Block kind. Work/Event schema versions are intentionally not
// reused here: a Work V2 projection may still contain a markdown schema v1
// Block, and each kind evolves independently.
type BlockSchemaRegistry struct {
	mu         sync.RWMutex
	validators map[string]map[int]BlockDataValidator
	migrations map[blockMigrationKey]BlockMigration
}

// NewBlockSchemaRegistry returns the production core registry. All existing
// V1 contracts remain available; file_list v2 is the first evolved contract
// and renames the presentation-only "desc" field to "description".
func NewBlockSchemaRegistry() *BlockSchemaRegistry {
	r := &BlockSchemaRegistry{
		validators: make(map[string]map[int]BlockDataValidator),
		migrations: make(map[blockMigrationKey]BlockMigration),
	}
	for kind := range coreBlockKinds {
		if err := r.Register(kind, 1, validateCoreBlockSchema(kind, 1)); err != nil {
			panic(err)
		}
	}
	if err := r.Register("file_list", 2, validateCoreBlockSchema("file_list", 2)); err != nil {
		panic(err)
	}
	if err := r.RegisterMigration(BlockMigration{
		Kind:        "file_list",
		FromVersion: 1,
		ToVersion:   2,
		Migrate:     migrateFileListV1ToV2,
	}); err != nil {
		panic(err)
	}
	return r
}

// Register adds one kind/version validator. Duplicate registrations fail
// explicitly so a hot reload cannot silently replace an active contract.
func (r *BlockSchemaRegistry) Register(kind string, version int, validate BlockDataValidator) error {
	if r == nil {
		return errors.New("work: block schema registry is nil")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" || version <= 0 || validate == nil {
		return errors.New("work: block schema register requires kind, positive version, and validator")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.validators == nil {
		r.validators = make(map[string]map[int]BlockDataValidator)
	}
	if r.validators[kind] == nil {
		r.validators[kind] = make(map[int]BlockDataValidator)
	}
	if _, exists := r.validators[kind][version]; exists {
		return fmt.Errorf("work: block schema %s v%d is already registered", kind, version)
	}
	r.validators[kind][version] = validate
	return nil
}

// RegisterMigration adds one adjacent, forward-only migration. Source and
// target validators must already exist so every step has a checked boundary.
func (r *BlockSchemaRegistry) RegisterMigration(migration BlockMigration) error {
	if r == nil {
		return errors.New("work: block schema registry is nil")
	}
	migration.Kind = strings.TrimSpace(migration.Kind)
	if migration.Kind == "" || migration.FromVersion <= 0 ||
		migration.ToVersion != migration.FromVersion+1 || migration.Migrate == nil {
		return errors.New("work: block migration requires kind, adjacent positive versions, and migrate function")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.validators[migration.Kind]
	if versions == nil || versions[migration.FromVersion] == nil || versions[migration.ToVersion] == nil {
		return fmt.Errorf("work: block migration %s v%d→v%d requires registered source and target schemas",
			migration.Kind, migration.FromVersion, migration.ToVersion)
	}
	if r.migrations == nil {
		r.migrations = make(map[blockMigrationKey]BlockMigration)
	}
	key := blockMigrationKey{kind: migration.Kind, from: migration.FromVersion}
	if _, exists := r.migrations[key]; exists {
		return fmt.Errorf("work: block migration %s v%d→v%d is already registered",
			migration.Kind, migration.FromVersion, migration.ToVersion)
	}
	r.migrations[key] = migration
	return nil
}

// Supports reports whether the binary can validate and write a kind/version.
func (r *BlockSchemaRegistry) Supports(kind string, version int) bool {
	if r == nil || version <= 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.validators[strings.TrimSpace(kind)][version] != nil
}

// LatestVersion returns the highest writable schema for kind.
func (r *BlockSchemaRegistry) LatestVersion(kind string) (int, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions := r.validators[strings.TrimSpace(kind)]
	latest := 0
	for version := range versions {
		if version > latest {
			latest = version
		}
	}
	return latest, latest > 0
}

// Validate checks one complete JSON payload against its kind/version contract.
func (r *BlockSchemaRegistry) Validate(kind string, version int, data json.RawMessage) error {
	if r == nil {
		return errors.New("work: block schema registry is unavailable")
	}
	kind = strings.TrimSpace(kind)
	if err := checkBlockSchemaSupport(r, kind, version); err != nil {
		return err
	}
	r.mu.RLock()
	validate := r.validators[kind][version]
	r.mu.RUnlock()
	if err := validate(append(json.RawMessage(nil), data...)); err != nil {
		return fmt.Errorf("work: block %s schema v%d: %w", kind, version, err)
	}
	return nil
}

// Migrate applies every adjacent step and validates each output. It never
// mutates the input bytes and returns the traversed schema versions.
func (r *BlockSchemaRegistry) Migrate(kind string, from, to int, data json.RawMessage) (json.RawMessage, []int, error) {
	if r == nil {
		return nil, nil, errors.New("work: block schema registry is unavailable")
	}
	kind = strings.TrimSpace(kind)
	if from <= 0 || to <= 0 {
		return nil, nil, errors.New("work: block migration versions must be positive")
	}
	if to < from {
		return nil, nil, fmt.Errorf("work: block schema downgrade %s v%d→v%d is not supported", kind, from, to)
	}
	if err := checkBlockSchemaSupport(r, kind, to); err != nil {
		return nil, nil, err
	}
	current := append(json.RawMessage(nil), data...)
	if err := r.Validate(kind, from, current); err != nil {
		return nil, nil, fmt.Errorf("validate migration source: %w", err)
	}
	path := []int{from}
	for version := from; version < to; version++ {
		r.mu.RLock()
		step, ok := r.migrations[blockMigrationKey{kind: kind, from: version}]
		r.mu.RUnlock()
		if !ok {
			return nil, path, fmt.Errorf("work: no block migration for %s v%d→v%d", kind, version, version+1)
		}
		next, err := step.Migrate(append(json.RawMessage(nil), current...))
		if err != nil {
			return nil, path, fmt.Errorf("work: migrate block %s v%d→v%d: %w", kind, version, version+1, err)
		}
		if err := r.Validate(kind, version+1, next); err != nil {
			return nil, path, fmt.Errorf("validate migration result: %w", err)
		}
		current = append(json.RawMessage(nil), next...)
		path = append(path, version+1)
	}
	return current, path, nil
}

func checkBlockSchemaSupport(registry *BlockSchemaRegistry, kind string, version int) error {
	if registry == nil {
		return errors.New("work: block schema registry is unavailable")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return errors.New("work: block kind is required")
	}
	if version <= 0 {
		return fmt.Errorf("work: block %s schema version must be positive, got %d", kind, version)
	}
	latest, ok := registry.LatestVersion(kind)
	if !ok {
		return fmt.Errorf("work: unsupported block kind %q", kind)
	}
	if registry.Supports(kind, version) {
		return nil
	}
	if version > latest {
		return &ErrFutureBlockSchema{Kind: kind, Got: version, CurrentMax: latest}
	}
	return fmt.Errorf("work: unsupported block %s schema version %d", kind, version)
}

// Versions returns a deterministic capability list for diagnostics/tests.
func (r *BlockSchemaRegistry) Versions(kind string) []int {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []int
	for version := range r.validators[strings.TrimSpace(kind)] {
		result = append(result, version)
	}
	sort.Ints(result)
	return result
}

// ErrFutureBlockSchema keeps future kind schemas readable while refusing
// mutation. CurrentMax is specific to the kind, not the Work envelope.
type ErrFutureBlockSchema struct {
	Kind       string
	Got        int
	CurrentMax int
}

func (e *ErrFutureBlockSchema) Error() string {
	if e == nil {
		return "work: unsupported block schema"
	}
	return fmt.Sprintf("work: block %s schema version %d exceeds current max %d; read-only access is required",
		e.Kind, e.Got, e.CurrentMax)
}

// Unwrap preserves the established future-schema error contract for callers
// that handle every Work schema family through ErrFutureSchema.
func (e *ErrFutureBlockSchema) Unwrap() error {
	if e == nil {
		return nil
	}
	return &ErrFutureSchema{Kind: "BlockInstance:" + e.Kind, Got: e.Got, CurrentMax: e.CurrentMax}
}

func validateCoreBlockSchema(kind string, version int) BlockDataValidator {
	return func(raw json.RawMessage) error {
		if len(bytes.TrimSpace(raw)) == 0 {
			return errors.New("data is required")
		}
		var data map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&data); err != nil || data == nil {
			return errors.New("data must be a JSON object")
		}
		if err := validateCoreBlockData(kind, data); err != nil {
			return err
		}
		if kind == "file_list" {
			return validateFileListVersion(data, version)
		}
		return nil
	}
}

func validateFileListVersion(data map[string]any, version int) error {
	files, _ := data["files"].([]any)
	for index, value := range files {
		file, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("files[%d] must be an object", index)
		}
		if _, ok := file["path"].(string); !ok {
			return fmt.Errorf("files[%d].path must be a string", index)
		}
		if _, ok := file["status"].(string); !ok {
			return fmt.Errorf("files[%d].status must be a string", index)
		}
		switch version {
		case 1:
			if _, exists := file["description"]; exists {
				return fmt.Errorf("files[%d].description belongs to schema v2", index)
			}
			if desc, exists := file["desc"]; exists {
				if _, ok := desc.(string); !ok {
					return fmt.Errorf("files[%d].desc must be a string", index)
				}
			}
		case 2:
			if _, exists := file["desc"]; exists {
				return fmt.Errorf("files[%d].desc was replaced by description in schema v2", index)
			}
			if description, exists := file["description"]; exists {
				if _, ok := description.(string); !ok {
					return fmt.Errorf("files[%d].description must be a string", index)
				}
			}
		}
	}
	return nil
}

func migrateFileListV1ToV2(raw json.RawMessage) (json.RawMessage, error) {
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil || data == nil {
		return nil, errors.New("file_list v1 data must be an object")
	}
	files, ok := data["files"].([]any)
	if !ok {
		return nil, errors.New("file_list v1 data requires files")
	}
	for index, value := range files {
		file, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("files[%d] must be an object", index)
		}
		desc, hasDesc := file["desc"]
		description, hasDescription := file["description"]
		if hasDesc && hasDescription && desc != description {
			return nil, fmt.Errorf("files[%d] contains conflicting desc and description", index)
		}
		if hasDesc && !hasDescription {
			file["description"] = desc
		}
		delete(file, "desc")
	}
	return json.Marshal(data)
}
