//go:build windows

package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testCreateNoWindow = 0x08000000

func TestPrepareCLICapabilityProbeHidesWindowsConsole(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "0")
	prepareCLICapabilityProbe(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("CLI capability probe did not enable HideWindow")
	}
	if cmd.SysProcAttr.CreationFlags&testCreateNoWindow == 0 {
		t.Fatalf("CLI capability probe creation flags = %#x, want CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
}

func TestProbeCLICapabilitiesCachesFailedWindowsCommand(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "codex.cmd")
	calls := filepath.Join(dir, "calls.txt")
	script := "@echo off\r\necho run>>\"%~dp0calls.txt\"\r\nexit /b 1\r\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake Codex CLI: %v", err)
	}
	key := cliCapabilityCacheKey(command, false)
	cliCapabilityCache.Delete(key)
	t.Cleanup(func() { cliCapabilityCache.Delete(key) })
	entry := &ProviderEntry{Kind: "cli", Command: command}

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := ProbeCLICapabilities(context.Background(), entry); err == nil {
			t.Fatalf("probe attempt %d unexpectedly succeeded", attempt+1)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read probe calls: %v", err)
	}
	if got := strings.Count(string(data), "run"); got != 1 {
		t.Fatalf("failed probe process starts = %d, want 1", got)
	}
}

func TestProbeCLICapabilitiesIsolatesIgnoredUserConfig(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "codex.cmd")
	userHome := filepath.Join(dir, "user-config")
	script := "@echo off\r\nif /i \"%CODEX_HOME%\"==\"" + userHome + "\" exit /b 2\r\necho browser_use stable true\r\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake Codex CLI: %v", err)
	}
	t.Setenv("CODEX_HOME", userHome)
	key := cliCapabilityCacheKey(command, true)
	cliCapabilityCache.Delete(key)
	t.Cleanup(func() { cliCapabilityCache.Delete(key) })

	got, err := ProbeCLICapabilities(context.Background(), &ProviderEntry{
		Kind: "cli", Command: command, Args: []string{"exec", "--ignore-user-config"},
	})
	if err != nil {
		t.Fatalf("ProbeCLICapabilities: %v", err)
	}
	if len(got) != 1 || got[0] != "web_search" {
		t.Fatalf("capabilities = %v, want [web_search]", got)
	}
}
