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

func TestParseDSHBundleManifestAndPatch(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, DSHManifest), `{
  "name": "@deepseek-ai/dsh-test-bundle",
  "version": "0.2.0",
  "description": "DSH test bundle",
  "repository": {"type":"git", "url":"git+https://example.com/dsh.git"},
  "dsh": {"bundle": {"patch": "./cordis.patch.yml"}}
}`)
	writeTestFile(t, filepath.Join(root, "cordis.patch.yml"), `
- insert:
    - id: todo
      name: '@deepseek-ai/dsh-tool-todo'
      config:
        timeout: !!js process.env.DSH_TIMEOUT ?? 1000
    - id: goal-ui
      name: '@deepseek-ai/dsh-client-ui-goal'
- id: todo
  disabled: false
`)
	writeTestFile(t, filepath.Join(root, "node_modules", "@deepseek-ai", "dsh-tool-todo", DSHManifest), `{
  "name": "@deepseek-ai/dsh-tool-todo"
}`)
	writeTestFile(t, filepath.Join(root, "node_modules", "@deepseek-ai", "dsh-client-ui-goal", DSHManifest), `{
  "name": "@deepseek-ai/dsh-client-ui-goal",
  "exports": {"./client": "./lib/client.js"},
  "dsh": {"client": {"platform": "web"}}
}`)

	pkg, warnings, err := ParseDir(root)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if pkg.ManifestKind != "dsh" || pkg.Manifest.Name != "dsh-test-bundle" || pkg.Manifest.Version != "0.2.0" {
		t.Fatalf("pkg = %+v", pkg)
	}
	if pkg.Manifest.Repository != "git+https://example.com/dsh.git" {
		t.Fatalf("repository = %q", pkg.Manifest.Repository)
	}
	dsh := pkg.Manifest.DSH
	if dsh == nil || dsh.PackageName != "@deepseek-ai/dsh-test-bundle" || dsh.Patch != "cordis.patch.yml" {
		t.Fatalf("dsh = %+v", dsh)
	}
	if len(dsh.Rows) != 2 || !dsh.Rows[0].Resolved || !dsh.Rows[1].Client || dsh.Rows[1].ClientEntry != "./client" {
		t.Fatalf("rows = %+v", dsh.Rows)
	}
	if dsh.Report.Level != "L1" || dsh.Report.Status != "recognized" || dsh.Report.Rows != 2 ||
		dsh.Report.ResolvedRows != 2 || dsh.Report.ClientRows != 1 || dsh.Report.DynamicValues != 1 || dsh.Report.OverridePatches != 1 {
		t.Fatalf("report = %+v", dsh.Report)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "left unevaluated") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestParseDSHBundleReportsMissingPackages(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, DSHManifest), `{
  "name": "@vendor/missing-deps",
  "dsh": {"bundle": {"patch": "patch.yml"}}
}`)
	writeTestFile(t, filepath.Join(root, "patch.yml"), `
- insert:
    - id: missing
      name: '@vendor/missing-plugin'
`)

	pkg, warnings, err := ParseDir(root)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if pkg.Manifest.Name != "missing-deps" || pkg.Manifest.DSH.Report.MissingPackages[0] != "@vendor/missing-plugin" {
		t.Fatalf("pkg = %+v", pkg)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not resolvable") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestNodePackageNameSupportsExportsWithoutPathEscape(t *testing.T) {
	tests := map[string]string{
		"@deepseek-ai/dsh-tool-subagent-control/list-agents": "@deepseek-ai/dsh-tool-subagent-control",
		"plain/subpath": "plain",
	}
	for input, want := range tests {
		if got, ok := nodePackageName(input); !ok || got != want {
			t.Fatalf("nodePackageName(%q) = %q, %v; want %q", input, got, ok, want)
		}
	}
	for _, input := range []string{"../outside", "@scope/../outside", "@/bad", "C:\\outside"} {
		if got, ok := nodePackageName(input); ok {
			t.Fatalf("nodePackageName(%q) = %q, true; want rejection", input, got)
		}
	}
}

func TestParseDSHBundleRejectsEscapingOrMissingPatch(t *testing.T) {
	for _, patch := range []string{"../outside.yml", "missing.yml"} {
		t.Run(patch, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, DSHManifest), `{"name":"bad","dsh":{"bundle":{"patch":"`+patch+`"}}}`)
			if _, _, err := ParseDir(root); err == nil || !strings.Contains(err.Error(), "dsh.bundle.patch") {
				t.Fatalf("ParseDir error = %v", err)
			}
		})
	}
}

func TestParseRealDSHBundles(t *testing.T) {
	repo := strings.TrimSpace(os.Getenv("DSH_COMPAT_TEST_ROOT"))
	if repo == "" {
		t.Skip("set DSH_COMPAT_TEST_ROOT to a deepseek-harness checkout")
	}
	for _, name := range []string{"base", "headless", "web-app"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(repo, "packages", "bundle", name)
			pkg, _, err := ParseDir(root)
			if err != nil {
				t.Fatalf("ParseDir(%s): %v", root, err)
			}
			if pkg.ManifestKind != "dsh" || pkg.Manifest.DSH == nil {
				t.Fatalf("pkg = %+v", pkg)
			}
			if pkg.Manifest.DSH.Report.Rows+pkg.Manifest.DSH.Report.OverridePatches == 0 {
				t.Fatalf("empty compatibility report: %+v", pkg.Manifest.DSH.Report)
			}
		})
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
