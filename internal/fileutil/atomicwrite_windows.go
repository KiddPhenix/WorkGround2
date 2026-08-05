package fileutil

import (
	"golang.org/x/sys/windows"
)

// replaceFile atomically replaces dest with src on Windows using
// MoveFileExW with MOVEFILE_REPLACE_EXISTING. Unlike os.Rename, this
// overwrites an existing destination atomically — a reader always sees
// either the complete old file or the complete new file, never a
// truncated intermediate state.
func replaceFile(src, dest string) error {
	srcPtr, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	destPtr, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(srcPtr, destPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
