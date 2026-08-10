//go:build windows

package check

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/windows"
)

var (
	getSystemDirectory = windows.GetSystemDirectory
	runTaskkill        = func(path string, args ...string) error { return exec.Command(path, args...).Run() }
)

// configureKill terminates the shell and its children on cancellation. Git Bash's `sh -c`
// otherwise leaves children such as `sleep` holding stdout/stderr pipes until WaitDelay.
// taskkill.exe is resolved from the Windows system directory rather than PATH; if it cannot
// run, Process.Kill still terminates the direct child.
func configureKill(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := killProcessTree(cmd.Process.Pid); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}

func killProcessTree(pid int) error {
	systemDir, err := getSystemDirectory()
	if err != nil {
		return fmt.Errorf("locate Windows system directory: %w", err)
	}
	taskkill := filepath.Join(systemDir, "taskkill.exe")
	return runTaskkill(taskkill, "/PID", strconv.Itoa(pid), "/T", "/F")
}
