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
