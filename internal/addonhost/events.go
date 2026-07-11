package addonhost

import (
	"os"
)

// ── Events API ──────────────────────────────────────────────────────────────

// EventKind constants for the well-known AddOn event types.
const (
	EventRecordsChanged = "addon.records.changed"
	EventTaskChanged    = "addon.task.changed"
	EventSkillsChanged  = "skills.changed"
	EventToolsChanged   = "tools.changed"
)

// RecordsChanged emits an addon.records.changed event for the given adapter.
func (h *Host) RecordsChanged(adapter string) {
	h.emit(EventRecordsChanged, adapter, "", nil)
}

// TaskChanged emits an addon.task.changed event.
func (h *Host) TaskChanged(taskID string) {
	h.emit(EventTaskChanged, "", taskID, nil)
}

// SkillsChanged notifies the host that the AddOn's contributed skills have
// changed (e.g. after a sync) and should be re-indexed.
func (h *Host) SkillsChanged() {
	h.emit(EventSkillsChanged, "", "", nil)
}

// ToolsChanged notifies the host that the AddOn's contributed tools have
// changed and should be re-enumerated.
func (h *Host) ToolsChanged() {
	h.emit(EventToolsChanged, "", "", nil)
}

// ── Helper utilities shared across packages ─────────────────────────────────

// stringField reads a string key from a map, defaulting to "".
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// removeQuiet removes a file, ignoring "not found" errors.
func removeQuiet(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
