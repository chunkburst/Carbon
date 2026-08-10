//go:build !windows

package check

import (
	"os/exec"
	"syscall"
)

// configureKill runs the command in its own process group and, on cancel, sends SIGKILL to
// the whole group — so a timeout reaps the sh and every child it spawned.
func configureKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
