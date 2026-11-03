//go:build !darwin && !linux

package release

import (
	"errors"
	"os"
	"os/exec"
)

var errOperatorBusy = errors.New("operator lease is active")

func lockOperator(*os.File) error { return errors.New("release operators require macOS or Linux") }
func unlockOperator(*os.File)     {}
func preserveRestoreOwner(string, os.FileInfo) error {
	return errors.New("approved restoration requires macOS or Linux")
}
func configureReleaseCommand(*exec.Cmd)    {}
func releaseCommandSettled(*exec.Cmd) bool { return false }
func deliveryFree(string) (int64, error)   { return 0, errors.New("delivery requires macOS or Linux") }
