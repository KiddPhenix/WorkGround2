package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"workground2/internal/config"
)

// sidebarGroupPlan is small metadata needed to locate and label one sidebar
// group. Building plans never enumerates session files.
type sidebarGroupPlan struct {
	group       SidebarGroup
	scope       string
	root        string
	dirs        []string
	topicIDs    []string
	titles      map[string]string
	titleSource map[string]string
	createdAt   map[string]int64
	pinned      map[string]bool
	crew        bool
}

const (
	maxSidebarLoadConcurrency = 4
	maxSidebarQueryCache      = 16
	maxSidebarQueryRevisions  = 8
)

type sidebarDiskIndexSource struct {
	crewStamp func() string
}

func (sidebarDiskIndexSource) plans(app *App) ([]sidebarGroupPlan, error) {
	if app == nil {
		return nil, fmt.Errorf("sidebar app is nil")
	}
	f := loadProjectsFile()
	plansByID := map[string]sidebarGroupPlan{}
	nodes := []ProjectNode{}

	globalTitles := loadTopicTitles("")
	globalIDs := orderedTopicIDs(f.GlobalTopics, globalTitles)
	globalDirs := uniqueSidebarDirs(config.SessionDir(), desktopSessionDir(globalWorkspaceRoot()))
	if len(app.sessionDirsOverride) > 0 {
		globalDirs = uniqueSidebarDirs(app.sessionDirsOverride...)
	}
	global := sidebarGroupPlan{
		group: SidebarGroup{ID: "global_folder", Kind: "global", Label: firstNonEmpty(strings.TrimSpace(f.GlobalTitle), "Global"), Root: globalWorkspaceRoot(), Color: f.GlobalColor, Icon: f.GlobalIcon, SessionCount: len(globalIDs)},
		scope: "global", topicIDs: globalIDs, titles: globalTitles, titleSource: loadTopicTitleSources(""), createdAt: loadTopicCreatedAts(""), pinned: stringSet(f.GlobalPinnedTopics),
		dirs: globalDirs,
	}
	global.group.LastActivityAt = latestCreatedAt(global.createdAt)
	plansByID[global.group.ID] = global
	nodes = append(nodes, sidebarPlanNode(global))

	if routes := autoBotChannelSessionRoutes(); len(routes) > 0 {
		crew := sidebarGroupPlan{
			group: SidebarGroup{ID: "crew_folder", Kind: "crew", Label: "Crew", SessionCount: len(routes)},
			scope: "global", crew: true, dirs: append([]string{}, app.knownSessionDirs()...),
		}
		plansByID[crew.group.ID] = crew
		nodes = append(nodes, sidebarPlanNode(crew))
	}

	for _, project := range f.Projects {
		root := normalizeProjectRoot(project.Root)
		titles := loadTopicTitles(root)
		ids := orderedTopicIDs(project.Topics, titles)
		projectDirs := uniqueSidebarDirs(desktopSessionDir(root))
		if len(app.sessionDirsOverride) > 0 {
			projectDirs = uniqueSidebarDirs(app.sessionDirsOverride...)
		}
		plan := sidebarGroupPlan{
			group: SidebarGroup{ID: "project_" + project.Root, Kind: "project", Label: firstNonEmpty(strings.TrimSpace(project.Title), workspaceName(root)), Root: root, Color: project.Color, Icon: project.Icon, Pinned: containsDesktopString(f.PinnedProjects, project.Root), SessionCount: len(ids)},
			scope: "project", root: root, dirs: projectDirs, topicIDs: ids, titles: titles,
			titleSource: loadTopicTitleSources(root), createdAt: loadTopicCreatedAts(root), pinned: stringSet(project.PinnedTopics),
		}
		plan.group.LastActivityAt = latestCreatedAt(plan.createdAt)
		plansByID[plan.group.ID] = plan
		nodes = append(nodes, sidebarPlanNode(plan))
	}

	ordered := applyPinnedProjectOrder(applyProjectTreeOrder(nodes, f.SidebarOrder), f.PinnedProjects)
	plans := make([]sidebarGroupPlan, 0, len(ordered))
	for _, node := range ordered {
		if plan, ok := plansByID[node.Key]; ok {
			plans = append(plans, plan)
		}
	}
	return plans, nil
}

func sidebarPlanNode(plan sidebarGroupPlan) ProjectNode {
	kind := plan.group.Kind
	if kind == "global" || kind == "crew" {
		kind += "_folder"
	}
	return ProjectNode{Key: plan.group.ID, Kind: kind, Label: plan.group.Label, Root: plan.group.Root, ProjectColor: plan.group.Color, ProjectIcon: plan.group.Icon, Pinned: plan.group.Pinned}
}

func (source sidebarDiskIndexSource) stamp(app *App, plan sidebarGroupPlan) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%v\x00", plan.group.ID, plan.scope, plan.root, plan.group.Label, plan.group.Color, plan.group.Icon, plan.group.Pinned, plan.topicIDs)
	keys := make([]string, 0, len(plan.titles))
	for key := range plan.titles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(hash, "%s=%s\x00", key, plan.titles[key])
	}
	pinned := make([]string, 0, len(plan.pinned))
	for key, value := range plan.pinned {
		if value {
			pinned = append(pinned, key)
		}
	}
	sort.Strings(pinned)
	fmt.Fprintf(hash, "pinned:%v\x00", pinned)
	routeStamp := sidebarCrewRouteStamp()
	if source.crewStamp != nil {
		routeStamp = source.crewStamp()
	}
	// A configured auto-bot route moves a physical project session into Crew.
	// Therefore every project projection, not only the Crew group, depends on
	// the stable route summary.
	fmt.Fprintf(hash, "routes:%s\x00", routeStamp)
	for _, path := range sidebarPlanStampPaths(plan) {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(hash, "%s:missing\x00", path)
			continue
		}
		fmt.Fprintf(hash, "%s:%d:%d\x00", path, info.Size(), info.ModTime().UnixNano())
	}
	for _, runtime := range sidebarRuntimeRows(app, plan) {
		fmt.Fprintf(hash, "%s:%s:%s:%t:%t:%s:%d\x00", runtime.ID, runtime.SessionPath, runtime.Status, runtime.Open, runtime.Running, runtime.Title, runtime.TurnStartedAt)
	}
	return hex.EncodeToString(hash.Sum(nil)[:12])
}

func sidebarPlanStampPaths(plan sidebarGroupPlan) []string {
	paths := append([]string{}, plan.dirs...)
	for _, dir := range plan.dirs {
		paths = append(paths, sessionTitlesPath(dir))
	}
	if plan.crew {
		if path := strings.TrimSpace(config.UserConfigPath()); path != "" {
			paths = append(paths, path)
		}
		return paths
	}
	root := plan.root
	if plan.scope == "global" {
		root = ""
	}
	return append(paths, topicTitlesPath(root), topicTitleSourcesPath(root), topicCreatedAtsPath(root))
}

func sidebarCrewRouteStamp() string {
	routes := autoBotChannelSessionRoutes()
	keys := make([]string, 0, len(routes))
	for key := range routes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		route := routes[key]
		fmt.Fprintf(hash, "%s:%s:%s:%s:%s:%s:%s:%s\x00", key, route.channel, route.channelLabel, route.remoteID, route.chatType, route.userID, route.threadID, route.sessionSource)
	}
	return hex.EncodeToString(hash.Sum(nil)[:12])
}

type sidebarRuntimeRow struct {
	ID            string
	SessionPath   string
	TopicID       string
	Title         string
	SessionKind   string
	SessionSource string
	Status        string
	Open          bool
	Running       bool
	TurnStartedAt int64
}

func sidebarRuntimeRows(app *App, plan sidebarGroupPlan) []sidebarRuntimeRow {
	if app == nil || plan.crew {
		return nil
	}
	app.mu.RLock()
	defer app.mu.RUnlock()
	rows := []sidebarRuntimeRow{}
	appendTab := func(tab *WorkspaceTab, open bool) {
		if tab == nil || !sidebarTabMatchesPlan(tab, plan) {
			return
		}
		status := activityStatusForTab(tab)
		running := false
		if tab.Ctrl != nil {
			runtimeStatus := tab.Ctrl.RuntimeStatus()
			running = runtimeStatus.RunningWork
			if status == "" && runtimeStatus.PendingPrompt {
				status = topicStatusWaitingConfirmation
			}
		}
		rows = append(rows, sidebarRuntimeRow{
			ID: tab.SessionID, SessionPath: tab.currentSessionPath(), TopicID: tab.TopicID, Title: tab.TopicTitle,
			SessionKind: string(tab.sessionKind), SessionSource: tabRuntimeSource(tab), Status: status,
			Open: open, Running: running, TurnStartedAt: tab.activeTurnStartedAt(),
		})
	}
	for _, tab := range app.tabs {
		appendTab(tab, true)
	}
	for _, tab := range app.detachedSessions {
		appendTab(tab, false)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// sidebarRuntimeByPath indexes live runtimes by their physical session path.
// The path (canonicalized through sessionRuntimeKey) is the only identity that
// may decorate a history row: a Topic can host several sessions, so a
// TopicID-keyed map would smear one runtime's ID/path across sibling rows.
func sidebarRuntimeByPath(app *App, plan sidebarGroupPlan) map[string]sidebarRuntimeRow {
	return sidebarRuntimeIndex(sidebarRuntimeRows(app, plan))
}

// sidebarRuntimeIndex folds runtime rows into one projection per canonical
// path. It is pure so the deterministic priority (running > open > closed) is
// covered without a live App. Empty paths are dropped and never decorate a row.
func sidebarRuntimeIndex(rows []sidebarRuntimeRow) map[string]sidebarRuntimeRow {
	out := map[string]sidebarRuntimeRow{}
	for _, runtime := range rows {
		key := sessionRuntimeKey(runtime.SessionPath)
		if key == "" {
			continue
		}
		if prior, ok := out[key]; !ok || sidebarRuntimePriority(runtime) > sidebarRuntimePriority(prior) {
			out[key] = runtime
		}
	}
	return out
}

func sidebarRuntimePriority(runtime sidebarRuntimeRow) int {
	switch {
	case runtime.Running:
		return 2
	case runtime.Open:
		return 1
	default:
		return 0
	}
}

func sidebarTabMatchesPlan(tab *WorkspaceTab, plan sidebarGroupPlan) bool {
	scope := strings.TrimSpace(tab.Scope)
	if scope == "" {
		scope = "global"
	}
	if plan.scope == "global" {
		return scope == "global"
	}
	return scope == "project" && normalizeProjectRoot(tab.WorkspaceRoot) == plan.root
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func latestCreatedAt(values map[string]int64) int64 {
	latest := int64(0)
	for _, value := range values {
		if value > latest {
			latest = value
		}
	}
	return latest
}

func uniqueSidebarDirs(values ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if absolute, err := filepath.Abs(value); err == nil {
			value = absolute
		}
		key := strings.ToLower(filepath.Clean(value))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}
