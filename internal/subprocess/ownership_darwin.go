package subprocess

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func inspectProcess(pid int) (ownedProcess, error) {
	// SysctlKinfoProc reports EIO for an empty result when a process has exited.
	// The slice API preserves that distinction from a real inspection failure.
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return ownedProcess{}, err
	}
	if len(infos) == 0 {
		return ownedProcess{}, unix.ESRCH
	}
	info := infos[0]
	if info.Proc.P_pid != int32(pid) || info.Proc.P_stat == 5 {
		return ownedProcess{}, unix.ESRCH
	}
	return ownedProcess{pid: pid, birth: fmt.Sprintf("%d.%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)}, nil
}

func processEnvironment(pid int) ([]byte, error) {
	// During exec macOS briefly exposes an identity but not its argument block.
	// Retry that bounded transition; do not mistake it for proof of no marker.
	var data []byte
	var err error
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		data, err = unix.SysctlRaw("kern.procargs2", pid)
		if !errors.Is(err, unix.EIO) && !errors.Is(err, unix.EINVAL) {
			break
		}
		if _, identityErr := inspectProcess(pid); identityErr != nil {
			return nil, identityErr
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, unix.ESRCH
	}
	argc := int(binary.NativeEndian.Uint32(data[:4]))
	data = data[4:]
	// Executable path, padding, argc arguments, then NUL-separated environment.
	i := bytes.IndexByte(data, 0)
	if i < 0 {
		return nil, unix.ESRCH
	}
	data = bytes.TrimLeft(data[i+1:], "\x00")
	for range argc {
		i = bytes.IndexByte(data, 0)
		if i < 0 {
			return nil, unix.ESRCH
		}
		data = data[i+1:]
	}
	return data, nil
}

func ownedProcesses(scope, exact string) ([]ownedProcess, error) {
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
	if err != nil {
		return nil, err
	}
	var result []ownedProcess
	deadline := time.Now().Add(time.Second)
	for _, info := range infos {
		if time.Now().After(deadline) {
			return nil, errors.New("process ownership inspection exceeded its time budget")
		}
		if info.Proc.P_stat == 5 {
			continue
		} // Zombies no longer execute or hold pipes.
		pid := int(info.Proc.P_pid)
		env, err := processEnvironment(pid)
		if err != nil {
			// macOS withholds environment data for protected unrelated processes.
			// Never infer ownership or signal those processes.
			if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EPERM) {
				continue
			}
			return nil, fmt.Errorf("inspect process %d environment: %w", pid, err)
		}
		marker := matchingMarker(env, scope, exact)
		if marker == "" {
			continue
		}
		process, err := inspectProcess(pid)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return nil, err
		}
		birth := fmt.Sprintf("%d.%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)
		if process.birth != birth {
			continue
		}
		process.marker = marker
		result = append(result, process)
	}
	return result, nil
}

func signalOwnedProcess(process ownedProcess, force bool) error {
	current, err := inspectProcess(process.pid)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.birth != process.birth {
		return nil
	}
	env, err := processEnvironment(process.pid)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	if matchingMarker(env, "", process.marker) != process.marker {
		return errors.New("owned process marker changed")
	}
	// Recheck birth after reading the environment to reject observed PID reuse.
	current, err = inspectProcess(process.pid)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.birth != process.birth {
		return nil
	}
	signal := unix.SIGTERM
	if force {
		signal = unix.SIGKILL
	}
	err = unix.Kill(process.pid, signal)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
