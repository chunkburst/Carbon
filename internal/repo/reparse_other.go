//go:build !windows

package repo

import (
	"io/fs"
	"os"
)

func isRepoReparsePoint(_ string, info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
