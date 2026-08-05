//go:build !windows

package fileutil

import "os"

// replaceFile atomically replaces dest with src.
// On Unix, os.Rename is atomic on most filesystems.
func replaceFile(src, dest string) error {
	return os.Rename(src, dest)
}
