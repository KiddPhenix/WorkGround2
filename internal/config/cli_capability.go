package config

import (
	"context"
	"errors"
	"fmt"
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
	if capabilities, err, ok := loadCLICapabilityCache(command, time.Now()); ok {
		return capabilities, err
	}

	cmd := exec.CommandContext(ctx, command, "features", "list")
	prepareCLICapabilityProbe(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("probe Codex CLI capabilities: %w", ctx.Err())
		}
		probeErr := fmt.Errorf("probe Codex CLI capabilities: %w", err)
		storeCLICapabilityCache(command, nil, probeErr, time.Now())
		return nil, probeErr
	}
	capabilities := parseCodexCapabilities(string(out))
	storeCLICapabilityCache(command, capabilities, nil, time.Now())
	return capabilities, nil
}

func prepareCLICapabilityProbe(cmd *exec.Cmd) {
	proc.HideWindow(cmd)
}

func loadCLICapabilityCache(command string, now time.Time) ([]string, error, bool) {
	value, ok := cliCapabilityCache.Load(command)
	if !ok {
		return nil, nil, false
	}
	entry, ok := value.(cliCapabilityCacheEntry)
	if !ok || !now.Before(entry.expiresAt) {
		cliCapabilityCache.Delete(command)
		return nil, nil, false
	}
	if entry.err != "" {
		return nil, errors.New(entry.err), true
	}
	return append([]string(nil), entry.capabilities...), nil, true
}

func storeCLICapabilityCache(command string, capabilities []string, err error, now time.Time) {
	entry := cliCapabilityCacheEntry{
		capabilities: append([]string(nil), capabilities...),
		expiresAt:    now.Add(cliCapabilityCacheTTL),
	}
	if err != nil {
		entry.err = err.Error()
	}
	cliCapabilityCache.Store(command, entry)
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
