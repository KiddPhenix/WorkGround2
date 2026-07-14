package main

import (
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
)

func TestExternalSessionIdleLimit(t *testing.T) {
	tests := []struct {
		name string
		meta agent.BranchMeta
		want time.Duration
		ok   bool
	}{
		{name: "cli", meta: agent.BranchMeta{SessionSource: "cli"}, want: cliSessionIdleLimit, ok: true},
		{name: "im", meta: agent.BranchMeta{SessionSource: "auto", Channel: "weixin"}, want: imSessionIdleLimit, ok: true},
		{name: "auto without channel", meta: agent.BranchMeta{SessionSource: "auto"}},
		{name: "local", meta: agent.BranchMeta{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := externalSessionIdleLimit(tt.meta)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("externalSessionIdleLimit(%+v) = (%s, %v), want (%s, %v)", tt.meta, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestShouldTrashExternalSessionHonorsSourceAndPin(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	write := func(name, source string, pinned bool) string {
		path := filepath.Join(dir, name+".jsonl")
		if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
			ID:            name,
			CreatedAt:     now.Add(-7 * time.Hour),
			UpdatedAt:     now.Add(-7 * time.Hour),
			SessionSource: source,
			Pinned:        pinned,
		}); err != nil {
			t.Fatalf("save %s meta: %v", name, err)
		}
		return path
	}
	if !app.shouldTrashExternalSession(write("cli", "cli", false), now) {
		t.Fatal("expired CLI session was not selected")
	}
	if app.shouldTrashExternalSession(write("pinned", "cli", true), now) {
		t.Fatal("pinned session was selected")
	}
	if app.shouldTrashExternalSession(write("local", "", false), now) {
		t.Fatal("local session was selected")
	}
}
