package main

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type nativeWindowAction int

const (
	nativeWindowActionMinimise nativeWindowAction = iota + 1
	nativeWindowActionDismiss
)

// MinimiseMainWindow backs the Windows frameless titlebar controls.
func (a *App) MinimiseMainWindow() {
	if a.windowMinimise != nil {
		a.windowMinimise()
		return
	}
	if a.ctx == nil {
		return
	}
	runtime.WindowMinimise(a.ctx)
}

// requestNativeWindowAction routes native title-bar controls through the same
// widget semantics as the Windows frameless controls. The platform callback
// returns immediately; the transition runs off the AppKit main thread and
// duplicate clicks collapse into the active request.
func (a *App) requestNativeWindowAction(action nativeWindowAction) {
	if a == nil || !a.nativeWindowActionInFlight.CompareAndSwap(false, true) {
		return
	}
	a.goSafe("nativeWindowAction", func() {
		defer a.nativeWindowActionInFlight.Store(false)
		if err := a.performNativeWindowAction(action); err != nil {
			slog.Error("desktop: native window action failed", "action", action, "err", err)
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "window:action-error", err.Error())
			}
		}
	})
}

func (a *App) performNativeWindowAction(action nativeWindowAction) error {
	enabled, _, err := a.desktopWidgetPreferences()
	if err != nil {
		return fmt.Errorf("read widget settings: %w", err)
	}
	if enabled {
		switch action {
		case nativeWindowActionMinimise:
			return a.enterWidgetMode()
		case nativeWindowActionDismiss:
			return a.dismissMainWindowToWidget()
		default:
			return fmt.Errorf("unknown native window action %d", action)
		}
	}

	// A settings toggle can race a native click that was already delivered.
	// Preserve standard window behavior when the widget was disabled before the
	// asynchronous callback reached Go.
	switch action {
	case nativeWindowActionMinimise:
		a.MinimiseMainWindow()
		return nil
	case nativeWindowActionDismiss:
		a.CloseMainWindow()
		return nil
	default:
		return fmt.Errorf("unknown native window action %d", action)
	}
}

// ToggleMaximiseMainWindow backs the Windows frameless titlebar controls.
func (a *App) ToggleMaximiseMainWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowToggleMaximise(a.ctx)
}

// IsMainWindowMaximised reports the native maximise state for the Windows
// frameless titlebar controls.
func (a *App) IsMainWindowMaximised() bool {
	if a.ctx == nil {
		return false
	}
	return runtime.WindowIsMaximised(a.ctx)
}

// CloseMainWindow preserves WorkGround2's configured close behavior for the
// Windows frameless titlebar close button.
func (a *App) CloseMainWindow() {
	if a.ctx == nil {
		return
	}
	if a.beforeClose(a.ctx) {
		return
	}
	a.forceQuit.Store(true)
	runtime.Quit(a.ctx)
}

// DismissMainWindow removes the active session's retained widget icon when it
// is currently visible, then enters widget mode. A missing icon is a successful
// no-op, so repeated clicks safely retry only the window transition.
func (a *App) DismissMainWindow() error {
	if a.ctx == nil {
		return errors.New("desktop window is not ready")
	}
	enabled, _, err := a.desktopWidgetPreferences()
	if err != nil {
		return fmt.Errorf("read widget settings before dismiss: %w", err)
	}
	if !enabled {
		return errors.New("desktop widget is disabled")
	}
	return a.dismissMainWindowToWidget()
}

func (a *App) dismissMainWindowToWidget() error {
	if _, err := a.removeActiveSessionDesktopIcon(); err != nil {
		return err
	}
	if err := a.enterWidgetMode(); err != nil {
		return fmt.Errorf("enter widget mode after dismiss: %w", err)
	}
	return nil
}
