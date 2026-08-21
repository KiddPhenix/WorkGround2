//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32                = syscall.NewLazyDLL("user32.dll")
	gdi32                 = syscall.NewLazyDLL("gdi32.dll")
	shcore                = syscall.NewLazyDLL("shcore.dll")
	dwmapi                = syscall.NewLazyDLL("dwmapi.dll")
	procFindWindowExW     = user32.NewProc("FindWindowExW")
	procWindowProcessID   = user32.NewProc("GetWindowThreadProcessId")
	procGetDpiForWindow   = user32.NewProc("GetDpiForWindow")
	procGetWindowRect     = user32.NewProc("GetWindowRect")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
	procRedrawWindow      = user32.NewProc("RedrawWindow")
	procEnumDisplay       = user32.NewProc("EnumDisplayMonitors")
	procMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW   = user32.NewProc("GetMonitorInfoW")
	procGetClientRect     = user32.NewProc("GetClientRect")
	procSetWindowRgn      = user32.NewProc("SetWindowRgn")
	procCreatePolygonRgn  = gdi32.NewProc("CreatePolygonRgn")
	procCreateRectRgn     = gdi32.NewProc("CreateRectRgn")
	procCombineRgn        = gdi32.NewProc("CombineRgn")
	procDeleteObject      = gdi32.NewProc("DeleteObject")
	procGetDpiForMonitor  = shcore.NewProc("GetDpiForMonitor")
	procDwmFlush          = dwmapi.NewProc("DwmFlush")
	procGetWindowLongW    = user32.NewProc("GetWindowLongW")
	procSetWindowLongW    = user32.NewProc("SetWindowLongW")
	procShowWindow        = user32.NewProc("ShowWindow")
	procIsWindowVisible   = user32.NewProc("IsWindowVisible")
)

type w32Point struct {
	X int32
	Y int32
}

type w32Rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type w32MonitorInfo struct {
	Size    uint32
	Monitor w32Rect
	Work    w32Rect
	Flags   uint32
}

const (
	monitorDefaultNearest = 2
	windowPosNoSize       = 0x0001
	windowPosNoMove       = 0x0002
	windowPosNoZOrder     = 0x0004
	windowPosNoActivate   = 0x0010
	windowPosFrameChanged = 0x0020
	windowPosNoOwnerOrder = 0x0200
	redrawInvalidate      = 0x0001
	redrawErase           = 0x0004
	redrawAllChildren     = 0x0080
	redrawUpdateNow       = 0x0100
	redrawFrame           = 0x0400
	widgetRedrawFlags     = redrawInvalidate | redrawErase | redrawAllChildren | redrawUpdateNow | redrawFrame

	gwlExStyle int32 = -20
	swHide           = 0
	swShow           = 5

	// Extended window styles that control taskbar presence. Every other bit is
	// preserved verbatim when switching, never cleared.
	wsExTransparent         = 0x00000020
	wsExToolWindow          = 0x00000080
	wsExControlParent       = 0x00010000
	wsExAppWindow           = 0x00040000
	wsExLayered             = 0x00080000
	wsExNoRedirectionBitmap = 0x00200000
	wsExNoActivate          = 0x08000000
)

type widgetMonitor struct {
	Handle  uintptr
	Work    w32Rect
	DPI     uint32
	Primary bool
}

// findWidgetHWND returns this process's Wails window. Filtering by process ID
// prevents one WorkGround2 instance from clipping another instance's window.
func findWidgetHWND() syscall.Handle {
	title, err := syscall.UTF16PtrFromString("WorkGround2")
	if err != nil {
		return 0
	}
	pid := uint32(os.Getpid())
	var after uintptr
	for {
		hwnd, _, _ := procFindWindowExW.Call(0, after, 0, uintptr(unsafe.Pointer(title)))
		if hwnd == 0 {
			return 0
		}
		var owner uint32
		procWindowProcessID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner == pid {
			return syscall.Handle(hwnd)
		}
		after = hwnd
	}
}

func widgetRegionPoints(width, height int, dpi uint32) ([8]w32Point, error) {
	if width < widgetMinWidth || height < widgetMinHeight {
		return [8]w32Point{}, fmt.Errorf("widget window too small (%dx%d)", width, height)
	}
	scale := func(value int) int32 { return int32(scaleForDPI(value, dpi)) }
	return [8]w32Point{
		{scale(17), 0},
		{scale(width - 12), 0},
		{scale(width), scale(12)},
		{scale(width), scale(height - 13)},
		{scale(width - 17), scale(height)},
		{scale(17), scale(height)},
		{0, scale(height - 15)},
		{0, scale(17)},
	}, nil
}

func scaleForDPI(value int, dpi uint32) int {
	if dpi == 0 {
		dpi = 96
	}
	return (value*int(dpi) + 48) / 96
}

func scaleToDefaultDPI(value int, dpi uint32) int {
	if dpi == 0 {
		dpi = 96
	}
	return (value*96 + int(dpi)/2) / int(dpi)
}

func widgetStateRect(state WidgetWindowState, dpi uint32) w32Rect {
	return w32Rect{
		Left:   int32(state.X),
		Top:    int32(state.Y),
		Right:  int32(state.X + scaleForDPI(state.Width, dpi)),
		Bottom: int32(state.Y + scaleForDPI(state.Height, dpi)),
	}
}

func rectIntersectionArea(a, b w32Rect) int64 {
	left := max(a.Left, b.Left)
	top := max(a.Top, b.Top)
	right := min(a.Right, b.Right)
	bottom := min(a.Bottom, b.Bottom)
	if right <= left || bottom <= top {
		return 0
	}
	return int64(right-left) * int64(bottom-top)
}

func clampWidgetStateToMonitor(state WidgetWindowState, monitor widgetMonitor) WidgetWindowState {
	workWidth := int(monitor.Work.Right - monitor.Work.Left)
	workHeight := int(monitor.Work.Bottom - monitor.Work.Top)
	state.Width = min(state.Width, max(widgetMinWidth, scaleToDefaultDPI(workWidth, monitor.DPI)))
	state.Height = min(state.Height, max(widgetMinHeight, scaleToDefaultDPI(workHeight, monitor.DPI)))
	width := scaleForDPI(state.Width, monitor.DPI)
	height := scaleForDPI(state.Height, monitor.DPI)
	maxX := int(monitor.Work.Right) - width
	maxY := int(monitor.Work.Bottom) - height
	state.X = max(int(monitor.Work.Left), min(state.X, maxX))
	state.Y = max(int(monitor.Work.Top), min(state.Y, maxY))
	return state
}

func normalizeWidgetStateForMonitors(state WidgetWindowState, monitors []widgetMonitor, fallback int) WidgetWindowState {
	if len(monitors) == 0 {
		return state
	}
	if fallback < 0 || fallback >= len(monitors) {
		fallback = 0
	}
	selected := fallback
	var bestArea int64
	for i, monitor := range monitors {
		area := rectIntersectionArea(widgetStateRect(state, monitor.DPI), monitor.Work)
		if area > bestArea {
			bestArea = area
			selected = i
		}
	}
	return clampWidgetStateToMonitor(state, monitors[selected])
}

func monitorDPI(handle uintptr, fallback uint32) uint32 {
	var x, y uint32
	hr, _, _ := procGetDpiForMonitor.Call(handle, 0, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y)))
	if hr == 0 && x > 0 {
		return x
	}
	if fallback == 0 {
		return 96
	}
	return fallback
}

func normalizeWidgetWindowState(_ context.Context, state WidgetWindowState) (WidgetWindowState, error) {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return state, fmt.Errorf("normalizeWidgetWindowState: window not found")
	}
	fallbackDPI, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	current, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultNearest)
	monitors := make([]widgetMonitor, 0, 4)
	callback := syscall.NewCallback(func(handle, _, _, _ uintptr) uintptr {
		info := w32MonitorInfo{Size: uint32(unsafe.Sizeof(w32MonitorInfo{}))}
		ret, _, _ := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
		if ret != 0 {
			monitors = append(monitors, widgetMonitor{
				Handle:  handle,
				Work:    info.Work,
				DPI:     monitorDPI(handle, uint32(fallbackDPI)),
				Primary: info.Flags&1 != 0,
			})
		}
		return 1
	})
	ret, _, callErr := procEnumDisplay.Call(0, 0, callback, 0)
	if ret == 0 {
		return state, fmt.Errorf("normalizeWidgetWindowState: enumerate monitors failed: %w", callErr)
	}
	if len(monitors) == 0 {
		return state, fmt.Errorf("normalizeWidgetWindowState: no visible monitors")
	}
	fallback := -1
	for i, monitor := range monitors {
		if monitor.Handle == current {
			fallback = i
			break
		}
		if fallback < 0 && monitor.Primary {
			fallback = i
		}
	}
	return normalizeWidgetStateForMonitors(state, monitors, fallback), nil
}

func runWidgetWindowRefresh(frame, redraw, flush func() error) error {
	if err := frame(); err != nil {
		return err
	}
	if err := redraw(); err != nil {
		return err
	}
	return flush()
}

func redrawWidgetWindow(hwnd syscall.Handle) error {
	return runWidgetWindowRefresh(
		func() error {
			flags := uintptr(windowPosNoMove | windowPosNoSize | windowPosNoZOrder | windowPosNoActivate | windowPosFrameChanged | windowPosNoOwnerOrder)
			ret, _, callErr := procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, flags)
			if ret == 0 {
				return fmt.Errorf("redrawWidgetWindow: frame refresh failed: %w", callErr)
			}
			return nil
		},
		func() error {
			ret, _, callErr := procRedrawWindow.Call(uintptr(hwnd), 0, 0, widgetRedrawFlags)
			if ret == 0 {
				return fmt.Errorf("redrawWidgetWindow: redraw failed: %w", callErr)
			}
			return nil
		},
		func() error {
			hr, _, _ := procDwmFlush.Call()
			if hr != 0 {
				return fmt.Errorf("redrawWidgetWindow: DwmFlush failed: HRESULT 0x%x", hr)
			}
			return nil
		},
	)
}

// setDesktopWindowBounds bypasses Wails' Windows SetPos implementation. Wails
// reads absolute screen coordinates but writes monitor-work-area-relative
// coordinates, so persisting and restoring its values drifts on every cycle.
// Width and height remain Wails logical units; x and y are absolute pixels.
func setDesktopWindowBounds(_ context.Context, width, height, x, y int) error {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return fmt.Errorf("setDesktopWindowBounds: window not found")
	}
	flags := uintptr(windowPosNoZOrder | windowPosNoActivate)
	ret, _, callErr := procSetWindowPos.Call(uintptr(hwnd), 0, uintptr(x), uintptr(y), 0, 0, flags|windowPosNoSize)
	if ret == 0 {
		return fmt.Errorf("setDesktopWindowBounds: move failed: %w", callErr)
	}
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	physicalWidth := scaleForDPI(width, uint32(dpi))
	physicalHeight := scaleForDPI(height, uint32(dpi))
	ret, _, callErr = procSetWindowPos.Call(
		uintptr(hwnd), 0, uintptr(x), uintptr(y), uintptr(physicalWidth), uintptr(physicalHeight), flags,
	)
	if ret == 0 {
		return fmt.Errorf("setDesktopWindowBounds: resize failed: %w", callErr)
	}
	var rect w32Rect
	ret, _, callErr = procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return fmt.Errorf("setDesktopWindowBounds: verify failed: %w", callErr)
	}
	if !nearWindowValue(int(rect.Left), x) || !nearWindowValue(int(rect.Top), y) ||
		!nearWindowValue(int(rect.Right-rect.Left), physicalWidth) || !nearWindowValue(int(rect.Bottom-rect.Top), physicalHeight) {
		return fmt.Errorf(
			"setDesktopWindowBounds: got (%d,%d %dx%d), want (%d,%d %dx%d)",
			rect.Left, rect.Top, rect.Right-rect.Left, rect.Bottom-rect.Top,
			x, y, physicalWidth, physicalHeight,
		)
	}
	return nil
}

func nearWindowValue(got, want int) bool {
	delta := got - want
	return delta >= -1 && delta <= 1
}

func nativeDefaultWidgetWindowState(_ context.Context) (WidgetWindowState, bool) {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return WidgetWindowState{}, false
	}
	monitor, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultNearest)
	if monitor == 0 {
		return WidgetWindowState{}, false
	}
	info := w32MonitorInfo{Size: uint32(unsafe.Sizeof(w32MonitorInfo{}))}
	ret, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return WidgetWindowState{}, false
	}
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	return defaultWidgetWindowStateForWorkArea(info.Work, uint32(dpi)), true
}

func defaultWidgetWindowStateForWorkArea(work w32Rect, dpi uint32) WidgetWindowState {
	logicalWidth := scaleToDefaultDPI(int(work.Right-work.Left), dpi)
	logicalHeight := scaleToDefaultDPI(int(work.Bottom-work.Top), dpi)
	state := defaultWidgetWindowStateForScreens(logicalWidth, logicalHeight)
	state.X = int(work.Right) - scaleForDPI(state.Width+widgetEdgeGap, dpi)
	state.Y = int(work.Bottom) - scaleForDPI(state.Height+widgetBottomGap, dpi)
	return state
}

func nativeDefaultDesktopIconWindowState(_ context.Context) (WidgetWindowState, bool) {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return WidgetWindowState{}, false
	}
	monitor, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultNearest)
	if monitor == 0 {
		return WidgetWindowState{}, false
	}
	info := w32MonitorInfo{Size: uint32(unsafe.Sizeof(w32MonitorInfo{}))}
	ret, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return WidgetWindowState{}, false
	}
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	return defaultDesktopIconWindowStateForWorkArea(info.Work, uint32(dpi)), true
}

func defaultDesktopIconWindowStateForWorkArea(work w32Rect, dpi uint32) WidgetWindowState {
	logicalWidth := scaleToDefaultDPI(int(work.Right-work.Left), dpi)
	logicalHeight := scaleToDefaultDPI(int(work.Bottom-work.Top), dpi)
	// The icon canvas is a bounded transparent surface anchored to the
	// bottom-right corner. It follows the desktopIcon defaults with the same
	// edge/bottom gap semantics as the pager, but never fills or exceeds the
	// work area — a full-screen WebView2 surface is what drives the periodic
	// DWM/GPU peaks.
	width := min(desktopIconWidth, max(desktopIconMinWidth, logicalWidth-widgetEdgeGap*2))
	height := min(desktopIconHeight, max(desktopIconMinHeight, logicalHeight-widgetBottomGap*2))
	width = min(width, logicalWidth)
	height = min(height, logicalHeight)
	return WidgetWindowState{
		Width:  width,
		Height: height,
		X:      max(int(work.Left), int(work.Right)-scaleForDPI(width+widgetEdgeGap, dpi)),
		Y:      max(int(work.Top), int(work.Bottom)-scaleForDPI(height+widgetBottomGap, dpi)),
	}
}

// desktopIconSurfaceStateForWorkArea clamps a requested icon-surface content
// size (plus its safety envelope) to the icon canvas bounds and anchors it at
// the work area's bottom-right corner. It never returns a surface larger than
// the work area, so the transparent WebView2 canvas stays bounded.
func desktopIconSurfaceStateForWorkArea(work w32Rect, dpi uint32, width, height, envelope int) WidgetWindowState {
	width = max(0, width)
	height = max(0, height)
	envelope = max(0, envelope)
	contentWidth := width + envelope*2
	contentHeight := height + envelope*2
	logicalWidth := scaleToDefaultDPI(int(work.Right-work.Left), dpi)
	logicalHeight := scaleToDefaultDPI(int(work.Bottom-work.Top), dpi)
	targetWidth := min(desktopIconWidth, max(desktopIconMinWidth, contentWidth))
	targetHeight := min(desktopIconHeight, max(desktopIconMinHeight, contentHeight))
	targetWidth = min(targetWidth, logicalWidth)
	targetHeight = min(targetHeight, logicalHeight)
	return WidgetWindowState{
		Width:  targetWidth,
		Height: targetHeight,
		X:      max(int(work.Left), int(work.Right)-scaleForDPI(targetWidth+widgetEdgeGap, dpi)),
		Y:      max(int(work.Top), int(work.Bottom)-scaleForDPI(targetHeight+widgetBottomGap, dpi)),
	}
}

// applyDesktopIconSurface resizes the native icon window to the requested
// surface bounds. It re-reads the monitor that owns the window on every call so
// display/DPI changes are picked up instead of a stale persisted geometry.
func applyDesktopIconSurface(_ context.Context, input DesktopIconSurfaceInput) (WidgetWindowState, error) {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return WidgetWindowState{}, fmt.Errorf("applyDesktopIconSurface: window not found")
	}
	monitor, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultNearest)
	if monitor == 0 {
		return WidgetWindowState{}, fmt.Errorf("applyDesktopIconSurface: monitor not found")
	}
	info := w32MonitorInfo{Size: uint32(unsafe.Sizeof(w32MonitorInfo{}))}
	ret, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return WidgetWindowState{}, fmt.Errorf("applyDesktopIconSurface: monitor info unavailable")
	}
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	state := desktopIconSurfaceStateForWorkArea(info.Work, uint32(dpi), input.Width, input.Height, input.Envelope)
	if err := setDesktopWindowBounds(nil, state.Width, state.Height, state.X, state.Y); err != nil {
		return WidgetWindowState{}, err
	}
	return state, nil
}

// setWidgetWindowRegion clips the native window to the same octagonal shape as
// the CSS clip-path on .widget-shell. width and height come from Wails window
// coordinates; SetWindowRgn needs them scaled by the target window DPI.
// Idempotent — subsequent calls replace the previous region.
func setWidgetWindowRegion(width, height int) error {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return fmt.Errorf("setWidgetWindowRegion: window not found")
	}
	// Polygon matching the CSS clip-path octagon, mapped from 200% shell-space
	// to native window coordinates (÷2).  Points are window-relative.
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	points, err := widgetRegionPoints(width, height, uint32(dpi))
	if err != nil {
		return fmt.Errorf("setWidgetWindowRegion: %w", err)
	}

	hrgn, _, _ := procCreatePolygonRgn.Call(
		uintptr(unsafe.Pointer(&points[0])),
		8,
		1, // ALTERNATE; fill mode is equivalent for this convex polygon.
	)
	if hrgn == 0 {
		return fmt.Errorf("setWidgetWindowRegion: CreatePolygonRgn failed")
	}

	// bRedraw=TRUE → Windows takes ownership of the region and repaints.
	// The old region (if any) is freed by the system.
	ret, _, _ := procSetWindowRgn.Call(uintptr(hwnd), hrgn, 1)
	if ret == 0 {
		procDeleteObject.Call(hrgn)
		return fmt.Errorf("setWidgetWindowRegion: SetWindowRgn failed")
	}
	return redrawWidgetWindow(hwnd)
}

func setDesktopIconHitRegions(rects []DesktopIconRect) error {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return fmt.Errorf("setDesktopIconHitRegions: window not found")
	}
	if len(rects) == 0 {
		return fmt.Errorf("setDesktopIconHitRegions: at least one hit rectangle is required")
	}
	var client w32Rect
	ret, _, _ := procGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&client)))
	if ret == 0 {
		return fmt.Errorf("setDesktopIconHitRegions: GetClientRect failed")
	}
	rects = normalizeDesktopIconRects(rects, int(client.Right-client.Left), int(client.Bottom-client.Top))
	if len(rects) == 0 {
		return fmt.Errorf("setDesktopIconHitRegions: no hit rectangle intersects the client area")
	}
	combined, _, _ := procCreateRectRgn.Call(0, 0, 0, 0)
	if combined == 0 {
		return fmt.Errorf("setDesktopIconHitRegions: CreateRectRgn failed")
	}
	for _, rect := range rects {
		left, top := rect.X, rect.Y
		right, bottom := rect.X+rect.Width, rect.Y+rect.Height
		part, _, _ := procCreateRectRgn.Call(uintptr(left), uintptr(top), uintptr(right), uintptr(bottom))
		if part == 0 {
			procDeleteObject.Call(combined)
			return fmt.Errorf("setDesktopIconHitRegions: CreateRectRgn part failed")
		}
		result, _, _ := procCombineRgn.Call(combined, combined, part, 2) // RGN_OR
		procDeleteObject.Call(part)
		if result == 0 {
			procDeleteObject.Call(combined)
			return fmt.Errorf("setDesktopIconHitRegions: CombineRgn failed")
		}
	}
	// Replace the region in one operation. Restoring the full window before every
	// update makes the whole transparent surface flash during icon clicks.
	ret, _, _ = procSetWindowRgn.Call(uintptr(hwnd), combined, 1)
	if ret == 0 {
		procDeleteObject.Call(combined)
		return fmt.Errorf("setDesktopIconHitRegions: apply hit region failed")
	}
	// bRedraw=TRUE already invalidates the visible region. A second synchronous
	// RedrawWindow/DwmFlush can present the old and new transparent surfaces in
	// separate frames, which exposes region edges as orange seams.
	return nil
}

// clearWidgetWindowRegion restores the native window to a full rectangle.
// Idempotent — safe to call even when no region is active.
func clearWidgetWindowRegion() error {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return fmt.Errorf("clearWidgetWindowRegion: window not found")
	}
	// NULL region → full rectangular window.
	ret, _, _ := procSetWindowRgn.Call(uintptr(hwnd), 0, 1)
	if ret == 0 {
		return fmt.Errorf("clearWidgetWindowRegion: SetWindowRgn failed")
	}
	return redrawWidgetWindow(hwnd)
}

// widgetTaskbarOps are the raw window operations a taskbar style switch needs.
// The production wiring talks to user32; tests inject fakes so no real Explorer
// or taskbar is touched.
type widgetTaskbarOps struct {
	findHWND func() syscall.Handle
	getStyle func(syscall.Handle) (uint32, error)
	setStyle func(syscall.Handle, uint32) error
	visible  func(syscall.Handle) bool
	show     func(syscall.Handle, bool) error
	frame    func(syscall.Handle) error
}

// widgetTaskbarStyleState retains the pre-transition visibility intent if both
// the final Show and rollback Show fail. It is keyed to the current HWND and
// protected because native taskbar calls may also be exercised independently
// of App.transitionWidgetMode's widgetMu boundary.
type widgetTaskbarStyleState struct {
	mu             sync.Mutex
	hwnd           syscall.Handle
	pendingVisible bool
}

var nativeWidgetTaskbarStyleState widgetTaskbarStyleState

// widgetTaskbarTargetStyle returns the extended style with the taskbar bits
// switched for the requested visibility. hide removes WS_EX_APPWINDOW and adds
// WS_EX_TOOLWINDOW; show reverses that. Every unrelated bit is preserved.
func widgetTaskbarTargetStyle(style uint32, hide bool) uint32 {
	if hide {
		return (style &^ wsExAppWindow) | wsExToolWindow
	}
	return (style &^ wsExToolWindow) | wsExAppWindow
}

// setWidgetTaskbarStyle switches the taskbar presence of the Wails window.
// Idempotent unless a prior failed transition left a pending visible intent.
// Visible windows follow the Windows dynamic-taskbar refresh order Hide →
// SetWindowLongW → SetWindowPos(SWP_FRAMECHANGED) → Show. StartHidden windows
// are never shown early. A pending visible intent is cleared only after Show
// succeeds and IsWindowVisible confirms recovery.
func setWidgetTaskbarStyle(state *widgetTaskbarStyleState, ops widgetTaskbarOps, hide bool) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	hwnd := ops.findHWND()
	if hwnd == 0 {
		state.hwnd = 0
		state.pendingVisible = false
		return errors.New("setWidgetTaskbarStyle: window not found")
	}
	if state.hwnd != hwnd {
		state.hwnd = hwnd
		state.pendingVisible = false
	}
	style, err := ops.getStyle(hwnd)
	if err != nil {
		return fmt.Errorf("setWidgetTaskbarStyle: read window style: %w", err)
	}
	target := widgetTaskbarTargetStyle(style, hide)
	if target == style {
		if state.pendingVisible {
			if err := showWidgetTaskbarWindow(state, ops, hwnd); err != nil {
				return fmt.Errorf("setWidgetTaskbarStyle: restore pending window visibility: %w", err)
			}
		}
		return nil
	}
	wasVisible := ops.visible(hwnd)
	if wasVisible {
		state.pendingVisible = true
		if err := ops.show(hwnd, false); err != nil {
			return errors.Join(
				fmt.Errorf("setWidgetTaskbarStyle: hide window before style change: %w", err),
				showWidgetTaskbarWindow(state, ops, hwnd),
			)
		}
	}
	if err := ops.setStyle(hwnd, target); err != nil {
		return errors.Join(
			fmt.Errorf("setWidgetTaskbarStyle: apply %s style: %w", widgetTaskbarModeName(hide), err),
			restoreWidgetTaskbarState(state, ops, hwnd, style),
		)
	}
	if err := ops.frame(hwnd); err != nil {
		return errors.Join(
			fmt.Errorf("setWidgetTaskbarStyle: refresh frame cache: %w", err),
			restoreWidgetTaskbarState(state, ops, hwnd, style),
		)
	}
	if state.pendingVisible {
		if err := showWidgetTaskbarWindow(state, ops, hwnd); err != nil {
			return errors.Join(
				fmt.Errorf("setWidgetTaskbarStyle: show window after style change: %w", err),
				restoreWidgetTaskbarState(state, ops, hwnd, style),
			)
		}
	}
	return nil
}

// restoreWidgetTaskbarState best-effort restores the original extended style,
// frame cache, and any retained visible intent after a failed style switch.
func restoreWidgetTaskbarState(state *widgetTaskbarStyleState, ops widgetTaskbarOps, hwnd syscall.Handle, style uint32) error {
	var errs []error
	if err := ops.setStyle(hwnd, style); err != nil {
		errs = append(errs, fmt.Errorf("restore window style: %w", err))
	}
	if err := ops.frame(hwnd); err != nil {
		errs = append(errs, fmt.Errorf("restore frame cache: %w", err))
	}
	if state.pendingVisible {
		if err := showWidgetTaskbarWindow(state, ops, hwnd); err != nil {
			errs = append(errs, fmt.Errorf("restore window visibility: %w", err))
		}
	}
	return errors.Join(errs...)
}

func showWidgetTaskbarWindow(state *widgetTaskbarStyleState, ops widgetTaskbarOps, hwnd syscall.Handle) error {
	if err := ops.show(hwnd, true); err != nil {
		return err
	}
	if !ops.visible(hwnd) {
		return errors.New("ShowWindow returned but window remains hidden")
	}
	state.pendingVisible = false
	return nil
}

func widgetTaskbarModeName(hide bool) string {
	if hide {
		return "hide"
	}
	return "show"
}

// windowLongIndex sign-extends the Win32 int index on every Windows architecture.
func windowLongIndex(index int32) uintptr { return uintptr(index) }

// widgetTaskbarGetStyle reads GWL_EXSTYLE through the architecture-independent
// GetWindowLongW API. LONG is always 32 bits, so uint32 preserves every style
// bit without depending on pointer size. A zero result is an error only when
// the call's cleared-then-read last-error is non-zero.
func widgetTaskbarGetStyle(hwnd syscall.Handle) (uint32, error) {
	style, _, callErr := procGetWindowLongW.Call(uintptr(hwnd), windowLongIndex(gwlExStyle))
	if style == 0 && callErr != syscall.Errno(0) {
		return 0, fmt.Errorf("GetWindowLongW(GWL_EXSTYLE): %w", callErr)
	}
	return uint32(style), nil
}

// widgetTaskbarSetStyle writes the 32-bit extended style with SetWindowLongW.
// Its zero return is ambiguous and is disambiguated using the captured error.
func widgetTaskbarSetStyle(hwnd syscall.Handle, style uint32) error {
	ret, _, callErr := procSetWindowLongW.Call(uintptr(hwnd), windowLongIndex(gwlExStyle), uintptr(style))
	if ret == 0 && callErr != syscall.Errno(0) {
		return fmt.Errorf("SetWindowLongW(GWL_EXSTYLE): %w", callErr)
	}
	return nil
}

// widgetTaskbarIsVisible reports whether the window is currently visible.
func widgetTaskbarIsVisible(hwnd syscall.Handle) bool {
	ret, _, _ := procIsWindowVisible.Call(uintptr(hwnd))
	return ret != 0
}

// widgetTaskbarShowWindow hides or shows the window. ShowWindow returns the
// previous visibility, which is 0 both for a previously hidden window and for a
// failure; the cleared-then-read last error disambiguates the two.
func widgetTaskbarShowWindow(hwnd syscall.Handle, show bool) error {
	cmd := uintptr(swHide)
	if show {
		cmd = swShow
	}
	prev, _, callErr := procShowWindow.Call(uintptr(hwnd), cmd)
	if prev == 0 && callErr != syscall.Errno(0) {
		name := "show"
		if !show {
			name = "hide"
		}
		return fmt.Errorf("ShowWindow(%s): %w", name, callErr)
	}
	return nil
}

// widgetTaskbarRefreshFrame nudges the non-client frame cache so Explorer
// re-evaluates the taskbar button after the extended style changed.
func widgetTaskbarRefreshFrame(hwnd syscall.Handle) error {
	flags := uintptr(windowPosNoMove | windowPosNoSize | windowPosNoZOrder | windowPosNoActivate | windowPosFrameChanged | windowPosNoOwnerOrder)
	ret, _, callErr := procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, flags)
	if ret == 0 {
		return fmt.Errorf("SetWindowPos(SWP_FRAMECHANGED): %w", callErr)
	}
	return nil
}

// setWidgetTaskbarHidden hides or restores the taskbar button of the native
// Wails window. Idempotent and safe to retry; see setWidgetTaskbarStyle for the
// refresh order and rollback behavior.
func setWidgetTaskbarHidden(hide bool) error {
	return setWidgetTaskbarStyle(&nativeWidgetTaskbarStyleState, widgetTaskbarOps{
		findHWND: func() syscall.Handle { return findWidgetHWND() },
		getStyle: widgetTaskbarGetStyle,
		setStyle: widgetTaskbarSetStyle,
		visible:  widgetTaskbarIsVisible,
		show:     widgetTaskbarShowWindow,
		frame:    widgetTaskbarRefreshFrame,
	}, hide)
}
