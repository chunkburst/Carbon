//go:build !windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockExclusiveNB takes a non-blocking exclusive advisory lock on f, returning errWouldBlock
// if another process holds it.
func lockExclusiveNB(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errWouldBlock
	}
	return err
}

// unlock releases the advisory lock (closing the fd also releases it).
func unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
