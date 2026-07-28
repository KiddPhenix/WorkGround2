package artifact

import (
	"bytes"
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

// MaxImageBytes is the maximum size of a validated artifact image.
const MaxImageBytes = 10 * 1024 * 1024

// ValidateImageFile revalidates a generated image at the shared boundary.
// It checks: absolute path, not a symlink, regular file, size bounds,
// within allowed roots, MIME is image/*, and decodable. Returns raw bytes and
// detected MIME type on success.
func ValidateImageFile(path string) ([]byte, string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "." || !filepath.IsAbs(cleaned) {
		return nil, "", fmt.Errorf("image path must be absolute")
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("image lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("image must not be a symlink")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxImageBytes {
		return nil, "", fmt.Errorf("image must be a regular file between 1 byte and 10 MB")
	}
	allowed := false
	for _, root := range AllowedImageRoots() {
		if PathWithinAbsolute(cleaned, root) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, "", fmt.Errorf("image %q is outside allowed output directories", cleaned)
	}
	f, err := os.Open(cleaned)
	if err != nil {
		return nil, "", fmt.Errorf("open image: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(info, opened) {
		return nil, "", fmt.Errorf("image changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 || len(raw) > MaxImageBytes {
		return nil, "", fmt.Errorf("image must be between 1 byte and 10 MB")
	}
	if after, err := f.Stat(); err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return nil, "", fmt.Errorf("image changed while reading")
	}
	mime, err := ValidateImageData(raw)
	if err != nil {
		return nil, "", err
	}
	return raw, mime, nil
}

// ValidateImageData verifies that data is a bounded, decodable raster image
// and returns its detected MIME type.
func ValidateImageData(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > MaxImageBytes {
		return "", fmt.Errorf("image must be between 1 byte and 10 MB")
	}
	mime := http.DetectContentType(raw)
	if !strings.HasPrefix(mime, "image/") {
		return "", fmt.Errorf("not an image (detected %q)", mime)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return "", fmt.Errorf("image decode: %w", err)
	}
	return mime, nil
}

// AllowedImageRoots returns the directories where generated images may reside.
// It includes draw-tool AddOn output dirs and CODEX_HOME/generated_images.
func AllowedImageRoots() []string {
	var roots []string
	home := config.WorkGround2HomeDir()
	providers, err := drawaddon.New(home).Providers()
	if err == nil {
		for _, p := range providers {
			root := filepath.Clean(strings.TrimSpace(p.OutputDir))
			if !p.Enabled || root == "." || root == "" {
				continue
			}
			if !filepath.IsAbs(root) {
				root = filepath.Join(home, "addons", "draw-tool", "outputs", p.ID, root)
			}
			roots = append(roots, filepath.Clean(root))
		}
	}
	if root := provider.CodexGeneratedImagesRoot(); root != "" {
		roots = append(roots, root)
	}
	return roots
}

// PathWithinAbsolute reports whether path is inside root, resolving symlinks
// on both sides before comparison.
func PathWithinAbsolute(path, root string) bool {
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
