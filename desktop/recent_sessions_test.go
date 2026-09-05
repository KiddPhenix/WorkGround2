package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestRecentSessionItemsReuseWidgetTaskProjection(t *testing.T) {
	pathA := filepath.Join("D:", "work", "sessions", "a.jsonl")
	pathB := filepath.Join("D:", "work", "sessions", "b.jsonl")
	tasks := []DesktopIconItem{
		{ID: "room:1", Kind: "room", Title: "Room"},
		{
			ID: "task:a", Kind: "task", SourceID: "tab-a", SessionID: "session-a", AppearanceSeed: "seed-a",
			Title: "A", Status: "running", UnreadCount: 2, WorkspaceIcon: "go", Retained: false,
			Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 4}, Revision: "rev-a",
			SessionRef: desktopIconTaskRef("project", filepath.Join("D:", "work"), "topic-a", pathA),
		},
		{ID: "fixed:new", Kind: "fixed", Title: "新建"},
		{
			ID: "task:b", Kind: "task", SourceID: "old-tab", SessionID: "session-b", AppearanceSeed: "seed-b",
			Title: "B", Status: "done", WorkspaceIcon: "python", Retained: true,
			Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 5}, Revision: "rev-b",
			SessionRef: desktopIconTaskRef("project", filepath.Join("D:", "work"), "topic-b", pathB),
		},
	}

	got := recentSessionItemsFromDesktopIcons(tasks, 50)
	if len(got) != 2 {
		t.Fatalf("items = %d, want exactly the two widget task items", len(got))
	}
	if !reflect.DeepEqual(got[0].Item, tasks[1]) || !reflect.DeepEqual(got[1].Item, tasks[3]) {
		t.Fatalf("rail changed widget items: got=%+v", got)
	}
	if got[0].Session.SessionPath != pathA || got[0].Session.TopicID != "topic-a" || !got[0].Session.Running {
		t.Fatalf("live open adapter = %+v", got[0].Session)
	}
	if got[0].Session.Status != topicStatusThinking {
		t.Fatalf("live sidebar status = %q, want %q", got[0].Session.Status, topicStatusThinking)
	}
	if got[1].Session.SessionPath != pathB || got[1].Session.ID != "old-tab" || !got[1].Item.Retained {
		t.Fatalf("retained open adapter = %+v item=%+v", got[1].Session, got[1].Item)
	}
}

func TestRecentSessionsColdStartUsesWidgetStateWithoutProjectTree(t *testing.T) {
	state := newDesktopIconState()
	state.Kept = map[string]desktopIconKept{
		"task:kept-a": {ItemID: "task:kept-a", SourceID: "tab-a", SessionID: "session-a", Title: "JOKE", Order: 1, Scope: "global", SessionPath: filepath.Join("D:", "sessions", "a.jsonl")},
		"task:kept-b": {ItemID: "task:kept-b", SourceID: "tab-b", SessionID: "session-b", Title: "算命笑话", Order: 2, Scope: "global", SessionPath: filepath.Join("D:", "sessions", "b.jsonl")},
	}
	app := &App{
		iconWidgetStateLoaded: true,
		iconWidgetState:       state,
		desktopIconProjectTree: func() []ProjectNode {
			t.Fatal("cold recent task projection read the project/Session tree")
			return nil
		},
	}

	page, err := app.RecentSessions(RecentSessionsRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Item.Title != "JOKE" || page.Items[1].Item.Title != "算命笑话" {
		t.Fatalf("cold recent items = %+v", page.Items)
	}
}

func TestRecentSessionsReuseLastFullWidgetSnapshot(t *testing.T) {
	want := DesktopIconItem{ID: "task:full", Kind: "task", Title: "完整快照", AppearanceSeed: "seed", Revision: "revision"}
	app := &App{
		iconWidgetStateLoaded:   true,
		iconWidgetState:         newDesktopIconState(),
		iconWidgetSnapshotReady: true,
		iconWidgetLastSnapshot: DesktopIconSnapshot{Items: []DesktopIconItem{
			{ID: "fixed:new", Kind: "fixed", Title: "新建"},
			want,
		}},
		desktopIconProjectTree: func() []ProjectNode {
			t.Fatal("cached recent task projection read the project/Session tree")
			return nil
		},
	}

	page, err := app.RecentSessions(RecentSessionsRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].Item, want) {
		t.Fatalf("cached recent item = %+v, want %+v", page.Items, want)
	}
}

func TestRecentSessionSidebarStatusAdaptsWidgetStates(t *testing.T) {
	cases := map[string]string{
		"running":       topicStatusThinking,
		"thinking":      topicStatusThinking,
		"needs_input":   topicStatusWaitingConfirmation,
		"needs_confirm": topicStatusWaitingConfirmation,
		"failed":        topicStatusError,
		"done":          "",
	}
	for input, want := range cases {
		if got := recentSessionSidebarStatus(input); got != want {
			t.Errorf("status %q = %q, want %q", input, got, want)
		}
	}
}

func TestRecentSessionItemsPreserveWidgetOrderAndLimit(t *testing.T) {
	items := []DesktopIconItem{
		{ID: "task:first", Kind: "task", Title: "first"},
		{ID: "workspace:a", Kind: "workspace", Title: "workspace"},
		{ID: "task:second", Kind: "task", Title: "second"},
		{ID: "task:third", Kind: "task", Title: "third"},
	}
	got := recentSessionItemsFromDesktopIcons(items, 2)
	if len(got) != 2 || got[0].Item.ID != "task:first" || got[1].Item.ID != "task:second" {
		t.Fatalf("order/limit = %+v", got)
	}
}

func TestRecentSessionItemBlankTitleAndMissingRefStayRenderable(t *testing.T) {
	item := DesktopIconItem{ID: "task:pending", Kind: "task", SourceID: "tab-pending", Title: " ", Status: "thinking"}
	got := recentSessionItemFromDesktopIcon(item)
	if got.Item.Title != item.Title || got.Session.Title != "新的会话" {
		t.Fatalf("blank title = item %q session %q", got.Item.Title, got.Session.Title)
	}
	if got.Session.Scope != "global" || got.Session.ID != "tab-pending" || !got.Session.Running {
		t.Fatalf("missing-ref adapter = %+v", got.Session)
	}
}
