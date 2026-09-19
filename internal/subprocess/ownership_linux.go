package subprocess

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func inspectProcess(pid int) (ownedProcess, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ownedProcess{}, err
	}
	index := strings.LastIndex(string(data), ") ")
	if index < 0 {
		return ownedProcess{}, errors.New("invalid process identity")
	}
	tail := string(data[index+2:])
	fields := strings.Fields(tail)
	if len(fields) < 20 {
		return ownedProcess{}, errors.New("invalid process identity")
	}
	if fields[0] == "Z" {
		return ownedProcess{}, os.ErrNotExist
	}
	return ownedProcess{pid: pid, birth: fields[19]}, nil
}

func processEnvironment(pid int) ([]byte, error) {
	file, err := os.Open(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, 2*1024*1024))
}

func ownedProcesses(scope, exact string) ([]ownedProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var result []ownedProcess
	deadline := time.Now().Add(time.Second)
	for _, entry := range entries {
		if time.Now().After(deadline) {
			return nil, errors.New("process ownership inspection exceeded its time budget")
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := entry.Info()
		if processDisappeared(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if stat.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			continue
		}
		process, err := inspectProcess(pid)
		if processDisappeared(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		env, err := processEnvironment(pid)
		if processDisappeared(err) || errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return nil, err
		}
		process.marker = matchingMarker(env, scope, exact)
		if process.marker != "" {
			result = append(result, process)
		}
	}
	return result, nil
}

func signalOwnedProcess(process ownedProcess, force bool) error {
	// pidfd pins the signal target across exit/PID reuse. No numeric-PID fallback.
	fd, err := unix.PidfdOpen(process.pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	current, err := inspectProcess(process.pid)
	if processDisappeared(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.birth != process.birth {
		return nil
	}
	env, err := processEnvironment(process.pid)
	if processDisappeared(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if matchingMarker(env, "", process.marker) != process.marker {
		return errors.New("owned process marker changed")
	}
	signal := unix.SIGTERM
	if force {
		signal = unix.SIGKILL
	}
	err = unix.PidfdSendSignal(fd, signal, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
