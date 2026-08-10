//go:build !windows

package store

import (
	"fmt"
	"os"
)

// secureAtomicTempFile makes the temporary file private before any durable content is
// written. CreateTemp starts at 0600 on POSIX, but the explicit chmod and verification
// make that boundary independent of implementation defaults and umask behavior.
// It acts on the opened file rather than its pathname so a same-directory attacker
// cannot redirect the permission change by swapping the temporary name.
func secureAtomicTempFile(file *os.File) error {
	if err := file.Chmod(managedWriteFileMode); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return verifyAtomicPrivateMode(info)
}

func verifyAtomicPrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return verifyAtomicPrivateMode(info)
}

func verifyAtomicPrivateMode(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if got := info.Mode().Perm(); got != managedWriteFileMode {
		return fmt.Errorf("permissions %04o, want %04o", got, managedWriteFileMode)
	}
	return nil
}

type atomicTempIdentity struct {
	info os.FileInfo
}

func captureAtomicTempIdentity(file *os.File) (atomicTempIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return atomicTempIdentity{}, err
	}
	return atomicTempIdentity{info: info}, nil
}

// validateAtomicTempFile proves that the name about to be renamed is still the same
// object we wrote and synced. Lstat prevents following an introduced symlink; SameFile
// catches a replacement with another ordinary file before the final publication call.
func validateAtomicTempFile(path string, expected atomicTempIdentity) error {
	if err := validateAtomicTemp(path); err != nil {
		return err
	}
	named, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect named temp file: %w", err)
	}
	if !os.SameFile(expected.info, named) {
		return fmt.Errorf("temporary path no longer names the opened file")
	}
	return nil
}

// removeAtomicTempFile cleans up only the exact staging object created by this writer.
// A residual same-identity pathname race is inherent to unlink-by-name APIs; if the
// identity check is inconclusive we intentionally leave the randomized temp in place
// rather than risk deleting a substituted entry.
func removeAtomicTempFile(path string, expected atomicTempIdentity) error {
	if err := validateAtomicTempFile(path, expected); err != nil {
		return nil
	}
	return os.Remove(path)
}
