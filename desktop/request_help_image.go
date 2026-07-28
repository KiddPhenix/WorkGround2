package main

import (
	"encoding/base64"
	"path/filepath"
	"strings"

	"workground2/internal/artifact"
)

// readRequestHelpImage revalidates a generated image by delegating to the
// shared artifact package.
func readRequestHelpImage(path string) ([]byte, string, error) {
	return artifact.ValidateImageFile(path)
}

// RequestHelpImageDataURL returns a browser-safe image after revalidation.
func (a *App) RequestHelpImageDataURL(path string) (string, error) {
	raw, mime, err := artifact.ValidateImageFile(path)
	if err != nil {
		return "", err
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// RequestHelpOpenImage opens a validated generated image in the OS default app.
func (a *App) RequestHelpOpenImage(path string) error {
	if _, _, err := artifact.ValidateImageFile(path); err != nil {
		return err
	}
	return openWorkspacePath(filepath.Clean(strings.TrimSpace(path)))
}

// RequestHelpRevealImage reveals a validated generated image in the native file manager.
func (a *App) RequestHelpRevealImage(path string) error {
	if _, _, err := artifact.ValidateImageFile(path); err != nil {
		return err
	}
	return revealPath(filepath.Clean(strings.TrimSpace(path)))
}
