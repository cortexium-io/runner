//go:build darwin || linux

package release

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

var errOperatorBusy = errors.New("operator lease is active")

func lockOperator(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errOperatorBusy
	}
	return err
}
func unlockOperator(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
