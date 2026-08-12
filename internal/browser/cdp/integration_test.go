//go:build browser_integration

package cdp_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/browser"
	"workground2/internal/browser/cdp"
)

type processDriver interface {
	browser.Driver
	ProcessID() int
	ProcessExited() bool
}

type captureFactory struct {
	base         browser.DriverFactory
	mu           sync.Mutex
	drivers      []processDriver
	observations []browser.Observation
}

func (f *captureFactory) New(ctx context.Context, opts browser.DriverOptions) (browser.Driver, error) {
	driver, err := f.base.New(ctx, opts)
	if err != nil {
		return nil, err
	}
	wrapped := &captureDriver{Driver: driver, owner: f}
	if process, ok := driver.(processDriver); ok {
		f.mu.Lock()
		f.drivers = append(f.drivers, process)
		f.mu.Unlock()
	}
	return wrapped, nil
}

func (f *captureFactory) onlyProcess(t *testing.T) processDriver {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.drivers) != 1 {
		t.Fatalf("captured process drivers=%d, want 1", len(f.drivers))
	}
	return f.drivers[0]
}

type captureDriver struct {
	browser.Driver
	owner *captureFactory
}

func (d *captureDriver) Observe(ctx context.Context, opts browser.ObserveOptions) (browser.Observation, error) {
	observation, err := d.Driver.Observe(ctx, opts)
	if err == nil {
		d.owner.mu.Lock()
		d.owner.observations = append(d.owner.observations, observation)
		d.owner.mu.Unlock()
	}
	return observation, err
}

// TestChromeWorkflow is intentionally behind two independent gates: the build
// tag keeps it out of normal binaries/tests, and the env var prevents an
// accidental tagged run from launching a local browser.
func TestChromeWorkflow(t *testing.T) {
	if os.Getenv("WORKGROUND2_BROWSER_INTEGRATION") != "1" {
		t.Skip("set WORKGROUND2_BROWSER_INTEGRATION=1 and use -tags browser_integration")
	}
	info, err := cdp.Discover(browser.BrowserAuto, os.Getenv("WORKGROUND2_BROWSER_EXECUTABLE"))
	if err != nil {
		t.Skipf("no supported Chrome/Chromium browser installed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Browser Integration</title>
<label>Name <input id="name" aria-label="Name"></label>
<label>Password <input id="pw" type="password" aria-label="Password"></label>
<input id="file" type="file" aria-label="Upload">
<button id="submit" onclick="document.getElementById('result').textContent='clicked:'+document.getElementById('name').value">Submit</button>
<button id="grow" onclick="document.body.style.height='4000px';this.textContent='grown'">Grow</button>
<a id="download" download="blocked.txt" href="/download">Download</a>
<p id="result">idle</p>
<p id="file-result">idle</p>
<script>
document.getElementById('file').addEventListener('change', function (event) {
  var file = event.target.files[0];
  var reader = new FileReader();
  reader.onload = function () {
    document.getElementById('file-result').textContent = 'file:' + file.name + ':' + reader.result;
  };
  reader.readAsText(file);
});
</script>`)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Next</title><p>navigated</p>`)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			fmt.Fprint(w, "slow")
		}
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="blocked.txt"`)
		fmt.Fprint(w, "must-not-be-written")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	profileRoot := t.TempDir()
	captured := &captureFactory{base: cdp.NewFactory(cdp.Options{})}
	manager, err := browser.NewManager(ctx, browser.Options{
		Factory: captured, BrowserKind: info.Kind,
		ExecutablePath: info.ExecutablePath, Headless: true,
		ProfileRoot: profileRoot, ActionTimeout: 20 * time.Second,
		StateTimeout: 20 * time.Second, IdleTimeout: time.Minute,
		AllowPasswordInput: true, AllowFileUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	const owner = "cdp-integration"
	opened, err := manager.Open(ctx, owner, browser.OpenRequest{URL: server.URL, RequestID: "open-1"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	inputIndex := findElement(t, state, func(element browser.Element) bool { return element.InputType == "text" && element.Name == "Name" })
	if _, err := manager.Type(ctx, owner, browser.TypeRequest{
		Revision: state.Revision, Index: inputIndex, Text: "workground2", Clear: true, RequestID: "type-1",
	}); err != nil {
		t.Fatal(err)
	}

	// Password input: allowed by default through the production CDP path.
	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatal(err)
	}
	passwordIndex := findElement(t, state, func(element browser.Element) bool {
		return element.InputType == "password" && element.Name == "Password"
	})
	if _, err := manager.Type(ctx, owner, browser.TypeRequest{
		Revision: state.Revision, Index: passwordIndex, Text: "sup3r-secret", Clear: true, RequestID: "type-pw",
	}); err != nil {
		t.Fatalf("password type: %v", err)
	}

	// Local file upload: FileReader exposes filename+content in page text.
	uploadFile := filepath.Join(t.TempDir(), "payload-upload.txt")
	if err := os.WriteFile(uploadFile, []byte("uploaded-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatal(err)
	}
	fileIndex := findElement(t, state, func(element browser.Element) bool { return element.InputType == "file" && element.Name == "Upload" })
	if _, err := manager.Upload(ctx, owner, browser.UploadRequest{
		Revision: state.Revision, Index: fileIndex, Files: []string{uploadFile}, RequestID: "upload-1",
	}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	// The page updates asynchronously through FileReader.onload; poll State.
	deadline := time.Now().Add(10 * time.Second)
	var uploadText string
	for time.Now().Before(deadline) {
		state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: true})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(state.Text, "file:payload-upload.txt:uploaded-content") {
			uploadText = state.Text
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if uploadText == "" {
		t.Fatalf("state after upload never exposed FileReader result: text=%q", state.Text)
	}

	_, err = manager.Click(ctx, owner, browser.ClickRequest{Revision: opened.Revision, Index: inputIndex, RequestID: "stale-1"})
	var stale *browser.Error
	if !errors.As(err, &stale) || stale.Code != browser.ErrStaleState {
		t.Fatalf("old revision error = %v, want stale_state", err)
	}

	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatal(err)
	}
	buttonIndex := findElement(t, state, func(element browser.Element) bool { return element.Role == "button" && element.Name == "Submit" })
	_, err = manager.Click(ctx, owner, browser.ClickRequest{
		Revision: state.Revision, Index: buttonIndex, RequestID: "click-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil || !strings.Contains(state.Text, "clicked:workground2") {
		t.Fatalf("state after click: text=%q err=%v", state.Text, err)
	}
	grow := findElement(t, state, func(element browser.Element) bool { return element.Name == "Grow" })
	grown, err := manager.Click(ctx, owner, browser.ClickRequest{Revision: state.Revision, Index: grow, RequestID: "grow-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Scroll(ctx, owner, browser.ScrollRequest{Revision: grown.AfterRevision, DeltaY: 800, RequestID: "scroll-1"}); err != nil {
		t.Fatal(err)
	}
	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	download := findElement(t, state, func(element browser.Element) bool { return element.Name == "Download" })
	runningProfiles, err := filepath.Glob(filepath.Join(profileRoot, "wg2-browser-*"))
	if err != nil || len(runningProfiles) != 1 {
		t.Fatalf("running profiles=%v err=%v", runningProfiles, err)
	}
	downloadDir := filepath.Join(runningProfiles[0], "Downloads")
	beforeDownloads, err := findDownloadArtifacts(downloadDir)
	if err != nil || len(beforeDownloads) != 0 {
		t.Fatalf("download artifacts existed before click: %v err=%v", beforeDownloads, err)
	}
	if _, err := manager.Click(ctx, owner, browser.ClickRequest{Revision: state.Revision, Index: download, RequestID: "download-1"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	afterDownloads, err := findDownloadArtifacts(downloadDir)
	if err != nil || len(afterDownloads) != 0 {
		t.Fatalf("download deny leaked artifacts: %v err=%v", afterDownloads, err)
	}
	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	navigated, err := manager.Navigate(ctx, owner, browser.NavigateRequest{URL: server.URL + "/next", RequestID: "navigate-1"})
	if err != nil || navigated.URL != server.URL+"/next" {
		t.Fatalf("navigate result=%+v err=%v", navigated, err)
	}

	newTab, err := manager.Tab(ctx, owner, browser.TabRequest{
		Revision: navigated.AfterRevision, Action: browser.TabNew, URL: server.URL, RequestID: "tab-new-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: false})
	if err != nil || len(state.Tabs) != 2 {
		t.Fatalf("new tab state: tabs=%d err=%v", len(state.Tabs), err)
	}
	var otherTab string
	for _, tab := range state.Tabs {
		if !tab.Active {
			otherTab = tab.ID
		}
	}
	activated, err := manager.Tab(ctx, owner, browser.TabRequest{
		Revision: newTab.AfterRevision, Action: browser.TabActivate, TabID: otherTab, RequestID: "tab-activate-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatal(err)
	}
	var closeID string
	for _, tab := range state.Tabs {
		if tab.Active {
			closeID = tab.ID
		}
	}
	closedTab, err := manager.Tab(ctx, owner, browser.TabRequest{
		Revision: activated.AfterRevision, Action: browser.TabClose, TabID: closeID, RequestID: "tab-close-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = manager.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil || state.Revision < closedTab.AfterRevision || len(state.Tabs) != 1 {
		t.Fatalf("state after tab close: revision=%d tabs=%+v err=%v", state.Revision, state.Tabs, err)
	}
	for _, tab := range state.Tabs {
		if tab.ID == closeID {
			t.Fatalf("closed tab %q remains: %+v", closeID, state.Tabs)
		}
	}
	profileDirs, err := filepath.Glob(filepath.Join(profileRoot, "wg2-browser-*"))
	if err != nil || len(profileDirs) != 1 {
		t.Fatalf("running browser profile dirs=%v err=%v", profileDirs, err)
	}
	endpoint := filepath.Join(profileDirs[0], "DevToolsActivePort")
	_, endpointErr := os.Stat(endpoint)
	if err := manager.Close(); err != nil {
		t.Fatalf("manager close: %v", err)
	}
	process := captured.onlyProcess(t)
	if process.ProcessID() <= 0 || !process.ProcessExited() {
		t.Fatalf("browser process evidence pid=%d exited=%v", process.ProcessID(), process.ProcessExited())
	}
	profiles, err := filepath.Glob(filepath.Join(profileRoot, "wg2-browser-*"))
	if err != nil || len(profiles) != 0 {
		t.Fatalf("temporary profile leaked after close: %v (glob err=%v)", profiles, err)
	}
	if endpointErr == nil {
		if _, err := os.Stat(endpoint); !os.IsNotExist(err) {
			t.Fatalf("DevTools endpoint survived Manager.Close: %v", err)
		}
	}
}

func findDownloadArtifacts(root string) ([]string, error) {
	var found []string
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return found, nil
	} else if err != nil {
		return nil, err
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := strings.ToLower(entry.Name())
		if !entry.IsDir() && (name == "blocked.txt" || strings.HasSuffix(name, ".crdownload")) {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}

func TestChromeCancellationIsolationAndIdleReaper(t *testing.T) {
	if os.Getenv("WORKGROUND2_BROWSER_INTEGRATION") != "1" {
		t.Skip("set WORKGROUND2_BROWSER_INTEGRATION=1 and use -tags browser_integration")
	}
	info, err := cdp.Discover(browser.BrowserAuto, os.Getenv("WORKGROUND2_BROWSER_EXECUTABLE"))
	if err != nil {
		t.Skipf("no supported Chrome/Chromium browser installed: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		}
		fmt.Fprint(w, `<!doctype html><title>Alive</title><button>ok</button>`)
	}))
	defer server.Close()
	root := t.TempDir()
	captured := &captureFactory{base: cdp.NewFactory(cdp.Options{})}
	mgr, err := browser.NewManager(context.Background(), browser.Options{
		Factory: captured, BrowserKind: info.Kind, ExecutablePath: info.ExecutablePath,
		Headless: true, ProfileRoot: root, ActionTimeout: 5 * time.Second, StateTimeout: 5 * time.Second,
		IdleTimeout: 3 * time.Second, SettleWindow: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if _, err := mgr.Open(context.Background(), "one", browser.OpenRequest{URL: server.URL, RequestID: "one-open"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Open(context.Background(), "two", browser.OpenRequest{URL: server.URL, RequestID: "two-open"}); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := mgr.Navigate(cancelCtx, "one", browser.NavigateRequest{URL: server.URL + "/slow", RequestID: "slow"}); err == nil {
		t.Fatal("canceled navigation succeeded")
	}
	if _, err := mgr.State(context.Background(), "two", browser.StateRequest{Refresh: true}); err != nil {
		t.Fatalf("owner two killed by owner one cancellation: %v", err)
	}
	// Do not poll State while waiting: State intentionally refreshes lastUsed.
	time.Sleep(4 * time.Second)
	deadline := time.Now().Add(5 * time.Second)
	var profiles []string
	for time.Now().Before(deadline) {
		profiles, _ = filepath.Glob(filepath.Join(root, "wg2-browser-*"))
		if len(profiles) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(profiles) != 0 {
		t.Fatalf("idle reaper leaked profiles/endpoints: %v", profiles)
	}
	captured.mu.Lock()
	processes := append([]processDriver(nil), captured.drivers...)
	captured.mu.Unlock()
	if len(processes) != 2 {
		t.Fatalf("captured processes=%d, want 2", len(processes))
	}
	for _, process := range processes {
		if process.ProcessID() <= 0 || !process.ProcessExited() {
			t.Fatalf("idle process evidence pid=%d exited=%v", process.ProcessID(), process.ProcessExited())
		}
	}
}

func TestChromeCrossOriginIframeTargetRouting(t *testing.T) {
	if os.Getenv("WORKGROUND2_BROWSER_INTEGRATION") != "1" {
		t.Skip("set WORKGROUND2_BROWSER_INTEGRATION=1 and use -tags browser_integration")
	}
	info, err := cdp.Discover(browser.BrowserAuto, os.Getenv("WORKGROUND2_BROWSER_EXECUTABLE"))
	if err != nil {
		t.Skipf("no supported Chrome/Chromium browser installed: %v", err)
	}
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Child</title><button aria-label="Iframe Button" onclick="document.body.append(' iframe-clicked')">Iframe Button</button>`)
	}))
	defer child.Close()
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<!doctype html><title>Outer</title><iframe src=%q></iframe>`, child.URL)
	}))
	defer outer.Close()
	// localhost and 127.0.0.1 force different sites while retaining local-only IO.
	outerURL := strings.Replace(outer.URL, "127.0.0.1", "localhost", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	captured := &captureFactory{base: cdp.NewFactory(cdp.Options{})}
	mgr, err := browser.NewManager(ctx, browser.Options{
		Factory: captured, BrowserKind: info.Kind, ExecutablePath: info.ExecutablePath,
		Headless: true, ProfileRoot: t.TempDir(), ActionTimeout: 15 * time.Second, StateTimeout: 15 * time.Second,
		IdleTimeout: time.Minute, SettleWindow: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	opened, err := mgr.Open(ctx, "iframe", browser.OpenRequest{URL: outerURL, RequestID: "open"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := mgr.State(ctx, "iframe", browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	index := findElement(t, state, func(element browser.Element) bool { return element.Name == "Iframe Button" })
	captured.mu.Lock()
	observations := append([]browser.Observation(nil), captured.observations...)
	captured.mu.Unlock()
	var iframeTarget string
	for _, observation := range observations {
		for _, node := range observation.Nodes {
			if node.Name == "Iframe Button" {
				iframeTarget = node.Ref.TargetID
			}
		}
	}
	if iframeTarget == "" || iframeTarget == state.ActiveTab {
		t.Fatalf("cross-origin iframe did not route to independent target: iframe=%q active=%q", iframeTarget, state.ActiveTab)
	}
	if _, err := mgr.Click(ctx, "iframe", browser.ClickRequest{Revision: opened.Revision, Index: index, RequestID: "iframe-click"}); err != nil {
		t.Fatal(err)
	}
	state, err = mgr.State(ctx, "iframe", browser.StateRequest{Refresh: true})
	if err != nil || !strings.Contains(state.Text, "iframe-clicked") {
		t.Fatalf("iframe click state text=%q warnings=%+v err=%v", state.Text, state.Warnings, err)
	}
}

func findElement(t *testing.T, state browser.PageState, match func(browser.Element) bool) int {
	t.Helper()
	for _, element := range state.Elements {
		if match(element) {
			return element.Index
		}
	}
	t.Fatalf("matching element missing from %+v", state.Elements)
	return 0
}
