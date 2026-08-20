package botruntime

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/bot"
	"workground2/internal/config"
)

func TestRemoteRemembererKeepsDistinctGroupUsers(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "feishu-lark", Provider: "feishu", Domain: "lark", Label: "Lark", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	remember := NewRemoteRememberer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	remember(bot.InboundMessage{
		Platform:     bot.PlatformFeishu,
		ConnectionID: "feishu-lark",
		Domain:       "lark",
		ChatType:     bot.ChatGroup,
		ChatID:       "oc-group-1",
		UserID:       "ou-user-1",
	})
	remember(bot.InboundMessage{
		Platform:     bot.PlatformFeishu,
		ConnectionID: "feishu-lark",
		Domain:       "lark",
		ChatType:     bot.ChatGroup,
		ChatID:       "oc-group-1",
		UserID:       "ou-user-2",
	})

	got := config.LoadForEdit(config.UserConfigPath())
	if users := got.Bot.Allowlist.FeishuUsers; len(users) != 2 || users[0] != "ou-user-1" || users[1] != "ou-user-2" {
		t.Fatalf("feishu users = %+v, want both group users", users)
	}
	if groups := got.Bot.Allowlist.FeishuGroups; len(groups) != 1 || groups[0] != "oc-group-1" {
		t.Fatalf("feishu groups = %+v, want group once", groups)
	}
	if mappings := got.Bot.Connections[0].SessionMappings; len(mappings) != 2 || mappings[0].RemoteID != "oc-group-1" || mappings[0].UserID != "ou-user-1" || mappings[1].RemoteID != "oc-group-1" || mappings[1].UserID != "ou-user-2" {
		t.Fatalf("session mappings = %+v, want distinct group-user mappings", mappings)
	}
}

func TestRememberInboundSessionFillsExistingMappingSessionID(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg := bot.InboundMessage{
		Platform:     bot.PlatformWeixin,
		ConnectionID: "weixin-weixin",
		Domain:       "weixin",
		ChatType:     bot.ChatDM,
		ChatID:       "wx-chat-1",
		UserID:       "wx-user-1",
	}
	if err := RememberInbound(msg); err != nil {
		t.Fatalf("remember inbound: %v", err)
	}
	if err := RememberInboundSession(msg, "path:/sessions/20260614-120000.000000000-deepseek.jsonl"); err != nil {
		t.Fatalf("remember inbound session: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mappings := got.Bot.Connections[0].SessionMappings
	if len(mappings) != 1 {
		t.Fatalf("mappings = %+v, want one mapping", mappings)
	}
	if mappings[0].RemoteID != "wx-chat-1" || mappings[0].SessionID != "path:/sessions/20260614-120000.000000000-deepseek.jsonl" || mappings[0].SessionSource != "auto" {
		t.Fatalf("mapping = %+v, want remote chat with session id", mappings[0])
	}
}

func TestRememberInboundSessionCreatesMappingWithSessionID(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "feishu-lark", Provider: "feishu", Domain: "lark", Label: "Lark", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if err := RememberInboundSession(bot.InboundMessage{
		Platform:     bot.PlatformFeishu,
		ConnectionID: "feishu-lark",
		Domain:       "lark",
		ChatType:     bot.ChatDM,
		ChatID:       "oc-chat-1",
		UserID:       "ou-user-1",
	}, "path:/sessions/topic-bot.jsonl"); err != nil {
		t.Fatalf("remember inbound session: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mappings := got.Bot.Connections[0].SessionMappings
	if len(mappings) != 1 || mappings[0].RemoteID != "oc-chat-1" || mappings[0].SessionID != "path:/sessions/topic-bot.jsonl" || mappings[0].SessionSource != "auto" {
		t.Fatalf("mappings = %+v, want mapping with session id", mappings)
	}
}

func TestRememberInboundSessionKeepsDistinctGroupUsers(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "feishu-lark", Provider: "feishu", Domain: "lark", Label: "Lark", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg1 := bot.InboundMessage{Platform: bot.PlatformFeishu, ConnectionID: "feishu-lark", Domain: "lark", ChatType: bot.ChatGroup, ChatID: "oc-group-1", UserID: "ou-user-1"}
	msg2 := bot.InboundMessage{Platform: bot.PlatformFeishu, ConnectionID: "feishu-lark", Domain: "lark", ChatType: bot.ChatGroup, ChatID: "oc-group-1", UserID: "ou-user-2"}
	if err := RememberInboundSession(msg1, "path:/sessions/user-1.jsonl"); err != nil {
		t.Fatalf("remember user 1: %v", err)
	}
	if err := RememberInboundSession(msg2, "path:/sessions/user-2.jsonl"); err != nil {
		t.Fatalf("remember user 2: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mappings := got.Bot.Connections[0].SessionMappings
	if len(mappings) != 2 {
		t.Fatalf("mappings = %+v, want two group-user mappings", mappings)
	}
	if mappings[0].UserID != "ou-user-1" || mappings[0].SessionID != "path:/sessions/user-1.jsonl" || mappings[1].UserID != "ou-user-2" || mappings[1].SessionID != "path:/sessions/user-2.jsonl" {
		t.Fatalf("mappings = %+v, want user-specific session ids", mappings)
	}
}

func TestRememberInboundSessionSharesThreadMappingAcrossUsers(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "feishu-lark", Provider: "feishu", Domain: "lark", Label: "Lark", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg1 := bot.InboundMessage{Platform: bot.PlatformFeishu, ConnectionID: "feishu-lark", Domain: "lark", ChatType: bot.ChatThread, ChatID: "oc-group-1", ThreadID: "thread-1", UserID: "ou-user-1"}
	msg2 := bot.InboundMessage{Platform: bot.PlatformFeishu, ConnectionID: "feishu-lark", Domain: "lark", ChatType: bot.ChatThread, ChatID: "oc-group-1", ThreadID: "thread-1", UserID: "ou-user-2"}
	if err := RememberInboundSession(msg1, "path:/sessions/thread-old.jsonl"); err != nil {
		t.Fatalf("remember user 1: %v", err)
	}
	if err := RememberInboundSession(msg2, "path:/sessions/thread-new.jsonl"); err != nil {
		t.Fatalf("remember user 2: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mappings := got.Bot.Connections[0].SessionMappings
	if len(mappings) != 1 {
		t.Fatalf("mappings = %+v, want one shared thread mapping", mappings)
	}
	if mappings[0].ChatType != string(bot.ChatThread) || mappings[0].ThreadID != "thread-1" || mappings[0].UserID != "" || mappings[0].SessionID != "path:/sessions/thread-new.jsonl" {
		t.Fatalf("mapping = %+v, want shared thread identity with latest auto session", mappings[0])
	}
}

func TestRememberInboundSessionPreservesExplicitMappingTarget(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected",
		SessionMappings: []config.BotConnectionSessionMapping{{RemoteID: "wx-chat-1", SessionID: "topic:manual-topic"}},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "weixin-weixin", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "wx-chat-1", UserID: "wx-user-1"}
	if err := RememberInboundSession(msg, "path:/sessions/auto.jsonl"); err != nil {
		t.Fatalf("remember inbound session: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mapping := got.Bot.Connections[0].SessionMappings[0]
	if mapping.SessionID != "topic:manual-topic" || mapping.SessionSource != "" {
		t.Fatalf("mapping = %+v, want explicit topic preserved", mapping)
	}
}

func TestRememberInboundSessionPreservesBareExplicitMappingTarget(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected",
		SessionMappings: []config.BotConnectionSessionMapping{{RemoteID: "wx-chat-1", SessionID: "manual-topic"}},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "weixin-weixin", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "wx-chat-1", UserID: "wx-user-1"}
	if err := RememberInboundSession(msg, "path:/sessions/auto.jsonl"); err != nil {
		t.Fatalf("remember inbound session: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mapping := got.Bot.Connections[0].SessionMappings[0]
	if mapping.SessionID != "manual-topic" || mapping.SessionSource != "" {
		t.Fatalf("mapping = %+v, want bare explicit target preserved", mapping)
	}
}

func TestRememberInboundSessionUsesActualWorkspaceWhenConnectionIsGlobal(t *testing.T) {
	isolateUserConfig(t)
	workspace := filepath.Join(t.TempDir(), "workspace")
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "weixin-weixin", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "wx-chat-1", UserID: "wx-user-1"}
	if err := RememberInboundSessionWorkspace(msg, "path:/sessions/auto.jsonl", workspace); err != nil {
		t.Fatalf("remember inbound session: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mapping := got.Bot.Connections[0].SessionMappings[0]
	if mapping.Scope != "project" || mapping.WorkspaceRoot != workspace {
		t.Fatalf("mapping = %+v, want actual workspace scope", mapping)
	}
}

func TestRememberInboundSessionKeepsConfiguredWorkspaceOverActualWorkspace(t *testing.T) {
	isolateUserConfig(t)
	configured := filepath.Join(t.TempDir(), "configured")
	actual := filepath.Join(t.TempDir(), "actual")
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected", WorkspaceRoot: configured,
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "weixin-weixin", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "wx-chat-1", UserID: "wx-user-1"}
	if err := RememberInboundSessionWorkspace(msg, "path:/sessions/auto.jsonl", actual); err != nil {
		t.Fatalf("remember inbound session: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mapping := got.Bot.Connections[0].SessionMappings[0]
	if mapping.Scope != "project" || mapping.WorkspaceRoot != configured {
		t.Fatalf("mapping = %+v, want configured workspace scope", mapping)
	}
}

func TestRememberInboundSessionUpdatesAutoMappingTarget(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected",
		SessionMappings: []config.BotConnectionSessionMapping{{RemoteID: "wx-chat-1", SessionID: "path:/sessions/old.jsonl", SessionSource: "auto"}},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "weixin-weixin", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "wx-chat-1", UserID: "wx-user-1"}
	if err := RememberInboundSession(msg, "path:/sessions/new.jsonl"); err != nil {
		t.Fatalf("remember inbound session: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mapping := got.Bot.Connections[0].SessionMappings[0]
	if mapping.SessionID != "path:/sessions/new.jsonl" || mapping.SessionSource != "auto" {
		t.Fatalf("mapping = %+v, want auto target updated", mapping)
	}
}

func TestRememberInboundSessionPersistsAutoSourceOnEverySession(t *testing.T) {
	isolateUserConfig(t)
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.jsonl")
	newPath := filepath.Join(dir, "new.jsonl")
	for _, path := range []string{oldPath, newPath} {
		if err := os.WriteFile(path, []byte(`{"role":"user","content":"from bot"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write session %s: %v", path, err)
		}
	}
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "weixin-weixin", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "wx-chat-1", UserID: "wx-user-1"}
	if err := RememberInboundSession(msg, "path:"+oldPath); err != nil {
		t.Fatalf("remember old session: %v", err)
	}
	if err := RememberInboundSession(msg, "path:"+newPath); err != nil {
		t.Fatalf("remember new session: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mappings := got.Bot.Connections[0].SessionMappings
	if len(mappings) != 1 || mappings[0].SessionID != "path:"+newPath || mappings[0].SessionSource != "auto" {
		t.Fatalf("mappings = %+v, want one current auto mapping to new session", mappings)
	}
	for _, path := range []string{oldPath, newPath} {
		meta, ok, err := agent.LoadBranchMeta(path)
		if err != nil || !ok {
			t.Fatalf("load meta %s: ok=%v err=%v", path, ok, err)
		}
		if meta.SessionSource != "auto" || meta.Channel != "weixin" || meta.ChannelLabel != "微信" || meta.RemoteID != "wx-chat-1" {
			t.Fatalf("meta for %s = %+v, want persisted bot source", path, meta)
		}
	}
}

func TestForgetAutoSessionMappingsForPathRemovesOnlyAutoPathTargets(t *testing.T) {
	isolateUserConfig(t)
	target := filepath.Join(t.TempDir(), "bot-channel.jsonl")
	other := filepath.Join(t.TempDir(), "other-channel.jsonl")
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected",
		SessionMappings: []config.BotConnectionSessionMapping{
			{RemoteID: "remove-path-prefix", SessionID: "path:" + target, SessionSource: "auto"},
			{RemoteID: "remove-raw-path", SessionID: target, SessionSource: "auto"},
			{RemoteID: "keep-explicit-path", SessionID: "path:" + target},
			{RemoteID: "keep-other-auto", SessionID: "path:" + other, SessionSource: "auto"},
			{RemoteID: "keep-topic-auto", SessionID: "topic:bot-topic", SessionSource: "auto"},
		},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if err := ForgetAutoSessionMappingsForPath(target); err != nil {
		t.Fatalf("forget auto session mappings: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mappings := got.Bot.Connections[0].SessionMappings
	if len(mappings) != 3 {
		t.Fatalf("mappings = %+v, want three preserved mappings", mappings)
	}
	remotes := map[string]bool{}
	for _, mapping := range mappings {
		remotes[mapping.RemoteID] = true
	}
	for _, remote := range []string{"keep-explicit-path", "keep-other-auto", "keep-topic-auto"} {
		if !remotes[remote] {
			t.Fatalf("mapping %q was not preserved: %+v", remote, mappings)
		}
	}
	if got.Bot.Connections[0].UpdatedAt == "" {
		t.Fatalf("connection UpdatedAt was not refreshed")
	}
}

func TestRememberInboundRegistersEndpointWithoutSession(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dm := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "weixin-weixin", Domain: "weixin", ChatType: bot.ChatDM, ChatID: "wx-chat-1", UserID: "wx-user-1"}
	group := bot.InboundMessage{Platform: bot.PlatformWeixin, ConnectionID: "weixin-weixin", Domain: "weixin", ChatType: bot.ChatGroup, ChatID: "oc-group-1", UserID: "ou-user-1"}
	if err := RememberInbound(dm); err != nil {
		t.Fatalf("remember dm: %v", err)
	}
	if err := RememberInbound(group); err != nil {
		t.Fatalf("remember group: %v", err)
	}
	// 重复入站幂等：不新增端点、不报错。
	if err := RememberInbound(dm); err != nil {
		t.Fatalf("remember dm again: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	endpoints := got.Bot.Connections[0].Endpoints
	if len(endpoints) != 2 {
		t.Fatalf("endpoints = %+v, want dm + group endpoints", endpoints)
	}
	byRemote := map[string]config.BotConnectionRemote{}
	for _, e := range endpoints {
		byRemote[e.RemoteID] = e
	}
	if e := byRemote["wx-chat-1"]; e.ChatType != "dm" {
		t.Fatalf("dm endpoint = %+v, want chat_type dm", e)
	}
	if e := byRemote["oc-group-1"]; e.ChatType != "group" {
		t.Fatalf("group endpoint = %+v, want chat_type group", e)
	}
	if byRemote["wx-chat-1"].UpdatedAt == "" {
		t.Fatalf("endpoint UpdatedAt was not set")
	}
}

func TestRememberInboundEndpointGroupSharesOneTarget(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "feishu-lark", Provider: "feishu", Domain: "lark", Label: "Lark", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	msg1 := bot.InboundMessage{Platform: bot.PlatformFeishu, ConnectionID: "feishu-lark", Domain: "lark", ChatType: bot.ChatGroup, ChatID: "oc-group-1", UserID: "ou-user-1"}
	msg2 := bot.InboundMessage{Platform: bot.PlatformFeishu, ConnectionID: "feishu-lark", Domain: "lark", ChatType: bot.ChatGroup, ChatID: "oc-group-1", UserID: "ou-user-2"}
	if err := RememberInbound(msg1); err != nil {
		t.Fatalf("remember user 1: %v", err)
	}
	if err := RememberInbound(msg2); err != nil {
		t.Fatalf("remember user 2: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	endpoints := got.Bot.Connections[0].Endpoints
	if len(endpoints) != 1 || endpoints[0].RemoteID != "oc-group-1" || endpoints[0].ChatType != "group" {
		t.Fatalf("endpoints = %+v, want one shared group endpoint", endpoints)
	}
}

func TestRememberInboundEndpointCapsPerConnection(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{
		{ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	for i := 0; i < maxEndpointsPerConnection+50; i++ {
		if err := RememberInbound(bot.InboundMessage{
			Platform:     bot.PlatformWeixin,
			ConnectionID: "weixin-weixin",
			Domain:       "weixin",
			ChatType:     bot.ChatDM,
			ChatID:       fmt.Sprintf("wx-chat-%03d", i),
			UserID:       "wx-user",
		}); err != nil {
			t.Fatalf("remember chat %d: %v", i, err)
		}
	}

	got := config.LoadForEdit(config.UserConfigPath())
	endpoints := got.Bot.Connections[0].Endpoints
	if len(endpoints) != maxEndpointsPerConnection {
		t.Fatalf("endpoints = %d, want capped at %d", len(endpoints), maxEndpointsPerConnection)
	}
	seen := make(map[string]bool)
	for _, e := range endpoints {
		if e.RemoteID == "" || e.UpdatedAt == "" {
			t.Fatalf("endpoint = %+v, want remote_id and updated_at", e)
		}
		if seen[e.RemoteID] {
			t.Fatalf("duplicate endpoint %q after cap", e.RemoteID)
		}
		seen[e.RemoteID] = true
	}
	latest := fmt.Sprintf("wx-chat-%03d", maxEndpointsPerConnection+49)
	if !seen[latest] {
		t.Fatalf("latest endpoint %q was discarded by the cap", latest)
	}
	if seen["wx-chat-000"] {
		t.Fatal("oldest endpoint was not evicted by the cap")
	}
}

func TestForgetAutoSessionMappingsKeepsRegisteredEndpoints(t *testing.T) {
	isolateUserConfig(t)
	target := filepath.Join(t.TempDir(), "bot-channel.jsonl")
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected",
		SessionMappings: []config.BotConnectionSessionMapping{
			{RemoteID: "wx-chat-1", SessionID: "path:" + target, SessionSource: "auto"},
		},
		Endpoints: []config.BotConnectionRemote{
			{RemoteID: "wx-chat-1", ChatType: "dm"},
		},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if err := ForgetAutoSessionMappingsForPath(target); err != nil {
		t.Fatalf("forget auto session mappings: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	if mappings := got.Bot.Connections[0].SessionMappings; len(mappings) != 0 {
		t.Fatalf("session mappings = %+v, want cleared by GC", mappings)
	}
	endpoints := got.Bot.Connections[0].Endpoints
	if len(endpoints) != 1 || endpoints[0].RemoteID != "wx-chat-1" || endpoints[0].ChatType != "dm" {
		t.Fatalf("endpoints = %+v, want preserved after session GC", endpoints)
	}
}

func TestConnectionChannelConfigsPreserveToolApprovalMode(t *testing.T) {
	connections := []config.BotConnectionConfig{
		{ID: "feishu-feishu", Provider: "feishu", Domain: "feishu", Enabled: true, ToolApprovalMode: "auto"},
		{ID: "feishu-lark", Provider: "feishu", Domain: "lark", Enabled: true, ToolApprovalMode: "yolo"},
		{ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Enabled: true, ToolApprovalMode: "ask"},
	}

	byConnection := ConnectionChannelConfigs(connections, true, true)
	if got := byConnection["feishu-feishu"].ToolApprovalMode; got != "auto" {
		t.Fatalf("feishu tool approval mode = %q, want auto", got)
	}
	if got := byConnection["feishu-lark"].ToolApprovalMode; got != "yolo" {
		t.Fatalf("lark tool approval mode = %q, want yolo", got)
	}
	if got := byConnection["weixin-weixin"].ToolApprovalMode; got != "ask" {
		t.Fatalf("weixin tool approval mode = %q, want explicit ask override", got)
	}

	byPlatform := ChannelConfigs(connections, true, true)
	if got := byPlatform[bot.PlatformFeishu].ToolApprovalMode; got != "yolo" {
		t.Fatalf("platform feishu tool approval mode = %q, want last enabled Feishu/Lark override", got)
	}
}

func isolateUserConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Chdir(t.TempDir())
}
