package bot

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workground2/internal/config"
	"workground2/internal/fileutil"
)

const (
	defaultPairingTTL        = time.Hour
	defaultPairingMaxPending = 3
	pairingAlphabet          = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

var errPairingCodeNotFound = errors.New("pairing code not found or expired")

type PairingConfig struct {
	Enabled               bool
	RequestTTL            time.Duration
	MaxPendingPerPlatform int
}

type PairingRequest struct {
	Code         string    `json:"code"`
	Platform     Platform  `json:"platform"`
	ConnectionID string    `json:"connection_id,omitempty"`
	Domain       string    `json:"domain,omitempty"`
	ChatType     ChatType  `json:"chat_type"`
	ChatID       string    `json:"chat_id"`
	UserID       string    `json:"user_id"`
	UserName     string    `json:"user_name,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type pairingFile struct {
	Requests []PairingRequest `json:"requests"`
}

// pairingMu serializes every load-modify-save of the pairing store, so
// concurrent adapter dispatch goroutines and CLI approve/reject cannot
// interleave read-modify-write cycles and drop each other's requests.
var pairingMu sync.Mutex

func NormalizePairingConfig(cfg PairingConfig) PairingConfig {
	if cfg.RequestTTL <= 0 {
		cfg.RequestTTL = defaultPairingTTL
	}
	if cfg.MaxPendingPerPlatform <= 0 {
		cfg.MaxPendingPerPlatform = defaultPairingMaxPending
	}
	return cfg
}

func PairingStorePath() string {
	dir := config.MemoryUserDir()
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "bot", "pairing.json")
}

func CreateOrRefreshPairingRequest(msg InboundMessage, cfg PairingConfig) (PairingRequest, bool, error) {
	cfg = NormalizePairingConfig(cfg)
	if !cfg.Enabled {
		return PairingRequest{}, false, errors.New("bot pairing is disabled")
	}
	if msg.ChatType != ChatDM && msg.ChatType != ChatDirect {
		return PairingRequest{}, false, errors.New("bot pairing only supports direct messages")
	}
	if strings.TrimSpace(msg.UserID) == "" || strings.TrimSpace(msg.ChatID) == "" {
		return PairingRequest{}, false, errors.New("bot pairing needs user_id and chat_id")
	}
	path := PairingStorePath()
	if path == "" {
		return PairingRequest{}, false, errors.New("WorkGround2 user state directory is unavailable")
	}
	pairingMu.Lock()
	defer pairingMu.Unlock()
	store, err := loadPairingFile(path)
	if err != nil {
		return PairingRequest{}, false, err
	}
	now := time.Now().UTC()
	store.Requests = pruneExpiredPairingRequests(store.Requests, now)
	for _, req := range store.Requests {
		if pairingRequestMatches(req, msg) {
			return req, false, savePairingFile(path, store)
		}
	}
	pendingForPlatform := 0
	for _, req := range store.Requests {
		if req.Platform == msg.Platform && strings.TrimSpace(req.ConnectionID) == strings.TrimSpace(msg.ConnectionID) {
			pendingForPlatform++
		}
	}
	if pendingForPlatform >= cfg.MaxPendingPerPlatform {
		return PairingRequest{}, false, fmt.Errorf("too many pending pairing requests for %s", msg.Platform)
	}
	code, err := newPairingCode()
	if err != nil {
		return PairingRequest{}, false, err
	}
	req := PairingRequest{
		Code:         code,
		Platform:     msg.Platform,
		ConnectionID: strings.TrimSpace(msg.ConnectionID),
		Domain:       strings.TrimSpace(msg.Domain),
		ChatType:     msg.ChatType,
		ChatID:       strings.TrimSpace(msg.ChatID),
		UserID:       strings.TrimSpace(msg.UserID),
		UserName:     strings.TrimSpace(msg.UserName),
		CreatedAt:    now,
		ExpiresAt:    now.Add(cfg.RequestTTL),
	}
	store.Requests = append(store.Requests, req)
	return req, true, savePairingFile(path, store)
}

func ListPairingRequests() ([]PairingRequest, error) {
	path := PairingStorePath()
	if path == "" {
		return nil, errors.New("WorkGround2 user state directory is unavailable")
	}
	pairingMu.Lock()
	defer pairingMu.Unlock()
	store, err := loadPairingFile(path)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	next := pruneExpiredPairingRequests(store.Requests, now)
	if len(next) != len(store.Requests) {
		store.Requests = next
		if err := savePairingFile(path, store); err != nil {
			return nil, err
		}
	}
	return next, nil
}

func ApprovePairingCode(code string) (PairingRequest, error) {
	// 先定位请求但不删除：授权必须先持久化成功，配对请求才允许消失，
	// 这样配置保存失败时用户仍可用同一码重试。
	req, err := findPairingCode(code)
	if err != nil {
		return PairingRequest{}, err
	}
	userPath := config.UserConfigPath()
	if userPath == "" {
		return PairingRequest{}, errors.New("WorkGround2 user config path is unavailable")
	}
	unlock := config.LockUserConfigEdits()
	defer unlock()
	cfg := config.LoadForEdit(userPath)
	if approvePairingForConnectionAccess(&cfg.Bot, req) {
		// 连接级授权已写入 cfg.Bot.Connections / cfg.Bot.QQ.Access。
	} else {
		cfg.Bot.Allowlist.Enabled = true
		switch req.Platform {
		case PlatformQQ:
			cfg.Bot.Allowlist.QQUsers, _ = appendUnique(cfg.Bot.Allowlist.QQUsers, req.UserID)
		case PlatformFeishu:
			cfg.Bot.Allowlist.FeishuUsers, _ = appendUnique(cfg.Bot.Allowlist.FeishuUsers, req.UserID)
		case PlatformWeixin:
			cfg.Bot.Allowlist.WeixinUsers, _ = appendUnique(cfg.Bot.Allowlist.WeixinUsers, req.UserID)
		}
		if allowlistAdminCount(cfg.Bot.Allowlist) == 0 {
			switch req.Platform {
			case PlatformQQ:
				cfg.Bot.Allowlist.QQAdmins, _ = appendUnique(cfg.Bot.Allowlist.QQAdmins, req.UserID)
				cfg.Bot.Allowlist.QQApprovers, _ = appendUnique(cfg.Bot.Allowlist.QQApprovers, req.UserID)
			case PlatformFeishu:
				cfg.Bot.Allowlist.FeishuAdmins, _ = appendUnique(cfg.Bot.Allowlist.FeishuAdmins, req.UserID)
				cfg.Bot.Allowlist.FeishuApprovers, _ = appendUnique(cfg.Bot.Allowlist.FeishuApprovers, req.UserID)
			case PlatformWeixin:
				cfg.Bot.Allowlist.WeixinAdmins, _ = appendUnique(cfg.Bot.Allowlist.WeixinAdmins, req.UserID)
				cfg.Bot.Allowlist.WeixinApprovers, _ = appendUnique(cfg.Bot.Allowlist.WeixinApprovers, req.UserID)
			}
		}
	}
	if err := cfg.SaveTo(userPath); err != nil {
		// 请求未删除，保留原码可安全重试。
		return PairingRequest{}, err
	}
	// 授权已持久化，最后删除配对请求。删除失败且请求仍在时返回错误
	// （可重试，access 追加幂等）；请求已不存在（并发批准或恰好过期）时
	// 授权已经生效，视为成功。
	if _, err := removePairingCode(code); err != nil {
		if _, findErr := findPairingCode(code); errors.Is(findErr, errPairingCodeNotFound) {
			return req, nil
		} else if findErr != nil {
			return PairingRequest{}, fmt.Errorf("pairing approved and saved, but the pending request state could not be verified (re-approving the same code is safe): %w", findErr)
		}
		return PairingRequest{}, fmt.Errorf("pairing approved and saved, but removing the pending request failed (re-approving the same code is safe): %w", err)
	}
	return req, nil
}

func approvePairingForConnectionAccess(botCfg *config.BotConfig, req PairingRequest) bool {
	if botCfg == nil {
		return false
	}
	connectionID := strings.TrimSpace(req.ConnectionID)
	if connectionID != "" {
		for i := range botCfg.Connections {
			if pairingConnectionMatches(botCfg.Connections[i], connectionID) {
				approvePairingAccess(&botCfg.Connections[i].Access, req.UserID)
				return true
			}
		}
	}
	if req.Platform == PlatformQQ && (connectionID == "" || connectionID == string(PlatformQQ)) {
		approvePairingAccess(&botCfg.QQ.Access, req.UserID)
		return true
	}
	return false
}

func approvePairingAccess(access *config.BotAccessConfig, userID string) {
	if access == nil {
		return
	}
	wasEmpty := !access.AllowAll &&
		len(access.Users) == 0 &&
		len(access.Groups) == 0 &&
		len(access.Approvers) == 0 &&
		len(access.Admins) == 0
	access.Enabled = true
	access.Users, _ = appendUnique(access.Users, userID)
	if wasEmpty {
		access.Admins, _ = appendUnique(access.Admins, userID)
		access.Approvers, _ = appendUnique(access.Approvers, userID)
	}
}

func pairingConnectionMatches(conn config.BotConnectionConfig, connectionID string) bool {
	connectionID = strings.TrimSpace(connectionID)
	if connectionID == "" {
		return false
	}
	if strings.TrimSpace(conn.ID) == connectionID {
		return true
	}
	return pairingConnectionRuntimeID(conn) == connectionID
}

func pairingConnectionRuntimeID(conn config.BotConnectionConfig) string {
	if id := strings.TrimSpace(conn.ID); id != "" {
		return id
	}
	provider := strings.TrimSpace(conn.Provider)
	domain := strings.TrimSpace(conn.Domain)
	if provider == "" {
		return ""
	}
	if domain == "" {
		return provider
	}
	return provider + "-" + domain
}

func RejectPairingCode(code string) (PairingRequest, error) {
	return removePairingCode(code)
}

// findPairingCode locates a live pairing request without modifying the store.
// It is the read side of approve: the request must survive until the granted
// access is durably saved, so a failed config write stays retryable.
func findPairingCode(code string) (PairingRequest, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return PairingRequest{}, errors.New("pairing code is required")
	}
	path := PairingStorePath()
	if path == "" {
		return PairingRequest{}, errors.New("WorkGround2 user state directory is unavailable")
	}
	pairingMu.Lock()
	defer pairingMu.Unlock()
	store, err := loadPairingFile(path)
	if err != nil {
		return PairingRequest{}, err
	}
	now := time.Now().UTC()
	for _, req := range pruneExpiredPairingRequests(store.Requests, now) {
		if strings.EqualFold(req.Code, code) {
			return req, nil
		}
	}
	return PairingRequest{}, fmt.Errorf("%w: %s", errPairingCodeNotFound, code)
}

func removePairingCode(code string) (PairingRequest, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return PairingRequest{}, errors.New("pairing code is required")
	}
	path := PairingStorePath()
	if path == "" {
		return PairingRequest{}, errors.New("WorkGround2 user state directory is unavailable")
	}
	pairingMu.Lock()
	defer pairingMu.Unlock()
	store, err := loadPairingFile(path)
	if err != nil {
		return PairingRequest{}, err
	}
	now := time.Now().UTC()
	next := pruneExpiredPairingRequests(store.Requests, now)
	var found PairingRequest
	kept := next[:0]
	for _, req := range next {
		if strings.EqualFold(req.Code, code) {
			found = req
			continue
		}
		kept = append(kept, req)
	}
	if found.Code == "" {
		return PairingRequest{}, fmt.Errorf("%w: %s", errPairingCodeNotFound, code)
	}
	store.Requests = kept
	return found, savePairingFile(path, store)
}

func loadPairingFile(path string) (pairingFile, error) {
	var store pairingFile
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return store, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return pairingFile{}, err
	}
	return store, nil
}

func savePairingFile(path string, store pairingFile) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(path, append(data, '\n'), 0o600)
}

func pruneExpiredPairingRequests(reqs []PairingRequest, now time.Time) []PairingRequest {
	out := reqs[:0]
	for _, req := range reqs {
		if req.ExpiresAt.IsZero() || now.Before(req.ExpiresAt) {
			out = append(out, req)
		}
	}
	return out
}

func pairingRequestMatches(req PairingRequest, msg InboundMessage) bool {
	return req.Platform == msg.Platform &&
		strings.TrimSpace(req.ConnectionID) == strings.TrimSpace(msg.ConnectionID) &&
		strings.TrimSpace(req.ChatID) == strings.TrimSpace(msg.ChatID) &&
		strings.TrimSpace(req.UserID) == strings.TrimSpace(msg.UserID)
}

func newPairingCode() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(pairingAlphabet)))
	for i := 0; i < 8; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(pairingAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func allowlistAdminCount(a config.BotAllowlist) int {
	return len(a.QQAdmins) + len(a.FeishuAdmins) + len(a.WeixinAdmins)
}

func appendUnique(values []string, next string) ([]string, bool) {
	next = strings.TrimSpace(next)
	if next == "" {
		return values, false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == next {
			return values, false
		}
	}
	return append(values, next), true
}
