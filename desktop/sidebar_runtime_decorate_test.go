package main

import "testing"

func TestSidebarRuntimeIndexKeysByPhysicalPath(t *testing.T) {
	pathA := "D:/work/a.jsonl"
	pathB := "D:/work/b.jsonl"
	rows := []sidebarRuntimeRow{
		{ID: "run-a", SessionPath: pathA, TopicID: "topic-1", Status: "working", Open: true},
		{ID: "run-b", SessionPath: pathB, TopicID: "topic-1", Status: "idle", Open: true},
	}
	index := sidebarRuntimeIndex(rows)
	if len(index) != 2 {
		t.Fatalf("runtime index size = %d, want 2", len(index))
	}
	if got := index[sessionRuntimeKey(pathA)].ID; got != "run-a" {
		t.Fatalf("pathA runtime ID = %q, want run-a", got)
	}
	if got := index[sessionRuntimeKey(pathB)].ID; got != "run-b" {
		t.Fatalf("pathB runtime ID = %q, want run-b", got)
	}
}

func TestSidebarRuntimeIndexRunningBeatsOpenAndClosed(t *testing.T) {
	path := "D:/work/a.jsonl"
	rows := []sidebarRuntimeRow{
		{ID: "closed", SessionPath: path, Open: false},
		{ID: "open", SessionPath: path, Open: true},
		{ID: "running", SessionPath: path, Open: true, Running: true},
	}
	if got := sidebarRuntimeIndex(rows)[sessionRuntimeKey(path)].ID; got != "running" {
		t.Fatalf("selected runtime ID = %q, want running", got)
	}

	// Priority must not depend on iteration order: a later open projection must
	// not displace an already-selected running projection.
	reordered := []sidebarRuntimeRow{
		{ID: "running", SessionPath: path, Open: false, Running: true},
		{ID: "open", SessionPath: path, Open: true},
	}
	if got := sidebarRuntimeIndex(reordered)[sessionRuntimeKey(path)].ID; got != "running" {
		t.Fatalf("reordered selection = %q, want running", got)
	}
}

func TestSidebarRuntimeIndexDropsEmptyPath(t *testing.T) {
	index := sidebarRuntimeIndex([]sidebarRuntimeRow{
		{ID: "ghost", SessionPath: "", TopicID: "topic-1", Open: true},
	})
	if len(index) != 0 {
		t.Fatalf("empty-path runtime leaked into index: %+v", index)
	}
}

func TestDecorateSidebarBoltRowMatchesOnlyPhysicalPath(t *testing.T) {
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_r", Kind: "project", Root: "D:/r"}, scope: "project", root: "D:/r"}
	pathA := "D:/r/sessions/a.jsonl"
	pathB := "D:/r/sessions/b.jsonl"
	runtimes := sidebarRuntimeIndex([]sidebarRuntimeRow{
		{ID: "run-a", SessionPath: pathA, Status: "working", Open: true, TurnStartedAt: 42, SessionKind: "normal", SessionSource: "auto"},
	})

	rowA := SidebarSession{ID: "hist-a", SessionID: "hist-a", TopicID: "topic-1", SessionPath: pathA, Title: "A"}
	rowB := SidebarSession{ID: "hist-b", SessionID: "hist-b", TopicID: "topic-1", SessionPath: pathB, Title: "B"}
	decorateSidebarBoltRow(&rowA, plan, nil, runtimes)
	decorateSidebarBoltRow(&rowB, plan, nil, runtimes)

	if rowA.ID != "run-a" || rowA.SessionID != "run-a" {
		t.Fatalf("matching row identity = %q/%q, want run-a", rowA.ID, rowA.SessionID)
	}
	if rowA.Status != "working" || !rowA.Open || rowA.TurnStartedAt != 42 {
		t.Fatalf("matching row runtime fields not decorated: %+v", rowA)
	}
	if rowB.ID != "hist-b" || rowB.SessionID != "hist-b" || rowB.SessionPath != pathB {
		t.Fatalf("sibling row identity/path overwritten: %+v", rowB)
	}
	if rowB.Status != "" || rowB.Open || rowB.TurnStartedAt != 0 {
		t.Fatalf("sibling row received runtime fields: %+v", rowB)
	}
}

func TestDecorateSidebarBoltRowMatchesCaseAndSeparatorVariants(t *testing.T) {
	runtimePath := "D:/r/sessions/Case.jsonl"
	rowPath := "d:\\r\\sessions\\case.jsonl"
	if sessionRuntimeKey(runtimePath) != sessionRuntimeKey(rowPath) {
		t.Fatalf("sessionRuntimeKey must fold case/separators: %q != %q", sessionRuntimeKey(runtimePath), sessionRuntimeKey(rowPath))
	}
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_c", Kind: "project", Root: "D:/r"}, scope: "project", root: "D:/r"}
	runtimes := sidebarRuntimeIndex([]sidebarRuntimeRow{
		{ID: "run-case", SessionPath: runtimePath, Status: "working", Open: true},
	})
	row := SidebarSession{ID: "hist-case", SessionID: "hist-case", TopicID: "topic-case", SessionPath: rowPath, Title: "Case"}
	decorateSidebarBoltRow(&row, plan, nil, runtimes)
	if row.ID != "run-case" {
		t.Fatalf("case/separator variant did not match runtime: %+v", row)
	}
}

func TestDecorateSidebarBoltRowEmptyRuntimeDoesNotPollute(t *testing.T) {
	plan := sidebarGroupPlan{group: SidebarGroup{ID: "project_e", Kind: "project", Root: "D:/r"}, scope: "project", root: "D:/r"}
	runtimes := sidebarRuntimeIndex([]sidebarRuntimeRow{
		{ID: "ghost", SessionPath: "", TopicID: "topic-1", Open: true},
	})
	row := SidebarSession{ID: "hist", SessionID: "hist", TopicID: "topic-1", SessionPath: "D:/r/sessions/real.jsonl", Title: "Real"}
	decorateSidebarBoltRow(&row, plan, nil, runtimes)
	if row.ID != "hist" || row.SessionID != "hist" || row.Open {
		t.Fatalf("empty-path runtime polluted an unrelated row: %+v", row)
	}
}
