package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	timeoutDiagnosticMethod = "node-v8-cpu-sampling-isolated-case-v1"
	frontendTimeoutMS       = 5000
	maxDiagnosticCases      = 8
	maxDiagnosticNameBytes  = 512
	maxRawCaseEvidence      = 4 << 20
)

type frontendTimeoutDiagnostics struct {
	Schema int                   `json:"schema"`
	Status string                `json:"status"`
	Method string                `json:"method"`
	Cases  []frontendTimeoutCase `json:"cases"`
	Reason string                `json:"reason,omitempty"`
}

type frontendTimeoutCase struct {
	File                 string              `json:"file"`
	FullName             string              `json:"full_name"`
	TimeoutMS            int                 `json:"timeout_ms"`
	OriginalDurationMS   float64             `json:"original_duration_ms"`
	DiagnosticDurationMS float64             `json:"diagnostic_duration_ms,omitempty"`
	Status               string              `json:"status"`
	Reason               string              `json:"reason,omitempty"`
	Profiles             []timeoutCPUProfile `json:"profiles,omitempty"`
}

func fiveSecondTimeout(message string) bool {
	return strings.HasPrefix(message, "Test timed out in 5000ms.\n")
}

func diagnoseFrontendTimeouts(ctx context.Context, o options, p *proof, vitest string) *frontendTimeoutDiagnostics {
	d := &frontendTimeoutDiagnostics{Schema: 1, Status: "blocked", Method: timeoutDiagnosticMethod}
	seen := make(map[[2]string]bool)
	for _, failure := range p.VitestDiagnostics.Failures {
		if !fiveSecondTimeout(failure.Message) {
			continue
		}
		key := [2]string{failure.File, failure.FullName}
		if seen[key] || len(d.Cases) == maxDiagnosticCases || len(failure.FullName) > maxDiagnosticNameBytes || failure.DurationMS <= 0 {
			d.Reason = "confirmed timeout identities are ambiguous or exceed the diagnostic bound"
			return d
		}
		seen[key] = true
		d.Cases = append(d.Cases, frontendTimeoutCase{
			File:               failure.File,
			FullName:           failure.FullName,
			TimeoutMS:          frontendTimeoutMS,
			OriginalDurationMS: failure.DurationMS,
			Status:             "blocked",
		})
	}
	if len(d.Cases) == 0 || len(d.Cases) != p.Failed {
		return nil
	}
	modules, err := timeoutDiagnosticRuntime(ctx, o)
	if err != nil {
		d.Reason = err.Error()
		return d
	}
	for i := range d.Cases {
		c := &d.Cases[i]
		if err := sampleTimeoutCase(ctx, o, p, vitest, modules, c); err != nil {
			c.Status, c.Reason = "blocked", clip(err.Error(), 400)
			d.Reason = "frontend timeout diagnosis incomplete for " + clip(c.File, 200) + ": " + c.Reason
			return d
		}
	}
	if ctx.Err() != nil {
		d.Reason = "original frontend deadline interrupted timeout diagnostics"
		return d
	}
	d.Status = "complete"
	return d
}

func timeoutDiagnosticRuntime(ctx context.Context, o options) (string, error) {
	modules, err := filepath.EvalSymlinks(o.modules)
	if err != nil {
		return "", errors.New("configured profiling dependency path is unavailable")
	}
	data, err := runtimeFile(o.modules, "vitest/package.json", 64<<10)
	var pkg struct{ Version string }
	if err != nil || json.Unmarshal(data, &pkg) != nil || pkg.Version != "4.1.11" {
		return "", errors.New("timeout diagnostics require the qualified Vitest 4.1.11 reporter and worker contract")
	}
	version := exec.CommandContext(ctx, o.node, "--version")
	version.WaitDelay = 2 * time.Second
	var output boundedOutput
	version.Stdout = &output
	if err := version.Run(); err != nil || strings.TrimSpace(output.String()) != "v24.21.0" {
		return "", errors.New("timeout diagnostics require the qualified Node 24.21.0 CPU profile format")
	}
	return modules, nil
}

func sampleTimeoutCase(ctx context.Context, o options, p *proof, vitest, modules string, c *frontendTimeoutCase) (problem error) {
	if ctx.Err() != nil {
		return errors.New("original frontend deadline exhausted")
	}
	parent, err := os.OpenRoot(o.scratch)
	if err != nil {
		return errors.New("cannot open owned timeout diagnostic scratch")
	}
	defer parent.Close()
	dir, err := os.MkdirTemp(o.scratch, "timeout-diagnostic-")
	if err != nil {
		return errors.New("cannot prepare owned timeout diagnostic scratch")
	}
	name := filepath.Base(dir)
	owned, err := parent.Lstat(name)
	if err != nil || !owned.IsDir() {
		return errors.New("cannot identify owned timeout diagnostic scratch")
	}
	report, junit := filepath.Join(dir, "vitest.json"), filepath.Join(dir, "vitest-junit.xml")
	// Run only the positively identified case. Leave the configured timeout,
	// pool, project configuration and overall attempt deadline in force.
	args := []string{"--cpu-prof", "--cpu-prof-dir=" + dir, "--cpu-prof-interval=10000"}
	args = append(args, vitestArguments(vitest, report, junit)...)
	args = append(args, "--testNamePattern=^"+regexp.QuoteMeta(c.FullName)+"$(?![\\s\\S])", c.File)
	cmd := exec.CommandContext(ctx, o.node, args...)
	cmd.Dir, cmd.WaitDelay = p.Frontend, 2*time.Second
	cmd.Env = append(os.Environ(), "CI=1", "NO_COLOR=1")
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		_ = parent.Remove(name)
		return errors.New("configured timeout diagnostic did not start")
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return errors.New("original frontend deadline interrupted timeout diagnostics")
	}
	var exit *exec.ExitError
	code := 0
	if err != nil {
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return errors.New("isolated timeout diagnostic did not settle with a test result")
		}
		code = exit.ExitCode()
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return errors.New("cannot open isolated CPU profile directory")
	}
	defer root.Close()
	entries, size, err := timeoutScratchEntries(root, owned)
	if err != nil {
		return err
	}
	defer func() { problem = errors.Join(problem, removeTimeoutScratch(parent, root, name, owned, entries)) }()
	if size > maxRawCaseEvidence {
		return errors.New("isolated CPU profile and reporter files exceed raw case evidence capacity")
	}
	// Only one case's raw evidence exists at a time. Its bounded summary is
	// the sole additional evidence placed under the existing artifact cap.
	available := maxRawCaseEvidence
	data, err := readDiagnosticEvidence(root, filepath.Base(report), available, "isolated Vitest report")
	if err != nil {
		return err
	}
	available -= len(data)
	outcome, duration, used, err := isolatedTimeoutOutcome(p, *c, data, root, filepath.Base(junit), code, available)
	if err != nil {
		return err
	}
	available -= used
	c.DiagnosticDurationMS = duration
	c.Profiles, err = readTimeoutProfiles(root, entries, cmd.Process.Pid, filepath.Dir(p.CanonicalFrontend), modules, &available)
	if err != nil {
		return err
	}
	c.Status = outcome
	return nil
}

func isolatedTimeoutOutcome(p *proof, c frontendTimeoutCase, data []byte, root *os.Root, junit string, code, available int) (string, float64, int, error) {
	invalid := errors.New("isolated Vitest report does not establish exactly the named test case")
	report, err := parseVitestReport(data)
	if err != nil || len(report.Results) != 1 {
		return "", 0, 0, invalid
	}
	file := report.Results[0]
	canonical, err := reportedIdentity(p.CanonicalFrontend, file.Name)
	if err != nil || canonical != filepath.Join(p.CanonicalFrontend, filepath.FromSlash(c.File)) || file.Message != "" || len(file.Assertions) != report.Total {
		return "", 0, 0, invalid
	}
	original, err := os.Stat(canonical)
	matched := false
	for _, source := range p.Files {
		if source.Selected == c.File && source.identity != nil && err == nil && original.Mode().IsRegular() && os.SameFile(source.identity, original) && source.identity.Size() == original.Size() && source.identity.ModTime().Equal(original.ModTime()) {
			matched = true
		}
	}
	if !matched {
		return "", 0, 0, invalid
	}
	var named vitestAssertion
	found, skipped := 0, 0
	for _, test := range file.Assertions {
		if test.FullName == c.FullName {
			named = test
			found++
			continue
		}
		switch test.Status {
		case "skipped", "pending", "disabled", "todo":
			skipped++
		default:
			return "", 0, 0, invalid
		}
	}
	if found != 1 || named.Duration == nil || *named.Duration <= 0 || math.IsInf(*named.Duration, 0) || skipped != report.Pending+report.Todo || report.Pending < 0 || report.Todo < 0 {
		return "", 0, 0, invalid
	}
	if code == 0 && report.Success && file.Status == "passed" && named.Status == "passed" && report.Passed == 1 && report.Failed == 0 && len(named.FailureMessages) == 0 && *named.Duration < c.OriginalDurationMS {
		return "isolated_pass", *named.Duration, 0, nil
	}
	if code != 1 || report.Success || file.Status != "failed" || named.Status != "failed" || report.Passed != 0 || report.Failed != 1 || len(named.FailureMessages) != 1 || !hasVitestPlaceholder(named.FailureMessages) {
		return "", 0, 0, errors.New("isolated case neither passed nor reproduced a confirmed five-second timeout")
	}
	resolver := vitestDiagnosticResolver{path: junit, root: root, limit: available}
	messages, err := resolver.resolve(c.File, file.Assertions, named)
	if err != nil {
		return "", 0, 0, fmt.Errorf("isolated case did not verify the original five-second timeout: %w", err)
	}
	if len(messages) != 1 || !fiveSecondTimeout(messages[0]) {
		return "", 0, 0, errors.New("isolated case did not reproduce the original five-second timeout")
	}
	return "timeout_reproduced", *named.Duration, resolver.proof.ReportBytes, nil
}
