package relayserver

import (
	"errors"
	"sync"
	"time"
)

var (
	errTunnelLimit  = errors.New("tunnel limit reached")
	errHostConflict = errors.New("tunnel host already bound")
	errHostOffline  = errors.New("tunnel host offline")
	errPeerLimit    = errors.New("peer limit reached")
)

type tunnel struct {
	id       string
	host     *client
	peers    map[string]*client
	lastHost time.Time
	grants   map[string]time.Time
}

type registry struct {
	mu         sync.RWMutex
	tunnels    map[string]*tunnel
	maxTunnels int
	maxPeers   int
	heartbeat  time.Duration
}

func newRegistry(cfg Config) *registry {
	return &registry{tunnels: make(map[string]*tunnel), maxTunnels: cfg.MaxTunnels, maxPeers: cfg.MaxPeersPerTunnel, heartbeat: cfg.IdleTimeout}
}

func (r *registry) create(id string, host *client) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for tunnelID, existing := range r.tunnels {
		if existing.host == nil && now.Sub(existing.lastHost) > r.heartbeat {
			delete(r.tunnels, tunnelID)
		}
	}
	if t := r.tunnels[id]; t != nil {
		if t.host != nil && t.host != host {
			return errHostConflict
		}
		t.host, t.lastHost = host, now
		return nil
	}
	if len(r.tunnels) >= r.maxTunnels {
		return errTunnelLimit
	}
	r.tunnels[id] = &tunnel{id: id, host: host, peers: make(map[string]*client), lastHost: now, grants: make(map[string]time.Time)}
	return nil
}

func (r *registry) grant(tunnelID, peerA, peerB string, expires time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tunnels[tunnelID]
	if t == nil || t.host == nil {
		return errHostOffline
	}
	if peerA == "" || peerB == "" || peerA == peerB || t.peers[peerA] == nil || t.peers[peerB] == nil {
		return errors.New("stream grant peers are not attached")
	}
	if expires.Before(time.Now()) || expires.After(time.Now().Add(10*time.Minute)) {
		return errors.New("stream grant expiry is invalid")
	}
	t.grants[peerPair(peerA, peerB)] = expires
	return nil
}

func (r *registry) streamAllowed(tunnelID, peerA, peerB string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tunnels[tunnelID]
	if t == nil {
		return false
	}
	key := peerPair(peerA, peerB)
	expires, ok := t.grants[key]
	if ok && time.Now().After(expires) {
		delete(t.grants, key)
		return false
	}
	return ok
}

func peerPair(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

func (r *registry) bind(id string, host *client) error { return r.create(id, host) }

func (r *registry) attach(tunnelID, peerID string, peer *client) (*client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tunnels[tunnelID]
	if t == nil || t.host == nil {
		return nil, errHostOffline
	}
	if len(t.peers) >= r.maxPeers {
		return nil, errPeerLimit
	}
	t.peers[peerID] = peer
	return t.host, nil
}

func (r *registry) host(tunnelID string) *client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t := r.tunnels[tunnelID]; t != nil {
		return t.host
	}
	return nil
}

func (r *registry) peer(tunnelID, peerID string) *client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t := r.tunnels[tunnelID]; t != nil {
		return t.peers[peerID]
	}
	return nil
}

func (r *registry) touch(tunnelID string, host *client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.tunnels[tunnelID]; t != nil && t.host == host {
		t.lastHost = time.Now()
	}
}

func (r *registry) active(tunnelID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t := r.tunnels[tunnelID]
	return t != nil && t.host != nil && time.Since(t.lastHost) <= r.heartbeat
}

func (r *registry) remove(c *client) (host *client, peers []*client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.tunnelID == "" {
		return nil, nil
	}
	t := r.tunnels[c.tunnelID]
	if t == nil {
		return nil, nil
	}
	if t.host == c {
		t.host = nil
		for _, p := range t.peers {
			peers = append(peers, p)
		}
		return nil, peers
	}
	if c.peerID != "" && t.peers[c.peerID] == c {
		delete(t.peers, c.peerID)
		return t.host, nil
	}
	return nil, nil
}

func (r *registry) counts() (int, int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	peers := 0
	for _, t := range r.tunnels {
		peers += len(t.peers)
	}
	return len(r.tunnels), peers
}
