//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWritePublishesCurrentUserOnlyDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("new\n")); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	// This reads the published security descriptor and requires exactly one ACE for
	// the current token SID; it is intentionally stronger than a POSIX-like chmod
	// assertion, which would not prove Windows DACL restriction.
	if err := verifyAtomicPrivateFile(path); err != nil {
		t.Fatalf("published DACL is not current-user-only: %v", err)
	}
}
