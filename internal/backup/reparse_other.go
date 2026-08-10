//go:build !windows

package backup

import (
	"io/fs"
	"os"
)

func isBackupReparsePoint(_ string, info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
