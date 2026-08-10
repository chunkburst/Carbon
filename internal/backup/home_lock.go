package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	// ErrHomeLockTimeout means another Carbon process is actively creating or
	// pruning local snapshots for the same home.
	ErrHomeLockTimeout    = errors.New("backup home lock timeout")
	errBackupHomeLockHeld = errors.New("backup home lock held")
)

const (
	backupHomeLockFilename = "scheduler.lock"
	backupHomeLockRetry    = 20 * time.Millisecond
	backupHomeLockTimeout  = 5 * time.Second
)

// LocalHomeLock is a short-lived per-Carbon-home advisory lock. It is held for
// a snapshot/prune transaction, never for a remote operation. The lock path is
// inside the local backup directory so it is excluded from snapshots.
type LocalHomeLock struct {
	file    *os.File
	localMu *sync.Mutex
	once    sync.Once
}

var localHomeLocks sync.Map // canonical Carbon root -> *sync.Mutex

// AcquireLocalHomeLock serializes local backup writers across Carbon processes
// and also protects multiple schedulers in this process. It creates only the
// trusted local backup directory and never uses a network client.
func AcquireLocalHomeLock(ctx context.Context, carbonRoot string) (*LocalHomeLock, error) {
	root, err := filepath.Abs(carbonRoot)
	if err != nil {
		return nil, fmt.Errorf("backup home lock resolve root: %w", err)
	}
	if err := ensureTrustedLocalDirectoryChain(root, false); err != nil {
		return nil, fmt.Errorf("backup home lock root is unsafe: %w", err)
	}
	backupRoot := filepath.Join(root, "backups")
	if err := ensureTrustedLocalDirectoryChain(backupRoot, true); err != nil {
		return nil, fmt.Errorf("backup home lock parent is unsafe: %w", err)
	}
	mutexValue, _ := localHomeLocks.LoadOrStore(root, &sync.Mutex{})
	localMu := mutexValue.(*sync.Mutex)
	if err := lockBackupLocalMutex(ctx, localMu); err != nil {
		return nil, err
	}
	unlockLocal := true
	defer func() {
		if unlockLocal {
			localMu.Unlock()
		}
	}()

	lockPath := filepath.Join(backupRoot, backupHomeLockFilename)
	if info, statErr := os.Lstat(lockPath); statErr == nil {
		if isBackupReparsePoint(lockPath, info) || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: backup home lock is not a regular file", ErrUnsafePath)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("backup home lock inspect: %w", statErr)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("backup home lock open: %w", err)
	}
	if info, statErr := file.Stat(); statErr != nil {
		_ = file.Close()
		return nil, fmt.Errorf("backup home lock inspect: %w", statErr)
	} else if isBackupReparsePoint(lockPath, info) || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: backup home lock is not a regular file", ErrUnsafePath)
	}
	if err := lockBackupHomeFile(ctx, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	unlockLocal = false
	return &LocalHomeLock{file: file, localMu: localMu}, nil
}

func lockBackupLocalMutex(ctx context.Context, mutex *sync.Mutex) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, backupHomeLockTimeout)
		defer cancel()
	}
	for {
		if mutex.TryLock() {
			return nil
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

// Release unlocks the OS advisory lock and closes its descriptor. It is safe
// to call more than once.
func (lock *LocalHomeLock) Release() error {
	if lock == nil {
		return nil
	}
	var result error
	lock.once.Do(func() {
		if lock.file != nil {
			unlockErr := unlockBackupHomeFile(lock.file)
			closeErr := lock.file.Close()
			if unlockErr != nil {
				result = unlockErr
			} else {
				result = closeErr
			}
		}
		if lock.localMu != nil {
			lock.localMu.Unlock()
		}
	})
	return result
}
