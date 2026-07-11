package config

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/pluginpkg"
)

func TestLoadMergesInstalledPluginSkillRootsAndMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	root := filepath.Join(home, "plugins", "superpowers")
	writeConfigTestFile(t, filepath.Join(root, pluginpkg.NativeManifest), `{
  "name": "superpowers",
  "version": "1.0.0",
  "skills": "skills",
  "mcpServers": {
    "helper": { "command": "bin/helper" }
  },
  "addon": {
    "kind": "skill-share",
    "storage": { "namespace": "team-skill-share" },
    "secrets": [
      { "id": "git-credential", "label": "Git credential", "purpose": "Read shared skill repository" }
    ]
  }
}`)
	if err := pluginpkg.Upsert(home, pluginpkg.InstalledPlugin{
		Name:         "superpowers",
		Root:         "plugins/superpowers",
		Version:      "1.0.0",
		ManifestKind: "WorkGround2",
		Enabled:      true,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Skills.Paths) == 0 || cfg.Skills.Paths[len(cfg.Skills.Paths)-1] != filepath.Join(root, "skills") {
		t.Fatalf("skills paths = %#v", cfg.Skills.Paths)
	}
	var found bool
	for _, p := range cfg.Plugins {
		if p.Name == "helper" {
			found = true
			if p.Command != filepath.Join(root, "bin", "helper") {
				t.Fatalf("plugin command = %q", p.Command)
			}
			if p.Env["WorkGround2_PLUGIN_NAME"] != "superpowers" {
				t.Fatalf("plugin env = %#v", p.Env)
			}
			if p.Env["WORKGROUND2_HOME"] != home || p.Env["WorkGround2_HOME"] != home || p.Env["WORKGROUND2_PLUGIN_ROOT"] != root || p.Env["WORKGROUND2_PLUGIN_NAME"] != "superpowers" {
				t.Fatalf("stable plugin env = %#v", p.Env)
			}
			if p.Env["WorkGround2_ADDON_KIND"] != "skill-share" || p.Env["WorkGround2_ADDON_STORAGE"] != "team-skill-share" {
				t.Fatalf("addon env = %#v", p.Env)
			}
			if p.Env["WORKGROUND2_ADDON_KIND"] != "skill-share" || p.Env["WORKGROUND2_ADDON_STORAGE_NAMESPACE"] != "team-skill-share" {
				t.Fatalf("stable addon env = %#v", p.Env)
			}
			if p.Env["WORKGROUND2_ADDON_HOME"] != filepath.Join(home, "addons", "team-skill-share") ||
				p.Env["WORKGROUND2_ADDON_CONFIG_DIR"] != filepath.Join(home, "addons", "team-skill-share", "config") ||
				p.Env["WORKGROUND2_ADDON_DATA_DIR"] != filepath.Join(home, "addons", "team-skill-share", "data") ||
				p.Env["WORKGROUND2_ADDON_STATE_DIR"] != filepath.Join(home, "addons", "team-skill-share", "state") {
				t.Fatalf("addon dirs env = %#v", p.Env)
			}
			if _, ok := p.Env["git-credential"]; ok {
				t.Fatalf("secret declaration leaked into env: %#v", p.Env)
			}
		}
	}
	if !found {
		t.Fatalf("plugin MCP server missing: %#v", cfg.Plugins)
	}
}

func writeConfigTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
