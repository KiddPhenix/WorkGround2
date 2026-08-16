package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
)

//go:embed all:decision_skill
var decisionSkillRaw embed.FS

const (
	decisionSkillVersion  = "1.0.0"
	decisionSkillProtocol = "decision-broker-local-api-v1"
)

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
	target := filepath.Join(codexDir, "skills", "ask-workground2-owner")
	root, err := fs.Sub(decisionSkillRaw, "decision_skill/ask-workground2-owner")
	if err != nil {
		return AICollaborationInjectResult{}, fmt.Errorf("decision skill embed: %w", err)
	}
	files, err := bundledFilesFromFS(root, ".")
	if err != nil {
		return AICollaborationInjectResult{}, err
	}
	previous := readManifest(target)
	var backups []string
	for _, file := range files {
		backup, writeErr := safeWriteSkillFile(target, file, previous)
		if writeErr != nil {
			return AICollaborationInjectResult{SkillPath: target, Backups: backups}, writeErr
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
		return AICollaborationInjectResult{SkillPath: target, Backups: backups}, err
	}
	if err := atomicWrite(filepath.Join(target, "manifest.json"), string(raw)+"\n"); err != nil {
		return AICollaborationInjectResult{SkillPath: target, Backups: backups}, err
	}
	return AICollaborationInjectResult{OK: true, SkillPath: target, Backups: backups}, nil
}
