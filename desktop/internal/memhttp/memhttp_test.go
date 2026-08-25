package memhttp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestServerServesHTTPRequest proves a full HTTP request/response round trip
// through the memory transport, both via the server's own client and via the
// process default transport (the path production code uses).
func TestServerServesHTTPRequest(t *testing.T) {
	server := NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "method=%s path=%s body=%s", r.Method, r.URL.Path, body)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/echo", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("server client request: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "method=POST path=/echo body=payload" {
		t.Fatalf("response = %d %q", resp.StatusCode, got)
	}

	// Production-style client: plain client on the default transport.
	viaDefault, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("default transport request: %v", err)
	}
	got, _ = io.ReadAll(viaDefault.Body)
	viaDefault.Body.Close()
	if !strings.Contains(string(got), "method=GET") {
		t.Fatalf("default transport response = %q", got)
	}
}

// TestServerStreamsSSE proves a streaming response (text/event-stream) works
// through the memory transport, as used by the collaboration event stream.
func TestServerStreamsSSE(t *testing.T) {
	server := NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(w, "id: %d\nevent: room\ndata: value-%d\n\n", i, i)
			flusher.Flush()
		}
	}))
	defer server.Close()

	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	for i := 1; i <= 3; i++ {
		want := fmt.Sprintf("id: %d\nevent: room\ndata: value-%d", i, i)
		if !strings.Contains(string(body), want) {
			t.Fatalf("stream missing %q in:\n%s", want, body)
		}
	}
}

// TestListenerShutdown proves Close stops Accept and dialing, and that the
// HTTP server shuts down cleanly.
func TestListenerShutdown(t *testing.T) {
	server := NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	// Serving works before shutdown.
	if resp, err := server.Client().Get(server.URL); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("before shutdown: resp=%v err=%v", resp, err)
	}

	server.Close()

	// Dialing a closed listener fails.
	addr := strings.TrimPrefix(server.URL, "http://")
	if conn, err := Dial("tcp", addr); err == nil {
		conn.Close()
		t.Fatalf("dial after close succeeded at %s", addr)
	}

	// The listener reports ErrClosed from Accept after Close.
	l := server.Listener.(*memListener)
	select {
	case c := <-l.pending:
		c.Close()
		t.Fatal("pending connection remained after close")
	default:
	}
	if _, err := l.Accept(); err == nil {
		t.Fatal("Accept succeeded after Close")
	}
}

// TestApplicationListenerRouting mimics the production listener seams: a
// listener obtained from Listen is served by a plain http.Server and reached
// through the address reported by Addr using the default transport.
func TestApplicationListenerRouting(t *testing.T) {
	l, err := Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("application routed"))
	})}
	go func() {
		_ = srv.Serve(l)
	}()
	defer srv.Close()
	defer l.Close()

	addr := l.Addr().(*net.TCPAddr)
	if addr.Port == 0 {
		t.Fatal("synthetic TCP port was not allocated")
	}
	url := "http://" + l.Addr().String()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("routed request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "application routed" {
		t.Fatalf("routed body = %q", body)
	}

	// Raw dial probe (the pattern used by restore tests) also reaches it.
	conn, err := Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dial probe: %v", err)
	}
	conn.Close()
}

// TestListenAllocatesDistinctPorts proves each listener gets its own synthetic
// port and stays routable.
func TestListenAllocatesDistinctPorts(t *testing.T) {
	first, err := Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	if first.Addr().String() == second.Addr().String() {
		t.Fatalf("listeners share address %s", first.Addr())
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("second"))
	})}
	go func() { _ = server.Serve(second) }()
	defer server.Close()

	resp, err := http.Get("http://" + second.Addr().String())
	if err != nil {
		t.Fatalf("second listener request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "second" {
		t.Fatalf("second listener body = %q", body)
	}
}

// TestUnspecifiedHostRegistersLoopbackAlias proves a 0.0.0.0-style bind (used
// by the file-origin listener) is reachable at 127.0.0.1.
func TestUnspecifiedHostRegistersLoopbackAlias(t *testing.T) {
	l, err := Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	addr := l.Addr().String()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("unspecified bind address = %q, want loopback", addr)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("loopback alias"))
	})}
	go func() { _ = server.Serve(l) }()
	defer server.Close()

	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatalf("loopback alias request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "loopback alias" {
		t.Fatalf("body = %q", body)
	}
}

// TestDialUnregisteredFails proves a dial to an unknown address errors instead
// of silently succeeding.
func TestDialUnregisteredFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := memDialContext(ctx, "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("dial to unregistered address succeeded")
	}
}
