//go:build darwin || linux

package execution

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"syscall"
	"time"
)

func setNativeProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func terminateNativeProcess(cmd *exec.Cmd) error {
	// The Go process handle knows when an owned child has already been reaped;
	// signalling a numeric process group after that could hit a reused PID.
	return cmd.Process.Signal(syscall.SIGTERM)
}
func killNativeProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }
func processAlive(pid int) bool             { return pid > 0 && !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }
func stopNativeSocket(ctx context.Context, path string) error {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	pid, err := nativePeerPID(conn.(*net.UnixConn))
	if err != nil {
		return err
	}
	if err = syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	for processAlive(pid) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
