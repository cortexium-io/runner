//go:build darwin

package execution

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func ownedNativeProcesses(owner string, since time.Time, known map[string]bool) ([]nativeChild, error) {
	return ownedDarwinNativeProcesses(owner, since, known, nativeProcessHasMarker)
}

func ownedDarwinNativeProcesses(owner string, since time.Time, known map[string]bool, marker func(int, string) (bool, error)) ([]nativeChild, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	type candidate struct {
		process unix.KinfoProc
		unique  string
		parent  string
	}
	var candidates []candidate
	for _, process := range processes {
		// SZOMB is 5 in Darwin's <sys/proc.h>. Zombies cannot write resources.
		if process.Eproc.Ucred.Uid != uint32(os.Getuid()) || process.Proc.P_pid == int32(os.Getpid()) || process.Proc.P_stat == 5 {
			continue
		}
		birth := time.Unix(process.Proc.P_starttime.Sec, int64(process.Proc.P_starttime.Usec)*1000)
		if birth.Before(since.Add(-time.Second)) {
			continue
		}
		pid := int(process.Proc.P_pid)
		unique, parent, uniqueErr := darwinNativeUnique(pid)
		matched, markerErr := marker(pid, owner)
		knownUnique := uniqueErr == nil && (known[fmt.Sprint(unique)] || known[fmt.Sprint(parent)])
		if uniqueErr != nil || markerErr != nil && !knownUnique {
			if !matched && !known["birth:"+darwinNativeIdentity(&process)] {
				continue // No positive ownership: unrelated host activity is not ours.
			}
			current, probeErr := darwinNativeProcess(pid)
			if probeErr == nil && !darwinNativeProcessMatches(current, nativeChild{PID: pid, Identity: darwinNativeIdentity(&process)}) {
				continue // Positive exit/replacement proof for the inventoried identity.
			}
			return nil, errors.Join(uniqueErr, markerErr, probeErr)
		}
		key, parentKey := fmt.Sprint(unique), fmt.Sprint(parent)
		candidates = append(candidates, candidate{process, key, parentKey})
		if matched {
			known[key] = true
			known["birth:"+darwinNativeIdentity(&process)] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, candidate := range candidates {
			if known[candidate.parent] && !known[candidate.unique] {
				known[candidate.unique], changed = true, true
			}
		}
	}
	var children []nativeChild
	for _, candidate := range candidates {
		pid := int(candidate.process.Proc.P_pid)
		if known[candidate.unique] {
			known["birth:"+darwinNativeIdentity(&candidate.process)] = true
			children = append(children, nativeChild{PID: pid, Identity: darwinNativeIdentity(&candidate.process)})
		}
	}
	return children, nil
}

func darwinNativeIdentity(process *unix.KinfoProc) string {
	return fmt.Sprintf("%d/%d/%d", process.Proc.P_pid, process.Proc.P_starttime.Sec, process.Proc.P_starttime.Usec)
}

// Darwin's process unique identity survives PID reuse. The kernel ABI is a
// fixed 56-byte structure (PROC_PIDUNIQIDENTIFIERINFO), including original
// parent's unique identity even when the numeric PPID has been reparented.
func darwinNativeUnique(pid int) (uint64, uint64, error) {
	var info struct {
		UUID          [16]byte
		Unique        uint64
		Parent        uint64
		Version       int32
		ParentVersion int32
		Reserved      [2]uint64
	}
	count, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, 2, uintptr(pid), 17, 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if errno != 0 {
		return 0, 0, errno
	}
	if count != unsafe.Sizeof(info) || info.Unique == 0 {
		return 0, 0, errors.New("process unique identity is unavailable")
	}
	return info.Unique, info.Parent, nil
}

func nativeProcessHasMarker(pid int, owner string) (bool, error) {
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return false, err
	}
	if len(data) < 4 {
		return false, errors.New("process argument metadata is incomplete")
	}
	argc := int(binary.LittleEndian.Uint32(data[:4]))
	data = data[4:]
	end := bytes.IndexByte(data, 0) // Executable path, then alignment padding.
	if end < 0 {
		return false, errors.New("process executable metadata is incomplete")
	}
	data = bytes.TrimLeft(data[end:], "\x00")
	for range argc {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return false, errors.New("process argv metadata is incomplete")
		}
		data = data[end+1:]
	}
	if len(bytes.Trim(data, "\x00")) == 0 {
		return false, errors.New("Darwin omitted the process environment")
	}
	marker := []byte(nativeOwnerVariable + "=" + owner)
	for _, value := range bytes.Split(data, []byte{0}) {
		if bytes.Equal(value, marker) {
			return true, nil
		}
	}
	return false, nil
}

func killOwnedNativeProcess(child nativeChild, owner string, known map[string]bool) error {
	return killDarwinNativeProcess(child, owner, known, nativeProcessHasMarker)
}

func darwinNativeProcess(pid int) (*unix.KinfoProc, error) {
	// A successful empty PID query proves absence. The single-record helper
	// instead reports a short result as an error, obscuring an ordinary exit.
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return nil, err
	}
	if len(processes) == 0 {
		return nil, nil
	}
	if len(processes) != 1 || processes[0].Proc.P_pid != int32(pid) {
		return nil, errors.New("process PID query returned an unexpected identity")
	}
	return &processes[0], nil
}

func darwinNativeProcessMatches(process *unix.KinfoProc, child nativeChild) bool {
	return process != nil && process.Eproc.Ucred.Uid == uint32(os.Getuid()) && darwinNativeIdentity(process) == child.Identity && process.Proc.P_stat != 5
}

func killDarwinNativeProcess(child nativeChild, owner string, known map[string]bool, marker func(int, string) (bool, error)) error {
	process, err := darwinNativeProcess(child.PID)
	if err != nil {
		return err
	}
	if !darwinNativeProcessMatches(process, child) {
		return nil // Original process exited; never signal a reused PID.
	}
	owned, err := marker(child.PID, owner)
	if !owned {
		unique, _, uniqueErr := darwinNativeUnique(child.PID)
		if uniqueErr == nil && known[fmt.Sprint(unique)] {
			owned, err = true, nil
		}
	}
	if err != nil || !owned {
		if err == nil {
			err = errors.New("owned process marker changed before termination")
		}
		// Ownership metadata can disappear between inventory and termination.
		// Its errno alone proves nothing about liveness: recheck the exact
		// birth identity and preserve uncertainty if that query also fails.
		current, probeErr := darwinNativeProcess(child.PID)
		if probeErr != nil {
			return errors.Join(err, fmt.Errorf("process identity recheck: %w", probeErr))
		}
		if !darwinNativeProcessMatches(current, child) {
			return nil
		}
		return err
	}
	err = unix.Kill(child.PID, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func rememberNativeRoot(pid int, known map[string]bool) error {
	unique, _, err := darwinNativeUnique(pid)
	if err == nil {
		known[fmt.Sprint(unique)] = true
	}
	if process, infoErr := unix.SysctlKinfoProc("kern.proc.pid", pid); infoErr == nil {
		known["birth:"+darwinNativeIdentity(process)] = true
	}
	return err
}

func nativeChildIdentity(pid int) (nativeChild, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return nativeChild{}, err
	}
	if process.Proc.P_stat == 5 {
		return nativeChild{PID: pid}, nil
	}
	return nativeChild{PID: pid, Identity: darwinNativeIdentity(process)}, nil
}
