//go:build !windows

package store

import (
	"io/fs"
	"os"
)

func isStoreReparsePoint(_ string, info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
