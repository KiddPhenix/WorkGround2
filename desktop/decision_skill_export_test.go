package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// readZipEntries returns a name->content map for the archive at path.
func readZipEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]string, len(reader.File))
	for _, file := range reader.File {
		content, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(content); err != nil {
			t.Fatal(err)
		}
		_ = content.Close()
		entries[file.Name] = buf.String()
	}
	return entries
}

func assertZipContains(t *testing.T, entries map[string]string, name, wantSubstring string) {
	t.Helper()
	content, ok := entries[name]
	if !ok {
		t.Fatalf("zip missing %s (have %d entries)", name, len(entries))
	}
	if wantSubstring != "" && !strings.Contains(content, wantSubstring) {
		t.Fatalf("zip entry %s missing %q", name, wantSubstring)
	}
}

func TestExportDecisionSkillsZipContainsBothSkills(t *testing.T) {
	target := filepath.Join(t.TempDir(), decisionSkillExportName)
	if err := exportDecisionSkillsZip(target); err != nil {
		t.Fatal(err)
	}
	entries := readZipEntries(t, target)
	// Both skill directories must be present with their key files, exactly as
	// embedded — the export never depends on an installed Codex skill.
	assertZipContains(t, entries, "ask-workground2-owner/SKILL.md", "name: ask-workground2-owner")
	assertZipContains(t, entries, "ask-workground2-owner/agents/openai.yaml", "")
	assertZipContains(t, entries, "ask-workground2-owner/scripts/decision.ps1", "")
	assertZipContains(t, entries, "notify-me/SKILL.md", "name: notify-me")
	assertZipContains(t, entries, "notify-me/agents/openai.yaml", "")
	assertZipContains(t, entries, "notify-me/scripts/notify.ps1", "")
}

func TestExportDecisionSkillsZipIsDeterministicSafeAndOverwritable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", decisionSkillExportName)
	if err := exportDecisionSkillsZip(target); err != nil {
		t.Fatal(err)
	}
	first := readZipEntries(t, target)
	if len(first) != 6 {
		t.Fatalf("zip entries = %d, want 6 (two skills, three files each)", len(first))
	}
	seen := make(map[string]struct{}, len(first))
	for name := range first {
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") || name == "" {
			t.Fatalf("unsafe zip entry %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("duplicate zip entry %q", name)
		}
		seen[name] = struct{}{}
	}
	firstRaw := mustReadRaw(t, target)
	// Repeated export to the same path must succeed (safe overwrite) and
	// produce byte-identical output (deterministic archive).
	if err := exportDecisionSkillsZip(target); err != nil {
		t.Fatalf("re-export over existing target: %v", err)
	}
	if second := mustReadRaw(t, target); !bytes.Equal(firstRaw, second) {
		t.Fatal("repeated export is not byte-identical")
	}
}

func mustReadRaw(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestExportDecisionSkillsZipFailsWithoutLeavingStaging(t *testing.T) {
	dir := t.TempDir()
	// A directory at the target path makes the final rename fail; the
	// temporary file must still be cleaned up so a retry can succeed.
	target := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exportDecisionSkillsZip(target); err == nil {
		t.Fatal("expected rename-over-directory to fail")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "."+filepath.Base(target)+".wg2tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staging files left behind: %v", matches)
	}
}

func TestExportDecisionSkillsDialogFlows(t *testing.T) {
	dir := t.TempDir()
	original := saveDecisionSkillDialog
	defer func() { saveDecisionSkillDialog = original }()

	t.Run("cancel returns canceled without writing", func(t *testing.T) {
		saveDecisionSkillDialog = func(_ context.Context, _ runtime.SaveDialogOptions) (string, error) {
			return "", nil
		}
		result, err := (&App{ctx: context.Background()}).ExportDecisionSkills()
		if err != nil || !result.Canceled || result.Exported || result.Path != "" {
			t.Fatalf("cancel result=%+v err=%v", result, err)
		}
	})

	t.Run("dialog error surfaces explicitly", func(t *testing.T) {
		saveDecisionSkillDialog = func(_ context.Context, _ runtime.SaveDialogOptions) (string, error) {
			return "", errors.New("dialog exploded")
		}
		result, err := (&App{ctx: context.Background()}).ExportDecisionSkills()
		if err == nil || result.Exported || result.Canceled {
			t.Fatalf("dialog error result=%+v err=%v", result, err)
		}
	})

	t.Run("uses stable default filename and appends zip extension", func(t *testing.T) {
		chosen := filepath.Join(dir, "custom-name") // no extension on purpose
		var seenOptions runtime.SaveDialogOptions
		saveDecisionSkillDialog = func(_ context.Context, options runtime.SaveDialogOptions) (string, error) {
			seenOptions = options
			return chosen, nil
		}
		result, err := (&App{ctx: context.Background()}).ExportDecisionSkills()
		if err != nil || !result.Exported || result.Path != chosen+".zip" {
			t.Fatalf("export result=%+v err=%v", result, err)
		}
		if seenOptions.DefaultFilename != decisionSkillExportName {
			t.Fatalf("default filename = %q, want %q", seenOptions.DefaultFilename, decisionSkillExportName)
		}
		if _, err := os.Stat(result.Path); err != nil {
			t.Fatalf("exported file missing: %v", err)
		}
		entries := readZipEntries(t, result.Path)
		if _, ok := entries["ask-workground2-owner/SKILL.md"]; !ok {
			t.Fatal("exported zip missing ask-workground2-owner/SKILL.md")
		}
	})

	t.Run("app without context is an explicit error", func(t *testing.T) {
		result, err := (&App{}).ExportDecisionSkills()
		if err == nil || result.Canceled || result.Exported {
			t.Fatalf("nil context result=%+v err=%v", result, err)
		}
	})
}
