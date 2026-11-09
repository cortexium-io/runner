//go:build darwin || linux

package execution

import (
	"errors"
	"os/exec"
	"syscall"
)

const commandGroupsSupported = true

func killCommandGroup(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
