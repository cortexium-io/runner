//go:build darwin || linux

package release

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func deliveryFree(path string) (int64, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return int64(stat.Bavail) * int64(stat.Bsize), err
}

func preserveRestoreOwner(file string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("restore ownership is unavailable")
	}
	return os.Chown(file, int(stat.Uid), int(stat.Gid))
}

var errOperatorBusy = errors.New("operator lease is active")

func lockOperator(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errOperatorBusy
	}
	return err
}
func unlockOperator(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }

func configureReleaseCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
func releaseCommandSettled(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return true
	}
	return errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH)
}
