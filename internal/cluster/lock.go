package cluster

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	clusterLockTimeout = 5 * time.Second
	lockRetryInterval  = 10 * time.Millisecond

	lockCacheNamespace = "Carbon"
	lockCacheDirectory = "carbon-cluster-locks"
)

var (
	errLockWouldBlock = errors.New("cluster: registry lock held")
	userCacheDir      = os.UserCacheDir
)

// withLock serializes manifest mutations across both goroutines and independent Carbon
// processes. The OS-locked file is kept in the per-user cache, not alongside the
// manifest. It must remain in place after unlock: removing a file lock can split
// waiters between its old inode and a newly-created one, allowing concurrent writes.
// Keeping a stable cache file means a cluster root never accumulates a lock artifact.
func withLock(root string, fn func() error) error {
	resolvedRoot, err := ResolveRoot(root)
	if err != nil {
		return err
	}
	path, err := clusterLockPath(resolvedRoot)
	if err != nil {
		return err
	}
	lockDir := filepath.Dir(path)
	if _, _, err := safeRegularFile(lockDir, path, true); err != nil {
		return fmt.Errorf("cluster: inspect registry lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("cluster: open registry lock: %w", err)
	}
	defer f.Close()
	if _, _, err := safeRegularFile(lockDir, path, false); err != nil {
		return fmt.Errorf("cluster: inspect registry lock: %w", err)
	}
	if err := acquireLock(f); err != nil {
		return err
	}
	defer unlockLock(f) //nolint:errcheck // closing also releases the OS lock
	return fn()
}

// clusterLockPath returns a stable, per-user cache lock path keyed by the canonical
// cluster root. The cache hierarchy is checked before every use so a symlink/reparse
// point cannot redirect the lock to an unexpected location.
func clusterLockPath(root string) (string, error) {
	cacheRoot, err := userCacheDir()
	if err != nil {
		return "", fmt.Errorf("cluster: locate registry lock cache: %w", err)
	}
	cacheRoot, err = filepath.Abs(cacheRoot)
	if err != nil {
		return "", fmt.Errorf("cluster: resolve registry lock cache: %w", err)
	}
	cacheRoot = filepath.Clean(cacheRoot)
	if _, _, err := safeLockDirectory(cacheRoot, cacheRoot, false); err != nil {
		return "", fmt.Errorf("cluster: inspect registry lock cache: %w", err)
	}

	namespace := filepath.Join(cacheRoot, lockCacheNamespace)
	if err := ensureSafeLockDirectory(cacheRoot, namespace); err != nil {
		return "", err
	}
	lockDir := filepath.Join(namespace, lockCacheDirectory)
	if err := ensureSafeLockDirectory(namespace, lockDir); err != nil {
		return "", err
	}

	sum := sha256.Sum256([]byte(pathKey(root)))
	return filepath.Join(lockDir, fmt.Sprintf("%s-%x", manifestLockName, sum[:])), nil
}

func ensureSafeLockDirectory(root, path string) error {
	err := os.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("cluster: create registry lock cache: %w", err)
	}
	if _, _, err := safeLockDirectory(root, path, false); err != nil {
		return fmt.Errorf("cluster: inspect registry lock cache: %w", err)
	}
	return nil
}

func safeLockDirectory(root, path string, allowMissing bool) (os.FileInfo, bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: registry lock cache is missing", ErrUnsafeManifest)
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: inspect registry lock cache %s: %v", ErrUnsafeManifest, path, err)
	}
	if isReparsePoint(path, fi) {
		return nil, false, fmt.Errorf("%w: refusing registry lock cache symlink/reparse point %s", ErrUnsafeManifest, path)
	}
	if !fi.IsDir() {
		return nil, false, fmt.Errorf("%w: expected registry lock cache directory %s", ErrUnsafeManifest, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !samePath(resolved, path) || !pathWithin(root, resolved) {
		return nil, false, fmt.Errorf("%w: registry lock cache escapes its expected location", ErrUnsafeManifest)
	}
	return fi, true, nil
}

func acquireLock(f *os.File) error {
	deadline := time.Now().Add(clusterLockTimeout)
	for {
		err := lockExclusiveNB(f)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errLockWouldBlock) {
			return fmt.Errorf("cluster: acquire registry lock: %w", err)
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(lockRetryInterval)
	}
}
