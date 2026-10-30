//go:build !darwin && !linux

package execution

import (
	"errors"
	"os/exec"
)

const commandGroupsSupported = false

func killCommandGroup(cmd *exec.Cmd) error {
	return errors.New("command process groups are supported on macOS and Linux only")
}
