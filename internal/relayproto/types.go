package relayproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type Hello struct {
	MinVersion   int      `json:"minVersion"`
	MaxVersion   int      `json:"maxVersion"`
	Capabilities []string `json:"capabilities,omitempty"`
	AccessToken  string   `json:"accessToken,omitempty"`
}

type HelloResult struct {
	Version      int      `json:"version"`
	Capabilities []string `json:"capabilities"`
	RelayID      string   `json:"relayId"`
}

type TunnelCreate struct {
	AccessToken string `json:"accessToken,omitempty"`
}

type HostBind struct {
	Capability  string `json:"capability"`
	AccessToken string `json:"accessToken,omitempty"`
}

type HostBound struct {
	TunnelID        string `json:"tunnelId"`
	HostCapability  string `json:"hostCapability,omitempty"`
	GuestCapability string `json:"guestCapability,omitempty"`
	ExpiresAt       int64  `json:"expiresAt,omitempty"`
}

type GuestAttach struct {
	Capability string `json:"capability,omitempty"`
	JoinRef    string `json:"joinRef,omitempty"`
}

type PeerOpened struct {
	TunnelID string `json:"tunnelId"`
	PeerID   string `json:"peerId"`
}

// StreamGrant is Relay-visible authorization for a short-lived peer stream.
type StreamGrant struct {
	GrantID   string    `json:"grantId"`
	PeerA     string    `json:"peerA"`
	PeerB     string    `json:"peerB"`
	ExpiresAt time.Time `json:"expiresAt"`
	MaxBytes  int64     `json:"maxBytes,omitempty"`
}

type Advertisement struct {
	PublicRoomID          string    `json:"publicRoomId"`
	Name                  string    `json:"name"`
	Description           string    `json:"description,omitempty"`
	Tags                  []string  `json:"tags,omitempty"`
	Visibility            string    `json:"visibility"`
	RequiresToken         bool      `json:"requiresToken"`
	OnlineCount           int       `json:"onlineCount,omitempty"`
	Capacity              int       `json:"capacity,omitempty"`
	HostPublicKey         string    `json:"hostPublicKey"`
	HostKeyFingerprint    string    `json:"hostKeyFingerprint"`
	AdvertisementRevision uint64    `json:"advertisementRevision"`
	ExpiresAt             time.Time `json:"expiresAt"`
	Signature             string    `json:"signature"`
}

// AdvertisementSigningBytes returns the stable bytes signed by the Room Host.
func AdvertisementSigningBytes(ad Advertisement) ([]byte, error) {
	ad.Signature = ""
	return json.Marshal(ad)
}

func HostKeyFingerprint(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type AdvertisementUpsert struct {
	Advertisement Advertisement `json:"advertisement"`
}
type AdvertisementRevoke struct {
	PublicRoomID string `json:"publicRoomId"`
	Revision     uint64 `json:"advertisementRevision"`
}

type RoomList struct {
	Rooms      []Advertisement `json:"rooms"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type JoinRefResult struct {
	JoinRef   string `json:"joinRef"`
	TunnelID  string `json:"tunnelId"`
	ExpiresAt int64  `json:"expiresAt"`
}

type RelayError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	RetryAfter int    `json:"retryAfter,omitempty"`
}

func (e RelayError) Error() string { return e.Code + ": " + e.Message }

const (
	ErrAuthFailed            = "relay_auth_failed"
	ErrTunnelNotFound        = "tunnel_not_found"
	ErrHostOffline           = "host_offline"
	ErrHostConflict          = "host_conflict"
	ErrProtocolMismatch      = "protocol_mismatch"
	ErrCapabilityExpired     = "capability_expired"
	ErrRateLimited           = "rate_limited"
	ErrBackpressure          = "backpressure"
	ErrAdvertisementRejected = "advertisement_rejected"
)
