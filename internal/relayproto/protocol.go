// Package relayproto defines the transport-visible WorkGround2 relay protocol.
package relayproto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	Version         = 1
	MaxHeaderBytes  = 8 << 10
	MaxMessageBytes = 1 << 20
)

const (
	TypeHello               = "relay.hello"
	TypeTunnelCreate        = "tunnel.create"
	TypeHostBind            = "host.bind"
	TypeHostBound           = "host.bound"
	TypeGuestAttach         = "guest.attach"
	TypePeerOpened          = "peer.opened"
	TypePeerClosed          = "peer.closed"
	TypeAdvertisementUpsert = "advertisement.upsert"
	TypeAdvertisementRevoke = "advertisement.revoke"
	TypePing                = "ping"
	TypePong                = "pong"
	TypeError               = "error"
)

// Header is the Relay-visible routing envelope. Payload remains opaque for
// end-to-end encrypted frame types.
type Header struct {
	Version        int      `json:"v"`
	Type           string   `json:"type"`
	RelayRequestID string   `json:"relayRequestId,omitempty"`
	TunnelID       string   `json:"tunnelId,omitempty"`
	PeerID         string   `json:"peerId,omitempty"`
	StreamID       uint32   `json:"streamId,omitempty"`
	Epoch          uint64   `json:"epoch,omitempty"`
	Sequence       uint64   `json:"seq,omitempty"`
	Flags          []string `json:"flags,omitempty"`
}

// Encode creates one WebSocket binary message.
func Encode(header Header, payload []byte) ([]byte, error) {
	if header.Version == 0 {
		header.Version = Version
	}
	b, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("marshal relay header: %w", err)
	}
	if len(b) > MaxHeaderBytes {
		return nil, fmt.Errorf("relay header exceeds %d bytes", MaxHeaderBytes)
	}
	if 4+len(b)+len(payload) > MaxMessageBytes {
		return nil, fmt.Errorf("relay message exceeds %d bytes", MaxMessageBytes)
	}
	out := make([]byte, 4+len(b)+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(b)))
	copy(out[4:], b)
	copy(out[4+len(b):], payload)
	return out, nil
}

// Decode validates and splits one WebSocket binary message.
func Decode(message []byte) (Header, []byte, error) {
	if len(message) < 4 {
		return Header{}, nil, errors.New("relay message is shorter than header length")
	}
	if len(message) > MaxMessageBytes {
		return Header{}, nil, fmt.Errorf("relay message exceeds %d bytes", MaxMessageBytes)
	}
	n := int(binary.BigEndian.Uint32(message[:4]))
	if n <= 0 || n > MaxHeaderBytes || 4+n > len(message) {
		return Header{}, nil, errors.New("invalid relay header length")
	}
	var h Header
	if err := json.Unmarshal(message[4:4+n], &h); err != nil {
		return Header{}, nil, fmt.Errorf("decode relay header: %w", err)
	}
	if h.Version != Version {
		return Header{}, nil, fmt.Errorf("unsupported relay version %d", h.Version)
	}
	if h.Type == "" {
		return Header{}, nil, errors.New("relay frame type is required")
	}
	return h, message[4+n:], nil
}

func MarshalPayload(v any) ([]byte, error) { return json.Marshal(v) }

func UnmarshalPayload(payload []byte, v any) error {
	if len(payload) == 0 {
		return errors.New("relay payload is required")
	}
	if err := json.Unmarshal(payload, v); err != nil {
		return fmt.Errorf("decode relay payload: %w", err)
	}
	return nil
}

// IsDataFrame identifies frames routed through the lower-priority data queue.
func IsDataFrame(frameType string) bool { return frameType == "stream.data" }

// IsRoutable identifies peer/host frames whose payload is opaque to Relay.
func IsRoutable(frameType string) bool {
	switch frameType {
	case "e2e.hello", "e2e.accept", "rpc.request", "rpc.response", "event.notify", "route.update",
		"stream.grant", "stream.open", "stream.data", "stream.window", "stream.end", "stream.reset":
		return true
	default:
		return false
	}
}
