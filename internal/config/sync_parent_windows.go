//go:build windows

package config

import "os"

// Windows does not provide reliable directory Sync behavior across every filesystem.
// The final MoveFileEx uses MOVEFILE_WRITE_THROUGH, so this extra directory attempt is
// intentionally best-effort only on Windows.
func syncAtomicParent(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	_ = f.Sync()
	_ = f.Close()
	return nil
}
