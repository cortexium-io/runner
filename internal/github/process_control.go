package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/securefs"
)

const maxProcessRecordBytes = 16 * 1024

// EnableGracefulStop exposes control only after a continuous worker is ready
// to honor it. Standalone planners and run --once retain their bounded lifetime.
func (l *ProcessLock) EnableGracefulStop(executable, launchdService string) error {
	if l == nil || l.file == nil || l.released || l.statusPath == "" {
		return errors.New("worker process lock is not active")
	}
	metadata := readProjectLockMetadata(l.file)
	l.controlState = RuntimeState{PID: metadata.PID, Owner: metadata.Owner, Project: metadata.Project,
		StartedAt: metadata.StartedAt, StopSupported: true, Executable: executable, LaunchdService: launchdService}
	return l.UpdateRuntime(l.controlState)
}

func (l *ProcessLock) StopRequested() (bool, error) {
	var request RuntimeState
	exists, err := readProcessRecord(stopRequestPath(l.Path, l.controlState), &request)
	if err != nil || !exists {
		return false, err
	}
	if !sameProcess(request, l.controlState) {
		return false, errors.New("stop request does not match this worker incarnation")
	}
	return true, nil
}

// RunningProcesses discovers this user's workers using the existing local
// locks. It never searches by executable name or sends a signal to a stored PID.
func RunningProcesses() ([]RuntimeState, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(cache, "cortexium-runner", "locks")
	if err := securefs.ValidatePrivateDir(directory); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	handle, err := os.Open(directory)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	entries, err := handle.ReadDir(10001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 10000 {
		return nil, errors.New("too many local Runner lock entries")
	}
	var result []RuntimeState
	var failures []error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "project-") || !strings.HasSuffix(name, ".lock") || len(name) != len("project-")+32+len(".lock") {
			continue
		}
		var metadata projectLockMetadata
		exists, err := readProcessRecord(filepath.Join(directory, name), &metadata)
		if err != nil {
			failures = append(failures, fmt.Errorf("inspect %s: %w", name, err))
			continue
		}
		if !exists {
			continue
		}
		if metadata.PID <= 0 || metadata.StartedAt.IsZero() || metadata.Owner == "" || metadata.Project <= 0 || name != projectLockFileName(metadata.Owner, metadata.Project) {
			failures = append(failures, fmt.Errorf("invalid local worker identity in %s", name))
			continue
		}
		state, active, err := InspectProcessState(config.GitHubProjectConfig{Owner: metadata.Owner, Number: metadata.Project})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if active {
			identity := RuntimeState{PID: metadata.PID, Owner: metadata.Owner, Project: metadata.Project, StartedAt: metadata.StartedAt}
			if !sameProcess(state, identity) {
				failures = append(failures, fmt.Errorf("worker runtime identity disagrees with held lock %s", name))
				continue
			}
			result = append(result, state)
		}
	}
	return result, errors.Join(failures...)
}

// ProcessStillRunning checks the held project lock and exact incarnation; PID
// reuse, a stale status file, or a replacement worker is not the selected worker.
func ProcessStillRunning(expected RuntimeState) (bool, RuntimeState, error) {
	state, active, err := InspectProcessState(config.GitHubProjectConfig{Owner: expected.Owner, Number: expected.Project})
	if err != nil || !active {
		return active, state, err
	}
	return sameProcess(state, expected), state, nil
}

func RequestProcessStop(state RuntimeState) error {
	if !state.StopSupported {
		return errors.New("worker does not support graceful stop; upgrade it at an idle boundary first")
	}
	active, _, err := ProcessStillRunning(state)
	if err != nil || !active {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	lock := filepath.Join(cache, "cortexium-runner", "locks", projectLockFileName(state.Owner, state.Project))
	path := stopRequestPath(lock, state)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := securefs.ValidatePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	directory, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	// Publish a complete request atomically: the worker must never observe
	// an empty or partially written control record as a local-control failure.
	if err = directory.ReplaceFile(filepath.Base(path), data, 0o600, securefs.FileState{}); err == nil {
		return nil
	}
	var previous RuntimeState
	if exists, readErr := readProcessRecord(path, &previous); readErr == nil && exists && sameProcess(previous, state) {
		return nil
	}
	return fmt.Errorf("write local stop request: %w", err)
}

func stopRequestPath(lock string, state RuntimeState) string {
	return fmt.Sprintf("%s.stop-%d-%d.json", strings.TrimSuffix(lock, ".lock"), state.PID, state.StartedAt.UnixNano())
}

func sameProcess(a, b RuntimeState) bool {
	return a.PID == b.PID && a.Owner == b.Owner && a.Project == b.Project && a.StartedAt.Equal(b.StartedAt)
}

func readProcessRecord(path string, value any) (bool, error) {
	if err := securefs.ValidatePrivateDir(filepath.Dir(path)); err != nil {
		return false, err
	}
	var data []byte
	var mode os.FileMode
	var state securefs.FileState
	var err error
	for range 3 {
		data, mode, state, err = securefs.ReadFile(path, maxProcessRecordBytes)
		if !errors.Is(err, securefs.ErrChanged) {
			break
		}
	}
	if err != nil || !state.Exists {
		return state.Exists, err
	}
	if mode != 0o600 {
		return true, errors.New("local process record must have mode 0600")
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return true, err
	}
	return true, json.Unmarshal(data, value)
}
