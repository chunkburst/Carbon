package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const managedWriteFileMode = 0o600

// errUnsafeAtomicWriteTarget is intentionally package-private: all public Store mutation
// entry points first resolve managed paths and return their more useful path errors. This
// second check defends the narrow interval between that resolution and the final replace.
var errUnsafeAtomicWriteTarget = errors.New("store: unsafe atomic-write target")

// ErrAtomicWritePublished means the replacement reached the destination but its final
// durability verification failed (for example, a POSIX parent-directory Sync failure).
// Callers must not treat this as "nothing changed": re-read the managed record before
// retrying or attempting compensation.
var ErrAtomicWritePublished = errors.New("store: atomic write published but durability is unconfirmed")

// atomicWrite durably replaces one managed file. The replacement is always prepared in
// the destination directory, so readers observe either the complete old file or the
// complete new file. It never uses a remove-then-rename fallback: in particular, a
// Windows replace failure leaves the old task/session/live/data file intact.
//
// The supplied path must already have come from a managed-path resolver. We validate the
// parent and final entry again immediately before publication because callers can hold a
// resolved path while doing encoding or optimistic-version work. The final rename replaces
// a directory entry rather than following a final symlink/reparse point; rejecting one
// explicitly keeps the no-escape contract fail-closed.
func atomicWrite(path string, data []byte) error {
	return atomicWriteWithDurability(path, data, atomicReplace, syncAtomicParent, nil)
}

// writeAtomic is the Store-owned entry point. It revalidates every physical component
// between the Store root and the destination both before staging and immediately before
// publication, which is stronger than checking only the direct parent/final entry.
func (s *Store) writeAtomic(path string, data []byte) error {
	if s.atomicWriteFn != nil {
		return s.atomicWriteFn(path, data)
	}
	return atomicWriteWithDurability(path, data, atomicReplace, syncAtomicParent, s.validateManagedWritePath)
}

func (s *Store) renameManaged(oldPath, newPath string) error {
	if err := s.validateManagedWritePath(oldPath); err != nil {
		return err
	}
	if err := s.validateManagedWritePath(newPath); err != nil {
		return err
	}
	if s.renameFn != nil {
		if err := s.renameFn(oldPath, newPath); err != nil {
			return err
		}
	} else if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	var syncErrors []error
	if err := syncAtomicParent(filepath.Dir(oldPath)); err != nil {
		syncErrors = append(syncErrors, fmt.Errorf("sync source parent: %w", err))
	}
	if filepath.Clean(filepath.Dir(oldPath)) != filepath.Clean(filepath.Dir(newPath)) {
		if err := syncAtomicParent(filepath.Dir(newPath)); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("sync destination parent: %w", err))
		}
	}
	if len(syncErrors) > 0 {
		return fmt.Errorf("%w: managed rename %s -> %s: %w", ErrAtomicWritePublished, oldPath, newPath, errors.Join(syncErrors...))
	}
	return nil
}

// atomicWriteWithReplace keeps the durable publication sequence testable without an
// unreliable permission race. Production always passes atomicReplace; tests can inject
// a failed final publication and assert that the old file and no temporary leftovers
// remain.
func atomicWriteWithReplace(path string, data []byte, replace func(from, to string) error) (err error) {
	return atomicWriteWithDurability(path, data, replace, syncAtomicParent, nil)
}

// atomicWriteWithDurability has explicit seams for final publication, parent-directory
// durability, and Store-owned path revalidation. They make failure semantics testable:
// a failed pre-publication replacement leaves the old file intact, while a failed
// post-publication Sync returns ErrAtomicWritePublished because the new file is visible.
func atomicWriteWithDurability(path string, data []byte, replace func(from, to string) error, syncParent func(string) error, validatePath func(string) error) (err error) {
	if replace == nil {
		return errors.New("store: atomic replacement function is required")
	}
	if syncParent == nil {
		return errors.New("store: atomic parent sync function is required")
	}
	path = filepath.Clean(path)
	if validatePath != nil {
		if err := validatePath(path); err != nil {
			return err
		}
	}
	if err := validateAtomicWriteTarget(path, true); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "carbon-write-*.tmp")
	if err != nil {
		return fmt.Errorf("store: create atomic temp: %w", err)
	}
	tmpName := tmp.Name()
	closed := false
	published := false
	tempIdentity, err := captureAtomicTempIdentity(tmp)
	if err != nil {
		_ = tmp.Close()
		// Do not unlink an entry whose identity we could not establish: retaining an
		// empty randomized temp file is safer than racing a substituted pathname.
		return fmt.Errorf("store: inspect newly created atomic temp: %w", err)
	}
	defer func() {
		if !published {
			// On failures before publication, remove only the exact private staging
			// object (including Sync/Close/replace failures). Do not remove an
			// unknown name after a swap, nor any old temporary name after success.
			// POSIX retains the descriptor through validation, so removing before Close
			// also prevents an unlinked staging inode from being immediately reused.
			if !atomicTempRequiresCloseBeforeReplace() {
				_ = removeAtomicTempFile(tmpName, tempIdentity)
			}
		}
		if !closed {
			_ = tmp.Close()
		}
		if !published && atomicTempRequiresCloseBeforeReplace() {
			_ = removeAtomicTempFile(tmpName, tempIdentity)
		}
	}()

	if err := secureAtomicTempFile(tmp); err != nil {
		return fmt.Errorf("store: secure atomic temp file: %w", err)
	}
	if n, err := tmp.Write(data); err != nil {
		return fmt.Errorf("store: write atomic temp: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("store: write atomic temp: %w", io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("store: sync atomic temp: %w", err)
	}
	if atomicTempRequiresCloseBeforeReplace() {
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("store: close atomic temp: %w", err)
		}
		closed = true
	}

	// Do not rely on a check made before serialization: a final path can be swapped
	// while the temporary file is being written. atomicReplace itself replaces the
	// entry rather than dereferencing it, and this recheck makes any such swap a
	// fail-closed error before publication.
	if validatePath != nil {
		if err := validatePath(path); err != nil {
			return err
		}
	}
	if err := validateAtomicWriteTarget(path, true); err != nil {
		return err
	}
	if err := validateAtomicTempFile(tmpName, tempIdentity); err != nil {
		return err
	}
	if err := replace(tmpName, path); err != nil {
		return fmt.Errorf("store: atomically replace %s: %w", path, err)
	}
	published = true
	if !closed {
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("%w: close published atomic temp %s: %w", ErrAtomicWritePublished, path, err)
		}
		closed = true
	}
	if err := validateAtomicRegularFile(path, false); err != nil {
		return fmt.Errorf("%w: verify published entry %s: %w", ErrAtomicWritePublished, path, err)
	}
	if err := verifyAtomicPrivateFile(path); err != nil {
		return fmt.Errorf("%w: verify published file %s: %w", ErrAtomicWritePublished, path, err)
	}

	// Syncing a directory is the POSIX durability boundary for a completed rename.
	// On Windows syncAtomicParent deliberately classifies this as best-effort because
	// directory handles are not uniformly syncable; atomicReplace uses
	// MOVEFILE_WRITE_THROUGH there. On POSIX an error is surfaced after publication.
	if err := syncParent(dir); err != nil {
		return fmt.Errorf("%w: sync parent %s: %w", ErrAtomicWritePublished, dir, err)
	}
	return nil
}

func validateAtomicWriteTarget(path string, allowMissing bool) error {
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%w: inspect parent %s: %v", errUnsafeAtomicWriteTarget, dir, err)
	}
	if isStoreReparsePoint(dir, info) || !info.IsDir() {
		return fmt.Errorf("%w: parent is not a real directory: %s", errUnsafeAtomicWriteTarget, dir)
	}
	return validateAtomicRegularFile(path, allowMissing)
}

func validateAtomicTemp(path string) error {
	return validateAtomicRegularFile(path, false)
}

func validateAtomicRegularFile(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", errUnsafeAtomicWriteTarget, path, err)
	}
	if isStoreReparsePoint(path, info) || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: refusing symlink, reparse point, or non-regular file %s", errUnsafeAtomicWriteTarget, path)
	}
	return nil
}
