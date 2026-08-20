package dsh

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeReady(t *testing.T) {
	dir := t.TempDir()
	entry := writeFile(t, dir, "cli.js", "#!/usr/bin/env node\n")
	config := writeFile(t, dir, "dsh.json", "{}\n")
	version := writeFile(t, dir, "package.json", `{"name":"dsh","version":"0.1.0-rc.8"}`)
	node := writeFile(t, dir, "node", "fake")

	res, err := Probe(Config{
		NodePath:             node,
		EntryPath:            entry,
		ConfigPath:           config,
		VersionPath:          version,
		RequiredVersion:      "0.1.0-rc.8",
		RequiredCapabilities: []Capability{CapInitialize, CapPrompt, CapShutdown},
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.Ready() {
		t.Fatalf("probe not ready: %+v", res.Missing)
	}
	if res.Version != "0.1.0-rc.8" || !res.VersionOK {
		t.Fatalf("version = %q ok=%v", res.Version, res.VersionOK)
	}
	if res.NodePath != node || res.EntryPath != entry {
		t.Fatalf("resolved paths: node=%q entry=%q", res.NodePath, res.EntryPath)
	}
}

func TestProbeReportsMissingPrecisely(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	res, err := Probe(Config{
		NodePath:             missing,
		ConfigPath:           filepath.Join(dir, "no-config.json"),
		RequiredVersion:      "0.1.0-rc.8",
		RequiredCapabilities: []Capability{CapInitialize, "session.resume"},
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Ready() {
		t.Fatalf("probe unexpectedly ready")
	}

	kinds := map[IssueKind]bool{}
	for _, iss := range res.Missing {
		kinds[iss.Kind] = true
	}
	for _, want := range []IssueKind{IssueNode, IssueEntry, IssueConfig, IssueVersion, IssueCapability} {
		if !kinds[want] {
			t.Errorf("missing %s issue; got %+v", want, res.Missing)
		}
	}
}

func TestProbeVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	entry := writeFile(t, dir, "cli.js", "// entry\n")
	version := writeFile(t, dir, "package.json", `{"version":"0.1.0-rc.7"}`)
	node := writeFile(t, dir, "node", "fake")

	res, err := Probe(Config{
		NodePath:        node,
		EntryPath:       entry,
		VersionPath:     version,
		RequiredVersion: "0.1.0-rc.8",
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Ready() || res.VersionOK {
		t.Fatalf("mismatch accepted: %+v", res)
	}
	var found bool
	for _, iss := range res.Missing {
		if iss.Kind == IssueVersion {
			found = true
		}
	}
	if !found {
		t.Fatalf("no version issue reported: %+v", res.Missing)
	}
}

func TestProbeFindsPackageAboveCompiledEntry(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib")
	if err := os.Mkdir(lib, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := writeFile(t, lib, "bin.js", "// compiled entry\n")
	config := writeFile(t, dir, "dsh.json", "{}\n")
	writeFile(t, dir, "package.json", `{"version":"0.1.0-rc.8"}`)
	node := writeFile(t, dir, "node", "fake")

	res, err := Probe(Config{
		NodePath:        node,
		EntryPath:       entry,
		ConfigPath:      config,
		RequiredVersion: "0.1.0-rc.8",
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.Ready() || res.Version != "0.1.0-rc.8" {
		t.Fatalf("version was not found above entry: %+v", res)
	}
}

func TestProbeRejectsMalformedVersionJSON(t *testing.T) {
	dir := t.TempDir()
	entry := writeFile(t, dir, "cli.js", "// entry\n")
	config := writeFile(t, dir, "dsh.json", "{}\n")
	version := writeFile(t, dir, "package.json", `{"version":`)
	node := writeFile(t, dir, "node", "fake")

	_, err := Probe(Config{
		NodePath:    node,
		EntryPath:   entry,
		ConfigPath:  config,
		VersionPath: version,
	})
	if err == nil {
		t.Fatal("malformed package.json accepted")
	}
}

func TestProbeRequiresConfig(t *testing.T) {
	dir := t.TempDir()
	entry := writeFile(t, dir, "cli.js", "// entry\n")
	writeFile(t, dir, "package.json", `{"version":"0.1.0-rc.8"}`)
	node := writeFile(t, dir, "node", "fake")

	res, err := Probe(Config{NodePath: node, EntryPath: entry})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, issue := range res.Missing {
		if issue.Kind == IssueConfig {
			return
		}
	}
	t.Fatalf("missing config issue not reported: %+v", res.Missing)
}

func TestRc8CapabilitiesReturnsDefensiveCopy(t *testing.T) {
	first := Rc8Capabilities()
	if len(first) == 0 {
		t.Fatalf("empty baseline")
	}
	first[0] = Capability("mutated")
	second := Rc8Capabilities()
	if second[0] == Capability("mutated") {
		t.Fatalf("baseline slice was mutated by caller")
	}
	if second[0] != CapInitialize {
		t.Fatalf("baseline corrupted: first=%q", second[0])
	}
}

func TestProbeDoesNotMutateFilesystem(t *testing.T) {
	dir := t.TempDir()
	entry := writeFile(t, dir, "cli.js", "// entry\n")
	node := writeFile(t, dir, "node", "fake")

	before := listDir(t, dir)
	if _, err := Probe(Config{NodePath: node, EntryPath: entry}); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	after := listDir(t, dir)
	if len(before) != len(after) {
		t.Fatalf("probe mutated filesystem: before=%v after=%v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("probe mutated filesystem: %v -> %v", before, after)
		}
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
