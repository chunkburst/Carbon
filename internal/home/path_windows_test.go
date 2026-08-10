//go:build windows

package home

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsHomeRejectsUNCAndDeviceNamespaces(t *testing.T) {
	for _, candidate := range []string{
		`\\server\share\carbon`,
		`//server/share/carbon`,
		`\\?\C:\carbon`,
		`\\.\PhysicalDrive0`,
		`\??\C:\carbon`,
	} {
		if !windowsSpecialPath(candidate) {
			t.Fatalf("windowsSpecialPath(%q) = false", candidate)
		}
		if _, err := resolveRoot(candidate); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("resolveRoot(%q) = %v, want ErrUnsafePath", candidate, err)
		}
		if validStoredPath(candidate) {
			t.Fatalf("validStoredPath(%q) = true", candidate)
		}
	}
	root, err := resolveRoot(t.TempDir())
	if err != nil {
		t.Fatalf("local temp root rejected: %v", err)
	}
	if !filepath.IsAbs(root) || windowsSpecialPath(root) {
		t.Fatalf("canonical local root = %q", root)
	}
}

func TestWindowsHomeRejectsReparseComponentWhenSupported(t *testing.T) {
	parent := t.TempDir()
	target := t.TempDir()
	child := filepath.Join(target, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "reparse")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Windows symlink creation is unavailable in this environment: %v", err)
	}
	if _, err := resolveRoot(filepath.Join(link, "child")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("reparse-component root = %v, want ErrUnsafePath", err)
	}
}
