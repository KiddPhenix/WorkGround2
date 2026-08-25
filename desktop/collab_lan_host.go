package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"workground2/internal/collab"
)

type collaborationLANRoom struct {
	owner      string
	generation uint64
}

// collaborationLANHost owns the single V2 listener and authoritative Service
// shared by every active Room in one Desktop process.
type collaborationLANHost struct {
	mu sync.Mutex

	listener  net.Listener
	server    *http.Server
	authority *collaborationAuthority
	host      string
	port      int
	rooms     map[string]collaborationLANRoom
	nextGen   uint64
	serveErr  error
}

func (a *App) sharedCollaborationLAN() *collaborationLANHost {
	a.collaborationLANMu.Lock()
	defer a.collaborationLANMu.Unlock()
	if a.collaborationLAN == nil {
		a.collaborationLAN = &collaborationLANHost{}
	}
	return a.collaborationLAN
}

func (a *App) closeCollaborationLAN(ctx context.Context) error {
	a.collaborationLANMu.Lock()
	host := a.collaborationLAN
	a.collaborationLAN = nil
	a.collaborationLANMu.Unlock()
	if host == nil {
		return nil
	}
	return host.Close(ctx)
}

func (h *collaborationLANHost) register(input HostCollaborationRoomInput, authority *collaborationAuthority, owner string) (int, func(), error) {
	room := strings.TrimSpace(input.Room)
	owner = strings.TrimSpace(owner)
	if room == "" || owner == "" {
		return 0, nil, fmt.Errorf("room and owner are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ensureStartedLocked(input.ListenHost, input.Port, authority); err != nil {
		return 0, nil, err
	}
	if current, ok := h.rooms[room]; ok && current.owner != owner {
		return 0, nil, fmt.Errorf("Room %q is already active in another Session", room)
	}
	h.nextGen++
	registration := collaborationLANRoom{owner: owner, generation: h.nextGen}
	h.rooms[room] = registration
	var once sync.Once
	release := func() {
		once.Do(func() {
			h.mu.Lock()
			if current, ok := h.rooms[room]; ok && current == registration {
				delete(h.rooms, room)
			}
			h.mu.Unlock()
		})
	}
	return h.port, release, nil
}

func (h *collaborationLANHost) ensureStartedLocked(host string, requestedPort int, authority *collaborationAuthority) error {
	if authority == nil || authority.service == nil || authority.hub == nil {
		return fmt.Errorf("collaboration authority is unavailable")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	if h.server != nil {
		if h.authority != authority {
			return fmt.Errorf("shared collaboration V2 listener already uses another authority")
		}
		if h.serveErr != nil {
			return fmt.Errorf("shared collaboration V2 listener stopped: %w", h.serveErr)
		}
		if !strings.EqualFold(h.host, host) {
			return fmt.Errorf("shared collaboration V2 listener already uses host %s", h.host)
		}
		if requestedPort != 0 && requestedPort != h.port {
			return fmt.Errorf("shared collaboration V2 listener already uses port %d", h.port)
		}
		return nil
	}
	if h.rooms == nil {
		h.rooms = make(map[string]collaborationLANRoom)
	}
	listener, err := listenNetwork("tcp", net.JoinHostPort(host, strconv.Itoa(requestedPort)))
	if err != nil {
		return err
	}
	h.listener, h.authority = listener, authority
	h.host = host
	h.port = listener.Addr().(*net.TCPAddr).Port
	h.serveErr = nil
	h.server = &http.Server{Handler: collab.NewV2Handler(authority.service, h.roomActive, authority.hub), ReadHeaderTimeout: 10 * time.Second}
	server := h.server
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.mu.Lock()
			if h.server == server {
				h.serveErr = err
			}
			h.mu.Unlock()
			fmt.Fprintf(os.Stderr, "collaboration V2 listener stopped: %v\n", err)
		}
	}()
	return nil
}

func (h *collaborationLANHost) roomActive(roomID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.rooms[strings.TrimSpace(roomID)]
	return ok
}

func (h *collaborationLANHost) Close(ctx context.Context) error {
	h.mu.Lock()
	server := h.server
	h.server, h.listener = nil, nil
	h.rooms = nil
	h.authority = nil
	h.host, h.port, h.serveErr = "", 0, nil
	h.mu.Unlock()
	if server == nil {
		return nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	}
	return server.Shutdown(ctx)
}
