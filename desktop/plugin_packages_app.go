package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"workground2/internal/config"
	"workground2/internal/installsource"
	"workground2/internal/pluginpkg"
)

type PluginView struct {
	Name            string     `json:"name"`
	Version         string     `json:"version,omitempty"`
	Description     string     `json:"description,omitempty"`
	Source          string     `json:"source,omitempty"`
	Root            string     `json:"root"`
	ManifestKind    string     `json:"manifestKind,omitempty"`
	Enabled         bool       `json:"enabled"`
	Skills          int        `json:"skills"`
	Hooks           int        `json:"hooks"`
	MCPServers      int        `json:"mcpServers"`
	LastCheckedAt   string     `json:"lastCheckedAt,omitempty"`
	LastUpdatedAt   string     `json:"lastUpdatedAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	UpdateAvailable bool       `json:"updateAvailable,omitempty"`
	RemoteVersion   string     `json:"remoteVersion,omitempty"`
	Warnings        []string   `json:"warnings,omitempty"`
	Error           string     `json:"error,omitempty"`
	AddOn           *AddOnView `json:"addon,omitempty"`
}

type AddOnView struct {
	Kind             string            `json:"kind,omitempty"`
	DisplayName      string            `json:"displayName,omitempty"`
	Capabilities     []string          `json:"capabilities,omitempty"`
	Panels           []AddOnPanelView  `json:"panels,omitempty"`
	ConfigSchema     string            `json:"configSchema,omitempty"`
	Secrets          []AddOnSecretView `json:"secrets,omitempty"`
	Runtime          *AddOnRuntimeView `json:"runtime,omitempty"`
	Update           *AddOnUpdateView  `json:"update,omitempty"`
	StorageNamespace string            `json:"storageNamespace,omitempty"`
}

type AddOnPanelView struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	Entry string `json:"entry,omitempty"`
}

type AddOnSecretView struct {
	ID       string `json:"id,omitempty"`
	Label    string `json:"label,omitempty"`
	Purpose  string `json:"purpose,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type AddOnRuntimeView struct {
	Type      string `json:"type,omitempty"`
	MCPServer string `json:"mcpServer,omitempty"`
}

type AddOnUpdateView struct {
	Type     string `json:"type,omitempty"`
	Strategy string `json:"strategy,omitempty"`
	Check    string `json:"check,omitempty"`
}

type PluginInstallOptions struct {
	DryRun  bool   `json:"dryRun,omitempty"`
	Link    bool   `json:"link,omitempty"`
	Replace bool   `json:"replace,omitempty"`
	Name    string `json:"name,omitempty"`
}

func (a *App) Plugins() []PluginView {
	st, err := pluginpkg.LoadState(config.WorkGround2HomeDir())
	if err != nil {
		return []PluginView{{Error: err.Error()}}
	}
	out := make([]PluginView, 0, len(st.Plugins))
	for _, p := range st.Plugins {
		view := PluginView{
			Name:            p.Name,
			Version:         p.Version,
			Description:     p.Description,
			Source:          p.Source,
			Root:            pluginpkg.ResolveRoot(config.WorkGround2HomeDir(), p.Root),
			ManifestKind:    p.ManifestKind,
			Enabled:         p.Enabled,
			LastCheckedAt:   p.LastCheckedAt,
			LastUpdatedAt:   p.LastUpdatedAt,
			LastError:       p.LastError,
			UpdateAvailable: p.UpdateAvailable,
			RemoteVersion:   p.RemoteVersion,
		}
		if pkg, warnings, err := pluginpkg.ParseDir(view.Root); err == nil {
			view.Skills, view.Hooks, view.MCPServers = pkg.CapabilityCounts()
			view.AddOn = addOnView(pkg.Manifest.AddOn)
			view.Warnings = warnings
		} else {
			view.Error = err.Error()
		}
		out = append(out, view)
	}
	return out
}

func addOnView(addon *pluginpkg.AddOn) *AddOnView {
	if addon == nil {
		return nil
	}
	out := &AddOnView{
		Kind:         addon.Kind,
		DisplayName:  addon.DisplayName,
		Capabilities: append([]string(nil), addon.Capabilities...),
		ConfigSchema: addon.ConfigSchema,
	}
	if addon.Storage != nil {
		out.StorageNamespace = addon.Storage.Namespace
	}
	if addon.Runtime != nil {
		out.Runtime = &AddOnRuntimeView{
			Type:      addon.Runtime.Type,
			MCPServer: addon.Runtime.MCPServer,
		}
	}
	if addon.Update != nil {
		out.Update = &AddOnUpdateView{
			Type:     addon.Update.Type,
			Strategy: addon.Update.Strategy,
			Check:    addon.Update.Check,
		}
	}
	for _, panel := range addon.Panels {
		out.Panels = append(out.Panels, AddOnPanelView{
			ID:    panel.ID,
			Title: panel.Title,
			Entry: panel.Entry,
		})
	}
	for _, secret := range addon.Secrets {
		out.Secrets = append(out.Secrets, AddOnSecretView{
			ID:       secret.ID,
			Label:    secret.Label,
			Purpose:  secret.Purpose,
			Required: secret.Required,
		})
	}
	return out
}

func (a *App) PlanPluginInstall(source string, opts PluginInstallOptions) (string, error) {
	opts.DryRun = true
	return a.runPluginInstallSource(source, opts, false)
}

func (a *App) InstallPlugin(source string, opts PluginInstallOptions) (string, error) {
	if err := a.ensureActiveTabRebuildAllowed("plugins"); err != nil {
		return "", err
	}
	out, err := a.runPluginInstallSource(source, opts, true)
	if err != nil {
		return "", err
	}
	a.invalidateSkillRootsCache()
	if rebuildErr := a.rebuild(); rebuildErr != nil {
		return out, rebuildErr
	}
	return out, nil
}

func (a *App) RemovePlugin(name string) error {
	if err := a.ensureActiveTabRebuildAllowed("plugins"); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"op": "uninstall", "kind": "plugin", "name": strings.TrimSpace(name), "scope": "global"})
	tl := installsource.NewTool(installsource.Options{
		ProjectRoot: a.activeWorkspaceRoot(),
		OnDisconnect: func(serverName string) bool {
			tab := a.activeTab()
			if tab == nil || tab.Ctrl == nil {
				return false
			}
			removed, _ := tab.Ctrl.RemoveMCPServer(serverName)
			return removed
		},
	})
	if _, err := tl.Execute(context.Background(), raw); err != nil {
		return err
	}
	a.invalidateSkillRootsCache()
	return a.rebuild()
}

func (a *App) SetPluginEnabled(name string, enabled bool) error {
	if err := a.ensureActiveTabRebuildAllowed("plugins"); err != nil {
		return err
	}
	if err := pluginpkg.SetEnabled(config.WorkGround2HomeDir(), strings.TrimSpace(name), enabled); err != nil {
		return err
	}
	a.invalidateSkillRootsCache()
	return a.rebuild()
}

func (a *App) UpdatePlugin(name string) (string, error) {
	name = strings.TrimSpace(name)
	for _, p := range a.Plugins() {
		if p.Name == name {
			if strings.TrimSpace(p.Source) == "" {
				return "", fmt.Errorf("plugin %q has no recorded source", name)
			}
			return a.InstallPlugin(p.Source, PluginInstallOptions{Name: name, Replace: true})
		}
	}
	return "", fmt.Errorf("plugin %q is not installed", name)
}

func (a *App) PluginDoctor(name string) PluginView {
	name = strings.TrimSpace(name)
	for _, p := range a.Plugins() {
		if p.Name != name {
			continue
		}
		if p.Error != "" {
			return p
		}
		if p.Root == "" {
			p.Error = "missing plugin root"
			return p
		}
		if _, err := os.Stat(p.Root); err != nil {
			p.Error = err.Error()
			return p
		}
		return p
	}
	return PluginView{Name: name, Error: "plugin is not installed"}
}

func (a *App) AddOnPanelSchema(name string, panelID string) (string, error) {
	name = strings.TrimSpace(name)
	panelID = strings.TrimSpace(panelID)
	if name == "" {
		return "", fmt.Errorf("plugin name is required")
	}
	if panelID == "" {
		return "", fmt.Errorf("addon panel id is required")
	}
	for _, p := range a.Plugins() {
		if p.Name != name {
			continue
		}
		if p.Error != "" {
			return "", fmt.Errorf("plugin %q: %s", name, p.Error)
		}
		pkg, _, err := pluginpkg.ParseDir(p.Root)
		if err != nil {
			return "", err
		}
		if pkg.Manifest.AddOn == nil {
			return "", fmt.Errorf("plugin %q is not an AddOn", name)
		}
		for _, panel := range pkg.Manifest.AddOn.Panels {
			if panel.ID != panelID && panel.Entry != panelID {
				continue
			}
			path, err := addonPanelSchemaPath(p.Root, panel.Entry)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(path)
			if err != nil {
				return "", err
			}
			if info.Size() > 512*1024 {
				return "", fmt.Errorf("addon panel schema %q is too large", panelID)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			var decoded any
			if err := json.Unmarshal(b, &decoded); err != nil {
				return "", fmt.Errorf("addon panel schema %q is invalid JSON: %w", panelID, err)
			}
			return string(b), nil
		}
		return "", fmt.Errorf("addon panel %q is not declared by plugin %q", panelID, name)
	}
	return "", fmt.Errorf("plugin %q is not installed", name)
}

func addonPanelSchemaPath(root string, entry string) (string, error) {
	root = strings.TrimSpace(root)
	entry = strings.TrimSpace(entry)
	if root == "" {
		return "", fmt.Errorf("plugin root is empty")
	}
	if entry == "" {
		return "", fmt.Errorf("addon panel entry is empty")
	}
	if filepath.IsAbs(entry) {
		return "", fmt.Errorf("addon panel entry must be relative")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleanEntry := filepath.Clean(filepath.FromSlash(entry))
	pathAbs, err := filepath.Abs(filepath.Join(rootAbs, cleanEntry))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("addon panel entry escapes plugin root")
	}
	return pathAbs, nil
}

func (a *App) runPluginInstallSource(source string, opts PluginInstallOptions, apply bool) (string, error) {
	mode := "copy"
	if opts.Link {
		mode = "link"
	}
	body := map[string]any{
		"source":  strings.TrimSpace(source),
		"kind":    "plugin",
		"mode":    mode,
		"replace": opts.Replace,
		"apply":   apply && !opts.DryRun,
	}
	if strings.TrimSpace(opts.Name) != "" {
		body["name"] = strings.TrimSpace(opts.Name)
	}
	raw, _ := json.Marshal(body)
	tl := installsource.NewTool(installsource.Options{ProjectRoot: a.activeWorkspaceRoot()})
	return tl.Execute(context.Background(), raw)
}
