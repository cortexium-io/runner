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
	"strings"
	"time"
	"unicode/utf8"
)

type options struct{ node, modules, source, scratch, tests string }

var errArtifactLimit = errors.New("frontend artifact exceeds its existing 16 MiB limit")

type proof struct {
	Schema            int             `json:"schema"`
	Status            string          `json:"status"`
	Tests             []string        `json:"tests"`
	Executed          int             `json:"executed"`
	Failed            int             `json:"failed"`
	Skipped           int             `json:"skipped"`
	Reason            string          `json:"reason,omitempty"`
	Output            string          `json:"output,omitempty"`
	Frontend          string          `json:"frontend,omitempty"`
	CanonicalFrontend string          `json:"canonical_frontend,omitempty"`
	ExitCode          *int            `json:"exit_code,omitempty"`
	ReportBytes       int             `json:"report_bytes,omitempty"`
	ReportSHA256      string          `json:"report_sha256,omitempty"`
	Files             []fileProof     `json:"files,omitempty"`
	Vitest            json.RawMessage `json:"vitest,omitempty"`
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
	FailureMessages []string `json:"failureMessages"`
}

func main() {
	var o options
	flag.StringVar(&o.node, "node", "", "configured Node executable")
	flag.StringVar(&o.modules, "modules", "", "immutable installed frontend dependencies")
	flag.StringVar(&o.source, "source", "", "read-only candidate source")
	flag.StringVar(&o.scratch, "scratch", "", "owned scratch directory")
	flag.StringVar(&o.tests, "tests", "", "JSON array of explicit frontend-relative test files")
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
		if err := writeJSON(filepath.Join(o.scratch, "frontend-check.json"), p); err != nil {
			if !errors.Is(err, errArtifactLimit) {
				return err
			}
			outcome, p.Status = "blocked", "blocked"
			p.Reason = "complete frontend evidence could not fit the existing 16 MiB artifact limit; raw report was not retained"
			p.Vitest = nil
			if err = writeJSON(filepath.Join(o.scratch, "frontend-check.json"), p); err != nil {
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
	report := filepath.Join(o.scratch, "vitest.json")
	if _, e := os.Lstat(report); !errors.Is(e, os.ErrNotExist) {
		return finish("blocked", "frontend report output already exists or cannot be identified; refusing stale evidence")
	}
	args := append([]string{filepath.Join(deps, "vitest", "vitest.mjs"), "run", "--reporter=json", "--outputFile", report}, tests...)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
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
		return errors.New("frontend check cancelled or deadline exceeded; execution must settle before replacement")
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
	data, readErr := io.ReadAll(io.LimitReader(input, (8<<20)+1))
	readErr = errors.Join(readErr, input.Close())
	if readErr != nil || len(data) > 8<<20 || !json.Valid(data) {
		return finish("blocked", "frontend test report is unreadable, oversized or malformed; test completion is not established")
	}
	p.Vitest, p.ReportBytes = json.RawMessage(data), len(data)
	p.ReportSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	if executionProblem != "" {
		return finish("blocked", executionProblem)
	}
	outcome, reason := evaluate(&p, code)
	return finish(outcome, reason)
}

func evaluate(p *proof, exitCode int) (string, string) {
	var result vitestReport
	var fields map[string]json.RawMessage
	if json.Unmarshal(p.Vitest, &result) != nil || json.Unmarshal(p.Vitest, &fields) != nil {
		return "blocked", "frontend report schema is invalid; test completion is not established"
	}
	for _, key := range []string{"success", "numTotalTests", "numPassedTests", "numFailedTests", "numPendingTests", "numTodoTests", "testResults"} {
		if raw, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return "blocked", "frontend report is missing required field: " + key
		}
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
	problem, assertionFailure := "", ""
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
				if f.Diagnostic == "" {
					f.Diagnostic = "failed assertion: " + clip(assertion.FullName, 200)
					if len(assertion.FailureMessages) > 0 {
						f.Diagnostic += ": " + clip(assertion.FailureMessages[0], 500)
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
