package artifact

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// MaxArtifactFileBytes bounds path-only artifacts before Work loads them into
// memory and persists them in its content-addressed BlobStore.
const MaxArtifactFileBytes = 128 * 1024 * 1024

// LoadWorkspaceFile resolves a path-only artifact inside workspaceRoot and
// returns a copy with validated bytes, absolute path, name, and MIME type.
// Paths outside workspaceRoot are rejected.
func LoadWorkspaceFile(item Discovered, workspaceRoot string) (Discovered, error) {
	if len(item.Data) > 0 {
		return item, nil
	}
	path, root, err := resolveFilePath(item.Path, workspaceRoot)
	if err != nil {
		return Discovered{}, err
	}
	if !PathWithinAbsolute(path, root) {
		return Discovered{}, fmt.Errorf("artifact %q is outside workspace", path)
	}
	return loadFileAtPath(item, path)
}

// LoadFile resolves a path-only artifact against baseDir and loads it without
// imposing a workspace boundary. Absolute paths are used directly; relative
// paths, including paths containing "..", are resolved from baseDir.
func LoadFile(item Discovered, baseDir string) (Discovered, error) {
	if len(item.Data) > 0 {
		return item, nil
	}
	path, _, err := resolveFilePath(item.Path, baseDir)
	if err != nil {
		return Discovered{}, err
	}
	return loadFileAtPath(item, path)
}

func resolveFilePath(path, baseDir string) (string, string, error) {
	base := filepath.Clean(strings.TrimSpace(baseDir))
	if base == "." || !filepath.IsAbs(base) {
		return "", "", fmt.Errorf("artifact base directory must be absolute")
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return "", "", fmt.Errorf("artifact path is empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path), base, nil
}

func loadFileAtPath(item Discovered, path string) (Discovered, error) {
	data, err := readValidatedFile(path)
	if err != nil {
		return Discovered{}, err
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

// readValidatedFile opens, reads, and safety-validates a file at the given
// path. It rejects symlinks, directories, empty files, files exceeding
// MaxArtifactFileBytes, and files that change during open or read.
func readValidatedFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("artifact lstat: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact must not be a symlink")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxArtifactFileBytes {
		return nil, fmt.Errorf("artifact must be a regular file between 1 byte and %d bytes", MaxArtifactFileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("artifact changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxArtifactFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > MaxArtifactFileBytes {
		return nil, fmt.Errorf("artifact must be between 1 byte and %d bytes", MaxArtifactFileBytes)
	}
	if after, err := file.Stat(); err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return nil, fmt.Errorf("artifact changed while reading")
	}
	return data, nil
}
