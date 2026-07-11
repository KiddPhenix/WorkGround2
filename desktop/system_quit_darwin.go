//go:build darwin

package main

/*
#cgo darwin LDFLAGS: -framework Cocoa
void installWorkGround2SystemQuitHook(void);
*/
import "C"

import "sync"

var installSystemQuitHookOnce sync.Once

func installSystemQuitHook() {
	installSystemQuitHookOnce.Do(func() {
		C.installWorkGround2SystemQuitHook()
	})
}

//export WorkGround2MarkSystemQuit
func WorkGround2MarkSystemQuit() {
	markSystemQuitRequested()
}
