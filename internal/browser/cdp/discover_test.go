package cdp

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"workground2/internal/browser"
)

func testEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDiscoverExplicitPathWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-browser")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := Discover(browser.BrowserEdge, path)
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(path)
	if info.ExecutablePath != abs || info.Kind != browser.BrowserEdge {
		t.Fatalf("unexpected explicit discovery: %+v", info)
	}
}

func TestDiscoverAutoOrder(t *testing.T) {
	var calls []browser.BrowserKind
	info, err := discoverWith(browser.BrowserAuto, func(kind browser.BrowserKind) (string, error) {
		calls = append(calls, kind)
		if kind == browser.BrowserChromium {
			return "chromium-path", nil
		}
		return "", errors.New("not installed")
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []browser.BrowserKind{browser.BrowserChrome, browser.BrowserEdge, browser.BrowserChromium}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("order = %v, want %v", calls, wantCalls)
	}
	if info.Kind != browser.BrowserChromium || info.ExecutablePath != "chromium-path" {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestDiscoverSpecificKind(t *testing.T) {
	var calls []browser.BrowserKind
	info, err := discoverWith(browser.BrowserChromeForTesting, func(kind browser.BrowserKind) (string, error) {
		calls = append(calls, kind)
		return "cft", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []browser.BrowserKind{browser.BrowserChromeForTesting}) || info.Kind != browser.BrowserChromeForTesting {
		t.Fatalf("specific discovery escaped kind boundary: calls=%v info=%+v", calls, info)
	}
}

func TestDiscoverRejectsUnknownKind(t *testing.T) {
	if _, err := Discover(browser.BrowserKind("firefox"), ""); err == nil {
		t.Fatal("expected unsupported kind error")
	}
}

func TestWindowsAmbiguousChromeExeIsNotUsedForChromiumOrCfT(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable-name rule")
	}
	if got := executableName(browser.BrowserChromium); got != "" {
		t.Fatalf("Chromium PATH executable = %q, want empty", got)
	}
	if got := executableName(browser.BrowserChromeForTesting); got != "" {
		t.Fatalf("CfT PATH executable = %q, want empty", got)
	}
}

func TestPlatformSearchPathsCoverSupportedKinds(t *testing.T) {
	tests := []struct {
		goos string
		env  map[string]string
		want map[browser.BrowserKind]string
	}{
		{
			goos: "windows", env: map[string]string{"LOCALAPPDATA": `C:\Users\test\AppData\Local`, "USERPROFILE": `C:\Users\test`},
			want: map[browser.BrowserKind]string{
				browser.BrowserChrome: `Google\Chrome\Application\chrome.exe`, browser.BrowserEdge: `Microsoft\Edge\Application\msedge.exe`,
				browser.BrowserChromium: `Chromium\Application\chrome.exe`, browser.BrowserChromeForTesting: `chrome-for-testing\chrome.exe`,
			},
		},
		{
			goos: "linux", env: map[string]string{"HOME": "/home/test"},
			want: map[browser.BrowserKind]string{
				browser.BrowserChrome: "google-chrome", browser.BrowserEdge: "microsoft-edge", browser.BrowserChromium: "chromium", browser.BrowserChromeForTesting: "chrome-for-testing/chrome",
			},
		},
		{
			goos: "darwin", env: map[string]string{"HOME": "/Users/test"},
			want: map[browser.BrowserKind]string{
				browser.BrowserChrome: "Google Chrome.app", browser.BrowserEdge: "Microsoft Edge.app", browser.BrowserChromium: "Chromium.app", browser.BrowserChromeForTesting: "Google Chrome for Testing.app",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			for _, kind := range browser.BrowserAutoOrder {
				paths := platformSearchPathsFor(tc.goos, kind, testEnv(tc.env))
				if len(paths) == 0 {
					t.Fatalf("%s has no %s candidates", tc.goos, kind)
				}
				joined := strings.Join(paths, "\n")
				if !strings.Contains(joined, tc.want[kind]) {
					t.Fatalf("%s %s candidates %q missing %q", tc.goos, kind, paths, tc.want[kind])
				}
			}
		})
	}
}
