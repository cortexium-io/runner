// runner-frontend-check verifies explicit Vue/TypeScript tests in owned scratch.
package main

import (
	"bytes"
	"context"
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
)

type options struct{ node, modules, source, scratch, tests string }
type proof struct {
	Status   string   `json:"status"`
	Tests    []string `json:"tests"`
	Executed int      `json:"executed"`
	Failed   int      `json:"failed"`
	Skipped  int      `json:"skipped"`
	Reason   string   `json:"reason,omitempty"`
	Output   string   `json:"output,omitempty"`
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
	p := proof{Status: "failed", Tests: tests}
	finish := func(cause error) error {
		if cause != nil {
			p.Reason = cause.Error()
		}
		if err := writeJSON(filepath.Join(o.scratch, "frontend-check.json"), p); err != nil {
			return err
		}
		outcome := "ready"
		if cause != nil {
			outcome = "changes_requested"
		}
		if err := writeJSON(filepath.Join(o.scratch, "domain.json"), map[string]string{"outcome": outcome, "reason": p.Reason}); err != nil {
			return err
		}
		fmt.Printf("frontend-check status=%s executed=%d failed=%d skipped=%d; %s\n", p.Status, p.Executed, p.Failed, p.Skipped, p.Reason)
		return nil // A conclusively failed test is source feedback, not an operator retry.
	}
	copy := filepath.Join(o.scratch, "source")
	if err = os.Mkdir(copy, 0700); err != nil {
		return err
	}
	for _, name := range []string{"frontend", "internal", "server", "cmd", "migrations", ".genny", "go.mod", "go.sum"} {
		if err = copySource(filepath.Join(o.source, name), filepath.Join(copy, name)); err != nil {
			return err
		}
	}
	// Existing tests import checked-in JSON fixtures; copy only actual imports,
	// not archived screenshots/build packages or unrelated historical evidence.
	imported, err := fixtureImports(o.source)
	if err != nil {
		return err
	}
	for _, name := range imported {
		if err = copySource(filepath.Join(o.source, name), filepath.Join(copy, name)); err != nil {
			return err
		}
	}
	frontend := filepath.Join(copy, "frontend")
	for _, test := range tests {
		info, e := os.Lstat(filepath.Join(frontend, filepath.FromSlash(test)))
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("selected frontend test is missing or not a regular file: " + test)
		}
	}
	deps := filepath.Join(frontend, "node_modules")
	if err = os.Mkdir(deps, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(o.modules)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".vite" || entry.Name() == ".vite-temp" || entry.Name() == ".cache" {
			continue
		}
		if err = os.Symlink(filepath.Join(o.modules, entry.Name()), filepath.Join(deps, entry.Name())); err != nil {
			return err
		}
	}
	report := filepath.Join(o.scratch, "vitest.json")
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
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return fmt.Errorf("configured frontend checker failed to execute: %w", err)
	}
	input, readErr := os.Open(report)
	if readErr != nil {
		return errors.New("frontend test report is missing; test completion is not established")
	}
	data, readErr := io.ReadAll(io.LimitReader(input, (8<<20)+1))
	readErr = errors.Join(readErr, input.Close())
	var result struct {
		Success bool `json:"success"`
		Total   int  `json:"numTotalTests"`
		Passed  int  `json:"numPassedTests"`
		Failed  int  `json:"numFailedTests"`
		Pending int  `json:"numPendingTests"`
		Todo    int  `json:"numTodoTests"`
		Results []struct {
			Assertions []struct {
				Status          string   `json:"status"`
				FullName        string   `json:"fullName"`
				FailureMessages []string `json:"failureMessages"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}
	if readErr != nil || len(data) > 8<<20 || json.Unmarshal(data, &result) != nil {
		return errors.New("frontend test report is invalid; test completion is not established")
	}
	p.Executed, p.Failed, p.Skipped = result.Passed+result.Failed, result.Failed, result.Pending+result.Todo
	assertions := 0
	for _, file := range result.Results {
		for _, assertion := range file.Assertions {
			assertions++
			if assertion.Status != "passed" {
				message := "required frontend assertion failed or skipped: " + assertion.FullName
				if len(assertion.FailureMessages) > 0 {
					message += ": " + assertion.FailureMessages[0]
				}
				if len(message) > 400 {
					message = message[:400]
				}
				return finish(errors.New(message))
			}
		}
	}
	if err != nil || !result.Success || result.Total == 0 || result.Passed != result.Total || result.Failed != 0 || p.Skipped != 0 || assertions != result.Total {
		return finish(errors.New("required frontend tests did not all execute and pass"))
	}
	p.Status = "passed"
	p.Reason = "Explicit frontend tests executed against the retained candidate; browser acceptance follows"
	return finish(nil)
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
			if !strings.HasPrefix(ref, ".") {
				continue
			}
			name, err := filepath.Rel(source, filepath.Join(filepath.Dir(path), ref))
			if err != nil || !strings.HasPrefix(filepath.ToSlash(name), "docs/") {
				continue
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
		return nil
	})
	return names, err
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
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
