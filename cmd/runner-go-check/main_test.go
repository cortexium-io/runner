package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "go-fake" {
		fakeGo()
		return
	}
	if filepath.Base(os.Args[0]) == "reporter-fake" {
		main()
		return
	}
	os.Exit(m.Run())
}

func fakeGo() {
	dir := filepath.Dir(os.Args[0])
	mode, _ := os.ReadFile(filepath.Join(dir, "mode"))
	if string(mode) == "group" {
		alive, _ := os.OpenFile(filepath.Join(dir, "alive"), os.O_CREATE|os.O_RDWR, 0600)
		_ = syscall.Flock(int(alive.Fd()), syscall.LOCK_EX)
		defer alive.Close()
	}
	file, _ := os.OpenFile(filepath.Join(dir, "calls"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	_ = json.NewEncoder(file).Encode(struct {
		Args      []string
		Cache     string
		PID, PGID int
	}{os.Args[1:], os.Getenv("GOCACHE"), os.Getpid(), syscall.Getpgrp()})
	_ = file.Close()
	selector := ""
	for i, arg := range os.Args {
		if arg == "-run" {
			selector = os.Args[i+1]
		}
	}
	development := strings.HasPrefix(selector, "^TestDevelopment$")
	emit := func(action, test, output string) {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": action, "Package": "fixture", "Test": test, "Output": output})
	}
	if strings.HasPrefix(string(mode), "packages-") {
		for _, pkg := range []string{"fixture/a", "fixture/b"} {
			put := func(action, test string) {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": action, "Package": pkg, "Test": test})
			}
			put("start", "")
			put("run", "TestSame")
			outcome := "pass"
			if string(mode) == "packages-fail" && pkg == "fixture/a" {
				outcome = "fail"
			}
			if string(mode) != "packages-unfinished-test" || pkg != "fixture/a" {
				put(outcome, "TestSame")
			}
			if string(mode) != "packages-unfinished-package" || pkg != "fixture/a" {
				put(outcome, "")
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": "skip", "Package": "fixture/no-tests"})
		if string(mode) == "packages-fail" {
			os.Exit(1)
		}
		return
	}
	if strings.HasPrefix(string(mode), "build-") {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": "build-output", "ImportPath": "fixture [fixture.test]", "Output": "fixture_test.go:3: undefined: missing\n"})
		if string(mode) != "build-missing-terminal" {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": "build-fail", "ImportPath": "fixture [fixture.test]"})
			// Go can report several compiler errors for the same build identity.
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": "build-output", "ImportPath": "fixture [fixture.test]", "Output": "fixture_test.go:4: undefined: secondMissing\n"})
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": "build-fail", "ImportPath": "fixture [fixture.test]"})
		}
		emit("start", "", "")
		failedBuild := "fixture [fixture.test]"
		if string(mode) == "build-wrong-link" {
			failedBuild = "foreign"
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": "fail", "Package": "fixture", "FailedBuild": failedBuild})
		os.Exit(1)
	}
	if string(mode) == "wait" || string(mode) == "group" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if string(mode) == "no-tests" {
		emit("pass", "", "")
		return
	}
	if string(mode) == "runtime-fail" {
		emit("start", "", "")
		emit("fail", "", "")
		os.Exit(1)
	}
	parent := "TestRegression"
	if development {
		parent = "TestDevelopment"
	}
	emit("run", parent, "")
	if string(mode) == "skip" {
		emit("run", parent+"/required", "")
		emit("skip", parent+"/required", "")
		emit("pass", parent, "")
		emit("pass", "", "")
		return
	}
	if string(mode) == "incomplete" {
		fmt.Fprintln(os.Stdout, "malformed")
		emit("output", parent, strings.Repeat("x", maxEvent))
	}
	for _, child := range []string{"negative", "boundary"} {
		emit("run", parent+"/"+child, "")
		emit("pass", parent+"/"+child, "")
	}
	if string(mode) == "mixed-skip" {
		emit("run", parent+"/required", "")
		emit("skip", parent+"/required", "")
	}
	if string(mode) == "fail" {
		emit("run", parent+"/constructor", "")
		emit("output", parent+"/constructor", "green diagnostic is not constructor success\n"+strings.Repeat("large diagnostic\n", 12000))
		emit("output", parent+"/constructor", "late t.Errorf: constructor rejected required start\n")
		emit("fail", parent+"/constructor", "")
		emit("fail", parent, "")
		emit("fail", "", "")
		os.Exit(1)
	}
	emit("pass", parent, "")
	emit("pass", "", "")
}

func fixture(t *testing.T, mode string) options {
	t.Helper()
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(dir, "go-fake")
	if err := os.Symlink(binary, cli); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	return options{
		goBinary:    cli,
		pkg:         "./fixture",
		regression:  "^TestRegression$",
		development: "^TestDevelopment$/^(16|20|30)$/^easy$",
		scratch:     dir,
		output:      filepath.Join(dir, "proof.json"),
		timeout:     10 * time.Second,
	}
}

func calls(t *testing.T, o options) []struct {
	Args  []string
	Cache string
} {
	t.Helper()
	f, err := os.Open(filepath.Join(o.scratch, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []struct {
		Args  []string
		Cache string
	}
	decoder := json.NewDecoder(f)
	for decoder.More() {
		var call struct {
			Args  []string
			Cache string
		}
		if err := decoder.Decode(&call); err != nil {
			t.Fatal(err)
		}
		out = append(out, call)
	}
	return out
}

func TestSequentialLiteralScopesPreserveChildren(t *testing.T) {
	o := fixture(t, "pass")
	t.Setenv("GOCACHE", filepath.Join(o.scratch, "same-cache"))
	if err := validate(o); err != nil {
		t.Fatal(err)
	}
	r := run(context.Background(), o)
	for _, scope := range []scopeResult{r.Regression, r.Development} {
		if scope.Status != "passed" || scope.Passed != 3 || scope.LeafPassed != 2 || scope.EvidenceIncomplete || !scope.ChildExited {
			t.Fatalf("lost child evidence: %+v", scope)
		}
	}
	want := []testResult{{Name: "TestRegression", Outcome: "pass"}, {Name: "TestRegression/boundary", Outcome: "pass"}, {Name: "TestRegression/negative", Outcome: "pass"}}
	if !reflect.DeepEqual(r.Regression.Tests, want) {
		t.Fatalf("incorrect child outcomes: %+v", r.Regression.Tests)
	}
	observed := calls(t, o)
	if len(observed) != 2 || observed[0].Cache != observed[1].Cache || observed[0].Cache != os.Getenv("GOCACHE") {
		t.Fatalf("not exactly two launches with one cache: %+v", observed)
	}
	for i, selector := range []string{o.regression, o.development} {
		wantArgs := []string{"test", "-json", "-mod=readonly", "-count=1", "-timeout", "10s", "-run", selector, o.pkg}
		if !reflect.DeepEqual(observed[i].Args, wantArgs) {
			t.Fatalf("selector was altered: %q", observed[i].Args)
		}
	}
	if err := save(o.output, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(o.output)
	if err != nil {
		t.Fatal(err)
	}
	var saved receipt
	if err := json.Unmarshal(b, &saved); err != nil || !reflect.DeepEqual(saved, r) {
		t.Fatalf("invalid proof receipt: %v", err)
	}
}

// Bare Runner package paths must select the local package offline, rather than
// making Go resolve an import path and unrelated missing module metadata.
func TestWorkspaceRelativePackageStaysOffline(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "internal", "puzzles")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/proof\n\ngo 1.20\n\nrequire example.invalid/uncached v1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "puzzles_test.go"), []byte("package puzzles\nimport \"testing\"\nfunc TestRegression(t *testing.T) {}\nfunc TestDevelopment(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for name, value := range map[string]string{
		"GOENV":       "off",
		"GOWORK":      "off",
		"GOTOOLCHAIN": "local",
		"GOPROXY":     "off",
		"GOSUMDB":     "off",
		"GOMODCACHE":  filepath.Join(root, "modules"),
		"GOCACHE":     filepath.Join(root, "build-cache"),
	} {
		t.Setenv(name, value)
	}
	for _, name := range []string{"internal/puzzles", "./internal/puzzles"} {
		t.Run(name, func(t *testing.T) {
			o := options{
				goBinary:    filepath.Join(runtime.GOROOT(), "bin", "go"),
				pkg:         name,
				regression:  "^TestRegression$",
				development: "^TestDevelopment$",
				timeout:     45 * time.Second,
			}
			r := run(context.Background(), o)
			if r.Regression.Status != "passed" || r.Development.Status != "passed" || r.Regression.LeafPassed != 1 || r.Development.LeafPassed != 1 {
				t.Fatalf("local package did not execute offline: %+v", r)
			}
		})
	}
	second, empty := filepath.Join(root, "second"), filepath.Join(root, "empty")
	for _, dir := range []string{second, empty} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(second, "second_test.go"), []byte("package second\nimport \"testing\"\nfunc TestRegression(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(empty, "empty.go"), []byte("package empty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	o := options{
		goBinary:  filepath.Join(runtime.GOROOT(), "bin", "go"),
		pkg:       "...",
		selection: "^TestRegression$",
		timeout:   45 * time.Second,
	}
	r := run(context.Background(), o)
	if exitCode(r) != 0 || r.Scope.Passed != 2 || len(r.Scope.Packages) != 3 {
		t.Fatalf("real Go multi-package receipt lost selected proof: %+v", r.Scope)
	}
	if err := os.WriteFile(filepath.Join(pkg, "broken_test.go"), []byte("package puzzles\nvar _ = undefinedSourceSymbol\n"), 0600); err != nil {
		t.Fatal(err)
	}
	o.pkg = "internal/puzzles"
	r = run(context.Background(), o)
	if exitCode(r) != 1 || len(r.Scope.Tests) != 0 || len(r.Scope.BuildFailures) == 0 || !strings.Contains(r.Scope.BuildFailures[0].Diagnostic, "undefinedSourceSymbol") {
		t.Fatalf("real Go build events did not identify settled source failure: %+v", r.Scope)
	}
}

func TestFailedRegressionKeepsEarlierOutcomesAndStops(t *testing.T) {
	o := fixture(t, "fail")
	r := run(context.Background(), o)
	if r.Regression.Status != "failed" || r.Regression.Failed != 2 || r.Regression.Passed != 2 || r.Regression.EvidenceIncomplete || r.Development.Status != "not_run" || len(calls(t, o)) != 1 {
		t.Fatalf("dishonest partial failure: %+v", r)
	}
	diagnostic := r.Regression.FirstFailureDiagnostic
	if r.Regression.FirstFailureTest != "TestRegression/constructor" || !strings.HasPrefix(diagnostic, "green diagnostic") || !strings.Contains(diagnostic, "late t.Errorf") || len(diagnostic) > maxDiagnostic {
		t.Fatalf("first failure diagnostics were lost/unbounded: %q", diagnostic)
	}
	for _, result := range r.Regression.Tests {
		if result.Name == "TestRegression/constructor" && result.Outcome != "fail" {
			t.Fatal("constructor success invented from diagnostic")
		}
	}
	var stdout bytes.Buffer
	if err := printProof(&stdout, r); err != nil || !strings.Contains(stdout.String(), "TestRegression/constructor") || !strings.Contains(stdout.String(), "late t.Errorf") {
		t.Fatalf("failed proof absent from bounded stdout: %v", err)
	}
}

func TestStdoutProofOmitsNamesExplicitlyAtCap(t *testing.T) {
	r := receipt{Package: "fixture", Regression: scopeResult{Status: "failed", Failed: 1024, FirstFailureTest: "TestFirst", Reason: "test failed"}}
	for i := 0; i < 1024; i++ {
		r.Regression.Tests = append(r.Regression.Tests, testResult{Name: fmt.Sprintf("TestLong/%d/%s", i, strings.Repeat("x", 400)), Outcome: "fail"})
	}
	var stdout bytes.Buffer
	if err := printProof(&stdout, r); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() > maxStdoutProof || !json.Valid(bytes.TrimSpace(stdout.Bytes())) || !strings.Contains(stdout.String(), `"test_names_omitted":true`) || !strings.Contains(stdout.String(), `"failed":1024`) || !strings.Contains(stdout.String(), "TestFirst") || len(r.Regression.Tests) != 1024 {
		t.Fatalf("unbounded or misleading stdout proof: bytes=%d", stdout.Len())
	}
}

func TestOuterOwnedGroupCancellationReachesGo(t *testing.T) {
	o := fixture(t, "group")
	binary, _ := os.Executable()
	reporter := filepath.Join(o.scratch, "reporter-fake")
	if err := os.Symlink(binary, reporter); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(reporter, "--go", o.goBinary, "--package", o.pkg, "--regression-run", o.regression, "--development-run", o.development, "--timeout", "10s", "--scratch", o.scratch, "--output", o.output)
	// This is the outer Runner-owner stand-in, not a reporter-created child group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	deadline := time.Now().Add(3 * time.Second)
	var observed struct{ PID, PGID int }
	for {
		b, err := os.ReadFile(filepath.Join(o.scratch, "calls"))
		if err == nil && json.Unmarshal(b, &observed) == nil && observed.PID != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake Go did not become ready")
		}
		time.Sleep(5 * time.Millisecond) // Synchronize on the fake child's explicit readiness record.
	}
	if observed.PGID != cmd.Process.Pid {
		t.Fatalf("Go escaped outer owned group: %+v, owner=%d", observed, cmd.Process.Pid)
	}
	alive, err := os.OpenFile(filepath.Join(o.scratch, "alive"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer alive.Close()
	if err := syscall.Flock(int(alive.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("fake Go did not retain its live-process lock")
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("outer cancellation did not stop reporter")
	}
	for {
		if err := syscall.Flock(int(alive.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Go retained a live lock after outer group cancellation")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMissingOrIncompleteProofDoesNotPass(t *testing.T) {
	for _, mode := range []string{"no-tests", "skip", "mixed-skip", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			o := fixture(t, mode)
			r := run(context.Background(), o)
			if r.Regression.Status != "failed" || r.Development.Status != "not_run" || len(calls(t, o)) != 1 {
				t.Fatalf("missing proof accepted: %+v", r)
			}
			if mode == "incomplete" && (!r.Regression.EvidenceIncomplete || r.Regression.Passed != 3) {
				t.Fatalf("events after oversized/malformed lines were not drained: %+v", r.Regression)
			}
			if mode == "skip" && (r.Regression.Passed != 1 || r.Regression.Skipped != 1 || r.Regression.LeafPassed != 0) {
				t.Fatalf("parent pass hid skipped required proof: %+v", r.Regression)
			}
			if mode == "mixed-skip" && (r.Regression.Passed != 3 || r.Regression.Skipped != 1 || !strings.Contains(r.Regression.Reason, "missing")) {
				t.Fatalf("passing siblings hid missing proof: %+v", r.Regression)
			}
		})
	}
}

func TestScopeTimeoutSettlesChild(t *testing.T) {
	o := fixture(t, "wait")
	o.timeout = time.Second
	r := run(context.Background(), o)
	if r.Regression.Status != "failed" || !r.Regression.ChildExited || !r.Regression.EvidenceIncomplete || r.Development.Status != "not_run" || !strings.Contains(r.Regression.Reason, "deadline") {
		t.Fatalf("timeout did not settle child: %+v", r)
	}
}

func TestSelectorsAndArtifactBoundary(t *testing.T) {
	o := fixture(t, "pass")
	for _, change := range []func(*options){
		func(o *options) { o.regression = "^TestRegression$/^positive$" },
		func(o *options) { o.regression = "^Test" },
		func(o *options) { o.development = "^TestDevelopment.*$" },
		func(o *options) { o.development = "^TestDevelopment$/easy" },
		func(o *options) { o.output = filepath.Join(filepath.Dir(o.scratch), "escape.json") },
	} {
		bad := o
		change(&bad)
		if err := validate(bad); err == nil {
			t.Fatalf("invalid selection/boundary accepted: %+v", bad)
		}
	}
}

func TestSingleScopeKeepsPackageIdentityAndCompleteReceipt(t *testing.T) {
	o := fixture(t, "packages-pass")
	o.pkg, o.selection, o.regression, o.development = "...", "^TestSame$", "", ""
	if err := validate(o); err != nil {
		t.Fatal(err)
	}
	r := run(context.Background(), o)
	if r.Package != "./..." || r.Scope == nil || r.Scope.Passed != 2 || r.Scope.LeafPassed != 2 || len(r.Scope.Packages) != 3 || exitCode(r) != 0 {
		t.Fatalf("single package scope lost or invented evidence: %+v", r)
	}
	want := []testResult{
		{Package: "fixture/a", Name: "TestSame", Outcome: "pass"},
		{Package: "fixture/b", Name: "TestSame", Outcome: "pass"},
	}
	if !reflect.DeepEqual(r.Scope.Tests, want) {
		t.Fatalf("same test name in separate packages conflated: %+v", r.Scope.Tests)
	}
	observed := calls(t, o)
	wantArgs := []string{"test", "-json", "-mod=readonly", "-count=1", "-timeout", "10s", "-run", o.selection, "./..."}
	if len(observed) != 1 || !reflect.DeepEqual(observed[0].Args, wantArgs) {
		t.Fatalf("single scope retried or altered: %+v", observed)
	}
	if err := save(o.output, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(o.output)
	var saved receipt
	if err != nil || json.Unmarshal(b, &saved) != nil || !reflect.DeepEqual(saved, r) || bytes.Contains(b, []byte(`"regression"`)) || bytes.Contains(b, []byte(`"development"`)) {
		t.Fatalf("full single-scope artifact missing or misleading: %s; %v", b, err)
	}
}

func TestSingleScopeDistinguishesSourceFailureAndIncompleteProof(t *testing.T) {
	for _, test := range []struct {
		mode string
		exit int
	}{
		{"pass", 0},
		{"fail", 1},
		{"packages-fail", 1},
		{"build-fail", 1},
		{"build-missing-terminal", 2},
		{"build-wrong-link", 2},
		{"runtime-fail", 2},
		{"no-tests", 2},
		{"skip", 2},
		{"mixed-skip", 2},
		{"incomplete", 2},
		{"packages-unfinished-test", 2},
		{"packages-unfinished-package", 2},
	} {
		t.Run(test.mode, func(t *testing.T) {
			o := fixture(t, test.mode)
			o.selection, o.regression, o.development = "^Test", "", ""
			r := run(context.Background(), o)
			if got := exitCode(r); got != test.exit || r.Scope == nil || !r.Scope.ChildExited || len(calls(t, o)) != 1 {
				t.Fatalf("wrong source/collection outcome %d: %+v", got, r.Scope)
			}
			if test.mode == "build-fail" && (len(r.Scope.Tests) != 0 || r.Scope.Failed != 0 || len(r.Scope.BuildFailures) != 1 || !strings.Contains(r.Scope.BuildFailures[0].Diagnostic, "undefined: missing")) {
				t.Fatalf("build failure was hidden or claimed as executed tests: %+v", r.Scope)
			}
		})
	}
}

func TestSingleScopeCLIOutcomesRetainArtifact(t *testing.T) {
	for _, test := range []struct {
		mode string
		exit int
	}{{"pass", 0}, {"fail", 1}, {"no-tests", 2}} {
		t.Run(test.mode, func(t *testing.T) {
			o := fixture(t, test.mode)
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			reporter := filepath.Join(o.scratch, "reporter-fake")
			if err := os.Symlink(binary, reporter); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(reporter, "--go", o.goBinary, "--package", "...", "--run", "^Test", "--timeout", "10s", "--scratch", o.scratch, "--output", o.output)
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != test.exit {
				t.Fatalf("CLI exit mismatch: %v; %s", err, output)
			}
			data, err := os.ReadFile(o.output)
			var r receipt
			if err != nil || json.Unmarshal(data, &r) != nil || r.Scope == nil || exitCode(r) != test.exit {
				t.Fatalf("CLI discarded failed/missing proof: %s; %v", data, err)
			}
		})
	}
}

func TestSingleScopeSelectionIsExclusiveAndRepositoryRelative(t *testing.T) {
	o := fixture(t, "pass")
	o.selection, o.regression, o.development = "^TestSelected$", "", ""
	for _, pkg := range []string{"...", "./...", ".", "internal/puzzles", "./internal/puzzles"} {
		o.pkg = pkg
		if err := validate(o); err != nil {
			t.Fatalf("valid scoped package %q rejected: %v", pkg, err)
		}
	}
	for _, change := range []func(*options){
		func(o *options) { o.selection = "[" },
		func(o *options) { o.regression = "^TestRegression$" },
		func(o *options) { o.development = "^TestDevelopment$" },
		func(o *options) { o.pkg = "../outside" },
		func(o *options) { o.pkg = "/outside" },
		func(o *options) { o.pkg = "internal/..." },
		func(o *options) { o.pkg = "./internal/../../outside" },
	} {
		bad := o
		change(&bad)
		if err := validate(bad); err == nil {
			t.Fatalf("invalid single scope accepted: %+v", bad)
		}
	}
}
