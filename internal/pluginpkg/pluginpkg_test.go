package pluginpkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fileencoding "workground2/internal/fileutil/encoding"
)

func TestParseCodexSuperpowersManifest(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, CodexManifest), `{
	  "name": "superpowers",
	  "version": "6.1.0",
	  "description": "Planning workflows",
	  "skills": "./skills/"
	}`)
	writeTestFile(t, filepath.Join(root, "hooks", "session-start-codex"), "#!/usr/bin/env bash\n")

	pkg, warnings, err := ParseDir(root)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if pkg.ManifestKind != "codex" || pkg.Manifest.Name != "superpowers" || pkg.Manifest.Version != "6.1.0" {
		t.Fatalf("pkg = %+v", pkg)
	}
	if got := pkg.SkillRoots(); len(got) != 1 || got[0] != filepath.Join(root, "skills") {
		t.Fatalf("SkillRoots = %#v", got)
	}
	if hooks := pkg.Manifest.Hooks["SessionStart"]; len(hooks) != 1 || hooks[0].Command != filepath.Join(root, "hooks", "session-start-codex") {
		t.Fatalf("SessionStart hooks = %+v", hooks)
	}
}

func TestParseDirDecodesGB18030Manifest(t *testing.T) {
	root := t.TempDir()
	manifest := `{"name":"cn-plugin","version":"1.0.0","description":"中文插件"}`
	if err := os.WriteFile(filepath.Join(root, NativeManifest), fileencoding.Encode(manifest, fileencoding.GB18030), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, warnings, err := ParseDir(root)
	if err != nil || len(warnings) != 0 || pkg.Manifest.Description != "中文插件" {
		t.Fatalf("ParseDir = pkg %+v warnings %v err %v", pkg, warnings, err)
	}
}

func TestRejectsEscapingSkillPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, NativeManifest), `{
	  "name": "bad",
	  "skills": "../skills"
	}`)
	if _, _, err := ParseDir(root); err == nil {
		t.Fatal("ParseDir should reject escaping skill path")
	}
}

func TestParseNativeAddOnManifest(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, NativeManifest), `{
	  "name": "team-skill-share",
	  "version": "1.2.0",
	  "description": "Shared skills",
	  "skills": ["skills"],
	  "addon": {
	    "kind": "skill-share",
	    "displayName": "Team Skill Share",
	    "capabilities": ["skills", "update", "settings"],
	    "runtime": { "type": "mcp", "mcpServer": "skill-share-runtime" },
	    "panels": [
	      { "id": "skill-share", "title": "Skill Share", "entry": "panels/skill-share" }
	    ],
	    "configSchema": "config.schema.json",
	    "secrets": [
	      { "id": "git-credential", "label": "Git credential", "purpose": "Read shared skill repository", "required": true }
	    ],
	    "update": {
	      "type": "git",
	      "strategy": "replace",
	      "check": "manual-or-startup",
	      "credential": "git-credential"
	    },
	    "storage": {
	      "namespace": "team-skill-share"
	    }
	  },
	  "mcpServers": {
	    "skill-share-runtime": { "command": "bin/skill-share-runtime" }
	  }
	}`)

	pkg, warnings, err := ParseDir(root)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	addon := pkg.Manifest.AddOn
	if addon == nil {
		t.Fatal("addon metadata should be parsed")
	}
	if addon.Kind != "skill-share" || addon.DisplayName != "Team Skill Share" || addon.ConfigSchema != "config.schema.json" {
		t.Fatalf("addon = %+v", addon)
	}
	if got := strings.Join(addon.Capabilities, ","); got != "skills,update,settings" {
		t.Fatalf("capabilities = %q", got)
	}
	if len(addon.Panels) != 1 || addon.Panels[0].ID != "skill-share" || addon.Panels[0].Entry != "panels/skill-share" {
		t.Fatalf("panels = %+v", addon.Panels)
	}
	if len(addon.Secrets) != 1 || addon.Secrets[0].ID != "git-credential" || !addon.Secrets[0].Required {
		t.Fatalf("secrets = %+v", addon.Secrets)
	}
	if addon.Runtime == nil || addon.Runtime.Type != "mcp" || addon.Runtime.MCPServer != "skill-share-runtime" {
		t.Fatalf("runtime = %+v", addon.Runtime)
	}
	if addon.Update == nil || addon.Update.Type != "git" || addon.Update.Credential != "git-credential" {
		t.Fatalf("update = %+v", addon.Update)
	}
	if addon.Storage == nil || addon.Storage.Namespace != "team-skill-share" {
		t.Fatalf("storage = %+v", addon.Storage)
	}
	if panels, secrets := pkg.AddOnCounts(); panels != 1 || secrets != 1 {
		t.Fatalf("AddOnCounts = %d, %d; want 1, 1", panels, secrets)
	}
}

func TestRejectsAddOnRuntimeMissingMCPServer(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, NativeManifest), `{
	  "name": "bad-addon",
	  "addon": {
	    "kind": "draw-tool",
	    "runtime": { "type": "mcp", "mcpServer": "draw-runtime" }
	  }
	}`)
	if _, _, err := ParseDir(root); err == nil || !strings.Contains(err.Error(), "not declared in mcpServers") {
		t.Fatalf("ParseDir error = %v, want missing mcpServers runtime error", err)
	}
}

func TestStateRoundTripSortsPlugins(t *testing.T) {
	home := t.TempDir()
	if err := Upsert(home, InstalledPlugin{Name: "zeta", Root: "plugins/zeta", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(home, InstalledPlugin{Name: "alpha", Root: "plugins/alpha", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Plugins) != 2 || st.Plugins[0].Name != "alpha" || st.Plugins[1].Name != "zeta" {
		t.Fatalf("state plugins = %+v", st.Plugins)
	}
}

func TestStateRoundTripRuntimeFields(t *testing.T) {
	home := t.TempDir()
	want := InstalledPlugin{
		Name:            "team-skill-share",
		Root:            "plugins/team-skill-share",
		Version:         "1.2.0",
		Enabled:         true,
		InstalledAt:     "2026-07-04T01:02:03Z",
		LastCheckedAt:   "2026-07-04T02:03:04Z",
		LastUpdatedAt:   "2026-07-04T03:04:05Z",
		LastError:       "network unavailable",
		UpdateAvailable: true,
		RemoteVersion:   "1.3.0",
	}
	if err := Upsert(home, want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(StatePath(home))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"installedAt", "lastCheckedAt", "lastUpdatedAt", "lastError", "updateAvailable", "remoteVersion"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("state JSON missing %q: %s", key, raw)
		}
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Plugins) != 1 {
		t.Fatalf("plugins = %+v", st.Plugins)
	}
	got := st.Plugins[0]
	if got.InstalledAt != want.InstalledAt || got.LastCheckedAt != want.LastCheckedAt ||
		got.LastUpdatedAt != want.LastUpdatedAt || got.LastError != want.LastError ||
		!got.UpdateAvailable || got.RemoteVersion != want.RemoteVersion {
		t.Fatalf("runtime fields = %+v", got)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
