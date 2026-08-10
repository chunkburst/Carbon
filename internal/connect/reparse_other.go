//go:build !windows

package connect

import (
	"io/fs"
	"os"
)

func isConnectReparsePoint(_ string, info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
