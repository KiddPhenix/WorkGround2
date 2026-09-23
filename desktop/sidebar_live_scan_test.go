package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
)

func TestSidebarScanPublishesWhileDirectoryChanges(t *testing.T) {
	dir := t.TempDir()
	meta := agent.BranchMeta{ID: "first", Scope: "project", WorkspaceRoot: dir, TopicID: "first", TopicTitle: "Before", Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	path := testSidebarWriteSessionMeta(t, dir, "first.jsonl", meta)
	plan := testSidebarProjectPlan("project_live", "Live", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{plan.group.ID: "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	reads := 0
	index.loadBranchMeta = func(path string) (agent.BranchMeta, bool, error) {
		value, ok, err := agent.LoadBranchMeta(path)
		reads++
		if reads == 1 {
			meta.TopicTitle = "After"
			if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
				t.Fatal(err)
			}
			second := meta
			second.ID, second.TopicID = "second", "second"
			testSidebarWriteSessionMeta(t, dir, "second.jsonl", second)
		}
		// Even an unrelated write on every read must not restart this scan.
		index.markDirty(app, path)
		return value, ok, err
	}
	query := SidebarSessionQuery{Mode: SidebarProjects, GroupID: plan.group.ID}
	first, err := index.listSessions(app, query)
	if err != nil || len(first.Items) != 1 || first.Items[0].Title != "Before" || reads != 1 {
		t.Fatalf("first snapshot=%+v reads=%d err=%v", first, reads, err)
	}
	second, err := index.listSessions(app, query)
	if err != nil || len(second.Items) != 2 {
		t.Fatalf("follow-up snapshot=%+v err=%v", second, err)
	}
	for _, row := range second.Items {
		if row.SessionPath == path && row.Title != "After" {
			t.Fatalf("write during scan was lost: %+v", row)
		}
	}
}

func TestSidebarScanIgnoresEventLogs(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "chat.events.jsonl")
	if err := os.WriteFile(events, []byte("event log, not a conversation"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := testSidebarProjectPlan("project_events", "Events", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{plan.group.ID: "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	index.loadBranchMeta = func(path string) (agent.BranchMeta, bool, error) {
		t.Errorf("event log incorrectly read as session: %s", path)
		return agent.BranchMeta{}, false, nil
	}
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := index.scanSidebarDir(plan, dir, state.db)
	if err != nil || len(rows) != 0 {
		t.Fatalf("event log indexed: rows=%d err=%v", len(rows), err)
	}
}
