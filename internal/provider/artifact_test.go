package provider

import (
	"path/filepath"
	"testing"
)

func TestCodexGeneratedImagesRootUsesExplicitHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if got, want := CodexGeneratedImagesRoot(), filepath.Join(home, "generated_images"); got != want {
		t.Fatalf("root = %q, want %q", got, want)
	}
}

func TestCodexGeneratedImagesRootFallsBackToUserHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	got := CodexGeneratedImagesRoot()
	if got == "" || filepath.Base(got) != "generated_images" || filepath.Base(filepath.Dir(got)) != ".codex" {
		t.Fatalf("unexpected fallback root %q", got)
	}
}
