//go:build windows

package check

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func isReparsePoint(path string, info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true // fail closed for an uninspectable path
	}
	attrs, err := windows.GetFileAttributes(p)
	return err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
