package main

import (
	"errors"
	"fmt"
)

// This file implements the tray 重定位/Relocate recovery. One click re-queries
// the live screen/work area and brings whichever native window state is active
// back into the visible usable area after a monitor or resolution change:
//
//   - widget mode (pager or desktop-icon surface) is re-shown, re-normalized
//     against the current monitors and re-applied, then persisted; the saved
//     main-window geometry is repaired at the same time so exiting widget mode
//     lands back on screen. An icon-surface change refreshes the authoritative
//     widgetSurface and revision and republishes widget:mode so React
//     re-establishes regions and rejects stale requests.
//   - main/session mode restores a hidden or minimized window (honoring
//     backgroundMaximised), reads the live geometry after the restore, fits an
//     oversized window into the work area and pulls an off-screen origin back
//     on a monitor, persisting the corrected geometry.
//
// Sessions and the mode are never touched; the whole sequence runs under
// widgetMu so it cannot interleave with Enter/ExitWidgetMode or a style
// switch. All geometry is re-read at click time — no screen cache is used.

// relocateActiveWindow is the single entry point for the tray Relocate item.
// It reports an error when the desktop window is not ready; failures surface
// through the caller (slog + window:action-error), and a repeated click retries
// safely because every step is idempotent.
func (a *App) relocateActiveWindow() error {
	if a.ctx == nil {
		return errors.New("desktop window is not ready")
	}
	a.widgetMu.Lock()
	var publish bool
	var err error
	if a.widgetMode {
		publish, err = a.relocateWidgetWindowLocked()
	} else {
		err = a.relocateMainWindowLocked()
	}
	mode := a.widgetMode
	a.widgetMu.Unlock()
	// Publish after releasing widgetMu: an icon-surface relocation clears the
	// native HRGN, so React must re-establish the surface/regions through an
	// authoritative read keyed on the new revision. Publishing even when a
	// later persistence step failed keeps the live window and the frontend in
	// sync; the returned error still surfaces for retry.
	if publish {
		a.runtimeEvents.Emit(a.ctx, "widget:mode", mode)
	}
	return err
}

// relocateWidgetWindowLocked re-shows the currently active widget window and
// re-normalizes it (pager or desktop-icon surface, per a.widgetStyle) onto a
// live monitor, re-applying and persisting the corrected geometry. The caller
// must hold widgetMu. The first return value reports whether an icon-surface
// geometry change needs a widget:mode republish. Geometry that already fits is
// still persisted so a previous apply-success/save-failure is healed on retry.
func (a *App) relocateWidgetWindowLocked() (publish bool, err error) {
	a.showRestoredWindow(false)
	current, _ := a.windowReadState()
	state := WidgetWindowState{Width: current.Width, Height: current.Height, X: current.X, Y: current.Y}
	normalized, err := a.normalizeWidgetState(state)
	if err != nil {
		return false, fmt.Errorf("relocate widget window: %w", err)
	}
	if normalized != state {
		cfg, _, err := a.loadDesktopUserConfigForView()
		if err != nil {
			return false, fmt.Errorf("relocate widget window: read settings: %w", err)
		}
		apply := a.applyWidgetGeometry
		if a.widgetStyle == "icons" {
			apply = a.applyDesktopIconGeometry
		}
		if err := apply(normalized, cfg.DesktopWidgetAlwaysOnTop()); err != nil {
			return false, fmt.Errorf("relocate widget window: %w", err)
		}
		if a.widgetStyle == "icons" {
			// applyDesktopIconGeometry clears the hit-region HRGN, so refresh the
			// authoritative surface runtime and fence the old frontend revision:
			// React re-establishes the surface through the republished mode event
			// and any stale in-flight resize/region request is rejected.
			a.widgetSurface = newDesktopIconSurfaceRuntime(normalized)
			a.widgetRevision++
			publish = true
		}
	}
	save := saveWidgetWindowState
	if a.widgetStyle == "icons" {
		save = saveDesktopIconWindowState
	}
	var errs []error
	if err := save(normalized); err != nil {
		errs = append(errs, fmt.Errorf("relocate widget window: persist geometry: %w", err))
	}
	// The main window is not visible now, but Exit/reconcile restore its saved
	// geometry without normalization; repair it here so switching back cannot
	// undo the recovery.
	if err := a.repairSavedMainWindowLocked(); err != nil {
		errs = append(errs, err)
	}
	return publish, errors.Join(errs...)
}

// repairSavedMainWindowLocked normalizes and persists the saved main-window
// geometry against the live monitors. The caller must hold widgetMu. A missing
// file is fine; every present file is rewritten (normalized or not) so an
// earlier save failure heals on the next relocate.
func (a *App) repairSavedMainWindowLocked() error {
	state, ok := loadWindowState()
	if !ok {
		return nil
	}
	normalized, err := a.normalizeMainState(state)
	if err != nil {
		return fmt.Errorf("relocate widget window: normalize saved main window: %w", err)
	}
	if err := saveMainWindowState(normalized); err != nil {
		return fmt.Errorf("relocate widget window: persist saved main window: %w", err)
	}
	return nil
}

// relocateMainWindowLocked restores the session/main window after a monitor or
// resolution change. The restore happens first (a minimized window reports
// meaningless -32000 coordinates before it), honoring the backgroundMaximised
// flag recorded when the window was hidden; geometry is read only after the
// show, so the correction starts from the real restored bounds. A maximised
// window is bounded by its monitor work area and Windows re-homes it when a
// display disappears, so it is only shown. Oversized windows are fitted into
// the live work area, off-screen origins pulled back onto a monitor, and the
// corrected geometry persisted — always, so an earlier apply-success/
// save-failure heals on retry. The caller must hold widgetMu.
func (a *App) relocateMainWindowLocked() error {
	_, maximisedBefore := a.windowReadState()
	backgroundMaximised := a.backgroundMaximised.Swap(false)
	wasMaximised := maximisedBefore || backgroundMaximised
	a.showRestoredWindow(wasMaximised)
	if maximisedBefore {
		return nil
	}
	current, maximised := a.windowReadState()
	if maximised {
		return nil
	}
	state := DesktopWindowState{Width: current.Width, Height: current.Height, X: current.X, Y: current.Y}
	if state.Width <= 0 || state.Height <= 0 {
		return fmt.Errorf("relocate main window: window has no usable size (%dx%d at %d,%d)", state.Width, state.Height, state.X, state.Y)
	}
	normalized, err := a.normalizeMainState(state)
	if err != nil {
		return fmt.Errorf("relocate main window: %w", err)
	}
	if normalized != state {
		if err := a.restoreMainGeometry(normalized, true); err != nil {
			return fmt.Errorf("relocate main window: %w", err)
		}
	}
	if err := saveMainWindowState(normalized); err != nil {
		return fmt.Errorf("relocate main window: persist geometry: %w", err)
	}
	return nil
}

// showRestoredWindow restores a hidden or minimized window (main or widget)
// before a Relocate geometry fix, using the same per-platform plan as the tray
// Open item (maximise-before-show on Windows when the window was maximised).
// windowRestoreShow is the test-only seam; nil uses the platform plan.
func (a *App) showRestoredWindow(wasMaximised bool) {
	if a.windowRestoreShow != nil {
		a.windowRestoreShow(wasMaximised)
		return
	}
	if a.ctx == nil {
		return
	}
	showFromBackground(a.ctx, wasMaximised)
}
