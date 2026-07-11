package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/fileutil"
	sess "workground2/internal/session"
)

// sessions.go holds the desktop-only session-management state that the shared
// kernel doesn't model: custom display titles. A session on disk is just a JSONL
// transcript named by timestamp+model, with no title slot — so the history panel
// stores user-chosen names in a sidecar map (basename → title) next to the .jsonl
// files. The preview (first user message) is the default name; a title overrides
// it. Deleting a session also drops its title entry.

const sessionDisplayFile = ".display.json"
const sessionPlannerDisplayFile = ".planner-display.json"
const sessionTrashDir = sess.TrashDir
const sessionTrashMetaFile = sess.TrashMetaFile

func sessionTitlesPath(dir string) string  { return sess.TitlesPath(dir) }
func sessionDisplayPath(dir string) string { return filepath.Join(dir, sessionDisplayFile) }
func sessionTrashPath(dir string) string   { return sess.TrashPath(dir) }

func desktopSessionDir(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return config.SessionDir()
		}
		root = cwd
	}
	if dir := config.ProjectSessionDir(root); dir != "" {
		return dir
	}
	return config.SessionDir()
}

// loadSessionTitles reads the basename→title map (missing/corrupt → empty).
func loadSessionTitles(dir string) map[string]string {
	m := map[string]string{}
	b, err := readFileWithTimeout(sess.TitlesPath(dir), topicFileReadTimeout)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func loadSessionTitlesForUpdate(dir string) (map[string]string, error) {
	return loadStringMapForUpdate(sess.TitlesPath(dir))
}

// saveSessionTitles writes the map atomically (temp file + rename).
func saveSessionTitles(dir string, m map[string]string) error {
	return sess.SaveTitles(dir, m)
}

// setSessionTitle sets (or, with an empty title, clears) a session's custom name.
func setSessionTitle(dir, sessionPath, title string) error {
	return sess.SetTitle(dir, sessionPath, title)
}

// deleteSessionFile moves a session's .jsonl and file sidecars into the local
// trash. Title/display sidecars stay in place so trash previews and restores can
// preserve the user's labels.
func deleteSessionFile(dir, sessionPath string) error {
	sessionPath, key, err := validateSessionPath(dir, sessionPath)
	if err != nil {
		return err
	}
	return trashSessionArtifacts(dir, sessionPath, key)
}

type trashedSessionMeta = sess.TrashedMeta

type sessionTrashArtifact = sess.Artifact

// sessionTelemetryPath delegates to the shared session package.
func sessionTelemetryPath(sessionPath string) string {
	return sess.TelemetryPath(sessionPath)
}

// sessionTrashArtifacts delegates to the shared session package.
func sessionTrashArtifacts(sessionPath, key string) []sessionTrashArtifact {
	return sess.Artifacts(sessionPath, key)
}

var errSessionBusyElsewhere = errors.New("session is in use by another WorkGround2 window or process")

func acquireSessionRemovalGuard(sessionPath string) (*agent.SessionRemovalGuard, error) {
	guard, err := agent.TryAcquireSessionRemovalGuard(sessionPath)
	if err != nil {
		if errors.Is(err, agent.ErrSessionLeaseHeld) {
			return nil, errSessionBusyElsewhere
		}
		return nil, err
	}
	return guard, nil
}

func sessionOwnedArtifactPaths(sessionPath string) []string {
	key := filepath.Base(sessionPath)
	artifacts := sessionTrashArtifacts(sessionPath, key)
	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Src) != "" {
			paths = append(paths, artifact.Src)
		}
	}
	return paths
}

func trashSessionArtifacts(dir, sessionPath, key string) error {
	return trashSessionArtifactsBeforeMove(dir, sessionPath, key, nil)
}

func reconcileDesktopCleanupPending(dir string) error {
	return agent.ReconcileCleanupPending(dir, func(item agent.CleanupPendingInfo) error {
		if strings.TrimSpace(item.Meta.Operation) == "delete" {
			sessionPath, key, err := validateSessionPath(dir, item.SessionPath)
			if err != nil {
				return err
			}
			return reconcileDesktopTrashSessionArtifacts(dir, sessionPath, key)
		}
		return removeDesktopSessionArtifacts(item.SessionPath)
	})
}

func reconcileDesktopTrashSessionArtifacts(dir, sessionPath, key string) error {
	guard, err := acquireSessionRemovalGuard(sessionPath)
	if err != nil {
		return err
	}
	defer guard.Release()
	itemDir := filepath.Join(sessionTrashPath(dir), key)
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		return err
	}
	for _, artifact := range sessionTrashArtifacts(sessionPath, key) {
		if err := sess.MovePathIfExists(artifact.Src, filepath.Join(itemDir, artifact.Name)); err != nil {
			return err
		}
	}
	if err := trashSubagentArtifacts(dir, sessionPath, itemDir); err != nil {
		return err
	}
	if err := guard.RemoveSidecarsAndRelease(); err != nil {
		return err
	}
	meta := trashedSessionMeta{Key: key, DeletedAt: time.Now().UnixMilli()}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(itemDir, sess.TrashMetaFile), b, 0o644); err != nil {
		return err
	}
	return agent.ClearCleanupPending(sessionPath)
}

func validateSessionTrashTarget(dir, sessionPath, key string) error {
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	itemDir := filepath.Join(sessionTrashPath(dir), key)
	if info, err := os.Stat(itemDir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("session trash target is not a directory: %s", key)
		}
		trashPath := filepath.Join(itemDir, key)
		if trashInfo, err := os.Stat(trashPath); err == nil && !trashInfo.IsDir() {
			discardable, err := liveSessionDiscardable(sessionPath)
			if err != nil {
				return err
			}
			if discardable {
				return nil
			}
			return fmt.Errorf("session already exists in trash: %s", key)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func prepareSessionTrashTarget(dir, sessionPath, key string) (bool, error) {
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	itemDir := filepath.Join(sessionTrashPath(dir), key)
	if info, err := os.Stat(itemDir); err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("session trash target is not a directory: %s", key)
		}
		trashPath := filepath.Join(itemDir, key)
		if trashInfo, err := os.Stat(trashPath); err == nil && !trashInfo.IsDir() {
			discardable, err := liveSessionDiscardable(sessionPath)
			if err != nil {
				return false, err
			}
			if discardable {
				return false, removeDesktopSessionArtifacts(sessionPath)
			}
			return false, fmt.Errorf("session already exists in trash: %s", key)
		} else if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		if err := os.RemoveAll(itemDir); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

func liveSessionDiscardable(sessionPath string) (bool, error) {
	if agent.IsCleanupPending(sessionPath) {
		return true, nil
	}
	info, err := os.Stat(sessionPath)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, nil
	}
	if info.Size() == 0 {
		return true, nil
	}
	session, err := agent.LoadSession(sessionPath)
	if err != nil {
		return false, nil
	}
	return !session.HasContent(), nil
}

func sessionFileHasConversationContent(sessionPath string) bool {
	if strings.TrimSpace(sessionPath) == "" || agent.IsCleanupPending(sessionPath) {
		return false
	}
	info, err := os.Stat(sessionPath)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return false
	}
	session, err := agent.LoadSession(sessionPath)
	if err != nil {
		return false
	}
	return session.HasContent()
}

func trashSessionArtifactsBeforeMove(dir, sessionPath, key string, beforeMove func()) error {
	if err := validateSessionTrashTarget(dir, sessionPath, key); err != nil {
		return err
	}
	shouldMove, err := prepareSessionTrashTarget(dir, sessionPath, key)
	if err != nil {
		return err
	}
	if !shouldMove {
		return nil
	}
	guard, err := acquireSessionRemovalGuard(sessionPath)
	if err != nil {
		return err
	}
	defer guard.Release()
	itemDir := filepath.Join(sessionTrashPath(dir), key)
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		return err
	}
	if beforeMove != nil {
		beforeMove()
	}
	for _, artifact := range sessionTrashArtifacts(sessionPath, key) {
		if err := sess.MovePathIfExists(artifact.Src, filepath.Join(itemDir, artifact.Name)); err != nil {
			return err
		}
	}
	if err := trashSubagentArtifacts(dir, sessionPath, itemDir); err != nil {
		return err
	}
	if err := guard.RemoveSidecarsAndRelease(); err != nil {
		return err
	}
	meta := trashedSessionMeta{Key: key, DeletedAt: time.Now().UnixMilli()}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(itemDir, sess.TrashMetaFile), b, 0o644); err != nil {
		return err
	}
	if err := agent.ClearCleanupPending(sessionPath); err != nil {
		return err
	}
	return nil
}

func listTrashedSessionFiles(dir string) ([]string, error) {
	root := sessionTrashPath(dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	paths := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := e.Name()
		if filepath.Ext(key) != ".jsonl" || filepath.Base(key) != key {
			continue
		}
		path := filepath.Join(root, key, key)
		validPath, _, _, err := validateTrashedSessionPath(dir, path)
		if err != nil {
			continue
		}
		if info, err := os.Stat(validPath); err == nil && !info.IsDir() {
			paths = append(paths, validPath)
		}
	}
	return paths, nil
}

func trashedSessionDeletedAt(path string) int64 {
	return sess.TrashedDeletedAt(path)
}

func restoreTrashedSessionFile(dir, path string) error {
	_, key, itemDir, err := validateTrashedSessionPath(dir, path)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, key)
	if _, err := os.Stat(target); err == nil {
		discardable, err := liveSessionDiscardable(target)
		if err != nil {
			return err
		}
		if !discardable {
			return fmt.Errorf("session already exists: %s", key)
		}
		if err := removeDesktopSessionArtifacts(target); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := checkRestoreSubagentConflicts(dir, itemDir); err != nil {
		return err
	}
	for _, artifact := range sessionTrashArtifacts(target, key) {
		if err := sess.MovePathIfExists(filepath.Join(itemDir, artifact.Name), artifact.Src); err != nil {
			return err
		}
	}
	if err := restoreSubagentArtifacts(dir, itemDir); err != nil {
		return err
	}
	return os.RemoveAll(itemDir)
}

func purgeTrashedSessionFile(dir, path string) error {
	_, key, itemDir, err := validateTrashedSessionPath(dir, path)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(itemDir); err != nil {
		return err
	}
	m, err := loadSessionTitlesForUpdate(dir)
	if err != nil {
		return err
	}
	if _, ok := m[key]; ok {
		delete(m, key)
		if err := saveSessionTitles(dir, m); err != nil {
			return err
		}
	}
	if dm := loadSessionDisplays(dir); dm[key] != nil {
		delete(dm, key)
		if err := saveSessionDisplays(dir, dm); err != nil {
			return err
		}
	}
	return nil
}

// movePathIfExists delegates to the shared session package.
func movePathIfExists(src, dst string) error {
	return sess.MovePathIfExists(src, dst)
}

// copyAndRemove recursively copies src to dst, then removes src. Used as a
// fallback when os.Rename fails (cross-device or Windows file-lock races).
func copyAndRemove(src, dst string) error {
	if err := copyPath(src, dst); err != nil {
		return err
	}
	// On Windows, wait briefly for any file handle release.
	time.Sleep(10 * time.Millisecond)
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
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if err := copyPath(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	// Open source file.
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	// Create destination file.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		in.Close()
		return err
	}
	// Copy content.
	_, err = io.Copy(out, in)
	// Close both files before any removal.
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

func trashSubagentArtifacts(dir, sessionPath, itemDir string) error {
	artifacts, err := agent.ListSubagentsByParent(dir, agent.BranchID(sessionPath))
	if err != nil {
		return err
	}
	trashSubagentDir := filepath.Join(itemDir, "subagents")
	for _, artifact := range artifacts {
		if err := sess.MovePathIfExists(artifact.SessionPath, filepath.Join(trashSubagentDir, filepath.Base(artifact.SessionPath))); err != nil {
			return err
		}
		if err := sess.MovePathIfExists(artifact.MetaPath, filepath.Join(trashSubagentDir, filepath.Base(artifact.MetaPath))); err != nil {
			return err
		}
	}
	return nil
}

func checkRestoreSubagentConflicts(dir, itemDir string) error {
	trashSubagentDir := filepath.Join(itemDir, "subagents")
	entries, err := os.ReadDir(trashSubagentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		target := filepath.Join(dir, "subagents", entry.Name())
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("subagent artifact already exists: %s", entry.Name())
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func restoreSubagentArtifacts(dir, itemDir string) error {
	trashSubagentDir := filepath.Join(itemDir, "subagents")
	entries, err := os.ReadDir(trashSubagentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := sess.MovePathIfExists(filepath.Join(trashSubagentDir, entry.Name()), filepath.Join(dir, "subagents", entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// validateSessionPath delegates to the shared session package.
func validateSessionPath(dir, sessionPath string) (string, string, error) {
	return sess.ValidatePath(dir, sessionPath)
}

// validateTrashedSessionPath delegates to the shared session package.
func validateTrashedSessionPath(dir, sessionPath string) (string, string, string, error) {
	return sess.ValidateTrashedPath(dir, sessionPath)
}

type sessionDisplayMap map[string]map[string]string

type sessionPlannerDisplayMap map[string][]plannerDisplayTurn

type plannerDisplayTurn struct {
	UserHash string           `json:"userHash"`
	Messages []HistoryMessage `json:"messages"`
}

func messageDisplayKey(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum[:])
}

func loadSessionDisplays(dir string) sessionDisplayMap {
	m := sessionDisplayMap{}
	b, err := readFileUTF8(sessionDisplayPath(dir))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func sessionPlannerDisplayPath(dir string) string {
	return filepath.Join(dir, sessionPlannerDisplayFile)
}

func loadSessionPlannerDisplays(dir string) sessionPlannerDisplayMap {
	m := sessionPlannerDisplayMap{}
	if strings.TrimSpace(dir) == "" {
		return m
	}
	b, err := readFileUTF8(sessionPlannerDisplayPath(dir))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveSessionPlannerDisplays(dir string, m sessionPlannerDisplayMap) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".planner-display.*.tmp")
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
	return fileutil.ReplaceFile(tmpPath, sessionPlannerDisplayPath(dir))
}

func recordSessionPlannerDisplay(dir, sessionPath, userContent string, messages []HistoryMessage) error {
	if strings.TrimSpace(sessionPath) == "" || strings.TrimSpace(userContent) == "" || len(messages) == 0 {
		return nil
	}
	m := loadSessionPlannerDisplays(dir)
	key := filepath.Base(sessionPath)
	turn := plannerDisplayTurn{
		UserHash: messageDisplayKey(userContent),
		Messages: cloneHistoryMessages(messages),
	}
	m[key] = append(m[key], turn)
	return saveSessionPlannerDisplays(dir, m)
}

func sessionPlannerDisplayTurns(dir, sessionPath string) []plannerDisplayTurn {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(sessionPath) == "" {
		return nil
	}
	turns := loadSessionPlannerDisplays(dir)[filepath.Base(sessionPath)]
	if len(turns) == 0 {
		return nil
	}
	out := make([]plannerDisplayTurn, 0, len(turns))
	for _, turn := range turns {
		if strings.TrimSpace(turn.UserHash) == "" || len(turn.Messages) == 0 {
			continue
		}
		out = append(out, plannerDisplayTurn{
			UserHash: turn.UserHash,
			Messages: cloneHistoryMessages(turn.Messages),
		})
	}
	return out
}

func saveSessionDisplays(dir string, m sessionDisplayMap) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".display.*.tmp")
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
	return fileutil.ReplaceFile(tmpPath, sessionDisplayPath(dir))
}

func recordSessionDisplay(dir, sessionPath, content, display string) error {
	if strings.TrimSpace(sessionPath) == "" || content == display || strings.TrimSpace(display) == "" {
		return nil
	}
	m := loadSessionDisplays(dir)
	key := filepath.Base(sessionPath)
	if m[key] == nil {
		m[key] = map[string]string{}
	}
	m[key][messageDisplayKey(content)] = display
	return saveSessionDisplays(dir, m)
}

// sessionDisplayResolver loads the sidecar once and returns a per-message
// resolver, so a transcript of N messages doesn't re-read .display.json N times.
func sessionDisplayResolver(dir, sessionPath string) func(content string) string {
	return sessionDisplayResolverFromMap(loadSessionDisplays(dir), sessionPath)
}

func sessionDisplayResolverFromMap(displays sessionDisplayMap, sessionPath string) func(content string) string {
	byHash := displays[filepath.Base(sessionPath)]
	return func(content string) string {
		if byHash != nil {
			if display := byHash[messageDisplayKey(content)]; strings.TrimSpace(display) != "" {
				return display
			}
		}
		return control.StripComposePrefixes(content)
	}
}

func resolveSessionDisplay(dir, sessionPath, content string) string {
	return sessionDisplayResolver(dir, sessionPath)(content)
}
