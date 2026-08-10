//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigAtomicWritePublishesCurrentUserOnlyDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("prefix: OLD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("prefix: NEW\n")); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	// verifyAtomicPrivateFile reads the actual security descriptor and rejects an
	// inherited or additional ACE; os.File.Chmod alone would not provide this proof.
	if err := verifyAtomicPrivateFile(path); err != nil {
		t.Fatalf("published DACL is not current-user-only: %v", err)
	}
}
