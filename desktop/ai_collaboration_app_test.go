package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalBundleHasAllAssets(t *testing.T) {
	names := make(map[string]bool)
	for _, bf := range canonicalBundle {
		names[bf.name] = true
	}
	for _, want := range []string{
		"SKILL.md",
		"references/cli.md",
		"scripts/dispatch.ps1",
	} {
		if !names[want] {
			t.Fatalf("canonicalBundle missing %q", want)
		}
	}
	if len(canonicalBundle) != 3 {
		t.Fatalf("canonicalBundle has %d files, want 3", len(canonicalBundle))
	}
}

func TestCanonicalBundleHashConsistency(t *testing.T) {
	for _, bf := range canonicalBundle {
		data, err := fs.ReadFile(aiCollaborationSkillRaw, "ai_collaboration_skill/"+bf.name)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", bf.name, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(data))
		if bf.sha256 != sum {
			t.Fatalf("hash mismatch for %q:\n stored: %s\n computed: %s", bf.name, bf.sha256, sum)
		}
		if bf.content != string(data) {
			t.Fatalf("content mismatch for %q", bf.name)
		}
	}
}

func TestAICollaborationPromptContainsExactAssets(t *testing.T) {
	prompt := aiCollaborationPrompt(`D:\Work\WorkGround2\workground2.exe`)

	// Must contain bundle version.
	if !strings.Contains(prompt, aiCollaborationBundleVersion) {
		t.Fatalf("prompt missing bundle version %q", aiCollaborationBundleVersion)
	}

	// Must contain every canonical file verbatim.
	for _, bf := range canonicalBundle {
		if !strings.Contains(prompt, bf.content) {
			t.Fatalf("prompt missing exact content of %q", bf.name)
		}
		if !strings.Contains(prompt, bf.sha256) {
			t.Fatalf("prompt missing SHA-256 of %q", bf.name)
		}
		fence := promptFence(bf.content)
		if !strings.Contains(prompt, fence) {
			t.Fatalf("prompt missing safe fence for %q", bf.name)
		}
	}
	manifest := bundleManifestJSON()
	manifestSum := sha256.Sum256([]byte(manifest))
	if !strings.Contains(prompt, "Path: manifest.json") ||
		!strings.Contains(prompt, manifest) ||
		!strings.Contains(prompt, hex.EncodeToString(manifestSum[:])) {
		t.Fatal("prompt missing exact manifest.json content or hash")
	}
	if !strings.Contains(prompt, "write manifest.json last") {
		t.Fatal("prompt does not require manifest-last installation")
	}
	if !strings.Contains(prompt, "preserve it as .bak") || !strings.Contains(prompt, "verify every SHA-256") {
		t.Fatal("prompt does not preserve modified files and verify installed bytes")
	}

	// Must contain CLI path.
	if !strings.Contains(prompt, `D:\Work\WorkGround2\workground2.exe`) {
		t.Fatalf("prompt missing CLI path")
	}

	// Must contain install instruction.
	if !strings.Contains(prompt, "Write every file below") {
		t.Fatalf("prompt missing install instruction")
	}
	for _, want := range []string{
		"Never delegate Skill installation, Skill creation or updates, or design file generation to WorkGround2",
		"even when WorkGround2 is explicitly requested",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing direct-execution rule %q", want)
		}
	}

	// Must contain the runtime AGENTS.md block.
	if !strings.Contains(prompt, aiCollaborationStart) {
		t.Fatalf("prompt missing runtime block markers")
	}

	// Must NOT contain old meta-contract instructions (outside fenced blocks).
	for _, unwanted := range []string{
		"## Compact skill requirements",                       // old meta-instruction
		"Write SKILL.md with the following concise content:",  // old meta-generation
		"Keep the generated runtime skill small:",             // old meta-generation
		"Do not copy installation, migration, path discovery", // old meta-constraint
		"Do not duplicate detailed CLI examples",              // old meta-constraint
		"Keep PowerShell implementation details inside",       // old meta-constraint
		"## dispatch.ps1 responsibilities",                    // old meta-contract
		"## references/cli.md responsibilities",               // old meta-contract
		"Do not reproduce the PowerShell implementation",      // old meta-contract
		"Do not repeat general ownership",                     // old meta-contract
		"generate/write SKILL with responsibilities",          // explicit meta-contract
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt should not contain %q", unwanted)
		}
	}
	cli := bundleFile(t, "references/cli.md")
	if len(promptFence(cli.content)) <= 3 {
		t.Fatal("nested CLI code fences are not protected by a longer outer fence")
	}
}

func bundleFile(t *testing.T, name string) bundledFile {
	t.Helper()
	for _, file := range canonicalBundle {
		if file.name == name {
			return file
		}
	}
	t.Fatalf("bundle file %q not found", name)
	return bundledFile{}
}

func TestAICollaborationPromptDeclaresSessionIDSemantics(t *testing.T) {
	prompt := aiCollaborationPrompt(`D:\wg.exe`)

	// SessionID must be explicitly required.
	for _, want := range []string{
		"display-only session name",
		"every desktop new creates a fresh SessionID",
		"SessionID",
		"--session",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing SessionID contract %q", want)
		}
	}

	// Must NOT suggest session names deduplicate.
	for _, unwanted := range []string{
		"stable session name",
		"session names are stable identifiers",
		"session name persists across",
		"reuse the session name",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt contains ambiguous session-identity wording: %q", unwanted)
		}
	}
}

func TestAICollaborationPromptDoesNotContainMetaContract(t *testing.T) {
	prompt := aiCollaborationPrompt(`D:\wg.exe`)

	// The old prompt told Codex to write/generate SKILL.md with responsibilities.
	// The new prompt gives exact file contents to copy.
	if strings.Contains(prompt, "## Compact skill requirements") ||
		strings.Contains(prompt, "dispatch.ps1 responsibilities") ||
		strings.Contains(prompt, "references/cli.md responsibilities") {
		t.Fatalf("prompt still contains meta-generation contract")
	}

	// The old prompt described what SKILL.md should contain as guidelines.
	// The new prompt gives the exact bytes.
	if strings.Contains(prompt, "## dispatch.ps1 responsibilities\n\nKeep all mechanical behavior") {
		t.Fatalf("prompt contains dispatch.ps1 meta-contract instead of exact bytes")
	}
}

func TestRuntimePromptStaysCompact(t *testing.T) {
	prompt := aiCollaborationRuntimePrompt(`D:\Work\WorkGround2\desktop\build\bin\WorkGround2.exe`)
	for _, want := range []string{
		"## WorkGround2 Worker",
		`CLI: D:\Work\WorkGround2\desktop\build\bin\WorkGround2.exe`,
		"installed `workground2-worker` skill",
		"If the skill is unavailable",
		"desktop new --workspace <root>",
		"desktop status --session <id> --json",
		"git diff --stat",
		"Background-only jobs do not block completion",
		"display-only session name",
		"every desktop new creates a fresh SessionID",
		"Never delegate Skill installation/creation/update",
		"design file generation",
		"even when WorkGround2 is explicitly requested; perform those tasks directly",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("runtime prompt missing %q:\n%s", want, prompt)
		}
	}
	if lines := strings.Count(prompt, "\n") + 1; lines > 12 {
		t.Fatalf("runtime prompt has %d lines, want at most 12:\n%s", lines, prompt)
	}
	// No installation or skill-generation details.
	if strings.Contains(prompt, "## Compact skill requirements") ||
		strings.Contains(prompt, "dispatch.ps1 responsibilities") ||
		strings.Contains(prompt, "meta-contract") {
		t.Fatalf("runtime prompt contains installation details:\n%s", prompt)
	}
}

func TestBundledSkillSkipsSkillManagementAndDesignFiles(t *testing.T) {
	skill := bundleFile(t, "SKILL.md").content
	for _, want := range []string{
		"Never use it for Skill installation, creation, or updates or for design file generation",
		"Even when WorkGround2 is explicitly requested",
		"install, create, or update Skills directly and generate design files directly",
		"Never delegate either category to WorkGround2",
	} {
		if !strings.Contains(skill, want) {
			t.Fatalf("bundled skill missing direct-execution rule %q", want)
		}
	}
}

func TestReplaceAICollaborationBlockIsIdempotent(t *testing.T) {
	first := replaceAICollaborationBlock("# Existing\n", "one")
	second := replaceAICollaborationBlock(first, "two")
	if strings.Count(second, aiCollaborationStart) != 1 || strings.Count(second, aiCollaborationEnd) != 1 {
		t.Fatalf("markers should appear once:\n%s", second)
	}
	if strings.Contains(second, "one") || !strings.Contains(second, "two") {
		t.Fatalf("block should be replaced:\n%s", second)
	}
	if !strings.Contains(second, "# Existing") {
		t.Fatalf("existing content should be preserved:\n%s", second)
	}
}

func TestInjectRuntimeBlockIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")
	cliPath := filepath.Join(dir, "wg.exe")

	// First inject creates the file.
	if err := injectRuntimeBlock(agentsPath, cliPath); err != nil {
		t.Fatal(err)
	}
	content1, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content1), aiCollaborationStart) {
		t.Fatalf("first inject missing markers:\n%s", string(content1))
	}

	// Second inject is idempotent.
	if err := injectRuntimeBlock(agentsPath, cliPath); err != nil {
		t.Fatal(err)
	}
	content2, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Markers should appear exactly once.
	if strings.Count(string(content2), aiCollaborationStart) != 1 {
		t.Fatalf("markers appear %d times after second inject", strings.Count(string(content2), aiCollaborationStart))
	}

	// Content should match.
	if string(content1) != string(content2) {
		t.Fatalf("idempotent inject changed content:\nbefore: %s\nafter: %s", string(content1), string(content2))
	}
}

func TestInjectAICollaborationInstallsBundleAndRuntime(t *testing.T) {
	codexDir := t.TempDir()
	result, err := injectAICollaboration(codexDir, `D:\Apps\WorkGround2.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.SkillPath == "" || result.Path == "" || len(result.Backups) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, file := range canonicalBundle {
		data, err := os.ReadFile(filepath.Join(result.SkillPath, filepath.FromSlash(file.name)))
		if err != nil || string(data) != file.content {
			t.Fatalf("installed %q mismatch: err=%v", file.name, err)
		}
	}
	agents, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), `CLI: D:\Apps\WorkGround2.exe`) {
		t.Fatalf("runtime block missing CLI path:\n%s", agents)
	}
	second, err := injectAICollaboration(codexDir, `D:\Apps\WorkGround2.exe`)
	if err != nil || len(second.Backups) != 0 {
		t.Fatalf("idempotent inject failed: result=%+v err=%v", second, err)
	}
}

func TestInstallSkillBundleIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "workground2-worker")

	// First install.
	conflicts, err := installSkillBundle(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("first install had conflicts: %v", conflicts)
	}

	// Verify all files exist.
	for _, bf := range canonicalBundle {
		target := filepath.Join(skillDir, filepath.FromSlash(bf.name))
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("missing file %q after install: %v", bf.name, err)
		}
		if string(data) != bf.content {
			t.Fatalf("content mismatch for %q", bf.name)
		}
	}

	// Second install — no conflicts, no changes.
	conflicts2, err := installSkillBundle(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts2) != 0 {
		t.Fatalf("second install had conflicts: %v", conflicts2)
	}

	// Manifest should exist.
	manifestPath := filepath.Join(skillDir, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest.json not found: %v", err)
	}
}

func TestInstallSkillBundleBacksUpModifiedFiles(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "workground2-worker")

	// First install.
	_, err := installSkillBundle(skillDir)
	if err != nil {
		t.Fatal(err)
	}

	// Modify SKILL.md.
	target := filepath.Join(skillDir, "SKILL.md")
	modified := "modified by user\n"
	if err := os.WriteFile(target, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second install should detect conflict and back up.
	conflicts, err := installSkillBundle(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := target + ".bak"
	if len(conflicts) != 1 || conflicts[0] != backupPath {
		t.Fatalf("expected backup %q, got %v", backupPath, conflicts)
	}

	// Backup file should exist with modified content.
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("backup %q not found: %v", backupPath, err)
	}
	if string(backupData) != modified {
		t.Fatalf("backup content mismatch: got %q, want %q", string(backupData), modified)
	}

	// Target should be restored to canonical.
	current, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != bundleFile(t, "SKILL.md").content {
		t.Fatalf("target not restored to canonical after backup")
	}
}

func TestSafeWriteReplacesManifestOwnedNestedFileWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "workground2-worker")
	name := "references/cli.md"
	target := filepath.Join(skillDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	oldContent := "old version content\n"
	if err := os.WriteFile(target, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}
	oldSum := sha256.Sum256([]byte(oldContent))
	newContent := "new canonical content\n"
	newSum := sha256.Sum256([]byte(newContent))
	backup, err := safeWriteSkillFile(skillDir, bundledFile{
		name: name, content: newContent, sha256: hex.EncodeToString(newSum[:]),
	}, map[string]string{name: hex.EncodeToString(oldSum[:])})
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("manifest-owned file unexpectedly backed up to %q", backup)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != newContent {
		t.Fatalf("nested file was not updated: %q", data)
	}
}

func TestInstallSkillBundlePreservesDistinctBackups(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "skills", "workground2-worker")
	if _, err := installSkillBundle(skillDir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(skillDir, "SKILL.md")
	for i, content := range []string{"first custom version\n", "second custom version\n"} {
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		backups, err := installSkillBundle(skillDir)
		if err != nil || len(backups) != 1 {
			t.Fatalf("install %d: backups=%v err=%v", i, backups, err)
		}
		data, err := os.ReadFile(backups[0])
		if err != nil || string(data) != content {
			t.Fatalf("backup %d mismatch: data=%q err=%v", i, data, err)
		}
	}
}

func TestAtomicWriteStripsBOM(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "test.md")
	content := "\uFEFF# Header\nBody\n"
	if err := atomicWrite(target, content); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(data), "\uFEFF") {
		t.Fatalf("BOM was not stripped")
	}
	if string(data) != "# Header\nBody\n" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestAtomicWriteReplacesExistingFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "test.md")
	if err := atomicWrite(target, "old\n"); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, "new\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new\n" {
		t.Fatalf("replace failed: data=%q err=%v", data, err)
	}
}

func TestWorkground2CLIPathPrefersDesktopBuildOverRepoRootStub(t *testing.T) {
	root := t.TempDir()
	desktopExe := filepath.Join(root, "desktop", "build", "bin", "WorkGround2.exe")
	rootExe := filepath.Join(root, "workground2.exe")
	if err := os.MkdirAll(filepath.Dir(desktopExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktopExe, []byte("desktop"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootExe, []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := existingWorkground2CLIPath(workground2CLICandidatesFor("", root, rootExe))
	if got != desktopExe {
		t.Fatalf("expected desktop build exe before repo root stub\nwant: %s\n got: %s", desktopExe, got)
	}
}

func TestWorkground2CLIPathUsesEnvOverrideFirst(t *testing.T) {
	root := t.TempDir()
	envExe := filepath.Join(root, "custom.exe")
	desktopExe := filepath.Join(root, "desktop", "build", "bin", "WorkGround2.exe")
	if err := os.MkdirAll(filepath.Dir(desktopExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envExe, []byte("env"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktopExe, []byte("desktop"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := existingWorkground2CLIPath(workground2CLICandidatesFor(envExe, root, ""))
	if got != envExe {
		t.Fatalf("expected WORKGROUND2_CLI override first\nwant: %s\n got: %s", envExe, got)
	}
}

func TestBundleVersionIsConstant(t *testing.T) {
	if aiCollaborationBundleVersion == "" {
		t.Fatal("bundle version is empty")
	}
	// Version should be valid semver-like.
	parts := strings.Split(aiCollaborationBundleVersion, ".")
	if len(parts) != 3 {
		t.Fatalf("bundle version %q is not semver-like", aiCollaborationBundleVersion)
	}
}

func TestPromptContainsRuntimeBlockWithBundledAssets(t *testing.T) {
	prompt := aiCollaborationPrompt(`D:\wg.exe`)

	// The prompt should include the runtime AGENTS.md block with the markers.
	if !strings.Contains(prompt, aiCollaborationStart) {
		t.Fatal("prompt missing runtime block markers")
	}

	// The runtime block in the prompt should contain the compact rules.
	if !strings.Contains(prompt, "## WorkGround2 Worker") {
		t.Fatal("prompt missing runtime block header")
	}

	// No duplicate blocks.
	if strings.Count(prompt, aiCollaborationStart) != 1 {
		t.Fatalf("runtime block markers appear %d times, want 1", strings.Count(prompt, aiCollaborationStart))
	}
}

func TestManifestJSONIsValid(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "workground2-worker")

	_, err := installSkillBundle(skillDir)
	if err != nil {
		t.Fatal(err)
	}

	manifest := readManifest(skillDir)
	if manifest == nil {
		t.Fatal("manifest could not be read")
	}
	if len(manifest) != len(canonicalBundle) {
		t.Fatalf("manifest has %d files, want %d", len(manifest), len(canonicalBundle))
	}
	for _, bf := range canonicalBundle {
		if manifest[bf.name] != bf.sha256 {
			t.Fatalf("manifest hash mismatch for %q: %s != %s", bf.name, manifest[bf.name], bf.sha256)
		}
	}
	data, err := os.ReadFile(filepath.Join(skillDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != bundleManifestJSON() {
		t.Fatal("installed manifest differs from exported canonical manifest")
	}
	var entry manifestEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.ProtocolVersion != "desktop-session-id-v1" {
		t.Fatalf("unexpected protocol version %q", entry.ProtocolVersion)
	}
}
