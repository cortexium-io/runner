// Package execution owns native process containment and the Codex protocol.
package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
	"golang.org/x/sys/unix"
)

// Lock is also used by inspection: taking and immediately releasing this lock
// observes coordinator ownership without changing authority in the database.
func Lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("coordinator already running")
	}
	return f, nil
}
func Running(path string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return true
	}
	return false
}
func Gone(p core.Process) bool { return p.PID > 1 && errors.Is(unix.Kill(-p.PID, 0), unix.ESRCH) }
func signalOwned(p core.Process, signal unix.Signal) error {
	identity, err := processIdentity(p.PID)
	if err != nil || identity != p.Identity {
		return errors.New("process identity is unresolved; retaining ownership")
	}
	return unix.Kill(-p.PID, signal)
}

// Run gates execution on a durable process record. EOF before authorization can
// only exit the waiting shell, so an unrecorded child cannot start the harness.
// The process group is never released merely because its leader exited.
func Run(ctx context.Context, command string, args []string, dir, input string, stdout, stderr io.Writer, started func(core.Process) error) (core.Process, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.Process{}, true, err
	}
	gate := `IFS= read -r authorization; [ "$authorization" = go ] || exit 125; exec "$@"`
	argv := append([]string{"-c", gate, "runner-launch"}, command)
	argv = append(argv, args...)
	cmd := exec.Command("/bin/sh", argv...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return core.Process{}, true, err
	}
	if err = cmd.Start(); err != nil {
		pipe.Close()
		return core.Process{}, true, err
	}
	p := core.Process{PID: cmd.Process.Pid}
	p.Identity, err = processIdentity(p.PID)
	if err == nil {
		err = started(p)
	}
	if err != nil {
		pipe.Close()
		_ = cmd.Wait()
		return p, Gone(p), err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	go func() { _, _ = io.WriteString(pipe, "go\n"+input); _ = pipe.Close() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
		if stopErr := signalOwned(p, unix.SIGTERM); stopErr != nil && !Gone(p) {
			return p, false, errors.Join(err, stopErr)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			if !Gone(p) {
				if stopErr := signalOwned(p, unix.SIGKILL); stopErr != nil {
					return p, false, errors.Join(err, stopErr)
				}
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				return p, false, errors.New("process did not stop; retaining ownership")
			}
		}
	}
	if !Gone(p) {
		return p, false, fmt.Errorf("process group remains after leader exit; retaining ownership: %w", err)
	}
	return p, true, err
}

// Bound retained logs and fail the invocation if evidence would be truncated.
type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.remaining {
		return 0, errors.New("execution evidence exceeds its byte limit")
	}
	n, e := w.writer.Write(b)
	w.remaining -= int64(n)
	return n, e
}
