package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	repopkg "carbon/internal/repo"
)

// storeRoot resolves the repository root once per path operation. Resolving the root is
// intentional: callers may open a repository through a symlink, but every managed path
// must still resolve beneath that physical repository directory.
func (s *Store) storeRoot() (string, error) {
	if err := validateStoreRootInput(s.root); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(s.root)
	if err != nil {
		return "", err
	}
	if err := validateStoreRootPath(abs); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if err := validateStoreRootPath(root); err != nil {
		return "", err
	}
	fi, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", s.root)
	}
	// A Store open is the compatibility boundary for legacy on-disk workspaces.
	// EnsureCarbonStore is idempotent and returns immediately for an already canonical
	// tree, while old-only state is copied and verified before any Store path is used.
	if err := repopkg.EnsureCarbonStore(root); err != nil {
		return "", err
	}
	return filepath.Clean(root), nil
}

// managedDir resolves a Store-owned directory component-by-component. This catches a
// symlink/reparse point at .carbon or any managed child before a later file operation can
// follow it outside the repository. Missing components are created only when requested.
func (s *Store) managedDir(create bool, components ...string) (string, error) {
	root, err := s.storeRoot()
	if err != nil {
		return "", err
	}
	dir := root
	for _, component := range components {
		path := filepath.Join(dir, component)
		info, err := os.Lstat(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) || !create {
				return "", err
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				return "", err
			}
			info, err = os.Lstat(path)
			if err != nil {
				return "", err
			}
		}
		if isStoreReparsePoint(path, info) {
			return "", fmt.Errorf("%w: refusing symlink or reparse directory %s", ErrPathOutsideRoot, path)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("not a directory: %s", path)
		}

		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		if !storePathWithin(root, resolved) {
			return "", fmt.Errorf("%w: %s", ErrPathOutsideRoot, path)
		}
		fi, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("not a directory: %s", path)
		}
		dir = filepath.Clean(resolved)
	}
	return dir, nil
}

// validateManagedWritePath rejects every reparse/symlink component from Store.root to
// a managed target. It is deliberately called twice by writeAtomic (before staging and
// just before final publication). Pathname APIs cannot eliminate a same-identity rename
// race without platform handle-relative operations, so callers must still treat a
// reported ErrAtomicWritePublished as requiring a fresh read; no final symlink is ever
// dereferenced by the replacement itself.
func (s *Store) validateManagedWritePath(path string) error {
	root, err := s.storeRoot()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	if !storePathWithin(root, abs) {
		return fmt.Errorf("%w: %s", ErrPathOutsideRoot, path)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPathOutsideRoot, path)
	}

	// Check every parent from the resolved Store root down. The root itself is an
	// explicit selection and may have been opened via a symlink before Store creation;
	// every component below it is Store-owned and therefore must be physical.
	parentRel := filepath.Dir(rel)
	current := root
	if parentRel != "." {
		for _, component := range strings.Split(parentRel, string(filepath.Separator)) {
			if component == "" || component == "." || component == ".." {
				return fmt.Errorf("%w: invalid managed path %s", ErrPathOutsideRoot, path)
			}
			current = filepath.Join(current, component)
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if isStoreReparsePoint(current, info) || !info.IsDir() {
				return fmt.Errorf("%w: refusing symlink, reparse point, or non-directory %s", ErrPathOutsideRoot, current)
			}
		}
	}

	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if isStoreReparsePoint(abs, info) || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: refusing symlink, reparse point, or non-regular file %s", ErrPathOutsideRoot, abs)
	}
	return nil
}

// managedFile returns a physical path beneath dir. It resolves a pre-existing final
// symlink before use and refuses one that leaves the repository. Mutating callers can
// additionally reject every final symlink so an apparent Store file can never overwrite
// or unlink a different managed file.
func (s *Store) managedFile(dir, name string, requireExisting, rejectFinalSymlink bool) (string, error) {
	if filepath.Base(name) != name || name == "." || name == ".." || name == "" {
		return "", fmt.Errorf("invalid store filename: %q", name)
	}
	root, err := s.storeRoot()
	if err != nil {
		return "", err
	}
	resolvedDir, err := resolveExistingStoreDir(dir)
	if err != nil {
		return "", err
	}
	if !storePathWithin(root, resolvedDir) {
		return "", fmt.Errorf("%w: %s", ErrPathOutsideRoot, dir)
	}

	path := filepath.Join(resolvedDir, name)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if requireExisting {
			return "", err
		}
		return path, nil
	}
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !storePathWithin(root, resolved) {
		return "", fmt.Errorf("%w: %s", ErrPathOutsideRoot, path)
	}
	if rejectFinalSymlink && isStoreReparsePoint(path, fi) {
		return "", fmt.Errorf("store: refusing symlinked or reparse managed file: %s", path)
	}
	fi, err = os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("store: expected file, found directory: %s", path)
	}
	return filepath.Clean(resolved), nil
}

func (s *Store) taskFilePath(id string, createDir, requireExisting, rejectFinalSymlink bool) (string, error) {
	dir, err := s.managedDir(createDir, carbonStoreDir, "tasks")
	if err != nil {
		return "", err
	}
	return s.managedFile(dir, id+".md", requireExisting, rejectFinalSymlink)
}

func (s *Store) sessionFilePath(id string, createDir, requireExisting, rejectFinalSymlink bool) (string, error) {
	dir, err := s.managedDir(createDir, carbonStoreDir, "sessions")
	if err != nil {
		return "", err
	}
	return s.managedFile(dir, id+".yaml", requireExisting, rejectFinalSymlink)
}

func (s *Store) liveFilePath(id string, createDir, requireExisting, rejectFinalSymlink bool) (string, error) {
	dir, err := s.managedDir(createDir, carbonStoreDir, "live")
	if err != nil {
		return "", err
	}
	return s.managedFile(dir, id+".json", requireExisting, rejectFinalSymlink)
}

func (s *Store) trashFilePath(id string, createDir, requireExisting, rejectFinalSymlink bool) (string, error) {
	dir, err := s.managedDir(createDir, carbonStoreDir, "trash")
	if err != nil {
		return "", err
	}
	return s.managedFile(dir, id+".md", requireExisting, rejectFinalSymlink)
}

func (s *Store) configFilePath(createDir, requireExisting bool) (string, error) {
	dir, err := s.managedDir(createDir, carbonStoreDir)
	if err != nil {
		return "", err
	}
	return s.managedFile(dir, "config.yaml", requireExisting, true)
}

func (s *Store) lockFilePath() (string, error) {
	dir, err := s.managedDir(true, carbonStoreDir)
	if err != nil {
		return "", err
	}
	return s.managedFile(dir, "write.lock", false, true)
}
