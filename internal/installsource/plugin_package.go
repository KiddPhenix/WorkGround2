package installsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"workground2/internal/pluginpkg"
)

func (t *installSourceTool) localPluginPackageAction(req request, root string) (action, []string, error) {
	pkg, warnings, err := pluginpkg.ParseDir(root)
	if err != nil {
		return action{}, warnings, newErr(ErrManifestMissing, "%v", err)
	}
	return t.pluginPackageAction(req, pkg, root), warnings, nil
}

func (t *installSourceTool) planGitHubPluginPackage(ctx context.Context, req request) ([]action, []string, error) {
	src, ok := parseGitHubRepoSource(req.Source)
	if !ok {
		return nil, nil, newErr(ErrUnsupportedKind, "plugin URL %q is not a GitHub repository", req.Source)
	}
	var warnings []string
	for _, branch := range src.branches() {
		for _, manifestPath := range []string{pluginpkg.NativeManifest, pluginpkg.CodexManifest} {
			rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", src.Owner, src.Repo, branch, joinURLPath(src.Path, manifestPath))
			body, err := t.fetchText(ctx, rawURL)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %s", rawURL, err.Error()))
				continue
			}
			tmp, err := os.MkdirTemp("", "WorkGround2-plugin-plan-*")
			if err != nil {
				return nil, warnings, err
			}
			defer os.RemoveAll(tmp)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(tmp, manifestPath)), 0o755); err != nil {
				return nil, warnings, err
			}
			if err := os.WriteFile(filepath.Join(tmp, manifestPath), []byte(body), 0o644); err != nil {
				return nil, warnings, err
			}
			if strings.EqualFold(manifestPath, pluginpkg.CodexManifest) {
				if hookBody, err := t.fetchText(ctx, fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", src.Owner, src.Repo, branch, joinURLPath(src.Path, "hooks/session-start-codex"))); err == nil {
					hookPath := filepath.Join(tmp, "hooks", "session-start-codex")
					if mkErr := os.MkdirAll(filepath.Dir(hookPath), 0o755); mkErr == nil {
						_ = os.WriteFile(hookPath, []byte(hookBody), 0o755)
					}
				}
			}
			pkg, pkgWarnings, err := pluginpkg.ParseDir(tmp)
			warnings = append(warnings, pkgWarnings...)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %s", rawURL, err.Error()))
				continue
			}
			act := t.pluginPackageAction(req, pkg, req.Source)
			act.Source = req.Source
			return []action{act}, warnings, nil
		}
	}
	return nil, warnings, newErr(ErrManifestMissing, "no plugin manifest found in GitHub repository %s/%s", src.Owner, src.Repo)
}

func (t *installSourceTool) pluginPackageAction(req request, pkg pluginpkg.Package, source string) action {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = pkg.Manifest.Name
	}
	root := ""
	if t.WorkGround2Home != "" {
		root = pluginpkg.InstallRoot(t.WorkGround2Home, name)
	}
	skills, hooks, mcp := pkg.CapabilityCounts()
	addonPanels, addonSecrets := pkg.AddOnCounts()
	a := action{
		Kind:         "plugin",
		Action:       "install_plugin_package",
		Name:         name,
		Source:       source,
		Target:       root,
		Scope:        "global",
		Mode:         modeForPlugin(req.Mode),
		ConfigPath:   pluginpkg.StatePath(t.WorkGround2Home),
		Skills:       pkg.Manifest.Skills,
		SkillCount:   skills,
		HookCount:    hooks,
		ToolCount:    mcp,
		ManifestKind: pkg.ManifestKind,
		Version:      pkg.Manifest.Version,
		RiskLevel:    RiskMedium,
		RiskReasons:  []string{"installs a plugin package that can add skills, hooks, and MCP servers"},
	}
	if pkg.Manifest.AddOn != nil {
		a.AddOnKind = pkg.Manifest.AddOn.Kind
		if pkg.Manifest.AddOn.Runtime != nil {
			a.AddOnRuntime = pkg.Manifest.AddOn.Runtime.Type
		}
		a.AddOnPanels = addonPanels
		a.AddOnSecrets = addonSecrets
		a.RiskReasons = append(a.RiskReasons, "installs an AddOn package that can expose settings, storage, and runtime actions")
	}
	if a.Mode == "link" {
		a.RiskReasons = append(a.RiskReasons, "links a plugin package from a mutable local directory")
	}
	if hooks > 0 {
		a.RiskReasons = append(a.RiskReasons, "registers shell hooks that execute during WorkGround2 sessions")
	}
	if mcp > 0 {
		a.RiskReasons = append(a.RiskReasons, "adds MCP servers that can change provider-visible tool schemas")
	}
	sort.Strings(a.Skills)
	return a
}

func modeForPlugin(mode string) string {
	if mode == "link" {
		return "link"
	}
	return "copy"
}

func (t *installSourceTool) applyInstallPluginPackage(ctx context.Context, req request, act *action) error {
	if t.WorkGround2Home == "" {
		return newErr(ErrSourceUnreadable, "plugin install requires a WorkGround2 home directory")
	}
	if !pluginpkg.IsValidName(act.Name) {
		return newErr(ErrInvalidManifest, "invalid plugin name %q", act.Name)
	}
	target := pluginpkg.InstallRoot(t.WorkGround2Home, act.Name)
	sourceRoot, cleanup, err := t.preparePluginSource(ctx, act.Source, act.Mode)
	if err != nil {
		return err
	}
	defer cleanup()
	pkg, warnings, err := pluginpkg.ParseDir(sourceRoot)
	if err != nil {
		return newErr(ErrInvalidManifest, "%v", err)
	}
	act.Warnings = append(act.Warnings, warnings...)
	if pkg.Manifest.Name != act.Name && strings.TrimSpace(req.Name) == "" {
		return newErr(ErrInvalidManifest, "planned plugin name %q but source now reports %q", act.Name, pkg.Manifest.Name)
	}
	previous, hadPrevious, err := installedPluginState(t.WorkGround2Home, act.Name)
	if err != nil {
		return err
	}
	if act.Mode == "link" {
		if !isLinkTargetSafe(sourceRoot, t.home, t.root) {
			return newErr(ErrUnsafeLinkTarget, "plugin source %s is outside %s and %s", sourceRoot, t.root, t.home)
		}
		if err := replaceSymlink(target, sourceRoot, req.Replace); err != nil {
			return err
		}
	} else {
		if err := replaceCopiedPlugin(sourceRoot, target, req.Replace); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	installedAt := previous.InstalledAt
	if installedAt == "" {
		installedAt = now
	}
	installed := pluginpkg.InstalledPlugin{
		Name:          act.Name,
		Source:        act.Source,
		Root:          pluginpkg.RelativeRoot(t.WorkGround2Home, target),
		Version:       pkg.Manifest.Version,
		Description:   pkg.Manifest.Description,
		ManifestKind:  pkg.ManifestKind,
		Enabled:       true,
		InstalledAt:   installedAt,
		LastCheckedAt: previous.LastCheckedAt,
		RemoteVersion: previous.RemoteVersion,
	}
	if hadPrevious {
		installed.LastUpdatedAt = now
	}
	if act.Mode == "link" {
		installed.Root = sourceRoot
	}
	if err := pluginpkg.Upsert(t.WorkGround2Home, installed); err != nil {
		return err
	}
	act.Target = target
	act.ManifestKind = pkg.ManifestKind
	act.Version = pkg.Manifest.Version
	act.SkillCount, act.HookCount, act.ToolCount = pkg.CapabilityCounts()
	if pkg.Manifest.AddOn != nil {
		act.AddOnKind = pkg.Manifest.AddOn.Kind
		if pkg.Manifest.AddOn.Runtime != nil {
			act.AddOnRuntime = pkg.Manifest.AddOn.Runtime.Type
		}
		act.AddOnPanels, act.AddOnSecrets = pkg.AddOnCounts()
	}
	return nil
}

func installedPluginState(WorkGround2Home, name string) (pluginpkg.InstalledPlugin, bool, error) {
	st, err := pluginpkg.LoadState(WorkGround2Home)
	if err != nil {
		return pluginpkg.InstalledPlugin{}, false, err
	}
	for _, p := range st.Plugins {
		if p.Name == name {
			return p, true, nil
		}
	}
	return pluginpkg.InstalledPlugin{}, false, nil
}

func (t *installSourceTool) preparePluginSource(ctx context.Context, source, mode string) (string, func(), error) {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "git:github.com/") {
		source = "https://github.com/" + strings.TrimPrefix(source, "git:github.com/")
	}
	if isURL(source) {
		src, ok := parseGitHubRepoSource(source)
		if !ok {
			return "", func() {}, newErr(ErrUnsupportedKind, "plugin URL %q is not a GitHub repository", source)
		}
		tmp, err := os.MkdirTemp("", "WorkGround2-plugin-*")
		if err != nil {
			return "", func() {}, err
		}
		cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", src.Owner, src.Repo)
		args := []string{"clone", "--depth=1"}
		if src.Branch != "" {
			args = append(args, "--branch", src.Branch)
		}
		args = append(args, cloneURL, tmp)
		cmd := exec.CommandContext(ctx, "git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(tmp)
			return "", func() {}, newErr(ErrSourceUnreadable, "git clone failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
		root := tmp
		if src.Path != "" {
			root = filepath.Join(tmp, filepath.FromSlash(src.Path))
		}
		return root, func() { _ = os.RemoveAll(tmp) }, nil
	}
	path := t.resolvePath(source)
	if looksLikePluginArchive(path) {
		if mode == "link" {
			return "", func() {}, newErr(ErrUnsupportedKind, "plugin archive %s cannot be installed with mode=link", path)
		}
		return extractPluginArchive(path)
	}
	if mode == "link" {
		return path, func() {}, nil
	}
	return path, func() {}, nil
}

func replaceCopiedPlugin(sourceRoot, target string, replace bool) error {
	exists, err := pathExists(target)
	if err != nil {
		return err
	}
	if exists {
		if !replace {
			return newErr(ErrAlreadyExists, "plugin package already exists at %s; retry with replace=true to update it", target)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+"-new-*")
	if err != nil {
		return err
	}
	if err := copyDir(sourceRoot, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	backup := ""
	if exists {
		backup = backupPath(target)
		if err := os.Rename(target, backup); err != nil {
			_ = os.RemoveAll(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		_ = os.RemoveAll(tmp)
		return err
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func replaceSymlink(target, sourceRoot string, replace bool) error {
	exists, err := pathExists(target)
	if err != nil {
		return err
	}
	if exists {
		if !replace {
			return newErr(ErrAlreadyExists, "plugin package already exists at %s; retry with replace=true to update it", target)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	backup := ""
	if exists {
		backup = backupPath(target)
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Symlink(sourceRoot, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return err
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func pathExists(path string) (bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func backupPath(path string) string {
	return path + ".old-" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
}

func (t *installSourceTool) applyRemovePluginPackage(_ request, act *action) error {
	installed, ok, err := pluginpkg.Remove(t.WorkGround2Home, act.Name)
	if err != nil || !ok {
		return err
	}
	root := pluginpkg.ResolveRoot(t.WorkGround2Home, installed.Root)
	if t.onDisconnect != nil {
		if pkg, _, err := pluginpkg.ParseDir(root); err == nil {
			names := make([]string, 0, len(pkg.Manifest.MCPServers))
			for name := range pkg.Manifest.MCPServers {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				t.onDisconnect(name)
			}
		}
	}
	pluginsDir := pluginpkg.PluginsDir(t.WorkGround2Home)
	if rel, err := filepath.Rel(pluginsDir, root); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		// Windows may hold file locks briefly after a subprocess exits.
		// Retry os.RemoveAll a few times so transient "Access is denied"
		// does not leave the plugin directory behind.
		if err := removeAllRetry(root); err != nil {
			return err
		}
	}
	return nil
}

// removeAllRetry removes root and its contents, retrying up to 3 times
// with backoff on failure. On Windows a just-killed subprocess may briefly
// hold file handles that cause os.RemoveAll to fail with "Access is denied".
// If all retries fail, it falls back to renaming the directory out of the way
// (same pattern used by replaceCopiedPlugin).
func removeAllRetry(root string) error {
	const maxRetries = 3
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := os.RemoveAll(root); err == nil {
			return nil
		} else {
			lastErr = err
		}
		// 200ms → 400ms → 800ms backoff
		time.Sleep(time.Duration(200*(1<<i)) * time.Millisecond)
	}
	// Last resort: rename so the plugin is gone from the live path even if
	// a lingering process still holds a handle inside the old tree.
	backup := backupPath(root)
	if err := os.Rename(root, backup); err != nil {
		return fmt.Errorf("removeAllRetry: %w; rename fallback also failed: %v", lastErr, err)
	}
	return nil
}
