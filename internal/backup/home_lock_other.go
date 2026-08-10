//go:build !windows

package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func lockBackupHomeFile(ctx context.Context, file *os.File) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, backupHomeLockTimeout)
		defer cancel()
	}
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("backup home lock acquire: %w", err)
		}
		timer := time.NewTimer(backupHomeLockRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %w", ErrHomeLockTimeout, ctx.Err())
		case <-timer.C:
		}
	}
}

func unlockBackupHomeFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
