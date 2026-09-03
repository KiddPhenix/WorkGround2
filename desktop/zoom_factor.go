package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/config"
)

const desktopZoomChangedEvent = "desktop:zoom-factor"

// DesktopZoomFactor persists the user's WebView2 zoom factor preference. The
// running controller applies changes immediately; main.go restores the same
// value before wails.Run() on the next launch.
type DesktopZoomFactor struct {
	ZoomFactor float64 `json:"zoomFactor"`
}

func zoomFactorPath() string {
	return filepath.Join(config.MemoryUserDir(), "desktop-zoom.json")
}

// loadZoomFactor reads the saved zoom factor. The bool is false when no saved
// value exists (first launch, missing file, corrupt JSON). Callers should fall
// back to 1.0 (no zoom) in that case.
func loadZoomFactor() (float64, bool) {
	path := zoomFactorPath()
	data, err := readFileUTF8(path)
	if err != nil {
		return 0, false
	}
	var zf DesktopZoomFactor
	if err := json.Unmarshal(data, &zf); err != nil {
		return 0, false
	}
	if zf.ZoomFactor < 0.5 || zf.ZoomFactor > 2.0 {
		return 0, false
	}
	return zf.ZoomFactor, true
}

// GetDesktopZoomFactor returns the current effective zoom factor. Successful
// live changes and persistence are committed together, so the saved value is
// also the active value; missing or corrupt state safely falls back to 1.0.
func (a *App) GetDesktopZoomFactor() float64 {
	zf, ok := loadZoomFactor()
	if !ok {
		return 1.0
	}
	return zf
}

func normalizeDesktopZoomFactor(factor float64) (float64, error) {
	if math.IsNaN(factor) || math.IsInf(factor, 0) {
		return 0, fmt.Errorf("desktop zoom factor must be finite")
	}
	if factor < 0.5 {
		factor = 0.5
	}
	if factor > 2.0 {
		factor = 2.0
	}
	return factor, nil
}

func saveZoomFactor(factor float64) error {
	path := zoomFactorPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(DesktopZoomFactor{ZoomFactor: factor})
	if err != nil {
		return err
	}
	return writeAtomic(path, data, 0o644)
}

func (a *App) applyDesktopZoom(factor float64) error {
	if a.desktopZoomApply != nil {
		return a.desktopZoomApply(factor)
	}
	if a.ctx == nil {
		return nil
	}
	return applyDesktopWebViewZoom(a.ctx, factor)
}

func (a *App) persistDesktopZoom(factor float64) error {
	if a.desktopZoomSave != nil {
		return a.desktopZoomSave(factor)
	}
	return saveZoomFactor(factor)
}

// SetDesktopZoomFactor applies a zoom factor to the running WebView and then
// persists it for the next launch. Persistence failure rolls the live WebView
// back to its previous effective value, keeping one recoverable source of truth.
func (a *App) SetDesktopZoomFactor(factor float64) error {
	normalized, err := normalizeDesktopZoomFactor(factor)
	if err != nil {
		return err
	}

	a.desktopZoomMu.Lock()
	defer a.desktopZoomMu.Unlock()

	previous := a.GetDesktopZoomFactor()
	if err := a.applyDesktopZoom(normalized); err != nil {
		return fmt.Errorf("apply desktop zoom: %w", err)
	}
	if err := a.persistDesktopZoom(normalized); err != nil {
		persistErr := fmt.Errorf("persist desktop zoom: %w", err)
		if rollbackErr := a.applyDesktopZoom(previous); rollbackErr != nil {
			return errors.Join(persistErr, fmt.Errorf("rollback desktop zoom to %.2f: %w", previous, rollbackErr))
		}
		return persistErr
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, desktopZoomChangedEvent, normalized)
	}
	return nil
}

// RestartApplication is retained for compatibility with older generated
// bindings. Display zoom changes no longer require it.
func (a *App) RestartApplication() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
