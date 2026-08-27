//go:build browser_integration

package cdp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"workground2/internal/browser"
	"workground2/internal/browser/cdp"
)

type processDriver interface {
	browser.Driver
	ProcessID() int
	ProcessExited() bool
	DebugEndpoint() string
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
	clickHTTPRelay(t, ctx, manager, owner, server.URL, "relay-open-1")
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
	if err != nil {
		t.Fatalf("navigate result=%+v err=%v", navigated, err)
	}
	navigated = clickHTTPRelay(t, ctx, manager, owner, server.URL+"/next", "relay-navigate-1")

	newTab, err := manager.Tab(ctx, owner, browser.TabRequest{
		Revision: navigated.AfterRevision, Action: browser.TabNew, URL: server.URL, RequestID: "tab-new-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	newTab = clickHTTPRelay(t, ctx, manager, owner, server.URL, "relay-tab-new-1")
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
	clickHTTPRelay(t, context.Background(), mgr, "one", server.URL, "one-relay")
	if _, err := mgr.Open(context.Background(), "two", browser.OpenRequest{URL: server.URL, RequestID: "two-open"}); err != nil {
		t.Fatal(err)
	}
	clickHTTPRelay(t, context.Background(), mgr, "two", server.URL, "two-relay")
	if _, err := mgr.Navigate(context.Background(), "one", browser.NavigateRequest{URL: server.URL + "/slow", RequestID: "slow"}); err != nil {
		t.Fatal(err)
	}
	slowState, err := mgr.State(context.Background(), "one", browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatal(err)
	}
	slowLink := findElement(t, slowState, func(element browser.Element) bool { return element.Href == server.URL+"/slow" })
	cancelCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := mgr.Click(cancelCtx, "one", browser.ClickRequest{Revision: slowState.Revision, Index: slowLink, RequestID: "slow-click"}); err == nil {
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

func clickHTTPRelay(t *testing.T, ctx context.Context, mgr *browser.Manager, owner, target, requestID string) browser.ActionResult {
	t.Helper()
	state, err := mgr.State(ctx, owner, browser.StateRequest{Refresh: false})
	if err != nil {
		t.Fatalf("state HTTP relay for %q: %v", target, err)
	}
	index := findElement(t, state, func(element browser.Element) bool { return element.Href == target })
	result, err := mgr.Click(ctx, owner, browser.ClickRequest{
		Revision: state.Revision, Index: index, RequestID: requestID,
	})
	if err != nil {
		t.Fatalf("click HTTP relay for %q: %v", target, err)
	}
	if strings.TrimSuffix(result.URL, "/") != strings.TrimSuffix(target, "/") {
		t.Fatalf("HTTP relay landed on %q, want %q", result.URL, target)
	}
	return result
}

// TestChromeDebugPortWebdriverSignal verifies the production launch change on a
// real visible Chrome: the debug endpoint is a fresh nonzero loopback port
// (never port=0, never a wildcard address), /json/version is reachable on it,
// a visible browser does not expose navigator.webdriver=true, and the port
// becomes unreachable once the browser is closed. Headless is deliberately not
// covered: headless Chrome always reports navigator.webdriver=true regardless
// of the port, so no false promise is made here.
func TestChromeDebugPortWebdriverSignal(t *testing.T) {
	if os.Getenv("WORKGROUND2_BROWSER_INTEGRATION") != "1" {
		t.Skip("set WORKGROUND2_BROWSER_INTEGRATION=1 and use -tags browser_integration")
	}
	info, err := cdp.Discover(browser.BrowserAuto, os.Getenv("WORKGROUND2_BROWSER_EXECUTABLE"))
	if err != nil {
		t.Skipf("no supported Chrome/Chromium browser installed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	captured := &captureFactory{base: cdp.NewFactory(cdp.Options{})}
	mgr, err := browser.NewManager(ctx, browser.Options{
		Factory: captured, BrowserKind: info.Kind, ExecutablePath: info.ExecutablePath,
		Headless: false, ProfileRoot: t.TempDir(), ActionTimeout: 20 * time.Second,
		StateTimeout: 20 * time.Second, IdleTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if _, err := mgr.Open(ctx, "webdriver", browser.OpenRequest{URL: "about:blank", RequestID: "wd-open"}); err != nil {
		t.Fatal(err)
	}
	proc := captured.onlyProcess(t)
	endpoint := proc.DebugEndpoint()
	if endpoint == "" {
		t.Fatal("driver exposed no debug endpoint")
	}
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		t.Fatalf("debug endpoint %q not host:port: %v", endpoint, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("debug endpoint port %q is not a nonzero TCP port: %v", portStr, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("debug endpoint host = %q, want 127.0.0.1", host)
	}

	// The endpoint must actually serve CDP: GET /json/version on the loopback port.
	resp, err := http.Get("http://" + endpoint + "/json/version")
	if err != nil {
		t.Fatalf("GET /json/version on %s: %v", endpoint, err)
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&version)
	_ = resp.Body.Close()
	if decodeErr != nil || !strings.HasPrefix(version.WebSocketDebuggerURL, "ws://127.0.0.1:") {
		t.Fatalf("/json/version did not return a loopback ws URL: %v body-decode-err=%v", version.WebSocketDebuggerURL, decodeErr)
	}

	// Attach as a second CDP client through the real endpoint and read
	// navigator.webdriver on a visible browser. NewRemoteAllocator connects to
	// the existing browser; it does not launch a process and does not set
	// --enable-automation, so it cannot mask the signal under test.
	remoteCtx, remoteCancel := chromedp.NewRemoteAllocator(ctx, version.WebSocketDebuggerURL)
	defer remoteCancel()
	tctx, tCancel := chromedp.NewContext(remoteCtx)
	defer tCancel()
	var webdriver bool
	if err := chromedp.Run(tctx, chromedp.Evaluate(`navigator.webdriver`, &webdriver)); err != nil {
		t.Fatalf("evaluate navigator.webdriver: %v", err)
	}
	if webdriver {
		t.Fatal("visible Chrome exposed navigator.webdriver=true; nonzero loopback port did not clear the port=0 automation signal")
	}

	// Close must tear down the browser, reclaim the profile, and make the port
	// unreachable.
	if err := mgr.Close(); err != nil {
		t.Fatalf("manager close: %v", err)
	}
	conn, err := net.DialTimeout("tcp4", endpoint, 2*time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("debug port %s still reachable after Close", endpoint)
	}
	if proc.ProcessID() <= 0 || !proc.ProcessExited() {
		t.Fatalf("browser process evidence pid=%d exited=%v", proc.ProcessID(), proc.ProcessExited())
	}
}

// TestPersistentBrowserDetachAndReattach proves the shared-runtime contract on
// a real browser: Close tears down only the CDP client, the browser and page
// survive, while a second unbound Driver creates a fresh page instead of
// claiming the existing target.
func TestPersistentBrowserDetachAndReattach(t *testing.T) {
	if os.Getenv("WORKGROUND2_BROWSER_INTEGRATION") != "1" {
		t.Skip("set WORKGROUND2_BROWSER_INTEGRATION=1 and use -tags browser_integration")
	}
	info, err := cdp.Discover(browser.BrowserAuto, os.Getenv("WORKGROUND2_BROWSER_EXECUTABLE"))
	if err != nil {
		t.Skipf("no supported Chrome/Chromium browser installed: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Persistent Reattach</title><button>same page</button>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	factory := cdp.NewFactory(cdp.Options{})
	opts := browser.DriverOptions{
		BrowserKind: info.Kind, ExecutablePath: info.ExecutablePath,
		Headless: true, UserDataDir: t.TempDir(), OwnProcess: false,
		DenyDownloads: true,
	}
	first, err := factory.New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	killer, ok := first.(browser.Killer)
	if !ok {
		t.Fatal("persistent driver does not expose orphan reaper")
	}
	defer killer.Kill()
	process, ok := first.(processDriver)
	if !ok {
		t.Fatal("persistent driver does not expose integration process evidence")
	}
	if err := first.Navigate(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	before, err := first.Observe(ctx, browser.ObserveOptions{})
	if err != nil || before.Title != "Persistent Reattach" {
		t.Fatalf("first observation title=%q err=%v", before.Title, err)
	}
	endpoint := "http://" + process.DebugEndpoint()
	endpointInfo, err := browser.ValidateEndpoint(ctx, nil, endpoint)
	if err != nil {
		t.Fatal(err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if process.ProcessExited() {
		t.Fatal("detach reported the shared browser process as exited")
	}
	if _, err := browser.ValidateEndpoint(ctx, nil, endpoint); err != nil {
		t.Fatalf("browser endpoint died after detach: %v", err)
	}

	opts.Attach = true
	opts.DebugURL = endpoint
	opts.WebSocketURL = endpointInfo.WebSocketURL
	second, err := factory.New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	source, ok := second.(browser.EndpointSource)
	if !ok {
		t.Fatal("reattached driver does not expose its CDP endpoint")
	}
	if got := source.CDPEndpoint(); got != endpoint {
		t.Fatalf("reattached endpoint = %q, want %q", got, endpoint)
	}
	after, err := second.Observe(ctx, browser.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if after.ActiveTab == before.ActiveTab || after.URL != "about:blank" {
		t.Fatalf("reattach claimed existing page: before=%+v after=%+v", before, after)
	}
	preserved := false
	for _, tab := range after.Tabs {
		if tab.ID == before.ActiveTab && tab.URL == before.URL && tab.Title == before.Title {
			preserved = true
			break
		}
	}
	if !preserved {
		t.Fatalf("reattach lost existing page: before=%+v after=%+v", before, after)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := browser.ValidateEndpoint(ctx, nil, endpoint); err != nil {
		t.Fatalf("second detach closed shared browser: %v", err)
	}
	if err := killer.Kill(); err != nil {
		t.Fatalf("integration orphan cleanup: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := browser.ValidateEndpoint(ctx, nil, endpoint); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("explicit orphan cleanup left the browser endpoint reachable")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestChromeBeforeUnloadDialogHandling(t *testing.T) {
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
		fmt.Fprint(w, `<!doctype html><title>Guard</title>
<button id="arm" onclick="window.addEventListener('beforeunload', function (e) { e.preventDefault(); e.returnValue = 'stay'; }); this.textContent='armed'">Arm</button>
<p id="marker">guard-page</p>`)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Next</title><p>next-page</p>`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	profileRoot := t.TempDir()
	captured := &captureFactory{base: cdp.NewFactory(cdp.Options{})}
	mgr, err := browser.NewManager(ctx, browser.Options{
		Factory: captured, BrowserKind: info.Kind, ExecutablePath: info.ExecutablePath,
		Headless: true, ProfileRoot: profileRoot, ActionTimeout: 20 * time.Second,
		StateTimeout: 20 * time.Second, IdleTimeout: time.Minute, SettleWindow: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	const owner = "dialog-test"
	if _, err := mgr.Open(ctx, owner, browser.OpenRequest{URL: server.URL, RequestID: "open"}); err != nil {
		t.Fatal(err)
	}
	clickHTTPRelay(t, ctx, mgr, owner, server.URL, "relay-open")

	// Arm the beforeunload handler with real user activation.
	state, err := mgr.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	armIndex := findElement(t, state, func(element browser.Element) bool { return element.Name == "Arm" })
	if _, err := mgr.Click(ctx, owner, browser.ClickRequest{Revision: state.Revision, Index: armIndex, RequestID: "arm-click"}); err != nil {
		t.Fatal(err)
	}

	// Default: navigation must be blocked (stay) with a structured
	// dialog_blocked error, and the page must not move.
	_, err = mgr.Navigate(ctx, owner, browser.NavigateRequest{URL: server.URL + "/next", RequestID: "nav-stay"})
	var blockedErr *browser.Error
	if !errors.As(err, &blockedErr) || blockedErr.Code != browser.ErrDialogBlocked {
		t.Fatalf("navigate over beforeunload = %v, want dialog_blocked", err)
	}
	if blockedErr.Dialog == nil || blockedErr.Dialog.Type != browser.DialogBeforeUnload {
		t.Fatalf("dialog context = %+v", blockedErr.Dialog)
	}
	state, err = mgr.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Text, "guard-page") || strings.Contains(state.Text, "next-page") {
		t.Fatalf("page moved after blocked navigation: %q", state.Text)
	}

	// allow_leave=true: the dialog is accepted and navigation completes.
	// HTTP URLs land on the relay interstitial first, which the workflow then
	// clicks through (same as TestChromeWorkflow).
	if _, err := mgr.Navigate(ctx, owner, browser.NavigateRequest{URL: server.URL + "/next", RequestID: "nav-leave", AllowLeave: true}); err != nil {
		t.Fatalf("navigate with allow_leave: %v", err)
	}
	clickHTTPRelay(t, ctx, mgr, owner, server.URL+"/next", "relay-nav-leave")
	state, err = mgr.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Text, "next-page") {
		t.Fatalf("allow_leave navigation did not reach next page: %q", state.Text)
	}

	// Tab close over beforeunload: default stays (dialog_blocked, active tab
	// unchanged), allow_leave=true closes.
	newTab, err := mgr.Tab(ctx, owner, browser.TabRequest{
		Revision: state.Revision, Action: browser.TabNew, URL: server.URL, RequestID: "tab-new",
	})
	if err != nil {
		t.Fatal(err)
	}
	clickHTTPRelay(t, ctx, mgr, owner, server.URL, "relay-tab-new")
	state, err = mgr.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	armIndex = findElement(t, state, func(element browser.Element) bool { return element.Name == "Arm" })
	if _, err := mgr.Click(ctx, owner, browser.ClickRequest{Revision: state.Revision, Index: armIndex, RequestID: "arm-2"}); err != nil {
		t.Fatal(err)
	}
	var guardTab string
	for _, tab := range state.Tabs {
		if tab.Active {
			guardTab = tab.ID
		}
	}
	state, err = mgr.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	// Default close runs Page.close, dismisses beforeunload, and keeps the tab.
	_, err = mgr.Tab(ctx, owner, browser.TabRequest{
		Revision: state.Revision, Action: browser.TabClose, TabID: guardTab, RequestID: "tab-close-stay",
	})
	if !errors.As(err, &blockedErr) || blockedErr.Code != browser.ErrDialogBlocked {
		t.Fatalf("tab close over beforeunload = %v, want dialog_blocked", err)
	}
	state, err = mgr.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tabs) != 2 {
		t.Fatalf("guarded tab closed despite dismissed beforeunload: %+v", state.Tabs)
	}

	// Explicit leave accepts beforeunload and closes the guarded tab.
	if _, err := mgr.Tab(ctx, owner, browser.TabRequest{
		Revision: state.Revision, Action: browser.TabClose, TabID: guardTab, RequestID: "tab-close-leave", AllowLeave: true,
	}); err != nil {
		t.Fatalf("tab close with allow_leave: %v", err)
	}
	state, err = mgr.State(ctx, owner, browser.StateRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tabs) != 1 {
		t.Fatalf("guarded tab did not close: %+v", state.Tabs)
	}
	_ = newTab
}
