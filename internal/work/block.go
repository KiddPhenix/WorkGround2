package work

import (
	"encoding/json"
	"time"
)

// ── BlockInstance ──────────────────────────────────────────────────────────

// BlockInstance is the runtime instance of a block on the front face of a Work.
type BlockInstance struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	SchemaVersion int               `json:"schemaVersion"`
	Revision      int64             `json:"revision"`
	Title         string            `json:"title,omitempty"`
	Status        BlockStatus       `json:"status"`
	Data          json.RawMessage   `json:"data"`
	Actions       []BlockActionSpec `json:"actions,omitempty"`
	Source        BlockSource       `json:"source"`
	Freshness     *BlockFreshness   `json:"freshness,omitempty"`
	Fallback      BlockFallback     `json:"fallback"`
	Tombstone     bool              `json:"tombstone,omitempty"`
	CreatedAt     time.Time         `json:"createdAt" ts_type:"string"`
	UpdatedAt     time.Time         `json:"updatedAt" ts_type:"string"`
}

// BlockStatus is the runtime readiness of a BlockInstance.
type BlockStatus string

const (
	BlockLoading BlockStatus = "loading"
	BlockReady   BlockStatus = "ready"
	BlockEmpty   BlockStatus = "empty"
	BlockStale   BlockStatus = "stale"
	BlockBlocked BlockStatus = "blocked"
	BlockFailed  BlockStatus = "failed"
)

// BlockSource records who or what produced a BlockInstance's data.
type BlockSource struct {
	Provider string `json:"provider"` // user | ai | tool:<name> | addon:<name> | controller
	Ref      string `json:"ref,omitempty"`
	Mode     string `json:"mode"` // snapshot | query | stream
	Verified bool   `json:"verified"`
}

// BlockFreshness carries optional freshness metadata for live Blocks.
type BlockFreshness struct {
	CheckedAt   *time.Time `json:"checkedAt,omitempty" ts_type:"string"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty" ts_type:"string"`
	RetryAt     *time.Time `json:"retryAt,omitempty" ts_type:"string"`
	StaleReason string     `json:"staleReason,omitempty"`
}

// BlockFallback is a degraded-text summary + optional raw data used when a
// Renderer is unavailable or the block schema is unknown.
type BlockFallback struct {
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// BlockPlacement positions a BlockInstance within the WorkCard layout. Layout
// metadata is kept separate from the block's semantic data.
type BlockPlacement struct {
	BlockID   string `json:"blockId"`
	Slot      string `json:"slot"` // primary | secondary | attention | result
	Order     int    `json:"order"`
	Span      int    `json:"span,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// BlockActionSpec describes an executable action that a Block can offer.
type BlockActionSpec struct {
	ID              string          `json:"id"`
	Label           string          `json:"label"`
	Intent          string          `json:"intent"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Risk            string          `json:"risk"` // read | write | destructive | external
	ConfirmRequired bool            `json:"confirmRequired"`
}

// ── BlockSpec (Blueprint template) ─────────────────────────────────────────

// BlockSpec defines a default block layout inside a Blueprint. It carries no
// concrete runtime data.
type BlockSpec struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schemaVersion"`
	Label         string          `json:"label"`
	Description   string          `json:"description,omitempty"`
	DefaultData   json.RawMessage `json:"defaultData,omitempty"`
	Placement     BlockPlacement  `json:"placement"`
	Editable      bool            `json:"editable"`
}

// ── BlockMigration ─────────────────────────────────────────────────────────

// BlockMigration defines a migration from one schema version to the next for a
// specific block kind. It is registered by kind and applied sequentially during
// rerun-upgrade preflight.
type BlockMigration struct {
	Kind        string
	FromVersion int
	ToVersion   int
	Migrate     func(data json.RawMessage) (json.RawMessage, error)
}
