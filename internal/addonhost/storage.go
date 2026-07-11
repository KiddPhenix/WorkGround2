package addonhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ── Storage API ─────────────────────────────────────────────────────────────

// StorageEntry is a single item returned by List.
type StorageEntry struct {
	Key  string `json:"key"`
	Etag string `json:"etag"`
}

// StorageGet reads a key's value.  Returns (value, etag, error).
// If the key does not exist, returns ("", "", ErrNotFound).
func (h *Host) StorageGet(key string) (value string, etag string, err error) {
	path := h.storageKeyPath(key)
	entry, err := readEtag(path)
	if os.IsNotExist(err) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	b, err := json.Marshal(entry.Value)
	if err != nil {
		return "", "", err
	}
	return string(b), entry.Etag, nil
}

// StoragePut creates or replaces a key.  If etag is non-empty, the write
// is conditional on the stored etag matching — returns ErrConflict on mismatch.
// Returns the new etag on success.
func (h *Host) StoragePut(key string, valueJSON string, etag string) (newEtag string, err error) {
	path := h.storageKeyPath(key)

	if etag != "" {
		existing, readErr := readEtag(path)
		if os.IsNotExist(readErr) {
			// If a conditional PUT targets a missing key, reject it.
			return "", ErrConflict
		}
		if readErr != nil {
			return "", readErr
		}
		if existing.Etag != etag {
			return "", ErrConflict
		}
	}

	var val any
	if err := json.Unmarshal([]byte(valueJSON), &val); err != nil {
		return "", err
	}
	return writeEtag(path, val)
}

// StoragePatch applies a JSON patch (RFC 6902 subset: top-level field
// merges only) to an existing value.  Etag semantics are the same as Put.
func (h *Host) StoragePatch(key string, patchJSON string, etag string) (newEtag string, err error) {
	path := h.storageKeyPath(key)

	existing, err := readEtag(path)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if etag != "" && existing.Etag != etag {
		return "", ErrConflict
	}

	// Only top-level key merges are supported.
	existingMap, ok := existing.Value.(map[string]any)
	if !ok {
		existingMap = map[string]any{}
	}

	var patch map[string]any
	if err := json.Unmarshal([]byte(patchJSON), &patch); err != nil {
		return "", err
	}
	for k, v := range patch {
		existingMap[k] = v
	}
	return writeEtag(path, existingMap)
}

// StorageDelete removes a key.  Idempotent — succeeds even if the key
// does not exist.
func (h *Host) StorageDelete(key string) error {
	path := h.storageKeyPath(key)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// StorageList enumerates keys under the given prefix.  Returns an empty
// slice (not nil) when no keys match.
func (h *Host) StorageList(prefix string) ([]StorageEntry, error) {
	dir := h.storageDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []StorageEntry{}, nil
	}
	if err != nil {
		return nil, err
	}

	var out []StorageEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		key := strings.TrimSuffix(name, ".json")
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		path := filepath.Join(dir, name)
		e, err := readEtag(path)
		if err != nil {
			// Skip unreadable entries silently.
			continue
		}
		out = append(out, StorageEntry{Key: key, Etag: e.Etag})
	}
	if out == nil {
		out = []StorageEntry{}
	}
	return out, nil
}
