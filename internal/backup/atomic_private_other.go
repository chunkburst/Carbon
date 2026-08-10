//go:build !windows

package backup

import (
	"fmt"
	"os"
)

func secureBackupPrivateTempFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return verifyBackupPrivateFile(path)
}

func verifyBackupPrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("backup private file permissions are unsafe")
	}
	return nil
}

func replaceBackupPrivateFile(from, to string) error { return os.Rename(from, to) }

func syncBackupPrivateParent(parent string) error {
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
