package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
)

type ProcessLock struct {
	file       *os.File
	Path       string
	StartedAt  time.Time
	statusPath string
	released   bool
}

type RuntimeState struct {
	PID        int       `json:"pid"`
	Owner      string    `json:"owner"`
	Project    int       `json:"project"`
	StartedAt  time.Time `json:"started_at"`
	LastPollAt time.Time `json:"last_poll_at,omitempty"`
	NextPollAt time.Time `json:"next_poll_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

type projectLockMetadata struct {
	PID       int       `json:"pid"`
	Owner     string    `json:"owner"`
	Project   int       `json:"project"`
	StartedAt time.Time `json:"started_at"`
}

func AcquireProcessLock(project config.GitHubProjectConfig) (*ProcessLock, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user cache directory for Runner lock: %w", err)
	}
	return acquireProjectProcessLockAt(filepath.Join(cacheDir, "cortexium-runner", "locks"), project)
}

func acquireProjectProcessLockAt(lockDir string, project config.GitHubProjectConfig) (*ProcessLock, error) {
	owner := strings.TrimSpace(project.Owner)
	if owner == "" || project.Number <= 0 {
		return nil, errors.New("GitHub Project owner and positive number are required for the Runner lock")
	}
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Runner lock directory: %w", err)
	}
	path := filepath.Join(lockDir, projectLockFileName(owner, project.Number))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Runner lock %s: %w", path, err)
	}
	locked, err := tryExclusiveFileLock(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("acquire Runner lock %s: %w", path, err)
	}
	if !locked {
		metadata := readProjectLockMetadata(file)
		file.Close()
		detail := ""
		if metadata.PID > 0 {
			detail = fmt.Sprintf(" (PID %d, started %s)", metadata.PID, metadata.StartedAt.Format(time.RFC3339))
		}
		return nil, fmt.Errorf("another Runner process is already active for GitHub Project %s/%d%s; lock: %s", owner, project.Number, detail, path)
	}
	metadata := projectLockMetadata{PID: os.Getpid(), Owner: owner, Project: project.Number, StartedAt: time.Now().UTC()}
	if err := writeProjectLockMetadata(file, metadata); err != nil {
		_ = unlockFile(file)
		file.Close()
		return nil, fmt.Errorf("record Runner lock metadata: %w", err)
	}
	lock := &ProcessLock{file: file, Path: path, StartedAt: metadata.StartedAt, statusPath: projectStatusPath(path)}
	if err := lock.UpdateRuntime(RuntimeState{PID: metadata.PID, Owner: owner, Project: project.Number, StartedAt: metadata.StartedAt}); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, err
	}
	return lock, nil
}

func (l *ProcessLock) UpdateRuntime(state RuntimeState) error {
	if l == nil || l.file == nil || l.released {
		return errors.New("Runner process lock is not active")
	}
	if state.PID == 0 {
		state.PID = os.Getpid()
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Runner runtime state: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(l.statusPath), ".runner-status-*.tmp")
	if err != nil {
		return fmt.Errorf("create Runner runtime state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, l.statusPath); err != nil {
		return fmt.Errorf("activate Runner runtime state: %w", err)
	}
	return nil
}

func (l *ProcessLock) Release() error {
	if l == nil || l.file == nil || l.released {
		return nil
	}
	l.released = true
	removeErr := os.Remove(l.statusPath)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("release Runner lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Runner lock: %w", closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove Runner runtime state: %w", removeErr)
	}
	return nil
}

func InspectProcessState(project config.GitHubProjectConfig) (RuntimeState, bool, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return RuntimeState{}, false, fmt.Errorf("resolve user cache directory for Runner status: %w", err)
	}
	path := filepath.Join(cacheDir, "cortexium-runner", "locks", projectLockFileName(project.Owner, project.Number))
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return RuntimeState{}, false, nil
	}
	if err != nil {
		return RuntimeState{}, false, fmt.Errorf("open Runner lock for status: %w", err)
	}
	defer file.Close()
	locked, err := tryExclusiveFileLock(file)
	if err != nil {
		return RuntimeState{}, false, err
	}
	if locked {
		_ = unlockFile(file)
		return RuntimeState{}, false, nil
	}
	var state RuntimeState
	data, readErr := os.ReadFile(projectStatusPath(path))
	if readErr == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return RuntimeState{}, true, fmt.Errorf("decode Runner runtime state: %w", err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return RuntimeState{}, true, fmt.Errorf("read Runner runtime state: %w", readErr)
	} else {
		metadata := readProjectLockMetadata(file)
		state = RuntimeState{PID: metadata.PID, Owner: metadata.Owner, Project: metadata.Project, StartedAt: metadata.StartedAt}
	}
	return state, true, nil
}

func projectStatusPath(lockPath string) string {
	return strings.TrimSuffix(lockPath, filepath.Ext(lockPath)) + ".status.json"
}

func projectLockFileName(owner string, number int) string {
	identity := strings.ToLower(strings.TrimSpace(owner)) + "/" + fmt.Sprint(number)
	digest := sha256.Sum256([]byte(identity))
	return "project-" + hex.EncodeToString(digest[:16]) + ".lock"
}

func writeProjectLockMetadata(file *os.File, metadata projectLockMetadata) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		return err
	}
	return file.Sync()
}

func readProjectLockMetadata(file *os.File) projectLockMetadata {
	if _, err := file.Seek(0, 0); err != nil {
		return projectLockMetadata{}
	}
	var metadata projectLockMetadata
	_ = json.NewDecoder(file).Decode(&metadata)
	return metadata
}
