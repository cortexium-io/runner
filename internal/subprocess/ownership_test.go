//go:build darwin || linux

package subprocess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDetachedDescendantCleanup(t *testing.T) {
	for _, behavior := range []string{"success", "failure", "cancel", "timeout"} {
		t.Run(behavior, func(t *testing.T) {
			readyPath := t.TempDir() + "/ready"
			if err := syscall.Mkfifo(readyPath, 0600); err != nil {
				t.Fatal(err)
			}
			ready, err := os.OpenFile(readyPath, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer ready.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var runCtx context.Context = ctx
			exit := behavior
			trigger := make(chan struct{})
			if behavior == "timeout" {
				runCtx = triggeredTimeoutContext{Context: ctx, done: trigger}
			}
			if behavior == "cancel" || behavior == "timeout" {
				exit = "block"
			}
			runCtx, marker, err := PrepareHarness(runCtx)
			if err != nil {
				t.Fatal(err)
			}
			defer (&invocationOwnership{marker: marker}).cleanup()
			// An unrelated process must survive even though it runs the same executable.
			canary := exec.Command("sleep", "60")
			if err := canary.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = canary.Process.Kill(); _ = canary.Wait() }()
			done := make(chan error, 1)
			go func() {
				_, err := OSRunner{}.RunBoundedHeadTailInput(runCtx, "env", []string{
					"RUNNER_SUBPROCESS_HELPER_ROLE=leader", "RUNNER_SUBPROCESS_HELPER_BEHAVIOR=" + exit,
					"RUNNER_SUBPROCESS_HELPER_READY=" + readyPath, "RUNNER_SUBPROCESS_HELPER_DETACHED=true",
					os.Args[0], "-test.run=^TestProcessTreeHelper$",
				}, "", time.Minute, nil, 1024, "[..]")
				done <- err
			}()
			readiness := make(chan string, 1)
			go func() {
				line, _ := bufio.NewReader(ready).ReadString('\n')
				readiness <- line
			}()
			var line string
			select {
			case line = <-readiness:
			case <-time.After(10 * time.Second):
				t.Fatal("fixture never became ready")
			}
			pid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatal(err)
			}
			if behavior == "cancel" {
				cancel()
			}
			if behavior == "timeout" {
				close(trigger)
			}
			select {
			case err := <-done:
				if behavior == "success" && err != nil {
					t.Fatal(err)
				}
				if behavior == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
				if behavior == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("unbounded cleanup")
			}
			if _, err := inspectProcess(pid); !errors.Is(err, syscall.ESRCH) && !os.IsNotExist(err) {
				t.Fatalf("detached descendant %d survived: %v", pid, err)
			}
			if err := canary.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unrelated process killed: %v", err)
			}
		})
	}
}

func TestOwnershipAdmissionDetectsOrphanWithoutKillingIt(t *testing.T) {
	scope := NewOwnershipScope(t.Name())
	// The process is alive but its recorded owner identity is not. Readiness is
	// synchronized through stdout so discovery cannot race exec/environment setup.
	marker := scope.id + ":1:not-the-owner-birth:" + strings.Repeat("ab", 24)
	child := exec.Command(os.Args[0], "-test.run=^TestOwnershipFixture$")
	child.Env = append(os.Environ(), "RUNNER_OWNERSHIP_FIXTURE=1", ownershipVariable+"="+marker)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := scope.CheckAdmission(); err == nil || !strings.Contains(err.Error(), "previous Runner") {
		t.Fatalf("orphan admitted: %v", err)
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("admission inspection killed work: %v", err)
	}
	if err := NewOwnershipScope("another-project").CheckAdmission(); err != nil {
		t.Fatalf("unrelated Project blocked: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if err := scope.CheckAdmission(); err != nil {
		t.Fatalf("confirmed cleanup did not release admission: %v", err)
	}
}

func TestOwnedSignalRejectsReusedIdentityAndForeignMarker(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestOwnershipFixture$")
	child.Env = append(os.Environ(), "RUNNER_OWNERSHIP_FIXTURE=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	process, err := inspectProcess(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	stale := process
	stale.birth = "different-start"
	if err := signalOwnedProcess(stale, true); err != nil {
		t.Fatal(err)
	}
	process.marker = "foreign-marker"
	if err := signalOwnedProcess(process, true); err == nil {
		t.Fatal("foreign process accepted as owned")
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated process killed: %v", err)
	}
}

func TestCleanupFailureKeepsAdmissionClosed(t *testing.T) {
	scope := NewOwnershipScope(t.Name())
	ctx := WithOwnershipScope(t.Context(), scope)
	markCleanupUnresolved(ctx)
	if !scope.Unresolved() {
		t.Fatal("cleanup failure lost")
	}
	if err := scope.CheckAdmission(); err == nil {
		t.Fatal("unresolved cleanup released admission")
	}
}

func TestOwnershipFixture(t *testing.T) {
	if os.Getenv("RUNNER_OWNERSHIP_FIXTURE") != "1" {
		return
	}
	fmt.Println("ready")
	time.Sleep(time.Hour)
}

func TestExitedProcessIdentityIsAbsent(t *testing.T) {
	child := exec.Command("true")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectProcess(pid); !errors.Is(err, syscall.ESRCH) && !os.IsNotExist(err) {
		t.Fatalf("exited process is not an inspection error: %v", err)
	}
}

func TestProcessDisappearanceIncludesProcReadRace(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		gone bool
	}{
		{"path already absent", &os.PathError{Op: "open", Path: "/proc/123/stat", Err: syscall.ENOENT}, true},
		{"exit during read", &os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.ESRCH}, true},
		{"wrapped exit during read", fmt.Errorf("inspect: %w", &os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.ESRCH}), true},
		{"permission denied", syscall.EPERM, false},
		{"unreadable identity", syscall.EIO, false},
		{"still present", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := processDisappeared(test.err); got != test.gone {
				t.Fatalf("process disappearance = %v, want %v for %v", got, test.gone, test.err)
			}
		})
	}
}
