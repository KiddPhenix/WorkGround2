package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"workground2/internal/agent"
)

const (
	externalSessionGCInterval = 5 * time.Minute
	imSessionIdleLimit        = 4 * time.Hour
	cliSessionIdleLimit       = 6 * time.Hour
)

// startExternalSessionGC reclaims idle sessions created by external surfaces.
// It starts only after tab restore, so an idle desktop-owned CLI tab can be
// detached safely through DeleteSession instead of racing its controller.
func (a *App) startExternalSessionGC() {
	if a.ctx == nil {
		return
	}
	a.goSafe("externalSessionGC", func() {
		// First sweep: reconcile historically trashed external sessions
		// that left orphan topic indexes behind.
		a.reconcileOrphanTopics()
		a.sweepExternalSessions(time.Now())
		ticker := time.NewTicker(externalSessionGCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case now := <-ticker.C:
				a.sweepExternalSessions(now)
			}
		}
	})
}

func externalSessionIdleLimit(meta agent.BranchMeta) (time.Duration, bool) {
	switch strings.ToLower(strings.TrimSpace(meta.SessionSource)) {
	case "cli":
		return cliSessionIdleLimit, true
	case "auto":
		if strings.TrimSpace(meta.Channel) == "" {
			return 0, false
		}
		return imSessionIdleLimit, true
	default:
		return 0, false
	}
}

func (a *App) sweepExternalSessions(now time.Time) int {
	if !a.externalSessionGCRunning.CompareAndSwap(false, true) {
		return 0
	}
	defer a.externalSessionGCRunning.Store(false)

	moved := 0
	for _, dir := range a.knownSessionDirs() {
		infos, err := agent.ListSessions(dir)
		if err != nil {
			slog.Warn("desktop: list external sessions for idle GC", "dir", dir, "err", err)
			continue
		}
		for _, info := range infos {
			if !a.shouldTrashExternalSession(info.Path, now) {
				continue
			}
			// Capture topic before DeleteSession removes the session.
			topicID := strings.TrimSpace(info.TopicID)
			scope := info.Scope
			workspaceRoot := info.WorkspaceRoot
			if err := a.DeleteSession(info.Path); err != nil {
				slog.Warn("desktop: idle session trash failed", "path", info.Path, "err", err)
				continue
			}
			if topicID != "" {
				if err := a.removeOrphanExternalTopic(topicID, scope, workspaceRoot); err != nil {
					slog.Warn("desktop: remove expired session topic index", "topic", topicID, "err", err)
				}
			}
			moved++
		}
	}
	if moved > 0 {
		slog.Info("desktop: moved idle external sessions to trash", "count", moved)
	}
	return moved
}

func (a *App) shouldTrashExternalSession(path string, now time.Time) bool {
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok || meta.UpdatedAt.IsZero() {
		return false
	}
	limit, eligible := externalSessionIdleLimit(meta)
	if !eligible || now.Sub(meta.UpdatedAt) < limit || a.externalSessionPinned(meta) {
		return false
	}
	local, busy := a.externalSessionRuntimeState(path)
	if busy || agent.IsCleanupPending(path) {
		return false
	}
	if !local && agent.SessionLeaseHeld(path) {
		return false
	}
	// Re-read just before destructive work. A late message/pin must win over a
	// stale listing snapshot.
	meta, ok, err = agent.LoadBranchMeta(path)
	if err != nil || !ok || meta.UpdatedAt.IsZero() || a.externalSessionPinned(meta) {
		return false
	}
	limit, eligible = externalSessionIdleLimit(meta)
	return eligible && now.Sub(meta.UpdatedAt) >= limit
}

func (a *App) externalSessionRuntimeState(path string) (local, busy bool) {
	key := sessionRuntimeKey(path)
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || sessionRuntimeKey(tab.currentSessionPath()) != key {
			continue
		}
		local = true
		if tab.Ctrl == nil {
			return local, true
		}
		status := tab.Ctrl.RuntimeStatus()
		return local, status.ActiveRuntimeWork || status.PendingPrompt || status.BackgroundJobs > 0
	}
	return false, false
}

func (a *App) externalSessionPinned(meta agent.BranchMeta) bool {
	if meta.Pinned || strings.TrimSpace(meta.TopicID) == "" {
		return meta.Pinned
	}
	f := loadProjectsFile()
	if strings.EqualFold(strings.TrimSpace(meta.Scope), "global") {
		return containsDesktopString(f.GlobalPinnedTopics, meta.TopicID)
	}
	for _, project := range f.Projects {
		if normalizeProjectRoot(project.Root) == normalizeProjectRoot(meta.WorkspaceRoot) {
			return containsDesktopString(project.PinnedTopics, meta.TopicID)
		}
	}
	return false
}

// reconcileOrphanTopics scans trashed external sessions and removes topic
// indexes for topics that have no remaining live sessions or runtime tabs.
// It targets only external sessions (CLI / auto IM) whose GC would have
// already moved them to trash, not user-deleted sessions. Idempotent:
// repeated calls skip topics that still have live sessions.
func (a *App) reconcileOrphanTopics() {
	for _, dir := range a.knownSessionDirs() {
		trashed, err := listTrashedSessionFiles(dir)
		if err != nil {
			slog.Warn("desktop: list trashed for orphan topic reconcile",
				"dir", dir, "err", err)
			continue
		}
		for _, trashPath := range trashed {
			meta, ok, err := agent.LoadBranchMeta(trashPath)
			if err != nil || !ok {
				continue
			}
			// Only external sessions (CLI / auto IM) are eligible.
			if _, eligible := externalSessionIdleLimit(meta); !eligible {
				continue
			}
			topicID := strings.TrimSpace(meta.TopicID)
			if topicID == "" {
				continue
			}
			if err := a.removeOrphanExternalTopic(topicID, meta.Scope, meta.WorkspaceRoot); err != nil {
				slog.Warn("desktop: reconcile orphan external topic", "topic", topicID, "err", err)
			}
		}
	}
}

// removeOrphanExternalTopic removes an external topic only after a final
// live-session/runtime check under the topic-index transaction lock. A failed
// sidecar write remains visible to the caller and is safe to retry next sweep.
func (a *App) removeOrphanExternalTopic(topicID, scope, workspaceRoot string) error {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(scope), "global") {
		workspaceRoot = ""
	} else {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
	}

	topicIndexMu.Lock()
	defer topicIndexMu.Unlock()
	if a.externalTopicHasLiveOwner(scope, workspaceRoot, topicID) {
		return nil
	}

	removed := false
	var errs []error
	if titles, err := loadTopicTitlesForUpdate(workspaceRoot); err != nil {
		errs = append(errs, fmt.Errorf("load topic titles: %w", err))
	} else if _, ok := titles[topicID]; ok {
		delete(titles, topicID)
		removed = true
		if err := saveTopicTitles(workspaceRoot, titles); err != nil {
			errs = append(errs, fmt.Errorf("save topic titles: %w", err))
		}
	}
	if sources, err := loadTopicTitleSourcesForUpdate(workspaceRoot); err != nil {
		errs = append(errs, fmt.Errorf("load topic title sources: %w", err))
	} else if _, ok := sources[topicID]; ok {
		delete(sources, topicID)
		removed = true
		if err := saveTopicTitleSources(workspaceRoot, sources); err != nil {
			errs = append(errs, fmt.Errorf("save topic title sources: %w", err))
		}
	}
	if created, err := loadTopicCreatedAtsForUpdate(workspaceRoot); err != nil {
		errs = append(errs, fmt.Errorf("load topic created times: %w", err))
	} else if _, ok := created[topicID]; ok {
		delete(created, topicID)
		removed = true
		if err := saveTopicCreatedAts(workspaceRoot, created); err != nil {
			errs = append(errs, fmt.Errorf("save topic created times: %w", err))
		}
	}
	if topicListedInProjects(loadProjectsFile(), topicID) {
		removed = true
	}
	if err := removeTopicFromProjectsFile(topicID); err != nil {
		errs = append(errs, fmt.Errorf("save projects index: %w", err))
	}
	if removed {
		a.emitProjectTreeChanged()
	}
	return errors.Join(errs...)
}

func (a *App) externalTopicHasLiveOwner(scope, workspaceRoot, topicID string) bool {
	if path, _ := a.findTopicSessionForTarget(scope, workspaceRoot, topicID); path != "" {
		return true
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.tabs {
		if tab != nil && tab.TopicID == topicID {
			return true
		}
	}
	for _, tab := range a.detachedSessions {
		if tab != nil && tab.TopicID == topicID {
			return true
		}
	}
	return false
}

func topicListedInProjects(f desktopProjectFile, topicID string) bool {
	if containsDesktopString(f.GlobalTopics, topicID) || containsDesktopString(f.GlobalPinnedTopics, topicID) {
		return true
	}
	for _, project := range f.Projects {
		if containsDesktopString(project.Topics, topicID) || containsDesktopString(project.PinnedTopics, topicID) {
			return true
		}
	}
	return false
}
