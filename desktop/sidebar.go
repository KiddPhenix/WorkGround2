package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SidebarMode selects the server-side session classification used by the
// desktop sidebar.
type SidebarMode string

const (
	SidebarProjects   SidebarMode = "projects"
	SidebarRooms      SidebarMode = "rooms"
	SidebarAssistants SidebarMode = "assistants"

	defaultSidebarPageSize = 20
	minSidebarPageSize     = 10
	maxSidebarPageSize     = 50
)

// SidebarGroup is a lightweight project summary. It deliberately carries no
// child sessions so the initial sidebar response stays bounded.
type SidebarGroup struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	Root           string `json:"root,omitempty"`
	Color          string `json:"color,omitempty"`
	Icon           string `json:"icon,omitempty"`
	Pinned         bool   `json:"pinned,omitempty"`
	SessionCount   int    `json:"sessionCount"`
	LastActivityAt int64  `json:"lastActivityAt,omitempty"`
}

// SidebarSession is the stable, flat row model shared by project, Room,
// Assistant and search views.
type SidebarSession struct {
	ID             string `json:"id"`
	SessionID      string `json:"sessionId,omitempty"`
	GroupID        string `json:"groupId"`
	Scope          string `json:"scope"`
	WorkspaceRoot  string `json:"workspaceRoot,omitempty"`
	Title          string `json:"title"`
	SessionPath    string `json:"sessionPath,omitempty"`
	TopicID        string `json:"topicId,omitempty"`
	TitleSource    string `json:"titleSource,omitempty"`
	SessionSource  string `json:"sessionSource,omitempty"`
	Channel        string `json:"channel,omitempty"`
	ChannelLabel   string `json:"channelLabel,omitempty"`
	SessionKind    string `json:"sessionKind,omitempty"`
	Status         string `json:"status,omitempty"`
	Turns          int    `json:"turns,omitempty"`
	Open           bool   `json:"open,omitempty"`
	Running        bool   `json:"running,omitempty"`
	TurnStartedAt  int64  `json:"turnStartedAt,omitempty"`
	Pinned         bool   `json:"pinned,omitempty"`
	CreatedAt      int64  `json:"createdAt,omitempty"`
	LastActivityAt int64  `json:"lastActivityAt,omitempty"`
	Revision       int64  `json:"revision"`
}

type SidebarSessionQuery struct {
	Mode      SidebarMode `json:"mode"`
	GroupID   string      `json:"groupId,omitempty"`
	Cursor    string      `json:"cursor,omitempty"`
	Limit     int         `json:"limit,omitempty"`
	RequestID string      `json:"requestId,omitempty"`
}

type SidebarSessionPage struct {
	Items      []SidebarSession `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
	Total      *int             `json:"total,omitempty"`
	Snapshot   string           `json:"snapshot"`
}

type SidebarSearchRequest struct {
	Query     string `json:"query"`
	Filter    string `json:"filter"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// SidebarSearchItem is an explicit union: Kind selects exactly one of Group or
// Session. Keeping the group on session rows lets the UI render group headers
// without another full-tree request.
type SidebarSearchItem struct {
	Kind           string          `json:"kind"`
	ID             string          `json:"id"`
	Group          *SidebarGroup   `json:"group,omitempty"`
	Session        *SidebarSession `json:"session,omitempty"`
	LastActivityAt int64           `json:"lastActivityAt,omitempty"`
}

type SidebarSearchPage struct {
	Items      []SidebarSearchItem `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
	Total      *int                `json:"total,omitempty"`
	Snapshot   string              `json:"snapshot"`
}

var (
	errInvalidSidebarMode   = errors.New("invalid sidebar mode")
	errInvalidSidebarFilter = errors.New("invalid sidebar filter")
	errInvalidSidebarCursor = errors.New("invalid or expired sidebar cursor")
)

// ListSidebarGroups returns project summaries only. Session rows are fetched
// lazily through ListSidebarSessions.
func (a *App) ListSidebarGroups(mode SidebarMode) ([]SidebarGroup, error) {
	if !validSidebarMode(mode) {
		return nil, fmt.Errorf("%w: %q", errInvalidSidebarMode, mode)
	}
	return desktopSidebarBolt.listGroups(a, mode)
}

// ListSidebarSessions returns one immutable keyset page. A cursor pins the
// request to the snapshot it came from, so retries and concurrent activity do
// not create duplicates or skip rows.
func (a *App) ListSidebarSessions(query SidebarSessionQuery) (SidebarSessionPage, error) {
	if !validSidebarMode(query.Mode) {
		return SidebarSessionPage{}, fmt.Errorf("%w: %q", errInvalidSidebarMode, query.Mode)
	}
	return desktopSidebarBolt.listSessions(a, query)
}

// SearchSidebar searches project names/roots and session titles/display paths
// from the same immutable sidebar snapshot used for pagination.
func (a *App) SearchSidebar(request SidebarSearchRequest) (SidebarSearchPage, error) {
	return desktopSidebarBolt.search(a, request)
}

func validSidebarMode(mode SidebarMode) bool {
	switch mode {
	case SidebarProjects, SidebarRooms, SidebarAssistants:
		return true
	default:
		return false
	}
}

func normalizeSidebarFilter(filter string) string {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return "all"
	}
	switch filter {
	case "all", "projects", "sessions":
		return filter
	default:
		return ""
	}
}

func normalizeSidebarLimit(limit int) int {
	if limit <= 0 {
		return defaultSidebarPageSize
	}
	if limit < minSidebarPageSize {
		return minSidebarPageSize
	}
	if limit > maxSidebarPageSize {
		return maxSidebarPageSize
	}
	return limit
}

func sidebarExplicitSessionKind(kind, source string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	source = strings.ToLower(strings.TrimSpace(source))
	switch {
	case kind == "assistant" || source == "assist":
		return "assistant"
	case kind == "collaboration" || source == "collaboration":
		return "collaboration"
	case kind != "":
		return kind
	default:
		return ""
	}
}

func sidebarContains(needle string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

type sidebarCursor struct {
	Version        uint64 `json:"v"`
	Kind           string `json:"k"`
	Signature      string `json:"q"`
	LastActivityAt int64  `json:"a"`
	LastID         string `json:"i"`
	LastKey        string `json:"s,omitempty"`
}

func encodeSidebarCursor(cursor sidebarCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeSidebarCursor(encoded string) (sidebarCursor, error) {
	var cursor sidebarCursor
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return cursor, errInvalidSidebarCursor
	}
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Kind == "" || cursor.Signature == "" || cursor.LastID == "" {
		return sidebarCursor{}, errInvalidSidebarCursor
	}
	return cursor, nil
}

func sidebarQuerySignature(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func sidebarSnapshotToken(app *App, version uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%p:%d", app, version)))
	return strconv.FormatUint(version, 36) + "-" + hex.EncodeToString(sum[:6])
}
