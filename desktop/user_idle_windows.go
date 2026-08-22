//go:build windows

package main

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32GetLastInputInfo = syscall.NewLazyDLL("user32.dll").NewProc("GetLastInputInfo")
	kernel32GetTickCount   = syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount")
)

type lastInputInfo struct {
	size uint32
	tick uint32
}

func platformSystemIdleDuration() (time.Duration, error) {
	info := lastInputInfo{size: uint32(unsafe.Sizeof(lastInputInfo{}))}
	ok, _, callErr := user32GetLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 0, fmt.Errorf("GetLastInputInfo: %w", callErr)
	}
	now, _, _ := kernel32GetTickCount.Call()
	// uint32 subtraction intentionally handles the 49.7-day tick wrap.
	elapsed := uint32(now) - info.tick
	return time.Duration(elapsed) * time.Millisecond, nil
}
