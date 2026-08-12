package cdp

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"workground2/internal/browser"
)

// Discover finds a Chromium-based browser executable.
func Discover(kind browser.BrowserKind, explicitPath string) (browser.BrowserInfo, error) {
	if !validKind(kind) {
		return browser.BrowserInfo{}, fmt.Errorf("unsupported browser kind %q", kind)
	}
	if explicitPath != "" {
		return resolveExplicit(explicitPath, kind)
	}
	return discoverWith(kind, findInstalled)
}

// resolveExplicit validates and returns info for an explicit executable path.
func resolveExplicit(exePath string, kind browser.BrowserKind) (browser.BrowserInfo, error) {
	abs, err := filepath.Abs(exePath)
	if err != nil {
		return browser.BrowserInfo{}, fmt.Errorf("resolve executable path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return browser.BrowserInfo{}, fmt.Errorf("executable not found at %s: %w", abs, err)
	}
	if kind == "" || kind == browser.BrowserAuto {
		kind = guessKind(abs)
	}
	return browser.BrowserInfo{
		Kind:            kind,
		Product:         string(kind),
		Version:         "", // filled by Driver after launch
		ProtocolVersion: "",
		ExecutablePath:  abs,
	}, nil
}

// discoverByKind finds a browser by kind and platform discovery order.
func discoverWith(kind browser.BrowserKind, find func(browser.BrowserKind) (string, error)) (browser.BrowserInfo, error) {
	if kind == "" || kind == browser.BrowserAuto {
		var errs []string
		for _, candidate := range browser.BrowserAutoOrder {
			path, err := find(candidate)
			if err == nil {
				return discoveredInfo(candidate, path), nil
			}
			errs = append(errs, err.Error())
		}
		return browser.BrowserInfo{}, fmt.Errorf("auto-discovery failed: %s", strings.Join(errs, "; "))
	}
	path, err := find(kind)
	if err != nil {
		return browser.BrowserInfo{}, err
	}
	return discoveredInfo(kind, path), nil
}

func discoveredInfo(kind browser.BrowserKind, path string) browser.BrowserInfo {
	return browser.BrowserInfo{Kind: kind, Product: string(kind), ExecutablePath: path}
}

// findInstalled finds a specific browser kind on the host.
func findInstalled(kind browser.BrowserKind) (string, error) {
	paths := platformSearchPaths(kind)
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Also try PATH lookup.
	name := executableName(kind)
	if name != "" {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found", kind)
}

func validKind(kind browser.BrowserKind) bool {
	switch kind {
	case "", browser.BrowserAuto, browser.BrowserChrome, browser.BrowserEdge,
		browser.BrowserChromium, browser.BrowserChromeForTesting:
		return true
	default:
		return false
	}
}

// executableName returns the binary name for a kind.
func executableName(kind browser.BrowserKind) string {
	switch kind {
	case browser.BrowserChrome:
		return chromeExeName()
	case browser.BrowserEdge:
		return edgeExeName()
	case browser.BrowserChromium:
		return chromiumExeName()
	case browser.BrowserChromeForTesting:
		return chromeForTestingExeName()
	}
	return ""
}

func chromeExeName() string {
	if runtime.GOOS == "windows" {
		return "chrome.exe"
	}
	return "google-chrome"
}

func edgeExeName() string {
	if runtime.GOOS == "windows" {
		return "msedge.exe"
	}
	return "microsoft-edge"
}

func chromiumExeName() string {
	if runtime.GOOS == "windows" {
		// chrome.exe is ambiguous on Windows and commonly resolves to Google
		// Chrome. Chromium discovery therefore uses installation candidates only.
		return ""
	}
	return "chromium"
}

func chromeForTestingExeName() string {
	if runtime.GOOS == "windows" {
		// CfT also ships as chrome.exe; generic PATH lookup cannot prove its kind.
		return ""
	}
	return "chrome"
}

// platformSearchPaths returns candidate paths for a browser kind on the current OS.
func platformSearchPaths(kind browser.BrowserKind) []string {
	return platformSearchPathsFor(runtime.GOOS, kind, os.Getenv)
}

func platformSearchPathsFor(goos string, kind browser.BrowserKind, getenv func(string) string) []string {
	switch goos {
	case "windows":
		return windowsSearchPaths(kind, getenv)
	case "darwin":
		return darwinSearchPaths(kind, getenv)
	default:
		return linuxSearchPaths(kind, getenv)
	}
}

func windowsSearchPaths(kind browser.BrowserKind, getenv func(string) string) []string {
	switch kind {
	case browser.BrowserChrome:
		return []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
		}
	case browser.BrowserEdge:
		return []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		}
	case browser.BrowserChromium:
		return []string{
			filepath.Join(getenv("LOCALAPPDATA"), `Chromium\Application\chrome.exe`),
			`C:\Program Files\Chromium\Application\chrome.exe`,
		}
	case browser.BrowserChromeForTesting:
		// Chrome for Testing is typically installed locally.
		home := getenv("USERPROFILE")
		if home == "" {
			home = getenv("HOMEDRIVE") + getenv("HOMEPATH")
		}
		return []string{
			filepath.Join(home, `chrome-for-testing\chrome.exe`),
		}
	}
	return nil
}

func linuxSearchPaths(kind browser.BrowserKind, getenv func(string) string) []string {
	switch kind {
	case browser.BrowserChrome:
		return []string{
			"/opt/google/chrome/chrome",
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			path.Join(getenv("HOME"), ".local/bin/google-chrome"),
		}
	case browser.BrowserEdge:
		return []string{
			"/opt/microsoft/msedge/msedge",
			"/usr/bin/microsoft-edge",
			"/usr/bin/microsoft-edge-stable",
		}
	case browser.BrowserChromium:
		return []string{
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
		}
	case browser.BrowserChromeForTesting:
		return []string{
			path.Join(getenv("HOME"), "chrome-for-testing/chrome"),
		}
	}
	return nil
}

func darwinSearchPaths(kind browser.BrowserKind, getenv func(string) string) []string {
	home := getenv("HOME")
	switch kind {
	case browser.BrowserChrome:
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			path.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
		}
	case browser.BrowserEdge:
		return []string{
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			path.Join(home, "Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"),
		}
	case browser.BrowserChromium:
		return []string{
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			path.Join(home, "Applications/Chromium.app/Contents/MacOS/Chromium"),
		}
	case browser.BrowserChromeForTesting:
		return []string{
			"/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			path.Join(home, "Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"),
			path.Join(home, "chrome-for-testing/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"),
		}
	}
	return nil
}

func guessKind(path string) browser.BrowserKind {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "edge") || strings.Contains(lower, "msedge") {
		return browser.BrowserEdge
	}
	if strings.Contains(lower, "chromium") && !strings.Contains(lower, "chrome") {
		return browser.BrowserChromium
	}
	if strings.Contains(lower, "chrome-for-testing") {
		return browser.BrowserChromeForTesting
	}
	return browser.BrowserChrome
}
