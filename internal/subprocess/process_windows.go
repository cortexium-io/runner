//go:build windows

package subprocess

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const createNewProcessGroup = 0x00000200

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

func terminateProcessGroup(cmd *exec.Cmd, _ bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	if err != nil {
		killErr := cmd.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			return nil
		}
		return killErr
	}
	return nil
}

func processGroupAlive(cmd *exec.Cmd) (bool, error) {
	// taskkill performs the Windows descendant-tree cleanup synchronously.
	// The process group has no queryable handle after its leader exits.
	return false, nil
}
