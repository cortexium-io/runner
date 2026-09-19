package subprocess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const ownershipVariable = "CORTEXIUM_RUNNER_PROCESS_OWNER"

// OwnershipEnvironmentVariable is forwarded explicitly by harnesses with
// filtered tool environments. It is an operational marker, not a credential.
const OwnershipEnvironmentVariable = ownershipVariable

// CleanupError means that releasing execution capacity would be unsafe. It is
// distinct from the command's exit status, timeout or provider failure.
type CleanupError struct{ Err error }

func (e *CleanupError) Error() string { return "owned process cleanup unresolved: " + e.Err.Error() }
func (e *CleanupError) Unwrap() error { return e.Err }

// OwnershipScope connects native harness invocations to local admission. A
// failed cleanup quarantines this scope until the operator stops it and checks
// surviving work. Inherited markers also expose orphans to a replacement Runner;
// no command lines, environment contents or process journal are persisted.
type OwnershipScope struct {
	id     string
	mu     sync.Mutex
	unsafe bool
}

func NewOwnershipScope(project string) *OwnershipScope {
	digest := sha256.Sum256([]byte(project))
	return &OwnershipScope{id: hex.EncodeToString(digest[:])}
}

type ownershipScopeKey struct{}
type harnessProcessKey struct{}
type invocationOwnershipKey struct{}
type cleanupObserverKey struct{}

func WithOwnershipScope(ctx context.Context, scope *OwnershipScope) context.Context {
	return context.WithValue(ctx, ownershipScopeKey{}, scope)
}

// WithCleanupObserver records a Runner-owned interval without coupling this
// OS boundary to metrics or exporting command/argument/environment data.
func WithCleanupObserver(ctx context.Context, start func() func(error)) context.Context {
	return context.WithValue(ctx, cleanupObserverKey{}, start)
}

func (s *OwnershipScope) Unresolved() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unsafe
}

// CheckAdmission never kills processes. A previous Runner's surviving tagged
// work requires operator inspection before this Project can admit new work.
func (s *OwnershipScope) CheckAdmission() error {
	if s == nil {
		return nil
	}
	if s.Unresolved() {
		return errors.New("agent admission paused: process cleanup is unresolved; inspect surviving work before restarting Runner")
	}
	processes, err := ownedProcesses(s.id, "")
	if err != nil {
		return fmt.Errorf("verify process ownership before admission: %w", err)
	}
	for _, process := range processes {
		parts := strings.Split(process.marker, ":")
		if len(parts) != 4 {
			continue
		}
		pid, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		owner, err := inspectProcess(pid)
		if err != nil || owner.birth != parts[2] {
			return errors.New("agent admission paused: owned work survived a previous Runner; inspect and stop that work before retrying")
		}
	}
	return nil
}

type ownedProcess struct {
	pid    int
	birth  string
	marker string
}

type invocationOwnership struct {
	marker string
}

// PrepareHarness allows an adapter to forward the exact marker into a filtered
// tool environment without widening its inherited environment policy.
func PrepareHarness(ctx context.Context) (context.Context, string, error) {
	ownership, err := newOwnership(ctx)
	if err != nil {
		return ctx, "", err
	}
	return context.WithValue(ctx, invocationOwnershipKey{}, ownership), ownership.marker, nil
}

func startOwnership(ctx context.Context, cmd *exec.Cmd) (*invocationOwnership, error) {
	if enabled, _ := ctx.Value(harnessProcessKey{}).(bool); !enabled {
		return nil, nil
	}
	ownership, _ := ctx.Value(invocationOwnershipKey{}).(*invocationOwnership)
	if ownership == nil {
		var err error
		ownership, err = newOwnership(ctx)
		if err != nil {
			return nil, err
		}
	}
	environment := cmd.Env
	if environment == nil {
		environment = os.Environ()
	}
	cmd.Env = applyEnvironmentOverride(environment, commandEnvironmentOverride{key: ownershipVariable, value: ownership.marker})
	return ownership, nil
}

func newOwnership(ctx context.Context) (*invocationOwnership, error) {
	owner, err := inspectProcess(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("identify harness owner: %w", err)
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	scope, _ := ctx.Value(ownershipScopeKey{}).(*OwnershipScope)
	id := "standalone"
	if scope != nil {
		id = scope.id
	}
	marker := fmt.Sprintf("%s:%d:%s:%x", id, os.Getpid(), owner.birth, nonce)
	return &invocationOwnership{marker: marker}, nil
}

func (ownership *invocationOwnership) cleanup() error {
	if ownership == nil {
		return nil
	}
	// Repeat discovery while terminating: children may fork while their parent
	// handles TERM. Matching is by an exact inherited random marker, not ancestry,
	// executable name, directory or PID alone.
	deadline := time.Now().Add(2 * processTerminationGracePeriod)
	forceAt := time.Now().Add(processTerminationGracePeriod)
	for {
		processes, err := ownedProcesses("", ownership.marker)
		if err != nil {
			return err
		}
		if len(processes) == 0 {
			return nil
		}
		for _, process := range processes {
			if err := signalOwnedProcess(process, time.Now().After(forceAt)); err != nil {
				return err
			}
		}
		if time.Now().After(deadline) {
			return errors.New("tagged processes survived forced termination")
		}
		time.Sleep(processGroupPollInterval)
	}
}

func markCleanupUnresolved(ctx context.Context) {
	if scope, _ := ctx.Value(ownershipScopeKey{}).(*OwnershipScope); scope != nil {
		scope.RecordCleanupFailure()
	}
}

// RecordCleanupFailure prevents this scope from releasing capacity even if
// the original timeout/provider error is later wrapped by another boundary.
func (s *OwnershipScope) RecordCleanupFailure() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.unsafe = true
	s.mu.Unlock()
}

func matchingMarker(environment []byte, scope, exact string) string {
	for _, entry := range strings.Split(string(environment), "\x00") {
		marker, found := strings.CutPrefix(entry, ownershipVariable+"=")
		if !found {
			continue
		}
		parts := strings.Split(marker, ":")
		if len(parts) != 4 || len(parts[3]) != 48 {
			continue
		}
		if _, err := hex.DecodeString(parts[3]); err != nil {
			continue
		}
		if exact != "" && marker == exact || scope != "" && parts[0] == scope {
			return marker
		}
	}
	return ""
}
