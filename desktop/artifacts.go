package main

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"workground2/internal/agent"
	"workground2/internal/artifact"
	"workground2/internal/provider"
)

// ArtifactView is a read-only snapshot of one discovered artifact returned to the
// frontend by ArtifactsForTab.
type ArtifactView struct {
	ArtifactID     string `json:"artifactId"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	SessionID      string `json:"sessionId"`
	Path           string `json:"path"`
	RelativePath   string `json:"relativePath"`
	SourceRunID    string `json:"sourceRunId"`
	LastVerifiedAt int64  `json:"lastVerifiedAt"`
}

var artifactTypeByExt = map[string]string{
	".bat": "script", ".cmd": "script", ".ps1": "script", ".sh": "script",
	".exe": "binary", ".app": "binary", ".appimage": "binary",
	".msi": "package", ".dmg": "package", ".apk": "package", ".deb": "package", ".rpm": "package",
	".zip": "archive", ".tar": "archive", ".gz": "archive", ".tgz": "archive", ".7z": "archive", ".rar": "archive",
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image", ".svg": "image", ".bmp": "image", ".ico": "image",
	".mp4": "video", ".webm": "video", ".mov": "video", ".mkv": "video",
	".mp3": "audio", ".wav": "audio", ".flac": "audio", ".ogg": "audio", ".m4a": "audio",
	".pdf": "document",
}

func classifyArtifact(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if t, ok := artifactTypeByExt[ext]; ok {
		return t
	}
	lower := strings.ToLower(filepath.Base(path))
	for _, d := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst"} {
		if strings.HasSuffix(lower, d) {
			return "archive"
		}
	}
	return "file"
}

func (a *App) ArtifactsForTab(tabID string) []ArtifactView {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if tab == nil {
		return []ArtifactView{}
	}
	if ctrl != nil {
		artifacts := extractArtifacts(ctrl.History(), tab.WorkspaceRoot)
		for i := range artifacts {
			artifacts[i].SessionID = tab.ID
			artifacts[i].ArtifactID = artifactID(tab.ID, artifacts[i].Path)
		}
		return artifacts
	}

	// Controller not yet ready — recover artifacts from the persisted session
	// file on disk. This covers the desktop startup window when
	// restoreOrBuildTabs is still constructing controllers asynchronously.
	path := strings.TrimSpace(tab.SessionPath)
	if path == "" {
		return []ArtifactView{}
	}
	sessionDir := tabSessionDir(tab)
	if sessionDir == "" {
		return []ArtifactView{}
	}
	sessionPath, _, err := validateSessionPath(sessionDir, path)
	if err != nil {
		slog.Warn("artifact session restore rejected",
			"tabID", tabID, "sessionPath", path, "sessionDir", sessionDir, "error", err)
		return []ArtifactView{}
	}
	session, err := agent.LoadSession(sessionPath)
	if err != nil {
		slog.Warn("artifact session restore failed",
			"tabID", tabID, "sessionPath", sessionPath, "error", err)
		return []ArtifactView{}
	}
	artifacts := extractArtifacts(session.Snapshot(), tab.WorkspaceRoot)
	for i := range artifacts {
		artifacts[i].SessionID = tab.ID
		artifacts[i].ArtifactID = artifactID(tab.ID, artifacts[i].Path)
	}
	return artifacts
}

func extractArtifacts(msgs []provider.Message, workspaceRoot string) []ArtifactView {
	root, err := filepath.Abs(strings.TrimSpace(workspaceRoot))
	if err != nil || strings.TrimSpace(workspaceRoot) == "" {
		return []ArtifactView{}
	}
	workspaceRoot = root
	seen := map[string]bool{}
	var artifacts []ArtifactView
	appendArtifact := func(abs, sourceRunID string, allowExternal bool) {
		abs = filepath.Clean(abs)
		if !filepath.IsAbs(abs) {
			return
		}
		rel, inWorkspace := workspaceRelativeIn(abs, workspaceRoot)
		if !inWorkspace {
			if !allowExternal {
				return
			}
			rel = filepath.Base(abs)
		}
		key := abs
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return
		}
		seen[key] = true

		status := "missing"
		var verifiedAt int64
		if info, err := os.Stat(abs); err == nil {
			status = "available"
			verifiedAt = info.ModTime().UnixMilli()
		}
		artifacts = append(artifacts, ArtifactView{
			ArtifactID:     artifactID(workspaceRoot, abs),
			Name:           filepath.Base(abs),
			Type:           classifyArtifact(abs),
			Status:         status,
			SessionID:      workspaceRoot,
			Path:           abs,
			RelativePath:   filepath.ToSlash(rel),
			SourceRunID:    sourceRunID,
			LastVerifiedAt: verifiedAt,
		})
	}

	discovered := artifact.Collect(msgs, artifact.DefaultProducers())
	for _, d := range discovered {
		if len(d.Data) > 0 {
			// Binary artifacts (e.g. validated images) carry absolute paths
			// and are allowed outside the workspace.
			appendArtifact(d.Path, d.SourceRunID, true)
		} else {
			// File-path artifacts need workspace-relative resolution.
			abs := resolvePath(workspaceRoot, d.Path)
			if abs == "" {
				continue
			}
			appendArtifact(abs, d.SourceRunID, false)
		}
	}

	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts
}

func artifactID(sessionID, absPath string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + absPath))
	return fmt.Sprintf("%x", sum[:12])
}

func resolvePath(root, p string) string {
	if isAbsPath(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(root, p))
}

func isAbsPath(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//")
}
