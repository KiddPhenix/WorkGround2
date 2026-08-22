package dshcompat

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/pluginpkg"
)

func TestDiscoverFallsBackToRunnableLocalSource(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	installed := filepath.Join(home, "plugins", "demo")
	source := filepath.Join(workspace, "source")
	writeTestFile(t, filepath.Join(installed, "package.json"), `{"name":"demo","dsh":{"bundle":{"patch":"cordis.patch.yml"}}}`)
	writeTestFile(t, filepath.Join(installed, "cordis.patch.yml"), "- insert:\n    - id: demo\n      name: missing-package\n")
	writeTestFile(t, filepath.Join(source, "package.json"), `{"name":"demo","dsh":{"bundle":{"patch":"cordis.patch.yml"}}}`)
	writeTestFile(t, filepath.Join(source, "cordis.patch.yml"), "- insert:\n    - id: demo\n      name: demo-package\n")
	writeTestFile(t, filepath.Join(source, "node_modules", "demo-package", "package.json"), `{"name":"demo-package"}`)
	anchor := filepath.Join(source, "apps", "cli", "package.json")
	writeTestFile(t, anchor, `{"name":"@deepseek-ai/dsh"}`)
	writeTestFile(t, filepath.Join(source, "node_modules", "@deepseek-ai", "dsh-app-boot", "package.json"), `{"name":"@deepseek-ai/dsh-app-boot"}`)
	if err := pluginpkg.Upsert(home, pluginpkg.InstalledPlugin{
		Name: "demo", Root: filepath.Join("plugins", "demo"), Source: source, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	specs, warnings := Discover(home, workspace, nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %+v", specs)
	}
	if specs[0].BundlePackageJSON != filepath.Join(source, "package.json") {
		t.Fatalf("BundlePackageJSON = %q", specs[0].BundlePackageJSON)
	}
	if specs[0].RuntimeAnchor != anchor {
		t.Fatalf("RuntimeAnchor = %q", specs[0].RuntimeAnchor)
	}
}

func TestResolveRuntimeAnchorRejectsNameOnlyPackage(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "cli", "package.json"), `{"name":"@deepseek-ai/dsh"}`)
	if _, err := ResolveRuntimeAnchor(root); err == nil {
		t.Fatal("ResolveRuntimeAnchor accepted a package that cannot resolve dsh-app-boot")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
