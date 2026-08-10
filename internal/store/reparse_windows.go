//go:build windows

package store

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// isStoreReparsePoint catches Windows junctions and other reparse points in addition to
// the symlink bit exposed by os.FileMode. Final managed files must never be replaced
// through one of those indirections.
func isStoreReparsePoint(path string, info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	path, err := atomicWindowsPath(path)
	if err != nil {
		return true
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(ptr)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
