package main

import (
	"os"
	"path/filepath"
	"strings"

	"workground2/internal/config"
)

// GetGlobalAgentsMD returns the content of the user-global AGENTS.md file
// (~/.WorkGround2/AGENTS.md), or "" when the file doesn't exist or can't be read.
func (a *App) GetGlobalAgentsMD() string {
	path := globalAgentsPath()
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetGlobalAgentsMD writes content to the user-global AGENTS.md file, creating the
// parent directory if needed. The content is trimmed of leading/trailing whitespace
// and written with a trailing newline for clean file termination.
func (a *App) SetGlobalAgentsMD(content string) error {
	path := globalAgentsPath()
	if path == "" {
		return os.ErrNotExist
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := strings.TrimSpace(content)
	if body != "" {
		body += "\n"
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// globalAgentsPath returns the canonical path for the user-global AGENTS.md file.
// It lives under the WorkGround2 home / state dir, alongside the user config.toml
// and the per-project auto-memory store — the same directory discoverDocs() scans
// for ScopeUser memory at boot.
func globalAgentsPath() string {
	return filepath.Join(config.MemoryUserDir(), "AGENTS.md")
}
