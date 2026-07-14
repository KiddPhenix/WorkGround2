package main

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const welcomeScanFileLimit = 500

// WorkspaceWelcomeView is a bounded, content-free snapshot used to build the
// new-session empty state. It exposes categories and counts, never file names or
// file contents.
type WorkspaceWelcomeView struct {
	WorkspaceName  string   `json:"workspaceName"`
	Scope          string   `json:"scope"`
	ContentKinds   []string `json:"contentKinds"`
	Confidence     float64  `json:"confidence"`
	FileCount      int      `json:"fileCount"`
	ChangedCount   int      `json:"changedCount"`
	SessionCount   int      `json:"sessionCount"`
	RecentTitle    string   `json:"recentTitle,omitempty"`
	RecentActivity int64    `json:"recentActivity,omitempty"`
	ScannedAt      int64    `json:"scannedAt"`
	Partial        bool     `json:"partial,omitempty"`
	Degraded       bool     `json:"degraded,omitempty"`
	DegradedReason string   `json:"degradedReason,omitempty"`
}

type welcomeProfile struct {
	kinds      []string
	confidence float64
	files      int
	partial    bool
}

var welcomeCodeExt = map[string]bool{
	".c": true, ".cc": true, ".cpp": true, ".cs": true, ".css": true, ".go": true,
	".h": true, ".hpp": true, ".html": true, ".java": true, ".js": true, ".jsx": true,
	".kt": true, ".lua": true, ".php": true, ".py": true, ".rb": true, ".rs": true,
	".scss": true, ".sh": true, ".swift": true, ".ts": true, ".tsx": true, ".vue": true,
}

var welcomeDocExt = map[string]bool{
	".doc": true, ".docx": true, ".epub": true, ".md": true, ".odt": true,
	".pdf": true, ".ppt": true, ".pptx": true, ".rst": true, ".txt": true,
}

var welcomeDataExt = map[string]bool{
	".csv": true, ".db": true, ".json": true, ".jsonl": true, ".parquet": true,
	".sql": true, ".sqlite": true, ".tsv": true, ".xls": true, ".xlsx": true, ".xml": true,
}

var welcomeMediaExt = map[string]bool{
	".aac": true, ".avi": true, ".bmp": true, ".gif": true, ".jpeg": true, ".jpg": true,
	".m4a": true, ".mkv": true, ".mov": true, ".mp3": true, ".mp4": true, ".png": true,
	".psd": true, ".svg": true, ".wav": true, ".webm": true, ".webp": true,
}

var welcomeResearchExt = map[string]bool{
	".bib": true, ".enw": true, ".ipynb": true, ".ris": true, ".tex": true,
}

var welcomeCodeMarkers = map[string]bool{
	"cargo.toml": true, "composer.json": true, "go.mod": true, "package.json": true,
	"pom.xml": true, "pyproject.toml": true, "requirements.txt": true, "unityproject": true,
}

// WorkspaceWelcome returns an idempotent snapshot for one tab. Filesystem
// access is shallow and capped so opening a new session cannot turn into a full
// workspace index.
func (a *App) WorkspaceWelcome(tabID string) WorkspaceWelcomeView {
	view := WorkspaceWelcomeView{ContentKinds: []string{}, ScannedAt: time.Now().UnixMilli()}
	tab, ok := a.welcomeTabSnapshot(tabID)
	if !ok {
		view.Degraded = true
		view.DegradedReason = "workspace unavailable"
		return view
	}
	view.Scope = tab.Scope
	if tab.Scope == "global" {
		view.WorkspaceName = globalProjectTitle()
		view.ContentKinds = []string{"unknown"}
	} else {
		view.WorkspaceName = workspaceName(tab.WorkspaceRoot)
		base, err := workspaceBaseFromRoot(tab.WorkspaceRoot)
		if err != nil {
			view.Degraded = true
			view.DegradedReason = "workspace scan unavailable"
		} else {
			profile := scanWorkspaceWelcome(base)
			view.ContentKinds = profile.kinds
			view.Confidence = profile.confidence
			view.FileCount = profile.files
			view.Partial = profile.partial
			if profile.partial {
				view.DegradedReason = "workspace scan partial"
			}
		}

		changes := a.WorkspaceChanges(tab.ID)
		view.ChangedCount = len(changes.Files)
	}
	view.SessionCount, view.RecentTitle, view.RecentActivity = welcomeRecentActivity(
		a.ListProjectTree(), tab.Scope, tab.WorkspaceRoot, tab.TopicID,
	)
	return view
}

func (a *App) welcomeTabSnapshot(tabID string) (WorkspaceTab, bool) {
	tabID = strings.TrimSpace(tabID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	tab := a.tabByIDLocked(tabID)
	if tab == nil {
		return WorkspaceTab{}, false
	}
	return WorkspaceTab{
		ID: tab.ID, Scope: tab.Scope, WorkspaceRoot: tab.WorkspaceRoot,
		TopicID: tab.TopicID,
	}, true
}

func scanWorkspaceWelcome(base string) welcomeProfile {
	counts := map[string]int{"code": 0, "docs": 0, "data": 0, "media": 0, "research": 0}
	profile := welcomeProfile{kinds: []string{}}
	marker := false
	root := filepath.Clean(base)
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			profile.partial = true
			return nil
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			profile.partial = true
			return nil
		}
		depth := len(strings.FieldsFunc(filepath.ToSlash(rel), func(r rune) bool { return r == '/' }))
		if entry.IsDir() {
			if depth > 2 || skipWorkspaceEntry(filepath.Dir(filepath.ToSlash(rel)), entry.Name(), true) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || skipWorkspaceEntry(filepath.Dir(filepath.ToSlash(rel)), entry.Name(), false) {
			return nil
		}
		if profile.files >= welcomeScanFileLimit {
			profile.partial = true
			return fs.SkipAll
		}
		profile.files++
		name := strings.ToLower(entry.Name())
		ext := strings.ToLower(filepath.Ext(name))
		if welcomeCodeMarkers[name] {
			marker = true
			counts["code"]++
		}
		switch {
		case welcomeResearchExt[ext]:
			counts["research"]++
		case welcomeCodeExt[ext]:
			counts["code"]++
		case welcomeDocExt[ext]:
			counts["docs"]++
		case welcomeDataExt[ext]:
			counts["data"]++
		case welcomeMediaExt[ext]:
			counts["media"]++
		}
		return nil
	})

	ordered := []string{"code", "docs", "data", "media", "research"}
	sort.SliceStable(ordered, func(i, j int) bool { return counts[ordered[i]] > counts[ordered[j]] })
	for _, kind := range ordered {
		if counts[kind] > 0 {
			profile.kinds = append(profile.kinds, kind)
		}
	}
	if profile.files == 0 {
		profile.kinds = []string{"empty"}
		profile.confidence = 0.9
	} else if marker {
		profile.confidence = 0.95
	} else if len(profile.kinds) > 0 && profile.files >= 5 {
		profile.confidence = 0.8
	} else if len(profile.kinds) > 0 {
		profile.confidence = 0.65
	} else {
		profile.kinds = []string{"unknown"}
		profile.confidence = 0.35
	}
	if profile.partial && profile.confidence > 0.55 {
		profile.confidence = 0.55
	}
	return profile
}

func welcomeRecentActivity(tree []ProjectNode, scope, root, currentTopic string) (int, string, int64) {
	normalizedRoot := normalizeProjectRoot(root)
	for _, node := range tree {
		matches := scope == "global" && node.Kind == "global_folder"
		if scope != "global" && node.Kind == "project" && normalizeProjectRoot(node.Root) == normalizedRoot {
			matches = true
		}
		if !matches {
			continue
		}
		count := 0
		recentTitle := ""
		var recentActivity int64
		for _, child := range node.Children {
			if child.Kind == "topic" || child.Kind == "session" || child.Kind == "global_topic" || child.Kind == "global_session" {
				count++
			}
			if recentTitle != "" || child.TopicID == currentTopic || strings.TrimSpace(child.Label) == "" {
				continue
			}
			recentTitle = strings.TrimSpace(child.Label)
			recentActivity = child.LastActivityAt
		}
		return count, recentTitle, recentActivity
	}
	return 0, "", 0
}
