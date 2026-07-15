package config

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var cliCapabilityCache sync.Map

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
	if cached, ok := cliCapabilityCache.Load(command); ok {
		return append([]string(nil), cached.([]string)...), nil
	}

	cmd := exec.CommandContext(ctx, command, "features", "list")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("probe Codex CLI capabilities: %w", ctx.Err())
		}
		return nil, fmt.Errorf("probe Codex CLI capabilities: %w", err)
	}
	capabilities := parseCodexCapabilities(string(out))
	cliCapabilityCache.Store(command, append([]string{}, capabilities...))
	return capabilities, nil
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
