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

	"workground2/internal/config"
	"workground2/internal/provider"
	"workground2/pkg/drawaddon"
)

const maxRequestHelpImageBytes = 10 * 1024 * 1024

func requestHelpImageAllowedRoots() []string {
	var roots []string
	home := config.WorkGround2HomeDir()
	providers, err := drawaddon.New(home).Providers()
	if err == nil {
		for _, provider := range providers {
			root := filepath.Clean(strings.TrimSpace(provider.OutputDir))
			if !provider.Enabled || root == "." || root == "" {
				continue
			}
			if !filepath.IsAbs(root) {
				root = filepath.Join(home, "addons", "draw-tool", "outputs", provider.ID, root)
			}
			roots = append(roots, filepath.Clean(root))
		}
	}
	if root := provider.CodexGeneratedImagesRoot(); root != "" {
		roots = append(roots, root)
	}
	return roots
}

// readRequestHelpImage revalidates a generated image at the desktop boundary.
func readRequestHelpImage(path string) ([]byte, string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "." || !filepath.IsAbs(cleaned) {
		return nil, "", fmt.Errorf("request_help image path must be absolute")
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("request_help image lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("request_help image must not be a symlink")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxRequestHelpImageBytes {
		return nil, "", fmt.Errorf("request_help image must be a regular file between 1 byte and 10 MB")
	}
	allowed := false
	for _, root := range requestHelpImageAllowedRoots() {
		if pathWithinAbsolute(cleaned, root) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, "", fmt.Errorf("request_help image %q is outside allowed output directories", cleaned)
	}
	f, err := os.Open(cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("open request_help image: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(info, opened) {
		return nil, "", fmt.Errorf("request_help image changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxRequestHelpImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 || len(raw) > maxRequestHelpImageBytes {
		return nil, "", fmt.Errorf("request_help image must be between 1 byte and 10 MB")
	}
	if after, err := f.Stat(); err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return nil, "", fmt.Errorf("request_help image changed while reading")
	}
	mime := http.DetectContentType(raw)
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", fmt.Errorf("request_help image is not an image (detected %q)", mime)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return nil, "", fmt.Errorf("request_help image decode: %w", err)
	}
	return raw, mime, nil
}

func pathWithinAbsolute(path, root string) bool {
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// RequestHelpImageDataURL returns a browser-safe image after revalidation.
func (a *App) RequestHelpImageDataURL(path string) (string, error) {
	raw, mime, err := readRequestHelpImage(path)
	if err != nil {
		return "", err
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// RequestHelpOpenImage opens a validated generated image in the OS default app.
func (a *App) RequestHelpOpenImage(path string) error {
	if _, _, err := readRequestHelpImage(path); err != nil {
		return err
	}
	return openWorkspacePath(filepath.Clean(strings.TrimSpace(path)))
}

// RequestHelpRevealImage reveals a validated generated image in the native file manager.
func (a *App) RequestHelpRevealImage(path string) error {
	if _, _, err := readRequestHelpImage(path); err != nil {
		return err
	}
	return revealPath(filepath.Clean(strings.TrimSpace(path)))
}
