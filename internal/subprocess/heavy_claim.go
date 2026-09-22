//go:build darwin || linux

package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
	"golang.org/x/sys/unix"
)

// HeavyClaim is one host-user resource claim, independent of Project and agent
// admission. The lock inode is never replaced. The small active record survives
// supervisor crashes; acquiring a free flock alone never authorizes a new run.
type HeavyClaim struct {
	file      *os.File
	info      os.FileInfo
	directory *securefs.Directory
	path      string
	state     securefs.FileState
	record    heavyClaimRecord
	ctx       context.Context
}

type heavyClaimRecord struct {
	Version      int    `json:"version"`
	Marker       string `json:"marker"`
	OuterMarker  string `json:"outer_marker,omitempty"`
	ProjectScope string `json:"project_scope,omitempty"`
}

func heavyClaimDirectory() (string, error) {
	// Harnesses deliberately replace HOME/XDG directories. The resource belongs
	// to the OS account, not that isolated environment or a particular Project.
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(account.HomeDir) {
		return "", errors.New("OS account home is unavailable for shared heavy verification")
	}
	cache := filepath.Join(account.HomeDir, ".cache")
	if runtime.GOOS == "darwin" {
		cache = filepath.Join(account.HomeDir, "Library", "Caches")
	}
	return filepath.Join(cache, "cortexium-runner", "heavy-verification"), nil
}

// AcquireHeavyVerification inherits the caller's permissions. If its sandbox
// cannot access the shared claim it fails; there is no broker or fallback lock.
// Recursive supported verification is rejected rather than deadlocking.
func AcquireHeavyVerification(ctx context.Context) (context.Context, *HeavyClaim, error) {
	path, err := heavyClaimDirectory()
	if err != nil {
		return ctx, nil, err
	}
	return acquireHeavyVerificationAt(ctx, path)
}

func acquireHeavyVerificationAt(ctx context.Context, path string) (context.Context, *HeavyClaim, error) {
	if os.Getenv(HeavyOwnershipEnvironmentVariable) != "" {
		return ctx, nil, errors.New("recursive heavy verification is not supported")
	}
	if owner, _ := ctx.Value(invocationOwnershipKey{}).(*invocationOwnership); owner != nil && owner.environmentVariable() == HeavyOwnershipEnvironmentVariable {
		return ctx, nil, errors.New("recursive heavy verification is not supported")
	}
	if err := securefs.EnsurePrivateDir(path); err != nil {
		return ctx, nil, err
	}
	dir, err := securefs.OpenDir(path)
	if err != nil {
		return ctx, nil, err
	}
	claim := &HeavyClaim{directory: dir, path: filepath.Join(path, "slot.lock"), ctx: ctx}
	fail := func(err error) (context.Context, *HeavyClaim, error) {
		claim.close()
		return ctx, nil, err
	}
	fd, err := unix.Open(claim.path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fail(err)
	}
	claim.file = os.NewFile(uintptr(fd), claim.path)
	claim.info, err = claim.file.Stat()
	if err != nil {
		return fail(err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fail(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return fail(errors.New("heavy verification lock must be a private owned regular file"))
	}
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := claim.verify(); err != nil {
			return fail(err)
		}
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fail(err)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fail(ctx.Err())
		case <-timer.C:
		}
	}
	if err := claim.verify(); err != nil {
		return fail(err)
	}
	previous, state, err := readHeavyClaim(dir)
	if err != nil {
		return fail(err)
	}
	claim.state = state
	if previous != nil {
		return fail(errors.New("heavy verification has an unresolved prior claim; inspect and recover it before admitting more work"))
	}
	// A missing record must not hide work from a previous launcher. This never
	// kills matching processes, including those whose owner is still alive.
	processes, err := ownedProcessesForVariable(HeavyOwnershipEnvironmentVariable, "heavy", "")
	if err != nil {
		return fail(err)
	}
	if len(processes) != 0 {
		return fail(errors.New("heavy verification work is still running without a resolved claim"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	ownership, err := newOwnership(context.Background())
	if err != nil {
		return fail(err)
	}
	ownership.marker = "heavy:" + strings.SplitN(ownership.marker, ":", 2)[1]
	ownership.variable = HeavyOwnershipEnvironmentVariable
	claim.record = heavyClaimRecord{Version: 1, Marker: ownership.marker, OuterMarker: os.Getenv(ownershipVariable)}
	if scope, _ := ctx.Value(ownershipScopeKey{}).(*OwnershipScope); scope != nil {
		claim.record.ProjectScope = scope.id
	}
	data, err := json.Marshal(claim.record)
	if err != nil {
		return fail(err)
	}
	if err := dir.ReplaceFile("active.json", data, 0600, state); err != nil {
		return fail(err)
	}
	_, claim.state, err = readHeavyClaim(dir)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return ctx, nil, errors.Join(err, claim.Finish(nil))
	}
	return context.WithValue(ctx, invocationOwnershipKey{}, ownership), claim, nil
}

func (c *HeavyClaim) verify() error {
	if err := c.directory.VerifyIdentity(); err != nil {
		return err
	}
	current, err := os.Lstat(c.path)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(c.info, current) {
		return errors.New("heavy verification lock identity changed")
	}
	return nil
}

// Finish is called after supervised execution, including descendant cleanup.
// Unresolved execution retains the durable claim even though the descriptor is
// closed. Subsequent invocations fail closed instead of silently overlapping it.
func (c *HeavyClaim) Finish(runErr error) (result error) {
	if c == nil || c.file == nil {
		return nil
	}
	defer c.close()
	defer func() {
		if result != nil {
			markCleanupUnresolved(c.ctx)
		}
	}()
	if err := c.verify(); err != nil {
		return &CleanupError{Err: err}
	}
	var cleanup *CleanupError
	if errors.As(runErr, &cleanup) {
		return cleanup
	}
	processes, err := ownedProcessesForVariable(HeavyOwnershipEnvironmentVariable, "", c.record.Marker)
	if err != nil {
		return &CleanupError{Err: err}
	}
	if len(processes) != 0 {
		return &CleanupError{Err: errors.New("heavy verification descendants remain")}
	}
	if err := c.directory.ReplaceFile("active.json", []byte("null\n"), 0600, c.state); err != nil {
		return &CleanupError{Err: err}
	}
	return nil
}

func (c *HeavyClaim) close() {
	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}
	if c.directory != nil {
		_ = c.directory.Close()
		c.directory = nil
	}
}

func readHeavyClaim(dir *securefs.Directory) (*heavyClaimRecord, securefs.FileState, error) {
	data, _, state, err := dir.ReadFile("active.json", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil, securefs.FileState{}, nil
	}
	if err != nil {
		return nil, state, err
	}
	if !state.Exists {
		return nil, state, nil
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return nil, state, err
	}
	var record *heavyClaimRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, state, errors.New("heavy verification claim is malformed")
	}
	if record != nil && (record.Version != 1 || matchingMarkerForVariable([]byte(HeavyOwnershipEnvironmentVariable+"="+record.Marker), HeavyOwnershipEnvironmentVariable, "heavy", "") == "") {
		return nil, state, errors.New("heavy verification claim identity is invalid")
	}
	return record, state, nil
}

// checkHeavyClaim connects a nested CLI failure to its outer harness's capacity
// quarantine. A model cannot turn cleanup uncertainty into success by ignoring
// a shell exit code. Missing storage is normal for users not using the launcher.
func checkHeavyClaim(scope, exact string) error {
	path, err := heavyClaimDirectory()
	if err != nil {
		return err
	}
	return checkHeavyClaimAt(path, scope, exact)
}

func checkHeavyClaimAt(path, scope, exact string) error {
	dir, err := securefs.OpenDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	record, _, err := readHeavyClaim(dir)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	if exact != "" && record.OuterMarker == exact || scope != "" && (record.ProjectScope == scope || strings.HasPrefix(record.OuterMarker, scope+":")) {
		if exact == "" {
			// A healthy invocation consumes only the heavy slot, not every free
			// agent slot in its Project. A free lock with an active record is
			// abandoned/quarantined, even if its former owner remains alive.
			fd, openErr := unix.Open(filepath.Join(path, "slot.lock"), unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if openErr != nil {
				return openErr
			}
			defer unix.Close(fd)
			lockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
			if lockErr == nil {
				defer unix.Flock(fd, unix.LOCK_UN)
				current, _, readErr := readHeavyClaim(dir)
				if readErr != nil {
					return readErr
				}
				if current == nil {
					return nil
				}
			} else if errors.Is(lockErr, unix.EWOULDBLOCK) || errors.Is(lockErr, unix.EAGAIN) {
				parts := strings.Split(record.Marker, ":")
				pid, parseErr := strconv.Atoi(parts[1])
				owner, inspectErr := inspectProcess(pid)
				if parseErr == nil && inspectErr == nil && owner.birth == parts[2] {
					return nil
				}
			} else {
				return lockErr
			}
		}
		return fmt.Errorf("nested heavy verification claim is unresolved; inspect surviving work before resuming")
	}
	return nil
}
