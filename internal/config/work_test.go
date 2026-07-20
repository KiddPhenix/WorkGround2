package config

import (
	"path/filepath"
	"testing"
)

func TestWorkConfigDefaultOff(t *testing.T) {
	cfg := Default()
	if cfg == nil {
		t.Fatal("Default() returned nil")
	}
	if cfg.Work.Enabled {
		t.Fatal("Work.Enabled must default to false")
	}
}

func TestWorkConfigExplicitOn(t *testing.T) {
	cfg := Default()
	cfg.Work.Enabled = true
	if !cfg.Work.Enabled {
		t.Fatal("Work.Enabled should be true after setting")
	}
}

func TestWorkConfigTOMLRoundTrip(t *testing.T) {
	// Verify the TOML tag is correct and the field participates in serialization.
	cfg := Default()
	cfg.Work.Enabled = true

	// Write the project-level config that LoadForRoot will pick up.
	dir := t.TempDir()
	path := filepath.Join(dir, "WorkGround2.toml")
	if err := cfg.WriteFile(path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loaded, err := LoadForRoot(dir)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if !loaded.Work.Enabled {
		t.Fatal("Work.Enabled should survive TOML round-trip")
	}
}
