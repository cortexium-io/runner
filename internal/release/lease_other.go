//go:build !darwin && !linux

package release

import (
	"errors"
	"os"
)

var errOperatorBusy = errors.New("operator lease is active")

func lockOperator(*os.File) error { return errors.New("release operators require macOS or Linux") }
func unlockOperator(*os.File)     {}
