package collab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fileTicketTTL       = 5 * time.Minute
	maxManifestBytes    = 32 << 20
	maxTransferHostList = 16
)

type RegisterFileOriginInput struct {
	Port   int      `json:"port"`
	Secret string   `json:"secret"`
	Hosts  []string `json:"hosts,omitempty"`
}

type FileTransferTicket struct {
	File       FileOffer `json:"file"`
	DirectURLs []string  `json:"directUrls,omitempty"`
	ProxyPath  string    `json:"proxyPath"`
	Ticket     string    `json:"ticket"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type fileOrigin struct {
	memberID string
	session  string
	host     string
	port     int
	hosts    []string
	secret   string
}

type fileTicketClaims struct {
	Room       string `json:"room"`
	FileID     string `json:"fileId"`
	OwnerID    string `json:"ownerId"`
	ReceiverID string `json:"receiverId"`
	Expires    int64  `json:"expires"`
}

type fileTransferRegistry struct {
	service *Service
	mu      sync.RWMutex
	origins map[string]fileOrigin
	client  *http.Client
}

func newFileTransferRegistry(service *Service) *fileTransferRegistry {
	return &fileTransferRegistry{
		service: service,
		origins: map[string]fileOrigin{},
		client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("file origin redirects are disabled")
		}},
	}
}

func (f *fileTransferRegistry) serve(w http.ResponseWriter, r *http.Request, room string, parts []string) {
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, fail(CodeNotFound, "file endpoint does not exist"))
		return
	}
	fileID, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, fail(CodeInvalid, "invalid file path"))
		return
	}
	switch {
	case len(parts) == 2 && parts[1] == "origin":
		f.register(w, r, room, fileID)
	case len(parts) == 2 && parts[1] == "ticket":
		f.ticket(w, r, room, fileID)
	case len(parts) == 2 && parts[1] == "manifest":
		f.proxy(w, r, room, fileID, "manifest", -1)
	case len(parts) == 3 && parts[1] == "chunks":
		index, err := strconv.Atoi(parts[2])
		if err != nil || index < 0 {
			writeError(w, fail(CodeInvalid, "invalid chunk index"))
			return
		}
		f.proxy(w, r, room, fileID, "chunk", index)
	default:
		writeError(w, fail(CodeNotFound, "file endpoint does not exist"))
	}
}

func (f *fileTransferRegistry) register(w http.ResponseWriter, r *http.Request, room, fileID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	memberID, err := f.service.Authenticate(r.Context(), room, sessionFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	file, err := f.service.File(r.Context(), room, fileID)
	if err != nil {
		writeError(w, err)
		return
	}
	if file.OwnerID != memberID {
		writeError(w, fail(CodeForbidden, "only the file owner can register its origin"))
		return
	}
	var input RegisterFileOriginInput
	if err := decodeBody(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if input.Port < 1 || input.Port > 65535 || len(input.Secret) < 32 || len(input.Secret) > 512 || len(input.Hosts) > maxTransferHostList {
		writeError(w, fail(CodeInvalid, "invalid file origin"))
		return
	}
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(strings.Trim(remoteHost, "[]")) == nil {
		writeError(w, fail(CodeInvalid, "file origin address is unavailable"))
		return
	}
	hostValues := []string{remoteHost}
	if remoteIP := net.ParseIP(strings.Trim(remoteHost, "[]")); remoteIP != nil && remoteIP.IsLoopback() {
		hostValues = append(append([]string(nil), input.Hosts...), remoteHost)
	}
	hosts := uniqueTransferHosts(hostValues)
	origin := fileOrigin{memberID: memberID, session: sessionFrom(r), host: remoteHost, port: input.Port, hosts: hosts, secret: input.Secret}
	f.mu.Lock()
	f.origins[fileOriginKey(room, fileID)] = origin
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"registered": true})
}

func (f *fileTransferRegistry) ticket(w http.ResponseWriter, r *http.Request, room, fileID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	receiverID, err := f.service.Authenticate(r.Context(), room, sessionFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	file, origin, err := f.activeOrigin(r.Context(), room, fileID)
	if err != nil {
		writeError(w, err)
		return
	}
	expiresAt := time.Now().UTC().Add(fileTicketTTL)
	ticket, err := signFileTicket(origin.secret, fileTicketClaims{Room: room, FileID: fileID, OwnerID: file.OwnerID, ReceiverID: receiverID, Expires: expiresAt.Unix()})
	if err != nil {
		writeError(w, err)
		return
	}
	paths := make([]string, 0, len(origin.hosts))
	for _, host := range origin.hosts {
		paths = append(paths, "http://"+net.JoinHostPort(host, strconv.Itoa(origin.port))+fileOriginPath(room, fileID))
	}
	writeJSON(w, http.StatusOK, FileTransferTicket{File: file, DirectURLs: paths, ProxyPath: "/collab/v1/rooms/" + url.PathEscape(room) + "/files/" + url.PathEscape(fileID), Ticket: ticket, ExpiresAt: expiresAt})
}

func (f *fileTransferRegistry) proxy(w http.ResponseWriter, r *http.Request, room, fileID, kind string, index int) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	receiverID, err := f.service.Authenticate(r.Context(), room, sessionFrom(r))
	if err != nil {
		writeError(w, err)
		return
	}
	file, origin, err := f.activeOrigin(r.Context(), room, fileID)
	if err != nil {
		writeError(w, err)
		return
	}
	if kind == "chunk" && index >= file.ChunkCount {
		writeError(w, fail(CodeInvalid, "chunk index is out of range"))
		return
	}
	expiresAt := time.Now().UTC().Add(fileTicketTTL)
	ticket, err := signFileTicket(origin.secret, fileTicketClaims{Room: room, FileID: fileID, OwnerID: file.OwnerID, ReceiverID: receiverID, Expires: expiresAt.Unix()})
	if err != nil {
		writeError(w, err)
		return
	}
	path := fileOriginPath(room, fileID) + "/" + kind
	if kind == "chunk" {
		path += "s/" + strconv.Itoa(index)
	}
	endpoint := "http://" + net.JoinHostPort(origin.host, strconv.Itoa(origin.port)) + path + "?ticket=" + url.QueryEscape(ticket)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	resp, err := f.client.Do(req)
	if err != nil {
		writeError(w, &Error{Code: CodeUnavailable, Message: "file owner is unavailable: " + err.Error(), Retryable: true, Cause: err})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		writeError(w, &Error{Code: CodeUnavailable, Message: strings.TrimSpace(string(body)), Retryable: resp.StatusCode >= 500 || resp.StatusCode == http.StatusConflict})
		return
	}
	limit := int64(maxManifestBytes)
	if kind == "chunk" {
		limit = file.ChunkSize + 1
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, limit))
}

func (f *fileTransferRegistry) activeOrigin(ctx context.Context, room, fileID string) (FileOffer, fileOrigin, error) {
	file, err := f.service.File(ctx, room, fileID)
	if err != nil {
		return FileOffer{}, fileOrigin{}, err
	}
	f.mu.RLock()
	origin, ok := f.origins[fileOriginKey(room, fileID)]
	f.mu.RUnlock()
	if !ok || origin.memberID != file.OwnerID {
		return FileOffer{}, fileOrigin{}, &Error{Code: CodeUnavailable, Message: "file owner is offline or has not restored this share", Retryable: true}
	}
	memberID, err := f.service.Authenticate(ctx, room, origin.session)
	if err != nil || memberID != file.OwnerID {
		f.mu.Lock()
		delete(f.origins, fileOriginKey(room, fileID))
		f.mu.Unlock()
		return FileOffer{}, fileOrigin{}, &Error{Code: CodeUnavailable, Message: "file owner is offline", Retryable: true}
	}
	return file, origin, nil
}

func signFileTicket(secret string, claims fileTicketClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func VerifyFileTicket(secret, ticket, room, fileID, ownerID string, now time.Time) error {
	parts := strings.Split(ticket, ".")
	if len(parts) != 2 {
		return fmt.Errorf("invalid transfer ticket")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("invalid transfer ticket")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("invalid transfer ticket")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return fmt.Errorf("invalid transfer ticket")
	}
	var claims fileTicketClaims
	if json.Unmarshal(payload, &claims) != nil || claims.Room != room || claims.FileID != fileID || claims.OwnerID != ownerID || claims.ReceiverID == "" || now.Unix() > claims.Expires {
		return fmt.Errorf("expired or mismatched transfer ticket")
	}
	return nil
}

func fileOriginKey(room, fileID string) string { return room + "\x00" + fileID }

func fileOriginPath(room, fileID string) string {
	return "/collab-file/v1/rooms/" + url.PathEscape(room) + "/files/" + url.PathEscape(fileID)
}

func uniqueTransferHosts(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	loopback := make([]string, 0, 1)
	for _, value := range values {
		value = strings.Trim(strings.TrimSpace(value), "[]")
		ip := net.ParseIP(strings.Split(value, "%")[0])
		if ip == nil || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || seen[value] {
			continue
		}
		seen[value] = true
		if ip.IsLoopback() {
			loopback = append(loopback, value)
		} else {
			result = append(result, value)
		}
		if len(result)+len(loopback) == maxTransferHostList {
			break
		}
	}
	return append(result, loopback...)
}
