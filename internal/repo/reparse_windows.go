//go:build windows

package repo

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// isRepoReparsePoint rejects junctions as well as ordinary symlinks. A Carbon data
// directory is trusted local state, so following a reparse point while importing it
// would let an unrelated location become part of the store.
func isRepoReparsePoint(path string, info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(ptr)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
