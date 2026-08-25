// Package memhttp provides an in-memory net.Listener and HTTP server for the
// Desktop test suite. Connections are net.Pipe pairs, so serving and dialing
// never touch the OS network stack — no socket is bound, and Windows Defender
// Firewall never sees the test binary as a listening application.
//
// Production code reaches in-memory listeners without modification: listeners
// register their synthetic "host:port" address, and the process-wide dial hooks
// (installed once in init) route matching addresses to the pipe before falling
// back to the real dialer. That preserves byte-for-byte behavior for any
// address that is not registered.
package memhttp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// dialTimeout bounds a rendezvous with the server's Accept loop so a listener
// that is never served fails loudly instead of hanging the test.
const dialTimeout = 30 * time.Second

var (
	registryMu sync.RWMutex
	registry   = map[string]*memListener{}

	installOnce sync.Once
)

// memListener is a net.Listener whose accepted connections are net.Pipe pairs.
// It never binds an OS socket. Addr reports a synthetic *net.TCPAddr so
// production code that asserts on *net.TCPAddr (for the port) keeps working.
type memListener struct {
	addr    net.Addr
	mu      sync.Mutex
	closed  bool
	pending chan net.Conn // server side of pipes waiting for Accept
	done    chan struct{}

	clientPort int // synthetic ephemeral port handed to the next peer
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.pending:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	close(l.done)
	l.mu.Unlock()
	unregister(l.addr.String())
	return nil
}

func (l *memListener) Addr() net.Addr { return l.addr }

// addrConn overlays synthetic TCP addresses on a net.Pipe pair so production
// code that parses r.RemoteAddr / r.LocalAddr (e.g. file-origin registration
// and the relay server) sees a realistic loopback peer instead of "pipe".
type addrConn struct {
	net.Conn
	local  net.Addr
	remote net.Addr
}

func (c *addrConn) LocalAddr() net.Addr  { return c.local }
func (c *addrConn) RemoteAddr() net.Addr { return c.remote }

// dial returns the client end of a fresh pipe; the server end is queued for the
// next Accept. The small buffer lets a dial complete before Serve's Accept loop
// starts; once the buffer is full, dial rendezvouses with Accept.
func (l *memListener) dial() (net.Conn, error) {
	server, client := net.Pipe()
	clientAddr := l.nextClientAddr()
	serverConn := &addrConn{Conn: server, local: l.addr, remote: clientAddr}
	clientConn := &addrConn{Conn: client, local: clientAddr, remote: l.addr}
	timer := time.NewTimer(dialTimeout)
	defer timer.Stop()
	select {
	case l.pending <- serverConn:
		return clientConn, nil
	case <-timer.C:
		serverConn.Close()
		clientConn.Close()
		return nil, fmt.Errorf("memhttp: listener at %s never accepted the connection", l.addr)
	case <-l.done:
		serverConn.Close()
		clientConn.Close()
		return nil, net.ErrClosed
	}
}

// nextClientAddr returns a synthetic loopback peer address distinct from the
// listener's own port, mirroring how a real TCP accept sees the client's
// ephemeral port.
func (l *memListener) nextClientAddr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	selfPort, _ := strconv.Atoi(portOf(l.addr))
	l.clientPort++
	if l.clientPort == selfPort {
		l.clientPort++
	}
	if l.clientPort > 65535 {
		l.clientPort = 20000
	}
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: l.clientPort}
}

// Listen returns an in-memory listener registered under a synthetic
// "host:port" address. The requested port (or an allocated one when 0) is only
// a routing key — nothing is bound on the OS. Acceptable networks are tcp,
// tcp4 and tcp6.
func Listen(network, address string) (net.Listener, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("memhttp: unsupported network %q", network)
	}
	host := "127.0.0.1"
	requestedPort := 0
	unspecified := false
	if h, p, err := net.SplitHostPort(address); err == nil {
		if h = strings.TrimSpace(h); h != "" {
			host = strings.Trim(h, "[]")
		}
		requestedPort, _ = strconv.Atoi(p)
	} else if address = strings.TrimSpace(address); address != "" {
		host = address
	}
	host = normalizeHost(host)
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		unspecified = true
		host = "127.0.0.1"
	}
	port := requestedPort
	if port == 0 || portInUse(port) {
		var err error
		port, err = allocatePort()
		if err != nil {
			return nil, err
		}
	}
	l := &memListener{
		addr:    &net.TCPAddr{IP: net.ParseIP(host), Port: port},
		pending: make(chan net.Conn, 1),
		done:    make(chan struct{}),
	}
	register(l, unspecified)
	return l, nil
}

// Dial connects to a listener previously created by Listen. It is the test
// counterpart of net.Dial for probes that verify a listener is accepting.
func Dial(network, addr string) (net.Conn, error) {
	conn, ok, err := dialRegistered(network, addr)
	if !ok {
		return nil, fmt.Errorf("memhttp: no in-memory listener registered at %s", addr)
	}
	return conn, err
}

// Server is an httptest.Server-like HTTP server served over an in-memory
// listener. Clients created by Client (or any HTTP client using the process
// default transport) reach it through the pipe transport.
type Server struct {
	URL      string
	Listener net.Listener

	srv *http.Server
}

// NewServer starts serving handler over an in-memory listener and returns the
// server. Close must be called when the test is done.
func NewServer(handler http.Handler) *Server {
	l, err := Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	s := &Server{URL: "http://" + l.Addr().String(), Listener: l, srv: srv}
	go func() {
		_ = srv.Serve(l)
	}()
	return s
}

// Client returns an HTTP client whose transport routes the server's registered
// address through the in-memory pipe.
func (s *Server) Client() *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: memDialContext}}
}

// Close shuts the HTTP server down and releases the listener registration.
func (s *Server) Close() {
	if s == nil {
		return
	}
	_ = s.srv.Close()
	_ = s.Listener.Close()
}

// ---------------------------------------------------------------------------
// Registry and process-wide dial hooks
// ---------------------------------------------------------------------------

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.Trim(host, "[]")
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

func normalizeAddr(network, address string) string {
	address = strings.TrimSpace(address)
	if h, p, err := net.SplitHostPort(address); err == nil {
		return net.JoinHostPort(normalizeHost(h), p)
	}
	return strings.ToLower(address)
}

func register(l *memListener, alsoLoopback bool) {
	addr := l.addr.String()
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[normalizeAddr("tcp", addr)] = l
	if alsoLoopback {
		registry[normalizeAddr("tcp", "127.0.0.1:"+portOf(l.addr))] = l
	}
}

func unregister(addr string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	key := normalizeAddr("tcp", addr)
	delete(registry, key)
	// Remove any loopback alias registered for an unspecified host.
	delete(registry, normalizeAddr("tcp", "127.0.0.1:"+portOfAddr(key)))
}

func portOf(addr net.Addr) string {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return strconv.Itoa(tcp.Port)
	}
	_, p, _ := net.SplitHostPort(addr.String())
	return p
}

func portOfAddr(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

func portInUse(port int) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	for _, l := range registry {
		if portOf(l.addr) == strconv.Itoa(port) {
			return true
		}
	}
	return false
}

func allocatePort() (int, error) {
	for i := 0; i < 64; i++ {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		port := 20000 + (int(b[0])<<8|int(b[1]))%40000
		if !portInUse(port) {
			return port, nil
		}
	}
	return 0, errors.New("memhttp: no free synthetic test port")
}

// dialRegistered routes a dial to the in-memory listener registered for addr,
// reporting ok=false when addr is not a registered synthetic address.
func dialRegistered(network, addr string) (net.Conn, bool, error) {
	key := normalizeAddr(network, addr)
	registryMu.RLock()
	l := registry[key]
	registryMu.RUnlock()
	if l == nil {
		return nil, false, nil
	}
	conn, err := l.dial()
	if err != nil {
		return nil, true, err
	}
	return conn, true, nil
}

// memDialContext dials registered synthetic addresses through the pipe and
// falls back to a real dialer for everything else.
func memDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if conn, ok, err := dialRegistered(network, addr); ok {
		return conn, err
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// init installs the process-wide dial hooks once, at test-binary startup, so
// every HTTP client (http.DefaultClient, &http.Client{}, netclient clones) and
// websocket dialer routes registered synthetic addresses through memory. Real
// addresses keep their exact previous behavior via the fallback dialer.
func init() {
	installOnce.Do(func() {
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			clone := base.Clone()
			orig := clone.DialContext
			if orig == nil {
				orig = (&net.Dialer{}).DialContext
			}
			clone.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if conn, ok, err := dialRegistered(network, addr); ok {
					return conn, err
				}
				return orig(ctx, network, addr)
			}
			http.DefaultTransport = clone
		}
		d := websocket.DefaultDialer
		orig := d.NetDial
		d.NetDial = func(network, addr string) (net.Conn, error) {
			if conn, ok, err := dialRegistered(network, addr); ok {
				return conn, err
			}
			if orig != nil {
				return orig(network, addr)
			}
			return net.Dial(network, addr)
		}
	})
}
