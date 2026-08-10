//go:build windows

package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validateConfigAtomicPathInput rejects UNC/device spellings before filepath.Abs can
// normalize them. An elevated Carbon sidecar must not persist config through a remote or
// device namespace, even if the final component happens to look like a regular file.
func validateConfigAtomicPathInput(raw string) error {
	if configWindowsSpecialPath(raw) {
		return fmt.Errorf("%w: Windows UNC or device config paths are not supported", ErrUnsafeConfigPath)
	}
	return nil
}

// validateConfigAtomicPathRoot enforces the local-drive portion of the same
// administrator-path policy as internal/home. validateConfigWritePath itself checks
// every existing component from this volume root to the destination's parent.
func validateConfigAtomicPathRoot(path string) error {
	if configWindowsSpecialPath(path) || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: config path must be an absolute local Windows path", ErrUnsafeConfigPath)
	}
	volume := filepath.VolumeName(path)
	if volume == "" || strings.HasPrefix(volume, `\\`) {
		return fmt.Errorf("%w: config path must have a local drive volume", ErrUnsafeConfigPath)
	}
	return nil
}

func configWindowsSpecialPath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "/", `\\`))
	if value == "" {
		return false
	}
	return strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, `\??\`)
}
