//go:build !windows

package home

import (
	"io/fs"
	"os"
)

func isReparsePoint(_ string, info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
