//go:build !windows

package check

import (
	"io/fs"
	"os"
)

func isReparsePoint(_ string, info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
