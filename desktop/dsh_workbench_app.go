package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/config"
	"workground2/internal/dshcompat"
	"workground2/internal/pluginpkg"
)

type DSHWorkbenchView struct {
	PluginName string `json:"pluginName"`
	URL        string `json:"url,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
}

type dshWorkbench struct {
	host *dshcompat.WebHost
	view DSHWorkbenchView
}

// StartDSHWorkbench starts or reuses a loopback-only DSH Web host whose own
// React runtime renders the selected Bundle's client plugins.
func (a *App) StartDSHWorkbench(name string) (DSHWorkbenchView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return DSHWorkbenchView{}, errors.New("plugin name is required")
	}
	a.dshWorkbenchMu.Lock()
	defer a.dshWorkbenchMu.Unlock()
	if current := a.dshWorkbenches[name]; current != nil {
		select {
		case <-current.host.Done():
			delete(a.dshWorkbenches, name)
		default:
			return current.view, nil
		}
	}

	item, root, err := a.dshBundleForWorkbench(name)
	if err != nil {
		return DSHWorkbenchView{}, err
	}
	anchor, err := dshcompat.ResolveRuntimeAnchor(root)
	if err != nil {
		return DSHWorkbenchView{}, err
	}
	startCtx, cancel := context.WithTimeout(a.bootContext(), 45*time.Second)
	defer cancel()
	host, err := dshcompat.StartWeb(startCtx, dshcompat.WebSpec{
		RuntimeAnchor: anchor,
		BundlePatch:   filepath.Join(root, filepath.FromSlash(item.Package.Manifest.DSH.Patch)),
		BundleName:    item.Package.Manifest.DSH.PackageName,
		Workspace:     a.activeWorkspaceRoot(),
		DSHHome:       filepath.Join(config.WorkGround2HomeDir(), "dsh", name),
		Stderr:        dshWorkbenchLog{name: name},
	})
	if err != nil {
		return DSHWorkbenchView{}, err
	}
	view := DSHWorkbenchView{
		PluginName: name,
		URL:        host.URL(),
		Status:     "ready",
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	a.dshWorkbenches[name] = &dshWorkbench{host: host, view: view}
	return view, nil
}

func (a *App) DSHWorkbench(name string) DSHWorkbenchView {
	a.dshWorkbenchMu.Lock()
	defer a.dshWorkbenchMu.Unlock()
	current := a.dshWorkbenches[strings.TrimSpace(name)]
	if current == nil {
		return DSHWorkbenchView{PluginName: name, Status: "stopped"}
	}
	select {
	case <-current.host.Done():
		view := current.view
		view.Status = "failed"
		if err := current.host.Err(); err != nil {
			view.Error = err.Error()
		} else {
			view.Error = "DSH web mirror stopped"
		}
		return view
	default:
		return current.view
	}
}

func (a *App) StopDSHWorkbench(name string) DSHWorkbenchView {
	name = strings.TrimSpace(name)
	a.dshWorkbenchMu.Lock()
	current := a.dshWorkbenches[name]
	delete(a.dshWorkbenches, name)
	a.dshWorkbenchMu.Unlock()
	if current != nil {
		current.host.Close()
	}
	return DSHWorkbenchView{PluginName: name, Status: "stopped"}
}

func (a *App) closeDSHWorkbenches() {
	a.dshWorkbenchMu.Lock()
	workbenches := a.dshWorkbenches
	a.dshWorkbenches = map[string]*dshWorkbench{}
	a.dshWorkbenchMu.Unlock()
	for _, workbench := range workbenches {
		workbench.host.Close()
	}
}

func (a *App) dshBundleForWorkbench(name string) (pluginpkg.InstalledPackage, string, error) {
	installed, warnings := pluginpkg.LoadInstalled(config.WorkGround2HomeDir())
	for _, warning := range warnings {
		slog.Warn("desktop: DSH package discovery warning", "warning", warning)
	}
	for _, item := range installed {
		if item.Installed.Name != name || item.Package.Manifest.DSH == nil {
			continue
		}
		root := dshcompat.ResolveBundleRoot(item, a.activeWorkspaceRoot())
		if root != item.Package.Root {
			pkg, sourceWarnings, err := pluginpkg.ParseDir(root)
			if err != nil {
				return pluginpkg.InstalledPackage{}, "", fmt.Errorf("parse DSH source fallback: %w", err)
			}
			item.Package = pkg
			item.Warnings = sourceWarnings
		}
		return item, root, nil
	}
	return pluginpkg.InstalledPackage{}, "", fmt.Errorf("enabled DSH plugin %q is not installed", name)
}

type dshWorkbenchLog struct{ name string }

func (w dshWorkbenchLog) Write(p []byte) (int, error) {
	message := strings.TrimSpace(string(p))
	if message != "" {
		slog.Info("desktop: DSH workbench", "plugin", w.name, "message", message)
	}
	return len(p), nil
}

var _ io.Writer = dshWorkbenchLog{}
