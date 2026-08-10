//go:build windows

package home

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func isReparsePoint(filename string, info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	p, err := windows.UTF16PtrFromString(filename)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(p)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
