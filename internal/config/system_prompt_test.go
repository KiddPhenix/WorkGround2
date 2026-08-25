package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	fileencoding "workground2/internal/fileutil/encoding"
)

func TestResolveSystemPromptForRootRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompts", "session.md"), []byte(" project session prompt \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	cfg := Default()
	cfg.Agent.SystemPromptFile = filepath.Join("prompts", "session.md")

	got, err := cfg.ResolveSystemPromptForRoot(root)
	if err != nil {
		t.Fatalf("ResolveSystemPromptForRoot: %v", err)
	}
	if got != "project session prompt" {
		t.Fatalf("system prompt = %q, want %q", got, "project session prompt")
	}
}

func TestResolveSystemPromptForRootAbsolutePath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.md")
	if err := os.WriteFile(path, []byte(" absolute session prompt \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	cfg := Default()
	cfg.Agent.SystemPromptFile = path

	got, err := cfg.ResolveSystemPromptForRoot(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveSystemPromptForRoot: %v", err)
	}
	if got != "absolute session prompt" {
		t.Fatalf("system prompt = %q, want %q", got, "absolute session prompt")
	}
}

func TestResolveSystemPromptForRootDecodesGB18030(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "prompts", "session.md")
	if err := os.WriteFile(path, fileencoding.Encode(" 请始终使用中文回答。 \n", fileencoding.GB18030), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Agent.SystemPromptFile = filepath.Join("prompts", "session.md")
	got, err := cfg.ResolveSystemPromptForRoot(root)
	if err != nil {
		t.Fatalf("ResolveSystemPromptForRoot: %v", err)
	}
	if got != "请始终使用中文回答。" {
		t.Fatalf("system prompt = %q", got)
	}
}

func TestBrowserPolicyCoversNativeFirstNoReload(t *testing.T) {
	for _, want := range []string{
		"browser_* tools",
		"do not launch or use Playwright for covered operations",
		"Playwright is fallback-only",
		"unavailable, explicitly fail, or lack a required capability",
		"same WorkGround2 browser via browser_attach",
		"Never reload, refresh, or navigate to the same URL merely to observe",
		"browser_state(refresh=true) only re-observes page state and never reloads the page",
		"Reload a page only when the user requested it or the page is unusable",
	} {
		if !strings.Contains(BrowserPolicy, want) {
			t.Fatalf("BrowserPolicy missing %q:\n%s", want, BrowserPolicy)
		}
	}
}
