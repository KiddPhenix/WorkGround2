package main

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/config"
)

func writeBackgroundTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func enabledBackgroundSource(kind, path string, recursive bool) config.DesktopSessionBackgroundSource {
	enabled := true
	return config.DesktopSessionBackgroundSource{Kind: kind, Path: path, Enabled: &enabled, Recursive: recursive}
}

func backgroundConfig(sources ...config.DesktopSessionBackgroundSource) config.DesktopSessionBackgroundConfig {
	mask, random := true, true
	return config.DesktopSessionBackgroundConfig{Enabled: true, MaskEnabled: &mask, RandomOnOpen: &random, Sources: sources}
}

func TestScanSessionBackgroundDeduplicatesAndReportsSources(t *testing.T) {
	root := t.TempDir()
	explicit := filepath.Join(root, "one.png")
	nested := filepath.Join(root, "nested", "two.webp")
	ignored := filepath.Join(root, "notes.txt")
	writeBackgroundTestFile(t, explicit)
	writeBackgroundTestFile(t, nested)
	writeBackgroundTestFile(t, ignored)

	bg := backgroundConfig(
		enabledBackgroundSource(config.SessionBackgroundSourceFile, explicit, false),
		enabledBackgroundSource(config.SessionBackgroundSourceFolder, root, true),
		enabledBackgroundSource(config.SessionBackgroundSourceFile, filepath.Join(root, "missing.jpg"), false),
	)
	catalog := scanSessionBackground(bg)
	if len(catalog.images) != 2 {
		t.Fatalf("images = %d, want 2: %+v", len(catalog.images), catalog.images)
	}
	if catalog.sources[0].ImageCount != 1 || catalog.sources[1].ImageCount != 1 {
		t.Fatalf("source counts = %+v", catalog.sources)
	}
	if catalog.sources[2].Error == "" {
		t.Fatalf("missing source error not exposed: %+v", catalog.sources[2])
	}
}

func TestScanSessionBackgroundFolderRecursionIsOptIn(t *testing.T) {
	root := t.TempDir()
	writeBackgroundTestFile(t, filepath.Join(root, "top.jpg"))
	writeBackgroundTestFile(t, filepath.Join(root, "nested", "deep.png"))

	nonRecursive := scanSessionBackground(backgroundConfig(enabledBackgroundSource(config.SessionBackgroundSourceFolder, root, false)))
	if len(nonRecursive.images) != 1 || filepath.Base(nonRecursive.images[0].path) != "top.jpg" {
		t.Fatalf("non-recursive images = %+v", nonRecursive.images)
	}
	recursive := scanSessionBackground(backgroundConfig(enabledBackgroundSource(config.SessionBackgroundSourceFolder, root, true)))
	if len(recursive.images) != 2 {
		t.Fatalf("recursive images = %+v", recursive.images)
	}
}

func TestSessionBackgroundSelectionIsIdempotentAndRotates(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "a.png")
	second := filepath.Join(root, "b.png")
	writeBackgroundTestFile(t, first)
	writeBackgroundTestFile(t, second)
	bg := backgroundConfig(enabledBackgroundSource(config.SessionBackgroundSourceFolder, root, false))

	service := newSessionBackgroundService()
	service.choose = func(int) int { return 1 }
	selected, ok := service.selectImage("tab", bg, false)
	if !ok || !sameBackgroundPath(selected.path, second) {
		t.Fatalf("random first selection = %+v, %v", selected, ok)
	}
	repeated, _ := service.selectImage("tab", bg, false)
	if !sameBackgroundPath(repeated.path, selected.path) {
		t.Fatalf("idempotent selection changed from %q to %q", selected.path, repeated.path)
	}
	rotated, _ := service.selectImage("tab", bg, true)
	if sameBackgroundPath(rotated.path, selected.path) || !sameBackgroundPath(rotated.path, first) {
		t.Fatalf("rotated selection = %q, want %q", rotated.path, first)
	}
}

func TestSessionBackgroundSelectionDeterministicAndBounded(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "only.bmp")
	writeBackgroundTestFile(t, image)
	bg := backgroundConfig(enabledBackgroundSource(config.SessionBackgroundSourceFile, image, false))
	randomOff := false
	bg.RandomOnOpen = &randomOff

	service := newSessionBackgroundService()
	for i := 0; i < sessionBackgroundSelectionLimit+10; i++ {
		selected, ok := service.selectImage(string(rune(i+1)), bg, false)
		if !ok || !sameBackgroundPath(selected.path, image) {
			t.Fatalf("selection %d = %+v, %v", i, selected, ok)
		}
	}
	if len(service.selected) != sessionBackgroundSelectionLimit {
		t.Fatalf("selected state size = %d, want %d", len(service.selected), sessionBackgroundSelectionLimit)
	}
	rotated, ok := service.selectImage("latest", bg, true)
	if !ok || !sameBackgroundPath(rotated.path, image) {
		t.Fatalf("single-image rotation = %+v, %v", rotated, ok)
	}
}

func TestSessionBackgroundAppPersistsSettingsAndSignsConfiguredImages(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	first := filepath.Join(root, "a.png")
	second := filepath.Join(root, "b.jpg")
	writeBackgroundTestFile(t, first)
	writeBackgroundTestFile(t, second)

	app := NewApp()
	view := SessionBackgroundSettingsView{
		Enabled: true, MaskEnabled: true, RandomOnOpen: false, RotateSeconds: 60,
		Sources: []SessionBackgroundSourceView{{Kind: config.SessionBackgroundSourceFolder, Path: root, Enabled: true}},
	}
	if err := app.SetSessionBackgroundSettings(view); err != nil {
		t.Fatalf("SetSessionBackgroundSettings: %v", err)
	}
	persisted, err := app.SessionBackgroundSettings()
	if err != nil {
		t.Fatalf("SessionBackgroundSettings: %v", err)
	}
	if !persisted.Enabled || persisted.ImageCount != 2 || persisted.RotateSeconds != 60 || persisted.RandomOnOpen {
		t.Fatalf("persisted settings = %+v", persisted)
	}

	selected, err := app.SessionBackground("tab-a")
	if err != nil {
		t.Fatalf("SessionBackground: %v", err)
	}
	if !sameBackgroundPath(selected.Path, first) || selected.URL == "" {
		t.Fatalf("initial image = %+v", selected)
	}
	repeated, err := app.SessionBackground("tab-a")
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Path != selected.Path || repeated.URL == selected.URL {
		t.Fatalf("idempotent image should keep path and refresh URL: first=%+v repeated=%+v", selected, repeated)
	}
	rotated, err := app.RotateSessionBackground("tab-a")
	if err != nil {
		t.Fatal(err)
	}
	if !sameBackgroundPath(rotated.Path, second) || rotated.URL == "" {
		t.Fatalf("rotated image = %+v", rotated)
	}
}

func TestSessionBackgroundAppDisabledAndEmptyTabAreExplicit(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	image, err := app.SessionBackground("tab")
	if err != nil || image.Path != "" || image.URL != "" {
		t.Fatalf("disabled background = %+v, %v", image, err)
	}
	if _, err := app.SessionBackground("  "); err == nil {
		t.Fatal("empty tab id succeeded")
	}
}
