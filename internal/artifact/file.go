package artifact

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// MaxWorkspaceFileBytes bounds path-only Session artifacts before Work loads
// them into memory and persists them in its content-addressed BlobStore.
const MaxWorkspaceFileBytes = 128 * 1024 * 1024

// LoadWorkspaceFile resolves a path-only artifact inside workspaceRoot and
// returns a copy with validated bytes, absolute path, name, and MIME type.
// External artifacts must be validated and populated by their Producer.
func LoadWorkspaceFile(item Discovered, workspaceRoot string) (Discovered, error) {
	if len(item.Data) > 0 {
		return item, nil
	}
	root := filepath.Clean(strings.TrimSpace(workspaceRoot))
	if root == "." || !filepath.IsAbs(root) {
		return Discovered{}, fmt.Errorf("workspace root must be absolute")
	}
	path := filepath.Clean(strings.TrimSpace(item.Path))
	if path == "." || path == "" {
		return Discovered{}, fmt.Errorf("artifact path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	if !PathWithinAbsolute(path, root) {
		return Discovered{}, fmt.Errorf("artifact %q is outside workspace", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Discovered{}, fmt.Errorf("artifact lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Discovered{}, fmt.Errorf("artifact must not be a symlink")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxWorkspaceFileBytes {
		return Discovered{}, fmt.Errorf("artifact must be a regular file between 1 byte and %d bytes", MaxWorkspaceFileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return Discovered{}, fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Discovered{}, err
	}
	if !os.SameFile(info, opened) {
		return Discovered{}, fmt.Errorf("artifact changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxWorkspaceFileBytes+1))
	if err != nil {
		return Discovered{}, err
	}
	if len(data) == 0 || len(data) > MaxWorkspaceFileBytes {
		return Discovered{}, fmt.Errorf("artifact must be between 1 byte and %d bytes", MaxWorkspaceFileBytes)
	}
	if after, err := file.Stat(); err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return Discovered{}, fmt.Errorf("artifact changed while reading")
	}
	item.Path = path
	item.Data = data
	if strings.TrimSpace(item.Name) == "" {
		item.Name = filepath.Base(path)
	}
	if strings.TrimSpace(item.Type) == "" {
		item.Type = http.DetectContentType(data)
	}
	return item, nil
}
