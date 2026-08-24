//go:build !windows

package config

import "os"

func atomicReplace(from, to string) error {
	return os.Rename(from, to)
}

// atomicTempRequiresCloseBeforeReplace is false on POSIX because rename may publish an
// open file. Keeping the descriptor open until after the rename prevents an attacker
// from deleting the temporary name and immediately reusing its inode before identity
// validation.
func atomicTempRequiresCloseBeforeReplace() bool { return false }
