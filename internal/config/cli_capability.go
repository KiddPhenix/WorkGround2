package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workground2/internal/proc"
)

var cliCapabilityCache sync.Map

const cliCapabilityCacheTTL = 5 * time.Minute

type cliCapabilityCacheEntry struct {
	capabilities []string
	err          string
	expiresAt    time.Time
}

// ProbeCLICapabilities detects action capabilities exposed by a known local
// CLI. Explicit provider capabilities disable probing so user intent wins.
func ProbeCLICapabilities(ctx context.Context, entry *ProviderEntry) ([]string, error) {
	if entry == nil || !strings.EqualFold(strings.TrimSpace(entry.Kind), "cli") || entry.Capabilities != nil {
		return nil, nil
	}
	command := strings.TrimSpace(entry.Command)
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(command), filepath.Ext(command)))
	if command == "" || name != "codex" {
		return nil, nil
	}
	isolated := hasCLIArg(entry.Args, "--ignore-user-config")
	cacheKey := cliCapabilityCacheKey(command, isolated)
	if capabilities, err, ok := loadCLICapabilityCache(cacheKey, time.Now()); ok {
		return capabilities, err
	}

	cmd := exec.CommandContext(ctx, command, "features", "list")
	prepareCLICapabilityProbe(cmd)
	cleanup := func() {}
	if isolated {
		var err error
		cleanup, err = isolateCodexCapabilityProbe(cmd)
		if err != nil {
			return nil, fmt.Errorf("prepare isolated Codex CLI capability probe: %w", err)
		}
	}
	defer cleanup()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("probe Codex CLI capabilities: %w", ctx.Err())
		}
		probeErr := fmt.Errorf("probe Codex CLI capabilities: %w", err)
		storeCLICapabilityCache(cacheKey, nil, probeErr, time.Now())
		return nil, probeErr
	}
	capabilities := parseCodexCapabilities(string(out))
	storeCLICapabilityCache(cacheKey, capabilities, nil, time.Now())
	return capabilities, nil
}

func prepareCLICapabilityProbe(cmd *exec.Cmd) {
	proc.HideWindow(cmd)
}

func isolateCodexCapabilityProbe(cmd *exec.Cmd) (func(), error) {
	dir, err := os.MkdirTemp("", "workground2-codex-probe-*")
	if err != nil {
		return nil, err
	}
	// Match a provider launched with --ignore-user-config. Running `features
	// list` against the user's config would otherwise let an unrelated,
	// temporarily incompatible option hide every Codex action capability.
	cmd.Env = setProcessEnv(os.Environ(), "CODEX_HOME", dir)
	return func() { _ = os.RemoveAll(dir) }, nil
}

func setProcessEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		name, _, found := strings.Cut(item, "=")
		if found && strings.EqualFold(name, key) {
			continue
		}
		out = append(out, item)
	}
	return append(out, prefix+value)
}

func hasCLIArg(args []string, want string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), want) {
			return true
		}
	}
	return false
}

func cliCapabilityCacheKey(command string, isolated bool) string {
	return fmt.Sprintf("%s\x00isolated=%t", command, isolated)
}

func loadCLICapabilityCache(key string, now time.Time) ([]string, error, bool) {
	value, ok := cliCapabilityCache.Load(key)
	if !ok {
		return nil, nil, false
	}
	entry, ok := value.(cliCapabilityCacheEntry)
	if !ok || !now.Before(entry.expiresAt) {
		cliCapabilityCache.Delete(key)
		return nil, nil, false
	}
	if entry.err != "" {
		return nil, errors.New(entry.err), true
	}
	return append([]string(nil), entry.capabilities...), nil, true
}

func storeCLICapabilityCache(key string, capabilities []string, err error, now time.Time) {
	entry := cliCapabilityCacheEntry{
		capabilities: append([]string(nil), capabilities...),
		expiresAt:    now.Add(cliCapabilityCacheTTL),
	}
	if err != nil {
		entry.err = err.Error()
	}
	cliCapabilityCache.Store(key, entry)
}

// AddCapabilities merges detected capabilities with the provider's effective
// baseline before making the result explicit. This preserves built-in vision
// and reasoning metadata when an action capability is discovered.
func (e *ProviderEntry) AddCapabilities(capabilities ...string) {
	if e == nil || len(capabilities) == 0 {
		return
	}
	values := append(EntryCapabilities(e), capabilities...)
	seen := make(map[string]bool, len(values))
	merged := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		merged = append(merged, value)
	}
	e.Capabilities = merged
}

func parseCodexCapabilities(output string) []string {
	enabled := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.EqualFold(fields[len(fields)-1], "true") {
			continue
		}
		enabled[fields[0]] = true
	}
	var caps []string
	if enabled["browser_use"] || enabled["standalone_web_search"] {
		caps = append(caps, string(CapWebSearch))
	}
	if enabled["image_generation"] {
		caps = append(caps, string(CapImageGeneration))
	}
	return caps
}
