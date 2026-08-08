//go:build windows

package main

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmGetIcon          = 0x007f
	wmSetIcon          = 0x0080
	iconSmall          = 0
	iconBig            = 1
	gclpHIcon          = -14
	gclpHIconSmall     = -34
	diNormal           = 0x0003
	srcCopy            = 0x00cc0020
	dtCenter           = 0x00000001
	dtVCenter          = 0x00000004
	dtSingleLine       = 0x00000020
	transparentBkMode  = 1
	fontWeightSemibold = 600
	defaultCharset     = 1
	cleartypeQuality   = 5
)

var (
	taskbarUser32               = windows.NewLazySystemDLL("user32.dll")
	taskbarGDI32                = windows.NewLazySystemDLL("gdi32.dll")
	taskbarProcEnumWindows      = taskbarUser32.NewProc("EnumWindows")
	taskbarProcWindowProcessID  = taskbarUser32.NewProc("GetWindowThreadProcessId")
	taskbarProcWindowTextW      = taskbarUser32.NewProc("GetWindowTextW")
	taskbarProcSendMessageW     = taskbarUser32.NewProc("SendMessageW")
	taskbarProcGetClassLongPtrW = taskbarUser32.NewProc("GetClassLongPtrW")
	taskbarProcGetIconInfo      = taskbarUser32.NewProc("GetIconInfo")
	taskbarProcDrawIconEx       = taskbarUser32.NewProc("DrawIconEx")
	taskbarProcGetDC            = taskbarUser32.NewProc("GetDC")
	taskbarProcReleaseDC        = taskbarUser32.NewProc("ReleaseDC")
	taskbarProcFillRect         = taskbarUser32.NewProc("FillRect")
	taskbarProcDrawTextW        = taskbarUser32.NewProc("DrawTextW")
	taskbarProcCreateIcon       = taskbarUser32.NewProc("CreateIconIndirect")
	taskbarProcDestroyIcon      = taskbarUser32.NewProc("DestroyIcon")
	taskbarProcCreateDC         = taskbarGDI32.NewProc("CreateCompatibleDC")
	taskbarProcDeleteDC         = taskbarGDI32.NewProc("DeleteDC")
	taskbarProcCreateBitmap     = taskbarGDI32.NewProc("CreateBitmap")
	taskbarProcCreateDIBSection = taskbarGDI32.NewProc("CreateDIBSection")
	taskbarProcSelectObject     = taskbarGDI32.NewProc("SelectObject")
	taskbarProcDeleteObject     = taskbarGDI32.NewProc("DeleteObject")
	taskbarProcCreateBrush      = taskbarGDI32.NewProc("CreateSolidBrush")
	taskbarProcCreatePen        = taskbarGDI32.NewProc("CreatePen")
	taskbarProcEllipse          = taskbarGDI32.NewProc("Ellipse")
	taskbarProcSetBkMode        = taskbarGDI32.NewProc("SetBkMode")
	taskbarProcSetTextColor     = taskbarGDI32.NewProc("SetTextColor")
	taskbarProcCreateFontW      = taskbarGDI32.NewProc("CreateFontW")
	taskbarProcGetObjectW       = taskbarGDI32.NewProc("GetObjectW")
	taskbarProcStretchBlt       = taskbarGDI32.NewProc("StretchBlt")
	taskbarProcGDIFlush         = taskbarGDI32.NewProc("GdiFlush")
)

type winRect struct {
	left, top, right, bottom int32
}

type iconInfo struct {
	isIcon             int32
	xHotspot, yHotspot uint32
	mask, color        uintptr
}

type winBitmap struct {
	typeCode   int32
	width      int32
	height     int32
	widthBytes int32
	planes     uint16
	bitsPixel  uint16
	bits       uintptr
}

type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	colorsUsed    uint32
	colorsNeeded  uint32
}

type bitmapInfo struct {
	header bitmapInfoHeader
	color  uint32
}

var windowBadge = struct {
	sync.Mutex
	hwnd                 uintptr
	baseBig, baseSmall   uintptr
	badgeBig, badgeSmall uintptr
}{}

func setTaskbarBadge(count int) error {
	hwnd, err := findTaskbarWindow()
	if hwnd == 0 {
		return fmt.Errorf("find WorkGround2 window: %w", err)
	}
	return setWindowBadge(hwnd, taskbarBadgeLabel(count))
}

func setWindowBadge(hwnd uintptr, label string) error {
	windowBadge.Lock()
	defer windowBadge.Unlock()

	if windowBadge.hwnd != hwnd {
		destroyWindowBadges()
		windowBadge.hwnd = hwnd
		windowBadge.baseBig = windowIcon(hwnd, iconBig, gclpHIcon)
		windowBadge.baseSmall = windowIcon(hwnd, iconSmall, gclpHIconSmall)
		if windowBadge.baseBig == 0 {
			windowBadge.baseBig = windowBadge.baseSmall
		}
		if windowBadge.baseSmall == 0 {
			windowBadge.baseSmall = windowBadge.baseBig
		}
	}

	if label == "" {
		setWindowIcons(hwnd, windowBadge.baseBig, windowBadge.baseSmall)
		destroyWindowBadges()
		return nil
	}

	big, err := createBadgedIcon(windowBadge.baseBig, label, 32)
	if err != nil {
		return err
	}
	small, err := createBadgedIcon(windowBadge.baseSmall, label, 16)
	if err != nil {
		taskbarProcDestroyIcon.Call(big)
		return err
	}
	setWindowIcons(hwnd, big, small)
	destroyWindowBadges()
	windowBadge.badgeBig, windowBadge.badgeSmall = big, small
	return nil
}

func windowIcon(hwnd, kind uintptr, classIndex int32) uintptr {
	icon, _, _ := taskbarProcSendMessageW.Call(hwnd, wmGetIcon, kind, 0)
	if icon != 0 {
		return icon
	}
	icon, _, _ = taskbarProcGetClassLongPtrW.Call(hwnd, uintptr(uint32(classIndex)))
	return icon
}

func setWindowIcons(hwnd, big, small uintptr) {
	taskbarProcSendMessageW.Call(hwnd, wmSetIcon, iconBig, big)
	taskbarProcSendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
}

func destroyWindowBadges() {
	if windowBadge.badgeBig != 0 {
		taskbarProcDestroyIcon.Call(windowBadge.badgeBig)
		windowBadge.badgeBig = 0
	}
	if windowBadge.badgeSmall != 0 {
		taskbarProcDestroyIcon.Call(windowBadge.badgeSmall)
		windowBadge.badgeSmall = 0
	}
}

func findTaskbarWindow() (uintptr, error) {
	var found uintptr
	processID := uint32(os.Getpid())
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var owner uint32
		taskbarProcWindowProcessID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner != processID {
			return 1
		}
		buffer := make([]uint16, 64)
		length, _, _ := taskbarProcWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
		if length == 0 || windows.UTF16ToString(buffer[:length]) != "WorkGround2" {
			return 1
		}
		found = hwnd
		return 0
	})
	result, _, callErr := taskbarProcEnumWindows.Call(callback, 0)
	if found != 0 {
		return found, nil
	}
	if result == 0 && callErr != windows.ERROR_SUCCESS {
		return 0, callErr
	}
	return 0, windows.ERROR_NOT_FOUND
}

func createBadgedIcon(base uintptr, label string, size int) (uintptr, error) {
	screenDC, _, callErr := taskbarProcGetDC.Call(0)
	if screenDC == 0 {
		return 0, fmt.Errorf("get screen device context: %w", callErr)
	}
	defer taskbarProcReleaseDC.Call(0, screenDC)

	colorDC, _, callErr := taskbarProcCreateDC.Call(screenDC)
	if colorDC == 0 {
		return 0, fmt.Errorf("create badge color context: %w", callErr)
	}
	defer taskbarProcDeleteDC.Call(colorDC)
	maskDC, _, callErr := taskbarProcCreateDC.Call(screenDC)
	if maskDC == 0 {
		return 0, fmt.Errorf("create badge mask context: %w", callErr)
	}
	defer taskbarProcDeleteDC.Call(maskDC)

	bitmapHeader := bitmapInfo{
		header: bitmapInfoHeader{
			size:     uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			width:    int32(size),
			height:   -int32(size),
			planes:   1,
			bitCount: 32,
		},
	}
	var colorBits unsafe.Pointer
	colorBitmap, _, callErr := taskbarProcCreateDIBSection.Call(screenDC, uintptr(unsafe.Pointer(&bitmapHeader)), 0, uintptr(unsafe.Pointer(&colorBits)), 0, 0)
	if colorBitmap == 0 {
		return 0, fmt.Errorf("create badge color DIB: %w", callErr)
	}
	defer taskbarProcDeleteObject.Call(colorBitmap)
	maskBitmap, _, callErr := taskbarProcCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, 0)
	if maskBitmap == 0 {
		return 0, fmt.Errorf("create badge mask bitmap: %w", callErr)
	}
	defer taskbarProcDeleteObject.Call(maskBitmap)

	oldColor, _, _ := taskbarProcSelectObject.Call(colorDC, colorBitmap)
	defer taskbarProcSelectObject.Call(colorDC, oldColor)
	oldMask, _, _ := taskbarProcSelectObject.Call(maskDC, maskBitmap)
	defer taskbarProcSelectObject.Call(maskDC, oldMask)

	full := winRect{right: int32(size), bottom: int32(size)}
	black := createBrush(rgb(0, 0, 0))
	white := createBrush(rgb(255, 255, 255))
	purple := createBrush(rgb(108, 86, 235))
	defer taskbarProcDeleteObject.Call(black)
	defer taskbarProcDeleteObject.Call(white)
	defer taskbarProcDeleteObject.Call(purple)
	taskbarProcFillRect.Call(colorDC, uintptr(unsafe.Pointer(&full)), black)
	taskbarProcFillRect.Call(maskDC, uintptr(unsafe.Pointer(&full)), white)

	if base != 0 {
		taskbarProcDrawIconEx.Call(colorDC, 0, 0, base, uintptr(size), uintptr(size), 0, 0, diNormal)
		copyIconMask(maskDC, base, size)
	}

	diameter := size * 5 / 8
	fontHeight := -size * 11 / 32
	if len(label) > 2 {
		diameter = size * 3 / 4
		fontHeight = -size / 4
	}
	badgeRect := winRect{right: int32(diameter), bottom: int32(diameter)}
	purplePen, _, _ := taskbarProcCreatePen.Call(0, 1, rgb(108, 86, 235))
	blackPen, _, _ := taskbarProcCreatePen.Call(0, 1, rgb(0, 0, 0))
	defer taskbarProcDeleteObject.Call(purplePen)
	defer taskbarProcDeleteObject.Call(blackPen)
	drawEllipse(colorDC, purple, purplePen, diameter)
	drawEllipse(maskDC, black, blackPen, diameter)

	fontName, _ := windows.UTF16PtrFromString("Segoe UI")
	font, _, callErr := taskbarProcCreateFontW.Call(uintptr(int64(fontHeight)), 0, 0, 0, fontWeightSemibold, 0, 0, 0, defaultCharset, 0, 0, cleartypeQuality, 0, uintptr(unsafe.Pointer(fontName)))
	if font == 0 {
		return 0, fmt.Errorf("create badge font: %w", callErr)
	}
	defer taskbarProcDeleteObject.Call(font)
	oldFont, _, _ := taskbarProcSelectObject.Call(colorDC, font)
	defer taskbarProcSelectObject.Call(colorDC, oldFont)
	taskbarProcSetBkMode.Call(colorDC, transparentBkMode)
	taskbarProcSetTextColor.Call(colorDC, rgb(255, 255, 255))
	text, _ := windows.UTF16PtrFromString(label)
	taskbarProcDrawTextW.Call(colorDC, uintptr(unsafe.Pointer(text)), uintptr(len([]rune(label))), uintptr(unsafe.Pointer(&badgeRect)), dtCenter|dtVCenter|dtSingleLine)
	setBadgeAlpha(colorBits, size, diameter)

	info := iconInfo{isIcon: 1, mask: maskBitmap, color: colorBitmap}
	icon, _, callErr := taskbarProcCreateIcon.Call(uintptr(unsafe.Pointer(&info)))
	if icon == 0 {
		return 0, fmt.Errorf("create badged window icon: %w", callErr)
	}
	return icon, nil
}

func setBadgeAlpha(bits unsafe.Pointer, size, diameter int) {
	if bits == nil || size <= 0 || diameter <= 0 {
		return
	}
	// GDI text and shape drawing leaves alpha at zero on a 32-bit DIB. Flush
	// the batch, then make the circular badge opaque while retaining its RGB.
	taskbarProcGDIFlush.Call()
	pixels := unsafe.Slice((*byte)(bits), size*size*4)
	radius := float64(diameter) / 2
	center := radius - 0.5
	for y := 0; y < diameter; y++ {
		for x := 0; x < diameter; x++ {
			dx, dy := float64(x)-center, float64(y)-center
			if dx*dx+dy*dy <= radius*radius {
				pixels[(y*size+x)*4+3] = 0xff
			}
		}
	}
}

func copyIconMask(targetDC, icon uintptr, size int) {
	var info iconInfo
	ok, _, _ := taskbarProcGetIconInfo.Call(icon, uintptr(unsafe.Pointer(&info)))
	if ok == 0 || info.mask == 0 {
		return
	}
	defer taskbarProcDeleteObject.Call(info.mask)
	if info.color != 0 {
		defer taskbarProcDeleteObject.Call(info.color)
	}
	var bitmap winBitmap
	if result, _, _ := taskbarProcGetObjectW.Call(info.mask, unsafe.Sizeof(bitmap), uintptr(unsafe.Pointer(&bitmap))); result == 0 {
		return
	}
	height := bitmap.height
	if info.color == 0 && height > 1 {
		height /= 2
	}
	sourceDC, _, _ := taskbarProcCreateDC.Call(0)
	if sourceDC == 0 {
		return
	}
	defer taskbarProcDeleteDC.Call(sourceDC)
	old, _, _ := taskbarProcSelectObject.Call(sourceDC, info.mask)
	defer taskbarProcSelectObject.Call(sourceDC, old)
	taskbarProcStretchBlt.Call(targetDC, 0, 0, uintptr(size), uintptr(size), sourceDC, 0, 0, uintptr(bitmap.width), uintptr(height), srcCopy)
}

func drawEllipse(dc, brush, pen uintptr, diameter int) {
	oldBrush, _, _ := taskbarProcSelectObject.Call(dc, brush)
	oldPen, _, _ := taskbarProcSelectObject.Call(dc, pen)
	taskbarProcEllipse.Call(dc, 0, 0, uintptr(diameter), uintptr(diameter))
	taskbarProcSelectObject.Call(dc, oldPen)
	taskbarProcSelectObject.Call(dc, oldBrush)
}

func createBrush(color uintptr) uintptr {
	brush, _, _ := taskbarProcCreateBrush.Call(color)
	return brush
}

func rgb(red, green, blue byte) uintptr {
	return uintptr(red) | uintptr(green)<<8 | uintptr(blue)<<16
}
