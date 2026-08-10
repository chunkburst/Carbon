//go:build !windows

package config

import (
	"io/fs"
	"os"
)

func isConfigReparsePoint(_ string, info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
