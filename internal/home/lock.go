package home

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
	homeLockTimeout = 5 * time.Second
	lockRetry       = 10 * time.Millisecond

	lockCacheNamespace = "Carbon"
	lockCacheDirectory = "home-locks"
	lockFilename       = "home.lock"
)

var (
	errLockWouldBlock = errors.New("carbon: home lock held")
	userCacheDir      = os.UserCacheDir
)

// withLock serializes mutations in one home across goroutines and independent Carbon
// processes. The stable lock file lives in the user cache, never under the selected home:
// deleting a root lock can split waiting processes across inodes, and leaving it in the
// home would make a user-visible artifact.
func withLock(root string, fn func() error) error {
	root, err := resolveRoot(root)
	if err != nil {
		return err
	}
	filename, err := homeLockPath(root)
	if err != nil {
		return err
	}
	lockRoot := filepath.Dir(filename)
	if _, _, err := safeRegularFile(lockRoot, filename, true); err != nil {
		return fmt.Errorf("carbon: inspect home lock: %w", err)
	}
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("carbon: open home lock: %w", err)
	}
	defer f.Close()
	if _, _, err := safeRegularFile(lockRoot, filename, false); err != nil {
		return fmt.Errorf("carbon: inspect home lock: %w", err)
	}
	if err := acquireLock(f); err != nil {
		return err
	}
	defer unlockLock(f) //nolint:errcheck // close also releases the OS lock
	return fn()
}

func homeLockPath(root string) (string, error) {
	cacheRoot, err := userCacheDir()
	if err != nil {
		return "", fmt.Errorf("carbon: locate home lock cache: %w", err)
	}
	cacheRoot, err = filepath.Abs(cacheRoot)
	if err != nil {
		return "", fmt.Errorf("carbon: resolve home lock cache: %w", err)
	}
	cacheRoot = filepath.Clean(cacheRoot)
	if _, _, err := safeLockDirectory(cacheRoot, cacheRoot, false); err != nil {
		return "", fmt.Errorf("carbon: inspect home lock cache: %w", err)
	}
	namespace := filepath.Join(cacheRoot, lockCacheNamespace)
	if err := ensureSafeLockDirectory(cacheRoot, namespace); err != nil {
		return "", err
	}
	lockRoot := filepath.Join(namespace, lockCacheDirectory)
	if err := ensureSafeLockDirectory(namespace, lockRoot); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonicalPathKey(root)))
	return filepath.Join(lockRoot, fmt.Sprintf("%s-%x", lockFilename, sum[:])), nil
}

func ensureSafeLockDirectory(root, filename string) error {
	err := os.Mkdir(filename, 0o700)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("carbon: create home lock cache: %w", err)
	}
	if _, _, err := safeLockDirectory(root, filename, false); err != nil {
		return fmt.Errorf("carbon: inspect home lock cache: %w", err)
	}
	return nil
}

func safeLockDirectory(root, filename string, allowMissing bool) (os.FileInfo, bool, error) {
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: home lock cache is missing", ErrUnsafePath)
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: inspect home lock cache %s: %v", ErrUnsafePath, filename, err)
	}
	if isReparsePoint(filename, info) || !info.IsDir() {
		return nil, false, fmt.Errorf("%w: refusing home lock cache directory %s", ErrUnsafePath, filename)
	}
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil || !samePath(resolved, filename) || !pathWithin(root, resolved) {
		return nil, false, fmt.Errorf("%w: home lock cache escapes expected location", ErrUnsafePath)
	}
	return info, true, nil
}

func acquireLock(f *os.File) error {
	deadline := time.Now().Add(homeLockTimeout)
	for {
		err := lockExclusiveNB(f)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errLockWouldBlock) {
			return fmt.Errorf("carbon: acquire home lock: %w", err)
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(lockRetry)
	}
}
