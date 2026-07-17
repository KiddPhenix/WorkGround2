// Package session provides shared filesystem operations for session sidecar
// files (custom titles, soft-delete trash) used by both the CLI and Desktop.
// The Desktop wraps these primitives with runtime safety (session guards,
// subagent cleanup, read timeouts); the CLI uses them directly for
// lightweight session management.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"workground2/internal/fileutil"
	"workground2/internal/store"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	TitlesFile         = ".titles.json"
	TrashDir           = ".trash"
	TrashMetaFile      = ".trash-meta.json"
	DefaultTrashSubDir = "subagents"
)

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

// TitlesPath returns the full path to the .titles.json sidecar for dir.
func TitlesPath(dir string) string { return filepath.Join(dir, TitlesFile) }

// TrashPath returns the full path to the .trash/ directory for dir.
func TrashPath(dir string) string { return filepath.Join(dir, TrashDir) }

// ---------------------------------------------------------------------------
// Title sidecar (.titles.json)
// ---------------------------------------------------------------------------

// LoadTitles reads the basename→title map. Missing or corrupt files return an
// empty map.
func LoadTitles(dir string) (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(TitlesPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]string{}, nil // corrupt → treat as empty
	}
	return m, nil
}

// LoadTitlesForUpdate is like LoadTitles but returns a mutable copy for
// read-modify-write cycles.
func LoadTitlesForUpdate(dir string) (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(TitlesPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]string{}, nil
	}
	return m, nil
}

// SaveTitles atomically writes the basename→title map (temp file + rename).
func SaveTitles(dir string, m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".titles.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return fileutil.ReplaceFile(tmpPath, TitlesPath(dir))
}

// SetTitle sets (or, with an empty title, clears) a session's custom name.
func SetTitle(dir, sessionPath, title string) error {
	sessionPath, _, err := ValidatePath(dir, sessionPath)
	if err != nil {
		return err
	}
	m, err := LoadTitlesForUpdate(dir)
	if err != nil {
		return err
	}
	key := filepath.Base(sessionPath)
	if strings.TrimSpace(title) == "" {
		delete(m, key)
	} else {
		m[key] = strings.TrimSpace(title)
	}
	return SaveTitles(dir, m)
}

// ---------------------------------------------------------------------------
// Trash metadata
// ---------------------------------------------------------------------------

// TrashedMeta is the per-item metadata written next to a trashed session.
type TrashedMeta struct {
	Key       string `json:"key"`
	DeletedAt int64  `json:"deletedAt"`
}

// TrashedDeletedAt reads the deletion timestamp from a trashed session dir.
func TrashedDeletedAt(path string) int64 {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), TrashMetaFile))
	if err != nil {
		return 0
	}
	var meta TrashedMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return 0
	}
	return meta.DeletedAt
}

// ---------------------------------------------------------------------------
// Trash artifacts (the files and directories that belong to a session)
// ---------------------------------------------------------------------------

// Artifact is one file or directory to move into or out of trash alongside
// the main .jsonl transcript.
type Artifact struct {
	Src  string // absolute path on the live filesystem
	Name string // basename inside the trash item directory
}

// TelemetryPath returns the path to a session's telemetry sidecar.
func TelemetryPath(sessionPath string) string {
	if strings.TrimSpace(sessionPath) == "" {
		return ""
	}
	return sessionPath + ".telemetry.json"
}

// Artifacts returns every sidecar file/dir that should move with a session
// when it is trashed or restored.
func Artifacts(sessionPath, key string) []Artifact {
	stem := strings.TrimSuffix(key, ".jsonl")
	return []Artifact{
		{Src: sessionPath, Name: key},
		{Src: store.SessionMeta(sessionPath), Name: key + ".meta"},
		{Src: store.SessionGoalState(sessionPath), Name: stem + ".goal-state.json"},
		{Src: store.SessionEventLog(sessionPath), Name: stem + ".events.jsonl"},
		{Src: store.SessionEventIndex(sessionPath), Name: stem + ".event-index.json"},
		{Src: store.SessionConflictLog(sessionPath), Name: stem + ".conflicts.jsonl"},
		{Src: TelemetryPath(sessionPath), Name: key + ".telemetry.json"},
		{Src: store.SessionTaskMemory(sessionPath), Name: stem + ".task-memory.json"},
		{Src: store.SessionCheckpointDir(sessionPath), Name: stem + ".ckpt"},
		{Src: store.SessionJobsDir(sessionPath), Name: stem + ".jobs"},
	}
}

// ---------------------------------------------------------------------------
// Path validation
// ---------------------------------------------------------------------------

// ValidatePath checks that sessionPath is a .jsonl file inside dir (following
// symlinks on both sides). It returns the absolute, symlink-resolved path and
// the base name.
func ValidatePath(dir, sessionPath string) (string, string, error) {
	if strings.TrimSpace(sessionPath) == "" {
		return "", "", fmt.Errorf("empty session path")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	path := sessionPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(absDir, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	if filepath.Ext(absPath) != ".jsonl" {
		return "", "", fmt.Errorf("not a session file: %s", sessionPath)
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("session path outside session dir: %s", sessionPath)
	}
	if info, err := os.Lstat(absPath); err == nil {
		if info.IsDir() {
			return "", "", fmt.Errorf("not a session file: %s", sessionPath)
		}
		realDir, dirErr := filepath.EvalSymlinks(absDir)
		if dirErr != nil {
			realDir = absDir
		}
		realPath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return "", "", err
		}
		rel, err := filepath.Rel(realDir, realPath)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
			return "", "", fmt.Errorf("session path escapes session dir: %s", sessionPath)
		}
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	return absPath, filepath.Base(absPath), nil
}

// ValidateTrashedPath checks that sessionPath is a valid .jsonl inside the
// .trash/ directory. Returns (absPath, key, itemDir, error).
func ValidateTrashedPath(dir, sessionPath string) (string, string, string, error) {
	if strings.TrimSpace(sessionPath) == "" {
		return "", "", "", fmt.Errorf("empty session path")
	}
	root, err := filepath.Abs(TrashPath(dir))
	if err != nil {
		return "", "", "", err
	}
	path := sessionPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", "", err
	}
	if filepath.Ext(absPath) != ".jsonl" {
		return "", "", "", fmt.Errorf("not a session file: %s", sessionPath)
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", "", "", fmt.Errorf("session path outside trash dir: %s", sessionPath)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != parts[1] {
		return "", "", "", fmt.Errorf("invalid trash session path: %s", sessionPath)
	}
	if info, err := os.Lstat(absPath); err == nil {
		if info.IsDir() {
			return "", "", "", fmt.Errorf("not a session file: %s", sessionPath)
		}
		realRoot, dirErr := filepath.EvalSymlinks(root)
		if dirErr != nil {
			realRoot = root
		}
		realPath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return "", "", "", err
		}
		rel, err := filepath.Rel(realRoot, realPath)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
			return "", "", "", fmt.Errorf("session path escapes trash dir: %s", sessionPath)
		}
	} else if !os.IsNotExist(err) {
		return "", "", "", err
	}
	return absPath, filepath.Base(absPath), filepath.Dir(absPath), nil
}

// ---------------------------------------------------------------------------
// Trash operations (simplified — no runtime guards, no subagent cleanup)
// ---------------------------------------------------------------------------

// ListTrashed returns absolute .jsonl paths of every session inside the
// .trash/ directory.
func ListTrashed(dir string) ([]string, error) {
	root := TrashPath(dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := e.Name()
		if filepath.Ext(key) != ".jsonl" || filepath.Base(key) != key {
			continue
		}
		path := filepath.Join(root, key, key)
		validPath, _, _, err := ValidateTrashedPath(dir, path)
		if err != nil {
			continue
		}
		if info, err := os.Stat(validPath); err == nil && !info.IsDir() {
			paths = append(paths, validPath)
		}
	}
	return paths, nil
}

// TrashSession moves a session's .jsonl and all sidecar artifacts into the
// .trash/<key>/ directory. This is the simplified version without runtime
// safety guards — callers that manage live controllers (Desktop) should use
// their own guarded wrappers.
func TrashSession(dir, sessionPath string) error {
	sessionPath, key, err := ValidatePath(dir, sessionPath)
	if err != nil {
		return err
	}
	// If the live file is already gone, nothing to do.
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return nil
	}
	itemDir := filepath.Join(TrashPath(dir), key)
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		return err
	}
	for _, art := range Artifacts(sessionPath, key) {
		if err := MovePathIfExists(art.Src, filepath.Join(itemDir, art.Name)); err != nil {
			return err
		}
	}
	meta := TrashedMeta{Key: key, DeletedAt: time.Now().UnixMilli()}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(itemDir, TrashMetaFile), b, 0o644)
}

// RestoreTrashedSession moves a trashed session's .jsonl and sidecars back
// into the live session directory. Simplified — no subagent conflict check.
func RestoreTrashedSession(dir, path string) error {
	_, key, itemDir, err := ValidateTrashedPath(dir, path)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, key)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("session already exists: %s", key)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, art := range Artifacts(target, key) {
		if err := MovePathIfExists(filepath.Join(itemDir, art.Name), art.Src); err != nil {
			return err
		}
	}
	return os.RemoveAll(itemDir)
}

// PurgeTrashedSession permanently deletes a trashed session and its sidecars.
func PurgeTrashedSession(dir, path string) error {
	_, key, itemDir, err := ValidateTrashedPath(dir, path)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(itemDir); err != nil {
		return err
	}
	// Clean up the title entry if one exists.
	m, err := LoadTitlesForUpdate(dir)
	if err != nil {
		return err
	}
	if _, ok := m[key]; ok {
		delete(m, key)
		_ = SaveTitles(dir, m) // best-effort
	}
	return nil
}

// ---------------------------------------------------------------------------
// File utilities
// ---------------------------------------------------------------------------

// MovePathIfExists moves src to dst. If src does not exist it silently
// succeeds. Falls back to copy+remove when os.Rename fails (cross-device or
// Windows file-lock races).
func MovePathIfExists(src, dst string) error {
	if _, err := os.Lstat(src); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isRenameCrossDeviceOrBusy(err) {
		return err
	}
	return copyAndRemove(src, dst)
}

// isRenameCrossDeviceOrBusy reports whether err is a cross-device rename or
// a "file busy" error that a copy+remove fallback can recover from.
func isRenameCrossDeviceOrBusy(err error) bool {
	if err == nil {
		return false
	}
	if le, ok := err.(*os.LinkError); ok {
		if le.Err == syscall.EXDEV {
			return true
		}
		if errno, ok := le.Err.(syscall.Errno); ok {
			return errno == 32 // ERROR_SHARING_VIOLATION
		}
	}
	return false
}

// copyAndRemove recursively copies src to dst, then removes src.
func copyAndRemove(src, dst string) error {
	if err := copyPath(src, dst); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond) // brief wait for Windows handle release
	return os.RemoveAll(src)
}

func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return copySymlink(src, dst)
	case mode.IsDir():
		return copyDir(src, dst, mode.Perm())
	case mode.IsRegular():
		return copyFile(src, dst, mode.Perm())
	default:
		return fmt.Errorf("unsupported file type in rename fallback: %s", src)
	}
}

func copyDir(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(dst, mode); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		in.Close()
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	in.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func copySymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	return os.Symlink(target, dst)
}
