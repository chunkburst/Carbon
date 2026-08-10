package repo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func repoSymlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable (for example, Windows without Developer Mode): %v", err)
	}
}

func TestInitRejectsEscapingManagedDirectorySymlinks(t *testing.T) {
	for _, name := range []string{CarbonDirName, "tasks", "runs", "sessions", "live"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			external := t.TempDir()
			link := filepath.Join(root, CarbonDirName)
			if name != CarbonDirName {
				if err := os.MkdirAll(link, 0o755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, name)
			}
			repoSymlinkOrSkip(t, external, link)

			err := Init(root, "SAFE")
			if !errors.Is(err, ErrCairnPathOutsideRoot) {
				t.Fatalf("Init with external %s symlink = %v, want ErrCairnPathOutsideRoot", name, err)
			}
			entries, err := os.ReadDir(external)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("Init wrote outside repository through %s: %+v", name, entries)
			}
		})
	}
}

func TestEnsureSessionDirsRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	carbon := filepath.Join(root, CarbonDirName)
	if err := os.MkdirAll(carbon, 0o755); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	repoSymlinkOrSkip(t, external, filepath.Join(carbon, "sessions"))

	if err := EnsureSessionDirs(root); !errors.Is(err, ErrCairnPathOutsideRoot) {
		t.Fatalf("EnsureSessionDirs through external symlink = %v, want ErrCairnPathOutsideRoot", err)
	}
}

func TestEnsureCarbonDirsReturnsValidatedCanonicalPath(t *testing.T) {
	root := t.TempDir()
	carbon, err := EnsureCarbonDirs(root, "tasks", "sessions", "live")
	if err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateCarbonPath(root, filepath.Join(root, CarbonDirName, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	if validated != filepath.Join(carbon, "tasks") {
		t.Fatalf("validated task dir = %q, want %q", validated, filepath.Join(carbon, "tasks"))
	}
}
