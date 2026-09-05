package main

import "strings"

// RecentSessionItem is the sidebar rail projection of one widget task item.
// Item is copied unchanged from DesktopIconSnapshot so both surfaces share the
// exact title, identity seed, status, badge, order and durable SessionRef.
// Session only adapts that same item for the sidebar's existing open route.
type RecentSessionItem struct {
	Item      DesktopIconItem `json:"item"`
	Session   SidebarSession  `json:"session"`
	GroupKind string          `json:"groupKind,omitempty"`
}

type RecentSessionsRequest struct {
	Limit     int    `json:"limit,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

type RecentSessionsPage struct {
	Items []RecentSessionItem `json:"items"`
}

const recentSessionRailLimit = 50

// RecentSessions deliberately consumes the widget's authoritative task
// projection. Once the full widget snapshot exists it is reused byte-for-byte.
// On a cold start, the same widget builder projects its persisted/live task
// sources without waiting for the unrelated project-tree enrichment pass.
func (a *App) RecentSessions(request RecentSessionsRequest) (RecentSessionsPage, error) {
	limit := request.Limit
	if limit <= 0 || limit > recentSessionRailLimit {
		limit = recentSessionRailLimit
	}
	items, err := a.recentDesktopIconItems()
	if err != nil {
		return RecentSessionsPage{}, err
	}
	return RecentSessionsPage{Items: recentSessionItemsFromDesktopIcons(items, limit)}, nil
}

// recentDesktopIconItems avoids the full snapshot's bounded five-second
// project-tree read on the window startup path. A completed widget projection
// is the strongest source and is returned unchanged. Before the first full
// projection, buildDesktopIconSnapshot provides the exact same task rules over
// the widget's live sources, unread state and durable kept state. Placeholder
// workspace slots preserve the widget task cap without reading Session List.
func (a *App) recentDesktopIconItems() ([]DesktopIconItem, error) {
	a.iconWidgetMu.Lock()
	a.loadDesktopIconStateLocked()
	if a.iconWidgetStateErr != nil {
		err := a.iconWidgetStateErr
		a.iconWidgetMu.Unlock()
		return nil, err
	}
	if a.iconWidgetSnapshotReady {
		items := append([]DesktopIconItem(nil), a.iconWidgetLastSnapshot.Items...)
		a.iconWidgetMu.Unlock()
		return items, nil
	}
	state := cloneDesktopIconState(a.iconWidgetState)
	a.iconWidgetMu.Unlock()

	spaceCount := min(max(state.WorkspaceSlots, 0), desktopIconMaxSpaces)
	spaces := make([]WidgetWorkspaceOption, spaceCount)
	for i := range spaces {
		spaces[i].Scope = "project"
	}
	snapshot := buildDesktopIconSnapshot(a.widgetSources(), a.UnreadState(), spaces, state, 0, nil, nil, nil, nil)
	return snapshot.Items, nil
}

func recentSessionItemsFromDesktopIcons(items []DesktopIconItem, limit int) []RecentSessionItem {
	if limit <= 0 || limit > recentSessionRailLimit {
		limit = recentSessionRailLimit
	}
	out := make([]RecentSessionItem, 0, min(limit, len(items)))
	for _, item := range items {
		if item.Kind != "task" {
			continue
		}
		out = append(out, recentSessionItemFromDesktopIcon(item))
		if len(out) == limit {
			break
		}
	}
	return out
}

func recentSessionItemFromDesktopIcon(item DesktopIconItem) RecentSessionItem {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = "新的会话"
	}

	ref := item.SessionRef
	scope, root, topicID, sessionPath := "global", "", "", ""
	if ref != nil {
		scope = strings.TrimSpace(ref.Scope)
		if scope == "project" {
			root = normalizeProjectRoot(ref.WorkspaceRoot)
		} else {
			scope = "global"
		}
		topicID = strings.TrimSpace(ref.TopicID)
		sessionPath = strings.TrimSpace(ref.SessionPath)
	}

	session := SidebarSession{
		ID:            firstNonEmpty(strings.TrimSpace(item.SourceID), strings.TrimSpace(item.SessionID), strings.TrimSpace(item.ID)),
		SessionID:     strings.TrimSpace(item.SessionID),
		Scope:         scope,
		WorkspaceRoot: root,
		Title:         title,
		SessionPath:   sessionPath,
		TopicID:       topicID,
		SessionKind:   "normal",
		Status:        recentSessionSidebarStatus(item.Status),
		Running:       item.Status == "running" || item.Status == "thinking",
	}
	return RecentSessionItem{Item: item, Session: session}
}

func recentSessionSidebarStatus(status string) string {
	switch status {
	case "running", "thinking":
		return topicStatusThinking
	case "needs_input", "needs_confirm":
		return topicStatusWaitingConfirmation
	case "failed":
		return topicStatusError
	default:
		return ""
	}
}
