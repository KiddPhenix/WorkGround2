package collab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxHTTPBody   = 1024 * 1024
	maxEventBatch = 1000
)

type Handler struct {
	service *Service
	hub     *Hub
	files   *fileTransferRegistry
}

// NewHandler exposes the V1 HTTP/JSON and SSE transport. When hub is omitted,
// the Service's hub is used, keeping persistence-before-broadcast ordering.
func NewHandler(service *Service, hubs ...*Hub) http.Handler {
	hub := service.Hub()
	if len(hubs) > 0 && hubs[0] != nil {
		hub = hubs[0]
		service.hub = hub
	}
	return &Handler{service: service, hub: hub, files: newFileTransferRegistry(service)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/collab/v1/join" {
		h.join(w, r)
		return
	}
	if r.URL.Path == "/collab/v1/leave" {
		h.signal(w, r, true)
		return
	}
	if r.URL.Path == "/collab/v1/heartbeat" {
		h.signal(w, r, false)
		return
	}
	const prefix = "/collab/v1/rooms/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeError(w, fail(CodeNotFound, "endpoint does not exist"))
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) >= 4 && parts[0] != "" && parts[1] == "files" {
		room, err := url.PathUnescape(parts[0])
		if err != nil {
			writeError(w, fail(CodeInvalid, "invalid room path"))
			return
		}
		h.files.serve(w, r, room, parts[2:], 1)
		return
	}
	if len(parts) == 3 && parts[0] != "" && parts[1] == "snapshot" && parts[2] == "manifest" {
		room, err := url.PathUnescape(parts[0])
		if err != nil {
			writeError(w, fail(CodeInvalid, "invalid room path"))
			return
		}
		h.snapshotManifest(w, r, room)
		return
	}
	if len(parts) == 4 && parts[0] != "" && parts[1] == "snapshot" && parts[2] == "chunks" {
		room, err := url.PathUnescape(parts[0])
		if err != nil {
			writeError(w, fail(CodeInvalid, "invalid room path"))
			return
		}
		h.snapshotChunk(w, r, room, parts[3])
		return
	}
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, fail(CodeNotFound, "endpoint does not exist"))
		return
	}
	room, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, fail(CodeInvalid, "invalid room path"))
		return
	}
	switch parts[1] {
	case "snapshot":
		h.snapshot(w, r, room)
	case "events":
		h.events(w, r, room)
	case "stream":
		h.stream(w, r, room)
	case "commands":
		h.command(w, r, room)
	default:
		writeError(w, fail(CodeNotFound, "endpoint does not exist"))
	}
}

func (h *Handler) join(w http.ResponseWriter, r *http.Request) {
	h.joinRoom(w, r, "")
}

func (h *Handler) joinRoom(w http.ResponseWriter, r *http.Request, room string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var input JoinInput
	if err := decodeBody(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if room != "" {
		if input.Room != "" && input.Room != room {
			writeError(w, fail(CodeInvalid, "room body does not match path"))
			return
		}
		input.Room = room
	}
	result, err := h.service.Join(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) signal(w http.ResponseWriter, r *http.Request, leave bool) {
	h.signalRoom(w, r, "", leave)
}

func (h *Handler) signalRoom(w http.ResponseWriter, r *http.Request, room string, leave bool) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var input SessionInput
	if err := decodeBody(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if room != "" {
		if input.Room != "" && input.Room != room {
			writeError(w, fail(CodeInvalid, "room body does not match path"))
			return
		}
		input.Room = room
	}
	if input.Session == "" {
		input.Session = sessionFrom(r)
	}
	var receipt CommandReceipt
	var err error
	if leave {
		receipt, err = h.service.Leave(r.Context(), input)
	} else {
		receipt, err = h.service.Heartbeat(r.Context(), input)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request, room string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	value, err := h.service.Snapshot(r.Context(), room, sessionFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *Handler) snapshotManifest(w http.ResponseWriter, r *http.Request, room string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	value, err := h.service.SnapshotManifest(r.Context(), room, sessionFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *Handler) snapshotChunk(w http.ResponseWriter, r *http.Request, room, rawIndex string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	index, err := strconv.Atoi(rawIndex)
	if err != nil || index < 0 {
		writeError(w, fail(CodeInvalid, "snapshot chunk index must be non-negative"))
		return
	}
	value, err := h.service.SnapshotChunk(r.Context(), room, sessionFrom(r), r.URL.Query().Get("snapshotId"), index)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Collab-Snapshot-ID", value.SnapshotID)
	w.Header().Set("X-Collab-Chunk-Index", strconv.Itoa(value.Index))
	w.Header().Set("X-Collab-Chunk-SHA256", value.SHA256)
	w.Header().Set("Content-Length", strconv.Itoa(len(value.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(value.Data)
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request, room string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	after, err := afterSequence(r)
	if err != nil {
		writeError(w, err)
		return
	}
	values, err := h.service.Events(r.Context(), room, sessionFrom(r), after)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(values) > maxEventBatch {
		values = values[:maxEventBatch]
	}
	writeJSON(w, http.StatusOK, values)
}

func (h *Handler) command(w http.ResponseWriter, r *http.Request, room string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var input CommandEnvelope
	if err := decodeBody(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if input.Room != "" && input.Room != room {
		writeError(w, fail(CodeInvalid, "room body does not match path"))
		return
	}
	input.Room = room
	if input.Session == "" {
		input.Session = sessionFrom(r)
	}
	receipt, err := h.service.Submit(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request, room string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, fail(CodeInternal, "streaming is unavailable"))
		return
	}
	after, err := afterSequence(r)
	if err != nil {
		writeError(w, err)
		return
	}
	session := sessionFrom(r)
	wake, unsubscribe, subscribeErr := h.hub.TrySubscribe(room)
	if subscribeErr != nil {
		writeError(w, subscribeErr)
		return
	}
	defer unsubscribe()
	// Authorize before committing headers. Subscribing first closes the race
	// between the initial catch-up and a concurrent persisted event.
	if _, err := h.service.Events(r.Context(), room, session, after); err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	last := after
	if err := h.sendAvailable(r.Context(), w, flusher, room, session, &last); err != nil {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-wake:
			if !ok {
				return
			}
			if err := h.sendAvailable(r.Context(), w, flusher, room, session, &last); err != nil {
				return
			}
		case <-ticker.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) sendAvailable(ctx context.Context, w io.Writer, flusher http.Flusher, room, session string, last *uint64) error {
	for {
		events, err := h.service.Events(ctx, room, session, *last)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		if len(events) > maxEventBatch {
			events = events[:maxEventBatch]
		}
		for _, event := range events {
			data, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: room\ndata: %s\n\n", event.Sequence, data); err != nil {
				return err
			}
			*last = event.Sequence
		}
		flusher.Flush()
		if len(events) < maxEventBatch {
			return nil
		}
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fail(CodeInvalid, "invalid JSON body: "+err.Error())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fail(CodeInvalid, "JSON body must contain one value")
	}
	return nil
}

func afterSequence(r *http.Request) (uint64, error) {
	value := r.URL.Query().Get("afterSequence")
	if value == "" {
		value = r.URL.Query().Get("after")
	}
	if value == "" {
		value = r.Header.Get("Last-Event-ID")
	}
	if value == "" {
		return 0, nil
	}
	after, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fail(CodeInvalid, "afterSequence must be an unsigned integer")
	}
	return after, nil
}

func sessionFrom(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Collab-Session")); value != "" {
		return value
	}
	const bearer = "Bearer "
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) >= len(bearer) && strings.EqualFold(value[:len(bearer)], bearer) {
		return strings.TrimSpace(value[len(bearer):])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	var value *Error
	if !errors.As(err, &value) {
		value = &Error{Code: CodeInternal, Message: "internal collaboration error", Retryable: true, Cause: err}
	}
	status := http.StatusInternalServerError
	switch value.Code {
	case CodeInvalid:
		status = http.StatusBadRequest
	case CodeUnauthorized, CodeResumeNeeded:
		status = http.StatusUnauthorized
	case CodeForbidden:
		status = http.StatusForbidden
	case CodeNotFound:
		status = http.StatusNotFound
	case CodeConflict:
		status = http.StatusConflict
	case CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, value)
}

func methodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	writeJSON(w, http.StatusMethodNotAllowed, &Error{Code: CodeInvalid, Message: "method not allowed"})
}
