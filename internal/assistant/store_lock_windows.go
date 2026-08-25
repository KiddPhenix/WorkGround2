//go:build windows

package assistant

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func lockStoreFile(path string, timeout time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(f.Fd())
	deadline := time.Now().Add(timeout)
	var overlapped windows.Overlapped
	for {
		flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
		if err := windows.LockFileEx(handle, flags, 0, 1, 0, &overlapped); err == nil {
			return func() {
				_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, ErrStoreBusy
		}
		time.Sleep(20 * time.Millisecond)
	}
}
