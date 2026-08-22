package collab

import (
	"net/http"
	"net/url"
	"strings"
)

// V2RoomActive reports whether a Room is currently hosted by the shared V2
// listener. Persisted Rooms remain unavailable until their Desktop runtime has
// registered them again.
type V2RoomActive func(roomID string) bool

type v2Handler struct {
	handler *Handler
	active  V2RoomActive
}

// NewV2Handler exposes the room-scoped V2 HTTP/JSON and SSE transport. The
// Service and file registry are shared across every active Room on the listener.
func NewV2Handler(service *Service, active V2RoomActive, hubs ...*Hub) http.Handler {
	base := NewHandler(service, hubs...).(*Handler)
	return &v2Handler{handler: base, active: active}
}

func (h *v2Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Collab-Protocol", "2")
	const prefix = "/collab/v2/rooms/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeError(w, fail(CodeNotFound, "endpoint does not exist"))
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, fail(CodeNotFound, "endpoint does not exist"))
		return
	}
	room, err := url.PathUnescape(parts[0])
	if err != nil || room == "" {
		writeError(w, fail(CodeInvalid, "invalid room path"))
		return
	}
	if h.active == nil || !h.active(room) {
		writeError(w, fail(CodeNotFound, "room is not active"))
		return
	}
	if len(parts) >= 4 && parts[1] == "files" {
		h.handler.files.serve(w, r, room, parts[2:], 2)
		return
	}
	if len(parts) == 3 && parts[1] == "snapshot" && parts[2] == "manifest" {
		h.handler.snapshotManifest(w, r, room)
		return
	}
	if len(parts) == 4 && parts[1] == "snapshot" && parts[2] == "chunks" {
		h.handler.snapshotChunk(w, r, room, parts[3])
		return
	}
	if len(parts) != 2 {
		writeError(w, fail(CodeNotFound, "endpoint does not exist"))
		return
	}
	switch parts[1] {
	case "join":
		h.handler.joinRoom(w, r, room)
	case "leave":
		h.handler.signalRoom(w, r, room, true)
	case "heartbeat":
		h.handler.signalRoom(w, r, room, false)
	case "snapshot":
		h.handler.snapshot(w, r, room)
	case "events":
		h.handler.events(w, r, room)
	case "stream":
		h.handler.stream(w, r, room)
	case "commands":
		h.handler.command(w, r, room)
	default:
		writeError(w, fail(CodeNotFound, "endpoint does not exist"))
	}
}
