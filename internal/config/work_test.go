package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestWorkConfigDefaultOn(t *testing.T) {
	cfg := Default()
	if cfg == nil {
		t.Fatal("Default() returned nil")
	}
	if !cfg.Work.Enabled {
		t.Fatal("Work.Enabled must default to true")
	}
}

func TestWorkConfigLoadStates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	t.Setenv("WorkGround2_PREFER_USER_CONFIG", "")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "WorkGround2.toml")
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{name: "missing section", body: "config_version = 3\n", want: true},
		{name: "missing enabled", body: "[work]\n", want: true},
		{name: "explicit true", body: "[work]\nenabled = true\n", want: true},
		{name: "explicit false", body: "[work]\nenabled = false\n", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadForRoot(root)
			if err != nil {
				t.Fatalf("LoadForRoot: %v", err)
			}
			if cfg.Work.Enabled != tc.want {
				t.Fatalf("Work.Enabled = %v, want %v", cfg.Work.Enabled, tc.want)
			}
		})
	}
}

func TestWorkConfigTOMLRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "true", false: "false"}[enabled], func(t *testing.T) {
			cfg := Default()
			cfg.Work.Enabled = enabled
			dir := t.TempDir()
			path := filepath.Join(dir, "WorkGround2.toml")
			if err := cfg.WriteFile(path); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			loaded, err := LoadForRoot(dir)
			if err != nil {
				t.Fatalf("LoadForRoot: %v", err)
			}
			if loaded.Work.Enabled != enabled {
				t.Fatalf("Work.Enabled = %v after round-trip, want %v", loaded.Work.Enabled, enabled)
			}
		})
	}
}

func TestWorkConfigPriorityAndRepeatedLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	t.Setenv("WorkGround2_PREFER_USER_CONFIG", "")
	userPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(userPath, []byte("[work]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	projectPath := filepath.Join(root, "WorkGround2.toml")
	if err := os.WriteFile(projectPath, []byte("config_version = 3\n\n[work]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		cfg, err := LoadForRoot(root)
		if err != nil {
			t.Fatalf("LoadForRoot #%d: %v", i+1, err)
		}
		if cfg.Work.Enabled {
			t.Fatalf("LoadForRoot #%d overwrote explicit user false", i+1)
		}
	}
	if err := os.WriteFile(projectPath, []byte("[work]\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot project override: %v", err)
	}
	if !cfg.Work.Enabled {
		t.Fatal("project explicit true did not override user false")
	}
	t.Setenv("WorkGround2_PREFER_USER_CONFIG", "1")
	cfg, err = LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot prefer user: %v", err)
	}
	if cfg.Work.Enabled {
		t.Fatal("preferred user explicit false did not override project true")
	}
}

func TestWorkConfigProjectDeltaPreservesExplicitFalse(t *testing.T) {
	cfg := Default()
	if delta := RenderTOMLProjectDelta(cfg); strings.Contains(delta, "[work]") {
		t.Fatalf("default-on Work should be omitted from project delta:\n%s", delta)
	}
	cfg.Work.Enabled = false
	delta := RenderTOMLProjectDelta(cfg)
	if !strings.Contains(delta, "[work]\nenabled = false") {
		t.Fatalf("project delta lost explicit Work opt-out:\n%s", delta)
	}
	loaded := Default()
	if _, err := toml.Decode(delta, loaded); err != nil {
		t.Fatalf("decode project delta: %v", err)
	}
	if loaded.Work.Enabled {
		t.Fatal("project delta did not override default-on Work")
	}
}

// ── V2 collaboration workbench flag ────────────────────────────────────────

func TestWorkConfigCollaborationWorkbenchV2DefaultOn(t *testing.T) {
	cfg := Default()
	if !cfg.Work.CollaborationWorkbenchV2 {
		t.Fatal("collaboration_workbench_v2 must default to true")
	}
}

func TestWorkConfigCollaborationWorkbenchV2RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Default-on survives round-trip.
	cfg := Default()
	dir := t.TempDir()
	path := filepath.Join(dir, "WorkGround2.toml")
	if err := cfg.WriteFile(path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded, err := LoadForRoot(dir)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if !loaded.Work.CollaborationWorkbenchV2 {
		t.Fatal("CollabV2 must default to true after round-trip")
	}

	// Explicit true round-trip via real WriteFile → LoadForRoot path.
	for _, v := range []bool{true, false} {
		cfg2 := Default()
		cfg2.Work.Enabled = false
		cfg2.Work.CollaborationWorkbenchV2 = v
		dir2 := t.TempDir()
		path2 := filepath.Join(dir2, "WorkGround2.toml")
		if err := cfg2.WriteFile(path2); err != nil {
			t.Fatalf("WriteFile(v=%v): %v", v, err)
		}
		loaded2, err := LoadForRoot(dir2)
		if err != nil {
			t.Fatalf("LoadForRoot(v=%v): %v", v, err)
		}
		if loaded2.Work.CollaborationWorkbenchV2 != v {
			t.Fatalf("CollabV2 = %v after round-trip, want %v",
				loaded2.Work.CollaborationWorkbenchV2, v)
		}
	}
}

func TestWorkConfigCollaborationWorkbenchV2LoadExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "WorkGround2.toml")
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"missing", "config_version = 3\n", true},
		{"missing section", "[work]\n", true},
		{"explicit true", "[work]\ncollaboration_workbench_v2 = true\n", true},
		{"explicit false", "[work]\ncollaboration_workbench_v2 = false\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadForRoot(root)
			if err != nil {
				t.Fatalf("LoadForRoot: %v", err)
			}
			if cfg.Work.CollaborationWorkbenchV2 != tc.want {
				t.Fatalf("CollabV2 = %v, want %v",
					cfg.Work.CollaborationWorkbenchV2, tc.want)
			}
		})
	}
}

// TestWorkConfigCollaborationWorkbenchV2Delta verifies that explicit false
// (opt-out from the default-on V2) appears in project delta, while the
// default true is omitted.
func TestWorkConfigCollaborationWorkbenchV2Delta(t *testing.T) {
	cfg := Default()
	// Default-on: no [work] section in delta when all fields match defaults.
	if delta := RenderTOMLProjectDelta(cfg); strings.Contains(delta, "[work]") {
		t.Fatalf("default-on V2 Work should be omitted from project delta:\n%s", delta)
	}

	// Explicit false must appear in delta.
	cfg.Work.CollaborationWorkbenchV2 = false
	delta := RenderTOMLProjectDelta(cfg)
	if !strings.Contains(delta, "[work]\nenabled = true") {
		t.Fatalf("project delta lost enabled line when V2 is explicit false:\n%s", delta)
	}
	if !strings.Contains(delta, "collaboration_workbench_v2 = false") {
		t.Fatalf("project delta lost explicit V2 opt-out:\n%s", delta)
	}

	// Round-trip: decode delta over default -> V2 stays false.
	loaded := Default()
	if _, err := toml.Decode(delta, loaded); err != nil {
		t.Fatalf("decode project delta: %v", err)
	}
	if loaded.Work.CollaborationWorkbenchV2 {
		t.Fatal("project delta did not override default-on V2")
	}
	if !loaded.Work.Enabled {
		t.Fatal("project delta should not change Enabled default")
	}
}
