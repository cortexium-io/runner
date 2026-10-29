//go:build !darwin && !linux

package execution

import (
	"context"
	"errors"
	"os/exec"
)

func setNativeProcessGroup(*exec.Cmd) {}
func terminateNativeProcess(*exec.Cmd) error {
	return errors.New("native process management requires macOS or Linux")
}
func killNativeProcess(*exec.Cmd) error {
	return errors.New("native process management requires macOS or Linux")
}
func processAlive(int) bool { return true }
func stopNativeSocket(context.Context, string) error {
	return errors.New("native process management requires macOS or Linux")
}
