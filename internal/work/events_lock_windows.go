//go:build windows

package work

import (
	"os"

	"golang.org/x/sys/windows"
)

type workLeaseLock struct {
	f          *os.File
	handle     windows.Handle
	overlapped windows.Overlapped
}

func tryLockWorkLease(path string) (*workLeaseLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	l := &workLeaseLock{f: f, handle: windows.Handle(f.Fd())}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(l.handle, flags, 0, 1, 0, &l.overlapped); err != nil {
		_ = f.Close()
		return nil, ErrWorkLeaseHeld
	}
	return l, nil
}

func (l *workLeaseLock) WriteMetadata(data []byte) error {
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	if _, err := l.f.Seek(0, 0); err != nil {
		return err
	}
	if _, err := l.f.Write(data); err != nil {
		return err
	}
	return l.f.Sync()
}

func (l *workLeaseLock) ClearMetadata() error { return l.WriteMetadata(nil) }

func (l *workLeaseLock) Unlock() {
	_ = windows.UnlockFileEx(l.handle, 0, 1, 0, &l.overlapped)
	_ = l.f.Close()
}
