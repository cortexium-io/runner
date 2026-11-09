//go:build !darwin && !linux

package execution

import (
	"errors"
	"os"
)

func tryCaptureLease(*os.File) (bool, error) {
	return false, errors.New("source capture leases require macOS or Linux")
}
