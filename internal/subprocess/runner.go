package subprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	processTerminationGracePeriod = 2 * time.Second
	processGroupPollInterval      = 10 * time.Millisecond
	GitStdoutLimit                = 32 * 1024 * 1024
	GitHubStdoutLimit             = 16 * 1024 * 1024
	DiagnosticStderrLimit         = 64 * 1024
)

func RunGit(ctx context.Context, runner Runner, args []string, dir string, timeout time.Duration) (Result, error) {
	return RunFailClosed(ctx, runner, "git", args, dir, timeout, GitStdoutLimit, DiagnosticStderrLimit)
}

// PrivilegedGitProfile pins the repository locations used by Git operations
// that run after an agent harness. Ordinary snapshot and harness Git commands
// intentionally do not use this profile so they continue to observe repository
// controls.
type PrivilegedGitProfile struct {
	WorkTree        string
	GitDirectory    string
	CommonDirectory string
	IndexFile       string
	ObjectDirectory string
}

// NewPrivilegedGitProfile validates already-derived linked-worktree paths. The
// caller is responsible for deriving them without following mutable Git config.
func NewPrivilegedGitProfile(workTree, gitDirectory, commonDirectory, indexFile, objectDirectory string) (PrivilegedGitProfile, error) {
	profile := PrivilegedGitProfile{
		WorkTree: filepath.Clean(strings.TrimSpace(workTree)), GitDirectory: filepath.Clean(strings.TrimSpace(gitDirectory)),
		CommonDirectory: filepath.Clean(strings.TrimSpace(commonDirectory)), IndexFile: filepath.Clean(strings.TrimSpace(indexFile)),
		ObjectDirectory: filepath.Clean(strings.TrimSpace(objectDirectory)),
	}
	for label, path := range map[string]string{
		"work tree": profile.WorkTree, "Git directory": profile.GitDirectory, "common Git directory": profile.CommonDirectory,
		"Git index": profile.IndexFile, "Git object directory": profile.ObjectDirectory,
	} {
		if path == "" || path == "." || !filepath.IsAbs(path) {
			return PrivilegedGitProfile{}, fmt.Errorf("privileged Git %s must be an absolute path", label)
		}
	}
	if profile.IndexFile != filepath.Join(profile.GitDirectory, "index") {
		return PrivilegedGitProfile{}, errors.New("privileged Git index is not the selected worktree index")
	}
	if profile.ObjectDirectory != filepath.Join(profile.CommonDirectory, "objects") {
		return PrivilegedGitProfile{}, errors.New("privileged Git object directory is not the selected common object store")
	}
	return profile, nil
}

type commandEnvironmentContextKey struct{}
type commandEnvironmentOverridesContextKey struct{}

type commandEnvironmentOverride struct {
	key   string
	value string
}

// WithEnvironmentVariable adds one explicit variable to ordinary child commands.
// A privileged command's complete allowlisted environment always takes
// precedence, so an override cannot widen that stronger boundary.
func WithEnvironmentVariable(ctx context.Context, key, value string) (context.Context, error) {
	if ctx == nil {
		return nil, errors.New("command environment variable requires a context")
	}
	if key == "" || strings.ContainsAny(key, "=\x00") {
		return nil, fmt.Errorf("invalid command environment variable %q", key)
	}
	if strings.ContainsRune(value, '\x00') {
		return nil, fmt.Errorf("command environment variable %q contains NUL", key)
	}
	return context.WithValue(ctx, commandEnvironmentOverridesContextKey{}, commandEnvironmentOverride{key: key, value: value}), nil
}

// RunPrivilegedGit executes Git with pinned repository selectors, an allowlisted
// process environment, no repository or external config, and explicit safe
// command config. The private context value keeps the existing Runner
// interface: a test adapter that delegates to OSRunner preserves the environment
// boundary without gaining access to or reconstructing the environment.
func RunPrivilegedGit(ctx context.Context, runner Runner, profile PrivilegedGitProfile, args []string, timeout time.Duration) (Result, error) {
	return runPrivilegedGit(ctx, runner, profile, args, timeout, "")
}

// RunPrivilegedGitNetwork is the privileged fetch/push variant. It retains the
// pinned repository and empty configuration boundary while enabling only HTTPS
// transport and an explicit GitHub CLI credential helper. Callers must supply a
// literal URL; inherited remotes and URL rewrite configuration remain absent.
func RunPrivilegedGitNetwork(ctx context.Context, runner Runner, profile PrivilegedGitProfile, args []string, timeout time.Duration) (Result, error) {
	if len(args) == 0 || args[0] != "fetch" && args[0] != "push" {
		return Result{}, errors.New("privileged network Git permits only fetch or push")
	}
	repositoryURL := ""
	for _, argument := range args[1:] {
		if strings.HasPrefix(argument, "https://github.com/") && strings.HasSuffix(argument, ".git") {
			if repositoryURL != "" {
				return Result{}, errors.New("privileged network Git requires one literal GitHub URL")
			}
			repositoryURL = argument
		}
	}
	if repositoryURL == "" {
		return Result{}, errors.New("privileged network Git requires one literal GitHub URL")
	}
	return runPrivilegedGit(ctx, runner, profile, args, timeout, repositoryURL)
}

func runPrivilegedGit(ctx context.Context, runner Runner, profile PrivilegedGitProfile, args []string, timeout time.Duration, repositoryURL string) (Result, error) {
	if runner == nil {
		runner = OSRunner{}
	}
	validated, err := NewPrivilegedGitProfile(profile.WorkTree, profile.GitDirectory, profile.CommonDirectory, profile.IndexFile, profile.ObjectDirectory)
	if err != nil {
		return Result{}, err
	}
	if err := rejectPrivilegedGitSelectors(args); err != nil {
		return Result{}, err
	}
	gitArgs := privilegedGitArguments()
	if repositoryURL != "" {
		gitArgs = append(gitArgs,
			"-c", "protocol.https.allow=always",
			"-c", "credential.helper=",
			"-c", "credential.https://github.com.helper=!gh auth git-credential",
			"-c", "url."+repositoryURL+".insteadOf="+repositoryURL,
			"-c", "http.proxy=",
			"-c", "http."+repositoryURL+".proxy=",
			"-c", "http.extraHeader=",
			"-c", "http."+repositoryURL+".extraHeader=",
			"-c", "http.followRedirects=false",
			"-c", "http."+repositoryURL+".followRedirects=false",
		)
	}
	gitArgs = append(gitArgs, args...)
	ctx = context.WithValue(ctx, commandEnvironmentContextKey{}, validated.environment(os.Environ()))
	return RunFailClosed(ctx, runner, "git", gitArgs, validated.WorkTree, timeout, GitStdoutLimit, DiagnosticStderrLimit)
}

func commandEnvironment(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	if environment, ok := ctx.Value(commandEnvironmentContextKey{}).([]string); ok {
		return environment
	}
	override, ok := ctx.Value(commandEnvironmentOverridesContextKey{}).(commandEnvironmentOverride)
	if !ok {
		return nil
	}
	return applyEnvironmentOverride(os.Environ(), override)
}

func applyEnvironmentOverride(environment []string, override commandEnvironmentOverride) []string {
	result := make([]string, 0, len(environment)+1)
	prefix := override.key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, prefix+override.value)
}

func privilegedGitArguments() []string {
	return []string{
		"--no-optional-locks", "--no-replace-objects",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "commit.gpgSign=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.excludesFile=" + os.DevNull,
		"-c", "diff.external=",
		"-c", "protocol.allow=never",
		"-c", "user.name=Local Project Runner",
		"-c", "user.email=local-project-runner@users.noreply.github.com",
	}
}

func rejectPrivilegedGitSelectors(args []string) error {
	if len(args) == 0 {
		return errors.New("privileged Git requires a subcommand")
	}
	command := strings.TrimSpace(args[0])
	if command == "" || strings.HasPrefix(command, "-") || strings.ContainsAny(command, "/\\\x00\r\n") {
		return fmt.Errorf("privileged Git arguments must begin with a subcommand, not a global repository or config selector: %q", args[0])
	}
	return nil
}

func (profile PrivilegedGitProfile) environment(inherited []string) []string {
	environment := make([]string, 0, 18)
	for _, entry := range inherited {
		key, _, found := strings.Cut(entry, "=")
		upper := strings.ToUpper(strings.TrimSpace(key))
		if !found || (upper != "PATH" && upper != "SYSTEMROOT" && upper != "WINDIR") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"LANG=C",
		"LC_ALL=C",
		"GIT_DIR="+profile.GitDirectory,
		"GIT_COMMON_DIR="+profile.CommonDirectory,
		"GIT_WORK_TREE="+profile.WorkTree,
		"GIT_INDEX_FILE="+profile.IndexFile,
		"GIT_OBJECT_DIRECTORY="+profile.ObjectDirectory,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=",
		"GIT_CONFIG="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_LITERAL_PATHSPECS=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func RunGitHub(ctx context.Context, runner Runner, args []string, dir string, timeout time.Duration) (Result, error) {
	return RunFailClosed(ctx, runner, "gh", args, dir, timeout, GitHubStdoutLimit, DiagnosticStderrLimit)
}

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Runner interface {
	Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (Result, error)
}

type BoundedInputRunner interface {
	RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string) (Result, error)
}

// FailClosedRunner bounds retained output at the OS pipe while continuing to
// drain both streams so the process and its descendants can be torn down
// safely. Any overflow is returned as an error; callers must never decode the
// partial stdout as machine output.
type FailClosedRunner interface {
	RunFailClosed(ctx context.Context, command string, args []string, dir string, timeout time.Duration, maxStdoutBytes, maxStderrBytes int) (Result, error)
}

type CaptureLimitError struct {
	Command string
	Stream  string
	Limit   int
}

func (e *CaptureLimitError) Error() string {
	return fmt.Sprintf("command %q %s exceeded capture limit of %d bytes", e.Command, e.Stream, e.Limit)
}

type HeadTailBoundedInputRunner interface {
	RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string) (Result, error)
}

type LineFilter func(line []byte) bool

type LineFilteredInputRunner interface {
	RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string, keep LineFilter) (Result, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (Result, error) {
	return runOSCommandWithEnvironment(ctx, command, args, dir, timeout, commandEnvironment(ctx))
}

func (OSRunner) RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string) (Result, error) {
	stdout := newBoundedCapture(maxBytesPerStream, truncationMarker)
	stderr := newBoundedCapture(maxBytesPerStream, truncationMarker)
	exitCode, err := runOSCommandToWritersInput(ctx, command, args, dir, timeout, nil, input, stdout, stderr)
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
}

func (OSRunner) RunFailClosed(ctx context.Context, command string, args []string, dir string, timeout time.Duration, maxStdoutBytes, maxStderrBytes int) (Result, error) {
	return runOSCommandFailClosed(ctx, command, args, dir, timeout, commandEnvironment(ctx), nil, maxStdoutBytes, maxStderrBytes)
}

// RunFailClosed executes through an injected runner while preserving a true
// streaming boundary for production OSRunner values. Test doubles that only
// implement Runner are checked immediately after their bounded synthetic
// result is returned.
func RunFailClosed(ctx context.Context, runner Runner, command string, args []string, dir string, timeout time.Duration, maxStdoutBytes, maxStderrBytes int) (Result, error) {
	if bounded, ok := runner.(FailClosedRunner); ok {
		return bounded.RunFailClosed(ctx, command, args, dir, timeout, maxStdoutBytes, maxStderrBytes)
	}
	result, err := runner.Run(ctx, command, args, dir, timeout)
	return failClosedResult(command, result, err, maxStdoutBytes, maxStderrBytes)
}

// RunOSFailClosedInput is the command-boundary variant for the few setup
// operations that deliberately provide stdin to Git.
func RunOSFailClosedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxStdoutBytes, maxStderrBytes int) (Result, error) {
	return runOSCommandFailClosed(ctx, command, args, dir, timeout, nil, input, maxStdoutBytes, maxStderrBytes)
}

func runOSCommandFailClosed(ctx context.Context, command string, args []string, dir string, timeout time.Duration, environment []string, input io.Reader, maxStdoutBytes, maxStderrBytes int) (Result, error) {
	stdout := newBoundedCapture(maxStdoutBytes, "")
	stderr := newBoundedCapture(maxStderrBytes, "")
	exitCode, err := runOSCommandToWritersInput(ctx, command, args, dir, timeout, environment, input, stdout, stderr)
	result := Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
	if stdout.truncated {
		result.Stdout = ""
		err = errors.Join(err, &CaptureLimitError{Command: command, Stream: "stdout", Limit: maxStdoutBytes})
	}
	if stderr.truncated {
		err = errors.Join(err, &CaptureLimitError{Command: command, Stream: "stderr", Limit: maxStderrBytes})
	}
	return result, err
}

func failClosedResult(command string, result Result, err error, maxStdoutBytes, maxStderrBytes int) (Result, error) {
	if maxStdoutBytes < 0 {
		maxStdoutBytes = 0
	}
	if maxStderrBytes < 0 {
		maxStderrBytes = 0
	}
	if len(result.Stdout) > maxStdoutBytes {
		result.Stdout = ""
		err = errors.Join(err, &CaptureLimitError{Command: command, Stream: "stdout", Limit: maxStdoutBytes})
	}
	if len(result.Stderr) > maxStderrBytes {
		result.Stderr = result.Stderr[:maxStderrBytes]
		err = errors.Join(err, &CaptureLimitError{Command: command, Stream: "stderr", Limit: maxStderrBytes})
	}
	return result, err
}

func (OSRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string) (Result, error) {
	stdout := newHeadTailCapture(maxBytesPerStream, truncationMarker)
	stderr := newHeadTailCapture(maxBytesPerStream, truncationMarker)
	exitCode, err := runOSCommandToWritersInput(ctx, command, args, dir, timeout, nil, input, stdout, stderr)
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
}

func (OSRunner) RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string, keep LineFilter) (Result, error) {
	stdout := newLineFilteredCapture(maxBytesPerStream, truncationMarker, keep)
	stderr := newBoundedCapture(maxBytesPerStream, truncationMarker)
	exitCode, err := runOSCommandToWritersInput(ctx, command, args, dir, timeout, nil, input, stdout, stderr)
	stdout.Flush()
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
}

func RunBoundedInput(ctx context.Context, runner Runner, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string) (Result, error) {
	if bounded, ok := runner.(BoundedInputRunner); ok {
		return bounded.RunBoundedInput(ctx, command, args, dir, timeout, input, maxBytesPerStream, truncationMarker)
	}
	return Result{}, fmt.Errorf("runner %T cannot provide safe stdin transport for %q", runner, command)
}

func RunBoundedHeadTailInput(ctx context.Context, runner Runner, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string) (Result, error) {
	if bounded, ok := runner.(HeadTailBoundedInputRunner); ok {
		return bounded.RunBoundedHeadTailInput(ctx, command, args, dir, timeout, input, maxBytesPerStream, truncationMarker)
	}
	return Result{}, fmt.Errorf("runner %T cannot provide safe stdin transport for %q", runner, command)
}

func RunLineFilteredInput(ctx context.Context, runner Runner, command string, args []string, dir string, timeout time.Duration, input io.Reader, maxBytesPerStream int, truncationMarker string, keep LineFilter) (Result, error) {
	if filtered, ok := runner.(LineFilteredInputRunner); ok {
		return filtered.RunLineFilteredInput(ctx, command, args, dir, timeout, input, maxBytesPerStream, truncationMarker, keep)
	}
	return Result{}, fmt.Errorf("runner %T cannot provide safe stdin transport for %q", runner, command)
}

func runOSCommandWithEnvironment(ctx context.Context, command string, args []string, dir string, timeout time.Duration, environment []string) (Result, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode, err := runOSCommandToWriters(ctx, command, args, dir, timeout, environment, &stdout, &stderr)
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
}

func runOSCommandToWriters(ctx context.Context, command string, args []string, dir string, timeout time.Duration, environment []string, stdout, stderr io.Writer) (int, error) {
	return runOSCommandToWritersInput(ctx, command, args, dir, timeout, environment, nil, stdout, stderr)
}

func runOSCommandToWritersInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, environment []string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.Command(command, args...)
	cmd.Dir = dir
	cmd.Stdin = input
	if environment != nil {
		cmd.Env = environment
	}

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return -1, fmt.Errorf("create stdout pipe: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return -1, fmt.Errorf("create stderr pipe: %w", err)
	}
	closePipes := func() {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	configureProcessGroup(cmd)
	if err = cmd.Start(); err != nil {
		closePipes()
		return exitCode(err), err
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	copyDone := make(chan error, 2)
	go copyProcessOutput(copyDone, stdout, stdoutReader)
	go copyProcessOutput(copyDone, stderr, stderrReader)

	waitDone := make(chan commandWaitResult, 1)
	go func() { waitDone <- waitForDirectProcess(cmd) }()

	var (
		waitResult     commandWaitResult
		haveWaitResult bool
		commandErr     error
	)
	select {
	case waitResult = <-waitDone:
		haveWaitResult = true
		commandErr = waitResult.err
	case <-ctx.Done():
		commandErr = ctx.Err()
	}

	waitResult, teardownErr := teardownProcessGroup(cmd, waitDone, waitResult, haveWaitResult)
	if teardownErr != nil {
		// A process that survived teardown may still hold either output pipe.
		// Closing the readers prevents an unbounded wait while returning the
		// teardown failure to the caller.
		_ = stdoutReader.Close()
		_ = stderrReader.Close()
	}
	copyErr := errors.Join(<-copyDone, <-copyDone)
	_ = stdoutReader.Close()
	_ = stderrReader.Close()

	if commandErr == nil && copyErr != nil {
		commandErr = copyErr
	}
	commandExitCode := exitCode(waitResult.err)
	if commandErr != nil && !haveWaitResult {
		commandExitCode = -1
	}
	if teardownErr != nil {
		commandErr = errors.Join(commandErr, fmt.Errorf("tear down process group: %w", teardownErr))
	}
	return commandExitCode, commandErr
}

type commandWaitResult struct {
	err error
}

func copyProcessOutput(done chan<- error, destination io.Writer, source *os.File) {
	_, err := io.Copy(destination, source)
	done <- err
}

func waitForDirectProcess(cmd *exec.Cmd) commandWaitResult {
	state, err := cmd.Process.Wait()
	cmd.ProcessState = state
	if err == nil && state != nil && !state.Success() {
		err = &exec.ExitError{ProcessState: state}
	}
	return commandWaitResult{err: err}
}

func teardownProcessGroup(cmd *exec.Cmd, waitDone <-chan commandWaitResult, waitResult commandWaitResult, haveWaitResult bool) (commandWaitResult, error) {
	gracefulErr := terminateProcessGroup(cmd, false)
	if gracefulErr == nil {
		var clean bool
		var err error
		waitResult, haveWaitResult, clean, err = waitForProcessGroup(cmd, waitDone, waitResult, haveWaitResult, processTerminationGracePeriod)
		if err == nil && clean {
			return waitResult, nil
		}
	}

	forceErr := terminateProcessGroup(cmd, true)
	if !haveWaitResult {
		waitResult = <-waitDone
		haveWaitResult = true
	}
	_, _, clean, verifyErr := waitForProcessGroup(cmd, waitDone, waitResult, haveWaitResult, processTerminationGracePeriod)
	if clean {
		return waitResult, nil
	}
	if verifyErr == nil {
		verifyErr = fmt.Errorf("process group %d still exists after forced termination", cmd.Process.Pid)
	}
	if forceErr != nil {
		return waitResult, errors.Join(fmt.Errorf("force termination: %w", forceErr), verifyErr)
	}
	return waitResult, verifyErr
}

func waitForProcessGroup(cmd *exec.Cmd, waitDone <-chan commandWaitResult, waitResult commandWaitResult, haveWaitResult bool, timeout time.Duration) (commandWaitResult, bool, bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(processGroupPollInterval)
	defer ticker.Stop()

	for {
		if !haveWaitResult {
			select {
			case waitResult = <-waitDone:
				haveWaitResult = true
			default:
			}
		}
		alive, err := processGroupAlive(cmd)
		if err != nil {
			return waitResult, haveWaitResult, false, err
		}
		if haveWaitResult && !alive {
			return waitResult, true, true, nil
		}

		select {
		case waitResult = <-waitDone:
			haveWaitResult = true
		case <-ticker.C:
		case <-timer.C:
			return waitResult, haveWaitResult, false, nil
		}
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return -1
}
