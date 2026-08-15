package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/config"
	"workground2/internal/fileutil"
	"workground2/internal/work"
)

// openExternalBrowser opens a validated URL through the Wails Desktop browser
// handler. Kept as a variable so tests can record calls without a Wails
// runtime context.
var openExternalBrowser = func(ctx context.Context, url string) error {
	if ctx == nil {
		return errors.New("work artifact: browser context is unavailable")
	}
	wruntime.BrowserOpenURL(ctx, url)
	return nil
}

// WorkArtifactFileIntent identifies one immutable artifact revision. Host file
// actions never accept a caller-provided path.
type WorkArtifactFileIntent struct {
	WorkID             string `json:"workId"`
	DefinitionRevision int64  `json:"definitionRevision"`
	SlotID             string `json:"slotId"`
	SlotRevision       int64  `json:"slotRevision"`
	ArtifactRefID      string `json:"artifactRefId"`
}

// OpenWorkArtifactForTab opens an authoritative Work artifact with the OS
// default application.
func (a *App) OpenWorkArtifactForTab(tabID string, input WorkArtifactFileIntent) error {
	path, err := a.resolveWorkArtifactFile(tabID, input)
	if err != nil {
		return err
	}
	return openWorkspacePath(path)
}

// RevealWorkArtifactForTab selects an authoritative Work artifact in the
// native file manager.
func (a *App) RevealWorkArtifactForTab(tabID string, input WorkArtifactFileIntent) error {
	path, err := a.resolveWorkArtifactFile(tabID, input)
	if err != nil {
		return err
	}
	return revealPath(path)
}

// OpenWorkArtifactURLForTab opens an authoritative URL artifact in the Desktop
// browser. The URL is resolved from the Work projection by the same strict
// slot/revision/ref identity as file artifacts — a caller-provided URL is
// never trusted. Only available refs with a valid absolute http(s) URL open;
// file-only refs fail with an explicit error.
func (a *App) OpenWorkArtifactURLForTab(tabID string, input WorkArtifactFileIntent) error {
	if err := validateWorkArtifactFileIntent(input); err != nil {
		return err
	}
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return err
	}
	view, err := wc.GetWork(a.bootContext(), input.WorkID)
	if err != nil {
		return fmt.Errorf("work artifact: load authoritative view: %w", err)
	}
	ref, ok := findWorkArtifactRef(view, input)
	if !ok {
		return errors.New("work artifact: artifact revision changed or is unavailable")
	}
	if ref.Status != work.ArtifactRefStatusAvailable {
		return fmt.Errorf("work artifact: artifact status is %q", ref.Status)
	}
	target := strings.TrimSpace(ref.URL)
	if !work.ValidateArtifactURL(target) {
		return errors.New("work artifact: authoritative artifact URL is invalid or unsafe")
	}
	return openExternalBrowser(a.ctx, target)
}

func (a *App) resolveWorkArtifactFile(tabID string, input WorkArtifactFileIntent) (string, error) {
	if err := validateWorkArtifactFileIntent(input); err != nil {
		return "", err
	}
	root, _, ok := a.workspaceTargetForTab(tabID)
	if !ok {
		return "", os.ErrNotExist
	}
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return "", err
	}
	view, err := wc.GetWork(a.bootContext(), input.WorkID)
	if err != nil {
		return "", fmt.Errorf("work artifact: load authoritative view: %w", err)
	}
	ref, ok := findWorkArtifactRef(view, input)
	if !ok {
		return "", errors.New("work artifact: artifact revision changed or is unavailable")
	}
	if ref.Status == work.ArtifactRefStatusMissing || ref.Status == work.ArtifactRefStatusFailed {
		return "", fmt.Errorf("work artifact: artifact status is %q", ref.Status)
	}
	// BlobDigest is authoritative when both source forms are present, matching
	// the shared artifact resolver and preventing a stale path from winning.
	if strings.TrimSpace(ref.BlobDigest) != "" {
		workDir := config.ProjectWorkDir(root)
		if workDir == "" {
			return "", errors.New("work artifact: project data directory is unavailable")
		}
		return materializeWorkArtifactBlob(workDir, input.WorkID, *ref)
	}
	if strings.TrimSpace(ref.RelativePath) != "" {
		path, allowed, pathErr := a.secureWorkspaceOrExternalPathForTab(tabID, ref.RelativePath)
		if pathErr != nil {
			return "", pathErr
		}
		if !allowed {
			return "", os.ErrInvalid
		}
		return requireRegularFile(path)
	}
	return "", errors.New("work artifact: artifact has no readable file source")
}

func validateWorkArtifactFileIntent(input WorkArtifactFileIntent) error {
	if strings.TrimSpace(input.WorkID) == "" ||
		strings.TrimSpace(input.SlotID) == "" ||
		strings.TrimSpace(input.ArtifactRefID) == "" ||
		input.DefinitionRevision <= 0 ||
		input.SlotRevision <= 0 {
		return errors.New("work artifact: complete artifact identity is required")
	}
	return nil
}

func findWorkArtifactRef(view *work.WorkView, input WorkArtifactFileIntent) (*work.ArtifactRef, bool) {
	if view == nil {
		return nil, false
	}
	// The V2 transport projection may keep slots under the Work; fall back the
	// same way the task artifact reporter does.
	slots := view.ArtifactSlots
	if len(slots) == 0 && view.Work != nil {
		slots = view.Work.V2ArtifactSlots
	}
	for i := range slots {
		slot := &slots[i]
		if slot.WorkID != input.WorkID ||
			slot.DefinitionRev != input.DefinitionRevision ||
			slot.ID != input.SlotID ||
			slot.Revision != input.SlotRevision {
			continue
		}
		for j := range slot.ArtifactRefs {
			if slot.ArtifactRefs[j].ID == input.ArtifactRefID {
				return &slot.ArtifactRefs[j], true
			}
		}
	}
	return nil, false
}

func materializeWorkArtifactBlob(workDir, workID string, ref work.ArtifactRef) (string, error) {
	store, err := work.NewFileWorkStore(workDir, 0)
	if err != nil {
		return "", err
	}
	data, err := store.Get(workID, ref.BlobDigest)
	if err != nil {
		return "", fmt.Errorf("work artifact: read blob: %w", err)
	}
	hash := strings.TrimPrefix(ref.BlobDigest, "sha256:")
	if len(hash) < 16 {
		return "", errors.New("work artifact: invalid blob digest")
	}
	target := filepath.Join(store.WorkDir(), workID, "files", hash[:16], safeArtifactFileName(ref.Name, ref.Type))
	if current, readErr := os.ReadFile(target); readErr == nil && bytes.Equal(current, data) {
		_ = os.Chmod(target, 0o444)
		return target, nil
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return "", fmt.Errorf("work artifact: inspect local file: %w", readErr)
	}
	// The path is content-addressed and managed by WorkGround2. Repair a
	// modified cache copy from the immutable authoritative blob.
	_ = os.Chmod(target, 0o600)
	if err := fileutil.AtomicWriteFile(target, data, 0o444); err != nil {
		return "", fmt.Errorf("work artifact: materialize local file: %w", err)
	}
	if err := os.Chmod(target, 0o444); err != nil {
		return "", fmt.Errorf("work artifact: protect local file: %w", err)
	}
	return target, nil
}

func safeArtifactFileName(name, mediaType string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." {
		name = "artifact" + artifactExtension(mediaType)
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(strings.TrimSpace(name), ". ")
	if name == "" {
		name = "artifact" + artifactExtension(mediaType)
	}
	runes := []rune(name)
	if len(runes) > 180 {
		ext := filepath.Ext(name)
		extRunes := []rune(ext)
		name = strings.TrimSpace(string(runes[:180-len(extRunes)])) + ext
	}
	return name
}

func artifactExtension(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx":
		return ".xlsx"
	case "text/markdown", "markdown", "md":
		return ".md"
	case "text/plain", "text", "txt":
		return ".txt"
	case "application/pdf", "pdf":
		return ".pdf"
	default:
		return ""
	}
}

func requireRegularFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("work artifact: source is not a regular file")
	}
	return path, nil
}
