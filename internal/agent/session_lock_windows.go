//go:build windows

package agent

import (
	"os"
	"time"
	"unsafe"

	"workground2/internal/store"

	"golang.org/x/sys/windows"
)

func lockSessionFile(path string) (func(), error) {
	f, err := os.OpenFile(store.SessionLockFile(path), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(f.Fd())
	deadline := time.Now().Add(sessionLockTimeout)
	for {
		var overlapped windows.Overlapped
		if err := windows.LockFileEx(handle,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, &overlapped); err == nil {
			return func() {
				_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
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
	handle     windows.Handle
	path       string
	overlapped windows.Overlapped
}

// Unlock releases the exclusive lock without deleting the lock file.
func (l *sessionLockFile) Unlock() {
	_ = windows.UnlockFileEx(l.handle, 0, 1, 0, &l.overlapped)
	_ = windows.CloseHandle(l.handle)
}

// tryTakeSessionLockFile opens lockPath and takes its exclusive LockFileEx
// region without blocking. A live holder surfaces as errSessionFileLockHeld.
//
// The handle asks for DELETE access up front: FileDispositionInfo requires it,
// and requesting it at open time keeps RemoveAndUnlock's deletion on the very
// handle that owns the lock. A sharing violation here means some process has
// the file open through Go's default share mode (which excludes DELETE) — for
// a lock file that is the same answer as losing the LockFileEx race.
func tryTakeSessionLockFile(lockPath string) (*sessionLockFile, error) {
	pathp, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pathp,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, errSessionFileLockHeld
	}
	l := &sessionLockFile{handle: handle, path: lockPath}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(handle, flags, 0, 1, 0, &l.overlapped); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, errSessionFileLockHeld
	}
	return l, nil
}

// RemoveAndUnlock deletes the lock file atomically with the release. Windows
// refuses a path-based delete of a file this process still holds open, so the
// removal is expressed on the held handle instead: mark the delete
// disposition, then unlock and close. The name dies with the handle, leaving
// no window where another process could adopt a lock file that is already
// doomed.
func (l *sessionLockFile) RemoveAndUnlock() error {
	// FILE_DISPOSITION_INFO with its BOOLEAN widened to a full word.
	info := struct{ DeleteFile uint32 }{DeleteFile: 1}
	dispErr := windows.SetFileInformationByHandle(l.handle, windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	l.Unlock()
	if dispErr != nil {
		// Delete disposition unsupported (exotic filesystem): fall back to a
		// path-based remove after the release. A short adoption window beats
		// leaving the sidecar behind forever.
		sessionLockDispositionFallbacks.Add(1)
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// tryLockSessionLeaseFile opens the lease lock file and takes a non-blocking
// exclusive lock. Returns an unlock function on success, or ErrSessionLeaseHeld
// when the lock is held.
func tryLockSessionLeaseFile(path string) (func(), error) {
	lockPath := store.SessionLeaseLock(path)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, ErrSessionLeaseHeld
	}
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(handle, flags, 0, 1, 0, &overlapped); err != nil {
		_ = f.Close()
		return nil, ErrSessionLeaseHeld
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
		_ = f.Close()
	}, nil
}
