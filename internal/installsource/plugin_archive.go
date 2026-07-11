package installsource

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"workground2/internal/pluginpkg"
)

const (
	maxPluginArchiveBytes = 200 << 20
	maxPluginArchiveFiles = 5000
)

func looksLikePluginArchive(source string) bool {
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(source)), ".zip")
}

func (t *installSourceTool) localPluginArchiveAction(req request, archivePath string) (action, []string, error) {
	if req.Mode == "link" {
		return action{}, nil, newErr(ErrUnsupportedKind, "plugin archive %s cannot be installed with mode=link", archivePath)
	}
	root, cleanup, err := extractPluginArchive(archivePath)
	if err != nil {
		return action{}, nil, err
	}
	defer cleanup()
	pkg, warnings, err := pluginpkg.ParseDir(root)
	if err != nil {
		return action{}, warnings, newErr(ErrManifestMissing, "%v", err)
	}
	return t.pluginPackageAction(req, pkg, archivePath), warnings, nil
}

func extractPluginArchive(archivePath string) (string, func(), error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", func() {}, newErr(ErrSourceUnreadable, "plugin archive %s is not readable: %v", archivePath, err)
	}
	defer zr.Close()
	if len(zr.File) > maxPluginArchiveFiles {
		return "", func() {}, newErr(ErrInvalidManifest, "plugin archive has too many files: %d > %d", len(zr.File), maxPluginArchiveFiles)
	}
	tmp, err := os.MkdirTemp("", "WorkGround2-plugin-archive-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	var total int64
	for _, f := range zr.File {
		rel, err := cleanArchiveName(f.Name)
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				cleanup()
				return "", func() {}, err
			}
			continue
		}
		mode := f.FileInfo().Mode()
		if mode&os.ModeSymlink != 0 || !mode.IsRegular() {
			cleanup()
			return "", func() {}, newErr(ErrInvalidManifest, "plugin archive entry %q must be a regular file", f.Name)
		}
		total += int64(f.UncompressedSize64)
		if total > maxPluginArchiveBytes {
			cleanup()
			return "", func() {}, newErr(ErrInvalidManifest, "plugin archive exceeds %d bytes", maxPluginArchiveBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "", func() {}, err
		}
		if err := extractArchiveFile(f, target); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	root, err := pluginRootInExtractedDir(tmp)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return root, cleanup, nil
}

func cleanArchiveName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return "", nil
	}
	if filepath.VolumeName(name) != "" || path.IsAbs(name) {
		return "", newErr(ErrInvalidManifest, "plugin archive entry %q must be relative", name)
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", newErr(ErrInvalidManifest, "plugin archive entry %q escapes the archive root", name)
	}
	return clean, nil
}

func extractArchiveFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	perm := f.FileInfo().Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

func pluginRootInExtractedDir(root string) (string, error) {
	if _, _, err := pluginpkg.ParseDir(root); err == nil {
		return root, nil
	} else if !isMissingPluginManifest(err) {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if _, _, err := pluginpkg.ParseDir(candidate); err == nil {
			matches = append(matches, candidate)
		} else if !isMissingPluginManifest(err) {
			return "", err
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no %s or %s found in plugin archive", pluginpkg.NativeManifest, pluginpkg.CodexManifest)
	default:
		return "", fmt.Errorf("plugin archive contains multiple plugin roots: %s", strings.Join(matches, ", "))
	}
}

func isMissingPluginManifest(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no "+pluginpkg.NativeManifest+" or "+pluginpkg.CodexManifest+" found")
}
