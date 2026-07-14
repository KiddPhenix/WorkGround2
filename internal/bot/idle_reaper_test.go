package bot

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/control"
)

func TestSweepIdleSessionsTrashesOnlyUnpinnedAutoSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idle.jsonl")
	now := time.Now().UTC()
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		ID:            "idle",
		CreatedAt:     now.Add(-5 * time.Hour),
		UpdatedAt:     now.Add(-5 * time.Hour),
		SessionSource: "auto",
		Channel:       "weixin",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	var expired string
	gw := &BotGateway{
		cfg: GatewayConfig{OnSessionIdle: func(got string) error { expired = got; return nil }},
		controllers: map[string]*sessionState{
			"idle": {ctrl: control.New(control.Options{SessionPath: path}), lastActive: now.Add(-5 * time.Hour)},
		},
		sessions: NewSessionManager(time.Millisecond),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if got := gw.sweepIdleSessions(now); got != 1 {
		t.Fatalf("swept %d sessions, want 1", got)
	}
	if expired != path {
		t.Fatalf("expired path = %q, want %q", expired, path)
	}
	if len(gw.controllers) != 0 {
		t.Fatalf("idle controller was retained")
	}
}

func TestSweepIdleSessionsKeepsPinnedSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pinned.jsonl")
	now := time.Now().UTC()
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		ID:            "pinned",
		CreatedAt:     now.Add(-5 * time.Hour),
		UpdatedAt:     now.Add(-5 * time.Hour),
		SessionSource: "auto",
		Channel:       "weixin",
		Pinned:        true,
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	gw := &BotGateway{
		cfg: GatewayConfig{OnSessionIdle: func(string) error { t.Fatal("pinned session expired"); return nil }},
		controllers: map[string]*sessionState{
			"pinned": {ctrl: control.New(control.Options{SessionPath: path}), lastActive: now.Add(-5 * time.Hour)},
		},
		sessions: NewSessionManager(time.Millisecond),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if got := gw.sweepIdleSessions(now); got != 0 {
		t.Fatalf("swept %d sessions, want 0", got)
	}
	if len(gw.controllers) != 1 {
		t.Fatalf("pinned controller was removed")
	}
}
