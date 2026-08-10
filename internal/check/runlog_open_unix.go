//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package check

import (
	"os"

	"golang.org/x/sys/unix"
)

func openRunLogNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		unix.Close(fd)
		return nil, os.ErrInvalid
	}
	return f, nil
}
