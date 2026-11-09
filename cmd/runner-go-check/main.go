// runner-go-check preserves focused Go proof without retaining test logs.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	maxEvent        = 1 << 20
	maxTests        = 1024
	maxName         = 512
	maxDiagnostic   = 2048
	maxStdoutProof  = 48 << 10
	settlementGrace = 2 * time.Second
	maxInputPatch   = 16 << 20
)

type options struct {
	goBinary, pkg, selection, regression, development, scratch, output string
	sourceBase, inputPatchID, inputPatch                               string
	timeout                                                            time.Duration
}

type testResult struct {
	Package string `json:"package,omitempty"`
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
}

type packageResult struct {
	Name        string `json:"name"`
	Outcome     string `json:"outcome"`
	FailedBuild string `json:"failed_build,omitempty"`
}

type buildFailure struct {
	Package    string `json:"package"`
	Diagnostic string `json:"diagnostic"`
}

type scopeResult struct {
	Selector               string          `json:"selector"`
	Status                 string          `json:"status"`
	Reason                 string          `json:"reason,omitempty"`
	ExitCode               *int            `json:"exit_code"`
	ChildExited            bool            `json:"child_exited"`
	ElapsedSeconds         float64         `json:"elapsed_seconds"`
	Passed                 int             `json:"passed"`
	Failed                 int             `json:"failed"`
	Skipped                int             `json:"skipped"`
	LeafPassed             int             `json:"leaf_passed"`
	EvidenceIncomplete     bool            `json:"evidence_incomplete"`
	Tests                  []testResult    `json:"tests"`
	Packages               []packageResult `json:"packages,omitempty"`
	BuildFailures          []buildFailure  `json:"build_failures,omitempty"`
	FirstFailureTest       string          `json:"first_failure_test,omitempty"`
	FirstFailurePackage    string          `json:"first_failure_package,omitempty"`
	FirstFailureDiagnostic string          `json:"first_failure_diagnostic,omitempty"`
}

type receipt struct {
	Package       string         `json:"package"`
	Scope         *scopeResult   `json:"scope,omitempty"`
	Regression    scopeResult    `json:"regression,omitzero"`
	Development   scopeResult    `json:"development,omitzero"`
	PreparedInput *preparedInput `json:"prepared_input,omitempty"`
}

// This identifies the declared prepared input, not the complete source tree.
// Runner authenticates the relationship between that input and the checkout.
type preparedInput struct {
	SourceBase       string    `json:"source_base"`
	InputPatchID     string    `json:"input_patch_id"`
	InputPatchSHA256 string    `json:"input_patch_sha256"`
	InputPatchBytes  int64     `json:"input_patch_bytes"`
	InputVerified    bool      `json:"input_verified"`
	Runtime          goRuntime `json:"runtime"`
}

type goRuntime struct {
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

func main() {
	o, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "go-check:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := run(ctx, o)
	saveErr := save(o.output, r)
	if err := printProof(os.Stdout, r); err != nil {
		if o.selection != "" {
			os.Exit(2)
		}
		os.Exit(1)
	}
	if saveErr != nil {
		fmt.Fprintln(os.Stderr, "go-check: could not save proof artifact")
		if o.selection != "" {
			os.Exit(2)
		}
		os.Exit(1)
	}
	if r.Scope != nil {
		fmt.Printf("%s; artifact=%s\n", summary("scope", *r.Scope), o.output)
	} else {
		fmt.Printf("%s; %s; artifact=%s\n", summary("regression", r.Regression), summary("development", r.Development), o.output)
	}
	os.Exit(exitCode(r))
}

func parseOptions(args []string) (options, error) {
	var o options
	f := flag.NewFlagSet("runner-go-check", flag.ContinueOnError)
	f.StringVar(&o.goBinary, "go", "", "absolute pinned Go executable")
	f.StringVar(&o.pkg, "package", "", "one workspace-relative Go package directory")
	f.StringVar(&o.selection, "run", "", "one configured test regex; exclusive with regression/development scopes")
	f.StringVar(&o.regression, "regression-run", "", "top-level regression regex; slash is forbidden")
	f.StringVar(&o.development, "development-run", "", "exact anchored development function, optionally anchored subtest regexes")
	f.StringVar(&o.scratch, "scratch", "", "existing caller-owned scratch directory")
	f.StringVar(&o.output, "output", "", "absolute JSON artifact path under scratch")
	f.StringVar(&o.sourceBase, "source-base", "", "Runner-prepared full source base; requires both input-patch flags")
	f.StringVar(&o.inputPatchID, "input-patch-id", "", "literal Runner artifact ID of the prepared patch")
	f.StringVar(&o.inputPatch, "input-patch", "", "absolute staged prepared patch to verify before and after Go")
	f.DurationVar(&o.timeout, "timeout", 0, "required Go test timeout; allows 2s settlement grace")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	missingPrepared := false
	f.Visit(func(v *flag.Flag) {
		if (v.Name == "source-base" || v.Name == "input-patch-id" || v.Name == "input-patch") && (o.sourceBase == "" || o.inputPatchID == "" || o.inputPatch == "") {
			missingPrepared = true
		}
	})
	if missingPrepared {
		return o, errors.New("all three nonempty prepared-input flags are required together")
	}
	return o, validate(o)
}

func validate(o options) error {
	if o.sourceBase != "" || o.inputPatchID != "" || o.inputPatch != "" {
		if !regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(o.sourceBase) || len(o.inputPatchID) > 1024 || !filepath.IsLocal(o.inputPatchID) || strings.ContainsAny(o.inputPatchID, "\\\x00\r\n") || filepath.ToSlash(filepath.Clean(o.inputPatchID)) != o.inputPatchID || !filepath.IsAbs(o.inputPatch) || filepath.Clean(o.inputPatch) != o.inputPatch || o.inputPatch == o.output {
			return errors.New("--source-base, --input-patch-id and --input-patch require a full base, literal artifact ID and distinct absolute patch path together")
		}
	}
	if !filepath.IsAbs(o.goBinary) || o.pkg == "" || len(o.pkg) > 1024 || strings.HasPrefix(o.pkg, "-") || strings.ContainsAny(o.pkg, " \t\r\n") || (strings.Contains(o.pkg, "...") && !(o.selection != "" && (o.pkg == "..." || o.pkg == "./..."))) || o.timeout <= 0 || o.timeout > time.Duration(1<<63-1)-settlementGrace {
		return errors.New("absolute --go, one --package and positive --timeout are required")
	}
	if o.selection != "" {
		if o.regression != "" || o.development != "" || len(o.selection) > 1024 {
			return errors.New("one bounded --run cannot be combined with regression/development scopes")
		}
		if _, err := regexp.Compile(o.selection); err != nil {
			return errors.New("invalid scope regex")
		}
		pkg := strings.TrimPrefix(o.pkg, "./")
		if pkg != "..." && pkg != "." && (!filepath.IsLocal(pkg) || strings.Contains(pkg, "..") || !regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`).MatchString(pkg)) {
			return errors.New("single scope package must be repository-relative or ...")
		}
	} else if err := validateTwoScopes(o); err != nil {
		return err
	}
	return validateArtifact(o)
}

func validateTwoScopes(o options) error {
	if o.regression == "" || len(o.regression) > 1024 || strings.Contains(o.regression, "/") {
		return errors.New("regression selector must be a bounded top-level regex without slash")
	}
	reg, err := regexp.Compile(o.regression)
	if err != nil {
		return errors.New("invalid regression regex")
	}
	parts := strings.Split(o.development, "/")
	if len(o.development) > 2048 || len(parts) > 8 || !regexp.MustCompile(`^\^Test[A-Za-z0-9_]+\$$`).MatchString(parts[0]) {
		return errors.New("development selector must start with one exact ^TestName$ function")
	}
	for _, part := range parts {
		if !strings.HasPrefix(part, "^") || !strings.HasSuffix(part, "$") {
			return errors.New("development subtest components must be anchored")
		}
		if _, err := regexp.Compile(part); err != nil {
			return errors.New("invalid development regex")
		}
	}
	if reg.MatchString(strings.Trim(parts[0], "^$")) {
		return errors.New("regression and development top-level scopes must be disjoint")
	}
	return nil
}

func validateArtifact(o options) error {
	if !filepath.IsAbs(o.scratch) || !filepath.IsAbs(o.output) {
		return errors.New("absolute --scratch and --output paths are required")
	}
	scratch, err := filepath.EvalSymlinks(o.scratch)
	if err != nil {
		return errors.New("scratch directory does not exist")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(o.output))
	if err != nil {
		return errors.New("output parent does not exist")
	}
	rel, err := filepath.Rel(scratch, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("output must be under caller scratch")
	}
	if info, err := os.Lstat(o.output); err == nil && !info.Mode().IsRegular() {
		return errors.New("output must be a regular file")
	}
	return nil
}

func summary(name string, s scopeResult) string {
	out := fmt.Sprintf("%s=%s (pass=%d fail=%d skip=%d)", name, s.Status, s.Passed, s.Failed, s.Skipped)
	if s.Reason != "" {
		out += fmt.Sprintf(" reason=%q", s.Reason)
	}
	if s.FirstFailureTest != "" {
		out += fmt.Sprintf(" first_failure=%q", s.FirstFailureTest)
	}
	return out
}

func printProof(w io.Writer, r receipt) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b)+1 > maxStdoutProof {
		r.Regression.Tests, r.Development.Tests = nil, nil
		if r.Scope != nil {
			scope := *r.Scope
			scope.Tests, scope.Packages, scope.BuildFailures = nil, nil, nil
			r.Scope = &scope
		}
		b, err = json.Marshal(struct {
			TestNamesOmitted     bool    `json:"test_names_omitted"`
			EvidenceListsOmitted bool    `json:"evidence_lists_omitted,omitempty"`
			Proof                receipt `json:"proof"`
		}{true, r.Scope != nil, r})
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func run(ctx context.Context, o options) receipt {
	if o.selection != "" && o.pkg == "..." {
		o.pkg = "./..."
	} else if filepath.IsLocal(o.pkg) && o.pkg != "." && !strings.HasPrefix(o.pkg, "./") {
		o.pkg = "./" + o.pkg
	}
	env := os.Environ() // Both scopes reuse precisely the same environment/cache.
	if o.sourceBase != "" {
		return runPrepared(ctx, o, env)
	}
	return runChecks(ctx, o, env)
}

func runChecks(ctx context.Context, o options, env []string) receipt {
	if o.selection != "" {
		scope := runScope(ctx, o, o.selection, env)
		return receipt{Package: o.pkg, Scope: &scope}
	}
	r := receipt{
		Package:     o.pkg,
		Regression:  scopeResult{Selector: o.regression, Status: "not_run", Tests: []testResult{}},
		Development: scopeResult{Selector: o.development, Status: "not_run", Reason: "regression proof did not pass", Tests: []testResult{}},
	}
	r.Regression = runScope(ctx, o, o.regression, env)
	if r.Regression.Status == "passed" {
		r.Development = runScope(ctx, o, o.development, env)
	}
	return r
}

func runPrepared(parent context.Context, o options, env []string) receipt {
	// Metadata collection, test execution and final verification share one budget.
	ctx, cancel := context.WithTimeout(parent, o.timeout+settlementGrace)
	defer cancel()
	p := &preparedInput{SourceBase: o.sourceBase, InputPatchID: o.inputPatchID}
	r := receipt{Package: o.pkg, PreparedInput: p}
	if o.selection != "" {
		r.Scope = &scopeResult{Selector: o.selection, Status: "not_run", Tests: []testResult{}}
	} else {
		r.Regression = scopeResult{Selector: o.regression, Status: "not_run", Tests: []testResult{}}
		r.Development = scopeResult{Selector: o.development, Status: "not_run", Tests: []testResult{}}
	}
	before, digest, err := hashInputPatch(ctx, o.inputPatch)
	if err != nil {
		return incompletePrepared(r, "prepared input could not be verified before Go")
	}
	p.InputPatchSHA256, p.InputPatchBytes = digest, before.Size()
	p.Runtime, err = childGoRuntime(ctx, o, env)
	if err != nil {
		return incompletePrepared(r, "configured Go runtime could not be verified")
	}
	r = runChecks(ctx, o, env)
	r.PreparedInput = p
	after, digest, err := hashInputPatch(ctx, o.inputPatch)
	if err != nil || ctx.Err() != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || digest != p.InputPatchSHA256 {
		return incompletePrepared(r, "prepared input changed or could not be verified after Go")
	}
	if (r.Scope != nil && !r.Scope.ChildExited) || (r.Scope == nil && (!r.Regression.ChildExited || (r.Development.Status != "not_run" && !r.Development.ChildExited))) {
		return incompletePrepared(r, "prepared input execution did not settle")
	}
	p.InputVerified = true
	return r
}

func incompletePrepared(r receipt, reason string) receipt {
	if r.Scope != nil {
		r.Scope.Status, r.Scope.Reason, r.Scope.EvidenceIncomplete = "failed", reason, true
	} else {
		r.Regression.Status, r.Regression.Reason, r.Regression.EvidenceIncomplete = "failed", reason, true
	}
	return r
}

func hashInputPatch(ctx context.Context, path string) (os.FileInfo, string, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxInputPatch {
		return nil, "", errors.New("patch must be a bounded regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, "", errors.New("patch identity changed")
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		count, readErr := f.Read(buf)
		n += int64(count)
		if n > maxInputPatch {
			return nil, "", errors.New("patch exceeds 16MiB")
		}
		_, _ = h.Write(buf[:count])
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, "", readErr
		}
	}
	final, err := f.Stat()
	current, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || final.Size() != n || info.Size() != n || info.Mode() != final.Mode() || !info.ModTime().Equal(final.ModTime()) {
		return nil, "", errors.New("patch changed while hashing")
	}
	return info, fmt.Sprintf("%x", h.Sum(nil)), nil
}

func childGoRuntime(ctx context.Context, o options, env []string) (goRuntime, error) {
	cmd := exec.CommandContext(ctx, o.goBinary, "env", "-json", "GOVERSION", "GOOS", "GOARCH")
	cmd.Env, cmd.WaitDelay = env, settlementGrace
	stdout := &diagnostic{}
	cmd.Stdout, cmd.Stderr = stdout, io.Discard
	if err := cmd.Run(); err != nil || ctx.Err() != nil || stdout.truncated {
		return goRuntime{}, errors.New("Go runtime query did not complete")
	}
	var fields struct{ GOVERSION, GOOS, GOARCH string }
	if err := json.Unmarshal([]byte(stdout.String()), &fields); err != nil || !regexp.MustCompile(`^go[0-9]+\.[A-Za-z0-9.+-]{1,100}$`).MatchString(fields.GOVERSION) || !regexp.MustCompile(`^[a-z0-9]{1,32}$`).MatchString(fields.GOOS) || !regexp.MustCompile(`^[a-z0-9]{1,32}$`).MatchString(fields.GOARCH) {
		return goRuntime{}, errors.New("Go runtime query was invalid")
	}
	return goRuntime{
		GoVersion: fields.GOVERSION,
		GOOS:      fields.GOOS,
		GOARCH:    fields.GOARCH,
	}, nil
}

func exitCode(r receipt) int {
	if r.PreparedInput != nil && !r.PreparedInput.InputVerified {
		if r.Scope != nil {
			return 2
		}
		return 1
	}
	if r.Scope != nil {
		s := r.Scope
		if s.Status == "passed" {
			return 0
		}
		if s.ChildExited && s.ExitCode != nil && *s.ExitCode == 1 && !s.EvidenceIncomplete && s.Skipped == 0 && (s.Failed > 0 || len(s.BuildFailures) > 0) {
			return 1
		}
		return 2
	}
	if r.Regression.Status == "passed" && r.Development.Status == "passed" {
		return 0
	}
	return 1
}

func runScope(parent context.Context, o options, selector string, env []string) scopeResult {
	r := scopeResult{Selector: selector, Status: "failed", Tests: []testResult{}}
	ctx, cancel := context.WithTimeout(parent, o.timeout+settlementGrace)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.goBinary, "test", "-json", "-mod=readonly", "-count=1", "-timeout", o.timeout.String(), "-run", selector, o.pkg)
	cmd.Env = env
	// Inherit Runner's owned process group: outer cancellation must reach both
	// this reporter and Go/tests. After Go's own timeout plus grace, CommandContext
	// kills only its direct child; the outer worker owns group settlement.
	cmd.WaitDelay = 2 * time.Second
	events := &eventStream{
		states:   make(map[testID]*testState),
		packages: make(map[string]*packageState),
		builds:   make(map[string]*buildState),
	}
	stderr := &diagnostic{}
	cmd.Stdout, cmd.Stderr = events, stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		r.Reason = "Go command did not start"
		return r
	}
	err := cmd.Wait()
	r.ElapsedSeconds = time.Since(start).Seconds()
	if cmd.ProcessState != nil {
		r.ChildExited = true // Wait returned the direct child's settled process state.
		code := cmd.ProcessState.ExitCode()
		r.ExitCode = &code
	}
	events.finish()
	r.EvidenceIncomplete = events.incomplete || len(events.packages) == 0 || ctx.Err() != nil
	packageFailed := false
	for name, state := range events.packages {
		outcome := state.outcome
		if outcome == "" {
			r.EvidenceIncomplete = true
		} else if outcome == "fail" {
			packageFailed = true
		}
		if o.selection != "" {
			if outcome == "" {
				outcome = "incomplete"
			}
			r.Packages = append(r.Packages, packageResult{Name: name, Outcome: outcome, FailedBuild: state.failedBuild})
			if state.failedBuild != "" {
				build := events.builds[state.failedBuild]
				if state.outcome != "fail" || build == nil || !build.failed || build.output.String() == "" {
					r.EvidenceIncomplete = true
				} else {
					build.linked = true
				}
			}
		}
	}
	if o.selection != "" {
		for name, state := range events.builds {
			if !state.failed {
				continue
			}
			if !state.linked || state.output.String() == "" {
				r.EvidenceIncomplete = true
			}
			r.BuildFailures = append(r.BuildFailures, buildFailure{Package: name, Diagnostic: state.output.String()})
		}
		sort.Slice(r.BuildFailures, func(i, j int) bool { return r.BuildFailures[i].Package < r.BuildFailures[j].Package })
	}
	for id, state := range events.states {
		if !state.ran || state.outcome == "" {
			r.EvidenceIncomplete = true
		}
		if state.outcome != "" || o.selection != "" {
			result := testResult{Name: id.name, Outcome: state.outcome}
			if o.selection != "" {
				result.Package = id.pkg
				if result.Outcome == "" {
					result.Outcome = "incomplete"
				}
			}
			r.Tests = append(r.Tests, result)
			switch state.outcome {
			case "pass":
				r.Passed++
				if !state.child && state.ran {
					r.LeafPassed++
				}
			case "fail":
				r.Failed++
			case "skip":
				r.Skipped++
			}
		}
	}
	sort.Slice(r.Tests, func(i, j int) bool {
		if r.Tests[i].Package != r.Tests[j].Package {
			return r.Tests[i].Package < r.Tests[j].Package
		}
		return r.Tests[i].Name < r.Tests[j].Name
	})
	sort.Slice(r.Packages, func(i, j int) bool { return r.Packages[i].Name < r.Packages[j].Name })
	r.FirstFailureTest, r.FirstFailureDiagnostic = events.failureTest, events.failure
	if o.selection != "" {
		r.FirstFailurePackage = events.failurePackage
	}
	if events.failureTest == "" && r.FirstFailureDiagnostic == "" && (err != nil || packageFailed || r.EvidenceIncomplete) {
		r.FirstFailureDiagnostic = events.packageOutput.String()
	}
	if events.failureTest == "" && r.FirstFailureDiagnostic == "" && err != nil {
		r.FirstFailureDiagnostic = stderr.String()
	}
	switch {
	case ctx.Err() != nil:
		r.Reason = "scope cancelled or deadline reached"
	case err != nil || r.Failed > 0 || packageFailed:
		r.Reason = "Go command or a test failed"
	case r.EvidenceIncomplete:
		r.Reason = "test evidence is incomplete"
		r.EvidenceIncomplete = true
	case r.Skipped > 0:
		r.Reason = "required proof is missing: selected test cases skipped"
	case r.LeafPassed == 0:
		r.Reason = "required scope has no passing executed leaf tests (no tests or all skipped)"
	default:
		r.Status = "passed"
	}
	return r
}

type diagnostic struct {
	head, tail []byte
	truncated  bool
}

func (b *diagnostic) Write(p []byte) (int, error) {
	n := len(p)
	const half = maxDiagnostic / 2
	remaining := half - len(b.head)
	if remaining > len(p) {
		remaining = len(p)
	}
	b.head = append(b.head, p[:remaining]...)
	p = p[remaining:]
	if len(b.tail)+len(p) > half {
		b.truncated = true
		if len(p) >= half {
			p = p[len(p)-half:]
			b.tail = b.tail[:0]
		} else {
			b.tail = b.tail[len(b.tail)+len(p)-half:]
		}
	}
	b.tail = append(b.tail, p...)
	return n, nil
}

func (b *diagnostic) String() string {
	if !b.truncated {
		return string(b.head) + string(b.tail)
	}
	const marker = "\n... [truncated] ...\n"
	return string(b.head[:len(b.head)-len(marker)]) + marker + string(b.tail)
}

type testState struct {
	ran, child bool
	outcome    string
	output     diagnostic
}

type testID struct{ pkg, name string }

type packageState struct{ outcome, failedBuild string }

type buildState struct {
	failed, linked bool
	output         diagnostic
}

type eventStream struct {
	line                                 []byte
	dropping, incomplete                 bool
	states                               map[testID]*testState
	packages                             map[string]*packageState
	builds                               map[string]*buildState
	failure, failureTest, failurePackage string
	packageOutput                        diagnostic
}

func (s *eventStream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			i = len(p)
		}
		if !s.dropping {
			if len(s.line)+i > maxEvent {
				s.line = nil
				s.dropping = true
				s.incomplete = true
			} else {
				s.line = append(s.line, p[:i]...)
			}
		}
		p = p[i:]
		if len(p) > 0 {
			s.finish()
			p = p[1:]
		}
	}
	return n, nil
}

func (s *eventStream) finish() {
	defer func() { s.line, s.dropping = s.line[:0], false }()
	if s.dropping || len(bytes.TrimSpace(s.line)) == 0 {
		return
	}
	var event struct{ Action, Package, Test, Output, ImportPath, FailedBuild string }
	if json.Unmarshal(s.line, &event) != nil || event.Action == "" {
		s.incomplete = true
		return
	}
	if event.Action == "build-output" || event.Action == "build-fail" {
		if event.ImportPath == "" || len(event.ImportPath) > maxName {
			s.incomplete = true
			return
		}
		state := s.builds[event.ImportPath]
		if state == nil {
			if len(s.builds) >= maxTests {
				s.incomplete = true
				return
			}
			state = &buildState{}
			s.builds[event.ImportPath] = state
		}
		if event.Action == "build-fail" {
			state.failed = true
		} else {
			_, _ = state.output.Write([]byte(event.Output))
			_, _ = s.packageOutput.Write([]byte(event.Output))
		}
		return
	}
	if event.Package == "" || len(event.Package) > maxName {
		s.incomplete = true
		return
	}
	state, seen := s.packages[event.Package]
	if !seen {
		if len(s.packages) >= maxTests {
			s.incomplete = true
			return
		}
		state = &packageState{}
		s.packages[event.Package] = state
	} else if state.outcome != "" {
		s.incomplete = true
	}
	if event.Test == "" {
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			state.outcome = event.Action
			if len(event.FailedBuild) > maxName {
				s.incomplete = true
			} else {
				state.failedBuild = event.FailedBuild
			}
		}
		if event.Action == "output" {
			_, _ = s.packageOutput.Write([]byte(event.Output))
		}
		return
	}
	if len(event.Test) > maxName {
		s.incomplete = true
		return
	}
	id := testID{event.Package, event.Test}
	test := s.states[id]
	if test == nil {
		if len(s.states) >= maxTests {
			s.incomplete = true
			return
		}
		test = &testState{}
		s.states[id] = test
		for ancestor := event.Test; strings.Contains(ancestor, "/"); {
			ancestor = ancestor[:strings.LastIndexByte(ancestor, '/')]
			if parent := s.states[testID{event.Package, ancestor}]; parent != nil {
				parent.child = true
			}
		}
	}
	switch event.Action {
	case "run":
		if test.ran || test.outcome != "" {
			s.incomplete = true
		}
		test.ran = true
	case "output":
		_, _ = test.output.Write([]byte(event.Output))
	case "pass", "fail", "skip":
		if test.outcome != "" {
			s.incomplete = true
		}
		test.outcome = event.Action
		if event.Action == "fail" && s.failureTest == "" {
			s.failureTest, s.failurePackage, s.failure = event.Test, event.Package, test.output.String()
		}
		test.output = diagnostic{}
	}
}

func save(path string, r receipt) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b)+1 > 16<<20 {
		return errors.New("proof artifact exceeds 16MiB")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".go-check-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
