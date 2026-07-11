package main

import (
	"log/slog"
	"time"

	"workground2/internal/agent"
)

const recoveryGCInterval = 6 * time.Hour

func (a *App) startRecoveryGC() {
	if a.ctx == nil {
		return
	}
	a.goSafe("recoveryGC", func() {
		a.sweepReclaimableRecoveryBranches()
		ticker := time.NewTicker(recoveryGCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				a.sweepReclaimableRecoveryBranches()
			}
		}
	})
}

func (a *App) sweepReclaimableRecoveryBranches() int {
	return a.reclaimRecoveryBranchesIn(a.knownSessionDirs(), time.Now())
}

func (a *App) reclaimRecoveryBranchesIn(dirs []string, now time.Time) int {
	reclaimed := 0
	for _, dir := range dirs {
		reclaimable, err := agent.ReclaimableRecoveryBranches(dir, now, agent.RecoveryGCGracePeriod)
		if err != nil {
			slog.Warn("desktop: scan reclaimable recovery branches", "dir", dir, "err", err)
			continue
		}
		for _, path := range reclaimable {
			if agent.SessionLeaseHeld(path) || a.sessionOpenInAnyTab(path) {
				continue
			}
			if err := a.DeleteSession(path); err != nil {
				slog.Warn("desktop: trash reclaimed recovery branch", "path", path, "err", err)
				continue
			}
			reclaimed++
		}
	}
	if reclaimed > 0 {
		slog.Info("desktop: moved redundant recovery branches to the session trash", "count", reclaimed)
		a.emitProjectTreeChanged()
	}
	return reclaimed
}

func (a *App) sessionOpenInAnyTab(path string) bool {
	key := sessionRuntimeKey(path)
	if key == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.tabs {
		if tab != nil && sessionRuntimeKey(tab.currentSessionPath()) == key {
			return true
		}
	}
	for _, tab := range a.detachedSessions {
		if tab != nil && sessionRuntimeKey(tab.currentSessionPath()) == key {
			return true
		}
	}
	return false
}
