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
		if errors.Is(uniqueErr, unix.ESRCH) || errors.Is(uniqueErr, unix.EINVAL) {
			continue
		}
		matched, err := nativeProcessHasMarker(pid, owner)
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EINVAL) {
			continue // It exited during inventory.
		}
		if uniqueErr != nil {
			if matched || known["birth:"+darwinNativeIdentity(&process)] {
				return nil, uniqueErr
			}
			continue // No positive ownership: unrelated host activity is not ours.
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
	process, err := unix.SysctlKinfoProc("kern.proc.pid", child.PID)
	if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if process.Eproc.Ucred.Uid != uint32(os.Getuid()) || darwinNativeIdentity(process) != child.Identity || process.Proc.P_stat == 5 {
		return nil // Original process exited; never signal a reused PID.
	}
	owned, err := nativeProcessHasMarker(child.PID, owner)
	if !owned {
		unique, _, uniqueErr := darwinNativeUnique(child.PID)
		if uniqueErr == nil && known[fmt.Sprint(unique)] {
			owned, err = true, nil
		}
	}
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if !owned {
		return errors.New("owned process marker changed before termination")
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
