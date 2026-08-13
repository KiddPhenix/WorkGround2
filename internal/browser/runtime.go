package browser

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"workground2/internal/fileutil"
)

const (
	// runtimeRecordName is the file under RuntimeDir that persists the shared
	// browser's loopback CDP endpoint. Written atomically with mode 0600; its
	// content never enters logs or model-visible JSON.
	runtimeRecordName = "endpoint.json"
	// runtimeLockName serializes cross-process launch/attach of the shared
	// browser so concurrent controllers cannot double-launch it.
	runtimeLockName = "launch.lock"
)

// EndpointInfo is the validated metadata of a shared browser's CDP endpoint.
type EndpointInfo struct {
	Endpoint     string // http://127.0.0.1:<port>
	WebSocketURL string // browser-level ws URL from /json/version
	Product      string // Browser product string from /json/version
}

// EndpointSource is implemented by Drivers that know their loopback CDP HTTP
// endpoint. It is used to persist the shared runtime record and to answer
// browser_attach; it never appears in model-visible JSON.
type EndpointSource interface {
	CDPEndpoint() string
}

// Killer is implemented by Drivers that can forcibly reap their own browser
// process. Used only to reclaim a just-launched orphan when the launch itself
// fails endpoint validation; normal cleanup never calls it.
type Killer interface {
	Kill() error
}

// endpointRecord is the on-disk form of the shared endpoint metadata.
type endpointRecord struct {
	Endpoint     string    `json:"endpoint"`
	WebSocketURL string    `json:"websocket_url"`
	Product      string    `json:"product"`
	Version      string    `json:"version"`
	Kind         string    `json:"kind"`
	CreatedAt    time.Time `json:"created_at"`
}

// RuntimeOptions configures the shared browser runtime.
type RuntimeOptions struct {
	Dir        string        // per-user runtime dir holding record + lock
	ProfileDir string        // persistent automation profile dir
	Client     *http.Client  // endpoint-validation HTTP client; nil uses a bounded default
	StaleAfter time.Duration // lock stale threshold; 0 uses 2m
	Poll       time.Duration // lock poll interval; 0 uses 50ms
}

// sharedRuntime coordinates cross-process attach/launch of one persistent
// browser per user. It owns the endpoint record and launch lock; the Driver
// lifetime stays with the Manager.
type sharedRuntime struct {
	dir        string
	profileDir string
	recordPath string
	lockPath   string
	client     *http.Client
	staleAfter time.Duration
	poll       time.Duration

	mu sync.Mutex // in-process launch/attach serialization
}

func newSharedRuntime(opts RuntimeOptions) *sharedRuntime {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	staleAfter := opts.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	poll := opts.Poll
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	return &sharedRuntime{
		dir:        opts.Dir,
		profileDir: opts.ProfileDir,
		recordPath: filepath.Join(opts.Dir, runtimeRecordName),
		lockPath:   filepath.Join(opts.Dir, runtimeLockName),
		client:     client,
		staleAfter: staleAfter,
		poll:       poll,
	}
}

// launchFunc starts a fresh shared browser using profileDir and returns its
// Driver. The Driver must implement EndpointSource.
type launchFunc func(ctx context.Context, profileDir string) (Driver, error)

// ResolveResult is the outcome of a resolve call.
type ResolveResult struct {
	Attach       bool   // true: attach to an existing record; false: Driver is freshly launched
	Endpoint     string // http://127.0.0.1:<port>
	WebSocketURL string // browser-level ws URL
	Driver       Driver // non-nil only when Attach is false
}

// resolve is the single cross-process entry point for "make a shared browser
// available". It acquires the launch lock, re-reads and strictly validates the
// record, and either returns an attach target or launches a fresh browser via
// launch and persists a new record. Concurrent callers converge: at most one
// launch occurs, and a valid record is never overwritten by a racing launch.
func (r *sharedRuntime) resolve(ctx context.Context, launch launchFunc) (ResolveResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	release, err := r.acquireLock(ctx)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("acquire browser launch lock: %w", err)
	}
	defer release()

	// Re-read under the lock: a concurrent process may have launched already.
	if rec, ok := r.loadRecord(); ok {
		if info, err := validateEndpoint(ctx, r.client, rec.Endpoint); err == nil {
			return ResolveResult{Attach: true, Endpoint: rec.Endpoint, WebSocketURL: info.WebSocketURL}, nil
		}
		// Stale or non-loopback/malicious record: clear it before relaunching.
		if err := r.clearRecord(); err != nil {
			return ResolveResult{}, err
		}
	} else if err := r.clearRecord(); err != nil {
		// A malformed/truncated record must not block atomic replacement on
		// Windows, where renaming over an existing file needs ReplaceFile.
		return ResolveResult{}, err
	}

	driver, err := launch(ctx, r.profileDir)
	if err != nil {
		return ResolveResult{}, err
	}
	src, ok := driver.(EndpointSource)
	if !ok || src.CDPEndpoint() == "" {
		reapDriver(driver)
		return ResolveResult{}, fmt.Errorf("launched browser does not expose a loopback CDP endpoint")
	}
	info, err := validateEndpoint(ctx, r.client, src.CDPEndpoint())
	if err != nil {
		reapDriver(driver)
		return ResolveResult{}, fmt.Errorf("launched browser endpoint validation failed: %w", err)
	}
	record := endpointRecord{
		Endpoint:     info.Endpoint,
		WebSocketURL: info.WebSocketURL,
		Product:      info.Product,
		Version:      driver.Info().Version,
		Kind:         string(driver.Info().Kind),
		CreatedAt:    time.Now().UTC(),
	}
	if err := r.writeRecord(record); err != nil {
		reapDriver(driver)
		return ResolveResult{}, err
	}
	return ResolveResult{Attach: false, Endpoint: info.Endpoint, WebSocketURL: info.WebSocketURL, Driver: driver}, nil
}

// clearIfInvalid rechecks a record after an attach race. It removes the record
// only when it still names the same endpoint and that endpoint is now invalid;
// a newer record or a healthy endpoint is never disturbed.
func (r *sharedRuntime) clearIfInvalid(ctx context.Context, endpoint string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	release, err := r.acquireLock(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	rec, ok := r.loadRecord()
	if !ok || rec.Endpoint != endpoint {
		return false, nil
	}
	if _, err := validateEndpoint(ctx, r.client, endpoint); err == nil {
		return false, nil
	}
	if err := r.clearRecord(); err != nil {
		return false, err
	}
	return true, nil
}

// reapDriver forcibly reclaims a just-launched browser whose record could not
// be trusted. Killers kill the process; anything else is closed.
func reapDriver(d Driver) {
	if k, ok := d.(Killer); ok {
		_ = k.Kill()
		return
	}
	_ = d.Close()
}

func (r *sharedRuntime) loadRecord() (endpointRecord, bool) {
	b, err := os.ReadFile(r.recordPath)
	if err != nil {
		return endpointRecord{}, false
	}
	var rec endpointRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return endpointRecord{}, false
	}
	if rec.Endpoint == "" {
		return endpointRecord{}, false
	}
	return rec, true
}

// writeRecord writes the record atomically and crash-safely with mode 0600.
// The endpoint metadata never enters logs.
func (r *sharedRuntime) writeRecord(rec endpointRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(r.recordPath, data, 0o600)
}

func (r *sharedRuntime) clearRecord() error {
	if err := os.Remove(r.recordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// acquireLock takes an exclusive cross-process lock via O_CREATE|O_EXCL and
// returns a release func. A crashed holder's lock is reclaimed once it exceeds
// staleAfter.
func (r *sharedRuntime) acquireLock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		return nil, err
	}
	for {
		f, err := os.OpenFile(r.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			token := lockToken()
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + ":" + token)
			_ = f.Close()
			stop := make(chan struct{})
			done := make(chan struct{})
			go r.keepLockAlive(token, stop, done)
			var once sync.Once
			return func() {
				once.Do(func() {
					close(stop)
					<-done
					if r.ownsLock(token) {
						_ = os.Remove(r.lockPath)
					}
				})
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if r.lockStale() {
			_ = os.Remove(r.lockPath)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.poll):
		}
	}
}

func lockToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b[:])
}

func (r *sharedRuntime) keepLockAlive(token string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	interval := r.staleAfter / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			if !r.ownsLock(token) {
				return
			}
			_ = os.Chtimes(r.lockPath, now, now)
		}
	}
}

func (r *sharedRuntime) ownsLock(token string) bool {
	b, err := os.ReadFile(r.lockPath)
	return err == nil && strings.HasSuffix(string(b), ":"+token)
}

func (r *sharedRuntime) lockStale() bool {
	info, err := os.Stat(r.lockPath)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > r.staleAfter
}

// validateEndpoint strictly checks that endpoint is a loopback HTTP CDP
// endpoint whose /json/version is reachable and returns a browser websocket
// URL. Non-loopback, non-HTTP, portless, and unreachable endpoints are all
// rejected so a stale or malicious record can never drive an attach.
func validateEndpoint(ctx context.Context, client *http.Client, endpoint string) (EndpointInfo, error) {
	return ValidateEndpoint(ctx, client, endpoint)
}

// ValidateEndpoint validates a persisted or freshly launched loopback CDP
// endpoint. It is exported for CDP launchers that must wait for readiness
// before attaching through the same browser-level websocket.
func ValidateEndpoint(ctx context.Context, client *http.Client, endpoint string) (EndpointInfo, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return EndpointInfo{}, fmt.Errorf("endpoint %q is not an http URL", endpoint)
	}
	host := u.Hostname()
	if host == "" {
		return EndpointInfo{}, fmt.Errorf("endpoint %q has no host", endpoint)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return EndpointInfo{}, fmt.Errorf("endpoint %q is not a loopback address", endpoint)
	}
	port := u.Port()
	if port == "" {
		return EndpointInfo{}, fmt.Errorf("endpoint %q has no port", endpoint)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p > 65535 {
		return EndpointInfo{}, fmt.Errorf("endpoint %q has an invalid port", endpoint)
	}

	versionURL := strings.TrimRight(endpoint, "/") + "/json/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, versionURL, nil)
	if err != nil {
		return EndpointInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return EndpointInfo{}, fmt.Errorf("GET %s: %w", versionURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EndpointInfo{}, fmt.Errorf("GET %s status %d", versionURL, resp.StatusCode)
	}
	var v struct {
		Browser              string `json:"Browser"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return EndpointInfo{}, fmt.Errorf("decode %s: %w", versionURL, err)
	}
	if strings.TrimSpace(v.Browser) == "" {
		return EndpointInfo{}, fmt.Errorf("%s returned an empty Browser", versionURL)
	}
	if !wsURLMatchesEndpoint(v.WebSocketDebuggerURL, u) {
		return EndpointInfo{}, fmt.Errorf("%s returned an invalid or mismatched websocket URL", versionURL)
	}
	return EndpointInfo{
		Endpoint:     endpoint,
		WebSocketURL: v.WebSocketDebuggerURL,
		Product:      v.Browser,
	}, nil
}

func wsURLMatchesEndpoint(raw string, endpoint *url.URL) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/devtools/browser/") {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback() && u.Hostname() == endpoint.Hostname() && u.Port() == endpoint.Port()
}
