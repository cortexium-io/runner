//go:build darwin || linux

package subprocess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cortexium-io/runner/internal/securefs"
	"golang.org/x/sys/unix"
)

type HeavyRecovery struct {
	Present     bool   `json:"present"`
	Recoverable bool   `json:"recoverable"`
	Token       string `json:"token,omitempty"`
	Cleared     bool   `json:"cleared"`
	Reason      string `json:"reason"`
}

// RecoverHeavyVerification is an explicit operator action, never admission-time
// cleanup. Preview yields the exact claim token. Applying requires that token,
// an absent original supervisor and no surviving tagged work. It signals no
// process and cannot turn an interrupted check into passing evidence.
func RecoverHeavyVerification(ctx context.Context, expectedToken string, apply bool) (HeavyRecovery, error) {
	root, err := heavyClaimDirectory()
	if err != nil {
		return HeavyRecovery{}, err
	}
	return recoverHeavyVerificationAt(ctx, root, expectedToken, apply)
}

func recoverHeavyVerificationAt(ctx context.Context, root, expectedToken string, apply bool) (HeavyRecovery, error) {
	result := HeavyRecovery{Reason: "no unresolved heavy verification claim"}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	dir, err := securefs.OpenDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer dir.Close()
	lockPath := filepath.Join(root, "slot.lock")
	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return result, err
	}
	file := os.NewFile(uintptr(fd), lockPath)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return result, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return result, errors.New("unsafe heavy verification lock")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return result, errors.New("heavy verification is active; recovery cannot acquire ownership")
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	claim := HeavyClaim{directory: dir, file: file, path: lockPath, info: info}
	if err := claim.verify(); err != nil {
		return result, err
	}
	record, state, err := readHeavyClaim(dir)
	if err != nil {
		return result, err
	}
	if record == nil {
		return result, nil
	}
	result.Present = true
	data, _, _, err := dir.ReadFile("active.json", 4096)
	if err != nil {
		return result, err
	}
	token := sha256.Sum256(data)
	result.Token = hex.EncodeToString(token[:])
	parts := strings.Split(record.Marker, ":")
	pid, err := strconv.Atoi(parts[1])
	if err != nil {
		return result, errors.New("invalid recorded supervisor identity")
	}
	owner, err := inspectProcess(pid)
	if err == nil && owner.birth == parts[2] {
		result.Reason = "original verification supervisor is still alive"
		return result, nil
	}
	if err != nil && !processDisappeared(err) {
		return result, err
	}
	processes, err := ownedProcessesForVariable(HeavyOwnershipEnvironmentVariable, "", record.Marker)
	if err != nil {
		return result, err
	}
	if len(processes) != 0 {
		result.Reason = "owned heavy verification descendants are still running"
		return result, nil
	}
	if err := dir.VerifyFile("active.json", state); err != nil {
		return result, err
	}
	result.Recoverable = true
	result.Reason = "supervisor and supported tagged descendants are absent; interrupted proof remains unavailable"
	if !apply {
		return result, nil
	}
	if expectedToken == "" || expectedToken != result.Token {
		return result, errors.New("heavy verification recovery requires the unchanged preview token")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := claim.verify(); err != nil {
		return result, err
	}
	if err := dir.ReplaceFile("active.json", []byte("null\n"), 0600, state); err != nil {
		return result, err
	}
	result.Cleared = true
	return result, nil
}
