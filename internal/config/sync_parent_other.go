//go:build !windows

package config

import (
	"errors"
	"fmt"
	"os"
)

func syncAtomicParent(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(
		wrapParentSyncError("sync directory", syncErr),
		wrapParentSyncError("close directory", closeErr),
	)
}

func wrapParentSyncError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
