//go:build !windows

package home

import (
	"fmt"
	"os"
	"syscall"
)

// sourceFingerprint uses a filesystem object identity, not the lexical path. A rename on
// the same filesystem retains dev/inode and can therefore be recognised safely.
func sourceFingerprint(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported source filesystem identity")
	}
	return fmt.Sprintf("fs:%x:%x", uint64(stat.Dev), uint64(stat.Ino)), nil
}
