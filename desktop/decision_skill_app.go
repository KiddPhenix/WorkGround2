package main

import (
	"archive/zip"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:decision_skill
var decisionSkillRaw embed.FS

const (
	decisionSkillVersion  = "1.2.0"
	decisionSkillProtocol = "decision-broker-local-api-v1"
	// decisionSkillExportName is the stable default filename for the
	// distributable ZIP; a user-chosen path keeps whatever the OS save dialog
	// returns, so this only matters as the default.
	decisionSkillExportName  = "workground2-owner-skills.zip"
	decisionSkillExportTitle = "导出主人决策 Skill"
)

// DecisionSkillExportResult describes the outcome of ExportDecisionSkills:
// Exported is true only after the ZIP was written, Canceled is true when the
// user dismissed the save dialog, and Path carries the written file when
// exported. A hard failure returns both false together with an error.
type DecisionSkillExportResult struct {
	Exported bool   `json:"exported"`
	Canceled bool   `json:"canceled"`
	Path     string `json:"path,omitempty"`
}

// saveDecisionSkillDialog is a variable so tests can replace the native save
// dialog (same seam as openWorkInputFileDialog in works.go).
var saveDecisionSkillDialog = runtime.SaveFileDialog

// ExportDecisionSkills exports the two bundled decision skills
// (ask-workground2-owner and notify-me) as one distributable ZIP through the
// native save dialog. It does not depend on any previously installed Codex
// skill. A cancelled dialog returns Canceled=true with no error; write failures
// return an explicit error and clean up any temporary file, so the user can
// safely retry.
func (a *App) ExportDecisionSkills() (DecisionSkillExportResult, error) {
	if a == nil || a.ctx == nil {
		return DecisionSkillExportResult{}, errors.New("app context is unavailable")
	}
	target, err := saveDecisionSkillDialog(a.ctx, runtime.SaveDialogOptions{
		Title:                decisionSkillExportTitle,
		DefaultFilename:      decisionSkillExportName,
		CanCreateDirectories: true,
		Filters:              []runtime.FileFilter{{DisplayName: "ZIP 压缩包", Pattern: "*.zip"}},
	})
	if err != nil {
		return DecisionSkillExportResult{}, fmt.Errorf("选择保存位置失败: %w", err)
	}
	if strings.TrimSpace(target) == "" {
		return DecisionSkillExportResult{Canceled: true}, nil
	}
	if filepath.Ext(target) == "" {
		target += ".zip"
	}
	if err := exportDecisionSkillsZip(target); err != nil {
		return DecisionSkillExportResult{}, err
	}
	return DecisionSkillExportResult{Exported: true, Path: target}, nil
}

// exportDecisionSkillsZip writes one deterministic ZIP containing both decision
// skill directories (with SKILL.md, agents/, scripts/, ...) to target. The
// archive is staged in a temporary file in the same directory and renamed onto
// target, so a failed or interrupted write never leaves a partial ZIP and a
// repeated export safely overwrites the previous file.
func exportDecisionSkillsZip(target string) error {
	files, err := decisionSkillBundleFiles()
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	staging, err := os.CreateTemp(dir, "."+filepath.Base(target)+".wg2tmp-*")
	if err != nil {
		return err
	}
	stagingName := staging.Name()
	defer os.Remove(stagingName) // cleanup on any failure path
	// Distributable archive: make it world-readable on Unix (a fresh temp file
	// would otherwise default to 0600). No effect on Windows.
	if err := staging.Chmod(0o644); err != nil {
		_ = staging.Close()
		return err
	}
	if err := writeDecisionSkillZip(staging, files); err != nil {
		_ = staging.Close()
		return err
	}
	if err := staging.Close(); err != nil {
		return err
	}
	return os.Rename(stagingName, target)
}

// decisionSkillBundleFiles collects every embedded file of both skills with a
// zip entry name prefixed by its skill directory, sorted for a deterministic
// archive. Names come from the embed filesystem, but each entry is still
// validated (no absolute path, no "..", no backslash) and checked for
// duplicates before any write.
func decisionSkillBundleFiles() ([]bundledFile, error) {
	root, err := fs.Sub(decisionSkillRaw, "decision_skill")
	if err != nil {
		return nil, fmt.Errorf("decision skill embed: %w", err)
	}
	var files []bundledFile
	for _, skillName := range []string{"ask-workground2-owner", "notify-me"} {
		skillRoot, err := fs.Sub(root, skillName)
		if err != nil {
			return nil, fmt.Errorf("decision skill %s embed: %w", skillName, err)
		}
		entries, err := bundledFilesFromFS(skillRoot, ".")
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			entry.name = path.Join(skillName, entry.name)
			files = append(files, entry)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if !safeZipEntryName(file.name) {
			return nil, fmt.Errorf("unsafe zip entry %q", file.name)
		}
		if _, duplicate := seen[file.name]; duplicate {
			return nil, fmt.Errorf("duplicate zip entry %q", file.name)
		}
		seen[file.name] = struct{}{}
	}
	return files, nil
}

func safeZipEntryName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	cleaned := path.Clean(name)
	return cleaned == name && cleaned != "." && !strings.HasPrefix(cleaned, "../")
}

// writeDecisionSkillZip streams the archive with a fixed entry timestamp and
// sorted entries, so identical inputs always produce identical bytes.
func writeDecisionSkillZip(w io.Writer, files []bundledFile) error {
	zw := zip.NewWriter(w)
	fixedModified := time.Unix(0, 0).UTC()
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate, Modified: fixedModified}
		header.SetMode(0o644)
		entry, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := entry.Write([]byte(file.content)); err != nil {
			return err
		}
	}
	return zw.Close()
}

// InstallDecisionSkill installs the bundled Agent client into Codex's global
// skills directory. Repeated installs are idempotent; locally modified files
// are backed up by the shared safe skill writer before replacement.
func (a *App) InstallDecisionSkill() (AICollaborationInjectResult, error) {
	codexDir, err := codexHomeDir()
	if err != nil {
		return AICollaborationInjectResult{}, err
	}
	return installDecisionSkill(codexDir)
}

func installDecisionSkill(codexDir string) (AICollaborationInjectResult, error) {
	askPath, askBackups, err := installBundledDecisionSkill(codexDir, "ask-workground2-owner")
	if err != nil {
		return AICollaborationInjectResult{SkillPath: askPath, Backups: askBackups}, err
	}
	_, notifyBackups, err := installBundledDecisionSkill(codexDir, "notify-me")
	backups := append(askBackups, notifyBackups...)
	if err != nil {
		return AICollaborationInjectResult{SkillPath: askPath, Backups: backups}, err
	}
	return AICollaborationInjectResult{OK: true, SkillPath: askPath, Backups: backups}, nil
}

func installBundledDecisionSkill(codexDir, name string) (string, []string, error) {
	target := filepath.Join(codexDir, "skills", name)
	root, err := fs.Sub(decisionSkillRaw, "decision_skill/"+name)
	if err != nil {
		return target, nil, fmt.Errorf("decision skill %s embed: %w", name, err)
	}
	files, err := bundledFilesFromFS(root, ".")
	if err != nil {
		return target, nil, err
	}
	previous := readManifest(target)
	var backups []string
	for _, file := range files {
		backup, writeErr := safeWriteSkillFile(target, file, previous)
		if writeErr != nil {
			return target, backups, writeErr
		}
		if backup != "" {
			backups = append(backups, backup)
		}
	}
	manifest := manifestEntry{Version: decisionSkillVersion, ProtocolVersion: decisionSkillProtocol, Files: make(map[string]string, len(files))}
	for _, file := range files {
		manifest.Files[file.name] = file.sha256
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return target, backups, err
	}
	if err := atomicWrite(filepath.Join(target, "manifest.json"), string(raw)+"\n"); err != nil {
		return target, backups, err
	}
	return target, backups, nil
}
