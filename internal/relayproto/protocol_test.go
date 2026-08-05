package relayproto

import (
	"testing"
	"time"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	wantHeader := Header{Version: Version, Type: "rpc.request", TunnelID: "tun_1", PeerID: "peer_1", StreamID: 7, Epoch: 3, Sequence: 42, Flags: []string{"encrypted"}}
	wantPayload := []byte("ciphertext")
	message, err := Encode(wantHeader, wantPayload)
	if err != nil {
		t.Fatal(err)
	}
	gotHeader, gotPayload, err := Decode(message)
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader.Type != wantHeader.Type || gotHeader.Sequence != wantHeader.Sequence || string(gotPayload) != string(wantPayload) {
		t.Fatalf("unexpected round trip: %#v %q", gotHeader, gotPayload)
	}
}

func TestEnvelopeRejectsInvalidLength(t *testing.T) {
	if _, _, err := Decode([]byte{0, 0, 32, 0, '{'}); err == nil {
		t.Fatal("expected invalid length error")
	}
}

func TestCapabilityVerifyAndRejectTamper(t *testing.T) {
	signer, err := NewSigner("relay-a", []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	token, claims, err := signer.Issue("tun_1", RoleHost, time.Minute, CapabilityLimits{MaxStreams: 3})
	if err != nil {
		t.Fatal(err)
	}
	got, err := signer.Verify(token, RoleHost, "tun_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Nonce != claims.Nonce || got.Limits.MaxStreams != 3 {
		t.Fatalf("unexpected claims: %#v", got)
	}
	replacement := byte('A')
	if token[len(token)-1] == replacement {
		replacement = 'B'
	}
	tampered := token[:len(token)-1] + string(replacement)
	if _, err := signer.Verify(tampered, RoleHost, "tun_1"); err == nil {
		t.Fatal("tampered capability was accepted")
	}
	if _, err := signer.Verify(token, RoleGuest, "tun_1"); err == nil {
		t.Fatal("wrong role was accepted")
	}
}

func TestCapabilityExpired(t *testing.T) {
	signer, _ := NewSigner("relay-a", []byte("01234567890123456789012345678901"))
	signer.now = func() time.Time { return time.Unix(1000, 0) }
	token, _, err := signer.Issue("tun_1", RoleGuest, time.Second, CapabilityLimits{})
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return time.Unix(1002, 0) }
	if _, err := signer.Verify(token, RoleGuest, "tun_1"); err == nil {
		t.Fatal("expired capability was accepted")
	}
}
