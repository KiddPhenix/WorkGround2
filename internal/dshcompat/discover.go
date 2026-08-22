package dshcompat

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"workground2/internal/pluginpkg"
)

// Discover returns runnable specs for enabled DSH packages plus explicit
// diagnostics for packages whose runtime anchor is unavailable.
func Discover(WorkGround2Home, workspace string, stderr io.Writer) ([]Spec, []string) {
	installed, loadWarnings := pluginpkg.LoadInstalled(WorkGround2Home)
	warnings := append([]string(nil), loadWarnings...)
	var specs []Spec
	for _, item := range installed {
		bundle := item.Package.Manifest.DSH
		if bundle == nil {
			continue
		}
		bundleRoot := ResolveBundleRoot(item, workspace)
		if bundleRoot == item.Package.Root {
			warnings = append(warnings, item.Warnings...)
		} else if sourcePkg, sourceWarnings, err := pluginpkg.ParseDir(bundleRoot); err != nil {
			warnings = append(warnings, fmt.Sprintf("dsh %s source fallback: %v", item.Installed.Name, err))
			continue
		} else {
			bundle = sourcePkg.Manifest.DSH
			warnings = append(warnings, sourceWarnings...)
		}
		anchor, err := ResolveRuntimeAnchor(bundleRoot)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("dsh %s: %v", item.Installed.Name, err))
			continue
		}
		specs = append(specs, Spec{
			Name:              item.Installed.Name,
			BundlePackageJSON: filepath.Join(bundleRoot, pluginpkg.DSHManifest),
			RuntimeAnchor:     anchor,
			Workspace:         workspace,
			DSHHome:           filepath.Join(WorkGround2Home, "dsh", item.Installed.Name),
			RuntimeDir:        filepath.Join(WorkGround2Home, "runtime", "dsh"),
			Stderr:            stderr,
		})
	}
	return specs, warnings
}

// ResolveBundleRoot prefers the managed install, then falls back to a still
// valid local source when copy mode omitted workspace dependency symlinks.
func ResolveBundleRoot(item pluginpkg.InstalledPackage, workspace string) string {
	root := item.Package.Root
	if item.Package.Manifest.DSH == nil || len(item.Package.Manifest.DSH.Report.MissingPackages) == 0 {
		return root
	}
	source := strings.TrimSpace(item.Installed.Source)
	if source == "" || strings.Contains(source, "://") || strings.HasPrefix(source, "git:") {
		return root
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(workspace, source)
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return root
	}
	pkg, _, err := pluginpkg.ParseDir(source)
	if err == nil && pkg.Manifest.DSH != nil {
		return pkg.Root
	}
	return root
}

// ResolveRuntimeAnchor locates a DSH host package that can resolve
// @deepseek-ai/dsh-app-boot. DSH_RUNTIME_ANCHOR is the explicit recovery seam.
func ResolveRuntimeAnchor(bundleRoot string) (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("DSH_RUNTIME_ANCHOR")); explicit != "" {
		explicit = filepath.Clean(explicit)
		if resolvesAppBoot(explicit) {
			return explicit, nil
		}
		return "", fmt.Errorf("DSH_RUNTIME_ANCHOR %s is not a DSH runtime package.json", explicit)
	}
	seen := map[string]bool{}
	var candidates []string
	for dir := filepath.Clean(bundleRoot); ; dir = filepath.Dir(dir) {
		candidates = append(candidates,
			filepath.Join(dir, "apps", "cli", pluginpkg.DSHManifest),
			filepath.Join(dir, "node_modules", "@deepseek-ai", "dsh", pluginpkg.DSHManifest),
			filepath.Join(dir, pluginpkg.DSHManifest),
		)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if resolvesAppBoot(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot locate @deepseek-ai/dsh-app-boot from %s; install DSH or set DSH_RUNTIME_ANCHOR", bundleRoot)
}

func resolvesAppBoot(anchor string) bool {
	dir := filepath.Dir(anchor)
	for current := dir; ; current = filepath.Dir(current) {
		candidate := filepath.Join(current, "node_modules", "@deepseek-ai", "dsh-app-boot", pluginpkg.DSHManifest)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}
