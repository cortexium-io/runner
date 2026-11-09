// runner-frontend-check verifies explicit Vue/TypeScript tests in owned scratch.
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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type options struct {
	node, modules, source, scratch, tests string
	diagnoseTimeouts                      bool
}

var errArtifactLimit = errors.New("frontend artifact exceeds its existing 16 MiB limit")

const evidenceLimit = 8 << 20

type proof struct {
	Schema             int                         `json:"schema"`
	Status             string                      `json:"status"`
	Tests              []string                    `json:"tests"`
	Executed           int                         `json:"executed"`
	Failed             int                         `json:"failed"`
	Skipped            int                         `json:"skipped"`
	Reason             string                      `json:"reason,omitempty"`
	Output             string                      `json:"output,omitempty"`
	Frontend           string                      `json:"frontend,omitempty"`
	CanonicalFrontend  string                      `json:"canonical_frontend,omitempty"`
	ExitCode           *int                        `json:"exit_code,omitempty"`
	ReportBytes        int                         `json:"report_bytes,omitempty"`
	ReportSHA256       string                      `json:"report_sha256,omitempty"`
	Files              []fileProof                 `json:"files,omitempty"`
	Vitest             json.RawMessage             `json:"vitest,omitempty"`
	VitestIncomplete   bool                        `json:"vitest_incomplete,omitempty"`
	VitestDiagnostics  *vitestDiagnostics          `json:"vitest_diagnostics,omitempty"`
	Typecheck          *typecheckProof             `json:"typecheck,omitempty"`
	TimeoutDiagnostics *frontendTimeoutDiagnostics `json:"frontend_timeout_diagnostics,omitempty"`
}

type typecheckProof struct {
	Status            string           `json:"status"`
	Node              string           `json:"node"`
	Tool              string           `json:"tool"`
	ToolSHA256        string           `json:"tool_sha256,omitempty"`
	Version           string           `json:"version,omitempty"`
	TypeScriptVersion string           `json:"typescript_version,omitempty"`
	Project           string           `json:"project"`
	ProjectSHA256     string           `json:"project_sha256,omitempty"`
	ExitCode          *int             `json:"exit_code,omitempty"`
	Complete          bool             `json:"complete"`
	Output            string           `json:"output,omitempty"`
	OutputBytes       int              `json:"output_bytes"`
	OutputSHA256      string           `json:"output_sha256,omitempty"`
	Diagnostics       []typeDiagnostic `json:"diagnostics,omitempty"`
	Reason            string           `json:"reason,omitempty"`
}

type typeDiagnostic struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type fileProof struct {
	Selected   string   `json:"selected,omitempty"`
	Reported   []string `json:"reported,omitempty"`
	Canonical  string   `json:"canonical,omitempty"`
	Assertions int      `json:"assertions"`
	Executed   int      `json:"executed"`
	Failed     int      `json:"failed"`
	Skipped    int      `json:"skipped"`
	Diagnostic string   `json:"diagnostic,omitempty"`
	identity   fs.FileInfo
}

type vitestReport struct {
	Success bool         `json:"success"`
	Total   int          `json:"numTotalTests"`
	Passed  int          `json:"numPassedTests"`
	Failed  int          `json:"numFailedTests"`
	Pending int          `json:"numPendingTests"`
	Todo    int          `json:"numTodoTests"`
	Results []vitestFile `json:"testResults"`
}

type vitestFile struct {
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	Message    string            `json:"message"`
	Assertions []vitestAssertion `json:"assertionResults"`
}

type vitestAssertion struct {
	Status          string   `json:"status"`
	FullName        string   `json:"fullName"`
	Title           string   `json:"title,omitempty"`
	AncestorTitles  []string `json:"ancestorTitles,omitempty"`
	Duration        *float64 `json:"duration,omitempty"`
	FailureMessages []string `json:"failureMessages"`
}

func main() {
	var o options
	flag.StringVar(&o.node, "node", "", "configured Node executable")
	flag.StringVar(&o.modules, "modules", "", "immutable installed frontend dependencies")
	flag.StringVar(&o.source, "source", "", "read-only candidate source")
	flag.StringVar(&o.scratch, "scratch", "", "owned scratch directory")
	flag.StringVar(&o.tests, "tests", "", "JSON array of explicit frontend-relative test files")
	flag.BoolVar(&o.diagnoseTimeouts, "diagnose-timeouts", false, "sample confirmed five-second timeouts in isolated cases after the full check")
	flag.Parse()
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func selections(raw string) ([]string, error) {
	var tests []string
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &tests) != nil || len(tests) == 0 || len(tests) > 32 {
		return nil, errors.New("frontend check requires 1..32 explicit test files as a JSON array")
	}
	seen := map[string]bool{}
	for _, name := range tests {
		if !regexp.MustCompile(`^(src|scripts)/[A-Za-z0-9_./-]+\.test\.(ts|js)$`).MatchString(name) || strings.Contains(name, "..") || filepath.ToSlash(filepath.Clean(name)) != name || seen[name] {
			return nil, errors.New("frontend test selections must be unique frontend-relative src/scripts test files, without patterns or flags")
		}
		seen[name] = true
	}
	return tests, nil
}

func run(o options) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	return runContext(ctx, o)
}

func runContext(ctx context.Context, o options) error {
	tests, err := selections(o.tests)
	if err != nil {
		return err
	}
	for _, p := range []string{o.node, o.modules, o.source, o.scratch} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errors.New("configured absolute runtime, dependency, source and scratch paths required")
		}
	}
	if err = os.MkdirAll(o.scratch, 0700); err != nil {
		return err
	}
	p := proof{Schema: 2, Status: "blocked", Tests: tests}
	finish := func(outcome, reason string) error {
		p.Status = map[string]string{"ready": "passed", "changes_requested": "failed", "blocked": "blocked"}[outcome]
		p.Reason = clip(reason, 1000)
		path := filepath.Join(o.scratch, "frontend-check.json")
		err := writeJSON(path, p)
		if errors.Is(err, errArtifactLimit) && p.TimeoutDiagnostics != nil {
			outcome, p.Status = "blocked", "blocked"
			p.Reason = "timeout diagnostic summary exceeded the existing 16 MiB artifact limit"
			p.TimeoutDiagnostics = &frontendTimeoutDiagnostics{
				Schema: 1,
				Status: "blocked",
				Method: timeoutDiagnosticMethod,
				Reason: p.Reason,
			}
			err = writeJSON(path, p)
		}
		if err != nil {
			if !errors.Is(err, errArtifactLimit) {
				return err
			}
			outcome, p.Status = "blocked", "blocked"
			p.Reason = "complete frontend evidence could not fit the existing 16 MiB artifact limit; raw evidence was not retained"
			p.Vitest = nil
			p.VitestDiagnostics = nil
			p.TimeoutDiagnostics = nil
			if p.Typecheck != nil {
				p.Typecheck.Output, p.Typecheck.Diagnostics, p.Typecheck.Complete = "", nil, false
			}
			if err = writeJSON(path, p); err != nil {
				return err
			}
		}
		if err := writeJSON(filepath.Join(o.scratch, "domain.json"), map[string]string{"outcome": outcome, "reason": clip(p.Reason, 480)}); err != nil {
			return err
		}
		summary := fmt.Sprintf("frontend-check status=%s executed=%d failed=%d skipped=%d; %s", p.Status, p.Executed, p.Failed, p.Skipped, p.Reason)
		fmt.Println(clip(summary, 480))
		return nil // The domain result distinguishes source feedback from unestablished execution.
	}
	copy := filepath.Join(o.scratch, "source")
	if err = os.Mkdir(copy, 0700); err != nil {
		return finish("blocked", "frontend scratch preparation: "+err.Error())
	}
	for _, name := range []string{"frontend", "internal", "server", "cmd", "migrations", ".genny", "go.mod", "go.sum"} {
		if err = copySource(filepath.Join(o.source, name), filepath.Join(copy, name)); err != nil {
			return finish("blocked", "frontend source preparation: "+err.Error())
		}
	}
	// Existing tests import checked-in JSON fixtures; copy only actual imports,
	// not archived screenshots/build packages or unrelated historical evidence.
	imported, err := fixtureImports(o.source)
	if err != nil {
		return finish("blocked", "frontend fixture preparation: "+err.Error())
	}
	for _, name := range imported {
		if err = copySource(filepath.Join(o.source, name), filepath.Join(copy, name)); err != nil {
			return finish("blocked", "frontend fixture preparation: "+err.Error())
		}
	}
	frontend := filepath.Join(copy, "frontend")
	p.Frontend = frontend
	p.CanonicalFrontend, err = filepath.EvalSymlinks(frontend)
	if err != nil {
		return finish("blocked", "frontend source identity: "+err.Error())
	}
	for _, test := range tests {
		path := filepath.Join(frontend, filepath.FromSlash(test))
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			return finish("blocked", "selected frontend test is missing or not a regular file: "+test)
		}
		canonical, e := filepath.EvalSymlinks(path)
		if e != nil {
			return finish("blocked", "selected frontend test identity: "+e.Error())
		}
		p.Files = append(p.Files, fileProof{Selected: test, Canonical: canonical, identity: info})
	}
	deps := filepath.Join(frontend, "node_modules")
	if err = os.Mkdir(deps, 0700); err != nil {
		return finish("blocked", "frontend dependency preparation: "+err.Error())
	}
	entries, err := os.ReadDir(o.modules)
	if err != nil {
		return finish("blocked", "frontend dependency preparation: "+err.Error())
	}
	for _, entry := range entries {
		if entry.Name() == ".vite" || entry.Name() == ".vite-temp" || entry.Name() == ".cache" {
			continue
		}
		if err = os.Symlink(filepath.Join(o.modules, entry.Name()), filepath.Join(deps, entry.Name())); err != nil {
			return finish("blocked", "frontend dependency preparation: "+err.Error())
		}
	}
	p.Typecheck = checkTypes(ctx, o, frontend, p.CanonicalFrontend)
	if p.Typecheck.Status == "blocked" {
		return finish("blocked", p.Typecheck.Reason)
	}
	report := filepath.Join(o.scratch, "vitest.json")
	junit := filepath.Join(o.scratch, "vitest-junit.xml")
	for _, file := range []string{report, junit} {
		if _, e := os.Lstat(file); !errors.Is(e, os.ErrNotExist) {
			return finish("blocked", "frontend report output already exists or cannot be identified; refusing stale evidence")
		}
	}
	vitest := filepath.Join(deps, "vitest", "vitest.mjs")
	args := append(vitestArguments(vitest, report, junit), tests...)
	command := exec.CommandContext(ctx, o.node, args...)
	command.Dir = frontend
	command.WaitDelay = 2 * time.Second
	command.Env = append(os.Environ(), "CI=1", "NO_COLOR=1")
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()
	p.Output = output.String()
	if ctx.Err() != nil {
		return finish("blocked", "frontend check cancelled or deadline exceeded; complete execution is not established")
	}
	var exit *exec.ExitError
	executionProblem := ""
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		executionProblem = fmt.Sprintf("configured frontend checker failed to execute: %v", err)
	}
	code := 0
	if exit != nil {
		code = exit.ExitCode()
	}
	p.ExitCode = &code
	info, readErr := os.Lstat(report)
	if readErr != nil || !info.Mode().IsRegular() {
		if executionProblem != "" {
			return finish("blocked", executionProblem+"; no regular completion report")
		}
		return finish("blocked", "frontend test report is missing or not a regular file; test completion is not established")
	}
	input, readErr := os.Open(report)
	if readErr != nil {
		return finish("blocked", "frontend test report is missing; test completion is not established")
	}
	opened, statErr := input.Stat()
	if statErr != nil || !os.SameFile(info, opened) {
		_ = input.Close()
		return finish("blocked", "frontend test report identity changed while opening")
	}
	remaining := evidenceLimit - p.Typecheck.OutputBytes
	data, readErr := io.ReadAll(io.LimitReader(input, int64(remaining)+1))
	readErr = errors.Join(readErr, input.Close())
	if readErr != nil || len(data) > remaining || !json.Valid(data) {
		return finish("blocked", "frontend test report is unreadable, oversized or malformed; test completion is not established")
	}
	p.Vitest, p.ReportBytes = json.RawMessage(data), len(data)
	p.ReportSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	if executionProblem != "" {
		return finish("blocked", executionProblem)
	}
	outcome, reason := evaluate(&p, code, junit, remaining-len(data))
	if o.diagnoseTimeouts && outcome == "changes_requested" && p.VitestDiagnostics != nil && p.Typecheck.Complete && p.Typecheck.Status == "passed" {
		p.TimeoutDiagnostics = diagnoseFrontendTimeouts(ctx, o, &p, vitest)
		if p.TimeoutDiagnostics != nil && p.TimeoutDiagnostics.Status != "complete" {
			return finish("blocked", p.TimeoutDiagnostics.Reason)
		}
	}
	if outcome != "blocked" && p.Typecheck.Status == "failed" {
		if outcome == "changes_requested" {
			return finish("changes_requested", p.Typecheck.Reason+"; "+reason)
		}
		return finish("changes_requested", p.Typecheck.Reason)
	}
	return finish(outcome, reason)
}

func vitestArguments(vitest, report, junit string) []string {
	return []string{
		vitest,
		"run",
		"--reporter=json",
		"--reporter=junit",
		"--outputFile.json=" + report,
		"--outputFile.junit=" + junit,
	}
}

func checkTypes(ctx context.Context, o options, frontend, canonicalFrontend string) *typecheckProof {
	p := &typecheckProof{
		Status:  "blocked",
		Node:    o.node,
		Tool:    filepath.Join(o.modules, "vue-tsc", "bin", "vue-tsc.js"),
		Project: "tsconfig.json",
	}
	blocked := func(reason string) *typecheckProof {
		p.Complete = false
		p.Reason = clip("frontend typecheck: "+reason, 1000)
		return p
	}
	for _, pkg := range []struct {
		name    string
		version *string
	}{
		{name: "vue-tsc", version: &p.Version},
		{name: "typescript", version: &p.TypeScriptVersion},
	} {
		data, err := runtimeFile(o.modules, filepath.Join(pkg.name, "package.json"), 64<<10)
		var metadata struct{ Name, Version string }
		if err != nil || json.Unmarshal(data, &metadata) != nil || metadata.Name != pkg.name || metadata.Version == "" {
			return blocked("configured " + pkg.name + " metadata is missing or invalid")
		}
		*pkg.version = metadata.Version
	}
	tool, err := runtimeFile(o.modules, filepath.Join("vue-tsc", "bin", "vue-tsc.js"), evidenceLimit)
	if err != nil {
		return blocked("configured vue-tsc entry is missing or outside its immutable modules")
	}
	p.ToolSHA256 = digest(tool)
	if _, err = runtimeFile(o.modules, filepath.Join("typescript", "lib", "typescript.js"), 16<<20); err != nil {
		return blocked("configured TypeScript runtime is missing or outside its immutable modules")
	}
	project, err := runtimeFile(frontend, p.Project, evidenceLimit)
	if err != nil {
		return blocked("tsconfig.json is missing or not a regular owned project")
	}
	p.ProjectSHA256 = digest(project)
	active, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &completeOutput{limit: evidenceLimit, cancel: cancel}
	command := exec.CommandContext(active, o.node, p.Tool, "--noEmit", "--pretty", "false", "--project", p.Project)
	command.Dir = frontend
	command.Env = append(os.Environ(), "CI=1", "NO_COLOR=1")
	command.WaitDelay = 2 * time.Second
	command.Stdout, command.Stderr = output, output
	err = command.Run()
	p.Output, p.OutputBytes = output.String(), output.Len()
	p.OutputSHA256 = digest(output.Bytes())
	if output.overflow {
		return blocked("complete compiler output exceeded the existing 8 MiB raw evidence limit")
	}
	if !utf8.Valid(output.Bytes()) {
		return blocked("compiler output is not valid UTF-8; complete diagnostics are not established")
	}
	if ctx.Err() != nil {
		return blocked("cancelled or deadline exceeded; complete compiler execution is not established")
	}
	var exit *exec.ExitError
	code := 0
	if err != nil {
		if !errors.As(err, &exit) || exit.ExitCode() < 0 {
			return blocked(fmt.Sprintf("configured compiler did not complete: %v", err))
		}
		code = exit.ExitCode()
	}
	p.ExitCode, p.Complete = &code, true
	if code == 0 && strings.TrimSpace(p.Output) == "" {
		p.Status, p.Reason = "passed", "Configured Vue/TypeScript noEmit check passed"
		return p
	}
	if code != 1 && code != 2 {
		return blocked("compiler exit and diagnostics do not establish a source failure")
	}
	pattern := regexp.MustCompile(`^(.+)\(([0-9]+),([0-9]+)\): error TS([0-9]+): (.*)$`)
	for _, line := range strings.Split(p.Output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			if len(p.Diagnostics) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
				continue // Complete continuation text is retained in Output.
			}
			return blocked("compiler output includes an unestablished runtime or project diagnostic")
		}
		path := match[1]
		if !filepath.IsAbs(path) {
			if !fs.ValidPath(filepath.ToSlash(path)) {
				return blocked("compiler diagnostic is outside the owned frontend")
			}
			path = filepath.Join(frontend, path)
		}
		canonical, err := reportedIdentity(canonicalFrontend, path)
		if err != nil {
			return blocked("compiler diagnostic is not a regular owned frontend file")
		}
		info, statErr := os.Stat(canonical)
		ext := filepath.Ext(canonical)
		if statErr != nil || !info.Mode().IsRegular() || ext != ".ts" && ext != ".tsx" && ext != ".vue" {
			return blocked("compiler diagnostic is not a regular owned TypeScript or Vue source")
		}
		lineNumber, lineErr := strconv.Atoi(match[2])
		column, columnErr := strconv.Atoi(match[3])
		code, codeErr := strconv.Atoi(match[4])
		if lineErr != nil || columnErr != nil || codeErr != nil || lineNumber < 1 || column < 1 || code < 1 || code == 2307 || code == 2688 || code == 6053 {
			return blocked("compiler diagnostic concerns missing input/dependencies or invalid location; source failure is not established")
		}
		p.Diagnostics = append(p.Diagnostics, typeDiagnostic{
			File:    canonical,
			Line:    lineNumber,
			Column:  column,
			Code:    code,
			Message: clip(match[5], 500),
		})
	}
	if len(p.Diagnostics) == 0 {
		return blocked("compiler returned no conclusive source diagnostics")
	}
	first := p.Diagnostics[0]
	rel, _ := filepath.Rel(canonicalFrontend, first.File)
	p.Status = "failed"
	p.Reason = clip(fmt.Sprintf("%s:%d:%d TS%d: %s", filepath.ToSlash(rel), first.Line, first.Column, first.Code, first.Message), 1000)
	return p
}

func runtimeFile(root, name string, limit int64) ([]byte, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	canonical, err := reportedIdentity(canonicalRoot, filepath.Join(root, name))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("runtime or project file is missing, not regular or oversized")
	}
	return os.ReadFile(canonical)
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

type completeOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *completeOutput) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		n, _ := b.Buffer.Write(data[:b.limit-b.Len()])
		b.overflow = true
		b.cancel()
		return n, errors.New("raw compiler evidence limit exceeded")
	}
	return b.Buffer.Write(data)
}

func (b *completeOutput) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, r)
}

func parseVitestReport(data []byte) (vitestReport, error) {
	var result vitestReport
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &result) != nil || json.Unmarshal(data, &fields) != nil {
		return result, errors.New("frontend report schema is invalid; test completion is not established")
	}
	for _, key := range []string{"success", "numTotalTests", "numPassedTests", "numFailedTests", "numPendingTests", "numTodoTests", "testResults"} {
		if raw, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return result, errors.New("frontend report is missing required field: " + key)
		}
	}
	return result, nil
}

func evaluate(p *proof, exitCode int, junitPath string, remainingEvidence int) (string, string) {
	result, err := parseVitestReport(p.Vitest)
	if err != nil {
		return "blocked", err.Error()
	}
	p.Executed, p.Failed, p.Skipped = result.Passed+result.Failed, result.Failed, result.Pending+result.Todo
	if result.Total < 0 || result.Passed < 0 || result.Failed < 0 || result.Pending < 0 || result.Todo < 0 || len(result.Results) > 64 {
		return "blocked", "frontend report has invalid or excessive counts"
	}
	selected := make(map[string]int, len(p.Files))
	for i, file := range p.Files {
		selected[file.Canonical] = i
	}
	seen := make(map[string]bool, len(selected))
	problem, assertionFailure, restoredFailure := "", "", ""
	diagnostics := vitestDiagnosticResolver{path: junitPath, limit: remainingEvidence}
	transformFailures := 0
	compilerFailure := p.Typecheck != nil && p.Typecheck.Complete && p.Typecheck.Status == "failed" && len(p.Typecheck.Diagnostics) > 0
	normalFailure := exitCode == 1 && !result.Success
	passed, failed, skipped, assertions := 0, 0, 0, 0
	for _, file := range result.Results {
		canonical, identityErr := reportedIdentity(p.CanonicalFrontend, file.Name)
		i, exists := selected[canonical]
		if identityErr != nil || !exists {
			message := "unexpected frontend report file: " + clip(file.Name, 300)
			if identityErr != nil {
				message += "; " + identityErr.Error()
			}
			p.Files = append(p.Files, fileProof{Reported: []string{clip(file.Name, 512)}, Canonical: clip(canonical, 512), Diagnostic: message})
			if problem == "" {
				problem = message
			}
			continue
		}
		f := &p.Files[i]
		if len(f.Reported) < 2 {
			f.Reported = append(f.Reported, clip(file.Name, 512))
		}
		reported, statErr := os.Stat(file.Name)
		if statErr != nil || !reported.Mode().IsRegular() || f.identity == nil || !os.SameFile(f.identity, reported) {
			f.Diagnostic = "reported file differs from the selected source identity"
			if problem == "" {
				problem = f.Selected + ": " + f.Diagnostic
			}
			continue
		}
		if seen[canonical] {
			f.Diagnostic = "selected file appears more than once, including canonical aliases"
			if problem == "" {
				problem = f.Selected + ": " + f.Diagnostic
			}
			continue
		}
		seen[canonical] = true
		f.Assertions = len(file.Assertions)
		sourceTransform := file.Status == "failed" && len(file.Assertions) == 0 && strings.HasPrefix(strings.TrimSpace(file.Message), "Transform failed with ")
		if compilerFailure && normalFailure && sourceTransform {
			// The compiler independently proved an owned-source failure. Keep
			// suite loading explicitly incomplete, without inventing assertions.
			transformFailures++
			f.Diagnostic = "assertions did not load: " + clip(file.Message, 400)
			continue
		}
		if len(file.Assertions) == 0 || file.Status != "passed" && file.Status != "failed" {
			f.Diagnostic = "selected file has no assertions or an invalid completion status: " + clip(file.Message, 400)
			if problem == "" {
				problem = f.Selected + ": " + f.Diagnostic
			}
		}
		for _, assertion := range file.Assertions {
			assertions++
			if assertion.FullName == "" && problem == "" {
				problem = f.Selected + ": assertion identity is missing"
			}
			switch assertion.Status {
			case "passed":
				passed++
				f.Executed++
			case "failed":
				failed++
				f.Executed++
				f.Failed++
				messages := assertion.FailureMessages
				if hasVitestPlaceholder(messages) {
					resolved, err := diagnostics.resolve(f.Selected, file.Assertions, assertion)
					if err != nil {
						f.Diagnostic = "unresolved Vitest failure for " + clip(assertion.FullName, 200) + ": " + err.Error()
						if problem == "" {
							problem = f.Selected + ": " + f.Diagnostic
						}
						continue
					}
					p.VitestDiagnostics = &diagnostics.proof
					messages = resolved
					if restoredFailure == "" {
						first := diagnostics.proof.Failures[0]
						restoredFailure = fmt.Sprintf("%s: failed assertion: %s (%.3fms elapsed): %s", f.Selected, clip(first.FullName, 200), first.DurationMS, clip(first.Message, 500))
					}
				}
				if f.Diagnostic == "" {
					f.Diagnostic = "failed assertion: " + clip(assertion.FullName, 200)
					if len(messages) > 0 {
						f.Diagnostic += ": " + clip(messages[0], 500)
					}
				}
				if assertionFailure == "" {
					assertionFailure = f.Selected + ": " + f.Diagnostic
				}
			case "pending", "skipped", "todo", "disabled":
				skipped++
				f.Skipped++
				if f.Diagnostic == "" {
					f.Diagnostic = "required assertion did not execute: " + clip(assertion.FullName, 300)
				}
			default:
				if problem == "" {
					problem = f.Selected + ": unknown assertion status " + clip(assertion.Status, 100)
				}
			}
		}
		if f.Failed > 0 && file.Status != "failed" || f.Failed == 0 && file.Status == "failed" {
			f.Diagnostic = "file completion status is inconsistent with its assertions: " + clip(file.Message, 400)
			if problem == "" {
				problem = f.Selected + ": " + f.Diagnostic
			}
		}
	}
	for i := range p.Tests {
		file := &p.Files[i]
		if !seen[file.Canonical] {
			file.Diagnostic = "selected file omitted from report"
			if problem == "" {
				problem = "frontend report omitted selected file: " + file.Selected
			}
		}
	}
	if problem != "" {
		return "blocked", problem
	}
	if transformFailures > 0 {
		p.VitestIncomplete = true
		if assertions != result.Total || passed != result.Passed || failed != result.Failed || skipped != result.Pending+result.Todo || result.Total != passed+failed+skipped || skipped != 0 {
			return "blocked", "frontend transform failure has inconsistent assertion counts or skipped tests"
		}
		return "changes_requested", "source transform prevented selected assertions; complete compiler and Vitest diagnostics retained"
	}
	if result.Total == 0 || assertions != result.Total || passed != result.Passed || failed != result.Failed || skipped != result.Pending+result.Todo || result.Total != passed+failed+skipped {
		return "blocked", "frontend report assertion counts do not establish complete selected-file execution"
	}
	if skipped != 0 {
		for _, file := range p.Files {
			if file.Skipped != 0 {
				return "blocked", file.Selected + ": required assertion did not execute; " + file.Diagnostic
			}
		}
	}
	if failed > 0 && !result.Success && exitCode == 1 {
		if restoredFailure != "" {
			return "changes_requested", restoredFailure
		}
		return "changes_requested", assertionFailure
	}
	if failed != 0 || !result.Success || exitCode != 0 {
		return "blocked", "frontend process exit/report status is inconsistent with executed assertions"
	}
	return "ready", "Explicit frontend tests executed against the retained candidate; browser acceptance follows"
}

func reportedIdentity(frontend, name string) (string, error) {
	if !filepath.IsAbs(name) || len(name) > 4096 || filepath.Clean(name) != name {
		return "", errors.New("report file must have an absolute clean identity")
	}
	canonical, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", errors.New("reported file cannot be resolved")
	}
	rel, err := filepath.Rel(frontend, canonical)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return canonical, errors.New("reported file is outside the owned frontend")
	}
	return canonical, nil
}

func clip(s string, limit int) string {
	if len(s) > limit {
		end := limit - len("…")
		if end < 0 {
			return strings.Repeat(".", limit)
		}
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		return s[:end] + "…"
	}
	return s
}

func copySource(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("source copy refuses symlinks: " + source)
	}
	if info.IsDir() {
		if err = os.MkdirAll(target, 0700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == "node_modules" || entry.Name() == ".git" {
				continue
			}
			if err = copySource(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("source copy requires regular files")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	return errors.Join(err, output.Close())
}

func fixtureImports(source string) ([]string, error) {
	imports := regexp.MustCompile(`(?:from\s*|import\s*\()['"]([^'"\n]+\.json)['"]`)
	seen := map[string]bool{}
	var names []string
	err := filepath.WalkDir(filepath.Join(source, "frontend"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".vue") {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("frontend source contains a symlink")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range imports.FindAllSubmatch(data, -1) {
			ref := string(match[1])
			if filepath.IsAbs(ref) {
				return fmt.Errorf("imported JSON fixture must be repository-relative: %s", ref)
			}
			if !strings.HasPrefix(ref, ".") {
				continue
			}
			name, err := filepath.Rel(source, filepath.Join(filepath.Dir(path), ref))
			if err != nil || !fs.ValidPath(filepath.ToSlash(name)) {
				return fmt.Errorf("imported JSON fixture escapes the source: %s", ref)
			}
			if !strings.HasPrefix(filepath.ToSlash(name), "docs/") && !strings.HasPrefix(filepath.ToSlash(name), "testdata/") {
				continue
			}
			if !seen[name] {
				if err := regularFixture(source, name); err != nil {
					return err
				}
				seen[name] = true
				names = append(names, name)
			}
		}
		return nil
	})
	return names, err
}

func regularFixture(source, name string) error {
	if !fs.ValidPath(filepath.ToSlash(name)) {
		return errors.New("imported JSON fixture path is not clean and contained")
	}
	root, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.ToSlash(name), "/")
	for i, part := range parts {
		root = filepath.Join(root, part)
		info, err := os.Lstat(root)
		if err != nil {
			return fmt.Errorf("imported JSON fixture %s: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("imported JSON fixture %s requires regular directories and a regular file without symlinks", name)
		}
	}
	return nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 16 << 10
	if n >= limit {
		b.Reset()
		p = p[n-limit:]
	} else if b.Len()+n > limit {
		b.Next(b.Len() + n - limit)
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func (b *boundedOutput) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, r)
}
func writeJSON(path string, value any) error {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if data.Len() > 16<<20 {
		return errArtifactLimit
	}
	return os.WriteFile(path, data.Bytes(), 0600)
}
