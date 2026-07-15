package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"workground2/internal/config"
)

const onboardingLocalCLITimeoutSeconds = 120

type LocalCLIOptionView struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	Protocol       string   `json:"protocol"`
	Model          string   `json:"model"`
	Capabilities   []string `json:"capabilities"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	Installed      bool     `json:"installed"`
	Version        string   `json:"version"`
	Error          string   `json:"error"`
}

type onboardingLocalCLIPreset struct {
	ID          string
	Name        string
	Description string
	Commands    []string
	Args        []string
	Protocol    string
	Model       string
}

var onboardingLocalCLIPresets = []onboardingLocalCLIPreset{
	{
		ID:          "codex",
		Name:        "Codex CLI",
		Description: "Runs Codex CLI in exec JSONL mode and sends the model request on stdin.",
		Commands:    []string{"codex", "codex.exe", "codex.cmd"},
		Args:        []string{"exec", "--json", "--ignore-user-config", "--skip-git-repo-check", "--sandbox", "read-only", "--model", "gpt-5.5"},
		Protocol:    "jsonl",
		Model:       "gpt-5.5",
	},
	{
		ID:          "claude",
		Name:        "Claude CLI",
		Description: "Runs Claude CLI in print mode and sends the model request on stdin.",
		Commands:    []string{"claude", "claude.exe", "claude.cmd"},
		Args:        []string{"--print"},
	},
	{
		ID:          "gemini",
		Name:        "Gemini CLI",
		Description: "Runs Gemini CLI and sends the model request on stdin.",
		Commands:    []string{"gemini", "gemini.exe", "gemini.cmd"},
	},
	{
		ID:          "opencode",
		Name:        "OpenCode CLI",
		Description: "Runs OpenCode CLI in stdin mode.",
		Commands:    []string{"opencode", "open-code", "opencode.exe", "opencode.cmd", "open-code.exe", "open-code.cmd"},
		Args:        []string{"run", "--stdin"},
	},
	{
		ID:          "kiro",
		Name:        "Kiro CLI",
		Description: "Runs Kiro CLI chat in non-interactive mode.",
		Commands:    []string{"kiro-cli", "kiro", "kiro-cli.exe", "kiro-cli.cmd", "kiro.exe", "kiro.cmd"},
		Args:        []string{"chat", "--no-interactive"},
	},
	{
		ID:          "deepseek",
		Name:        "DeepSeek CLI",
		Description: "Runs a DeepSeek-compatible local CLI and sends the model request on stdin.",
		Commands:    []string{"deepseek", "deepseek-cli", "deepseek.exe", "deepseek.cmd", "deepseek-cli.exe", "deepseek-cli.cmd"},
		Args:        []string{"--prompt-stdin"},
	},
	{
		ID:          "zcode",
		Name:        "ZCode CLI",
		Description: "Runs ZCode CLI in prompt-stdin mode.",
		Commands:    []string{"zcode", "zcode.exe", "zcode.cmd"},
		Args:        []string{"--prompt-stdin"},
	},
	{
		ID:          "pi",
		Name:        "Pi",
		Description: "Runs Pi CLI in prompt-stdin mode.",
		Commands:    []string{"pi", "pi.exe", "pi.cmd"},
		Args:        []string{"--prompt-stdin"},
	},
	{
		ID:          "deepcode",
		Name:        "DeepCode",
		Description: "Runs DeepCode CLI in prompt-stdin mode.",
		Commands:    []string{"deepcode", "deepcode.exe", "deepcode.cmd"},
		Args:        []string{"--prompt-stdin"},
	},
}

func (a *App) ScanLocalCLIProviders() []LocalCLIOptionView {
	return scanLocalCLIOptions()
}

func (a *App) ConnectLocalCLIProvider(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return fmt.Errorf("local cli id is required")
	}
	var selected *LocalCLIOptionView
	options := scanLocalCLIOptions()
	for i := range options {
		if options[i].ID == id {
			selected = &options[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("unknown local cli %q", id)
	}
	if !selected.Installed || strings.TrimSpace(selected.Command) == "" {
		if selected.Error != "" {
			return fmt.Errorf("%s is not available: %s", selected.Name, selected.Error)
		}
		return fmt.Errorf("%s is not installed or not on PATH", selected.Name)
	}
	entry := localCLIProviderEntry(*selected)
	return a.applyConfigChange(func(c *config.Config) error {
		hasAPI := hasConfiguredAPIProvider(c)
		if err := c.UpsertProvider(entry); err != nil {
			return err
		}
		addProviderAccess(c, entry.Name)
		if !hasAPI {
			c.DefaultModel = entry.Name + "/" + entry.DefaultModel()
		}
		return nil
	})
}

func hasConfiguredAPIProvider(c *config.Config) bool {
	for i := range c.Providers {
		p := &c.Providers[i]
		if strings.EqualFold(strings.TrimSpace(p.Kind), "cli") || !p.Configured() || len(p.ModelList()) == 0 {
			continue
		}
		return true
	}
	return false
}

func localCLIProviderEntry(opt LocalCLIOptionView) config.ProviderEntry {
	model := strings.TrimSpace(opt.Model)
	if model == "" {
		model = "default"
	}
	timeout := opt.TimeoutSeconds
	if timeout <= 0 {
		timeout = onboardingLocalCLITimeoutSeconds
	}
	protocol := strings.TrimSpace(opt.Protocol)
	if protocol == "" {
		protocol = "text"
	}
	entry := config.ProviderEntry{
		Name:           localCLIProviderName(opt.ID),
		Kind:           "cli",
		Command:        strings.TrimSpace(opt.Command),
		Args:           append([]string{}, opt.Args...),
		Protocol:       protocol,
		TimeoutSeconds: timeout,
		Model:          model,
		Models:         []string{model},
		Default:        model,
		ContextWindow:  128000,
	}
	entry.AddCapabilities(opt.Capabilities...)
	return entry
}

func localCLIProviderName(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	var b strings.Builder
	lastDash := false
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	stem := strings.Trim(b.String(), "-")
	if stem == "" {
		stem = "cli"
	}
	return "local-" + stem
}

func scanLocalCLIOptions() []LocalCLIOptionView {
	return scanLocalCLIOptionsWithPresets(onboardingLocalCLIPresets)
}

func scanLocalCLIOptionsWithPresets(presets []onboardingLocalCLIPreset) []LocalCLIOptionView {
	out := make([]LocalCLIOptionView, 0, len(presets))
	for _, preset := range presets {
		command, errText := resolveOnboardingCLICommand(preset)
		installed := command != ""
		model := strings.TrimSpace(preset.Model)
		if model == "" {
			model = "default"
		}
		protocol := strings.TrimSpace(preset.Protocol)
		if protocol == "" {
			protocol = "text"
		}
		displayCommand := command
		if displayCommand == "" && len(preset.Commands) > 0 {
			displayCommand = preset.Commands[0]
		}
		version := ""
		capabilities := []string{}
		if installed {
			version = readLocalCLIVersion(command)
			probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			probed, _ := config.ProbeCLICapabilities(probeCtx, &config.ProviderEntry{
				Kind: "cli", Command: command, Model: model,
			})
			cancel()
			capabilities = append(capabilities, probed...)
		}
		out = append(out, LocalCLIOptionView{
			ID:             strings.ToLower(strings.TrimSpace(preset.ID)),
			Name:           preset.Name,
			Description:    preset.Description,
			Command:        displayCommand,
			Args:           append([]string{}, preset.Args...),
			Protocol:       protocol,
			Model:          model,
			Capabilities:   capabilities,
			TimeoutSeconds: onboardingLocalCLITimeoutSeconds,
			Installed:      installed,
			Version:        version,
			Error:          errText,
		})
	}
	return out
}

func resolveOnboardingCLICommand(preset onboardingLocalCLIPreset) (string, string) {
	var windowsApps string
	for _, candidate := range localCLICommandCandidates(preset) {
		resolved, ok := resolveLocalCLICommandCandidate(candidate)
		if !ok {
			continue
		}
		if runtime.GOOS == "windows" && isWindowsAppsCommandPath(resolved) {
			windowsApps = resolved
			continue
		}
		return resolved, ""
	}
	if windowsApps != "" {
		return "", "WindowsApps shim ignored: " + windowsApps
	}
	return "", ""
}

func localCLICommandCandidates(preset onboardingLocalCLIPreset) []string {
	var candidates []string
	if runtime.GOOS == "windows" && preset.ID == "codex" {
		candidates = append(candidates, codexWindowsCommandCandidates()...)
	}
	for _, command := range preset.Commands {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		candidates = append(candidates, command)
		if runtime.GOOS == "windows" && filepath.Ext(command) == "" && !strings.ContainsAny(command, `/\`) {
			candidates = append(candidates, command+".exe", command+".cmd")
		}
	}
	return uniqueCLIStrings(candidates)
}

func codexWindowsCommandCandidates() []string {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if localAppData == "" {
		return nil
	}
	root := filepath.Join(localAppData, "OpenAI", "Codex", "bin")
	out := []string{filepath.Join(root, "codex.exe")}
	matches, _ := filepath.Glob(filepath.Join(root, "*", "codex.exe"))
	sort.Strings(matches)
	out = append(out, matches...)
	return out
}

func resolveLocalCLICommandCandidate(candidate string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", false
	}
	if filepath.IsAbs(candidate) || strings.ContainsAny(candidate, `/\`) {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			if abs, err := filepath.Abs(candidate); err == nil {
				return abs, true
			}
			return candidate, true
		}
		return "", false
	}
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		return "", false
	}
	return resolved, true
}

func isWindowsAppsCommandPath(path string) bool {
	path = strings.ToLower(filepath.Clean(strings.TrimSpace(path)))
	return strings.Contains(path, `\windowsapps\`)
}

func readLocalCLIVersion(command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, "--version")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 160 {
			return line[:160]
		}
		return line
	}
	return ""
}

func uniqueCLIStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
