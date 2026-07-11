// Package addonhost provides the host-side APIs that AddOn runtimes can call.
// It implements the four Phase 2 capabilities:
//
//   - Storage: namespace-scoped key-value store with etag/revision
//   - Secrets: credential management with redaction
//   - Tasks: long-running task lifecycle tracking
//   - Events: typed event emission for UI refresh
//
// Each Host instance is scoped to a single AddOn namespace declared in the
// AddOn manifest.  A builtin AddOn receives a Host directly; an MCP AddOn
// accesses these capabilities through MCP protocol extensions that the host
// translates into calls on a shared Host instance.
package addonhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── Core types ──────────────────────────────────────────────────────────────

// HostContext carries the immutable AddOn metadata the host needs.
type HostContext struct {
	AddOnName    string `json:"addonName"`
	AddOnVersion string `json:"addonVersion"`
	HostVersion  string `json:"hostVersion"`
	Home         string `json:"-"` // WorkGround2 home directory root
	Namespace    string `json:"-"` // declared storage namespace
	ConfigDir    string `json:"configDir"`
	DataDir      string `json:"dataDir"`
	StateDir     string `json:"stateDir"`
	Locale       string `json:"locale"`
}

// Host is the per-AddOn gateway to host-side capabilities.
type Host struct {
	Ctx          HostContext
	secretLookup func(ref string) (string, error)

	mu      sync.Mutex
	taskSeq int64
	tasks   map[string]*Task
	events  Emitter

	// Optional Phase 5 capabilities.
	llm    *LLMCapability
	tools  ToolRegistry
	skills SkillRunner
}

// Emitter receives typed events from AddOns and forwards them to consumers.
type Emitter interface {
	Emit(event EventPayload)
}

// EventPayload is a single typed event from an AddOn.
type EventPayload struct {
	Kind    string         `json:"kind"`  // "addon.records.changed" etc.
	AddOn   string         `json:"addon"` // AddOn name
	Adapter string         `json:"adapter,omitempty"`
	TaskID  string         `json:"taskId,omitempty"`
	Extra   map[string]any `json:"extra,omitempty"`
}

// New creates a Host for the given AddOn context.
func New(ctx HostContext, secretLookup func(ref string) (string, error), events Emitter) *Host {
	ctx.Home = filepath.Clean(ctx.Home)
	ctx.ConfigDir = filepath.Join(ctx.Home, "addons", ctx.Namespace)
	ctx.DataDir = filepath.Join(ctx.ConfigDir, "data")
	ctx.StateDir = filepath.Join(ctx.ConfigDir, "state")
	return &Host{
		Ctx:          ctx,
		secretLookup: secretLookup,
		tasks:        make(map[string]*Task),
		events:       events,
	}
}

// SetLLMCapability enables LLM completion/stream for this AddOn.
func (h *Host) SetLLMCapability(cap *LLMCapability) {
	h.llm = cap
}

// SetToolRegistry enables tool access for this AddOn.
func (h *Host) SetToolRegistry(reg ToolRegistry) {
	h.tools = reg
}

// SetSkillRunner enables skill invocation for this AddOn.
func (h *Host) SetSkillRunner(runner SkillRunner) {
	h.skills = runner
}

func (h *Host) emit(kind string, adapter string, taskID string, extra map[string]any) {
	if h.events == nil {
		return
	}
	h.events.Emit(EventPayload{
		Kind:    kind,
		AddOn:   h.Ctx.AddOnName,
		Adapter: adapter,
		TaskID:  taskID,
		Extra:   extra,
	})
}

// ── Storage helpers ─────────────────────────────────────────────────────────

func (h *Host) storageDir() string {
	return h.Ctx.ConfigDir
}

func (h *Host) storageKeyPath(key string) string {
	// Sanitise key so it can't escape the namespace directory.
	safe := safeStorageKey(key)
	return filepath.Join(h.storageDir(), safe+".json")
}

func safeStorageKey(key string) string {
	// Allow a-zA-Z0-9._- only; replace everything else with _.
	var b strings.Builder
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.TrimRight(b.String(), "._-")
}

// ── Errors ──────────────────────────────────────────────────────────────────

var (
	ErrNotFound   = errors.New("addonhost: key not found")
	ErrConflict   = errors.New("addonhost: etag conflict")
	ErrBadRequest = errors.New("addonhost: bad request")
)

// ── Etag utilities ──────────────────────────────────────────────────────────

type etagEntry struct {
	Value any    `json:"value"`
	Etag  string `json:"etag"`
}

func readEtag(path string) (*etagEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e etagEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("addonhost: corrupt etag entry: %w", err)
	}
	return &e, nil
}

func writeEtag(path string, value any) (string, error) {
	etag := fmt.Sprintf("%d", time.Now().UnixNano())
	e := etagEntry{Value: value, Etag: etag}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return etag, nil
}
