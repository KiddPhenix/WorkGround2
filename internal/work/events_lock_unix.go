//go:build !windows

package work

import (
	"os"

	"golang.org/x/sys/unix"
)

type workLeaseLock struct{ f *os.File }

func tryLockWorkLease(path string) (*workLeaseLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrWorkLeaseHeld
	}
	return &workLeaseLock{f: f}, nil
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
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
}
