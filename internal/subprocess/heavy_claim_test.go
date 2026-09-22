//go:build darwin || linux

package subprocess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
)

func TestHeavyClaimSerializesAndCanceledWaiterDoesNotLaunch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims")
	ctx, first, err := acquireHeavyVerificationAt(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Finish(nil)
	if _, _, err := acquireHeavyVerificationAt(ctx, path); err == nil {
		t.Fatal("recursive claim admitted")
	}
	waitCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, second, err := acquireHeavyVerificationAt(waitCtx, path); !errors.Is(err, context.Canceled) || second != nil {
		t.Fatalf("canceled waiter: %v", err)
	}
	if err := first.Finish(nil); err != nil {
		t.Fatal(err)
	}
	_, next, err := acquireHeavyVerificationAt(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestHeavyClaimUnresolvedSurvivesDescriptorRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims")
	outer := "test-scope:123:456:" + strings.Repeat("aa", 24)
	t.Setenv(ownershipVariable, outer)
	_, claim, err := acquireHeavyVerificationAt(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkHeavyClaimAt(path, "test-scope", ""); err != nil {
		t.Fatalf("healthy heavy check consumed unrelated agent capacity: %v", err)
	}
	if err := claim.Finish(&CleanupError{Err: errors.New("fixture unresolved")}); err == nil {
		t.Fatal("cleanup failure lost")
	}
	if _, _, err := acquireHeavyVerificationAt(t.Context(), path); err == nil || !strings.Contains(err.Error(), "unresolved prior claim") {
		t.Fatalf("stale claim admitted: %v", err)
	}
	if err := checkHeavyClaimAt(path, "", outer); err == nil {
		t.Fatal("outer capacity could be released")
	}
	if err := checkHeavyClaimAt(path, "test-scope", ""); err == nil {
		t.Fatal("restart forgot unresolved claim")
	}
	if err := checkHeavyClaimAt(path, "other", ""); err != nil {
		t.Fatalf("unrelated project quarantined: %v", err)
	}
}

func TestHeavyClaimLockSubstitutionFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims")
	_, claim, err := acquireHeavyVerificationAt(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.close()
	if err := os.Rename(claim.path, claim.path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claim.path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := claim.Finish(nil); err == nil {
		t.Fatal("substituted lock accepted")
	}
	if _, _, err := acquireHeavyVerificationAt(t.Context(), path); err == nil {
		t.Fatal("replacement lock hid active claim")
	}
}

func TestHeavyClaimPreservesOuterAndCleansOnlyInnerMarker(t *testing.T) {
	outerCtx, marker, err := PrepareHarness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = outerCtx
	t.Setenv(ownershipVariable, marker)
	sibling := exec.Command(os.Args[0], "-test.run=^TestOwnershipFixture$")
	sibling.Env = append(os.Environ(), "RUNNER_OWNERSHIP_FIXTURE=1")
	stdout, err := sibling.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	ctx, claim, err := acquireHeavyVerificationAt(t.Context(), filepath.Join(t.TempDir(), "claims"))
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := (OSRunner{}).RunBoundedHeadTailInput(ctx, "/bin/sh", []string{"-c", "printf '%s\\n%s\\n' \"$CORTEXIUM_RUNNER_PROCESS_OWNER\" \"$CORTEXIUM_RUNNER_HEAVY_OWNER\""}, "", time.Second, nil, 4096, "...")
	finishErr := claim.Finish(runErr)
	if runErr != nil || finishErr != nil {
		t.Fatalf("run=%v finish=%v", runErr, finishErr)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 2 || lines[0] != marker || !strings.HasPrefix(lines[1], "heavy:") {
		t.Fatalf("markers not preserved: %q", result.Stdout)
	}
	if err := sibling.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("sibling killed: %v", err)
	}
}

func TestHeavyClaimDirectoryIgnoresHarnessHome(t *testing.T) {
	before, err := heavyClaimDirectory()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	after, err := heavyClaimDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("isolated harness acquired a different host resource")
	}
}

func TestHeavySupervisorCrashDoesNotAdmitOverSurvivingWork(t *testing.T) {
	for _, mode := range []string{"nested", "direct-project"} {
		t.Run(mode, func(t *testing.T) { testHeavySupervisorCrash(t, mode) })
	}
}

func testHeavySupervisorCrash(t *testing.T, mode string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claims")
	readyPath := filepath.Join(t.TempDir(), "ready")
	if err := syscall.Mkfifo(readyPath, 0600); err != nil {
		t.Fatal(err)
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	_, outer, err := PrepareHarness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestHeavySupervisorHelper$")
	helper.Env = append(os.Environ(), "RUNNER_HEAVY_HELPER="+path, "RUNNER_HEAVY_READY="+readyPath, ownershipVariable+"="+outer)
	if mode == "direct-project" {
		helper.Env = replaceEnvironment(helper.Env, ownershipVariable, "")
		helper.Env = append(helper.Env, "RUNNER_HEAVY_PROJECT=direct-project")
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = helper.Process.Kill(); _ = helper.Wait() }()
	line := make(chan string, 1)
	go func() { text, _ := bufio.NewReader(ready).ReadString('\n'); line <- text }()
	var pid int
	select {
	case text := <-line:
		pid, err = strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("detached heavy fixture did not become ready")
	}
	dir, err := securefs.OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := readHeavyClaim(dir)
	_ = dir.Close()
	if err != nil || record == nil {
		t.Fatalf("missing active record: %v", err)
	}
	defer (&invocationOwnership{marker: record.Marker, variable: HeavyOwnershipEnvironmentVariable}).cleanup()
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	if _, _, err := acquireHeavyVerificationAt(t.Context(), path); err == nil {
		t.Fatal("free flock hid surviving work")
	}
	if _, err := inspectProcess(pid); err != nil {
		t.Fatalf("admission killed unrelated-to-admission work: %v", err)
	}
	if mode == "nested" {
		if err := checkHeavyClaimAt(path, "", outer); err == nil {
			t.Fatal("outer assignment could report resolved cleanup")
		}
	} else {
		replacement := NewOwnershipScope("direct-project")
		if record.OuterMarker != "" || record.ProjectScope != replacement.id {
			t.Fatal("direct claim lost context ownership")
		}
		if err := checkHeavyClaimAt(path, replacement.id, ""); err == nil {
			t.Fatal("replacement Project admitted over abandoned verification")
		}
	}
	preview, err := recoverHeavyVerificationAt(t.Context(), path, "", false)
	if err != nil || preview.Recoverable {
		t.Fatalf("live descendants recoverable: %+v %v", preview, err)
	}
	// The enclosing harness can still discover and remove every descendant:
	// the heavy marker did not replace its original marker.
	cleanupOwner := &invocationOwnership{marker: outer}
	if mode == "direct-project" {
		cleanupOwner = &invocationOwnership{marker: record.Marker, variable: HeavyOwnershipEnvironmentVariable}
	}
	if err := cleanupOwner.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectProcess(pid); !processDisappeared(err) {
		t.Fatalf("outer cleanup lost nested descendant: %v", err)
	}
	preview, err = recoverHeavyVerificationAt(t.Context(), path, "", false)
	if err != nil || !preview.Recoverable {
		t.Fatalf("absent supported work not recoverable: %+v %v", preview, err)
	}
	if _, err := recoverHeavyVerificationAt(t.Context(), path, "wrong", true); err == nil {
		t.Fatal("recovery accepted stale preview")
	}
	recovered, err := recoverHeavyVerificationAt(t.Context(), path, preview.Token, true)
	if err != nil || !recovered.Cleared {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	_, claim, err := acquireHeavyVerificationAt(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestHeavySupervisorHelper(t *testing.T) {
	path := os.Getenv("RUNNER_HEAVY_HELPER")
	if path == "" {
		return
	}
	var ctx context.Context = context.Background()
	if project := os.Getenv("RUNNER_HEAVY_PROJECT"); project != "" {
		ctx = WithOwnershipScope(ctx, NewOwnershipScope(project))
	}
	ctx, claim, err := acquireHeavyVerificationAt(ctx, path)
	if err != nil {
		panic(err)
	}
	_, err = (OSRunner{}).RunBoundedHeadTailInput(ctx, "env", []string{
		"RUNNER_SUBPROCESS_HELPER_ROLE=leader", "RUNNER_SUBPROCESS_HELPER_BEHAVIOR=block", "RUNNER_SUBPROCESS_HELPER_DETACHED=true",
		"RUNNER_SUBPROCESS_HELPER_READY=" + os.Getenv("RUNNER_HEAVY_READY"), os.Args[0], "-test.run=^TestProcessTreeHelper$",
	}, "", time.Minute, nil, 4096, "...")
	if finishErr := claim.Finish(err); finishErr != nil {
		fmt.Fprintln(os.Stderr, finishErr)
	}
}

type cancelAtGrantContext struct {
	context.Context
	checks atomic.Int32
}

func (c *cancelAtGrantContext) Err() error {
	if c.checks.Add(1) >= 2 {
		return context.Canceled
	}
	return nil
}

func TestHeavyClaimCancellationAfterLockBeforeGrant(t *testing.T) {
	root := filepath.Join(t.TempDir(), "claims")
	ctx := &cancelAtGrantContext{Context: t.Context()}
	if _, claim, err := acquireHeavyVerificationAt(ctx, root); !errors.Is(err, context.Canceled) || claim != nil {
		t.Fatalf("late cancellation granted: %v", err)
	}
	dir, err := securefs.OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	claim, _, err := readHeavyClaim(dir)
	if err != nil || claim != nil {
		t.Fatalf("canceled grant left active work: %+v %v", claim, err)
	}
	marker := filepath.Join(t.TempDir(), "must-not-launch")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (OSRunner{}).RunBoundedHeadTailInput(canceled, "/bin/sh", []string{"-c", "touch \"$1\"", "fixture", marker}, "", time.Second, nil, 100, "..."); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled command launched")
	}
}

type notifyHeavyWaitContext struct {
	context.Context
	checks int
}

func (c *notifyHeavyWaitContext) Err() error {
	c.checks++
	if c.checks == 2 {
		fmt.Println("waiting")
	}
	return c.Context.Err()
}

func TestHeavyClaimsWaitAcrossProcessesAndEntrypoints(t *testing.T) {
	root := filepath.Join(t.TempDir(), "claims")
	start := func(waiter bool) (*exec.Cmd, *bufio.Reader, *os.File) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestHeavyWaitHelper$")
		cmd.Env = append(os.Environ(), "RUNNER_HEAVY_WAIT_ROOT="+root, "RUNNER_HEAVY_WAITER="+strconv.FormatBool(waiter))
		read, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		inputR, inputW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdin = inputR
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = inputR.Close()
		t.Cleanup(func() { _ = inputW.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd, bufio.NewReader(read), inputW
	}
	readLine := func(reader *bufio.Reader) string {
		t.Helper()
		ch := make(chan string, 1)
		go func() { line, _ := reader.ReadString('\n'); ch <- strings.TrimSpace(line) }()
		select {
		case line := <-ch:
			return line
		case <-time.After(10 * time.Second):
			t.Fatal("claim fixture failed to synchronize")
			return ""
		}
	}
	first, firstOut, firstIn := start(false)
	if line := readLine(firstOut); line != "acquired" {
		t.Fatalf("first: %q", line)
	}
	second, secondOut, secondIn := start(true)
	if line := readLine(secondOut); line != "waiting" {
		t.Fatalf("second did not wait on shared resource: %q", line)
	}
	if _, err := firstIn.Write([]byte("finish\n")); err != nil {
		t.Fatal(err)
	}
	if line := readLine(firstOut); line != "finished" {
		t.Fatalf("first cleanup: %q", line)
	}
	if err := first.Wait(); err != nil {
		t.Fatal(err)
	}
	if line := readLine(secondOut); line != "acquired" {
		t.Fatalf("second not admitted after cleanup: %q", line)
	}
	if _, err := secondIn.Write([]byte("finish\n")); err != nil {
		t.Fatal(err)
	}
	if line := readLine(secondOut); line != "finished" {
		t.Fatalf("second cleanup: %q", line)
	}
	if err := second.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestHeavyWaitHelper(t *testing.T) {
	root := os.Getenv("RUNNER_HEAVY_WAIT_ROOT")
	if root == "" {
		return
	}
	var ctx context.Context = context.Background()
	if os.Getenv("RUNNER_HEAVY_WAITER") == "true" {
		ctx = &notifyHeavyWaitContext{Context: ctx}
	}
	_, claim, err := acquireHeavyVerificationAt(ctx, root)
	if err != nil {
		panic(err)
	}
	fmt.Println("acquired")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		panic(err)
	}
	if err := claim.Finish(nil); err != nil {
		panic(err)
	}
	fmt.Println("finished")
}
