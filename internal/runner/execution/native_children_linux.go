//go:build linux

package execution

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func linuxNativeIdentity(pid int) (string, bool, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid))
	info, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	if info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		return "", false, nil
	}
	data, err := os.ReadFile(filepath.Join(path, "stat"))
	if err != nil {
		return "", false, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", false, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return "", false, errors.New("incomplete process stat")
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", false, nil
	}
	return fmt.Sprintf("%d/%s", pid, fields[19]), true, nil
}

func nativeProcessHasMarker(pid int, owner string) (bool, error) {
	f, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return false, err
	}
	marker := []byte(nativeOwnerVariable + "=" + owner)
	for _, value := range bytes.Split(data, []byte{0}) {
		if bytes.Equal(value, marker) {
			return true, nil
		}
	}
	return false, nil
}

func ownedNativeProcesses(owner string, since time.Time, known map[string]bool) ([]nativeChild, error) {
	hz, err := linuxNativeClockTicks()
	if err != nil {
		return nil, err
	}
	var system unix.Sysinfo_t
	if err := unix.Sysinfo(&system); err != nil {
		return nil, err
	}
	boot := time.Now().Unix() - system.Uptime
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var children []nativeChild
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		identity, candidate, err := linuxNativeIdentity(pid)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			continue // Unrelated protected host processes grant no ownership.
		}
		if !candidate {
			continue
		}
		parts := strings.Split(identity, "/")
		start, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return nil, err
		}
		birth := time.Unix(boot+int64(start/hz), 0)
		if birth.Before(since.Add(-2 * time.Second)) {
			continue
		}
		matched, err := nativeProcessHasMarker(pid, owner)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			if known[identity] {
				return nil, err
			}
			continue
		}
		if matched {
			known[identity] = true
			children = append(children, nativeChild{PID: pid, Identity: identity})
		} else if known[identity] {
			children = append(children, nativeChild{PID: pid, Identity: identity})
		}
	}
	return children, nil
}

func killOwnedNativeProcess(child nativeChild, owner string, known map[string]bool) error {
	// A pidfd pins the selected process while identity and marker are checked.
	fd, err := unix.PidfdOpen(child.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	identity, alive, err := linuxNativeIdentity(child.PID)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	if !alive || identity != child.Identity {
		return nil
	}
	owned, err := nativeProcessHasMarker(child.PID, owner)
	if err != nil {
		return err
	}
	if !owned && !known[identity] {
		return errors.New("owned process marker changed before termination")
	}
	err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func rememberNativeRoot(pid int, known map[string]bool) error {
	identity, alive, err := linuxNativeIdentity(pid)
	if err == nil && alive {
		known[identity] = true
	}
	return err
}

func nativeChildIdentity(pid int) (nativeChild, error) {
	identity, _, err := linuxNativeIdentity(pid)
	return nativeChild{PID: pid, Identity: identity}, err
}

func linuxNativeClockTicks() (uint64, error) {
	data, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return 0, err
	}
	word := strconv.IntSize / 8
	for offset := 0; offset+2*word <= len(data); offset += 2 * word {
		var tag, value uint64
		if word == 8 {
			tag = binary.NativeEndian.Uint64(data[offset:])
			value = binary.NativeEndian.Uint64(data[offset+word:])
		} else {
			tag = uint64(binary.NativeEndian.Uint32(data[offset:]))
			value = uint64(binary.NativeEndian.Uint32(data[offset+word:]))
		}
		if tag == 17 && value > 0 {
			return value, nil
		} // AT_CLKTCK
	}
	return 0, errors.New("kernel process clock ticks are unavailable")
}
