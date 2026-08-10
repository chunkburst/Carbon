//go:build windows

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateStoreRootInput rejects namespace spellings before filepath.Abs or
// EvalSymlinks can normalize a remote/device path into an apparently ordinary path.
// Carbon's elevated sidecar only accepts local, physical data roots on Windows.
func validateStoreRootInput(raw string) error {
	if storeWindowsSpecialPath(raw) {
		return fmt.Errorf("%w: Windows UNC or device store roots are not supported", ErrPathOutsideRoot)
	}
	return nil
}

// validateStoreRootPath checks every component of the Store root, matching Carbon
// home's administrator-path policy. Checking only .carbon or its direct parent is not
// enough: a parent junction can redirect an elevated writer before the managed subtree
// is reached. The post-EvalSymlinks call in storeRoot is defensive; a lexical reparse
// component is rejected before it can normally reach that point.
func validateStoreRootPath(root string) error {
	if storeWindowsSpecialPath(root) || !filepath.IsAbs(root) {
		return fmt.Errorf("%w: Store root must be an absolute local Windows path", ErrPathOutsideRoot)
	}
	volume := filepath.VolumeName(root)
	if volume == "" || strings.HasPrefix(volume, `\\`) {
		return fmt.Errorf("%w: Store root must have a local drive volume", ErrPathOutsideRoot)
	}
	remaining := strings.TrimLeft(strings.TrimPrefix(root, volume), `\\/`)
	current := volume + string(filepath.Separator)
	for _, component := range strings.FieldsFunc(remaining, func(r rune) bool { return r == '\\' || r == '/' }) {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("%w: invalid Windows Store root component", ErrPathOutsideRoot)
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("%w: inspect Windows Store root component %s: %v", ErrPathOutsideRoot, current, err)
		}
		if isStoreReparsePoint(current, info) {
			return fmt.Errorf("%w: Windows Store root component is a junction/reparse point: %s", ErrPathOutsideRoot, current)
		}
	}
	return nil
}

func storeWindowsSpecialPath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "/", `\\`))
	if value == "" {
		return false
	}
	return strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, `\??\`)
}
