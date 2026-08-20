// Package dsh holds the keyless, filesystem-only foundations for the DeepSeek
// Harness (DSH) Managed Runner: capability/version probing and the
// newline-delimited JSON-RPC protocol surface. P1 ships only these foundations;
// process ownership, event mapping and the concrete Runner land in P2.
//
// Nothing in this package starts DSH, installs or builds dependencies, or
// hard-codes an install path — Probe takes explicit paths and only reads the
// filesystem (plus PATH lookup for the node binary).
package dsh

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Capability names one DSH SDK JSON-RPC method a runner may rely on.
type Capability string

const (
	CapInitialize       Capability = "initialize"
	CapPrompt           Capability = "session/prompt"
	CapShutdown         Capability = "shutdown"
	CapSessionEvent     Capability = "session.event"
	CapSessionStatus    Capability = "session.status"
	CapSubagentStarted  Capability = "subagent.started"
	CapSubagentFinished Capability = "subagent.finished"
)

// rc8Capabilities is the keyless protocol surface verified against
// dsh-v0.1.0-rc.8 (commit 141eb6fef83422698aef7a981029e843e8161534). It is
// unexported so callers cannot mutate the baseline.
var rc8Capabilities = []Capability{
	CapInitialize,
	CapPrompt,
	CapShutdown,
	CapSessionEvent,
	CapSessionStatus,
	CapSubagentStarted,
	CapSubagentFinished,
}

// Rc8Capabilities returns a defensive copy of the rc.8 capability baseline.
func Rc8Capabilities() []Capability {
	return append([]Capability(nil), rc8Capabilities...)
}

// Config is the explicit input to Probe. Every path is caller-supplied; no
// default install location is assumed.
type Config struct {
	NodePath             string       // node executable; empty resolves via PATH
	EntryPath            string       // DSH entry file (cli/main)
	ConfigPath           string       // DSH config file
	VersionPath          string       // optional JSON file carrying "version"
	RequiredVersion      string       // optional exact baseline, e.g. "0.1.0-rc.8"
	RequiredCapabilities []Capability // optional must-have method surface
	KnownCapabilities    []Capability // supported set; nil defaults to Rc8Capabilities
}

// IssueKind identifies what a probe found missing.
type IssueKind string

const (
	IssueNode       IssueKind = "node"
	IssueEntry      IssueKind = "entry"
	IssueConfig     IssueKind = "config"
	IssueVersion    IssueKind = "version"
	IssueCapability IssueKind = "capability"
)

// Issue is one precise missing/mismatched probe finding.
type Issue struct {
	Kind   IssueKind `json:"kind"`
	Detail string    `json:"detail"`
}

// Result reports what Probe resolved. Missing findings are reported here, not
// as an error; only unreadable input files surface as an error.
type Result struct {
	NodePath     string       `json:"nodePath,omitempty"`
	EntryPath    string       `json:"entryPath,omitempty"`
	ConfigPath   string       `json:"configPath,omitempty"`
	Version      string       `json:"version,omitempty"`
	VersionOK    bool         `json:"versionOk"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	Missing      []Issue      `json:"missing,omitempty"`
}

// Ready reports whether the probe found everything required.
func (r Result) Ready() bool { return len(r.Missing) == 0 }

// Probe checks the DSH filesystem, version and capability surface without
// executing DSH or touching the network. It never installs or builds anything.
func Probe(cfg Config) (Result, error) {
	res := Result{
		NodePath:     cfg.NodePath,
		EntryPath:    cfg.EntryPath,
		ConfigPath:   cfg.ConfigPath,
		Capabilities: append([]Capability(nil), cfg.KnownCapabilities...),
	}
	if len(res.Capabilities) == 0 {
		res.Capabilities = Rc8Capabilities()
	}

	nodePath := cfg.NodePath
	if nodePath == "" {
		if p, err := exec.LookPath("node"); err == nil {
			nodePath = p
		}
	}
	res.NodePath = nodePath
	switch {
	case nodePath == "":
		res.Missing = append(res.Missing, Issue{Kind: IssueNode, Detail: "node executable not found on PATH"})
	case !isFile(nodePath):
		res.Missing = append(res.Missing, Issue{Kind: IssueNode, Detail: "node path is not a file: " + nodePath})
	}

	switch {
	case cfg.EntryPath == "":
		res.Missing = append(res.Missing, Issue{Kind: IssueEntry, Detail: "entry path is empty"})
	case !isFile(cfg.EntryPath):
		res.Missing = append(res.Missing, Issue{Kind: IssueEntry, Detail: "entry path is not a file: " + cfg.EntryPath})
	}

	switch {
	case cfg.ConfigPath == "":
		res.Missing = append(res.Missing, Issue{Kind: IssueConfig, Detail: "config path is empty"})
	case !isFile(cfg.ConfigPath):
		res.Missing = append(res.Missing, Issue{Kind: IssueConfig, Detail: "config path is not a file: " + cfg.ConfigPath})
	}

	version, err := detectVersion(cfg)
	if err != nil {
		return res, err
	}
	res.Version = version
	if cfg.RequiredVersion == "" {
		res.VersionOK = true
	} else {
		switch {
		case version == "":
			res.Missing = append(res.Missing, Issue{Kind: IssueVersion, Detail: "version required " + cfg.RequiredVersion + " but none detected"})
		case !versionMatches(version, cfg.RequiredVersion):
			res.Missing = append(res.Missing, Issue{Kind: IssueVersion, Detail: "unsupported version " + version + ", require " + cfg.RequiredVersion})
		default:
			res.VersionOK = true
		}
	}

	supported := make(map[Capability]bool, len(res.Capabilities))
	for _, c := range res.Capabilities {
		supported[c] = true
	}
	for _, c := range cfg.RequiredCapabilities {
		if !supported[c] {
			res.Missing = append(res.Missing, Issue{Kind: IssueCapability, Detail: "missing capability " + string(c)})
		}
	}

	return res, nil
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// detectVersion reads the version from cfg.VersionPath, falling back to the
// nearest package.json at or above the entry directory. The bounded upward
// search covers compiled entry points such as lib/bin.js without walking an
// unrelated directory tree.
func detectVersion(cfg Config) (string, error) {
	path := cfg.VersionPath
	if path == "" && cfg.EntryPath != "" {
		dir := filepath.Dir(cfg.EntryPath)
		for range 6 {
			candidate := filepath.Join(dir, "package.json")
			if isFile(candidate) {
				path = candidate
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("dsh: read version file %s: %w", path, err)
	}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		var pkg struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(data, &pkg); err != nil {
			return "", fmt.Errorf("dsh: parse version file %s: %w", path, err)
		}
		if strings.TrimSpace(pkg.Version) == "" {
			return "", fmt.Errorf("dsh: version file %s has no version", path)
		}
		return strings.TrimSpace(pkg.Version), nil
	}
	return strings.TrimSpace(string(data)), nil
}

// versionMatches compares exact baselines, ignoring an optional leading "v".
func versionMatches(got, want string) bool {
	return normalizeVersion(got) == normalizeVersion(want)
}

func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}
