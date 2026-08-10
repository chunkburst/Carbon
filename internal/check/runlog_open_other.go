//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package check

import "os"

// The supported release targets all have a no-follow implementation. Keep a portable
// fallback for unusual Go targets; callers have already Lstat'ed and canonicalized the
// file, so this still fails closed for an existing symlink.
func openRunLogNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
