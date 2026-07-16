package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxArtifactImageBytes = 10 * 1024 * 1024

// artifactImageAllowedRoots returns the set of directories where artifact images
// may reside: the workspace root plus request_help image output directories.
func (a *App) artifactImageAllowedRoots(tabID string) []string {
	var roots []string
	tab, _ := a.tabAndCtrlByID(tabID)
	if tab != nil {
		root := filepath.Clean(strings.TrimSpace(tab.WorkspaceRoot))
		if root != "" && root != "." {
			roots = append(roots, root)
		}
	}
	roots = append(roots, requestHelpImageAllowedRoots()...)
	return roots
}

// readArtifactImage cross-references an artifact by tabID+artifactID via
// ArtifactsForTab, validates it is a type=image / status=available artifact,
// then re-validates the file on disk with the same TOCTOU-safe checks as
// readRequestHelpImage (regular non-symlink file, size bounded, MIME=image/*,
// decodable, stable before/after read).
func (a *App) readArtifactImage(tabID, artifactID string) ([]byte, string, error) {
	if tabID == "" || artifactID == "" {
		return nil, "", fmt.Errorf("tabID and artifactID are required")
	}
	records := a.ArtifactsForTab(tabID)
	var target *ArtifactView
	for i := range records {
		if records[i].ArtifactID == artifactID {
			target = &records[i]
			break
		}
	}
	if target == nil {
		return nil, "", fmt.Errorf("artifact %q not found in tab %q", artifactID, tabID)
	}
	if target.Type != "image" {
		return nil, "", fmt.Errorf("artifact %q is type %q, not image", artifactID, target.Type)
	}
	if target.Status != "available" {
		return nil, "", fmt.Errorf("artifact %q status is %q, not available", artifactID, target.Status)
	}
	cleaned := filepath.Clean(strings.TrimSpace(target.Path))
	if cleaned == "." || !filepath.IsAbs(cleaned) {
		return nil, "", fmt.Errorf("artifact image path must be absolute")
	}

	// Lstat to reject symlinks.
	info, err := os.Lstat(cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("artifact image lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("artifact image must not be a symlink")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxArtifactImageBytes {
		return nil, "", fmt.Errorf("artifact image must be a regular file between 1 byte and 10 MB")
	}

	// Boundary check: workspace root or request_help allowed roots.
	allowed := false
	for _, root := range a.artifactImageAllowedRoots(tabID) {
		if pathWithinAbsolute(cleaned, root) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, "", fmt.Errorf("artifact image %q is outside allowed directories", cleaned)
	}

	f, err := os.Open(cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("open artifact image: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(info, opened) {
		return nil, "", fmt.Errorf("artifact image changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxArtifactImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 || len(raw) > maxArtifactImageBytes {
		return nil, "", fmt.Errorf("artifact image must be between 1 byte and 10 MB")
	}
	if after, err := f.Stat(); err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return nil, "", fmt.Errorf("artifact image changed while reading")
	}
	mime := http.DetectContentType(raw)
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", fmt.Errorf("artifact image is not an image (detected %q)", mime)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return nil, "", fmt.Errorf("artifact image decode: %w", err)
	}
	return raw, mime, nil
}

// ArtifactImageDataURL returns a browser-safe data URL for an image artifact
// identified by tabID + artifactID. It re-validates the file on every call.
func (a *App) ArtifactImageDataURL(tabID, artifactID string) (string, error) {
	raw, mime, err := a.readArtifactImage(tabID, artifactID)
	if err != nil {
		return "", err
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// ArtifactOpenImage opens a validated artifact image in the OS default app.
func (a *App) ArtifactOpenImage(tabID, artifactID string) error {
	records := a.ArtifactsForTab(tabID)
	var target *ArtifactView
	for i := range records {
		if records[i].ArtifactID == artifactID {
			target = &records[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("artifact %q not found in tab %q", artifactID, tabID)
	}
	if target.Type != "image" || target.Status != "available" {
		return fmt.Errorf("artifact %q is not an available image", artifactID)
	}
	cleaned := filepath.Clean(strings.TrimSpace(target.Path))
	if _, _, err := a.readArtifactImage(tabID, artifactID); err != nil {
		return err
	}
	return openWorkspacePath(cleaned)
}

// ArtifactRevealImage reveals a validated artifact image in the native file manager.
func (a *App) ArtifactRevealImage(tabID, artifactID string) error {
	records := a.ArtifactsForTab(tabID)
	var target *ArtifactView
	for i := range records {
		if records[i].ArtifactID == artifactID {
			target = &records[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("artifact %q not found in tab %q", artifactID, tabID)
	}
	if target.Type != "image" || target.Status != "available" {
		return fmt.Errorf("artifact %q is not an available image", artifactID)
	}
	if _, _, err := a.readArtifactImage(tabID, artifactID); err != nil {
		return err
	}
	return revealPath(filepath.Clean(strings.TrimSpace(target.Path)))
}
