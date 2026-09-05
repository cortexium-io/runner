package github

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
)

func TestProjectProcessLockAllowsOnlyOneLocalRunnerPerProject(t *testing.T) {
	project := config.GitHubProjectConfig{Owner: "Example", Number: 7}
	lockDir := t.TempDir()
	first, err := acquireProjectProcessLockAt(lockDir, project)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer first.Release()
	if first.Path == "" {
		t.Fatal("lock path was not reported")
	}
	second, err := acquireProjectProcessLockAt(lockDir, project)
	if err == nil || second != nil {
		t.Fatalf("second lock unexpectedly succeeded: lock=%#v error=%v", second, err)
	}
	for _, expected := range []string{"Example/7", fmt.Sprintf("PID %d", os.Getpid()), first.Path} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("lock conflict missing %q: %v", expected, err)
		}
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	third, err := acquireProjectProcessLockAt(lockDir, project)
	if err != nil {
		t.Fatalf("reacquire released lock: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("release reacquired lock: %v", err)
	}
}

func TestPlanningAndExecutionLocksDoNotOwnWorkerStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	project := config.GitHubProjectConfig{Owner: "example", Number: 42}
	planner, err := AcquirePlanningLock(project)
	if err != nil {
		t.Fatal(err)
	}
	defer planner.Release()
	if _, active, err := InspectProcessState(project); err != nil || active {
		t.Fatalf("standalone planning impersonates a worker: active=%t err=%v", active, err)
	}
	worker, err := AcquireProcessLock(project)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Release()
	before, _, _ := InspectProcessState(project)
	first, err := AcquireExecutionSlot(project, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := AcquireExecutionSlot(project, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if extra, err := AcquireExecutionSlot(project, 2); !errors.Is(err, ErrProjectLockBusy) {
		_ = extra.Release()
		t.Fatalf("capacity exceeded: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireExecutionSlot(project, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Release()
	if err := planner.Release(); err != nil {
		t.Fatal(err)
	}
	after, active, err := InspectProcessState(project)
	if err != nil || !active || after != before {
		t.Fatalf("operation locks changed worker state: before=%+v after=%+v active=%t err=%v", before, after, active, err)
	}
}

func TestProjectProcessLockPublishesAndRemovesRuntimeState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	project := config.GitHubProjectConfig{Owner: "example", Number: 42}
	lock, err := AcquireProcessLock(project)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	started := time.Now().UTC().Truncate(time.Second)
	next := started.Add(30 * time.Second)
	if err := lock.UpdateRuntime(RuntimeState{Owner: project.Owner, Project: project.Number, StartedAt: started, LastPollAt: started, NextPollAt: next}); err != nil {
		t.Fatalf("update runtime state: %v", err)
	}
	state, active, err := InspectProcessState(project)
	if err != nil || !active || state.PID != os.Getpid() || !state.NextPollAt.Equal(next) {
		t.Fatalf("inspect active state: active=%t state=%#v err=%v", active, state, err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	_, active, err = InspectProcessState(project)
	if err != nil || active {
		t.Fatalf("released process still appears active: active=%t err=%v", active, err)
	}
}

func TestProjectProcessLocksDifferentProjectsIndependently(t *testing.T) {
	lockDir := t.TempDir()
	first, err := acquireProjectProcessLockAt(lockDir, config.GitHubProjectConfig{Owner: "example", Number: 7})
	if err != nil {
		t.Fatalf("acquire first Project: %v", err)
	}
	defer first.Release()
	second, err := acquireProjectProcessLockAt(lockDir, config.GitHubProjectConfig{Owner: "example", Number: 8})
	if err != nil {
		t.Fatalf("different Project was blocked: %v", err)
	}
	defer second.Release()
	if first.Path == second.Path {
		t.Fatalf("different Projects shared lock path %s", first.Path)
	}
}
