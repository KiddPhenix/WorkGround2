package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"workground2/internal/config"
	"workground2/internal/relayproto"
)

const relayControlTimeout = 15 * time.Second

type collaborationRelaySocket struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
	seq       atomic.Uint64
}

func dialCollaborationRelay(ctx context.Context, relay config.RelayConfig) (*collaborationRelaySocket, relayproto.HelloResult, error) {
	if _, ok := ctx.Deadline(); !ok {
		timeout := 10 * time.Second
		if cfg, err := config.Load(); err == nil && cfg.Collaboration.ConnectTimeout > 0 {
			timeout = time.Duration(cfg.Collaboration.ConnectTimeout) * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if !relay.Enabled {
		return nil, relayproto.HelloResult{}, fmt.Errorf("relay %q is disabled", relay.ID)
	}
	if err := validateRelayDialURL(relay.URL, relay.AllowInsecure); err != nil {
		return nil, relayproto.HelloResult{}, err
	}
	header := make(http.Header)
	if name := strings.TrimSpace(relay.AccessTokenEnv); name != "" {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
	}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, relay.URL, header)
	if err != nil {
		if response != nil {
			return nil, relayproto.HelloResult{}, fmt.Errorf("dial relay %q: %s", relay.ID, response.Status)
		}
		return nil, relayproto.HelloResult{}, fmt.Errorf("dial relay %q: %w", relay.ID, err)
	}
	socket := &collaborationRelaySocket{conn: conn}
	requestID := newCollaborationRequestID("relay-hello")
	accessToken := ""
	if relay.AccessTokenEnv != "" {
		accessToken = strings.TrimSpace(os.Getenv(relay.AccessTokenEnv))
	}
	if err := socket.write(relayproto.Header{Type: relayproto.TypeHello, RelayRequestID: requestID}, relayproto.Hello{
		MinVersion: 1, MaxVersion: 1, Capabilities: []string{"rpc", "event", "file_stream", "discovery_v1"}, AccessToken: accessToken,
	}); err != nil {
		_ = socket.Close(context.Background())
		return nil, relayproto.HelloResult{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(relayControlTimeout))
	helloHeader, payload, err := socket.read()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = socket.Close(context.Background())
		return nil, relayproto.HelloResult{}, err
	}
	if helloHeader.Type == relayproto.TypeError {
		_ = socket.Close(context.Background())
		return nil, relayproto.HelloResult{}, decodeRelayError(payload)
	}
	if helloHeader.Type != relayproto.TypeHello || helloHeader.RelayRequestID != requestID {
		_ = socket.Close(context.Background())
		return nil, relayproto.HelloResult{}, fmt.Errorf("relay %q returned an invalid hello response", relay.ID)
	}
	var hello relayproto.HelloResult
	if err := relayproto.UnmarshalPayload(payload, &hello); err != nil {
		_ = socket.Close(context.Background())
		return nil, relayproto.HelloResult{}, err
	}
	if hello.Version != relayproto.Version {
		_ = socket.Close(context.Background())
		return nil, relayproto.HelloResult{}, fmt.Errorf("relay %q negotiated unsupported protocol %d", relay.ID, hello.Version)
	}
	return socket, hello, nil
}

func validateRelayDialURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid Relay URL %q", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "wss":
		return nil
	case "ws":
		if allowInsecure || isRelayLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("public ws:// Relay %q is blocked; enable allow_insecure for this Relay in Settings", u.Hostname())
	default:
		return fmt.Errorf("Relay URL must use wss:// or ws://")
	}
}

func isRelayLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(host), "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *collaborationRelaySocket) write(header relayproto.Header, value any) error {
	payload, err := relayproto.MarshalPayload(value)
	if err != nil {
		return err
	}
	return s.writeBytes(header, payload)
}

func (s *collaborationRelaySocket) writeBytes(header relayproto.Header, payload []byte) error {
	if header.Sequence == 0 && relayproto.IsRoutable(header.Type) {
		header.Sequence = s.seq.Add(1)
	}
	message, err := relayproto.Encode(header, payload)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	err = s.conn.WriteMessage(websocket.BinaryMessage, message)
	_ = s.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		return &collaborationTransportError{message: "write Relay frame: " + err.Error(), retryable: true}
	}
	return nil
}

func (s *collaborationRelaySocket) read() (relayproto.Header, []byte, error) {
	messageType, message, err := s.conn.ReadMessage()
	if err != nil {
		return relayproto.Header{}, nil, &collaborationTransportError{message: "read Relay frame: " + err.Error(), retryable: true}
	}
	if messageType != websocket.BinaryMessage {
		return relayproto.Header{}, nil, &collaborationTransportError{message: "Relay returned a non-binary frame", retryable: false}
	}
	header, payload, err := relayproto.Decode(message)
	if err != nil {
		return relayproto.Header{}, nil, &collaborationTransportError{message: err.Error(), retryable: false}
	}
	return header, payload, nil
}

func (s *collaborationRelaySocket) Close(ctx context.Context) error {
	var result error
	s.closeOnce.Do(func() {
		deadline := time.Now().Add(time.Second)
		if value, ok := ctx.Deadline(); ok {
			deadline = value
		}
		s.writeMu.Lock()
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline)
		result = s.conn.Close()
		s.writeMu.Unlock()
	})
	return result
}

func decodeRelayError(payload []byte) error {
	var relayErr relayproto.RelayError
	if err := json.Unmarshal(payload, &relayErr); err != nil {
		return fmt.Errorf("decode Relay error: %w", err)
	}
	if relayErr.Message == "" {
		relayErr.Message = relayErr.Code
	}
	return &collaborationTransportError{message: relayErr.Error(), retryable: relayErr.Retryable}
}

func collaborationRelayByID(relayID string) (config.RelayConfig, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.RelayConfig{}, fmt.Errorf("load Relay settings: %w", err)
	}
	for _, relay := range cfg.Collaboration.Relays {
		if strings.EqualFold(strings.TrimSpace(relay.ID), strings.TrimSpace(relayID)) {
			return relay, nil
		}
	}
	return config.RelayConfig{}, fmt.Errorf("Relay %q is not configured", relayID)
}

func collaborationRelayByURL(relayURL string) (config.RelayConfig, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.RelayConfig{}, fmt.Errorf("load Relay settings: %w", err)
	}
	want, err := relayURLKey(relayURL)
	if err != nil {
		return config.RelayConfig{}, err
	}
	for _, relay := range cfg.Collaboration.Relays {
		key, keyErr := relayURLKey(relay.URL)
		if keyErr == nil && key == want {
			return relay, nil
		}
	}
	return config.RelayConfig{}, fmt.Errorf("Relay URL %q is not configured", relayURL)
}

func relayURLKey(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid Relay URL %q", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

func waitRelayControl(socket *collaborationRelaySocket, requestID string, accepted ...string) (relayproto.Header, []byte, error) {
	_ = socket.conn.SetReadDeadline(time.Now().Add(relayControlTimeout))
	defer socket.conn.SetReadDeadline(time.Time{})
	for {
		header, payload, err := socket.read()
		if err != nil {
			return relayproto.Header{}, nil, err
		}
		if header.Type == relayproto.TypeError && (header.RelayRequestID == "" || header.RelayRequestID == requestID) {
			return relayproto.Header{}, nil, decodeRelayError(payload)
		}
		if header.RelayRequestID != requestID {
			continue
		}
		for _, frameType := range accepted {
			if header.Type == frameType {
				return header, payload, nil
			}
		}
		return relayproto.Header{}, nil, errors.New("Relay returned an unexpected control frame: " + header.Type)
	}
}
