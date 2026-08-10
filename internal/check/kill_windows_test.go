//go:build windows

package check

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestKillProcessTreeUsesSystemTaskkill(t *testing.T) {
	oldDir := getSystemDirectory
	oldRun := runTaskkill
	t.Cleanup(func() {
		getSystemDirectory = oldDir
		runTaskkill = oldRun
	})

	getSystemDirectory = func() (string, error) { return `C:\Windows\System32`, nil }
	var gotPath string
	var gotArgs []string
	runTaskkill = func(path string, args ...string) error {
		gotPath = path
		gotArgs = append([]string(nil), args...)
		return nil
	}

	if err := killProcessTree(1234); err != nil {
		t.Fatalf("killProcessTree: %v", err)
	}
	if want := filepath.Join(`C:\Windows\System32`, "taskkill.exe"); gotPath != want {
		t.Fatalf("taskkill path = %q, want %q", gotPath, want)
	}
	if want := []string{"/PID", "1234", "/T", "/F"}; !slices.Equal(gotArgs, want) {
		t.Fatalf("taskkill args = %#v, want %#v", gotArgs, want)
	}
}

func TestKillProcessTreeReturnsSystemDirectoryError(t *testing.T) {
	oldDir := getSystemDirectory
	oldRun := runTaskkill
	t.Cleanup(func() {
		getSystemDirectory = oldDir
		runTaskkill = oldRun
	})
	want := errors.New("system directory unavailable")
	getSystemDirectory = func() (string, error) { return "", want }
	runTaskkill = func(string, ...string) error {
		t.Fatal("taskkill should not run without a system directory")
		return nil
	}

	if err := killProcessTree(1234); !errors.Is(err, want) {
		t.Fatalf("killProcessTree = %v, want wrapped %v", err, want)
	}
}
