package boot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent/testutil"
	browserpkg "workground2/internal/browser"
	"workground2/internal/config"
	"workground2/internal/event"
	"workground2/internal/netclient"
	"workground2/internal/sandbox"
	"workground2/internal/tool"
	"workground2/internal/tool/builtin"
)

type fakeBrowserCloser struct {
	mu            sync.Mutex
	closeCalls    int
	sessionOwners []string
	closeErr      error
	sessionErr    error
}

type transientBrowserCloser struct {
	mu       sync.Mutex
	calls    int
	failures int
	closeErr error
}

func (f *transientBrowserCloser) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failures > 0 {
		f.failures--
		return f.closeErr
	}
	return nil
}

func (f *fakeBrowserCloser) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return f.closeErr
}

func (f *fakeBrowserCloser) CloseSession(_ context.Context, owner string) (browserpkg.CloseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionOwners = append(f.sessionOwners, owner)
	return browserpkg.CloseResult{SessionID: owner, Closed: true}, f.sessionErr
}

func (f *fakeBrowserCloser) snapshot() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls, append([]string(nil), f.sessionOwners...)
}

func TestBrowserLifecycleBuildOwnership(t *testing.T) {
	t.Run("build failure closes exactly once", func(t *testing.T) {
		closer := &fakeBrowserCloser{}
		owner := newBrowserLifecycle(closer, event.Discard)
		owner.releaseIfOwned()
		owner.releaseIfOwned()
		calls, _ := closer.snapshot()
		if calls != 1 {
			t.Fatalf("Close calls = %d, want 1", calls)
		}
	})

	t.Run("successful transfer leaves cleanup to controller", func(t *testing.T) {
		closer := &fakeBrowserCloser{}
		owner := newBrowserLifecycle(closer, event.Discard)
		owner.transfer()
		owner.releaseIfOwned()
		if calls, _ := closer.snapshot(); calls != 0 {
			t.Fatalf("Build guard closed transferred manager %d times", calls)
		}
		owner.close("test cleanup")
		owner.close("test cleanup")
		if calls, _ := closer.snapshot(); calls != 1 {
			t.Fatalf("controller cleanup Close calls = %d, want 1", calls)
		}
	})
}

func TestBrowserLifecycleCloseErrorIsObservable(t *testing.T) {
	closer := &fakeBrowserCloser{closeErr: errors.New("close boom")}
	var mu sync.Mutex
	var notices []event.Event
	sink := event.FuncSink(func(e event.Event) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, e)
	})
	owner := newBrowserLifecycle(closer, sink)
	owner.releaseIfOwned()
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 || notices[0].Kind != event.Notice || notices[0].Level != event.LevelWarn {
		t.Fatalf("close notices = %+v", notices)
	}
}

func TestBrowserLifecycleRetriesWithinSingleControllerCleanup(t *testing.T) {
	closer := &transientBrowserCloser{failures: 1, closeErr: errors.New("transient close")}
	owner := newBrowserLifecycle(closer, event.Discard)
	owner.close("test cleanup")
	owner.close("test cleanup")
	closer.mu.Lock()
	defer closer.mu.Unlock()
	if closer.calls != 2 {
		t.Fatalf("Close calls = %d, want one failed attempt plus one retry", closer.calls)
	}
}

func TestBrowserTaskCleanupIsOwnerScopedAndConcurrentIdempotent(t *testing.T) {
	closer := &fakeBrowserCloser{}
	cleanup := browserTaskCleanup(closer, "work-task-owner", event.Discard)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cleanup()
		}()
	}
	wg.Wait()
	closeCalls, owners := closer.snapshot()
	if closeCalls != 0 {
		t.Fatalf("task cleanup closed global manager %d times", closeCalls)
	}
	if len(owners) != 1 || owners[0] != "work-task-owner" {
		t.Fatalf("CloseSession owners = %v, want exactly [work-task-owner]", owners)
	}
}

func TestBrowserManagerOptionsCarriesIncognito(t *testing.T) {
	// Default config: incognito must resolve to false on the Manager options.
	opts := browserManagerOptions(config.Default(), nil)
	if opts.Incognito {
		t.Fatal("default browserManagerOptions.Incognito = true, want false")
	}
	if opts.Headless || opts.AllowPasswordInput == false || opts.AllowFileUpload == false {
		t.Fatalf("default browserManagerOptions regressed unrelated fields: %+v", opts)
	}

	// Explicit true flows from config into the runtime options.
	cfg := config.Default()
	incognito := true
	headless := true
	cfg.Tools.Browser.Incognito = &incognito
	cfg.Tools.Browser.Headless = &headless
	opts = browserManagerOptions(cfg, nil)
	if !opts.Incognito {
		t.Fatal("browserManagerOptions.Incognito = false, want true")
	}
	if !opts.Headless {
		t.Fatal("browserManagerOptions.Headless = false, want true")
	}
	if opts.Factory != nil {
		t.Fatal("browserManagerOptions should not invent a factory")
	}
}

func TestBrowserToolSelectionMatrix(t *testing.T) {
	tests := []struct {
		name       string
		enabled    []string
		economy    bool
		browserOn  bool
		wantCreate bool
		wantState  bool
		wantClick  bool
	}{
		{name: "empty means all", browserOn: true, wantCreate: true, wantState: true, wantClick: true},
		{name: "explicit subset", enabled: []string{"read_file", "browser_state"}, browserOn: true, wantCreate: true, wantState: true},
		{name: "browser disabled", browserOn: false},
		{name: "unrelated filter", enabled: []string{"read_file"}, browserOn: true},
		{name: "economy hides browser", economy: true, browserOn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective := tt.enabled
			if tt.economy {
				effective = tokenEconomyBuiltins(effective)
			}
			create := tt.browserOn && !tt.economy && browserToolsSelected(effective)
			if create != tt.wantCreate {
				t.Fatalf("manager create = %v, want %v (effective=%v)", create, tt.wantCreate, effective)
			}
			if create {
				if got := builtinToolEnabled(effective, "browser_state"); got != tt.wantState {
					t.Fatalf("browser_state enabled = %v, want %v", got, tt.wantState)
				}
				if got := builtinToolEnabled(effective, "browser_click"); got != tt.wantClick {
					t.Fatalf("browser_click enabled = %v, want %v", got, tt.wantClick)
				}
			}
		})
	}
}

func TestAddBuiltinsDoesNotWarnForRuntimeBrowserNames(t *testing.T) {
	var stderr bytes.Buffer
	addBuiltins(tool.NewRegistry(), []string{"browser_state"}, nil, sandbox.Spec{}, time.Second, builtin.SearchSpec{}, &stderr, "", netclient.ProxySpec{}, nil, builtin.NewPathResolver(), nil, nil, true)
	if stderr.Len() != 0 {
		t.Fatalf("runtime browser tool was reported unknown: %s", stderr.String())
	}
}

func TestBuildBrowserVisibilityMatrix(t *testing.T) {
	tests := []struct {
		name      string
		tools     string
		browser   string
		tokenMode string
		want      []string
	}{
		{
			name:  "empty registers all",
			tools: "enabled = []",
			want:  sortedBrowserRuntimeTools(),
		},
		{
			name:    "browser disabled",
			tools:   "enabled = []",
			browser: "enabled = false",
		},
		{
			name:  "explicit subset",
			tools: `enabled = ["read_file", "browser_state"]`,
			want:  []string{"browser_state"},
		},
		{
			name:  "unrelated filter",
			tools: `enabled = ["read_file"]`,
		},
		{
			name:      "economy hides browser",
			tools:     "enabled = []",
			tokenMode: TokenModeEconomy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfigHome(t)
			dir := robustTempDir(t)
			registerBootTokenProfileTestProvider()
			setBootTokenProfileTestProvider(t, testutil.NewMock("browser-wiring"))
			configText := fmt.Sprintf(`default_model = "test-model"

[agent]
system_prompt = "BASE"

[tools]
%s

[tools.browser]
%s

[[providers]]
name = "test-model"
kind = "boot-token-profile-test"
model = "x"
`, tt.tools, tt.browser)
			if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(configText), 0o600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			ctrl, err := Build(context.Background(), Options{WorkspaceRoot: dir, TokenMode: tt.tokenMode, Sink: event.Discard, Stderr: &stderr})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer ctrl.Close()
			var got []string
			for _, entry := range ctrl.ToolContractEntries() {
				if browserRuntimeTools[entry.Name] {
					got = append(got, entry.Name)
				}
			}
			sort.Strings(got)
			if !equalStrings(got, tt.want) {
				t.Fatalf("browser tools = %v, want %v", got, tt.want)
			}
			if bytes.Contains(stderr.Bytes(), []byte("unknown built-in tool \"browser_")) {
				t.Fatalf("runtime browser name produced unknown warning: %s", stderr.String())
			}
		})
	}
}

func sortedBrowserRuntimeTools() []string {
	names := make([]string, 0, len(browserRuntimeTools))
	for name := range browserRuntimeTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
