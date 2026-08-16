package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubDriver is a minimal Driver used only to exercise the shared runtime
// coordinator. It reports a loopback CDP endpoint and a fixed version.
type stubDriver struct {
	endpoint string
	info     BrowserInfo
	killed   atomic.Bool
	closed   atomic.Bool
}

func (d *stubDriver) CDPEndpoint() string { return d.endpoint }
func (d *stubDriver) Info() BrowserInfo   { return d.info }
func (d *stubDriver) Kill() error {
	d.killed.Store(true)
	d.closed.Store(true)
	return nil
}
func (d *stubDriver) Close() error { d.closed.Store(true); return nil }

func (d *stubDriver) Navigate(context.Context, string) error { return nil }
func (d *stubDriver) Observe(context.Context, ObserveOptions) (Observation, error) {
	return Observation{}, nil
}
func (d *stubDriver) Click(context.Context, NodeRef) error            { return nil }
func (d *stubDriver) Type(context.Context, NodeRef, TypeInput) error  { return nil }
func (d *stubDriver) Upload(context.Context, NodeRef, []string) error { return nil }
func (d *stubDriver) Scroll(context.Context, ScrollInput) error       { return nil }
func (d *stubDriver) NewTab(context.Context, string) (string, error)  { return "", nil }
func (d *stubDriver) ActivateTab(context.Context, string) error       { return nil }
func (d *stubDriver) CloseTab(context.Context, string) error          { return nil }
func (d *stubDriver) Invalidations() <-chan Invalidation              { return nil }
func (d *stubDriver) NavigateWithOptions(context.Context, string, ActionOptions) error {
	return nil
}
func (d *stubDriver) ClickWithOptions(context.Context, NodeRef, ActionOptions) error {
	return nil
}
func (d *stubDriver) CloseTabWithOptions(context.Context, string, ActionOptions) error {
	return nil
}

// versionClient returns an http.Client whose transport always answers
// /json/version with the supplied websocket URL.
func versionClient(wsURL string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/json/version" {
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		}
		body, _ := json.Marshal(map[string]string{
			"Browser":              "Chrome/151.0",
			"webSocketDebuggerUrl": wsURL,
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       ioNopCloser(string(body)),
		}, nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type nopReadCloser struct{ *strings.Reader }

func (nopReadCloser) Close() error        { return nil }
func ioNopCloser(s string) *nopReadCloser { return &nopReadCloser{strings.NewReader(s)} }

func TestValidateEndpointRejectsNonLoopback(t *testing.T) {
	for _, endpoint := range []string{
		"http://8.8.8.8:9222",
		"http://0.0.0.0:9222",
		"http://example.com:9222",
		"https://127.0.0.1:9222",
		"http://127.0.0.1", // no port
		"http://127.0.0.1:9222/other",
		"http://127.0.0.1:9222?redirect=1",
		"http://user@127.0.0.1:9222",
	} {
		if _, err := validateEndpoint(context.Background(), versionClient("ws://127.0.0.1:9222/devtools/browser/x"), endpoint); err == nil {
			t.Fatalf("validateEndpoint(%q) = nil, want rejection", endpoint)
		}
	}
}

func TestValidateEndpointAcceptsLoopbackJSON(t *testing.T) {
	info, err := validateEndpoint(context.Background(),
		versionClient("ws://127.0.0.1:9222/devtools/browser/abc"),
		"http://127.0.0.1:9222")
	if err != nil {
		t.Fatal(err)
	}
	if info.Endpoint != "http://127.0.0.1:9222" || info.WebSocketURL != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.Product == "" {
		t.Fatal("product should be populated from /json/version")
	}
}

func TestValidateEndpointRejectsNonLoopbackWebSocket(t *testing.T) {
	for _, wsURL := range []string{
		"ws://8.8.8.8:9222/devtools/browser/x",
		"ws://127.0.0.1:9333/devtools/browser/x",
		"ws://127.0.0.1:9222/devtools/page/x",
	} {
		_, err := validateEndpoint(context.Background(), versionClient(wsURL), "http://127.0.0.1:9222")
		if err == nil {
			t.Fatalf("websocket URL %q must be rejected", wsURL)
		}
	}
}

func TestWriteRecordIsAtomicAnd0600(t *testing.T) {
	dir := t.TempDir()
	rt := newSharedRuntime(RuntimeOptions{Dir: dir, ProfileDir: filepath.Join(dir, "profile")})
	rec := endpointRecord{Endpoint: "http://127.0.0.1:9222", WebSocketURL: "ws://127.0.0.1:9222/devtools/browser/x", Product: "Chrome", Version: "151.0", Kind: "chrome", CreatedAt: time.Now().UTC()}
	if err := rt.writeRecord(rec); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(rt.recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %o, want 600", info.Mode().Perm())
	}
	got, ok := rt.loadRecord()
	if !ok || got.Endpoint != rec.Endpoint || got.WebSocketURL != rec.WebSocketURL {
		t.Fatalf("loadRecord = %+v ok=%v", got, ok)
	}
	// No temp files may remain after the atomic rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestResolveAttachesToValidRecord(t *testing.T) {
	dir := t.TempDir()
	rt := newSharedRuntime(RuntimeOptions{
		Dir: dir, ProfileDir: filepath.Join(dir, "profile"),
		Client: versionClient("ws://127.0.0.1:9222/devtools/browser/x"),
	})
	if err := rt.writeRecord(endpointRecord{Endpoint: "http://127.0.0.1:9222", WebSocketURL: "ws://127.0.0.1:9222/devtools/browser/x", Product: "Chrome", Version: "151.0", Kind: "chrome"}); err != nil {
		t.Fatal(err)
	}

	var launches atomic.Int32
	launch := func(ctx context.Context, profileDir string) (Driver, error) {
		launches.Add(1)
		return &stubDriver{endpoint: "http://127.0.0.1:9999", info: BrowserInfo{Kind: BrowserChrome, Product: "Chrome", Version: "151.0"}}, nil
	}
	res, err := rt.resolve(context.Background(), launch)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Attach || res.Endpoint != "http://127.0.0.1:9222" || res.Driver != nil {
		t.Fatalf("resolve did not attach to valid record: %+v", res)
	}
	if launches.Load() != 0 {
		t.Fatal("resolve launched a new browser despite a valid record")
	}
}

func TestResolveReplacesStaleRecord(t *testing.T) {
	dir := t.TempDir()
	rt := newSharedRuntime(RuntimeOptions{
		Dir: dir, ProfileDir: filepath.Join(dir, "profile"),
		Client: versionClient("ws://127.0.0.1:9222/devtools/browser/x"),
	})
	// A stale/malicious record: non-loopback endpoint must be rejected and
	// replaced by a fresh launch.
	if err := rt.writeRecord(endpointRecord{Endpoint: "http://8.8.8.8:9222", WebSocketURL: "ws://8.8.8.8:9222/devtools/browser/x", Product: "Chrome", Version: "151.0", Kind: "chrome"}); err != nil {
		t.Fatal(err)
	}

	var launches atomic.Int32
	launch := func(ctx context.Context, profileDir string) (Driver, error) {
		launches.Add(1)
		return &stubDriver{endpoint: "http://127.0.0.1:9222", info: BrowserInfo{Kind: BrowserChrome, Product: "Chrome", Version: "151.0"}}, nil
	}
	res, err := rt.resolve(context.Background(), launch)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attach || res.Driver == nil || res.Endpoint != "http://127.0.0.1:9222" {
		t.Fatalf("resolve did not replace stale record: %+v", res)
	}
	if launches.Load() != 1 {
		t.Fatalf("launches = %d, want 1", launches.Load())
	}
	rec, ok := rt.loadRecord()
	if !ok || rec.Endpoint != "http://127.0.0.1:9222" {
		t.Fatalf("record not replaced with launch endpoint: %+v ok=%v", rec, ok)
	}
}

func TestResolveReplacesMalformedRecord(t *testing.T) {
	dir := t.TempDir()
	rt := newSharedRuntime(RuntimeOptions{
		Dir: dir, ProfileDir: filepath.Join(dir, "profile"),
		Client: versionClient("ws://127.0.0.1:9222/devtools/browser/x"),
	})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.recordPath, []byte(`{"endpoint":`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := rt.resolve(context.Background(), func(context.Context, string) (Driver, error) {
		return &stubDriver{endpoint: "http://127.0.0.1:9222", info: BrowserInfo{Kind: BrowserChrome, Product: "Chrome", Version: "151.0"}}, nil
	})
	if err != nil || res.Attach || res.Driver == nil {
		t.Fatalf("resolve malformed record: result=%+v err=%v", res, err)
	}
	if rec, ok := rt.loadRecord(); !ok || rec.Endpoint != "http://127.0.0.1:9222" {
		t.Fatalf("malformed record was not replaced: %+v ok=%v", rec, ok)
	}
}

func TestResolveConcurrentLaunchConverges(t *testing.T) {
	dir := t.TempDir()
	rt := newSharedRuntime(RuntimeOptions{
		Dir: dir, ProfileDir: filepath.Join(dir, "profile"),
		Client: versionClient("ws://127.0.0.1:9222/devtools/browser/x"),
	})

	var launches atomic.Int32
	release := make(chan struct{})
	launch := func(ctx context.Context, profileDir string) (Driver, error) {
		launches.Add(1)
		<-release
		return &stubDriver{endpoint: "http://127.0.0.1:9222", info: BrowserInfo{Kind: BrowserChrome, Product: "Chrome", Version: "151.0"}}, nil
	}

	const n = 16
	results := make([]ResolveResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = rt.resolve(context.Background(), launch)
		}(i)
	}
	// Let the first caller enter launch, then release it; the rest must attach.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d error: %v", i, errs[i])
		}
	}
	if launches.Load() != 1 {
		t.Fatalf("launches = %d, want exactly 1", launches.Load())
	}
	attached := 0
	for i := 0; i < n; i++ {
		if results[i].Attach {
			attached++
		}
	}
	if attached != n-1 {
		t.Fatalf("attached = %d, want %d", attached, n-1)
	}
}

func TestResolveReapsOrphanOnValidationFailure(t *testing.T) {
	dir := t.TempDir()
	// The client rejects the launched endpoint (non-200), so the launched driver
	// must be killed rather than leaked.
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: ioNopCloser("")}, nil
	})}
	rt := newSharedRuntime(RuntimeOptions{Dir: dir, ProfileDir: filepath.Join(dir, "profile"), Client: client})

	d := &stubDriver{endpoint: "http://127.0.0.1:9222", info: BrowserInfo{Kind: BrowserChrome, Product: "Chrome", Version: "151.0"}}
	_, err := rt.resolve(context.Background(), func(ctx context.Context, profileDir string) (Driver, error) {
		return d, nil
	})
	if err == nil {
		t.Fatal("resolve should fail when the launched endpoint does not validate")
	}
	if !d.killed.Load() {
		t.Fatal("launched orphan was not killed")
	}
}

func TestAcquireLockReclaimsStaleHolder(t *testing.T) {
	dir := t.TempDir()
	rt := newSharedRuntime(RuntimeOptions{Dir: dir, ProfileDir: filepath.Join(dir, "profile"), StaleAfter: 20 * time.Millisecond})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed holder by writing a lock file with an old mtime.
	if err := os.WriteFile(rt.lockPath, []byte("99999"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(rt.lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := rt.acquireLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestAcquireLockRespectsContext(t *testing.T) {
	dir := t.TempDir()
	rt := newSharedRuntime(RuntimeOptions{Dir: dir, ProfileDir: filepath.Join(dir, "profile"), StaleAfter: time.Minute})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.lockPath, []byte("123"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := rt.acquireLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquireLock error = %v, want context deadline", err)
	}
}

func TestAcquireLockHeartbeatPreventsLiveHolderReap(t *testing.T) {
	dir := t.TempDir()
	rt := newSharedRuntime(RuntimeOptions{Dir: dir, ProfileDir: filepath.Join(dir, "profile"), StaleAfter: 300 * time.Millisecond, Poll: 5 * time.Millisecond})
	release, err := rt.acquireLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	initial, err := os.Stat(rt.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, statErr := os.Stat(rt.lockPath)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.ModTime().After(initial.ModTime()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock heartbeat did not refresh modification time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rt.lockStale() {
		t.Fatal("heartbeat allowed a live launch lock to become stale")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := rt.acquireLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire error = %v, want deadline", err)
	}
}

func TestEndpointValidationAgainstLiveServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"Browser":"Chrome/151.0","webSocketDebuggerUrl":"ws://%s/devtools/browser/x"}`, r.Host)
	}))
	defer srv.Close()

	// httptest.Server listens on a loopback address; validateEndpoint must
	// accept it only when it is an IP loopback (127.0.0.1/::1).
	endpoint := srv.URL
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Skipf("httptest host %q is not an IP loopback; skip", host)
	}
	info, err := validateEndpoint(context.Background(), http.DefaultClient, endpoint)
	if err != nil {
		t.Fatalf("validateEndpoint(%s): %v", endpoint, err)
	}
	if info.WebSocketURL != "ws://"+u.Host+"/devtools/browser/x" {
		t.Fatalf("unexpected ws url: %s", info.WebSocketURL)
	}
}
