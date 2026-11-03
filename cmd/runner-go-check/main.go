// runner-go-check preserves focused Go proof without retaining test logs.
package main

import (
	"bytes"
	"context"
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
)

type options struct {
	goBinary, pkg, regression, development, scratch, output string
	timeout                                                 time.Duration
}

type testResult struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
}

type scopeResult struct {
	Selector               string       `json:"selector"`
	Status                 string       `json:"status"`
	Reason                 string       `json:"reason,omitempty"`
	ExitCode               *int         `json:"exit_code"`
	ChildExited            bool         `json:"child_exited"`
	ElapsedSeconds         float64      `json:"elapsed_seconds"`
	Passed                 int          `json:"passed"`
	Failed                 int          `json:"failed"`
	Skipped                int          `json:"skipped"`
	LeafPassed             int          `json:"leaf_passed"`
	EvidenceIncomplete     bool         `json:"evidence_incomplete"`
	Tests                  []testResult `json:"tests"`
	FirstFailureTest       string       `json:"first_failure_test,omitempty"`
	FirstFailureDiagnostic string       `json:"first_failure_diagnostic,omitempty"`
}

type receipt struct {
	Package     string      `json:"package"`
	Regression  scopeResult `json:"regression"`
	Development scopeResult `json:"development"`
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
		os.Exit(1)
	}
	if saveErr != nil {
		fmt.Fprintln(os.Stderr, "go-check: could not save proof artifact")
		os.Exit(1)
	}
	fmt.Printf("%s; %s; artifact=%s\n", summary("regression", r.Regression), summary("development", r.Development), o.output)
	if r.Regression.Status != "passed" || r.Development.Status != "passed" {
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	var o options
	f := flag.NewFlagSet("runner-go-check", flag.ContinueOnError)
	f.StringVar(&o.goBinary, "go", "", "absolute pinned Go executable")
	f.StringVar(&o.pkg, "package", "", "one workspace-relative Go package directory")
	f.StringVar(&o.regression, "regression-run", "", "top-level regression regex; slash is forbidden")
	f.StringVar(&o.development, "development-run", "", "exact anchored development function, optionally anchored subtest regexes")
	f.StringVar(&o.scratch, "scratch", "", "existing caller-owned scratch directory")
	f.StringVar(&o.output, "output", "", "absolute JSON artifact path under scratch")
	f.DurationVar(&o.timeout, "timeout", 0, "required Go test timeout; allows 2s settlement grace")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	return o, validate(o)
}

func validate(o options) error {
	if !filepath.IsAbs(o.goBinary) || o.pkg == "" || len(o.pkg) > 1024 || strings.HasPrefix(o.pkg, "-") || strings.ContainsAny(o.pkg, " \t\r\n") || strings.Contains(o.pkg, "...") || o.timeout <= 0 || o.timeout > time.Duration(1<<63-1)-settlementGrace {
		return errors.New("absolute --go, one --package and positive --timeout are required")
	}
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
		b, err = json.Marshal(struct {
			TestNamesOmitted bool    `json:"test_names_omitted"`
			Proof            receipt `json:"proof"`
		}{true, r})
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func run(ctx context.Context, o options) receipt {
	if filepath.IsLocal(o.pkg) && o.pkg != "." && !strings.HasPrefix(o.pkg, "./") {
		o.pkg = "./" + o.pkg
	}
	r := receipt{
		Package:     o.pkg,
		Regression:  scopeResult{Selector: o.regression, Status: "not_run", Tests: []testResult{}},
		Development: scopeResult{Selector: o.development, Status: "not_run", Reason: "regression proof did not pass", Tests: []testResult{}},
	}
	env := os.Environ() // Both scopes reuse precisely the same environment/cache.
	r.Regression = runScope(ctx, o, o.regression, env)
	if r.Regression.Status == "passed" {
		r.Development = runScope(ctx, o, o.development, env)
	}
	return r
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
	events := &eventStream{states: make(map[string]*testState)}
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
	r.EvidenceIncomplete = events.incomplete || events.packageOutcome == "" || ctx.Err() != nil
	for name, state := range events.states {
		if !state.ran || state.outcome == "" {
			r.EvidenceIncomplete = true
		}
		if state.outcome != "" {
			r.Tests = append(r.Tests, testResult{Name: name, Outcome: state.outcome})
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
	sort.Slice(r.Tests, func(i, j int) bool { return r.Tests[i].Name < r.Tests[j].Name })
	r.FirstFailureTest, r.FirstFailureDiagnostic = events.failureTest, events.failure
	if events.failureTest == "" && r.FirstFailureDiagnostic == "" && (err != nil || events.packageOutcome != "pass") {
		r.FirstFailureDiagnostic = events.packageOutput.String()
	}
	if events.failureTest == "" && r.FirstFailureDiagnostic == "" && err != nil {
		r.FirstFailureDiagnostic = stderr.String()
	}
	switch {
	case ctx.Err() != nil:
		r.Reason = "scope cancelled or deadline reached"
	case err != nil || r.Failed > 0 || events.packageOutcome == "fail":
		r.Reason = "Go command or a test failed"
	case r.EvidenceIncomplete || events.packageOutcome != "pass":
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

type eventStream struct {
	line                                 []byte
	dropping, incomplete                 bool
	states                               map[string]*testState
	packageOutcome, failure, failureTest string
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
	var event struct{ Action, Package, Test, Output string }
	if json.Unmarshal(s.line, &event) != nil || event.Action == "" {
		s.incomplete = true
		return
	}
	if event.Test == "" {
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			s.packageOutcome = event.Action
		}
		if event.Action == "output" || event.Action == "build-output" {
			_, _ = s.packageOutput.Write([]byte(event.Output))
		}
		return
	}
	if len(event.Test) > maxName {
		s.incomplete = true
		return
	}
	state := s.states[event.Test]
	if state == nil {
		if len(s.states) >= maxTests {
			s.incomplete = true
			return
		}
		state = &testState{}
		s.states[event.Test] = state
		for ancestor := event.Test; strings.Contains(ancestor, "/"); {
			ancestor = ancestor[:strings.LastIndexByte(ancestor, '/')]
			if parent := s.states[ancestor]; parent != nil {
				parent.child = true
			}
		}
	}
	switch event.Action {
	case "run":
		state.ran = true
	case "output":
		_, _ = state.output.Write([]byte(event.Output))
	case "pass", "fail", "skip":
		if state.outcome != "" {
			s.incomplete = true
		}
		state.outcome = event.Action
		if event.Action == "fail" && s.failureTest == "" {
			s.failureTest, s.failure = event.Test, state.output.String()
		}
		state.output = diagnostic{}
	}
}

func save(path string, r receipt) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".go-check-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(r)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
