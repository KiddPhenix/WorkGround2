package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	bolt "go.etcd.io/bbolt"
	"workground2/internal/agent"
	"workground2/internal/provider"
)

func TestSidebarPageLimitBoundaries(t *testing.T) {
	if got := normalizeSidebarLimit(0); got != 20 {
		t.Fatalf("default limit = %d", got)
	}
	if got := normalizeSidebarLimit(1); got != 10 {
		t.Fatalf("minimum limit = %d", got)
	}
	if got := normalizeSidebarLimit(99); got != 50 {
		t.Fatalf("maximum limit = %d", got)
	}
}

func TestSidebarDiskStampIncludesPinnedTitlesAndCrewRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(sessionTitlesPath(dir), []byte(`{"a":"one"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	routeVersion := "routes-v1"
	source := sidebarDiskIndexSource{crewStamp: func() string { return routeVersion }}
	plan := sidebarGroupPlan{
		group: SidebarGroup{ID: "crew_folder", Kind: "crew", Label: "Crew"}, crew: true,
		dirs: []string{dir}, pinned: map[string]bool{"a": true}, titles: map[string]string{},
	}
	first := source.stamp(&App{}, plan)
	plan.pinned["b"] = true
	pinned := source.stamp(&App{}, plan)
	if pinned == first {
		t.Fatal("pinned topics did not change stamp")
	}
	titlePath := filepath.Clean(sessionTitlesPath(dir))
	info, err := os.Stat(titlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(titlePath, []byte(`{"a":"renamed title"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force a distinct mtime on filesystems with coarse timestamp resolution.
	nextTime := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(titlePath, nextTime, nextTime); err != nil {
		t.Fatal(err)
	}
	titled := source.stamp(&App{}, plan)
	if titled == pinned {
		t.Fatal("session title sidecar did not change stamp")
	}
	routeVersion = "routes-v2"
	if routed := source.stamp(&App{}, plan); routed == titled {
		t.Fatal("crew route summary did not change stamp")
	}
	project := plan
	project.group.ID, project.group.Kind, project.crew = "project_routes", "project", false
	projectStamp := source.stamp(&App{}, project)
	routeVersion = "routes-v3"
	if source.stamp(&App{}, project) == projectStamp {
		t.Fatal("ordinary project stamp did not include crew routes")
	}
}

func TestSidebarBoltIndexPagesRealSidecarsAndKeepsOrphanWork(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 25; i++ {
		path := filepath.Join(dir, fmt.Sprintf("session-%02d.jsonl", i))
		if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
			ID: fmt.Sprintf("session-%02d", i), Scope: "project", WorkspaceRoot: dir,
			TopicID: fmt.Sprintf("topic-%02d", i), TopicTitle: fmt.Sprintf("Session %02d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute), UpdatedAt: base.Add(time.Duration(i) * time.Minute),
			Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion,
		}); err != nil {
			t.Fatal(err)
		}
	}
	orphan := filepath.Join(dir, "orphan-work.jsonl")
	if err := os.WriteFile(orphan, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(orphan, agent.BranchMeta{
		ID: "orphan-work", Scope: "project", WorkspaceRoot: dir, SessionKind: agent.SessionKindWork,
		CreatedAt: base.Add(26 * time.Minute), UpdatedAt: base.Add(26 * time.Minute), Turns: 0, SchemaVersion: agent.BranchMetaCountsVersion,
	}); err != nil {
		t.Fatal(err)
	}

	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_real", Kind: "project", Label: "Real", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	source := &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_real": "meta-v1"}, items: map[string][]SidebarSession{}, loads: map[string]int{}}
	sidebarDBPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return sidebarDBPath })
	index.source = source
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	groups, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(groups) != 1 || source.loads["project_real"] != 0 {
		t.Fatalf("summary groups=%+v source loads=%v err=%v", groups, source.loads, err)
	}
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_real"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 20 || first.Total == nil || *first.Total != 26 || first.NextCursor == "" {
		t.Fatalf("first bolt page items=%d total=%v cursor=%q", len(first.Items), first.Total, first.NextCursor)
	}
	second, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_real", Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 6 || second.NextCursor != "" {
		t.Fatalf("second bolt page items=%d cursor=%q err=%v", len(second.Items), second.NextCursor, err)
	}
	foundOrphan := false
	for _, item := range append(first.Items, second.Items...) {
		foundOrphan = foundOrphan || item.ID == "orphan-work"
	}
	if !foundOrphan {
		t.Fatal("topic-less zero-turn Work session was omitted")
	}
	changedPath := filepath.Join(dir, "session-00.jsonl")
	changedMeta, ok, err := agent.LoadBranchMeta(changedPath)
	if err != nil || !ok {
		t.Fatalf("load changed sidecar: ok=%v err=%v", ok, err)
	}
	changedMeta.CustomTitle = "Updated Session"
	changedMeta.UpdatedAt = base.Add(3 * time.Hour)
	if err := agent.SaveBranchMetaPreserveUpdated(changedPath, changedMeta); err != nil {
		t.Fatal(err)
	}
	forced := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(dir, forced, forced); err != nil {
		t.Fatal(err)
	}
	refreshed, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_real"})
	if err != nil || len(refreshed.Items) == 0 || refreshed.Total == nil || *refreshed.Total != 26 || refreshed.Items[0].ID != "session-00" || refreshed.Items[0].Title != "Updated Session" || refreshed.Snapshot == first.Snapshot {
		t.Fatalf("incremental refresh items=%d total=%d first=%+v snapshot=%q/%q err=%v", len(refreshed.Items), *refreshed.Total, refreshed.Items, refreshed.Snapshot, first.Snapshot, err)
	}
	// The old immutable query bucket remains readable, so a retried page does
	// not jump into the refreshed ordering.
	oldRetry, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_real", Cursor: first.NextCursor})
	if err != nil || len(oldRetry.Items) != 6 || oldRetry.Snapshot != first.Snapshot {
		t.Fatalf("old cursor retry items=%d snapshot=%q err=%v", len(oldRetry.Items), oldRetry.Snapshot, err)
	}
}

func TestSidebarCollapsesRecoveryChainToLeaf(t *testing.T) {
	dbDir := t.TempDir()
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(dbDir, "sidebar.db") })
	project := testSidebarProjectPlan("project_chain", "Chain", `D:\sessions`, nil)
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{project}, stamps: map[string]string{"project_chain": "v1"}}
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}

	row := func(id, parentID string, recovered bool, activity int64) SidebarSession {
		return SidebarSession{ID: id, GroupID: "project_chain", Scope: "project", TopicID: "topic-chain", Title: id, SessionPath: `D:\sessions\` + id + ".jsonl", Recovered: recovered, ParentID: parentID, LastActivityAt: activity}
	}
	stage := func(rows ...SidebarSession) {
		t.Helper()
		scanned := make([]sidebarScannedFile, len(rows))
		for i := range rows {
			scanned[i] = sidebarScannedFile{path: rows[i].SessionPath, signature: "v1", row: &rows[i]}
		}
		if _, err := applySidebarScan(state.db, "project_chain", `D:\sessions`, "v1", scanned); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(page SidebarSessionPage) map[string]bool {
		out := make(map[string]bool, len(page.Items))
		for _, it := range page.Items {
			out[it.ID] = true
		}
		return out
	}

	// root -> r1 -> r2 -> r3 (leaf): only r3 may surface in list or search.
	stage(row("root", "", false, 0), row("r1", "root", true, 1), row("r2", "r1", true, 2), row("r3", "r2", true, 3))
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_chain"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total == nil || *page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "r3" {
		t.Fatalf("chain leaf list: total=%v items=%v", page.Total, ids(page))
	}
	searched, err := index.search(app, SidebarSearchRequest{Filter: "sessions"})
	if err != nil {
		t.Fatal(err)
	}
	if searched.Total == nil || *searched.Total != 1 || len(searched.Items) != 1 || searched.Items[0].Session == nil || searched.Items[0].Session.ID != "r3" {
		t.Fatalf("chain leaf search: total=%v items=%+v", searched.Total, searched.Items)
	}

	// A different Topic may reuse a branch ID without becoming part of this
	// recovery chain. Topic scope is part of the ancestor key.
	otherRoot := row("root", "", false, 4)
	otherRoot.TopicID = "topic-other"
	otherRoot.SessionPath = `D:\sessions\other-root.jsonl`
	stage(row("root", "", false, 0), row("r1", "root", true, 1), row("r2", "r1", true, 2), row("r3", "r2", true, 3), otherRoot)
	page, err = index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_chain"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total == nil || *page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("cross-topic same ID: total=%v items=%v", page.Total, ids(page))
	}

	// A normal explicit fork (Recovered=false) is not collapsed alongside the
	// recovery leaf.
	stage(row("root", "", false, 0), row("r1", "root", true, 1), row("r2", "r1", true, 2), row("r3", "r2", true, 3), row("fork", "root", false, 4))
	page, err = index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_chain"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total == nil || *page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("chain+fork: total=%v items=%v", page.Total, ids(page))
	}
	got := ids(page)
	if !got["fork"] || !got["r3"] {
		t.Fatalf("chain+fork items=%v, want fork and r3", got)
	}

	// Delete the leaf: the next-deepest recovered ancestor reappears without
	// touching history files.
	stage(row("root", "", false, 0), row("r1", "root", true, 1), row("r2", "r1", true, 2), row("fork", "root", false, 4))
	page, err = index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_chain"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total == nil || *page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("leaf deleted: total=%v items=%v", page.Total, ids(page))
	}
	got = ids(page)
	if !got["fork"] || !got["r2"] {
		t.Fatalf("leaf deleted items=%v, want fork and r2", got)
	}

	// Duplicate / out-of-order re-scan is idempotent.
	stage(row("root", "", false, 0), row("r1", "root", true, 1), row("r2", "r1", true, 2), row("fork", "root", false, 4))
	page, err = index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_chain"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total == nil || *page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("duplicate scan: total=%v items=%v", page.Total, ids(page))
	}
	got = ids(page)
	if !got["fork"] || !got["r2"] {
		t.Fatalf("duplicate scan items=%v, want fork and r2", got)
	}
}

func TestSidebarCollapsesRecoveryChainInGroupCount(t *testing.T) {
	dbDir := t.TempDir()
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(dbDir, "sidebar.db") })
	project := testSidebarProjectPlan("project_rooms_chain", "Rooms", `D:\sessions`, nil)
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{project}, stamps: map[string]string{"project_rooms_chain": "v1"}}
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	rows := []SidebarSession{
		{ID: "room-root", GroupID: "project_rooms_chain", Scope: "project", Title: "Root", SessionPath: `D:\sessions\room-root.jsonl`, SessionSource: "collaboration", LastActivityAt: 1},
		{ID: "room-r1", GroupID: "project_rooms_chain", Scope: "project", Title: "Recovery", SessionPath: `D:\sessions\room-r1.jsonl`, SessionSource: "collaboration", Recovered: true, ParentID: "room-root", LastActivityAt: 2},
	}
	scanned := make([]sidebarScannedFile, len(rows))
	for i := range rows {
		scanned[i] = sidebarScannedFile{path: rows[i].SessionPath, signature: "v1", row: &rows[i]}
	}
	if _, err := applySidebarScan(state.db, "project_rooms_chain", `D:\sessions`, "v1", scanned); err != nil {
		t.Fatal(err)
	}
	groups, err := index.listGroups(app, SidebarRooms)
	if err != nil || len(groups) != 1 || groups[0].SessionCount != 1 {
		t.Fatalf("room groups=%+v err=%v, want single leaf count", groups, err)
	}
}

func TestSidebarRecoverySchemaRebuildsOldRowsFromSidecars(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	topicID := "topic-recovery-schema"
	testSidebarWriteSessionMeta(t, dir, "root.jsonl", agent.BranchMeta{
		ID: "root", Scope: "project", WorkspaceRoot: dir, TopicID: topicID, TopicTitle: "Recovery", Turns: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base,
	})
	leafPath := testSidebarWriteSessionMeta(t, dir, "leaf.jsonl", agent.BranchMeta{
		ID: "leaf", Scope: "project", WorkspaceRoot: dir, TopicID: topicID, TopicTitle: "Recovery", Turns: 1,
		Recovered: true, ParentID: "root", RecoveryDepth: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base.Add(time.Minute),
	})

	plan := testSidebarProjectPlan("project_recovery_schema", "Recovery Schema", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_recovery_schema": "v1"}})
	app := &App{}
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_recovery_schema"})
	if err != nil || first.Total == nil || *first.Total != 1 || len(first.Items) != 1 || first.Items[0].ID != "leaf" {
		t.Fatalf("initial recovery projection=%+v err=%v", first, err)
	}

	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.db.Update(func(tx *bolt.Tx) error {
		rows := tx.Bucket(sidebarBoltRows)
		var stale SidebarSession
		if err := json.Unmarshal(rows.Get([]byte(leafPath)), &stale); err != nil {
			return err
		}
		stale.Recovered, stale.ParentID = false, ""
		encoded, err := json.Marshal(stale)
		if err != nil {
			return err
		}
		if err := rows.Put([]byte(leafPath), encoded); err != nil {
			return err
		}
		// Version 5 predates persisted Recovery/ParentID projection.
		return tx.Bucket(sidebarBoltMeta).Put([]byte("schema"), []byte("5"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.close(app); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.close(app) })

	rebuilt, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_recovery_schema"})
	if err != nil || rebuilt.Total == nil || *rebuilt.Total != 1 || len(rebuilt.Items) != 1 || rebuilt.Items[0].ID != "leaf" {
		t.Fatalf("schema-5 recovery rebuild=%+v err=%v", rebuilt, err)
	}
}

func TestSidebarBoltQueryRefreshesSameSignatureGroupMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicID: "topic", TopicTitle: "Task",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion,
	}); err != nil {
		t.Fatal(err)
	}
	plan := sidebarGroupPlan{
		group: SidebarGroup{ID: "project_meta", Kind: "project", Label: "Alpha", Root: dir, Color: "red", Icon: "go"},
		scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{"topic": "Task"}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{},
	}
	source := &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_meta": "meta-v1"}, items: map[string][]SidebarSession{}, loads: map[string]int{}}
	sidebarDBPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return sidebarDBPath })
	index.source = source
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	first, err := index.search(app, SidebarSearchRequest{Filter: "all"})
	if err != nil || len(first.Items) != 1 || first.Items[0].Group == nil || first.Items[0].Group.Label != "Alpha" {
		t.Fatalf("first metadata = %+v, err=%v", first, err)
	}
	updated := plan
	updated.group.Label, updated.group.Color, updated.group.Icon, updated.group.Pinned = "Beta", "blue", "rust", true
	updated.pinned = map[string]bool{"topic": true}
	source.plansValue = []sidebarGroupPlan{updated}
	source.stamps["project_meta"] = "meta-v2"
	second, err := index.search(app, SidebarSearchRequest{Filter: "all"})
	if err != nil || len(second.Items) != 1 || second.Items[0].Group == nil {
		t.Fatalf("second metadata = %+v, err=%v", second, err)
	}
	group, session := second.Items[0].Group, second.Items[0].Session
	if group.Label != "Beta" || group.Color != "blue" || group.Icon != "rust" || !group.Pinned || session == nil || !session.Pinned || first.Snapshot == second.Snapshot {
		t.Fatalf("refreshed group=%+v session=%+v snapshots=%q/%q", group, session, first.Snapshot, second.Snapshot)
	}
}

func TestSidebarBoltTenThousandIndexPageIsBounded(t *testing.T) {
	dbDir := t.TempDir()
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(dbDir, "sidebar.db") })
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_10k", Kind: "project", Label: "Ten Thousand", SessionCount: 10_000}, scope: "project", titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	crew := sidebarGroupPlan{group: SidebarGroup{ID: "crew_folder", Kind: "crew", Label: "Crew"}, scope: "global", crew: true, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	source := &sidebarTestSource{plansValue: []sidebarGroupPlan{crew, plan}, stamps: map[string]string{"project_10k": "v1", "crew_folder": "v1"}, items: map[string][]SidebarSession{}, loads: map[string]int{}}
	index.source = source
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	scanned := make([]sidebarScannedFile, 10_000)
	for i := range scanned {
		path := fmt.Sprintf(`D:\sessions\%05d.jsonl`, i)
		row := SidebarSession{ID: fmt.Sprintf("session-%05d", i), GroupID: "project_10k", Scope: "project", Title: fmt.Sprintf("Session %05d", i), SessionPath: path, LastActivityAt: int64(i)}
		scanned[i] = sidebarScannedFile{path: path, signature: "v1", row: &row}
	}
	if _, err := applySidebarScan(state.db, "project_10k", `D:\sessions`, "v1", scanned); err != nil {
		t.Fatal(err)
	}
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_10k"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 20 || page.Total == nil || *page.Total != 10_000 || page.NextCursor == "" || page.Items[0].ID != "session-09999" {
		t.Fatalf("10k page items=%d total=%v cursor=%q first=%q", len(page.Items), page.Total, page.NextCursor, page.Items[0].ID)
	}
	signature := sidebarQuerySignature("sessions", string(SidebarProjects), "project_10k")
	index.mu.Lock()
	query := state.queries["sessions\x00"+signature]
	index.mu.Unlock()
	if query == nil || !query.direct || query.generation == "" || query.bucket != "" {
		t.Fatalf("10k project page materialized query bucket: %+v", query)
	}
	if err := state.db.View(func(tx *bolt.Tx) error {
		key, _ := tx.Bucket(sidebarBoltQueries).Cursor().First()
		if key != nil {
			t.Fatalf("project first page created query bucket %q", key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSidebarBoltPendingGenerationRecoversAfterBuildOrPublishFailure(t *testing.T) {
	for _, faultAt := range []string{"build", "publish"} {
		t.Run(faultAt, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "session.jsonl")
			if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			base := time.Now().UTC().Add(-time.Hour)
			meta := agent.BranchMeta{ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Before", Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base}
			if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
				t.Fatal(err)
			}
			plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_pending", Kind: "project", Label: "Pending", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
			dbPath := filepath.Join(t.TempDir(), "sidebar.db")
			index := newSidebarBoltIndex(func(*App) string { return dbPath })
			index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_pending": "v1"}}
			app := &App{}
			t.Cleanup(func() { _ = index.close(app) })
			first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_pending"})
			if err != nil || len(first.Items) != 1 || first.Items[0].Title != "Before" {
				t.Fatalf("initial page=%+v err=%v", first, err)
			}
			state, err := index.open(app)
			if err != nil {
				t.Fatal(err)
			}
			var activeBefore string
			if err := state.db.View(func(tx *bolt.Tx) error {
				activeBefore = string(tx.Bucket(sidebarBoltMeta).Get([]byte("active:project_pending")))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			meta.TopicTitle, meta.UpdatedAt = "After", base.Add(time.Hour)
			if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
				t.Fatal(err)
			}
			index.markDirty(app, agent.BranchMetaPath(path))
			fired := false
			fault := func() error {
				if !fired {
					fired = true
					return errors.New("injected " + faultAt + " failure")
				}
				return nil
			}
			if faultAt == "build" {
				index.buildFault = fault
			} else {
				index.publishFault = fault
			}
			if _, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_pending"}); err == nil || !strings.Contains(err.Error(), "injected "+faultAt) {
				t.Fatalf("injected failure err=%v", err)
			}
			if err := state.db.View(func(tx *bolt.Tx) error {
				metaBucket := tx.Bucket(sidebarBoltMeta)
				if len(metaBucket.Get([]byte("pending:project_pending"))) == 0 {
					t.Fatal("failed publish lost persistent pending marker")
				}
				if active := string(metaBucket.Get([]byte("active:project_pending"))); active != activeBefore {
					t.Fatalf("failed publish changed active generation: %q/%q", activeBefore, active)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := index.close(app); err != nil {
				t.Fatal(err)
			}
			index.buildFault, index.publishFault = nil, nil
			recovered, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_pending"})
			if err != nil || len(recovered.Items) != 1 || recovered.Items[0].Title != "After" {
				t.Fatalf("recovered page=%+v err=%v", recovered, err)
			}
			state, err = index.open(app)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.db.View(func(tx *bolt.Tx) error {
				metaBucket := tx.Bucket(sidebarBoltMeta)
				if pending := metaBucket.Get([]byte("pending:project_pending")); pending != nil {
					t.Fatalf("successful publish retained pending marker: %x", pending)
				}
				if active := string(metaBucket.Get([]byte("active:project_pending"))); active == activeBefore {
					t.Fatalf("successful retry did not advance active generation: %q", active)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSidebarBoltCrewRouteAddAndRemoveReclassifiesProjectSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "channel.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		ID: "channel-session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Channel session", Turns: 1,
		SessionSource: "auto", Channel: "wechat", SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	project := sidebarGroupPlan{group: SidebarGroup{ID: "project_routes", Kind: "project", Label: "Routes", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	crew := sidebarGroupPlan{group: SidebarGroup{ID: "crew_folder", Kind: "crew", Label: "Crew"}, scope: "global", crew: true, dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	routeVersion := "none"
	source := &sidebarTestSource{plansValue: []sidebarGroupPlan{project}, stampFunc: func(plan sidebarGroupPlan) string { return plan.group.ID + ":" + routeVersion }}
	routes := map[string]channelSessionRoute{}
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	index.source = source
	index.routes = func() map[string]channelSessionRoute { return routes }
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	ordinary, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: project.group.ID})
	if err != nil || ordinary.Total == nil || *ordinary.Total != 1 || ordinary.Items[0].ID != "channel-session" {
		t.Fatalf("initial ordinary page=%+v err=%v", ordinary, err)
	}
	routes = map[string]channelSessionRoute{sessionRuntimeKey(path): {channel: "wechat", sessionSource: "auto"}}
	routeVersion = "configured"
	source.plansValue = []sidebarGroupPlan{crew, project}
	ordinary, err = index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: project.group.ID})
	if err != nil || ordinary.Total == nil || *ordinary.Total != 0 {
		t.Fatalf("routed project page=%+v err=%v", ordinary, err)
	}
	crewPage, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: crew.group.ID})
	if err != nil || crewPage.Total == nil || *crewPage.Total != 1 || len(crewPage.Items) != 1 || crewPage.Items[0].ID != "channel-session" {
		t.Fatalf("routed crew page=%+v err=%v", crewPage, err)
	}

	routes = map[string]channelSessionRoute{}
	routeVersion = "removed"
	source.plansValue = []sidebarGroupPlan{project}
	returned, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: project.group.ID})
	if err != nil || returned.Total == nil || *returned.Total != 1 || len(returned.Items) != 1 || returned.Items[0].ID != "channel-session" {
		t.Fatalf("returned project page=%+v err=%v", returned, err)
	}
}

func TestSidebarBoltHighWaterResetsDerivedIndexAndAllowsRetry(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_capacity", Kind: "project", Label: "Capacity"}, scope: "project", titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_capacity": "v1"}}
	app := &App{}
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("capacity-test"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("padding"), bytes.Repeat([]byte("x"), 64<<10))
	}); err != nil {
		t.Fatal(err)
	}
	index.maxBytes = 1
	if _, err := index.listGroups(app, SidebarProjects); err == nil || !strings.Contains(err.Error(), "derived index was reset; retry") {
		t.Fatalf("high-water error=%v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("oversized derived index was not removed: %v", err)
	}
	index.maxBytes = 512 << 20
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_capacity"})
	if err != nil || page.Total == nil || *page.Total != 0 {
		t.Fatalf("retry page=%+v err=%v", page, err)
	}
	t.Cleanup(func() { _ = index.close(app) })
}

func TestSidebarBoltLifecycleSerializesResetAndReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	app := &App{}
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("capacity-test"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("padding"), bytes.Repeat([]byte("x"), 64<<10))
	}); err != nil {
		t.Fatal(err)
	}
	index.maxBytes = 1
	resetWindow := make(chan struct{})
	releaseReset := make(chan struct{})
	index.resetHook = func() {
		close(resetWindow)
		<-releaseReset
	}
	resetDone := make(chan error, 1)
	go func() { resetDone <- index.enforceCapacity(app) }()
	<-resetWindow
	openDone := make(chan *sidebarBoltAppState, 1)
	openErr := make(chan error, 1)
	go func() {
		state, err := index.open(app)
		openDone <- state
		openErr <- err
	}()
	select {
	case <-openDone:
		t.Fatal("reopen crossed the close/remove reset window")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseReset)
	if err := <-resetDone; err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("reset error=%v", err)
	}
	reopened := <-openDone
	if err := <-openErr; err != nil || reopened == nil || reopened.db == nil {
		t.Fatalf("reopen state=%+v err=%v", reopened, err)
	}
	index.resetHook = nil
	index.maxBytes = 512 << 20
	if err := reopened.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(sidebarBoltMeta).Put([]byte("reopened"), []byte("kept"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("old reset removed reopened database: %v", err)
	}
	t.Cleanup(func() { _ = index.close(app) })
}

func TestSidebarBoltLifecycleOpenQueryResetStress(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	index.maxBytes = 1
	app := &App{}
	var wait sync.WaitGroup
	errs := make(chan error, 500)
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 50 {
				state, err := index.open(app)
				if err != nil {
					errs <- err
					continue
				}
				err = state.db.View(func(tx *bolt.Tx) error {
					_ = tx.Bucket(sidebarBoltMeta).Get([]byte("schema"))
					return nil
				})
				if err != nil && !errors.Is(err, bolt.ErrDatabaseNotOpen) {
					errs <- err
				}
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		for range 50 {
			err := index.enforceCapacity(app)
			if err != nil && !strings.Contains(err.Error(), "retry") {
				errs <- err
			}
		}
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("lifecycle stress: %v", err)
	}
	index.maxBytes = 512 << 20
	state, err := index.open(app)
	if err != nil || state == nil || state.db == nil {
		t.Fatalf("final reopen state=%+v err=%v", state, err)
	}
	if err := state.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(sidebarBoltMeta) == nil {
			t.Fatal("final reopened index was not initialized")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.close(app) })
}

func TestSidebarBoltSchemaMismatchSafelyRebuilds(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Recovered", Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_schema", Kind: "project", Label: "Schema", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_schema": "v1"}}
	app := &App{}
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_schema"})
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("initial query=%+v err=%v", first, err)
	}
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(sidebarBoltMeta).Put([]byte("schema"), []byte(fmt.Sprint(sidebarBoltSchema+1))); err != nil {
			return err
		}
		return tx.Bucket(sidebarBoltRows).Put([]byte("stale"), []byte("row"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.close(app); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.close(app) })
	rebuilt, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_schema"})
	if err != nil || len(rebuilt.Items) != 1 || rebuilt.Items[0].ID != "session" {
		t.Fatalf("schema rebuild query=%+v err=%v", rebuilt, err)
	}
}

func TestSidebarBoltCorruptFileIsDisposable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	if err := os.WriteFile(dbPath, []byte("not a bolt database"), 0o600); err != nil {
		t.Fatal(err)
	}
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	app := &App{}
	if _, err := index.open(app); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.close(app) })
}

func TestSidebarBoltQueryLRUEvictsCursor(t *testing.T) {
	dbDir := t.TempDir()
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(dbDir, "sidebar.db") })
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_lru", Kind: "project", Label: "LRU"}, scope: "project", titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_lru": "v1"}}
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	scanned := make([]sidebarScannedFile, 21)
	for i := range scanned {
		path := fmt.Sprintf(`D:\sessions\lru-%02d.jsonl`, i)
		row := SidebarSession{ID: fmt.Sprintf("lru-%02d", i), GroupID: "project_lru", Scope: "project", Title: "Session", SessionPath: path, LastActivityAt: int64(i)}
		scanned[i] = sidebarScannedFile{path: path, signature: "v1", row: &row}
	}
	if _, err := applySidebarScan(state.db, "project_lru", `D:\sessions`, "v1", scanned); err != nil {
		t.Fatal(err)
	}
	first, err := index.search(app, SidebarSearchRequest{Query: "Session", Filter: "sessions"})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first search cursor=%q err=%v", first.NextCursor, err)
	}
	cursor, err := decodeSidebarCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	malformed := cursor
	malformed.LastKey = "***"
	badCursor, err := encodeSidebarCursor(malformed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.search(app, SidebarSearchRequest{Query: "Session", Filter: "sessions", Cursor: badCursor}); !errors.Is(err, errInvalidSidebarCursor) {
		t.Fatalf("malformed lastKey error=%v", err)
	}
	index.mu.Lock()
	original := state.byRev[cursor.Version]
	state.byRev[cursor.Version] = &sidebarBoltQuery{revision: cursor.Version, kind: "sessions", signature: cursor.Signature}
	index.mu.Unlock()
	if _, err := index.search(app, SidebarSearchRequest{Query: "Session", Filter: "sessions", Cursor: first.NextCursor}); !errors.Is(err, errInvalidSidebarCursor) {
		t.Fatalf("cross-query revision error=%v", err)
	}
	index.mu.Lock()
	state.byRev[cursor.Version] = original
	index.mu.Unlock()
	for i := 0; i < maxSidebarQueryCache; i++ {
		if _, err := index.search(app, SidebarSearchRequest{Query: fmt.Sprintf("missing-%d", i), Filter: "sessions"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := index.search(app, SidebarSearchRequest{Query: "Session", Filter: "sessions", Cursor: first.NextCursor}); !errors.Is(err, errInvalidSidebarCursor) {
		t.Fatalf("evicted cursor error=%v", err)
	}
}

func TestSidebarBoltClassifiesLegacySourcesAndCrew(t *testing.T) {
	dbDir := t.TempDir()
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(dbDir, "sidebar.db") })
	project := sidebarGroupPlan{group: SidebarGroup{ID: "project_modes", Kind: "project", Label: "Modes"}, scope: "project", titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	crew := sidebarGroupPlan{group: SidebarGroup{ID: "crew_folder", Kind: "crew", Label: "Crew"}, scope: "global", crew: true, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{project, crew}, stamps: map[string]string{"project_modes": "v1", "crew_folder": "v1"}}
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	rows := []SidebarSession{
		{ID: "normal", GroupID: "project_modes", Scope: "project", Title: "Normal", SessionPath: `D:\sessions\normal.jsonl`, LastActivityAt: 4},
		{ID: "room", GroupID: "project_modes", Scope: "project", Title: "Room", SessionPath: `D:\sessions\room.jsonl`, SessionSource: "collaboration", LastActivityAt: 3},
		{ID: "assist", GroupID: "project_modes", Scope: "project", Title: "Assistant", SessionPath: `D:\sessions\assist.jsonl`, SessionSource: "assist", LastActivityAt: 2},
		{ID: "crew", GroupID: "project_modes", Scope: "project", Title: "Crew Chat", SessionPath: `D:\sessions\crew.jsonl`, SessionSource: "wechat", Channel: "wechat", LastActivityAt: 1},
	}
	scanned := make([]sidebarScannedFile, len(rows))
	for i := range rows {
		scanned[i] = sidebarScannedFile{path: rows[i].SessionPath, signature: "v1", row: &rows[i]}
	}
	if _, err := applySidebarScan(state.db, "project_modes", `D:\sessions`, "v1", scanned); err != nil {
		t.Fatal(err)
	}
	directView := index.view
	viewCount := 0
	index.view = func(db *bolt.DB, fn func(*bolt.Tx) error) error {
		viewCount++
		return directView(db, fn)
	}
	roomGroups, err := index.listGroups(app, SidebarRooms)
	if err != nil || len(roomGroups) != 1 || roomGroups[0].ID != "project_modes" || roomGroups[0].SessionCount != 1 {
		t.Fatalf("room groups=%+v err=%v", roomGroups, err)
	}
	if viewCount != 1 {
		t.Fatalf("ROOM group stats scanned rows %d times, want once", viewCount)
	}
	viewCount = 0
	assistantGroups, err := index.listGroups(app, SidebarAssistants)
	if err != nil || len(assistantGroups) != 1 || assistantGroups[0].ID != "project_modes" || assistantGroups[0].SessionCount != 1 {
		t.Fatalf("assistant groups=%+v err=%v", assistantGroups, err)
	}
	if viewCount != 1 {
		t.Fatalf("assistant group stats scanned rows %d times, want once", viewCount)
	}
	projectPage, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_modes"})
	if err != nil || projectPage.Total == nil || *projectPage.Total != 1 || len(projectPage.Items) != 1 || projectPage.Items[0].ID != "normal" {
		t.Fatalf("project page=%+v err=%v", projectPage, err)
	}
	roomPage, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarRooms, GroupID: "project_modes"})
	if err != nil || roomPage.Total == nil || *roomPage.Total != 1 || len(roomPage.Items) != 1 || roomPage.Items[0].ID != "room" {
		t.Fatalf("room page=%+v err=%v", roomPage, err)
	}
	assistantPage, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarAssistants, GroupID: "project_modes"})
	if err != nil || assistantPage.Total == nil || *assistantPage.Total != 1 || len(assistantPage.Items) != 1 || assistantPage.Items[0].ID != "assist" {
		t.Fatalf("assistant page=%+v err=%v", assistantPage, err)
	}
	crewPage, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "crew_folder"})
	if err != nil || len(crewPage.Items) != 1 || crewPage.Items[0].ID != "crew" || crewPage.Items[0].GroupID != "crew_folder" {
		t.Fatalf("crew page=%+v err=%v", crewPage, err)
	}
	searched, err := index.search(app, SidebarSearchRequest{Query: "Crew Chat", Filter: "sessions"})
	if err != nil || len(searched.Items) != 1 || searched.Items[0].Group == nil || searched.Items[0].Group.ID != "crew_folder" {
		t.Fatalf("crew search=%+v err=%v", searched, err)
	}
}

func TestSidebarSidecarReadDoesNotWriteTruthSource(t *testing.T) {
	dir := t.TempDir()
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "global_folder", Kind: "global", Label: "Global"}, scope: "global", titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(t.TempDir(), "sidebar.db") })

	missing := filepath.Join(dir, "missing-meta.jsonl")
	if err := os.WriteFile(missing, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := index.sidebarRowFromSidecar(plan, missing, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agent.BranchMetaPath(missing)); !os.IsNotExist(err) {
		t.Fatalf("index read created missing sidecar: %v", err)
	}

	legacy := filepath.Join(dir, "legacy.jsonl")
	if err := os.WriteFile(legacy, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(legacy, agent.BranchMeta{ID: "legacy", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), SchemaVersion: 0}); err != nil {
		t.Fatal(err)
	}
	metaPath := agent.BranchMetaPath(legacy)
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := index.sidebarRowFromSidecar(plan, legacy, false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("index read rewrote legacy sidecar")
	}
}

func TestSidebarBoltPeriodicAuditFindsContentChangeWithoutDirMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	meta := agent.BranchMeta{ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Before", Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base}
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_audit", Kind: "project", Label: "Audit", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	sidebarDBPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return sidebarDBPath })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_audit": "v1"}}
	fakeNow := time.Now()
	index.now = func() time.Time { return fakeNow }
	index.auditEvery = time.Second
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_audit"})
	if err != nil || len(first.Items) != 1 || first.Items[0].Title != "Before" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.CustomTitle = "After"
	meta.UpdatedAt = base.Add(time.Hour)
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(agent.BranchMetaPath(path), fakeNow.Add(time.Minute), fakeNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, dirInfo.ModTime(), dirInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	stillCached, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_audit"})
	if err != nil || len(stillCached.Items) != 1 || stillCached.Items[0].Title != "Before" {
		t.Fatalf("query before audit=%+v err=%v", stillCached, err)
	}
	fakeNow = fakeNow.Add(2 * time.Second)
	refreshed, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_audit"})
	if err != nil || len(refreshed.Items) != 1 || refreshed.Items[0].Title != "After" || refreshed.Snapshot == first.Snapshot {
		t.Fatalf("query after audit=%+v err=%v", refreshed, err)
	}
}

func TestSidebarBoltSearchSkipsPeriodicAudit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	meta := agent.BranchMeta{ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Before", Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base}
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_search_audit", Kind: "project", Label: "Search Audit", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_search_audit": "v1"}}
	fakeNow := time.Now()
	index.now = func() time.Time { return fakeNow }
	index.auditEvery = time.Second
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_search_audit"})
	if err != nil || len(first.Items) != 1 || first.Items[0].Title != "Before" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.CustomTitle = "After"
	meta.UpdatedAt = base.Add(time.Hour)
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, dirInfo.ModTime(), dirInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	fakeNow = fakeNow.Add(2 * time.Second)
	searched, err := index.search(app, SidebarSearchRequest{Filter: "sessions"})
	if err != nil || len(searched.Items) != 1 || searched.Items[0].Session == nil || searched.Items[0].Session.Title != "Before" {
		t.Fatalf("search=%+v err=%v", searched, err)
	}
	refreshed, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_search_audit"})
	if err != nil || len(refreshed.Items) != 1 || refreshed.Items[0].Title != "After" {
		t.Fatalf("refreshed=%+v err=%v", refreshed, err)
	}
}

func TestSidebarBoltSearchMatchesPreviewOnlyKeyword(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	// 关键词「保险」落在 18 字标题截断窗口之外，只存在于 sidecar 已缓存的
	// Preview 字段，而 Title / SessionPath / group Label / Root 都不包含它。
	// 这对应小组件 DesktopIconSearch 的 haystack 覆盖，主窗口搜索也必须命中。
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir,
		CustomTitle: "普通会话", TopicTitle: "普通会话",
		Preview: "aaaaaaaaaaaaaaaaaaaa保险", Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion,
		CreatedAt: base, UpdatedAt: base,
	})
	plan := testSidebarProjectPlan("project_preview", "Search Preview", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_preview": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	all, err := index.search(app, SidebarSearchRequest{Query: "保险", Filter: "all"})
	if err != nil || len(all.Items) != 1 || all.Items[0].Session == nil || all.Items[0].Session.Preview == "" {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	sessions, err := index.search(app, SidebarSearchRequest{Query: "保险", Filter: "sessions"})
	if err != nil || len(sessions.Items) != 1 {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
	projects, err := index.search(app, SidebarSearchRequest{Query: "保险", Filter: "projects"})
	if err != nil || len(projects.Items) != 0 {
		t.Fatalf("projects must not match a session-only preview keyword: %+v err=%v", projects, err)
	}
	miss, err := index.search(app, SidebarSearchRequest{Query: "不在任何字段", Filter: "all"})
	if err != nil || len(miss.Items) != 0 {
		t.Fatalf("miss=%+v err=%v", miss, err)
	}
}

func TestSidebarBoltAuditWithoutChangesKeepsGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Stable", Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_stable", Kind: "project", Label: "Stable", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	sidebarDBPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return sidebarDBPath })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_stable": "v1"}}
	fakeNow := time.Now()
	index.now = func() time.Time { return fakeNow }
	index.auditEvery = time.Second
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_stable"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	readGeneration := func() uint64 {
		var generation uint64
		if err := state.db.View(func(tx *bolt.Tx) error {
			generation = bytesToUint64(tx.Bucket(sidebarBoltMeta).Get([]byte("generation:project_stable")))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return generation
	}
	before := readGeneration()
	fakeNow = fakeNow.Add(2 * time.Second)
	second, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_stable"})
	if err != nil {
		t.Fatal(err)
	}
	if after := readGeneration(); before == 0 || after != before || second.Snapshot != first.Snapshot {
		t.Fatalf("no-op audit generation=%d/%d snapshot=%q/%q", before, after, first.Snapshot, second.Snapshot)
	}
}

func TestSidebarBoltDirtySignalFindsContentChangeImmediately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := agent.BranchMeta{ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Before", Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(agent.BranchMetaPath(path), future, future); err != nil {
		t.Fatal(err)
	}
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_dirty", Kind: "project", Label: "Dirty", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	sidebarDBPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return sidebarDBPath })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_dirty": "v1"}}
	index.auditEvery = time.Hour
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_dirty"})
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.CustomTitle = "Dirty Update"
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, dirInfo.ModTime(), dirInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	index.markDirty(app, agent.BranchMetaPath(path))
	refreshed, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_dirty"})
	if err != nil || len(refreshed.Items) != 1 || refreshed.Items[0].Title != "Dirty Update" || refreshed.Snapshot == first.Snapshot {
		t.Fatalf("dirty refresh=%+v err=%v", refreshed, err)
	}
}

func TestSidebarBoltDirtyTranscriptRefreshesDerivedCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	session := agent.NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "first prompt"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("load transcript meta ok=%v err=%v", ok, err)
	}
	meta.Scope, meta.WorkspaceRoot = "project", dir
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_transcript", Kind: "project", Label: "Transcript", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	sidebarDBPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return sidebarDBPath })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_transcript": "v1"}}
	index.auditEvery = time.Hour
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_transcript"})
	if err != nil || len(first.Items) != 1 || first.Items[0].Turns != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	metaPath := agent.BranchMetaPath(path)
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	metaInfo, err := os.Stat(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	session.Add(provider.Message{Role: provider.RoleUser, Content: "second prompt"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(metaPath, metaInfo.ModTime(), metaInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, dirInfo.ModTime(), dirInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	index.markDirty(app, path)
	refreshed, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_transcript"})
	if err != nil || len(refreshed.Items) != 1 || refreshed.Items[0].Turns != 2 || refreshed.Snapshot == first.Snapshot {
		t.Fatalf("transcript refresh=%+v err=%v", refreshed, err)
	}
}

func TestSidebarBoltLegacyProjectSessionWithoutMetaIsVisible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	session := agent.NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "legacy project prompt"})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(agent.BranchMetaPath(path)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_legacy", Kind: "project", Label: "Legacy", Root: dir}, scope: "project", root: normalizeProjectRoot(dir), dirs: []string{dir}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	sidebarDBPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return sidebarDBPath })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_legacy": "v1"}}
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_legacy"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("legacy project page=%+v err=%v", page, err)
	}
	row := page.Items[0]
	if row.Scope != "project" || row.WorkspaceRoot != normalizeProjectRoot(dir) || !strings.HasPrefix(row.Title, "legacy project pro") {
		t.Fatalf("legacy project row=%+v", row)
	}
	if _, err := os.Stat(agent.BranchMetaPath(path)); !os.IsNotExist(err) {
		t.Fatalf("legacy index read created sidecar: %v", err)
	}
}

func TestSidebarBoltCorruptionAfterOpenResetsAndRetries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_fault", Kind: "project", Label: "Fault"}, scope: "project", dirs: []string{t.TempDir()}, titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_fault": "v1"}}
	app := &App{}
	if _, err := index.open(app); err != nil {
		t.Fatal(err)
	}
	fired := false
	directView := index.view
	index.view = func(db *bolt.DB, fn func(*bolt.Tx) error) error {
		if !fired {
			fired = true
			return directView(db, func(*bolt.Tx) error { panic("invalid page type") })
		}
		return directView(db, fn)
	}
	if _, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_fault"}); !errors.Is(err, bolt.ErrChecksum) {
		t.Fatalf("corruption error=%v", err)
	}
	index.mu.Lock()
	_, stillOpen := index.states[app]
	index.mu.Unlock()
	if stillOpen {
		t.Fatal("corrupt index state stayed open")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt derived index was not isolated: %v", err)
	}
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_fault"})
	if err != nil || page.Total == nil || *page.Total != 0 {
		t.Fatalf("retry page=%+v err=%v", page, err)
	}
	t.Cleanup(func() { _ = index.close(app) })
}

func TestSidebarBoltUpdateCorruptionAfterOpenResetsAndRetries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_update_fault", Kind: "project", Label: "Fault"}, scope: "project", titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_update_fault": "v1"}}
	app := &App{}
	if _, err := index.open(app); err != nil {
		t.Fatal(err)
	}
	fired := false
	directUpdate := index.update
	index.update = func(db *bolt.DB, fn func(*bolt.Tx) error) error {
		if !fired {
			fired = true
			return directUpdate(db, func(*bolt.Tx) error { panic("checksum page failure") })
		}
		return directUpdate(db, fn)
	}
	if _, err := index.search(app, SidebarSearchRequest{Query: "Fault", Filter: "projects"}); !errors.Is(err, bolt.ErrChecksum) {
		t.Fatalf("update corruption error=%v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt derived index was not isolated: %v", err)
	}
	page, err := index.search(app, SidebarSearchRequest{Query: "Fault", Filter: "projects"})
	if err != nil || page.Total == nil || *page.Total != 1 {
		t.Fatalf("retry page=%+v err=%v", page, err)
	}
	t.Cleanup(func() { _ = index.close(app) })
}

func TestSidebarBoltQueryWaitsForGroupSyncPublish(t *testing.T) {
	dbDir := t.TempDir()
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(dbDir, "sidebar.db") })
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_sync", Kind: "project", Label: "Sync"}, scope: "project", titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{}}
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_sync": "v1"}}
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	lock := index.planLock(app, "project_sync")
	lock.Lock()
	done := make(chan SidebarSessionPage, 1)
	errs := make(chan error, 1)
	go func() {
		page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_sync"})
		done <- page
		errs <- err
	}()
	select {
	case <-done:
		lock.Unlock()
		t.Fatal("query observed group while sync lock was held")
	case <-time.After(30 * time.Millisecond):
	}
	state, err := index.open(app)
	if err != nil {
		lock.Unlock()
		t.Fatal(err)
	}
	row := SidebarSession{ID: "published", GroupID: "project_sync", Scope: "project", Title: "Published", SessionPath: `D:\sessions\published.jsonl`, LastActivityAt: 1}
	if _, err := applySidebarScan(state.db, "project_sync", `D:\sessions`, "v1", []sidebarScannedFile{{path: row.SessionPath, signature: "v1", row: &row}}); err != nil {
		lock.Unlock()
		t.Fatal(err)
	}
	lock.Unlock()
	page := <-done
	if err := <-errs; err != nil || len(page.Items) != 1 || page.Items[0].ID != "published" {
		t.Fatalf("published page=%+v err=%v", page, err)
	}
}

func TestSidebarBoltCloseReleasesAppStateAndLocks(t *testing.T) {
	sidebarDBPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return sidebarDBPath })
	app := &App{}
	if _, err := index.open(app); err != nil {
		t.Fatal(err)
	}
	_ = index.planLock(app, "project")
	_ = index.planLock(app, "query:test")
	if err := index.close(app); err != nil {
		t.Fatal(err)
	}
	index.mu.Lock()
	_, hasState := index.states[app]
	index.mu.Unlock()
	prefix := fmt.Sprintf("%p\x00", app)
	index.locksMu.Lock()
	remainingLocks := 0
	for key := range index.syncLocks {
		if strings.HasPrefix(key, prefix) {
			remainingLocks++
		}
	}
	index.locksMu.Unlock()
	if hasState || remainingLocks != 0 {
		t.Fatalf("close leaked state=%t locks=%d", hasState, remainingLocks)
	}
	if err := index.close(app); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	index.mu.Lock()
	_, recreated := index.states[app]
	index.mu.Unlock()
	if recreated {
		t.Fatal("idempotent close recreated app state")
	}
}

func TestSessionWatcherAddStopLifecycleStress(t *testing.T) {
	dir := t.TempDir()
	for range 50 {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			t.Fatal(err)
		}
		app := &App{sessionDirsOverride: []string{dir}}
		sw := &sessionWatcher{app: app, w: watcher, dirs: map[string]bool{}}
		var wait sync.WaitGroup
		for worker := 0; worker < 4; worker++ {
			wait.Add(1)
			go func(worker int) {
				defer wait.Done()
				for i := 0; i < 20; i++ {
					switch worker {
					case 0:
						sw.addDir(dir)
					case 1:
						sw.refreshDirs()
					case 2:
						sw.scheduleNotify()
					default:
						sw.stop()
					}
				}
			}(worker)
		}
		wait.Wait()
		sw.stop()
		sw.mu.Lock()
		stopped := sw.stopped
		timer := sw.timer
		sw.mu.Unlock()
		if !stopped || timer != nil {
			t.Fatalf("watcher lifecycle stopped=%t timer=%v", stopped, timer)
		}
	}
}

type sidebarTestSource struct {
	plansValue []sidebarGroupPlan
	stamps     map[string]string
	stampFunc  func(sidebarGroupPlan) string
	loads      map[string]int
	items      map[string][]SidebarSession
}

func (s *sidebarTestSource) plans(*App) ([]sidebarGroupPlan, error) {
	return append([]sidebarGroupPlan{}, s.plansValue...), nil
}

func (s *sidebarTestSource) stamp(_ *App, plan sidebarGroupPlan) string {
	if s.stampFunc != nil {
		return s.stampFunc(plan)
	}
	return s.stamps[plan.group.ID]
}

func testSidebarProjectPlan(id, label, root string, dirs []string) sidebarGroupPlan {
	return sidebarGroupPlan{
		group: SidebarGroup{ID: id, Kind: "project", Label: label, Root: root},
		scope: "project", root: normalizeProjectRoot(root), dirs: dirs,
		titles: map[string]string{}, titleSource: map[string]string{}, createdAt: map[string]int64{}, pinned: map[string]bool{},
	}
}

func testSidebarWriteBrokenMeta(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent.BranchMetaPath(path), []byte("{ this is not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func corruptSidebarMeta(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(agent.BranchMetaPath(path), []byte("{ corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testSidebarWriteSessionMeta(t *testing.T, dir, name string, meta agent.BranchMeta) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	return path
}

// testSidebarBoltIndex builds an index whose dbPath is computed once, avoiding
// the "t.TempDir() per closure call" bug that silently opened a fresh database
// on every path() evaluation.
func testSidebarBoltIndex(t *testing.T, source sidebarPlanSource) *sidebarBoltIndex {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	index.source = source
	return index
}

func testSidebarReadGeneration(t *testing.T, index *sidebarBoltIndex, app *App, groupID string) uint64 {
	t.Helper()
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	var generation uint64
	if err := state.db.View(func(tx *bolt.Tx) error {
		generation = bytesToUint64(tx.Bucket(sidebarBoltMeta).Get([]byte("generation:" + groupID)))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestSidebarBoltIsolatesBrokenSidecarAndKeepsHealthyRooms(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	testSidebarWriteSessionMeta(t, dir, "room.jsonl", agent.BranchMeta{
		ID: "room", Scope: "project", WorkspaceRoot: dir, TopicID: "room-topic", TopicTitle: "RoomMarker",
		SessionKind: agent.SessionKindCollaboration, Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion,
		CreatedAt: base, UpdatedAt: base,
	})
	testSidebarWriteBrokenMeta(t, dir, "broken.jsonl")

	plan := testSidebarProjectPlan("project_rooms", "Rooms", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_rooms": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	groups, err := index.listGroups(app, SidebarRooms)
	if err != nil || len(groups) != 1 || groups[0].ID != "project_rooms" || groups[0].SessionCount != 1 {
		t.Fatalf("room groups=%+v err=%v", groups, err)
	}
	// A never-indexed (cold) broken sidecar is owned by Projects, so it must not
	// warn in the ROOM view even though it shares a directory with a healthy ROOM.
	roomIssues, err := index.listIssues(app, SidebarRooms)
	if err != nil {
		t.Fatal(err)
	}
	if len(roomIssues) != 0 {
		t.Fatalf("cold broken sidecar leaked into ROOM issues=%+v", roomIssues)
	}
	projectIssues, err := index.listIssues(app, SidebarProjects)
	if err != nil || len(projectIssues) != 1 || projectIssues[0].Code != "meta_decode" || !projectIssues[0].Retryable || projectIssues[0].ObservedAt == 0 {
		t.Fatalf("project issues=%+v err=%v", projectIssues, err)
	}
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarRooms, GroupID: "project_rooms"})
	if err != nil || page.Total == nil || *page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "room" {
		t.Fatalf("rooms page=%+v err=%v", page, err)
	}
	searched, err := index.search(app, SidebarSearchRequest{Filter: "all", Query: "RoomMarker"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range searched.Items {
		if item.Session != nil && item.Session.ID == "room" {
			found = true
		}
	}
	if !found {
		t.Fatalf("search did not return the healthy ROOM: items=%+v", searched.Items)
	}
}

func TestSidebarIssueOwnershipFromOldRow(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	roomPath := testSidebarWriteSessionMeta(t, dir, "room.jsonl", agent.BranchMeta{
		ID: "room", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Room",
		SessionKind: agent.SessionKindCollaboration, Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion,
		CreatedAt: base, UpdatedAt: base,
	})
	plan := testSidebarProjectPlan("project_room_oldrow", "Room", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_room_oldrow": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarRooms, GroupID: "project_room_oldrow"})
	if err != nil || first.Total == nil || *first.Total != 1 {
		t.Fatalf("initial room page=%+v err=%v", first, err)
	}
	corruptSidebarMeta(t, roomPath)
	index.markDirty(app, agent.BranchMetaPath(roomPath))
	second, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarRooms, GroupID: "project_room_oldrow"})
	if err != nil || second.Total == nil || *second.Total != 0 {
		t.Fatalf("corrupted room page=%+v err=%v", second, err)
	}
	roomIssues, err := index.listIssues(app, SidebarRooms)
	if err != nil || len(roomIssues) != 1 || roomIssues[0].Code != "meta_decode" {
		t.Fatalf("room issues=%+v err=%v", roomIssues, err)
	}
	if projectIssues, _ := index.listIssues(app, SidebarProjects); len(projectIssues) != 1 {
		t.Fatalf("project issues=%+v", projectIssues)
	}
}

func TestSidebarIssueMixedProjectNormalAndRoom(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	normalPath := testSidebarWriteSessionMeta(t, dir, "normal.jsonl", agent.BranchMeta{
		ID: "normal", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Normal", Turns: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base,
	})
	testSidebarWriteSessionMeta(t, dir, "room.jsonl", agent.BranchMeta{
		ID: "room", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Room",
		SessionKind: agent.SessionKindCollaboration, Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion,
		CreatedAt: base, UpdatedAt: base,
	})
	plan := testSidebarProjectPlan("project_mixed", "Mixed", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_mixed": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	if _, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_mixed"}); err != nil {
		t.Fatal(err)
	}
	corruptSidebarMeta(t, normalPath)
	index.markDirty(app, agent.BranchMetaPath(normalPath))
	if _, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_mixed"}); err != nil {
		t.Fatal(err)
	}
	if roomIssues, _ := index.listIssues(app, SidebarRooms); len(roomIssues) != 0 {
		t.Fatalf("normal break leaked into ROOM: %+v", roomIssues)
	}
	if projectIssues, _ := index.listIssues(app, SidebarProjects); len(projectIssues) != 1 {
		t.Fatalf("project issues=%+v", projectIssues)
	}
}

func TestSidebarBoltBrokenProjectSidecarDoesNotAffectRooms(t *testing.T) {
	projectDir := t.TempDir()
	roomDir := t.TempDir()
	testSidebarWriteBrokenMeta(t, projectDir, "broken.jsonl")

	base := time.Now().UTC().Add(-time.Hour)
	testSidebarWriteSessionMeta(t, roomDir, "room.jsonl", agent.BranchMeta{
		ID: "room", Scope: "project", WorkspaceRoot: roomDir, TopicTitle: "Room",
		SessionKind: agent.SessionKindCollaboration, Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion,
		CreatedAt: base, UpdatedAt: base,
	})

	brokenPlan := testSidebarProjectPlan("project_broken", "Broken", projectDir, []string{projectDir})
	roomPlan := testSidebarProjectPlan("project_room", "Room", roomDir, []string{roomDir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{brokenPlan, roomPlan}, stamps: map[string]string{"project_broken": "v1", "project_room": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	groups, err := index.listGroups(app, SidebarRooms)
	if err != nil || len(groups) != 1 || groups[0].ID != "project_room" || groups[0].SessionCount != 1 {
		t.Fatalf("room groups=%+v err=%v", groups, err)
	}
	// ROOM scope must not surface the broken non-ROOM project's issue.
	roomIssues, err := index.listIssues(app, SidebarRooms)
	if err != nil || len(roomIssues) != 0 {
		t.Fatalf("room issues=%+v err=%v", roomIssues, err)
	}
	// Projects scope (all groups) does surface it.
	projectIssues, err := index.listIssues(app, SidebarProjects)
	if err != nil || len(projectIssues) != 1 || projectIssues[0].Code != "meta_decode" {
		t.Fatalf("project issues=%+v err=%v", projectIssues, err)
	}
}

func TestSidebarBoltTransientMetaFailureRecovers(t *testing.T) {
	dir := t.TempDir()
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Transient", Turns: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	plan := testSidebarProjectPlan("project_transient", "Transient", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_transient": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	real := index.loadBranchMeta
	calls := 0
	index.loadBranchMeta = func(p string) (agent.BranchMeta, bool, error) {
		calls++
		if calls == 1 {
			return agent.BranchMeta{}, false, &agent.BranchMetaDecodeError{Err: errors.New("transient")}
		}
		return real(p)
	}
	index.branchMetaBackoffs = []time.Duration{0}

	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_transient"})
	if err != nil || page.Total == nil || *page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "session" {
		t.Fatalf("transient page=%+v err=%v", page, err)
	}
	if calls != 2 {
		t.Fatalf("transient load calls=%d, want 2", calls)
	}
	if issues, _ := index.listIssues(app, SidebarProjects); len(issues) != 0 {
		t.Fatalf("transient issues=%+v", issues)
	}
}

func TestSidebarBoltPersistentBrokenSidecarIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Stable", Turns: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	testSidebarWriteBrokenMeta(t, dir, "broken.jsonl")

	plan := testSidebarProjectPlan("project_stable_broken", "Stable Broken", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_stable_broken": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_stable_broken"})
	if err != nil || first.Total == nil || *first.Total != 1 {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	firstIssues, err := index.listIssues(app, SidebarProjects)
	if err != nil || len(firstIssues) != 1 {
		t.Fatalf("first issues=%+v err=%v", firstIssues, err)
	}
	before := testSidebarReadGeneration(t, index, app, "project_stable_broken")
	second, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_stable_broken"})
	if err != nil {
		t.Fatal(err)
	}
	secondIssues, err := index.listIssues(app, SidebarProjects)
	if err != nil || len(secondIssues) != 1 {
		t.Fatalf("second issues=%+v err=%v", secondIssues, err)
	}
	if after := testSidebarReadGeneration(t, index, app, "project_stable_broken"); before == 0 || after != before || second.Snapshot != first.Snapshot {
		t.Fatalf("broken sidecar regenerated: %d/%d snapshot=%q/%q", before, after, first.Snapshot, second.Snapshot)
	}
}

func TestSidebarBoltBrokenSidecarRecoversOnRepair(t *testing.T) {
	dir := t.TempDir()
	brokenPath := testSidebarWriteBrokenMeta(t, dir, "session.jsonl")
	plan := testSidebarProjectPlan("project_repair", "Repair", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_repair": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_repair"})
	if err != nil || page.Total == nil || *page.Total != 0 {
		t.Fatalf("broken page=%+v err=%v", page, err)
	}
	if issues, _ := index.listIssues(app, SidebarProjects); len(issues) != 1 {
		t.Fatalf("broken issues=%+v", issues)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(brokenPath, agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Repaired", Turns: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	index.markDirty(app, agent.BranchMetaPath(brokenPath))
	repaired, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_repair"})
	if err != nil || repaired.Total == nil || *repaired.Total != 1 || len(repaired.Items) != 1 || repaired.Items[0].ID != "session" {
		t.Fatalf("repaired page=%+v err=%v", repaired, err)
	}
	if issues, _ := index.listIssues(app, SidebarProjects); len(issues) != 0 {
		t.Fatalf("repaired issues=%+v", issues)
	}
}

func TestSidebarBoltBrokenSidecarClearsIssueOnDelete(t *testing.T) {
	dir := t.TempDir()
	brokenPath := testSidebarWriteBrokenMeta(t, dir, "session.jsonl")
	plan := testSidebarProjectPlan("project_delete", "Delete", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_delete": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	if _, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_delete"}); err != nil {
		t.Fatal(err)
	}
	if issues, _ := index.listIssues(app, SidebarProjects); len(issues) != 1 {
		t.Fatalf("broken issues=%+v", issues)
	}
	if err := os.Remove(brokenPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(agent.BranchMetaPath(brokenPath)); err != nil {
		t.Fatal(err)
	}
	index.markDirty(app, brokenPath)
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_delete"})
	if err != nil || page.Total == nil || *page.Total != 0 {
		t.Fatalf("deleted page=%+v err=%v", page, err)
	}
	if issues, _ := index.listIssues(app, SidebarProjects); len(issues) != 0 {
		t.Fatalf("deleted issues=%+v", issues)
	}
}

func TestSidebarBoltPermissionErrorFailsWholeScan(t *testing.T) {
	dir := t.TempDir()
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicTitle: "Stable", Turns: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})

	plan := testSidebarProjectPlan("project_perm", "Perm", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_perm": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_perm"})
	if err != nil || first.Total == nil || *first.Total != 1 {
		t.Fatalf("initial page=%+v err=%v", first, err)
	}
	before := testSidebarReadGeneration(t, index, app, "project_perm")

	// A new sidecar appears whose meta read fails with a permission error. That
	// is not a damaged sidecar: the whole scan must fail without publishing a
	// missing-row generation or writing an issue.
	badPath := filepath.Join(dir, "unreadable.jsonl")
	if err := os.WriteFile(badPath, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	real := index.loadBranchMeta
	index.loadBranchMeta = func(p string) (agent.BranchMeta, bool, error) {
		if p == badPath {
			return agent.BranchMeta{}, false, &os.PathError{Op: "read", Path: p, Err: os.ErrPermission}
		}
		return real(p)
	}
	index.markDirty(app, badPath)
	_, err = index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_perm"})
	if err == nil {
		t.Fatal("permission error did not fail the scan")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("scan error does not wrap the permission error: %v", err)
	}
	if after := testSidebarReadGeneration(t, index, app, "project_perm"); after != before {
		t.Fatalf("permission error advanced generation: %d/%d", before, after)
	}
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.db.View(func(tx *bolt.Tx) error {
		if issues := tx.Bucket(sidebarBoltIssues); issues.Stats().KeyN != 0 {
			t.Fatalf("permission error wrote issue bucket entries: %+v", issues.Stats())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSidebarBoltIssueManifestBatchFaultReopenSelfHeals(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	const healthyCount = 260 // > sidebarBoltBatchSize so updates span two batches
	for i := 0; i < healthyCount; i++ {
		testSidebarWriteSessionMeta(t, dir, fmt.Sprintf("session-%03d.jsonl", i), agent.BranchMeta{
			ID: fmt.Sprintf("session-%03d", i), Scope: "project", WorkspaceRoot: dir, TopicTitle: fmt.Sprintf("S%03d", i),
			Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base,
		})
	}
	testSidebarWriteBrokenMeta(t, dir, "broken.jsonl")

	plan := testSidebarProjectPlan("project_batch", "Batch", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_batch": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	// First batch commits (256 updates); the second batch faults, so the pending
	// marker stays set and the second half is not yet persisted.
	batchCalls := 0
	index.scanBatchFault = func() error {
		batchCalls++
		if batchCalls == 2 {
			return errors.New("injected batch failure")
		}
		return nil
	}
	if _, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_batch"}); err == nil || !strings.Contains(err.Error(), "injected batch failure") {
		t.Fatalf("batch fault err=%v", err)
	}
	if err := index.close(app); err != nil {
		t.Fatal(err)
	}
	index.scanBatchFault = nil
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_batch"})
	if err != nil || page.Total == nil || *page.Total != healthyCount || len(page.Items) != 20 {
		t.Fatalf("recovered page total=%v items=%d err=%v", page.Total, len(page.Items), err)
	}
	issues, err := index.listIssues(app, SidebarProjects)
	if err != nil || len(issues) != 1 || issues[0].Code != "meta_decode" {
		t.Fatalf("recovered issues=%+v err=%v", issues, err)
	}
	// The pending marker must be cleared and the generation advanced after recovery.
	state, err := index.open(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.db.View(func(tx *bolt.Tx) error {
		if pending := tx.Bucket(sidebarBoltMeta).Get([]byte("pending:project_batch")); pending != nil {
			t.Fatalf("pending marker retained after recovery: %x", pending)
		}
		if generation := bytesToUint64(tx.Bucket(sidebarBoltMeta).Get([]byte("generation:project_batch"))); generation == 0 {
			t.Fatal("generation did not advance after recovery")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSidebarBoltPaginationDiscoversBrokenItem(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		testSidebarWriteSessionMeta(t, dir, fmt.Sprintf("session-%d.jsonl", i), agent.BranchMeta{
			ID: fmt.Sprintf("session-%d", i), Scope: "project", WorkspaceRoot: dir, TopicTitle: fmt.Sprintf("S%d", i),
			Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base,
		})
	}
	testSidebarWriteBrokenMeta(t, dir, "broken.jsonl")

	plan := testSidebarProjectPlan("project_paged", "Paged", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_paged": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	// The broken item is discovered during a page scan and must not reject the page.
	page, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_paged"})
	if err != nil || page.Total == nil || *page.Total != 5 || len(page.Items) != 5 {
		t.Fatalf("paged page=%+v err=%v", page, err)
	}
	issues, err := index.listIssues(app, SidebarProjects)
	if err != nil || len(issues) != 1 || issues[0].Code != "meta_decode" {
		t.Fatalf("paged issues=%+v err=%v", issues, err)
	}
}

func TestSidebarIssueDTOOmitsPathsAndCause(t *testing.T) {
	dir := t.TempDir()
	testSidebarWriteBrokenMeta(t, dir, "broken.jsonl")
	plan := testSidebarProjectPlan("project_dto", "DTO", dir, []string{dir})
	index := testSidebarBoltIndex(t, &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_dto": "v1"}})
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	if _, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_dto"}); err != nil {
		t.Fatal(err)
	}
	issues, err := index.listIssues(app, SidebarProjects)
	if err != nil || len(issues) != 1 {
		t.Fatalf("issues=%+v err=%v", issues, err)
	}
	encoded, err := json.Marshal(issues)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for _, forbidden := range []string{dir, "broken.jsonl", "not valid json", "sessionPath", "metaPath", "workspace", "cause"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("issue JSON leaks %q: %s", forbidden, raw)
		}
	}
}

func TestSidebarRefreshIssuesTargetsAffectedPlan(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	brokenPath := testSidebarWriteBrokenMeta(t, dirA, "broken.jsonl")
	base := time.Now().UTC().Add(-time.Hour)
	testSidebarWriteSessionMeta(t, dirB, "session.jsonl", agent.BranchMeta{
		ID: "session-b", Scope: "project", WorkspaceRoot: dirB, TopicTitle: "B", Turns: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base,
	})

	planA := testSidebarProjectPlan("project_a", "A", dirA, []string{dirA})
	planB := testSidebarProjectPlan("project_b", "B", dirB, []string{dirB})
	scanned := map[string]int{}
	source := &sidebarTestSource{
		plansValue: []sidebarGroupPlan{planA, planB},
		stampFunc: func(plan sidebarGroupPlan) string {
			scanned[plan.group.ID]++
			return "v1"
		},
	}
	index := testSidebarBoltIndex(t, source)
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })

	// Produce the issue by scanning A once.
	if _, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_a"}); err != nil {
		t.Fatal(err)
	}
	if issues, _ := index.listIssues(app, SidebarProjects); len(issues) != 1 {
		t.Fatalf("initial issues=%+v", issues)
	}

	// Repair the sidecar (as the watcher would) without expanding anything.
	if err := agent.SaveBranchMetaPreserveUpdated(brokenPath, agent.BranchMeta{
		ID: "broken", Scope: "project", WorkspaceRoot: dirA, TopicTitle: "Fixed", Turns: 1,
		SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	index.markDirty(app, agent.BranchMetaPath(brokenPath))
	scanned = map[string]int{}
	issues, err := index.refreshIssues(app, SidebarProjects)
	if err != nil || len(issues) != 0 {
		t.Fatalf("refreshed issues=%+v err=%v", issues, err)
	}
	if scanned["project_a"] == 0 {
		t.Fatal("RefreshSidebarIssues did not re-scan the affected plan")
	}
	if scanned["project_b"] != 0 {
		t.Fatalf("RefreshSidebarIssues scanned an unaffected plan: %+v", scanned)
	}
}

func TestRefreshSidebarIssuesInvalidMode(t *testing.T) {
	app := &App{}
	if _, err := app.RefreshSidebarIssues(SidebarMode("bogus")); !errors.Is(err, errInvalidSidebarMode) {
		t.Fatalf("invalid mode err=%v", err)
	}
}
