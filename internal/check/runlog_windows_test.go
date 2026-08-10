//go:build windows

package check

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunRejectsRunLogDirectoryJunctionOutsideRoot(t *testing.T) {
	r := runner(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(r.LogDir), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", r.LogDir, outside)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("junction unavailable: %v (%s)", err, output)
	}

	_, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if !errors.Is(err, ErrUnsafeRunLogPath) {
		t.Fatalf("Run with junctioned log directory = %v, want ErrUnsafeRunLogPath", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside directory received a run log: %+v", entries)
	}
}
