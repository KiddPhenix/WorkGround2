package relayserver

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"workground2/internal/relayproto"
)

type adRecord struct {
	ad       relayproto.Advertisement
	tunnelID string
}

type discovery struct {
	mu        sync.RWMutex
	ads       map[string]adRecord
	usedJoins map[string]time.Time
	ttl       time.Duration
	signer    *relayproto.Signer
	registry  *registry
	joinTTL   time.Duration
}

func newDiscovery(ttl, joinTTL time.Duration, signer *relayproto.Signer, registry *registry) *discovery {
	return &discovery{ads: make(map[string]adRecord), usedJoins: make(map[string]time.Time), ttl: ttl, joinTTL: joinTTL, signer: signer, registry: registry}
}

func (d *discovery) upsert(tunnelID string, ad relayproto.Advertisement) error {
	if err := validateAdvertisement(ad); err != nil {
		return err
	}
	if !d.registry.active(tunnelID) {
		return errHostOffline
	}
	now := time.Now().UTC()
	if ad.ExpiresAt.Before(now) || ad.ExpiresAt.After(now.Add(d.ttl)) {
		return errors.New("advertisement expiry is outside the allowed TTL")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.ads[ad.PublicRoomID]; ok && old.ad.AdvertisementRevision > ad.AdvertisementRevision {
		return errors.New("advertisement revision is stale")
	}
	d.ads[ad.PublicRoomID] = adRecord{ad: ad, tunnelID: tunnelID}
	return nil
}

func (d *discovery) revoke(tunnelID, roomID string, revision uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.ads[roomID]; ok && old.tunnelID == tunnelID && revision >= old.ad.AdvertisementRevision {
		delete(d.ads, roomID)
	}
}

func (d *discovery) get(roomID string) (adRecord, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.ads[roomID]
	if !ok || time.Now().After(r.ad.ExpiresAt) || !d.registry.active(r.tunnelID) {
		if ok {
			delete(d.ads, roomID)
		}
		return adRecord{}, false
	}
	return r, true
}

func (d *discovery) list(query, tag, cursor string, limit int) relayproto.RoomList {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	rooms := make([]relayproto.Advertisement, 0, len(d.ads))
	for id, r := range d.ads {
		if now.After(r.ad.ExpiresAt) || !d.registry.active(r.tunnelID) {
			delete(d.ads, id)
			continue
		}
		if r.ad.Visibility != "public" {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(r.ad.Name+" "+r.ad.Description), strings.ToLower(query)) {
			continue
		}
		if tag != "" && !containsFold(r.ad.Tags, tag) {
			continue
		}
		rooms = append(rooms, r.ad)
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].PublicRoomID < rooms[j].PublicRoomID })
	offset := decodeCursor(cursor)
	if offset > len(rooms) {
		offset = len(rooms)
	}
	end := offset + limit
	if end > len(rooms) {
		end = len(rooms)
	}
	result := relayproto.RoomList{Rooms: append([]relayproto.Advertisement(nil), rooms[offset:end]...)}
	if end < len(rooms) {
		result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return result
}

func (d *discovery) issueJoin(roomID string) (relayproto.JoinRefResult, error) {
	r, ok := d.get(roomID)
	if !ok {
		return relayproto.JoinRefResult{}, errors.New("room is not active")
	}
	token, claims, err := d.signer.Issue(r.tunnelID, relayproto.RoleJoin, d.joinTTL, relayproto.CapabilityLimits{})
	if err != nil {
		return relayproto.JoinRefResult{}, err
	}
	return relayproto.JoinRefResult{JoinRef: token, TunnelID: r.tunnelID, ExpiresAt: claims.ExpiresAt}, nil
}

func (d *discovery) consumeJoin(token, tunnelID string) error {
	claims, err := d.signer.Verify(token, relayproto.RoleJoin, tunnelID)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for nonce, expires := range d.usedJoins {
		if now.After(expires) {
			delete(d.usedJoins, nonce)
		}
	}
	if _, used := d.usedJoins[claims.Nonce]; used {
		return errors.New("join reference already used")
	}
	d.usedJoins[claims.Nonce] = time.Unix(claims.ExpiresAt, 0)
	return nil
}

func validateAdvertisement(ad relayproto.Advertisement) error {
	if ad.PublicRoomID == "" || len(ad.PublicRoomID) > 128 || !utf8.ValidString(ad.PublicRoomID) {
		return errors.New("invalid public room id")
	}
	if ad.Name == "" || utf8.RuneCountInString(ad.Name) > 100 {
		return errors.New("invalid room name")
	}
	if ad.Visibility != "public" && ad.Visibility != "unlisted" {
		return errors.New("invalid visibility")
	}
	if utf8.RuneCountInString(ad.Description) > 500 || len(ad.Tags) > 16 || ad.Capacity < 0 || ad.OnlineCount < 0 {
		return errors.New("invalid advertisement fields")
	}
	if ad.AdvertisementRevision == 0 {
		return errors.New("advertisement revision is required")
	}
	pub, err := decodeBase64(ad.HostPublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid host public key")
	}
	if ad.HostKeyFingerprint != relayproto.HostKeyFingerprint(pub) {
		return errors.New("host key fingerprint mismatch")
	}
	sig, err := decodeBase64(ad.Signature)
	if err != nil {
		return errors.New("invalid advertisement signature")
	}
	b, _ := relayproto.AdvertisementSigningBytes(ad)
	if !ed25519.Verify(ed25519.PublicKey(pub), b, sig) {
		return errors.New("invalid advertisement signature")
	}
	return nil
}

func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
func containsFold(values []string, want string) bool {
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}
func decodeCursor(cursor string) int {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(string(b))
	if n < 0 {
		return 0
	}
	return n
}
