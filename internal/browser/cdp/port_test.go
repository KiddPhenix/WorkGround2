package cdp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// captureLaunchArgs builds an ExecAllocator from opts, forces launch to fail on
// a nonexistent executable, and captures the exact argv Chrome would have
// received. Fully hermetic: no browser process is ever started.
func captureLaunchArgs(t *testing.T, opts ...chromedp.ExecAllocatorOption) []string {
	t.Helper()
	var captured []string
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), append([]chromedp.ExecAllocatorOption{
		chromedp.ExecPath(filepath.Join(t.TempDir(), "missing-chrome")),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			captured = append([]string(nil), cmd.Args...)
		}),
	}, opts...)...)
	defer cancel()
	child, childCancel := chromedp.NewContext(ctx)
	defer childCancel()
	_ = chromedp.Run(child) // exec fails; args were captured before Start
	if len(captured) == 0 {
		t.Fatal("exec allocator captured no args")
	}
	return captured
}

func TestPickDebugPortIsNonzeroLoopback(t *testing.T) {
	for i := 0; i < 20; i++ {
		port, err := pickDebugPort()
		if err != nil {
			t.Fatalf("pickDebugPort #%d: %v", i, err)
		}
		if port <= 0 || port > 65535 {
			t.Fatalf("pickDebugPort #%d returned %d, want nonzero TCP port", i, port)
		}
		// The probed port must actually be free on the loopback interface:
		// re-binding 127.0.0.1:port must succeed.
		ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("probed port %d not reusable on loopback: %v", port, err)
		}
		_ = ln.Close()
	}
}

func TestPickDebugPortFailureIsExplicit(t *testing.T) {
	orig := debugProbeListen
	debugProbeListen = func() (net.Listener, error) {
		return nil, errors.New("probe listen denied")
	}
	t.Cleanup(func() { debugProbeListen = orig })
	port, err := pickDebugPort()
	if port != 0 {
		t.Fatalf("pickDebugPort returned port %d on failure, want 0", port)
	}
	if err == nil || !strings.Contains(err.Error(), "probe listen denied") {
		t.Fatalf("pickDebugPort failure error = %v, want explicit probe error", err)
	}
}

func TestDebugLaunchFlagsRejectNonPositivePort(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 70000} {
		flags, err := debugLaunchFlags(port)
		if err == nil {
			t.Fatalf("debugLaunchFlags(%d) succeeded with %d flags, want error", port, len(flags))
		}
		if !strings.Contains(err.Error(), "nonzero") {
			t.Fatalf("debugLaunchFlags(%d) error %q missing nonzero-port explanation", port, err)
		}
	}
}

func TestDebugLaunchFlagsSerializeLoopbackArgs(t *testing.T) {
	flags, err := debugLaunchFlags(3456)
	if err != nil {
		t.Fatal(err)
	}
	args := captureLaunchArgs(t, flags...)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--remote-debugging-address=127.0.0.1") {
		t.Fatalf("launch args missing loopback address: %v", args)
	}
	if !strings.Contains(joined, "--remote-debugging-port=3456") {
		t.Fatalf("launch args missing nonzero port: %v", args)
	}
	if strings.Contains(joined, "--remote-debugging-port=0") {
		t.Fatalf("launch args still use port=0: %v", args)
	}
	if strings.Contains(joined, "0.0.0.0") || strings.Contains(joined, "::") {
		t.Fatalf("launch args expose a wildcard address: %v", args)
	}
	if strings.Contains(joined, "--enable-automation") {
		t.Fatalf("launch args enable automation: %v", args)
	}
}

func TestPrepareDebugLaunchRecordsEndpoint(t *testing.T) {
	d := &driver{pickPort: func() (int, error) { return 4321, nil }}
	flags, err := d.prepareDebugLaunch()
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 2 {
		t.Fatalf("prepareDebugLaunch returned %d flags, want 2", len(flags))
	}
	if got := d.DebugEndpoint(); got != "127.0.0.1:4321" {
		t.Fatalf("debug endpoint = %q, want 127.0.0.1:4321", got)
	}
}

func TestPrepareDebugLaunchPickerFailure(t *testing.T) {
	d := &driver{pickPort: func() (int, error) { return 0, errors.New("picker down") }}
	flags, err := d.prepareDebugLaunch()
	if err == nil || !strings.Contains(err.Error(), "picker down") {
		t.Fatalf("prepareDebugLaunch error = %v, want picker failure surfaced", err)
	}
	if flags != nil {
		t.Fatalf("prepareDebugLaunch returned flags on failure: %v", flags)
	}
	if got := d.DebugEndpoint(); got != "" {
		t.Fatalf("debug endpoint set to %q on failure, want empty", got)
	}
}

func TestIsDebugPortConflict(t *testing.T) {
	for _, msg := range []string{
		"chrome failed to start:\n[123:456:ERROR:tcp_server_socket_posix.cc(1)] bind() returned an error, errno=98 (Address already in use)",
		"chrome failed to start:\n[123:456:ERROR:tcp_server_socket_posix.cc(1)] bind() returned an error, errno=10048 (Address already in use)",
		"browser startup failed: chrome failed to start:\nsome other line\nAddress already in use",
		"EADDRINUSE: listen tcp4 127.0.0.1:9222: bind: address already in use",
	} {
		if !isDebugPortConflict(errors.New(msg)) {
			t.Fatalf("isDebugPortConflict(%q) = false, want true", msg)
		}
	}
	for _, msg := range []string{
		"",
		"browser startup failed: chrome failed to start:\nexecutable file not found",
		"read browser version: websocket url timeout reached",
		"context deadline exceeded",
	} {
		if isDebugPortConflict(errors.New(msg)) {
			t.Fatalf("isDebugPortConflict(%q) = true, want false", msg)
		}
	}
	if isDebugPortConflict(nil) {
		t.Fatal("isDebugPortConflict(nil) = true, want false")
	}
}
