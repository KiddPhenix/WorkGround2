package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// relayTokenLen is the size of a relay page token in bytes. The token is
// rendered as 2*relayTokenLen hex characters in the relay URL.
const (
	relayTokenLen  = 16
	relayPageLimit = 256
)

type relayTarget struct {
	url   string
	order uint64
}

// httpRelay is an in-process loopback HTTP relay. Plain http:// navigation
// first opens a relay page here; the page shows the target URL as an explicit
// click-through link so Chromium navigates from a user gesture instead of
// auto-upgrading address-bar-style HTTP navigation to HTTPS.
//
// The relay binds 127.0.0.1 on a random port and starts lazily on first use.
// Every target URL lives behind an unpredictable token kept in memory only —
// it never appears in the relay URL, access logs or the address bar.
type httpRelay struct {
	mu   sync.Mutex
	srv  *http.Server
	ln   net.Listener
	base string // http://127.0.0.1:<port>
	urls map[string]relayTarget
	seq  uint64
}

func newHTTPRelay() *httpRelay {
	return &httpRelay{urls: make(map[string]relayTarget)}
}

// start lazily starts the listener. It is idempotent: an already-running
// relay returns nil. On failure the relay is left in its initial state so the
// next start attempt can retry cleanly.
func (r *httpRelay) start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("http relay listen failed: %w", err)
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(r.serveHTTP),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	r.srv = srv
	r.ln = ln
	r.base = "http://" + ln.Addr().String()
	go r.serve(srv, ln)
	return nil
}

// serve runs the server until Shutdown; an unexpected failure clears the
// running state so a later start() can bring the relay back up.
func (r *httpRelay) serve(srv *http.Server, ln net.Listener) {
	err := srv.Serve(ln)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	r.mu.Lock()
	if r.srv == srv {
		r.srv = nil
		r.ln = nil
		r.base = ""
		r.urls = make(map[string]relayTarget)
		r.seq = 0
	}
	r.mu.Unlock()
	slog.Error("http relay server failed", "err", err)
}

// register stores the target URL under a fresh unpredictable token and
// returns the relay URL for it. The target never appears in the returned URL.
func (r *httpRelay) register(target string) (string, error) {
	var b [relayTokenLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("relay token generation failed: %w", err)
	}
	token := hex.EncodeToString(b[:])
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.srv == nil {
		return "", errors.New("http relay is not running")
	}
	if len(r.urls) >= relayPageLimit {
		var oldestToken string
		var oldest uint64
		for candidate, page := range r.urls {
			if oldestToken == "" || page.order < oldest {
				oldestToken = candidate
				oldest = page.order
			}
		}
		delete(r.urls, oldestToken)
	}
	r.seq++
	r.urls[token] = relayTarget{url: target, order: r.seq}
	return r.base + "/" + token, nil
}

// Close shuts the server down and drops every pending target. It is
// idempotent and safe to call when the relay was never started.
func (r *httpRelay) Close() error {
	r.mu.Lock()
	srv := r.srv
	r.srv = nil
	r.ln = nil
	r.base = ""
	r.urls = make(map[string]relayTarget)
	r.seq = 0
	r.mu.Unlock()
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func (r *httpRelay) serveHTTP(w http.ResponseWriter, req *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")

	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(req.URL.Path, "/")
	if !validRelayToken(token) {
		http.NotFound(w, req)
		return
	}
	r.mu.Lock()
	page, ok := r.urls[token]
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, relayPageHTML(page.url))
}

// validRelayToken reports whether s has the exact shape of a generated token.
func validRelayToken(s string) bool {
	if len(s) != relayTokenLen*2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// relayPageHTML renders the click-through confirmation page. The target URL
// is HTML-escaped everywhere it appears; the page has no script and no
// automatic navigation, only an explicit link the user must click.
func relayPageHTML(target string) string {
	esc := html.EscapeString(target)
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>确认访问 HTTP 地址</title>
<style>
body { font-family: system-ui, sans-serif; max-width: 42em; margin: 3em auto; padding: 0 1em; line-height: 1.6; }
.warn { color: #9a3412; font-weight: 600; }
.tip { color: #525252; font-size: .95em; }
a { word-break: break-all; }
</style>
</head>
<body>
<h1>确认访问不安全的 HTTP 地址</h1>
<p>目标地址使用<span class="warn">未加密的 HTTP 协议</span>。访问该地址时，传输内容可能被网络上的第三方读取或篡改。</p>
<p>请确认你信任该服务器并了解风险后，再点击下面的链接继续：</p>
<p><a href="%s" rel="noreferrer noopener">%s</a></p>
<p class="tip">第一次打开 Chrome 时，需要用户手动确认访问 HTTP（而非 HTTPS）。</p>
</body>
</html>`, esc, esc)
}
