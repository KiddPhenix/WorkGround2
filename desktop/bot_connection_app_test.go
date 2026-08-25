package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/bot"
	"workground2/internal/config"

	"workground2/desktop/internal/memhttp"
)

func TestNormalizeBotInstallTarget(t *testing.T) {
	cases := []struct {
		provider     string
		domain       string
		wantProvider string
		wantDomain   string
	}{
		{provider: "lark", wantProvider: "feishu", wantDomain: "lark"},
		{provider: "feishu", domain: "lark", wantProvider: "feishu", wantDomain: "lark"},
		{provider: "wechat", wantProvider: "weixin", wantDomain: "weixin"},
		{provider: "weixin", domain: "anything", wantProvider: "weixin", wantDomain: "weixin"},
		{provider: "unknown", domain: "unknown", wantProvider: "feishu", wantDomain: "feishu"},
	}
	for _, tc := range cases {
		gotProvider, gotDomain := normalizeBotInstallTarget(tc.provider, tc.domain)
		if gotProvider != tc.wantProvider || gotDomain != tc.wantDomain {
			t.Fatalf("normalizeBotInstallTarget(%q,%q) = %q,%q; want %q,%q", tc.provider, tc.domain, gotProvider, gotDomain, tc.wantProvider, tc.wantDomain)
		}
	}
}

func TestLarkInstallFollowsSDKDomainSwitchAndStoresSecret(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Cleanup(func() { _ = os.Unsetenv("LARK_BOT_APP_SECRET") })
	pollCount := 0
	var beginHost string
	var pollHosts []string
	var actions []string
	withRewrittenHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/v1/app/registration" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch r.Form.Get("action") {
		case "begin":
			beginHost = r.Header.Get("X-Test-Original-Host")
			actions = append(actions, "begin")
			if r.Form.Get("archetype") != "PersonalAgent" || r.Form.Get("auth_method") != "client_secret" {
				http.Error(w, "wrong begin form", http.StatusBadRequest)
				return
			}
			writeJSON(t, w, map[string]any{
				"device_code":               "dev-lark",
				"verification_uri_complete": "https://open.feishu.cn/page/launcher?user_code=CODE",
				"user_code":                 "CODE",
				"interval":                  3,
				"expire_in":                 300,
			})
		case "poll":
			pollHosts = append(pollHosts, r.Header.Get("X-Test-Original-Host"))
			actions = append(actions, "poll")
			if r.Form.Get("device_code") != "dev-lark" {
				http.Error(w, "wrong device code", http.StatusBadRequest)
				return
			}
			pollCount++
			if pollCount == 1 {
				writeJSON(t, w, map[string]any{"user_info": map[string]any{"tenant_brand": "lark"}})
				return
			}
			writeJSON(t, w, map[string]any{
				"client_id":     "cli-1",
				"client_secret": "secret-1",
				"user_info":     map[string]any{"tenant_brand": "lark", "open_id": "ou-installer"},
			})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))

	app := NewApp()
	start, err := app.StartBotConnectionInstall("lark", "")
	if err != nil {
		t.Fatalf("StartBotConnectionInstall: %v", err)
	}
	if !start.OK || start.Domain != "lark" || start.InstallID == "" || start.URL == "" || start.DeviceCode != "dev-lark" {
		t.Fatalf("start result = %+v, want ok lark-capable QR result", start)
	}
	qrURL, err := url.Parse(start.URL)
	if err != nil {
		t.Fatalf("start URL = %q, want valid QR URL: %v", start.URL, err)
	}
	query := qrURL.Query()
	if query.Get("user_code") != "CODE" || query.Get("from") != "sdk" || query.Get("tp") != "sdk" || query.Get("source") != "go-sdk" {
		t.Fatalf("start URL query = %v, want SDK registration QR metadata with user_code", query)
	}
	if qrURL.Host != "open.feishu.cn" {
		t.Fatalf("start URL host = %q, want SDK Feishu launcher host", qrURL.Host)
	}

	pending, err := app.PollBotConnectionInstall(start.InstallID)
	if err != nil {
		t.Fatalf("PollBotConnectionInstall pending: %v", err)
	}
	if pending.Done || pending.Status != "pending" {
		t.Fatalf("pending poll result = %+v, want pending domain switch", pending)
	}
	poll, err := app.PollBotConnectionInstall(start.InstallID)
	if err != nil {
		t.Fatalf("PollBotConnectionInstall: %v", err)
	}
	if !poll.Done {
		t.Fatalf("poll result = %+v, want done", poll)
	}
	if poll.Connection.Provider != "feishu" || poll.Connection.Domain != "lark" || poll.Connection.ID != "feishu-lark" {
		t.Fatalf("connection = %+v, want feishu-lark from tenant_brand", poll.Connection)
	}
	if beginHost != "accounts.feishu.cn" {
		t.Fatalf("begin host = %q, want SDK Feishu accounts host", beginHost)
	}
	if got := strings.Join(pollHosts, ","); got != "accounts.feishu.cn,accounts.larksuite.com" {
		t.Fatalf("poll hosts = %q, want Feishu poll then Lark poll", got)
	}
	if got := strings.Join(actions, ","); got != "begin,poll,poll" {
		t.Fatalf("registration actions = %q, want SDK begin, domain switch, final poll", got)
	}
	if poll.Connection.WorkspaceRoot != "" {
		t.Fatalf("connection workspaceRoot = %q, want empty global default", poll.Connection.WorkspaceRoot)
	}
	if poll.Connection.Credential.AppID != "cli-1" || poll.Connection.Credential.AppSecretEnv != "LARK_BOT_APP_SECRET" || !poll.Connection.Credential.SecretSet {
		t.Fatalf("credential = %+v, want stored Lark secret", poll.Connection.Credential)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if !cfg.Bot.Enabled || !cfg.Bot.Feishu.Enabled || cfg.Bot.Feishu.Domain != "lark" || cfg.Bot.Feishu.Mode != "websocket" || !cfg.Bot.Feishu.RequireMention {
		t.Fatalf("saved feishu config = %+v, want enabled websocket lark with mention gating", cfg.Bot.Feishu)
	}
	if len(cfg.Bot.Allowlist.FeishuUsers) != 1 || cfg.Bot.Allowlist.FeishuUsers[0] != "ou-installer" {
		t.Fatalf("feishu allowlist = %+v, want installer open_id", cfg.Bot.Allowlist.FeishuUsers)
	}
	if err := os.Unsetenv("LARK_BOT_APP_SECRET"); err != nil {
		t.Fatalf("unset lark secret env: %v", err)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := os.Getenv("LARK_BOT_APP_SECRET"); got != "secret-1" {
		t.Fatalf("reloaded LARK_BOT_APP_SECRET = %q, want persisted secret", got)
	}
	if len(reloaded.Bot.Connections) != 1 || !botConnectionView(reloaded.Bot.Connections[0]).Credential.SecretSet {
		t.Fatalf("reloaded connections = %+v, want secret to survive restart", reloaded.Bot.Connections)
	}
}

func TestFeishuInstallSwitchesToLarkDomainWhenTenantBrandIsLark(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Cleanup(func() { _ = os.Unsetenv("LARK_BOT_APP_SECRET") })
	pollCount := 0
	var beginHost string
	var pollHosts []string
	withRewrittenHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/v1/app/registration" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch r.Form.Get("action") {
		case "begin":
			beginHost = r.Header.Get("X-Test-Original-Host")
			writeJSON(t, w, map[string]any{
				"device_code":               "dev-feishu",
				"verification_uri_complete": "https://accounts.example/verify?user_code=CODE",
				"user_code":                 "CODE",
				"interval":                  3,
				"expire_in":                 300,
			})
		case "poll":
			pollHosts = append(pollHosts, r.Header.Get("X-Test-Original-Host"))
			if r.Form.Get("device_code") != "dev-feishu" {
				http.Error(w, "wrong device code", http.StatusBadRequest)
				return
			}
			pollCount++
			if pollCount == 1 {
				writeJSON(t, w, map[string]any{"user_info": map[string]any{"tenant_brand": "lark"}})
				return
			}
			writeJSON(t, w, map[string]any{
				"client_id":     "cli-lark",
				"client_secret": "secret-lark",
				"user_info":     map[string]any{"tenant_brand": "lark", "open_id": "ou-lark-installer"},
			})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))

	app := NewApp()
	start, err := app.StartBotConnectionInstall("feishu", "")
	if err != nil {
		t.Fatalf("StartBotConnectionInstall: %v", err)
	}
	if !start.OK || start.Domain != "feishu" || start.DeviceCode != "dev-feishu" {
		t.Fatalf("start result = %+v, want Feishu QR result", start)
	}
	pending, err := app.PollBotConnectionInstall(start.InstallID)
	if err != nil {
		t.Fatalf("PollBotConnectionInstall pending: %v", err)
	}
	if pending.Done || pending.Status != "pending" {
		t.Fatalf("pending poll result = %+v, want pending domain switch", pending)
	}
	poll, err := app.PollBotConnectionInstall(start.InstallID)
	if err != nil {
		t.Fatalf("PollBotConnectionInstall: %v", err)
	}
	if !poll.Done || poll.Connection.Domain != "lark" || poll.Connection.Credential.AppSecretEnv != "LARK_BOT_APP_SECRET" {
		t.Fatalf("poll result = %+v, want stored Lark connection after domain switch", poll)
	}
	if beginHost != "accounts.feishu.cn" {
		t.Fatalf("begin host = %q, want Feishu accounts host", beginHost)
	}
	if got := strings.Join(pollHosts, ","); got != "accounts.feishu.cn,accounts.larksuite.com" {
		t.Fatalf("poll hosts = %q, want Feishu poll then Lark poll", got)
	}
}

func TestFeishuInstallStoresFeishuSecretAndSurvivesReload(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Cleanup(func() { _ = os.Unsetenv("FEISHU_BOT_APP_SECRET") })
	var hosts []string
	withRewrittenHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/v1/app/registration" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		hosts = append(hosts, r.Form.Get("action")+":"+r.Header.Get("X-Test-Original-Host"))
		switch r.Form.Get("action") {
		case "begin":
			writeJSON(t, w, map[string]any{
				"device_code":               "dev-feishu",
				"verification_uri_complete": "https://accounts.example/verify?user_code=CODE",
				"user_code":                 "CODE",
				"interval":                  3,
				"expire_in":                 300,
			})
		case "poll":
			writeJSON(t, w, map[string]any{
				"client_id":     "cli-feishu",
				"client_secret": "secret-feishu",
				"user_info":     map[string]any{"tenant_brand": "feishu", "open_id": "ou-feishu-installer"},
			})
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))

	app := NewApp()
	start, err := app.StartBotConnectionInstall("feishu", "")
	if err != nil {
		t.Fatalf("StartBotConnectionInstall: %v", err)
	}
	if !start.OK || start.Domain != "feishu" || start.InstallID == "" {
		t.Fatalf("start result = %+v, want ok Feishu QR result", start)
	}
	poll, err := app.PollBotConnectionInstall(start.InstallID)
	if err != nil {
		t.Fatalf("PollBotConnectionInstall: %v", err)
	}
	if !poll.Done {
		t.Fatalf("poll result = %+v, want done", poll)
	}
	if poll.Connection.Provider != "feishu" || poll.Connection.Domain != "feishu" || poll.Connection.ID != "feishu-feishu" {
		t.Fatalf("connection = %+v, want feishu-feishu", poll.Connection)
	}
	if poll.Connection.Credential.AppID != "cli-feishu" || poll.Connection.Credential.AppSecretEnv != "FEISHU_BOT_APP_SECRET" || !poll.Connection.Credential.SecretSet {
		t.Fatalf("credential = %+v, want stored Feishu secret", poll.Connection.Credential)
	}
	if got := strings.Join(hosts, ","); got != "begin:accounts.feishu.cn,poll:accounts.feishu.cn" {
		t.Fatalf("registration hosts = %q, want Feishu begin and poll", got)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if !cfg.Bot.Enabled || !cfg.Bot.Feishu.Enabled || cfg.Bot.Feishu.Domain != "feishu" || cfg.Bot.Feishu.AppID != "cli-feishu" {
		t.Fatalf("saved feishu config = %+v, want enabled Feishu websocket config", cfg.Bot.Feishu)
	}
	if len(cfg.Bot.Allowlist.FeishuUsers) != 1 || cfg.Bot.Allowlist.FeishuUsers[0] != "ou-feishu-installer" {
		t.Fatalf("feishu allowlist = %+v, want installer open_id", cfg.Bot.Allowlist.FeishuUsers)
	}
	if err := os.Unsetenv("FEISHU_BOT_APP_SECRET"); err != nil {
		t.Fatalf("unset feishu secret env: %v", err)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := os.Getenv("FEISHU_BOT_APP_SECRET"); got != "secret-feishu" {
		t.Fatalf("reloaded FEISHU_BOT_APP_SECRET = %q, want persisted secret", got)
	}
	if len(reloaded.Bot.Connections) != 1 || !botConnectionView(reloaded.Bot.Connections[0]).Credential.SecretSet {
		t.Fatalf("reloaded connections = %+v, want secret to survive restart", reloaded.Bot.Connections)
	}
}

func TestWeixinInstallStoresSavedAccountAndConnection(t *testing.T) {
	isolateDesktopUserDirs(t)
	withRewrittenHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			if r.URL.Query().Get("bot_type") != "3" {
				http.Error(w, "missing bot type", http.StatusBadRequest)
				return
			}
			writeJSON(t, w, map[string]any{
				"qrcode":             "qr-weixin",
				"qrcode_img_content": "data:image/png;base64,abc",
			})
		case "/ilink/bot/get_qrcode_status":
			if r.URL.Query().Get("qrcode") != "qr-weixin" {
				http.Error(w, "wrong qr", http.StatusBadRequest)
				return
			}
			writeJSON(t, w, map[string]any{
				"status":        "confirmed",
				"ilink_bot_id":  "weixin-account",
				"bot_token":     "token-1",
				"ilink_user_id": "user-1",
				"baseurl":       "https://ilinkai.weixin.qq.com",
			})
		default:
			http.NotFound(w, r)
		}
	}))

	app := NewApp()
	start, err := app.StartBotConnectionInstall("weixin", "")
	if err != nil {
		t.Fatalf("StartBotConnectionInstall: %v", err)
	}
	if !start.OK || start.Provider != "weixin" || start.Domain != "weixin" || start.URL != "data:image/png;base64,abc" || start.DeviceCode != "qr-weixin" {
		t.Fatalf("start result = %+v, want weixin QR result", start)
	}

	poll, err := app.PollBotConnectionInstall(start.InstallID)
	if err != nil {
		t.Fatalf("PollBotConnectionInstall: %v", err)
	}
	if !poll.Done {
		t.Fatalf("poll result = %+v, want done", poll)
	}
	if poll.Connection.Provider != "weixin" || poll.Connection.Domain != "weixin" || poll.Connection.Credential.AccountID != "weixin-account" {
		t.Fatalf("connection = %+v, want weixin account connection", poll.Connection)
	}
	if poll.Connection.WorkspaceRoot != "" {
		t.Fatalf("connection workspaceRoot = %q, want empty global default", poll.Connection.WorkspaceRoot)
	}
	if poll.Connection.Credential.TokenEnv != "WEIXIN_BOT_TOKEN" || !poll.Connection.Credential.SecretSet {
		t.Fatalf("credential = %+v, want saved account to count as configured token", poll.Connection.Credential)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if !cfg.Bot.Enabled || !cfg.Bot.Weixin.Enabled || cfg.Bot.Weixin.AccountID != "weixin-account" || cfg.Bot.Weixin.TokenEnv != "WEIXIN_BOT_TOKEN" {
		t.Fatalf("saved weixin config = %+v, want enabled saved account", cfg.Bot.Weixin)
	}
	if len(cfg.Bot.Allowlist.WeixinUsers) != 1 || cfg.Bot.Allowlist.WeixinUsers[0] != "user-1" {
		t.Fatalf("weixin allowlist = %+v, want installer user id", cfg.Bot.Allowlist.WeixinUsers)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(reloaded.Bot.Connections) != 1 {
		t.Fatalf("reloaded connections = %+v, want saved weixin connection", reloaded.Bot.Connections)
	}
	reloadedConnection := botConnectionView(reloaded.Bot.Connections[0])
	if reloadedConnection.Credential.AccountID != "weixin-account" || !reloadedConnection.Credential.SecretSet {
		t.Fatalf("reloaded credential = %+v, want saved weixin account to survive restart", reloadedConnection.Credential)
	}
}

func TestFeishuRegistrationQRCodeURLAddsSDKMetadata(t *testing.T) {
	qrURL, err := feishuRegistrationQRCodeURL("https://open.larksuite.com/page/launcher?user_code=ABCD-1234&source=old")
	if err != nil {
		t.Fatalf("feishuRegistrationQRCodeURL: %v", err)
	}
	parsed, err := url.Parse(qrURL)
	if err != nil {
		t.Fatalf("parse QR URL: %v", err)
	}
	query := parsed.Query()
	if query.Get("user_code") != "ABCD-1234" {
		t.Fatalf("user_code = %q, want preserved code", query.Get("user_code"))
	}
	if query.Get("from") != "sdk" || query.Get("tp") != "sdk" || query.Get("source") != "go-sdk" {
		t.Fatalf("query = %v, want SDK registration metadata", query)
	}
}

func TestDiagnoseBotConnectionBuildsReportDetailForMissingSecret(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("FEISHU_BOT_APP_SECRET_PRIVATE", "")
	app := NewApp()
	if _, err := app.upsertBotConnection(config.BotConnectionConfig{
		ID:            "feishu-lark",
		Provider:      "feishu",
		Domain:        "lark",
		Label:         "Lark",
		Enabled:       true,
		Status:        "connected",
		WorkspaceRoot: "/Users/alice/work/WorkGround2",
		Credential: config.BotConnectionCredential{
			AppID:        "cli-private",
			AppSecretEnv: "FEISHU_BOT_APP_SECRET_PRIVATE",
		},
		SessionMappings: []config.BotConnectionSessionMapping{{
			RemoteID:      "ou-private",
			SessionID:     "session-private",
			Scope:         "project",
			WorkspaceRoot: "/Users/alice/work/WorkGround2",
		}},
	}, nil); err != nil {
		t.Fatalf("upsert connection: %v", err)
	}

	diag, err := app.DiagnoseBotConnection("feishu-lark")
	if err != nil {
		t.Fatalf("DiagnoseBotConnection: %v", err)
	}
	if diag.Status != "warning" || diag.Phase != "credential" || diag.Code != "secret_missing" || diag.ReportKind != "bot" || diag.ReportDetail == "" {
		t.Fatalf("diagnostic = %+v, want warning credential report", diag)
	}
	for _, leaked := range []string{"FEISHU_BOT_APP_SECRET_PRIVATE", "/Users/alice", "ou-private", "session-private"} {
		if strings.Contains(diag.ReportDetail, leaked) {
			t.Fatalf("diagnostic report leaked %q in %s", leaked, diag.ReportDetail)
		}
	}
	var payload frontendCrashPayload
	if err := json.Unmarshal([]byte(diag.ReportDetail), &payload); err != nil {
		t.Fatalf("report detail is not structured JSON: %v", err)
	}
	if payload.Kind != "bot" || payload.Source != "bot.runtime" || payload.Label != "bot.feishu.lark.credential" {
		t.Fatalf("payload = %+v, want bot runtime credential label", payload)
	}
	for _, want := range []string{
		"app_secret_env_configured: true",
		"secret_available: false",
		"workspace_scope: project",
		"session_mappings: 1",
		"summary: required bot credential is not available",
	} {
		if !strings.Contains(payload.Message, want) {
			t.Fatalf("payload message = %q, want it to contain %q", payload.Message, want)
		}
	}
	report, err := crashReportFromDetail(diag.ReportKind, diag.ReportDetail)
	if err != nil {
		t.Fatalf("crashReportFromDetail: %v", err)
	}
	if report.Kind != "bot" || report.Source != "bot.runtime" || report.ErrorType != "BotConnectionDiagnostic" {
		t.Fatalf("report = %+v, want accepted bot report", report)
	}
}

func TestBotConnectionSendFailureReportRedactsEnvNames(t *testing.T) {
	conn := config.BotConnectionConfig{
		ID:       "feishu-lark",
		Provider: "feishu",
		Domain:   "lark",
		Label:    "Lark",
		Enabled:  true,
		Status:   "connected",
		Credential: config.BotConnectionCredential{
			AppSecretEnv: "FEISHU_BOT_APP_SECRET_PRIVATE",
		},
	}
	diag := botConnectionDiagnostic(&conn, conn.ID, "error", "send", "test_send_failed", "feishu app_id or FEISHU_BOT_APP_SECRET_PRIVATE is not configured", true)
	if diag.ReportKind != "bot" || diag.ReportDetail == "" {
		t.Fatalf("diagnostic = %+v, want reportable bot diagnostic", diag)
	}
	if strings.Contains(diag.ReportDetail, "FEISHU_BOT_APP_SECRET_PRIVATE") {
		t.Fatalf("diagnostic report leaked env name in %s", diag.ReportDetail)
	}
	var payload frontendCrashPayload
	if err := json.Unmarshal([]byte(diag.ReportDetail), &payload); err != nil {
		t.Fatalf("report detail is not structured JSON: %v", err)
	}
	if !strings.Contains(payload.ErrorMessage, "[redacted-env]") {
		t.Fatalf("payload errorMessage = %q, want redacted env marker", payload.ErrorMessage)
	}
}

func TestDiagnoseWeixinConnectionDetectsMissingSavedAccountWithoutTokenEnv(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	if _, err := app.upsertBotConnection(config.BotConnectionConfig{
		ID:       "weixin-weixin",
		Provider: "weixin",
		Domain:   "weixin",
		Label:    "微信",
		Enabled:  true,
		Status:   "connected",
		Credential: config.BotConnectionCredential{
			AccountID: "missing-account",
		},
	}, nil); err != nil {
		t.Fatalf("upsert connection: %v", err)
	}

	diag, err := app.DiagnoseBotConnection("weixin-weixin")
	if err != nil {
		t.Fatalf("DiagnoseBotConnection: %v", err)
	}
	if diag.Status != "warning" || diag.Phase != "credential" || diag.Code != "secret_missing" || diag.ReportKind != "bot" || diag.ReportDetail == "" {
		t.Fatalf("diagnostic = %+v, want missing local credential warning", diag)
	}
	if strings.Contains(diag.ReportDetail, "missing-account") {
		t.Fatalf("diagnostic report leaked account id in %s", diag.ReportDetail)
	}
}

func TestRememberBotConnectionRemoteStoresStableEndpoint(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	if _, err := app.upsertBotConnection(config.BotConnectionConfig{
		ID:       "feishu-lark",
		Provider: "feishu",
		Domain:   "lark",
		Label:    "kun",
		Enabled:  true,
		Status:   "connected",
	}, nil); err != nil {
		t.Fatalf("upsert global connection: %v", err)
	}
	if err := app.rememberBotConnectionRemote("feishu-lark", "ou_global"); err != nil {
		t.Fatalf("remember global remote: %v", err)
	}
	if err := app.rememberBotConnectionRemote("feishu-lark", "ou_global"); err != nil {
		t.Fatalf("remember global remote again: %v", err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	endpoints := cfg.Bot.Connections[0].Endpoints
	if len(endpoints) != 1 {
		t.Fatalf("endpoints = %+v, want one stable endpoint", endpoints)
	}
	if got := endpoints[0]; got.RemoteID != "ou_global" || got.ChatType != string(bot.ChatDM) || got.UpdatedAt == "" {
		t.Fatalf("global endpoint = %+v, want dm endpoint with updated_at", got)
	}
	if mappings := cfg.Bot.Connections[0].SessionMappings; len(mappings) != 0 {
		t.Fatalf("session mappings = %+v, want untouched by remote registration", mappings)
	}

	if _, err := app.upsertBotConnection(config.BotConnectionConfig{
		ID:            "weixin-project",
		Provider:      "weixin",
		Domain:        "weixin",
		Label:         "project",
		Enabled:       true,
		Status:        "connected",
		WorkspaceRoot: "/tmp/WorkGround2-project",
	}, nil); err != nil {
		t.Fatalf("upsert project connection: %v", err)
	}
	if err := app.rememberBotConnectionRemote("weixin-project", "wxid_project"); err != nil {
		t.Fatalf("remember project remote: %v", err)
	}
	cfg = config.LoadForEdit(config.UserConfigPath())
	var projectEndpoint config.BotConnectionRemote
	for _, conn := range cfg.Bot.Connections {
		if conn.ID == "weixin-project" && len(conn.Endpoints) == 1 {
			projectEndpoint = conn.Endpoints[0]
		}
	}
	if projectEndpoint.RemoteID != "wxid_project" || projectEndpoint.ChatType != string(bot.ChatDM) {
		t.Fatalf("project endpoint = %+v, want stable dm endpoint", projectEndpoint)
	}
}

func TestUpsertBotConnectionPreservesRegisteredEndpoints(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	if _, err := app.upsertBotConnection(config.BotConnectionConfig{
		ID:       "weixin-weixin",
		Provider: "weixin",
		Domain:   "weixin",
		Label:    "微信",
		Enabled:  true,
		Status:   "connected",
	}, nil); err != nil {
		t.Fatalf("upsert connection: %v", err)
	}
	if err := app.rememberBotConnectionRemote("weixin-weixin", "wx-owner"); err != nil {
		t.Fatalf("remember remote: %v", err)
	}

	// 重装/重配对会整体替换连接记录：已登记端点必须保留（通知目标数据源）。
	if _, err := app.upsertBotConnection(config.BotConnectionConfig{
		ID:       "weixin-weixin",
		Provider: "weixin",
		Domain:   "weixin",
		Label:    "微信",
		Enabled:  true,
		Status:   "connected",
	}, nil); err != nil {
		t.Fatalf("re-upsert connection: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	endpoints := cfg.Bot.Connections[0].Endpoints
	if len(endpoints) != 1 || endpoints[0].RemoteID != "wx-owner" || endpoints[0].ChatType != string(bot.ChatDM) {
		t.Fatalf("endpoints = %+v, want preserved after re-install", endpoints)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func withRewrittenHTTP(t *testing.T, handler http.Handler) {
	t.Helper()
	server := memhttp.NewServer(handler)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	http.DefaultTransport = rewriteHTTPTransport{target: target, next: previous}
	t.Cleanup(func() {
		http.DefaultTransport = previous
		server.Close()
	})
}

type rewriteHTTPTransport struct {
	target *url.URL
	next   http.RoundTripper
}

func (r rewriteHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Test-Original-Host", req.URL.Host)
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	clone.Host = r.target.Host
	if r.next == nil {
		r.next = http.DefaultTransport
	}
	clone.URL.Path = "/" + strings.TrimLeft(clone.URL.Path, "/")
	return r.next.RoundTrip(clone)
}

func writeWeixinSavedAccount(t *testing.T, accountID, token, userID string) {
	t.Helper()
	dir := filepath.Join(config.MemoryUserDir(), "weixin", "accounts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir weixin accounts: %v", err)
	}
	body := fmt.Sprintf(`{"token":%q,"base_url":"https://ilinkai.weixin.qq.com","user_id":%q,"saved_at":"2026-08-17T00:00:00Z"}`, token, userID)
	if err := os.WriteFile(filepath.Join(dir, accountID+".json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write weixin saved account: %v", err)
	}
}

// TestBackfillFeishuConnectionPreservesGlobalAccess verifies the legacy feishu
// backfill copies existing global allowlist roles and groups into the new
// connection-level access, so connection access cannot shadow the global grant.
func TestBackfillFeishuConnectionPreservesGlobalAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.Default()
	cfg.Bot.Enabled = true
	cfg.Bot.Feishu = config.FeishuBotConfig{Enabled: true, AppID: "cli_fs", AppSecretEnv: "FEISHU_BOT_APP_SECRET", Domain: "feishu"}
	cfg.Bot.Allowlist = config.BotAllowlist{
		Enabled:         true,
		AllowAll:        true,
		FeishuUsers:     []string{"ou_global_user"},
		FeishuGroups:    []string{"oc_global_group"},
		FeishuApprovers: []string{"ou_global_approver"},
		FeishuAdmins:    []string{"ou_global_admin"},
	}
	if !backfillFeishuConnection(cfg) {
		t.Fatal("backfillFeishuConnection returned false")
	}
	var conn config.BotConnectionConfig
	for _, c := range cfg.Bot.Connections {
		if c.Provider == "feishu" {
			conn = c
		}
	}
	if conn.ID == "" {
		t.Fatal("feishu connection not backfilled")
	}
	access := conn.Access
	if !access.AllowAll {
		t.Error("access allow_all not preserved from global allowlist")
	}
	if !stringSlicesEqual(access.Users, cfg.Bot.Allowlist.FeishuUsers) {
		t.Errorf("access users = %+v, want global %+v preserved", access.Users, cfg.Bot.Allowlist.FeishuUsers)
	}
	if !stringSlicesEqual(access.Groups, cfg.Bot.Allowlist.FeishuGroups) {
		t.Errorf("access groups = %+v, want global %+v preserved", access.Groups, cfg.Bot.Allowlist.FeishuGroups)
	}
	if !stringSlicesEqual(access.Approvers, cfg.Bot.Allowlist.FeishuApprovers) {
		t.Errorf("access approvers = %+v, want global %+v preserved", access.Approvers, cfg.Bot.Allowlist.FeishuApprovers)
	}
	if !stringSlicesEqual(access.Admins, cfg.Bot.Allowlist.FeishuAdmins) {
		t.Errorf("access admins = %+v, want global %+v preserved", access.Admins, cfg.Bot.Allowlist.FeishuAdmins)
	}
}

// TestBackfillWeixinConnectionPreservesGlobalAccess is the WeChat counterpart
// of the feishu backfill test.
func TestBackfillWeixinConnectionPreservesGlobalAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	writeWeixinSavedAccount(t, "wx-test", "token-1", "user-1")
	cfg := config.Default()
	cfg.Bot.Enabled = true
	cfg.Bot.Allowlist = config.BotAllowlist{
		Enabled:         true,
		AllowAll:        true,
		WeixinUsers:     []string{"wx_global_user"},
		WeixinGroups:    []string{"wx_global_group"},
		WeixinApprovers: []string{"wx_global_approver"},
		WeixinAdmins:    []string{"wx_global_admin"},
	}
	if !backfillWeixinConnection(cfg) {
		t.Fatal("backfillWeixinConnection returned false")
	}
	var conn config.BotConnectionConfig
	for _, c := range cfg.Bot.Connections {
		if c.Provider == "weixin" {
			conn = c
		}
	}
	if conn.ID == "" {
		t.Fatal("weixin connection not backfilled")
	}
	access := conn.Access
	if !access.AllowAll {
		t.Error("access allow_all not preserved from global allowlist")
	}
	if !stringSlicesEqual(access.Users, []string{"wx_global_user", "user-1"}) {
		t.Errorf("access users = %+v, want global and saved account users preserved", access.Users)
	}
	if !stringSlicesEqual(access.Groups, cfg.Bot.Allowlist.WeixinGroups) {
		t.Errorf("access groups = %+v, want global %+v preserved", access.Groups, cfg.Bot.Allowlist.WeixinGroups)
	}
	if !stringSlicesEqual(access.Approvers, cfg.Bot.Allowlist.WeixinApprovers) {
		t.Errorf("access approvers = %+v, want global %+v preserved", access.Approvers, cfg.Bot.Allowlist.WeixinApprovers)
	}
	if !stringSlicesEqual(access.Admins, cfg.Bot.Allowlist.WeixinAdmins) {
		t.Errorf("access admins = %+v, want global %+v preserved", access.Admins, cfg.Bot.Allowlist.WeixinAdmins)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
