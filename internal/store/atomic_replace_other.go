//go:build !windows

package store

import "os"

// atomicReplace publishes a same-directory temporary file. On POSIX os.Rename replaces
// the directory entry atomically; atomicWrite syncs the parent directory afterwards.
func atomicReplace(from, to string) error {
	return os.Rename(from, to)
}

// atomicTempRequiresCloseBeforeReplace is false on POSIX because rename may publish an
// open file. Keeping the descriptor open until after the rename prevents an attacker
// from deleting the temporary name and immediately reusing its inode before identity
// validation.
func atomicTempRequiresCloseBeforeReplace() bool { return false }
