package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

// This opt-in check touches only a disposable LaunchAgent and private fixture
// cache. No GitHub access, model calls, installed skills or real services.
func TestLaunchdGracefulStopAndUpdateRestore(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("RUNNER_TEST_LAUNCHD") != "1" {
		t.Skip("set RUNNER_TEST_LAUNCHD=1 on a logged-in macOS host")
	}
	fixture := t.TempDir()
	t.Setenv("HOME", fixture)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(fixture, "cache"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	label := fmt.Sprintf("io.cortexium.runner.test-stop-%d", os.Getpid())
	target, err := launchdTarget(label)
	if err != nil {
		t.Fatal(err)
	}
	escape := func(s string) string { var out bytes.Buffer; _ = xml.EscapeText(&out, []byte(s)); return out.String() }
	plist := filepath.Join(fixture, label+".plist")
	log := filepath.Join(fixture, "helper.log")
	contents := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>-test.run=^TestRunnerLaunchdHelper$</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>1</integer>
<key>EnvironmentVariables</key><dict><key>RUNNER_LAUNCHD_TEST_HELPER</key><string>1</string><key>HOME</key><string>%s</string><key>XDG_CACHE_HOME</key><string>%s</string></dict>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>`, label, escape(executable), escape(fixture), escape(filepath.Join(fixture, "cache")), escape(log), escape(log))
	if err := os.WriteFile(plist, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = launchctl(context.Background(), "bootout", target) })
	if _, err := launchctl(t.Context(), "bootstrap", fmt.Sprintf("gui/%d", os.Geteuid()), plist); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var initial github.RuntimeState
	for initial.PID == 0 {
		workers, err := github.RunningProcesses()
		if err != nil {
			t.Fatal(err)
		}
		for _, worker := range workers {
			if worker.LaunchdService == target {
				initial = worker
			}
		}
		select {
		case <-ctx.Done():
			data, _ := os.ReadFile(log)
			t.Fatalf("fixture did not start: %s", data)
		case <-ticker.C:
		}
	}
	resume, err := prepareRunnerUpdate(ctx, executable, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if resume == nil {
		t.Fatal("managed service was not selected")
	}
	if stopped, err := launchdStopped(ctx, initial); err != nil || !stopped {
		t.Fatalf("KeepAlive service not unloaded: %v %v", stopped, err)
	}
	if err := resume(); err != nil {
		data, _ := os.ReadFile(log)
		t.Fatalf("restore: %v; helper: %s", err, data)
	}
	workers, err := github.RunningProcesses()
	if err != nil || len(workers) != 1 || workers[0].PID == initial.PID {
		t.Fatalf("restored instance: %+v %v", workers, err)
	}
	var output, diagnostic bytes.Buffer
	if code := execute(ctx, []string{"stop", "--wait", "--timeout=20s"}, bytes.NewReader(nil), &output, &diagnostic); code != 0 {
		t.Fatalf("stop CLI: %s %s", &output, &diagnostic)
	}
	if stopped, err := launchdStopped(ctx, workers[0]); err != nil || !stopped {
		t.Fatalf("CLI left job loaded: %v %v", stopped, err)
	}
	if data, err := os.ReadFile(plist); err != nil || string(data) != contents {
		t.Fatalf("stop changed service settings: %v", err)
	}
}

func TestRunnerLaunchdHelper(t *testing.T) {
	if os.Getenv("RUNNER_LAUNCHD_TEST_HELPER") != "1" {
		return
	}
	// Match the real main's SIGTERM handling while bootout unloads this job.
	_, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	executable, _ := os.Executable()
	executable, _ = filepath.EvalSymlinks(executable)
	target, err := currentLaunchdService(t.Context(), executable)
	if err != nil || target == "" {
		t.Fatalf("identity: %q %v; parent=%d XPC=%q", target, err, os.Getppid(), os.Getenv("XPC_SERVICE_NAME"))
	}
	worker, err := github.AcquireProcessLock(config.GitHubProjectConfig{Owner: "runner-stop-fixture", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Release()
	if err := worker.EnableGracefulStop(executable, target); err != nil {
		t.Fatal(err)
	}
	state, _, err := github.InspectProcessState(config.GitHubProjectConfig{Owner: "runner-stop-fixture", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	state.LastPollAt = time.Now()
	if err := worker.UpdateRuntime(state); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		requested, err := worker.StopRequested()
		if err != nil {
			t.Fatal(err)
		}
		if !requested {
			continue
		}
		state.Stopping = true
		if err := worker.UpdateRuntime(state); err != nil {
			t.Fatal(err)
		}
		if err := worker.Release(); err != nil {
			t.Fatal(err)
		}
		if err := unloadDrainedLaunchdService(context.Background(), target, os.Getpid()); err != nil {
			t.Fatal(err)
		}
		return
	}
}
