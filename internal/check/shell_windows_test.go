//go:build windows

package check

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestGitBashShellCandidatesIncludeProgramFiles(t *testing.T) {
	candidates := gitBashShellCandidatesFor(`C:\Program Files`, "", `C:\Users\test\AppData\Local`, `C:\Users\test`)
	if !slices.Contains(candidates, `C:\Program Files\Git\bin\sh.exe`) {
		t.Fatalf("Git Bash candidates missing Program Files shell: %#v", candidates)
	}
	if !slices.Contains(candidates, `C:\Users\test\scoop\apps\git\current\bin\sh.exe`) {
		t.Fatalf("Git Bash candidates missing Scoop shell: %#v", candidates)
	}
}

func TestFirstExistingFileSkipsMissingCandidates(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), "sh.exe")
	if err := os.WriteFile(candidate, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstExistingFile([]string{filepath.Join(t.TempDir(), "missing.exe"), candidate}); got != candidate {
		t.Fatalf("firstExistingFile = %q, want %q", got, candidate)
	}
}
