//go:build windows

package engine

import (
	"os/exec"
)

// setupProcessGroup configures process termination on Windows systems.
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
