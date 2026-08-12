package cdp

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/chromedp/chromedp"
)

// debugProbeListen creates the TCP listener used to probe for a currently free
// port. It is a package-level var so tests can inject failure.
var debugProbeListen = func() (net.Listener, error) {
	return net.Listen("tcp4", "127.0.0.1:0")
}

// debugPortUnavailable marks failures that are safe to retry with a fresh
// launch attempt: either the loopback probe itself failed or Chrome lost the
// probe-to-bind race. Keeping this typed avoids treating unrelated startup
// errors as port conflicts based only on broad text matching.
type debugPortUnavailable struct{ cause error }

func (e *debugPortUnavailable) Error() string { return e.cause.Error() }
func (e *debugPortUnavailable) Unwrap() error { return e.cause }

// pickDebugPort selects a currently free nonzero TCP port on the IPv4 loopback
// interface by listening on 127.0.0.1:0, reading the assigned port, and closing
// the temporary listener. Any failure is returned explicitly; callers must not
// assume a port number on error. The chosen port is only a candidate: it can
// still be taken between this probe and Chrome's own bind (TOCTOU), which the
// Factory retry loop absorbs.
func pickDebugPort() (int, error) {
	ln, err := debugProbeListen()
	if err != nil {
		return 0, &debugPortUnavailable{cause: fmt.Errorf("pick debug port: %w", err)}
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("pick debug port: listener address is %T, want *net.TCPAddr", ln.Addr())
	}
	if addr.Port <= 0 || addr.Port > 65535 {
		return 0, fmt.Errorf("pick debug port: OS assigned invalid port %d", addr.Port)
	}
	return addr.Port, nil
}

// debugLaunchFlags returns the explicit loopback remote-debugging flags for a
// nonzero port. Passing 0 or an out-of-range port is a programming error and is
// rejected here so no call path can fall back to chromedp's implicit
// --remote-debugging-port=0 automation signal. The address is hard-coded to the
// IPv4 loopback: never 0.0.0.0, an IPv6 wildcard, or a public address.
func debugLaunchFlags(port int) ([]chromedp.ExecAllocatorOption, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid remote debugging port %d: must be a nonzero TCP port", port)
	}
	return []chromedp.ExecAllocatorOption{
		chromedp.Flag("remote-debugging-address", "127.0.0.1"),
		chromedp.Flag("remote-debugging-port", strconv.Itoa(port)),
	}, nil
}

// debugEndpointFor builds the 127.0.0.1:port endpoint from a validated nonzero
// port. Used only for lifecycle/package-internal evidence; it never enters
// BrowserInfo, ToolResult, or logs.
func debugEndpointFor(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// isDebugPortConflict reports whether err looks like Chrome failing to bind the
// requested remote debugging port (the port was taken between our probe and
// Chrome's bind). Matching is deliberately broad: chromedp surfaces Chrome's
// stderr inside a fmt-wrapped "chrome failed to start" error, and platforms
// word the bind failure differently.
func isDebugPortConflict(err error) bool {
	if err == nil {
		return false
	}
	var unavailable *debugPortUnavailable
	if errors.As(err, &unavailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"address already in use",
		"bind() returned an error",
		"errno=10048",
		"eaddrinuse",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
