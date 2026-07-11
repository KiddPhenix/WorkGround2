//go:build !windows

package agent

import (
	"os"
	"time"

	"workground2/internal/store"

	"golang.org/x/sys/unix"
)

func lockSessionFile(path string) (func(), error) {
	f, err := os.OpenFile(store.SessionLockFile(path), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(sessionLockTimeout)
	for {
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, errSessionFileLockHeld
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// sessionLockFile is a non-blocking exclusive lock on a lock file itself,
// used by cleanup paths that may need to delete the file they locked.
type sessionLockFile struct {
	f *os.File
}

// Unlock releases the exclusive lock without deleting the lock file.
func (l *sessionLockFile) Unlock() {
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
}

// tryTakeSessionLockFile opens lockPath and takes its exclusive flock without
// blocking. A live holder surfaces as errSessionFileLockHeld.
func tryTakeSessionLockFile(lockPath string) (*sessionLockFile, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errSessionFileLockHeld
	}
	return &sessionLockFile{f: f}, nil
}

// RemoveAndUnlock deletes the lock file atomically with the release, then
// unlocks and closes. Returns any path-based deletion error.
func (l *sessionLockFile) RemoveAndUnlock() error {
	if err := os.Remove(l.f.Name()); err != nil {
		sessionLockDispositionFallbacks.Add(1)
	}
	l.Unlock()
	return nil
}

// tryLockSessionLeaseFile opens the lease lock file and takes a blocking
// exclusive lock. Returns an unlock function on success, or
// ErrSessionLeaseHeld when the lock is held.
func tryLockSessionLeaseFile(path string) (func(), error) {
	lockPath := store.SessionLeaseLock(path)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, ErrSessionLeaseHeld
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, ErrSessionLeaseHeld
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
