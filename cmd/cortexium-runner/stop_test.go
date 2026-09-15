package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

func TestStopCLIWithoutConfigFindsWorkersAndNeverKillsOnTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var out, diagnostic bytes.Buffer
	if code := execute(t.Context(), []string{"stop"}, strings.NewReader(""), &out, &diagnostic); code != 0 || !strings.Contains(out.String(), "No matching") {
		t.Fatalf("empty stop: %d %s %s", code, &out, &diagnostic)
	}
	worker, err := github.AcquireProcessLock(config.GitHubProjectConfig{Owner: "example", Number: 42})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Release()
	if err := worker.EnableGracefulStop("/installed/runner", ""); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	code := execute(t.Context(), []string{"stop", "--wait", "--timeout", "10ms"}, strings.NewReader(""), &out, &diagnostic)
	if code == 0 || !strings.Contains(diagnostic.String(), "no work was killed") {
		t.Fatalf("timeout: %d %s", code, &diagnostic)
	}
	if requested, err := worker.StopRequested(); err != nil || !requested {
		t.Fatalf("request lost: %v %v", requested, err)
	}
	process, active, err := github.InspectProcessState(config.GitHubProjectConfig{Owner: "example", Number: 42})
	if err != nil || !active {
		t.Fatalf("worker interrupted: %v %v", active, err)
	}
	process.Stopping, process.Active = true, 2
	if err := worker.UpdateRuntime(process); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostic.Reset()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if code := execute(ctx, []string{"stop"}, strings.NewReader(""), &out, &diagnostic); code != 0 || !strings.Contains(out.String(), "finishing 2") {
		t.Fatalf("acknowledgment: %d %s %s", code, &out, &diagnostic)
	}
}

func TestStopCLIOptionalFilterLeavesOtherProjectUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cfg := completeCLITestConfig("/project")
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	selected, err := github.AcquireProcessLock(*cfg.GitHubProject)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Release()
	other, err := github.AcquireProcessLock(config.GitHubProjectConfig{Owner: "other", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	for _, worker := range []*github.ProcessLock{selected, other} {
		if err := worker.EnableGracefulStop("/installed/runner", ""); err != nil {
			t.Fatal(err)
		}
	}
	state, _, err := github.InspectProcessState(*cfg.GitHubProject)
	if err != nil {
		t.Fatal(err)
	}
	state.Stopping = true
	if err := selected.UpdateRuntime(state); err != nil {
		t.Fatal(err)
	}
	if err := runStop(t.Context(), []string{"--config", path, "--timeout=1s"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if stop, err := selected.StopRequested(); err != nil || !stop {
		t.Fatalf("selected: %v %v", stop, err)
	}
	if stop, err := other.StopRequested(); err != nil || stop {
		t.Fatalf("unrelated: %v %v", stop, err)
	}
	if resume, err := prepareRunnerUpdate(t.Context(), "/installed/runner", io.Discard); err == nil || resume != nil {
		t.Fatal("update silently took over an existing stop/foreground worker")
	}
}

func TestRestartRefusesChangedServiceDefinitionBeforeCallingLaunchd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.plist")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := runnerServiceDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restartUpdatedRunner(t.Context(), stoppedRunnerService{plist: path, digest: digest}); err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("changed definition: %v", err)
	}
}

func TestLaunchdIdentityParsingRejectsNestedAndForeignTargets(t *testing.T) {
	output := "job = {\n\tpid = 123\n\tenvironment = {\n\t\tpid = 999\n\t}\n\tprogram = /installed/runner\n}\n"
	if launchdValue(output, "pid") != "123" || launchdValue(output, "program") != "/installed/runner" {
		t.Fatal("wrong launchd identity")
	}
	for _, invalid := range []string{"system/io.runner", "gui/999999/io.runner", "gui/1/../io.runner"} {
		if validateLaunchdTarget(invalid) == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	target, err := launchdTarget("io.cortexium.runner.test")
	if err != nil || validateLaunchdTarget(target) != nil {
		t.Fatalf("own service rejected: %q %v", target, err)
	}
}
