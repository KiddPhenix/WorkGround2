package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed all:ai_collaboration_skill
var aiCollaborationSkillRaw embed.FS

const (
	aiCollaborationStart         = "<!-- WORKGROUND2_AI_COLLABORATION_BEGIN -->"
	aiCollaborationEnd           = "<!-- WORKGROUND2_AI_COLLABORATION_END -->"
	aiCollaborationBundleVersion = "1.0.0"
)

type AICollaborationInjectResult struct {
	OK        bool     `json:"ok"`
	Path      string   `json:"path"`
	SkillPath string   `json:"skillPath,omitempty"`
	Backups   []string `json:"backups,omitempty"`
}

// bundledFile holds one embedded asset and its pre-computed SHA-256.
type bundledFile struct {
	name    string // relative path inside ai_collaboration_skill/
	content string
	sha256  string
}

// canonicalBundle is populated at init from the embedded filesystem.
var canonicalBundle []bundledFile

func init() {
	skillFS, err := fs.Sub(aiCollaborationSkillRaw, "ai_collaboration_skill")
	if err != nil {
		panic(fmt.Sprintf("ai_collaboration_skill embed: %v", err))
	}
	canonicalBundle, err = bundledFilesFromFS(skillFS, ".")
	if err != nil {
		panic(fmt.Sprintf("ai_collaboration_skill files: %v", err))
	}
}

func bundledFilesFromFS(fsys fs.FS, root string) ([]bundledFile, error) {
	var out []bundledFile
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		content := string(data)
		sum := sha256.Sum256(data)
		out = append(out, bundledFile{
			name:    filepath.ToSlash(path),
			content: content,
			sha256:  hex.EncodeToString(sum[:]),
		})
		return nil
	})
	return out, err
}

// AICollaborationPrompt returns the prompt users can paste into Codex or another
// AI tool. It contains the exact canonical SKILL.md, references/cli.md, and
// scripts/dispatch.ps1 contents, plus the bundle version and SHA-256 hashes.
// The receiving AI must write these bytes faithfully to install the skill.
func (a *App) AICollaborationPrompt() string {
	return aiCollaborationPrompt(workground2CLIPath())
}

// InjectAICollaborationPrompt installs the canonical skill bundle into the
// detected Codex skills directory and injects the compact runtime rules into
// the global AGENTS.md. Installation is idempotent, retryable, and preserves
// user-modified files via backup.
func (a *App) InjectAICollaborationPrompt() (AICollaborationInjectResult, error) {
	cliPath := workground2CLIPath()
	codexDir, err := codexHomeDir()
	if err != nil {
		return AICollaborationInjectResult{}, err
	}
	return injectAICollaboration(codexDir, cliPath)
}

func injectAICollaboration(codexDir, cliPath string) (AICollaborationInjectResult, error) {
	skillDir := filepath.Join(codexDir, "skills", "workground2-worker")
	backups, err := installSkillBundle(skillDir)
	if err != nil {
		return AICollaborationInjectResult{}, err
	}

	agentsPath := filepath.Join(codexDir, "AGENTS.md")
	result := AICollaborationInjectResult{
		Path:      agentsPath,
		SkillPath: skillDir,
		Backups:   backups,
	}
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		return result, err
	}
	if err := injectRuntimeBlock(agentsPath, cliPath); err != nil {
		return result, err
	}

	result.OK = true
	return result, nil
}

// aiCollaborationPrompt builds the copy prompt containing exact canonical
// asset contents, bundle version, and SHA-256 hashes.
func aiCollaborationPrompt(cliPath string) string {
	cli := strings.TrimSpace(cliPath)
	if cli == "" {
		cli = "workground2"
	}
	bt := "`"

	var b strings.Builder

	// Header: version, CLI path, and instruction.
	b.WriteString("WorkGround2 Worker Skill Bundle v")
	b.WriteString(aiCollaborationBundleVersion)
	b.WriteString("\nCLI: ")
	b.WriteString(cli)
	b.WriteString("\n\n")
	b.WriteString("## Install instruction\n\n")
	b.WriteString("Write every file below to the Codex global skills directory ")
	b.WriteString("(default " + bt + "~/.codex/skills/workground2-worker/" + bt + "), ")
	b.WriteString("then add the runtime AGENTS.md block. ")
	b.WriteString("Each file is a verbatim fenced block: use the exact SHA-256, ")
	b.WriteString("version, path, and content. ")
	b.WriteString("Before replacing differing existing content, preserve it as .bak (or the next free .bak.N). ")
	b.WriteString("Write through temporary files, verify every SHA-256, use UTF-8 without BOM, preserve LF line endings, and write manifest.json last. ")
	b.WriteString("Do not paraphrase, summarise, or regenerate any file.\n\n")

	for _, bf := range canonicalBundle {
		writePromptFile(&b, bf.name, bf.sha256, bf.content)
	}
	manifest := bundleManifestJSON()
	manifestSum := sha256.Sum256([]byte(manifest))
	writePromptFile(&b, "manifest.json", hex.EncodeToString(manifestSum[:]), manifest)

	// Runtime AGENTS.md block.
	b.WriteString("---\n")
	b.WriteString("Path: AGENTS.md runtime block\n")
	b.WriteString("Insert or replace the block between ")
	b.WriteString(aiCollaborationStart)
	b.WriteString(" and ")
	b.WriteString(aiCollaborationEnd)
	b.WriteString(" in the global AGENTS.md. ")
	b.WriteString("If the file does not exist, create it.\n\n")
	b.WriteString("```markdown\n")
	b.WriteString(aiCollaborationRuntimePrompt(cli))
	b.WriteString("\n```\n")

	return b.String()
}

func writePromptFile(b *strings.Builder, name, hash, content string) {
	b.WriteString("---\nPath: ")
	b.WriteString(name)
	b.WriteString("\nSHA-256: ")
	b.WriteString(hash)
	fence := promptFence(content)
	b.WriteString("\n")
	b.WriteString(fence)
	switch {
	case strings.HasSuffix(name, ".md"):
		b.WriteString("markdown")
	case strings.HasSuffix(name, ".ps1"):
		b.WriteString("powershell")
	case strings.HasSuffix(name, ".json"):
		b.WriteString("json")
	}
	b.WriteString("\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(fence)
	b.WriteString("\n\n")
}

func promptFence(content string) string {
	longest := 0
	run := 0
	for _, r := range content {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	if longest < 3 {
		longest = 3
	}
	return strings.Repeat("`", longest+1)
}

// aiCollaborationRuntimePrompt returns the compact runtime rules for the
// global AGENTS.md. It includes the resolved CLI path and does not contain
// installation or skill-generation instructions.
func aiCollaborationRuntimePrompt(cliPath string) string {
	cli := strings.TrimSpace(cliPath)
	if cli == "" {
		cli = "workground2"
	}
	return strings.TrimSpace(fmt.Sprintf(`## WorkGround2 Worker

CLI: %s

Use the installed `+"`workground2-worker`"+` skill for eligible code, documentation, configuration, and test changes. Codex defines a compact outcome/scope/acceptance packet, WorkGround2 inspects and implements, and Codex reviews only the scoped diff plus final validation.

If the skill is unavailable, dispatch directly with `+"`desktop new --workspace <root> --session-name <display-name> --yolo --no-wait <packet>`"+`, capture the returned SessionID, then poll `+"`desktop status --session <id> --json`"+`. The name is display-only; every desktop new creates a fresh SessionID. Exit 0 means dispatched; resolve returned interactions with the same SessionID and wait for foreground work to end.

Keep Codex context small: do not duplicate repository background or pre-solve delegated implementation; prefer one dispatch, one compact worker report, `+"`git diff --stat`"+`, scoped diff review, and one validation pass. Read full worker transcripts or files only after failure.

Skip delegation for tiny/read-only, multimodal/GUI, secrets/security/release/git-publish, or under-specified work. Use the current workspace root, a display-only session name, YOLO, and asynchronous dispatch. Treat exit code 0 as dispatched; complete when foreground work and interactions end. Background-only jobs do not block completion.`, cli))
}

// installSkillBundle writes canonical files into skillDir. It is idempotent:
// files already matching the canonical SHA-256 are skipped. Files that differ
// from both the canonical and any previously-installed version are backed up
// as .bak before overwriting. A manifest.json recording the bundle version and
// file hashes is written last.
func installSkillBundle(skillDir string) (backups []string, err error) {
	prevManifest := readManifest(skillDir)
	for _, bf := range canonicalBundle {
		backup, err := safeWriteSkillFile(skillDir, bf, prevManifest)
		if err != nil {
			return backups, err
		}
		if backup != "" {
			backups = append(backups, backup)
		}
	}
	if err := writeManifest(skillDir); err != nil {
		return backups, err
	}
	return backups, nil
}

// safeWriteSkillFile writes content to target if it differs from the canonical
// hash. If the existing file was modified by the user (differs from both
// canonical and previous manifest), it is backed up before overwriting.
func safeWriteSkillFile(skillDir string, file bundledFile, prevManifest map[string]string) (backup string, err error) {
	target := filepath.Join(skillDir, filepath.FromSlash(file.name))
	existing, readErr := os.ReadFile(target)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}

	if readErr == nil {
		existingSum := fmt.Sprintf("%x", sha256.Sum256(existing))
		if existingSum == file.sha256 {
			return "", nil // already canonical
		}
		// Check if the existing file matches a previous manifest version.
		prevHash := prevManifest[file.name]
		if prevHash != "" && existingSum == prevHash {
			// Previously installed version — safe to overwrite.
		} else {
			// User-modified or unknown — back up.
			backup, err = backupFile(target)
			if err != nil {
				return "", fmt.Errorf("backup %s: %w", target, err)
			}
		}
	}

	// Write via staging for atomic replacement.
	if err := atomicWrite(target, file.content); err != nil {
		return "", err
	}
	return backup, nil
}

// atomicWrite writes content to a staging file then renames it onto target.
func atomicWrite(target, content string) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content = strings.TrimPrefix(content, "\uFEFF")
	stagingFile, err := os.CreateTemp(dir, "."+filepath.Base(target)+".wg2tmp-*")
	if err != nil {
		return err
	}
	staging := stagingFile.Name()
	closed := false
	defer func() {
		if !closed {
			_ = stagingFile.Close()
		}
		_ = os.Remove(staging)
	}()
	if err := stagingFile.Chmod(0o644); err != nil {
		return err
	}
	if _, err := stagingFile.Write([]byte(content)); err != nil {
		return err
	}
	if err := stagingFile.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(staging, target); err != nil {
		return err
	}
	return nil
}

// backupFile copies src to dst byte-for-byte for recoverable backup.
func backupFile(src string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	for i := 0; ; i++ {
		suffix := ".bak"
		if i > 0 {
			suffix = fmt.Sprintf(".bak.%d", i)
		}
		dst := src + suffix
		existing, readErr := os.ReadFile(dst)
		if readErr == nil {
			if fmt.Sprintf("%x", sha256.Sum256(existing)) == fmt.Sprintf("%x", sha256.Sum256(data)) {
				return dst, nil
			}
			continue
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", readErr
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return "", err
		}
		return dst, nil
	}
}

// manifestEntry is written to manifest.json in the skill directory.
type manifestEntry struct {
	Version         string            `json:"version"`
	ProtocolVersion string            `json:"protocolVersion"`
	Files           map[string]string `json:"files"` // relative path → SHA-256
}

func readManifest(skillDir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(skillDir, "manifest.json"))
	if err != nil {
		return nil
	}
	var m manifestEntry
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m.Files
}

func writeManifest(skillDir string) error {
	return atomicWrite(filepath.Join(skillDir, "manifest.json"), bundleManifestJSON())
}

func bundleManifestJSON() string {
	m := manifestEntry{
		Version:         aiCollaborationBundleVersion,
		ProtocolVersion: "desktop-session-id-v1",
		Files:           make(map[string]string),
	}
	for _, bf := range canonicalBundle {
		m.Files[bf.name] = bf.sha256
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("marshal AI collaboration manifest: %v", err))
	}
	data = append(data, '\n')
	return string(data)
}

// injectRuntimeBlock inserts or replaces the compact runtime block in the
// global AGENTS.md.
func injectRuntimeBlock(agentsPath, cliPath string) error {
	existing, err := os.ReadFile(agentsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := replaceAICollaborationBlock(string(existing), aiCollaborationRuntimePrompt(cliPath))
	return atomicWrite(agentsPath, next)
}

// ── existing helpers (unchanged) ─────────────────────────────────────────────

func workground2CLIPath() string {
	if path := existingWorkground2CLIPath(workground2CLICandidates()); path != "" {
		return path
	}
	if path, err := exec.LookPath("workground2"); err == nil && strings.TrimSpace(path) != "" {
		return path
	}
	return "workground2"
}

func existingWorkground2CLIPath(candidates []string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if abs, err := filepath.Abs(candidate); err == nil {
			candidate = abs
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func workground2CLICandidates() []string {
	cwd, _ := os.Getwd()
	exe, _ := os.Executable()
	return workground2CLICandidatesFor(os.Getenv("WORKGROUND2_CLI"), cwd, exe)
}

func workground2CLICandidatesFor(envPath, cwd, exePath string) []string {
	var out []string
	if envPath != "" {
		out = append(out, envPath)
	}
	if exePath != "" && filepath.Base(exePath) == "WorkGround2.exe" {
		out = append(out, exePath)
	}
	if cwd != "" {
		out = append(out,
			filepath.Join(cwd, "desktop", "build", "bin", "WorkGround2.exe"),
			filepath.Join(cwd, "desktop", "build", "bin", "workground2.exe"),
			filepath.Join(cwd, "build", "bin", "WorkGround2.exe"),
			filepath.Join(cwd, "build", "bin", "workground2.exe"),
			filepath.Join(cwd, "workground2.exe"),
			filepath.Join(cwd, "bin", "workground2.exe"),
			filepath.Join(cwd, "..", "workground2.exe"),
			filepath.Join(cwd, "..", "bin", "workground2.exe"),
		)
	}
	if exePath != "" {
		dir := filepath.Dir(exePath)
		out = append(out,
			filepath.Join(dir, "WorkGround2.exe"),
			filepath.Join(dir, "workground2.exe"),
			filepath.Join(dir, "bin", "workground2.exe"),
			filepath.Join(dir, "..", "desktop", "build", "bin", "WorkGround2.exe"),
			filepath.Join(dir, "..", "desktop", "build", "bin", "workground2.exe"),
			filepath.Join(dir, "..", "build", "bin", "WorkGround2.exe"),
			filepath.Join(dir, "..", "build", "bin", "workground2.exe"),
			filepath.Join(dir, "..", "workground2.exe"),
			filepath.Join(dir, "..", "bin", "workground2.exe"),
		)
	}
	return out
}

func replaceAICollaborationBlock(existing, prompt string) string {
	block := aiCollaborationStart + "\n" + strings.TrimSpace(prompt) + "\n" + aiCollaborationEnd
	start := strings.Index(existing, aiCollaborationStart)
	if start >= 0 {
		endRel := strings.Index(existing[start:], aiCollaborationEnd)
		if endRel >= 0 {
			end := start + endRel + len(aiCollaborationEnd)
			before := strings.TrimRight(existing[:start], " \t\r\n")
			after := strings.TrimLeft(existing[end:], " \t\r\n")
			return joinNonEmptyBlocks(before, block, after)
		}
	}
	return joinNonEmptyBlocks(existing, block)
}

func joinNonEmptyBlocks(parts ...string) string {
	var out []string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n\n") + "\n"
}

func codexHomeDir() (string, error) {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home, nil
	}
	for _, dir := range codexHomeCandidates() {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".codex"), nil
	}
	return "", errors.New("codex home not found")
}

func codexHomeCandidates() []string {
	out := []string{
		`D:\Codex\.codex`,
		`C:\Codex\.codex`,
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".codex"))
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		out = append(out, filepath.Join(appData, "Codex"))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		out = append(out, filepath.Join(local, "OpenAI", "Codex"))
	}
	return out
}
