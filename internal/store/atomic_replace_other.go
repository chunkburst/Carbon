//go:build !windows

package store

import "os"

// atomicReplace publishes a same-directory temporary file. On POSIX os.Rename replaces
// the directory entry atomically; atomicWrite syncs the parent directory afterwards.
func atomicReplace(from, to string) error {
	return os.Rename(from, to)
}
