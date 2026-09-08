package main

import (
	"log/slog"
	"sync"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type desktopTray struct {
	end          func()
	openItem     *systray.MenuItem
	relocateItem *systray.MenuItem
	quitItem     *systray.MenuItem
	once         sync.Once
	ready        chan struct{}
	readyOnce    sync.Once
}

func newDesktopTray() *desktopTray {
	return &desktopTray{ready: make(chan struct{})}
}

func (t *desktopTray) markReady() {
	t.readyOnce.Do(func() {
		close(t.ready)
	})
}

func (a *App) startTray() bool {
	if !traySupported() {
		return false
	}
	a.mu.Lock()
	if a.tray != nil {
		a.mu.Unlock()
		return true
	}
	t := newDesktopTray()
	a.tray = t
	a.mu.Unlock()

	t.end = startDesktopTray(func() {
		systray.SetIcon(trayIconBytes)
		systray.SetTitle("WorkGround2")
		systray.SetTooltip("WorkGround2")
		// Run off the systray Win32 message loop: SetOnTapped fires inside wndProc,
		// so a blocking showFromTray (a wedged webview after sleep freezes
		// runtime.WindowShow) would stall the whole tray's message pump (#3834). The
		// menu items below are already decoupled via goroutines for the same reason.
		systray.SetOnTapped(func() { a.goSafe("showFromTray", a.showFromTray) })
		// Keep secondary/right-click on systray's native menu path.
		systray.SetOnSecondaryTapped(nil)

		labels := trayMenuLabels(a.trayLocale())
		t.openItem = systray.AddMenuItem(labels.openTitle, labels.openTooltip)
		t.relocateItem = systray.AddMenuItem(labels.relocateTitle, labels.relocateTooltip)
		t.quitItem = systray.AddMenuItem(labels.quitTitle, labels.quitTooltip)

		a.mu.Lock()
		a.trayReady = true
		a.mu.Unlock()
		t.markReady()

		a.goSafe("trayOpenLoop", func() {
			for range t.openItem.ClickedCh {
				a.showFromTray()
			}
		})
		a.goSafe("trayRelocateLoop", func() {
			for range t.relocateItem.ClickedCh {
				a.relocateFromTray()
			}
		})
		a.goSafe("trayQuitLoop", func() {
			for range t.quitItem.ClickedCh {
				a.quitFromTray()
			}
		})
	}, func() {
		a.mu.Lock()
		if a.tray == t {
			a.trayReady = false
			a.tray = nil
		}
		a.mu.Unlock()
	})
	return true
}

func (a *App) stopTray() {
	a.mu.RLock()
	t := a.tray
	a.mu.RUnlock()
	if t == nil || t.end == nil {
		return
	}
	t.once.Do(t.end)
}

func (a *App) updateTrayLocale(locale string) {
	a.mu.RLock()
	t := a.tray
	a.mu.RUnlock()
	if t == nil || t.openItem == nil || t.relocateItem == nil || t.quitItem == nil {
		return
	}
	labels := trayMenuLabels(locale)
	t.openItem.SetTitle(labels.openTitle)
	t.openItem.SetTooltip(labels.openTooltip)
	t.relocateItem.SetTitle(labels.relocateTitle)
	t.relocateItem.SetTooltip(labels.relocateTooltip)
	t.quitItem.SetTitle(labels.quitTitle)
	t.quitItem.SetTooltip(labels.quitTooltip)
}

func (a *App) trayLocale() string {
	cfg, _, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return ""
	}
	return cfg.DesktopLanguage()
}

func (a *App) showFromTray() {
	a.showMainWindow()
}

// relocateFromTray runs the tray 重定位/Relocate recovery off the systray
// message loop: a click re-queries the live screen geometry and repositions the
// active widget/session window back into the visible work area. A single
// in-flight guard collapses repeated clicks while one recovery runs; the
// underlying geometry work is idempotent, so re-fired clicks after a failure
// retry safely.
func (a *App) relocateFromTray() {
	if a.ctx == nil || !a.relocateInFlight.CompareAndSwap(false, true) {
		return
	}
	a.goSafe("trayRelocate", func() {
		defer a.relocateInFlight.Store(false)
		if err := a.relocateActiveWindow(); err != nil {
			slog.Error("desktop: tray relocate failed", "err", err)
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "window:action-error", err.Error())
			}
		}
	})
}

func (a *App) quitFromTray() {
	a.quitApp()
}

type trayLabels struct {
	openTitle       string
	openTooltip     string
	relocateTitle   string
	relocateTooltip string
	quitTitle       string
	quitTooltip     string
}

func trayMenuLabels(locale string) trayLabels {
	if locale == "zh" {
		return trayLabels{
			openTitle:       "打开",
			openTooltip:     "打开 WorkGround2 窗口",
			relocateTitle:   "重定位",
			relocateTooltip: "重新检测屏幕并将窗口移回可见区域",
			quitTitle:       "退出",
			quitTooltip:     "退出 WorkGround2",
		}
	}
	return trayLabels{
		openTitle:       "Open",
		openTooltip:     "Open the WorkGround2 window",
		relocateTitle:   "Relocate",
		relocateTooltip: "Re-detect screens and bring the window back into the visible area",
		quitTitle:       "Quit",
		quitTooltip:     "Quit WorkGround2",
	}
}
