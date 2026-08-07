package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"

	"workground2/internal/relayproto"
)

type relayE2EHello struct {
	PublicKey string `json:"publicKey"`
	Nonce     string `json:"nonce"`
}

type relayE2EAccept struct {
	PublicKey          string `json:"publicKey"`
	HostPublicKey      string `json:"hostPublicKey"`
	HostKeyFingerprint string `json:"hostKeyFingerprint"`
	Signature          string `json:"signature"`
}

type relayE2ETranscript struct {
	Version     int    `json:"v"`
	TunnelID    string `json:"tunnelId"`
	PeerID      string `json:"peerId"`
	GuestPublic string `json:"guestPublicKey"`
	GuestNonce  string `json:"guestNonce"`
	HostPublic  string `json:"hostPublicKey"`
}

type relayCipher struct {
	send cipher.AEAD
	recv cipher.AEAD
}

func newRelayHostKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func relayHostKeyFingerprint(public ed25519.PublicKey) string {
	return relayproto.HostKeyFingerprint(public)
}

func newRelayEphemeral() (*ecdh.PrivateKey, string, string, error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", "", err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", "", err
	}
	return private, base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(nonce), nil
}

func relayHostAccept(tunnelID, peerID string, hello relayE2EHello, identity ed25519.PrivateKey) (relayE2EAccept, *relayCipher, error) {
	private, hostPublic, _, err := newRelayEphemeral()
	if err != nil {
		return relayE2EAccept{}, nil, err
	}
	guestPublic, err := decodeRelayX25519(hello.PublicKey)
	if err != nil {
		return relayE2EAccept{}, nil, err
	}
	shared, err := private.ECDH(guestPublic)
	if err != nil {
		return relayE2EAccept{}, nil, fmt.Errorf("relay e2e key agreement: %w", err)
	}
	transcript := relayE2ETranscript{Version: 1, TunnelID: tunnelID, PeerID: peerID, GuestPublic: hello.PublicKey, GuestNonce: hello.Nonce, HostPublic: hostPublic}
	encoded, _ := json.Marshal(transcript)
	public := identity.Public().(ed25519.PublicKey)
	accept := relayE2EAccept{
		PublicKey: hostPublic, HostPublicKey: base64.RawURLEncoding.EncodeToString(public),
		HostKeyFingerprint: relayHostKeyFingerprint(public), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(identity, encoded)),
	}
	keys, err := deriveRelayCipher(shared, encoded, true)
	return accept, keys, err
}

func relayGuestAccept(private *ecdh.PrivateKey, tunnelID, peerID string, hello relayE2EHello, accept relayE2EAccept, expectedHostKey string) (*relayCipher, error) {
	publicBytes, err := base64.RawURLEncoding.DecodeString(accept.HostPublicKey)
	if err != nil || len(publicBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid relay Host public key")
	}
	if relayHostKeyFingerprint(ed25519.PublicKey(publicBytes)) != accept.HostKeyFingerprint {
		return nil, fmt.Errorf("relay Host key fingerprint is invalid")
	}
	if expectedHostKey != "" {
		expected, decodeErr := decodeRelayPublicValue(expectedHostKey)
		if decodeErr != nil || len(expected) != ed25519.PublicKeySize || !ed25519.PublicKey(publicBytes).Equal(ed25519.PublicKey(expected)) {
			return nil, fmt.Errorf("relay Host identity does not match invitation")
		}
	}
	transcript := relayE2ETranscript{Version: 1, TunnelID: tunnelID, PeerID: peerID, GuestPublic: hello.PublicKey, GuestNonce: hello.Nonce, HostPublic: accept.PublicKey}
	encoded, _ := json.Marshal(transcript)
	signature, err := base64.RawURLEncoding.DecodeString(accept.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicBytes), encoded, signature) {
		return nil, fmt.Errorf("relay Host handshake signature is invalid")
	}
	hostPublic, err := decodeRelayX25519(accept.PublicKey)
	if err != nil {
		return nil, err
	}
	shared, err := private.ECDH(hostPublic)
	if err != nil {
		return nil, fmt.Errorf("relay e2e key agreement: %w", err)
	}
	return deriveRelayCipher(shared, encoded, false)
}

func decodeRelayX25519(value string) (*ecdh.PublicKey, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode relay e2e public key: %w", err)
	}
	public, err := ecdh.X25519().NewPublicKey(data)
	if err != nil {
		return nil, fmt.Errorf("invalid relay e2e public key: %w", err)
	}
	return public, nil
}

func deriveRelayCipher(shared, transcript []byte, host bool) (*relayCipher, error) {
	material, err := hkdf.Key(sha256.New, shared, transcript, "workground2-room-relay-v1", 64)
	if err != nil {
		return nil, err
	}
	guestToHost, err := newRelayAEAD(material[:32])
	if err != nil {
		return nil, err
	}
	hostToGuest, err := newRelayAEAD(material[32:])
	if err != nil {
		return nil, err
	}
	if host {
		return &relayCipher{send: hostToGuest, recv: guestToHost}, nil
	}
	return &relayCipher{send: guestToHost, recv: hostToGuest}, nil
}

func newRelayAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c *relayCipher) seal(header relayproto.Header, plaintext []byte) ([]byte, error) {
	return c.send.Seal(nil, relayNonce(header), plaintext, relayAAD(header)), nil
}

func (c *relayCipher) open(header relayproto.Header, ciphertext []byte) ([]byte, error) {
	return c.recv.Open(nil, relayNonce(header), ciphertext, relayAAD(header))
}

func relayNonce(header relayproto.Header) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint32(nonce[:4], uint32(header.Epoch))
	binary.BigEndian.PutUint64(nonce[4:], header.Sequence)
	return nonce
}

func relayAAD(header relayproto.Header) []byte {
	flags := append([]string(nil), header.Flags...)
	slices.Sort(flags)
	value := struct {
		Version  int      `json:"v"`
		Type     string   `json:"type"`
		TunnelID string   `json:"tunnelId"`
		PeerID   string   `json:"peerId"`
		StreamID uint32   `json:"streamId"`
		Epoch    uint64   `json:"epoch"`
		Sequence uint64   `json:"seq"`
		Flags    []string `json:"flags"`
	}{header.Version, header.Type, header.TunnelID, header.PeerID, header.StreamID, header.Epoch, header.Sequence, flags}
	data, _ := json.Marshal(value)
	return data
}

func appendUniqueRelayFlag(flags []string, value string) []string {
	for _, flag := range flags {
		if flag == value {
			return flags
		}
	}
	return append(flags, value)
}
