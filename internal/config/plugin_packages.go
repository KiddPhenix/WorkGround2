package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"workground2/internal/pluginpkg"
)

// mergeInstalledPluginPackages overlays enabled plugin package capabilities onto
// the in-memory config. It never writes config.toml: plugin package state lives
// in <WorkGround2 home>/plugin-packages.json so uninstall/disable can remove the
// entire bundle without editing user-authored config.
func mergeInstalledPluginPackages(cfg *Config, root string) []string {
	if cfg == nil {
		return nil
	}
	WorkGround2Home := WorkGround2HomeDir()
	if strings.TrimSpace(WorkGround2Home) == "" {
		return nil
	}
	installed, warnings := pluginpkg.LoadInstalled(WorkGround2Home)
	sort.SliceStable(installed, func(i, j int) bool {
		return installed[i].Installed.Name < installed[j].Installed.Name
	})
	for _, item := range installed {
		pkg := item.Package
		for _, warning := range item.Warnings {
			warnings = append(warnings, fmt.Sprintf("%s: %s", item.Installed.Name, warning))
		}
		for _, skillRoot := range pkg.SkillRoots() {
			if !stringSliceContainsPath(cfg.Skills.Paths, skillRoot) {
				cfg.Skills.Paths = append(cfg.Skills.Paths, skillRoot)
			}
		}
		for name, srv := range pkg.Manifest.MCPServers {
			if pluginNameExists(cfg.Plugins, name) {
				warnings = append(warnings, fmt.Sprintf("%s: plugin MCP server %q skipped because config already defines that name", item.Installed.Name, name))
				continue
			}
			entry := PluginEntry{
				Name:      name,
				Type:      srv.Type,
				Command:   pluginPackageCommand(pkg.Root, srv.Command),
				Args:      append([]string(nil), srv.Args...),
				Env:       pluginPackageEnv(item.Installed, pkg, srv.Env),
				URL:       strings.TrimSpace(srv.URL),
				Headers:   cloneStringMap(srv.Headers),
				AutoStart: srv.AutoStart,
				Tier:      srv.Tier,
			}
			cfg.Plugins = append(cfg.Plugins, entry)
		}
	}
	return warnings
}

func pluginPackageCommand(root, command string) string {
	command = strings.TrimSpace(command)
	if command == "" || filepath.IsAbs(command) {
		return command
	}
	return filepath.Join(root, filepath.FromSlash(command))
}

func pluginPackageEnv(installed pluginpkg.InstalledPlugin, pkg pluginpkg.Package, env map[string]string) map[string]string {
	out := cloneStringMap(env)
	if out == nil {
		out = map[string]string{}
	}
	WorkGround2Home := WorkGround2HomeDir()
	if WorkGround2Home != "" {
		out["WORKGROUND2_HOME"] = WorkGround2Home
		out["WorkGround2_HOME"] = WorkGround2Home
	}
	out["WorkGround2_PLUGIN_ROOT"] = pkg.Root
	out["WorkGround2_PLUGIN_NAME"] = installed.Name
	out["WORKGROUND2_PLUGIN_ROOT"] = pkg.Root
	out["WORKGROUND2_PLUGIN_NAME"] = installed.Name
	if installed.Version != "" {
		out["WorkGround2_PLUGIN_VERSION"] = installed.Version
		out["WORKGROUND2_PLUGIN_VERSION"] = installed.Version
	}
	if pkg.Manifest.AddOn != nil {
		if kind := strings.TrimSpace(pkg.Manifest.AddOn.Kind); kind != "" {
			out["WorkGround2_ADDON_KIND"] = kind
			out["WORKGROUND2_ADDON_KIND"] = kind
		}
		if namespace := pluginPackageAddOnNamespace(installed, pkg); namespace != "" {
			out["WorkGround2_ADDON_STORAGE"] = namespace
			out["WORKGROUND2_ADDON_STORAGE_NAMESPACE"] = namespace
			if WorkGround2Home != "" {
				addonHome := filepath.Join(WorkGround2Home, "addons", namespace)
				out["WORKGROUND2_ADDON_HOME"] = addonHome
				out["WORKGROUND2_ADDON_CONFIG_DIR"] = filepath.Join(addonHome, "config")
				out["WORKGROUND2_ADDON_DATA_DIR"] = filepath.Join(addonHome, "data")
				out["WORKGROUND2_ADDON_STATE_DIR"] = filepath.Join(addonHome, "state")
			}
		}
	}
	return out
}

func pluginPackageAddOnNamespace(installed pluginpkg.InstalledPlugin, pkg pluginpkg.Package) string {
	if pkg.Manifest.AddOn == nil {
		return ""
	}
	if pkg.Manifest.AddOn.Storage != nil {
		if namespace := strings.TrimSpace(pkg.Manifest.AddOn.Storage.Namespace); namespace != "" {
			return namespace
		}
	}
	if kind := strings.TrimSpace(pkg.Manifest.AddOn.Kind); kind != "" {
		return kind
	}
	return installed.Name
}

func pluginNameExists(entries []PluginEntry, name string) bool {
	for _, p := range entries {
		if p.Name == name {
			return true
		}
	}
	return false
}

func stringSliceContainsPath(paths []string, path string) bool {
	canon := CanonicalSkillPath(path)
	for _, existing := range paths {
		if CanonicalSkillPath(ExpandVars(existing)) == canon {
			return true
		}
	}
	return false
}
