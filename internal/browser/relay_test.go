package browser

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startTestRelay starts a relay and registers a target, returning the relay
// and its URL.
func startTestRelay(t *testing.T, target string) (*httpRelay, string) {
	t.Helper()
	r := newHTTPRelay()
	if err := r.start(); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	u, err := r.register(target)
	if err != nil {
		t.Fatalf("relay register: %v", err)
	}
	return r, u
}

func TestRelayStartIsIdempotentAndLoopbackOnly(t *testing.T) {
	r := newHTTPRelay()
	if err := r.start(); err != nil {
		t.Fatalf("first start: %v", err)
	}
	defer r.Close()
	if err := r.start(); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if !strings.HasPrefix(r.base, "http://127.0.0.1:") {
		t.Fatalf("relay base must be loopback, got %q", r.base)
	}
}

func TestRelayCloseIsIdempotent(t *testing.T) {
	r := newHTTPRelay()
	if err := r.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if r.base != "" {
		t.Fatalf("expected empty base after close, got %q", r.base)
	}
}

func TestRelayRegisterUsesUnpredictableTokens(t *testing.T) {
	r, u1 := startTestRelay(t, "http://example.com")
	u2, err := r.register("http://example.com")
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	tok1 := strings.TrimPrefix(u1, r.base+"/")
	tok2 := strings.TrimPrefix(u2, r.base+"/")
	if tok1 == tok2 {
		t.Fatalf("two registrations must get distinct tokens, both %q", tok1)
	}
	for _, tok := range []string{tok1, tok2} {
		if !validRelayToken(tok) {
			t.Fatalf("expected valid hex token, got %q", tok)
		}
		if strings.Contains(r.base+"/"+tok, "example.com") {
			t.Fatalf("target URL leaked into relay URL: %s", r.base+"/"+tok)
		}
	}
}

func TestRelayGetServesEscapedClickThrough(t *testing.T) {
	// Query characters must survive escaping inside href and text.
	target := `http://example.test/a?q=1&r="<x>` + "&s=中文"
	_, u := startTestRelay(t, target)

	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET relay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("expected html content type, got %q", ct)
	}
	for _, header := range []string{"Cache-Control", "Referrer-Policy", "X-Content-Type-Options", "Content-Security-Policy"} {
		if resp.Header.Get(header) == "" {
			t.Fatalf("missing security header %s", header)
		}
	}
	if v := resp.Header.Get("Cache-Control"); !strings.Contains(v, "no-store") {
		t.Fatalf("Cache-Control must disable store, got %q", v)
	}
	if v := resp.Header.Get("Referrer-Policy"); !strings.Contains(v, "no-referrer") {
		t.Fatalf("Referrer-Policy must be no-referrer, got %q", v)
	}
	if v := resp.Header.Get("X-Content-Type-Options"); !strings.Contains(v, "nosniff") {
		t.Fatalf("X-Content-Type-Options must be nosniff, got %q", v)
	}
	if v := resp.Header.Get("Content-Security-Policy"); !strings.Contains(v, "default-src 'none'") {
		t.Fatalf("CSP must be tightened, got %q", v)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)
	for _, needle := range []string{
		`href="http://example.test/a?q=1&amp;r=&#34;&lt;x&gt;&amp;s=中文" rel="noreferrer noopener"`,
		`未加密的 HTTP 协议`,
		`第一次打开 Chrome 时，需要用户手动确认访问 HTTP（而非 HTTPS）。`,
		"http://example.test/a?q=1&amp;r=&#34;&lt;x&gt;&amp;s=中文",
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("page missing %q\npage:\n%s", needle, page)
		}
	}
	if strings.Contains(page, "<script") {
		t.Fatalf("page must not contain script: %s", page)
	}
}

func TestRelayHeadSucceedsWithoutBody(t *testing.T) {
	_, u := startTestRelay(t, "http://example.com")
	req, err := http.NewRequest(http.MethodHead, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD relay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if n, _ := io.ReadAll(resp.Body); len(n) != 0 {
		t.Fatalf("HEAD must have empty body, got %d bytes", len(n))
	}
}

func TestRelayUnknownTokenIs404(t *testing.T) {
	r, _ := startTestRelay(t, "http://example.com")
	for _, path := range []string{"/", "/deadbeef", "/0000000000000000000000000000000", "/00000000000000000000000000000000/extra"} {
		resp, err := http.Get(r.base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: expected 404, got %d", path, resp.StatusCode)
		}
	}
}

func TestRelayRejectsNonGetHead(t *testing.T) {
	_, u := startTestRelay(t, "http://example.com")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(method, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s relay: %v", method, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s: expected 405, got %d", method, resp.StatusCode)
		}
	}
}

func TestRelayRegisterAfterCloseFails(t *testing.T) {
	r := newHTTPRelay()
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.register("http://example.com"); err == nil {
		t.Fatal("expected register to fail after close")
	}
}

func TestRelayPagesAreBounded(t *testing.T) {
	r, first := startTestRelay(t, "http://example.com/0")
	for i := 1; i <= relayPageLimit; i++ {
		if _, err := r.register("http://example.com/next"); err != nil {
			t.Fatalf("register page %d: %v", i, err)
		}
	}
	resp, err := http.Get(first)
	if err != nil {
		t.Fatalf("GET evicted page: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("oldest page must be evicted at limit, got %d", resp.StatusCode)
	}
}

func TestRelayStartsAgainAfterFailure(t *testing.T) {
	// Simulate an unexpected server failure by closing the listener out from
	// under it; the serve goroutine clears state so start() can retry.
	r := newHTTPRelay()
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	firstBase := r.base
	r.mu.Lock()
	ln := r.ln
	r.mu.Unlock()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	base := func() string {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.base
	}
	for base() != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if base() != "" {
		t.Fatal("relay did not clear state after shutdown")
	}
	if err := r.start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer r.Close()
	if got := base(); got == "" || got == firstBase {
		t.Fatalf("expected fresh base after restart, got %q", got)
	}
}
