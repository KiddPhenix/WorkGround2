package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"workground2/internal/config"
	"workground2/internal/relayproto"
)

type ListCollaborationRoomsInput struct {
	Query    string   `json:"query,omitempty"`
	Tag      string   `json:"tag,omitempty"`
	RelayIDs []string `json:"relayIds,omitempty"`
	Cursor   string   `json:"cursor,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

type CollaborationRoomListing struct {
	PublicRoomID  string                    `json:"publicRoomId"`
	Room          string                    `json:"room"`
	Name          string                    `json:"name"`
	Description   string                    `json:"description,omitempty"`
	Tags          []string                  `json:"tags,omitempty"`
	RequiresToken bool                      `json:"requiresToken"`
	OnlineCount   int                       `json:"onlineCount,omitempty"`
	Capacity      int                       `json:"capacity,omitempty"`
	HostKey       string                    `json:"hostKey"`
	Routes        []CollaborationRouteInput `json:"routes"`
	JoinRef       string                    `json:"joinRef,omitempty"`
	ExpiresAt     string                    `json:"expiresAt,omitempty"`
}

type CollaborationRoomQueryResult struct {
	Rooms      []CollaborationRoomListing `json:"rooms"`
	NextCursor string                     `json:"nextCursor,omitempty"`
}

type CollaborationRelayProbeResult struct {
	RelayID      string   `json:"relayId"`
	Status       string   `json:"status"`
	RelayVersion int      `json:"relayVersion,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	LatencyMS    int64    `json:"latencyMs,omitempty"`
	LastError    string   `json:"lastError,omitempty"`
	Retryable    bool     `json:"retryable,omitempty"`
}

func (a *App) ProbeCollaborationRelay(relayID string) CollaborationRelayProbeResult {
	result := CollaborationRelayProbeResult{RelayID: strings.TrimSpace(relayID), Status: "failed"}
	relay, err := collaborationRelayByID(relayID)
	if err != nil {
		result.LastError = err.Error()
		return result
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(a.bootContext(), 15*time.Second)
	defer cancel()
	socket, hello, err := dialCollaborationRelay(ctx, relay)
	if err != nil {
		result.LastError, result.Retryable = err.Error(), collaborationErrorRetryable(err)
		return result
	}
	_ = socket.Close(context.Background())
	result.Status, result.RelayVersion, result.Capabilities = "connected", hello.Version, append([]string(nil), hello.Capabilities...)
	result.LatencyMS = time.Since(started).Milliseconds()
	return result
}

type relayRoomQueryPart struct {
	relay config.RelayConfig
	list  relayproto.RoomList
	err   error
}

// ListCollaborationRooms queries enabled Discovery Relays concurrently. Each
// advertisement is signature-checked before it is projected to the UI; one
// failed Relay does not hide healthy results from the others.
func (a *App) ListCollaborationRooms(input ListCollaborationRoomsInput) (CollaborationRoomQueryResult, error) {
	cfg, err := config.LoadForRoot(a.activeWorkspaceRoot())
	if err != nil {
		return CollaborationRoomQueryResult{}, err
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	selected := make(map[string]bool, len(input.RelayIDs))
	for _, relayID := range input.RelayIDs {
		selected[strings.ToLower(strings.TrimSpace(relayID))] = true
	}
	relays := make([]config.RelayConfig, 0, len(cfg.Collaboration.Relays))
	for _, relay := range cfg.Collaboration.Relays {
		if !relay.Enabled || !relay.Discovery || len(selected) > 0 && !selected[strings.ToLower(relay.ID)] {
			continue
		}
		relays = append(relays, relay)
	}
	if len(relays) == 0 {
		return CollaborationRoomQueryResult{Rooms: []CollaborationRoomListing{}}, nil
	}
	timeout := time.Duration(cfg.Collaboration.ConnectTimeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(a.bootContext(), timeout)
	defer cancel()
	parts := make(chan relayRoomQueryPart, len(relays))
	var wg sync.WaitGroup
	for _, relay := range relays {
		relay := relay
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, queryErr := queryRelayRooms(ctx, relay, input, limit)
			parts <- relayRoomQueryPart{relay: relay, list: list, err: queryErr}
		}()
	}
	wg.Wait()
	close(parts)
	byID := map[string]*CollaborationRoomListing{}
	joinPriority := map[string]int{}
	var failures []string
	for part := range parts {
		if part.err != nil {
			failures = append(failures, part.relay.ID+": "+part.err.Error())
			continue
		}
		for _, ad := range part.list.Rooms {
			if err := verifyRelayAdvertisement(ad); err != nil {
				failures = append(failures, part.relay.ID+": "+err.Error())
				continue
			}
			entry := byID[ad.PublicRoomID]
			if entry == nil {
				entry = &CollaborationRoomListing{
					PublicRoomID: ad.PublicRoomID, Room: ad.PublicRoomID, Name: ad.Name, Description: ad.Description,
					Tags: append([]string(nil), ad.Tags...), RequiresToken: ad.RequiresToken, OnlineCount: ad.OnlineCount,
					Capacity: ad.Capacity, HostKey: ad.HostPublicKey, ExpiresAt: ad.ExpiresAt.Format(time.RFC3339),
				}
				byID[ad.PublicRoomID] = entry
			}
			joinRef, tunnelID, refErr := fetchRelayJoinRef(ctx, part.relay, ad.PublicRoomID)
			if refErr != nil {
				failures = append(failures, part.relay.ID+": "+refErr.Error())
				continue
			}
			route := CollaborationRouteInput{ID: "relay:" + part.relay.ID, Kind: "relay", RelayID: part.relay.ID, URL: part.relay.URL, TunnelID: tunnelID, Priority: part.relay.Priority}
			entry.Routes = append(entry.Routes, route)
			if entry.JoinRef == "" || part.relay.Priority > joinPriority[ad.PublicRoomID] {
				entry.JoinRef = joinRef
				joinPriority[ad.PublicRoomID] = part.relay.Priority
			}
		}
	}
	rooms := make([]CollaborationRoomListing, 0, len(byID))
	for _, room := range byID {
		if len(room.Routes) > 0 {
			sort.SliceStable(room.Routes, func(i, j int) bool { return room.Routes[i].Priority > room.Routes[j].Priority })
			rooms = append(rooms, *room)
		}
	}
	sort.SliceStable(rooms, func(i, j int) bool {
		if rooms[i].OnlineCount != rooms[j].OnlineCount {
			return rooms[i].OnlineCount > rooms[j].OnlineCount
		}
		return strings.ToLower(rooms[i].Name) < strings.ToLower(rooms[j].Name)
	})
	if len(rooms) == 0 && len(failures) > 0 {
		return CollaborationRoomQueryResult{Rooms: []CollaborationRoomListing{}}, &collaborationTransportError{message: "Room discovery failed: " + strings.Join(failures, "; "), retryable: true}
	}
	return CollaborationRoomQueryResult{Rooms: rooms}, nil
}

func queryRelayRooms(ctx context.Context, relay config.RelayConfig, input ListCollaborationRoomsInput, limit int) (relayproto.RoomList, error) {
	base, err := relayHTTPURL(relay.URL)
	if err != nil {
		return relayproto.RoomList{}, err
	}
	query := base.Query()
	query.Set("limit", strconv.Itoa(limit))
	if value := strings.TrimSpace(input.Query); value != "" {
		query.Set("query", value)
	}
	if value := strings.TrimSpace(input.Tag); value != "" {
		query.Set("tag", value)
	}
	if value := strings.TrimSpace(input.Cursor); value != "" {
		query.Set("cursor", value)
	}
	base.Path = "/relay/v1/rooms"
	base.RawQuery = query.Encode()
	var result relayproto.RoomList
	err = relayHTTPJSON(ctx, relay, http.MethodGet, base.String(), &result)
	return result, err
}

func fetchRelayJoinRef(ctx context.Context, relay config.RelayConfig, publicRoomID string) (string, string, error) {
	base, err := relayHTTPURL(relay.URL)
	if err != nil {
		return "", "", err
	}
	base.Path = "/relay/v1/rooms/" + url.PathEscape(publicRoomID) + "/join-ref"
	var result relayproto.JoinRefResult
	if err := relayHTTPJSON(ctx, relay, http.MethodPost, base.String(), &result); err != nil {
		return "", "", err
	}
	return result.JoinRef, result.TunnelID, nil
}

func relayHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return nil, fmt.Errorf("invalid Relay scheme")
	}
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

func relayHTTPJSON(ctx context.Context, relay config.RelayConfig, method, endpoint string, output any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	if relay.AccessTokenEnv != "" {
		if token := strings.TrimSpace(os.Getenv(relay.AccessTokenEnv)); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if strings.EqualFold(req.URL.Scheme, "https") && strings.Contains(err.Error(), "server gave HTTP response to HTTPS client") {
			return fmt.Errorf("Relay %q is configured as wss:// but %s answered with plaintext HTTP; use ws:// for the local no-TLS Relay or configure TLS on the server: %w", relay.ID, req.URL.Host, err)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var relayErr relayproto.RelayError
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&relayErr) == nil && relayErr.Message != "" {
			return relayErr
		}
		return fmt.Errorf("Relay returned %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(output)
}

func verifyRelayAdvertisement(ad relayproto.Advertisement) error {
	public, err := decodeRelayPublicValue(ad.HostPublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("Room %q has an invalid Host key", ad.PublicRoomID)
	}
	if relayproto.HostKeyFingerprint(public) != ad.HostKeyFingerprint {
		return fmt.Errorf("Room %q Host fingerprint mismatch", ad.PublicRoomID)
	}
	signature, err := decodeRelayPublicValue(ad.Signature)
	if err != nil {
		return fmt.Errorf("Room %q has an invalid signature", ad.PublicRoomID)
	}
	signing, err := relayproto.AdvertisementSigningBytes(ad)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(public), signing, signature) {
		return fmt.Errorf("Room %q advertisement signature is invalid", ad.PublicRoomID)
	}
	if !ad.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("Room %q advertisement expired", ad.PublicRoomID)
	}
	return nil
}

func decodeRelayPublicValue(value string) ([]byte, error) {
	if data, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return data, nil
	}
	return base64.RawURLEncoding.DecodeString(value)
}
