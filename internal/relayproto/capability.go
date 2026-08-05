package relayproto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Role string

const (
	RoleHost  Role = "host"
	RoleGuest Role = "guest"
	RoleJoin  Role = "join"
)

type CapabilityLimits struct {
	MaxStreams        int   `json:"maxStreams,omitempty"`
	MaxBytesPerSecond int64 `json:"maxBytesPerSecond,omitempty"`
}

type CapabilityClaims struct {
	Version   int              `json:"version"`
	RelayID   string           `json:"relayId"`
	TunnelID  string           `json:"tunnelId"`
	Role      Role             `json:"role"`
	IssuedAt  int64            `json:"issuedAt"`
	ExpiresAt int64            `json:"expiresAt"`
	Limits    CapabilityLimits `json:"limits,omitempty"`
	Nonce     string           `json:"nonce"`
}

type Signer struct {
	relayID string
	key     []byte
	now     func() time.Time
}

func NewSigner(relayID string, key []byte) (*Signer, error) {
	if relayID == "" {
		return nil, errors.New("relay id is required")
	}
	if len(key) < 32 {
		return nil, errors.New("relay master key must contain at least 32 bytes")
	}
	return &Signer{relayID: relayID, key: append([]byte(nil), key...), now: time.Now}, nil
}

func (s *Signer) Issue(tunnelID string, role Role, ttl time.Duration, limits CapabilityLimits) (string, CapabilityClaims, error) {
	if tunnelID == "" || !validRole(role) || ttl <= 0 {
		return "", CapabilityClaims{}, errors.New("invalid capability request")
	}
	nonce := make([]byte, 18)
	if _, err := rand.Read(nonce); err != nil {
		return "", CapabilityClaims{}, fmt.Errorf("generate capability nonce: %w", err)
	}
	now := s.now().UTC()
	claims := CapabilityClaims{
		Version: Version, RelayID: s.relayID, TunnelID: tunnelID, Role: role,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix(), Limits: limits,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	}
	token, err := s.Sign(claims)
	return token, claims, err
}

func (s *Signer) Sign(claims CapabilityClaims) (string, error) {
	b, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Signer) Verify(token string, expected Role, tunnelID string) (CapabilityClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return CapabilityClaims{}, errors.New("invalid capability format")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(sig) != parts[1] {
		return CapabilityClaims{}, errors.New("invalid capability signature")
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return CapabilityClaims{}, errors.New("invalid capability signature")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(b) != parts[0] {
		return CapabilityClaims{}, errors.New("invalid capability payload")
	}
	var claims CapabilityClaims
	if err := json.Unmarshal(b, &claims); err != nil {
		return CapabilityClaims{}, errors.New("invalid capability claims")
	}
	if claims.Version != Version || claims.RelayID != s.relayID || claims.Role != expected || (tunnelID != "" && claims.TunnelID != tunnelID) {
		return CapabilityClaims{}, errors.New("capability scope mismatch")
	}
	if claims.ExpiresAt <= s.now().Unix() {
		return CapabilityClaims{}, errors.New("capability expired")
	}
	if claims.IssuedAt > s.now().Add(time.Minute).Unix() {
		return CapabilityClaims{}, errors.New("capability issued in the future")
	}
	return claims, nil
}

func validRole(role Role) bool { return role == RoleHost || role == RoleGuest || role == RoleJoin }
