//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRootRejectsWindowsRemoteAndDeviceNamespaces(t *testing.T) {
	for _, root := range []string{
		`\\server\share\repo`,
		`//server/share/repo`,
		`\\?\C:\repo`,
		`\\.\PhysicalDrive0`,
		`\??\C:\repo`,
	} {
		s := New(root)
		if _, err := s.storeRoot(); !errors.Is(err, ErrPathOutsideRoot) {
			t.Fatalf("storeRoot(%q) = %v, want ErrPathOutsideRoot", root, err)
		}
	}
}

func TestStoreRootRejectsWindowsReparseComponentWhenSupported(t *testing.T) {
	parent := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(parent, "store-root-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Windows symlink creation is unavailable in this environment: %v", err)
	}
	if _, err := New(link).storeRoot(); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("storeRoot through reparse component = %v, want ErrPathOutsideRoot", err)
	}
}
