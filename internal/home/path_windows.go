//go:build windows

package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateRootInput rejects namespace syntaxes before filepath.Abs/EvalSymlinks can
// normalise them into a form that hides their remote or device origin.
func validateRootInput(raw string) error {
	if windowsSpecialPath(raw) {
		return fmt.Errorf("%w: Windows UNC or device paths are not supported for Carbon homes", ErrUnsafePath)
	}
	return nil
}

// validateCanonicalRoot verifies that a local drive path has no junction, symlink, or
// other reparse point in any component. Checking only the final directory is not
// sufficient: a parent junction can be swapped after a superficially safe child is
// chosen, particularly when an elevated sidecar is using the same metadata home.
func validateCanonicalRoot(root string) error {
	if windowsSpecialPath(root) || !filepath.IsAbs(root) {
		return fmt.Errorf("%w: Carbon home must be a local absolute drive path", ErrUnsafePath)
	}
	volume := filepath.VolumeName(root)
	if volume == "" || strings.HasPrefix(volume, `\\`) {
		return fmt.Errorf("%w: Carbon home must have a local drive volume", ErrUnsafePath)
	}
	remaining := strings.TrimLeft(strings.TrimPrefix(root, volume), `\\/`)
	current := volume + string(filepath.Separator)
	for _, component := range strings.FieldsFunc(remaining, func(r rune) bool { return r == '\\' || r == '/' }) {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("%w: invalid Windows home path component", ErrUnsafePath)
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("%w: inspect Windows home component %s: %v", ErrUnsafePath, current, err)
		}
		if isReparsePoint(current, info) {
			return fmt.Errorf("%w: Windows home component is a junction/reparse point: %s", ErrUnsafePath, current)
		}
	}
	return nil
}

// validLocalStoredPath is intentionally lexical because a project source may be
// offline. Existing paths are validated through resolveRoot before use; an offline
// stored source still cannot use a remote/device namespace.
func validLocalStoredPath(value string) bool { return !windowsSpecialPath(value) }

func windowsSpecialPath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "/", `\`))
	if value == "" {
		return false
	}
	// This covers ordinary UNC (\\server\\share), verbatim/device paths (\\?\\ and
	// \\.\\), and the native object-manager spelling (\??\). Checking the broad
	// double-backslash prefix first also rejects encoded UNC variants consistently.
	return strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, `\??\`)
}
