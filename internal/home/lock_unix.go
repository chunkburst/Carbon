//go:build !windows

package home

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockExclusiveNB(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errLockWouldBlock
	}
	return err
}

func unlockLock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
