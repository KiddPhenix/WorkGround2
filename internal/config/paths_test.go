package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectWorkDirEmpty(t *testing.T) {
	// Empty root returns empty.
	if got := ProjectWorkDir(""); got != "" {
		t.Fatalf("ProjectWorkDir(\"\") = %q, want \"\"", got)
	}
}

func TestProjectWorkDirUsesProjectDataRoute(t *testing.T) {
	home := isolateUserConfigHome(t)
	root := t.TempDir()

	got := ProjectWorkDir(root)
	if got == "" {
		t.Fatal("ProjectWorkDir returned empty")
	}

	// Must be under the project data route, not in the git workspace.
	wantPrefix := filepath.Join(expectedDefaultWorkGround2Home(home), "projects")
	if !strings.HasPrefix(filepath.ToSlash(got), filepath.ToSlash(wantPrefix)) {
		t.Fatalf("ProjectWorkDir = %q, want prefix %q", got, wantPrefix)
	}
	if strings.HasPrefix(filepath.ToSlash(got), filepath.ToSlash(root)) {
		t.Fatalf("ProjectWorkDir %q must not be under the git workspace %q", got, root)
	}

	// Should end with /works.
	if !strings.HasSuffix(filepath.ToSlash(got), "/works") {
		t.Fatalf("ProjectWorkDir = %q, want /works suffix", got)
	}
}

func TestProjectWorkDirDefaultsNotInRepo(t *testing.T) {
	// Verify that ProjectWorkDir never returns a path inside the workspace root
	// itself — it always routes through the project data directory.
	root := t.TempDir()

	// Create a .workground2 dir in the workspace to confirm we ignore it.
	if err := os.MkdirAll(filepath.Join(root, ".workground2"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := ProjectWorkDir(root)
	if got == "" {
		t.Skip("ProjectWorkDir returned empty — MemoryUserDir unavailable")
	}

	// Must not be inside the workspace root.
	if strings.HasPrefix(filepath.ToSlash(got), filepath.ToSlash(root)+"/") {
		t.Fatalf("ProjectWorkDir %q must not be inside the workspace root %q", got, root)
	}

	// Must not be .workground2 inside the root.
	if strings.Contains(got, ".workground2") || strings.Contains(got, ".WorkGround2") {
		t.Fatalf("ProjectWorkDir %q must not create .workground2 in the workspace", got)
	}
}

func TestProjectWorkDirWindowsPath(t *testing.T) {
	isolateUserConfigHome(t)

	// Simulate a Windows-style root.
	root := `C:\Users\test\MyProject`
	got := ProjectWorkDir(root)
	if got == "" {
		t.Fatal("ProjectWorkDir with Windows root returned empty")
	}
	if strings.Contains(got, ":") {
		// The slug replaces ':' with '-', so the final path should be safe.
		// Just check it doesn't panic and produces a path.
		if !strings.Contains(got, "works") {
			t.Fatalf("ProjectWorkDir = %q, want to contain 'works'", got)
		}
	}

	// Verify it's under the project data route, not in the workspace.
	if strings.HasPrefix(filepath.ToSlash(got), filepath.ToSlash(root)) {
		t.Fatalf("ProjectWorkDir %q must not be under the workspace root", got)
	}
}

func TestProjectWorkDirUnicodePath(t *testing.T) {
	isolateUserConfigHome(t)
	root := filepath.Join(t.TempDir(), "项目", "测试")

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	got := ProjectWorkDir(root)
	if got == "" {
		t.Fatal("ProjectWorkDir with unicode root returned empty")
	}
	// Must not be the root itself.
	if filepath.Clean(got) == filepath.Clean(root) {
		t.Fatalf("ProjectWorkDir = %q, must not equal root", got)
	}
}

func TestProjectVocabularyDirUsesPrivateProjectState(t *testing.T) {
	home := isolateUserConfigHome(t)
	root := t.TempDir()

	got := ProjectVocabularyDir(root)
	wantPrefix := filepath.Join(expectedDefaultWorkGround2Home(home), "projects", WorkspaceSlug(root))
	if !strings.HasPrefix(filepath.Clean(got), filepath.Clean(wantPrefix)) || !strings.HasSuffix(filepath.ToSlash(got), "/vocabulary") {
		t.Fatalf("ProjectVocabularyDir = %q, want %q/vocabulary", got, wantPrefix)
	}
	if strings.HasPrefix(filepath.Clean(got), filepath.Clean(root)+string(filepath.Separator)) {
		t.Fatalf("learned vocabulary must stay outside workspace: %q", got)
	}
}
