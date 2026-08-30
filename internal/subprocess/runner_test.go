//go:build !windows

package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunBoundedCapsBothStreams(t *testing.T) {
	result, err := RunBoundedInput(t.Context(), OSRunner{}, "sh", []string{"-c", "printf '1234567890'; printf 'abcdefghij' >&2"}, "", 5*time.Second, strings.NewReader(""), 8, "[..]")
	if err != nil {
		t.Fatalf("run bounded command: %v", err)
	}
	if result.Stdout != "1234[..]" || result.Stderr != "abcd[..]" {
		t.Fatalf("unexpected bounded result: %#v", result)
	}
}

func TestRunBoundedInputPassesStdinWithoutArgFallback(t *testing.T) {
	result, err := RunBoundedInput(t.Context(), OSRunner{}, "sh", []string{"-c", "cat"}, "", 5*time.Second, strings.NewReader("stdin-payload"), 64, "[..]")
	if err != nil {
		t.Fatalf("run bounded command with input: %v", err)
	}
	if result.Stdout != "stdin-payload" {
		t.Fatalf("unexpected stdout: %#v", result)
	}
}

func TestEnvironmentVariableReachesOrdinaryCommandsWithoutWideningPrivilegedEnvironment(t *testing.T) {
	t.Setenv("RUNNER_TEST_ENVIRONMENT", "inherited")
	ctx, err := WithEnvironmentVariable(t.Context(), "RUNNER_TEST_ENVIRONMENT", "overridden")
	if err != nil {
		t.Fatal(err)
	}
	result, err := OSRunner{}.Run(ctx, "sh", []string{"-c", `printf '%s' "$RUNNER_TEST_ENVIRONMENT"`}, "", 5*time.Second)
	if err != nil || result.Stdout != "overridden" {
		t.Fatalf("ordinary command environment = %#v, %v", result, err)
	}

	privileged := context.WithValue(ctx, commandEnvironmentContextKey{}, []string{"PATH=/trusted"})
	if got := strings.Join(commandEnvironment(privileged), "\n"); got != "PATH=/trusted" {
		t.Fatalf("privileged environment accepted ordinary overrides: %q", got)
	}
}

func TestEnvironmentVariableRejectsInvalidNamesAndValues(t *testing.T) {
	for _, candidate := range [][2]string{
		{"", "value"},
		{"BAD=NAME", "value"},
		{"GOOD_NAME", "bad\x00value"},
	} {
		if _, err := WithEnvironmentVariable(t.Context(), candidate[0], candidate[1]); err == nil {
			t.Fatalf("accepted invalid environment variable %#v", candidate)
		}
	}
}

type nonInputRunner struct{}

func (nonInputRunner) Run(_ context.Context, _ string, _ []string, _ string, _ time.Duration) (Result, error) {
	return Result{}, nil
}

func TestRunBoundedInputFailsClosedWhenRunnerCannotProvideSafeTransport(t *testing.T) {
	_, err := RunBoundedInput(t.Context(), nonInputRunner{}, "codex", []string{"exec"}, "", 5*time.Second, bytes.NewBufferString("secret"), 64, "[..]")
	if err == nil || !strings.Contains(err.Error(), "safe stdin transport") {
		t.Fatalf("expected safe-transport failure, got %v", err)
	}
}

func TestRunFailClosedAcceptsExactLimits(t *testing.T) {
	result, err := RunFailClosed(t.Context(), OSRunner{}, "sh", []string{"-c", "printf '12345678'; printf 'abcd' >&2"}, "", 5*time.Second, 8, 4)
	if err != nil {
		t.Fatalf("run exact-limit command: %v", err)
	}
	if result.Stdout != "12345678" || result.Stderr != "abcd" {
		t.Fatalf("unexpected exact-limit result: %#v", result)
	}
}

func TestRunFailClosedRejectsLimitPlusOneWithoutRetainingMachineOutput(t *testing.T) {
	result, err := RunFailClosed(t.Context(), OSRunner{}, "sh", []string{"-c", "printf 'secret123'; printf 'diagnostic' >&2"}, "", 5*time.Second, 8, 4)
	var limitErr *CaptureLimitError
	if !errors.As(err, &limitErr) || limitErr.Stream != "stdout" || limitErr.Limit != 8 {
		t.Fatalf("expected stdout capture error, got %v", err)
	}
	if result.Stdout != "" || len(result.Stderr) > 4 {
		t.Fatalf("overflow retained unsafe output: %#v", result)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("overflow diagnostic copied command output: %v", err)
	}
}

func TestRunFailClosedRetainsOnlyBoundedStderrOnLimitPlusOne(t *testing.T) {
	result, err := RunFailClosed(t.Context(), OSRunner{}, "sh", []string{"-c", "printf '12345' >&2"}, "", 5*time.Second, 8, 4)
	var limitErr *CaptureLimitError
	if !errors.As(err, &limitErr) || limitErr.Stream != "stderr" || limitErr.Limit != 4 {
		t.Fatalf("expected stderr capture error, got %v", err)
	}
	if result.Stdout != "" || result.Stderr != "1234" {
		t.Fatalf("stderr overflow retained more than its diagnostic limit: %#v", result)
	}
	if strings.Contains(err.Error(), "12345") {
		t.Fatalf("stderr overflow diagnostic copied command output: %v", err)
	}
}

func TestPrivilegedGitProfileScrubsInheritedSelectorsAndPinsRepositoryPaths(t *testing.T) {
	root := t.TempDir()
	profile, err := NewPrivilegedGitProfile(
		filepath.Join(root, "worktree"), filepath.Join(root, "common", "worktrees", "task"), filepath.Join(root, "common"),
		filepath.Join(root, "common", "worktrees", "task", "index"), filepath.Join(root, "common", "objects"),
	)
	if err != nil {
		t.Fatal(err)
	}
	environment := profile.environment([]string{
		"PATH=/usr/bin", "LANG=attacker", "LC_ALL=attacker", "HOME=/attacker", "XDG_CONFIG_HOME=/attacker/config", "GNUPGHOME=/attacker/gnupg",
		"LD_PRELOAD=/attacker/library", "DYLD_INSERT_LIBRARIES=/attacker/library", "EMAIL=attacker@example.invalid",
		"GIT_DIR=/attacker/repo", "GIT_INDEX_FILE=/attacker/index", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/attacker/hooks",
		"GH_CONFIG_DIR=/attacker/gh", "GH_TOKEN=attacker-token", "GITHUB_TOKEN=attacker-token",
	}, false)
	joined := strings.Join(environment, "\n")
	for _, forbidden := range []string{"/attacker", "GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("privileged environment retained %q:\n%s", forbidden, joined)
		}
	}
	for _, required := range []string{
		"PATH=/usr/bin", "LANG=C", "LC_ALL=C", "GIT_DIR=" + profile.GitDirectory, "GIT_COMMON_DIR=" + profile.CommonDirectory,
		"GIT_WORK_TREE=" + profile.WorkTree, "GIT_INDEX_FILE=" + profile.IndexFile, "GIT_OBJECT_DIRECTORY=" + profile.ObjectDirectory,
		"GIT_CONFIG=" + os.DevNull, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_NO_REPLACE_OBJECTS=1",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("privileged environment omitted %q:\n%s", required, joined)
		}
	}
}

func TestPrivilegedGitProfileRejectsCallerConfigAndRepositoryOverrides(t *testing.T) {
	for _, args := range [][]string{
		{"-c", "core.hooksPath=/attacker", "status"},
		{"-ccore.hooksPath=/attacker", "status"},
		{"-C/attacker", "status"},
		{"--git-dir=/attacker", "status"},
		{"--work-tree", "/attacker", "status"},
		{"--config-env=core.hooksPath=HOOK_PATH", "status"},
	} {
		if err := rejectPrivilegedGitSelectors(args); err == nil {
			t.Fatalf("privileged Git accepted selector override %#v", args)
		}
	}
}

type delegatingGitRunner struct{}

func (delegatingGitRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (Result, error) {
	return OSRunner{}.Run(ctx, command, args, dir, timeout)
}

type recordingGitRunner struct {
	command     string
	args        []string
	environment []string
}

func (r *recordingGitRunner) Run(ctx context.Context, command string, args []string, _ string, _ time.Duration) (Result, error) {
	r.command = command
	r.args = append([]string(nil), args...)
	r.environment = append([]string(nil), commandEnvironment(ctx)...)
	return Result{}, nil
}

func TestPrivilegedNetworkGitPinsLiteralURLAndRejectsOtherOperations(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", "/trusted/gh")
	t.Setenv("GH_TOKEN", "must-not-reach-git")
	t.Setenv("GITHUB_TOKEN", "must-not-reach-git")
	root := t.TempDir()
	profile, err := NewPrivilegedGitProfile(
		filepath.Join(root, "worktree"), filepath.Join(root, "common", "worktrees", "task"), filepath.Join(root, "common"),
		filepath.Join(root, "common", "worktrees", "task", "index"), filepath.Join(root, "common", "objects"),
	)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingGitRunner{}
	url := "https://github.com/owner/repo.git"
	if _, err := RunPrivilegedGitNetwork(t.Context(), runner, profile, []string{"push", url, "abc:refs/heads/task"}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.args, "\n")
	for _, required := range []string{
		"protocol.allow=never", "protocol.https.allow=always", "credential.helper=", "credential.https://github.com.helper=!gh auth git-credential",
		"url." + url + ".insteadOf=" + url, "http.proxy=", "http." + url + ".proxy=", "http.extraHeader=", "http." + url + ".extraHeader=",
		"http.followRedirects=false", "http." + url + ".followRedirects=false", url,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("privileged network Git omitted %q:\n%s", required, joined)
		}
	}
	for _, args := range [][]string{{"merge", url}, {"push", "ssh://github.com/owner/repo.git", "abc:refs/heads/task"}, {"fetch", url, "https://github.com/other/repo.git"}} {
		if _, err := RunPrivilegedGitNetwork(t.Context(), runner, profile, args, 5*time.Second); err == nil {
			t.Fatalf("privileged network Git accepted %#v", args)
		}
	}
	environment := strings.Join(runner.environment, "\n")
	for _, required := range []string{"GH_CONFIG_DIR=/trusted/gh", "GH_PROMPT_DISABLED=1", "GH_TELEMETRY=false"} {
		if !strings.Contains(environment, required) {
			t.Fatalf("privileged network Git omitted %q:\n%s", required, environment)
		}
	}
	for _, forbidden := range []string{"GH_TOKEN=", "GITHUB_TOKEN=", "HOME=", "XDG_CONFIG_HOME="} {
		if strings.Contains(environment, forbidden) {
			t.Fatalf("privileged network Git retained %q:\n%s", forbidden, environment)
		}
	}
}

func TestGitHubCLIConfigDirectoryUsesDocumentedAbsolutePrecedence(t *testing.T) {
	tests := []struct {
		name        string
		environment []string
		want        string
	}{
		{name: "explicit", environment: []string{"GH_CONFIG_DIR=/operator/gh", "XDG_CONFIG_HOME=/operator/xdg", "HOME=/operator"}, want: "/operator/gh"},
		{name: "xdg", environment: []string{"XDG_CONFIG_HOME=/operator/xdg", "HOME=/operator"}, want: "/operator/xdg/gh"},
		{name: "home", environment: []string{"HOME=/operator"}, want: "/operator/.config/gh"},
		{name: "relative values fail closed", environment: []string{"GH_CONFIG_DIR=relative", "XDG_CONFIG_HOME=relative", "HOME=relative"}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := githubCLIConfigDirectory(test.environment); got != test.want {
				t.Fatalf("GitHub CLI config directory = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPrivilegedGitEnvironmentSurvivesRunnerDelegation(t *testing.T) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "-C", repo, "init").CombinedOutput(); err != nil {
		t.Fatalf("initialize repository: %v: %s", err, output)
	}
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	gitDirectory := filepath.Join(repo, ".git")
	profile, err := NewPrivilegedGitProfile(repo, gitDirectory, gitDirectory, filepath.Join(gitDirectory, "index"), filepath.Join(gitDirectory, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "attacker.git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "attacker.index"))
	result, err := RunPrivilegedGit(t.Context(), delegatingGitRunner{}, profile, []string{"rev-parse", "--show-toplevel"}, 5*time.Second)
	if err != nil {
		t.Fatalf("run delegated privileged Git: %v: %s", err, result.Stderr)
	}
	if strings.TrimSpace(result.Stdout) != repo {
		t.Fatalf("delegated privileged Git worktree = %q, want %q", strings.TrimSpace(result.Stdout), repo)
	}
}

func TestRunFailClosedCleansProcessTreeAfterOverflow(t *testing.T) {
	outcome := runProcessTreeScenario(t, t.Context(), "success", true, nil, func(ctx context.Context, command string, args []string) (Result, error) {
		return RunFailClosed(ctx, OSRunner{}, command, args, "", 0, 1, 64)
	})
	var limitErr *CaptureLimitError
	if !errors.As(outcome.err, &limitErr) {
		t.Fatalf("expected capture overflow after process cleanup, got %v", outcome.err)
	}
}

func TestRunBoundedHeadTailPreservesFinalOutput(t *testing.T) {
	result, err := RunBoundedHeadTailInput(t.Context(), OSRunner{}, "sh", []string{"-c", "printf '1234567890'; printf 'abcdefghij' >&2"}, "", 5*time.Second, strings.NewReader(""), 8, "[..]")
	if err != nil {
		t.Fatalf("run head-tail bounded command: %v", err)
	}
	if result.Stdout != "12[..]90" || result.Stderr != "ab[..]ij" {
		t.Fatalf("unexpected head-tail bounded result: %#v", result)
	}
}

func TestRunBoundedHeadTailDoesNotTruncateOutputBelowLimit(t *testing.T) {
	result, err := RunBoundedHeadTailInput(t.Context(), OSRunner{}, "sh", []string{"-c", "printf '123456'"}, "", 5*time.Second, strings.NewReader(""), 8, "[..]")
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "123456" {
		t.Fatalf("unexpected output: %q", result.Stdout)
	}
}

func TestProcessTreeCleanupOnSuccess(t *testing.T) {
	outcome := runProcessTreeScenario(t, t.Context(), "success", true, nil, func(ctx context.Context, command string, args []string) (Result, error) {
		return OSRunner{}.Run(ctx, command, args, "", 0)
	})
	if outcome.err != nil {
		t.Fatalf("run successful command: %v", outcome.err)
	}
	if outcome.result.ExitCode != 0 || !strings.Contains(outcome.result.Stdout, "leader-ready") || !strings.Contains(outcome.result.Stdout, "ignoring-term") {
		t.Fatalf("unexpected successful result: %#v", outcome.result)
	}
}

func TestProcessTreeCleanupOnCommandFailure(t *testing.T) {
	outcome := runProcessTreeScenario(t, t.Context(), "failure", false, nil, func(ctx context.Context, command string, args []string) (Result, error) {
		return RunBoundedInput(ctx, OSRunner{}, command, args, "", 0, strings.NewReader(""), 1024, "[..]")
	})
	var exitErr *exec.ExitError
	if !errors.As(outcome.err, &exitErr) {
		t.Fatalf("expected exit error, got %v", outcome.err)
	}
	if outcome.result.ExitCode != 23 || !strings.Contains(outcome.result.Stdout, "leader-ready") {
		t.Fatalf("unexpected failed result: %#v", outcome.result)
	}
}

func TestProcessTreeCleanupOnTimeout(t *testing.T) {
	done := make(chan struct{})
	ctx := triggeredTimeoutContext{Context: t.Context(), done: done}
	outcome := runProcessTreeScenario(t, ctx, "block", false, func() { close(done) }, func(ctx context.Context, command string, args []string) (Result, error) {
		return RunBoundedHeadTailInput(ctx, OSRunner{}, command, args, "", time.Hour, strings.NewReader(""), 1024, "[..]")
	})
	if !errors.Is(outcome.err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", outcome.err)
	}
	if outcome.result.ExitCode != -1 {
		t.Fatalf("unexpected deadline exit code: %#v", outcome.result)
	}
	if !strings.Contains(outcome.result.Stdout, "leader-terminated") {
		t.Fatalf("deadline lost final process output: %#v", outcome.result)
	}
}

func TestProcessTreeCleanupOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	outcome := runProcessTreeScenario(t, ctx, "block", false, cancel, func(ctx context.Context, command string, args []string) (Result, error) {
		return RunLineFilteredInput(ctx, OSRunner{}, command, args, "", 0, strings.NewReader(""), 1024, "[..]", func([]byte) bool { return true })
	})
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("expected cancellation error, got %v", outcome.err)
	}
	if outcome.result.ExitCode != -1 {
		t.Fatalf("unexpected cancellation exit code: %#v", outcome.result)
	}
	if !strings.Contains(outcome.result.Stdout, "leader-terminated") {
		t.Fatalf("cancellation lost final process output: %#v", outcome.result)
	}
}

type processTreeOutcome struct {
	result Result
	err    error
}

type processTreeRun func(context.Context, string, []string) (Result, error)

func runProcessTreeScenario(t *testing.T, ctx context.Context, behavior string, ignoreTerm bool, onReady func(), run processTreeRun) processTreeOutcome {
	t.Helper()
	readyPath := t.TempDir() + "/ready"
	if err := syscall.Mkfifo(readyPath, 0o600); err != nil {
		t.Fatalf("create readiness FIFO: %v", err)
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open readiness FIFO: %v", err)
	}
	defer ready.Close()

	args := []string{
		"RUNNER_SUBPROCESS_HELPER_ROLE=leader",
		"RUNNER_SUBPROCESS_HELPER_BEHAVIOR=" + behavior,
		"RUNNER_SUBPROCESS_HELPER_READY=" + readyPath,
		"RUNNER_SUBPROCESS_HELPER_IGNORE_TERM=" + strconv.FormatBool(ignoreTerm),
		os.Args[0],
		"-test.run=^TestProcessTreeHelper$",
	}
	resultDone := make(chan processTreeOutcome, 1)
	go func() {
		result, runErr := run(ctx, "env", args)
		resultDone <- processTreeOutcome{result: result, err: runErr}
	}()

	pidDone := make(chan int, 1)
	readErr := make(chan error, 1)
	go func() {
		line, readErrValue := bufio.NewReader(ready).ReadString('\n')
		if readErrValue != nil {
			readErr <- readErrValue
			return
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(line))
		if parseErr != nil {
			readErr <- parseErr
			return
		}
		pidDone <- pid
	}()

	var pid int
	select {
	case pid = <-pidDone:
	case err = <-readErr:
		t.Fatalf("read descendant readiness: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for descendant readiness")
	}
	if onReady != nil {
		onReady()
	}

	var outcome processTreeOutcome
	select {
	case outcome = <-resultDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for command completion")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("descendant process %d survived command completion: %v", pid, err)
	}
	return outcome
}

// triggeredTimeoutContext lets the test trigger DeadlineExceeded only after
// the process tree has acknowledged readiness, without clock-based sleeps.
type triggeredTimeoutContext struct {
	context.Context
	done <-chan struct{}
}

func (ctx triggeredTimeoutContext) Done() <-chan struct{} {
	return ctx.done
}

func (ctx triggeredTimeoutContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestProcessTreeHelper(t *testing.T) {
	switch os.Getenv("RUNNER_SUBPROCESS_HELPER_ROLE") {
	case "":
		return
	case "leader":
		runProcessTreeLeader()
	case "descendant":
		runProcessTreeDescendant()
	default:
		t.Fatalf("unknown process helper role")
	}
}

func runProcessTreeLeader() {
	childReady, childReadyWriter, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$")
	child.Env = replaceEnvironment(os.Environ(), "RUNNER_SUBPROCESS_HELPER_ROLE", "descendant")
	child.ExtraFiles = []*os.File{childReadyWriter}
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		panic(err)
	}
	_ = childReadyWriter.Close()
	var ready [1]byte
	if _, err := io.ReadFull(childReady, ready[:]); err != nil {
		panic(err)
	}
	_ = childReady.Close()

	behavior := os.Getenv("RUNNER_SUBPROCESS_HELPER_BEHAVIOR")
	var termination chan os.Signal
	if behavior == "block" {
		termination = make(chan os.Signal, 1)
		signal.Notify(termination, syscall.SIGTERM)
	}
	parentReady, err := os.OpenFile(os.Getenv("RUNNER_SUBPROCESS_HELPER_READY"), os.O_WRONLY, 0)
	if err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintf(parentReady, "%d\n", child.Process.Pid); err != nil {
		panic(err)
	}
	_ = parentReady.Close()
	fmt.Fprintln(os.Stdout, "leader-ready")

	switch behavior {
	case "success":
		return
	case "failure":
		os.Exit(23)
	case "block":
		<-termination
		signal.Stop(termination)
		fmt.Fprintln(os.Stdout, "leader-terminated")
		return
	default:
		panic("unknown process helper behavior")
	}
}

func runProcessTreeDescendant() {
	if os.Getenv("RUNNER_SUBPROCESS_HELPER_IGNORE_TERM") == "true" {
		signal.Ignore(syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ignoring-term")
	}
	internalReady := os.NewFile(3, "internal-ready")
	if _, err := internalReady.Write([]byte{1}); err != nil {
		panic(err)
	}
	_ = internalReady.Close()
	fmt.Fprintln(os.Stdout, "descendant-ready")
	blocker, keepAlive, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	defer blocker.Close()
	defer keepAlive.Close()
	var blocked [1]byte
	for {
		if _, err := blocker.Read(blocked[:]); err != nil {
			panic(err)
		}
	}
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
