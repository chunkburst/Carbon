//go:build !windows

package config

import (
	"fmt"
	"os"
)

// secureAtomicTempFile acts on the opened file so a temporary pathname swap cannot
// redirect the permission change before config data is written.
func secureAtomicTempFile(file *os.File) error {
	if err := file.Chmod(configWriteFileMode); err != nil {
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
	if got := info.Mode().Perm(); got != configWriteFileMode {
		return fmt.Errorf("permissions %04o, want %04o", got, configWriteFileMode)
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

// validateAtomicTempFile refuses a temp-name replacement after the file was opened.
// This runs immediately before rename, after all content has been synced.
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

// removeAtomicTempFile removes only the original staging object. A failed identity
// check deliberately becomes a no-op so error cleanup cannot delete a substituted file.
func removeAtomicTempFile(path string, expected atomicTempIdentity) error {
	if err := validateAtomicTempFile(path, expected); err != nil {
		return nil
	}
	return os.Remove(path)
}
