package check

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	errRunLogExists = errors.New("check run log already exists")

	// ErrLogDirOutsideRoot is returned when the configured run-log directory does
	// not live below its trusted log root after canonical path resolution.
	ErrLogDirOutsideRoot = errors.New("check run log directory must be within runner root")

	// ErrUnsafeRunLogPath is returned for a run-log directory component or file
	// that is a symlink, Windows reparse point, directory, or another non-regular
	// filesystem object. Run output can contain arbitrary command output, so it
	// must never be redirected outside the repository by one of these objects.
	ErrUnsafeRunLogPath = errors.New("check run log path is unsafe")
)

// RunLogDir returns the canonical, existing run-log directory after verifying that
// every component below root is an ordinary directory. It deliberately rejects
// symlinks and Windows reparse points rather than following them: .carbon/runs is
// writable by the repository owner and must not become an arbitrary write/read
// redirector.
func RunLogDir(root, logDir string) (string, error) {
	return prepareRunLogDir(root, logDir, false)
}

// ReadRunLog reads one direct child of logDir without following a symlink or Windows
// reparse point. The caller supplies root so both the directory and file are checked
// against the repository boundary.
func ReadRunLog(root, logDir, path string) ([]byte, error) {
	dir, err := RunLogDir(root, logDir)
	if err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("check: resolve run log path: %w", err)
	}
	if !isDirectChild(dir, abs) {
		return nil, fmt.Errorf("%w: %s", ErrLogDirOutsideRoot, path)
	}

	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if isReparsePoint(abs, info) || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrUnsafeRunLogPath, abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("check: resolve run log: %w", err)
	}
	if !isDirectChild(dir, resolved) {
		return nil, fmt.Errorf("%w: %s", ErrLogDirOutsideRoot, abs)
	}

	f, err := openRunLogNoFollow(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %v", ErrUnsafeRunLogPath, abs, err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, fmt.Errorf("check: stat run log: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrUnsafeRunLogPath, abs)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("check: read run log: %w", err)
	}
	return data, nil
}

// prepareRunLogDir returns a safe log directory. When create is true, missing path
// components are made one at a time and inspected immediately; MkdirAll would follow
// an existing symlink/reparse point before this package could reject it.
func prepareRunLogDir(root, logDir string, create bool) (string, error) {
	if root == "" {
		root = "."
	}
	if logDir == "" {
		logDir = root
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("check: resolve runner root: %w", err)
	}
	logAbs, err := filepath.Abs(logDir)
	if err != nil {
		return "", fmt.Errorf("check: resolve run log directory: %w", err)
	}
	if !isWithinRoot(rootAbs, logAbs) {
		return "", fmt.Errorf("%w: %s", ErrLogDirOutsideRoot, logAbs)
	}
	rel, err := filepath.Rel(rootAbs, logAbs)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrLogDirOutsideRoot, logAbs)
	}

	rootReal, err := existingDir(root)
	if err != nil {
		return "", fmt.Errorf("check: resolve runner root: %w", err)
	}
	if rel == "." {
		return rootReal, nil
	}

	current := rootReal
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			if !create {
				return "", err
			}
			if err := os.Mkdir(next, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("check: create run log directory: %w", err)
			}
			info, err = os.Lstat(next)
		}
		if err != nil {
			return "", fmt.Errorf("check: inspect run log directory: %w", err)
		}
		if isReparsePoint(next, info) || !info.IsDir() {
			return "", fmt.Errorf("%w: %s", ErrUnsafeRunLogPath, next)
		}
		resolved, err := filepath.EvalSymlinks(next)
		if err != nil {
			return "", fmt.Errorf("check: resolve run log directory: %w", err)
		}
		if !isWithinRoot(rootReal, resolved) {
			return "", fmt.Errorf("%w: %s", ErrLogDirOutsideRoot, next)
		}
		current = next
	}
	return current, nil
}

func freshRunLogPath(dir, path string) error {
	if !isDirectChild(dir, path) {
		return fmt.Errorf("%w: %s", ErrLogDirOutsideRoot, path)
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("check: inspect run log: %w", err)
	case isReparsePoint(path, info) || !info.Mode().IsRegular():
		return fmt.Errorf("%w: %s", ErrUnsafeRunLogPath, path)
	default:
		// Do not overwrite a prior run, even a regular file. Besides preserving the
		// audit trail, O_EXCL below makes this race-safe if a file appears later.
		return fmt.Errorf("%w: %s", errRunLogExists, path)
	}
}

func isDirectChild(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel) && filepath.Dir(rel) == "."
}
