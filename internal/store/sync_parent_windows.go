//go:build windows

package store

import "os"

// syncAtomicParent is intentionally best-effort on Windows: directory handles are not
// uniformly syncable across supported filesystems. atomicReplace uses
// MOVEFILE_WRITE_THROUGH, while failures from this optional directory Sync are classified
// as non-fatal only on Windows.
func syncAtomicParent(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	_ = f.Sync()
	_ = f.Close()
	return nil
}
