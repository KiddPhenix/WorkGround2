package main

import (
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
			if err := a.DeleteSession(info.Path); err != nil {
				slog.Warn("desktop: idle session trash failed", "path", info.Path, "err", err)
				continue
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
