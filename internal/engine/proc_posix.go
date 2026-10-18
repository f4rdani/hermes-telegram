//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

// setupProcessGroup configures process group isolation on POSIX systems
// so cancelling terminates the process and any spawned children (bash, ffmpeg, scripts).
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
}
