package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestGracefulStopDiscoveryRequestAndReplacement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	project := config.GitHubProjectConfig{Owner: "example", Number: 42}
	worker, err := AcquireProcessLock(project)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Release()
	if err := worker.EnableGracefulStop("/installed/runner", ""); err != nil {
		t.Fatal(err)
	}
	planner, err := AcquirePlanningLock(project)
	if err != nil {
		t.Fatal(err)
	}
	defer planner.Release()
	workers, err := RunningProcesses()
	if err != nil || len(workers) != 1 {
		t.Fatalf("workers=%+v err=%v", workers, err)
	}
	before := workers[0]
	if stop, err := worker.StopRequested(); err != nil || stop {
		t.Fatalf("unexpected request: %v %v", stop, err)
	}
	for range 2 {
		if err := RequestProcessStop(before); err != nil {
			t.Fatal(err)
		}
	}
	if stop, err := worker.StopRequested(); err != nil || !stop {
		t.Fatalf("request missing: %v %v", stop, err)
	}
	path := stopRequestPath(worker.Path, before)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("request mode: %v %v", info, err)
	}
	if err := worker.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("request survived release: %v", err)
	}
	replacement, err := AcquireProcessLock(project)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	if err := replacement.EnableGracefulStop("/installed/runner", ""); err != nil {
		t.Fatal(err)
	}
	// A delayed request for the old incarnation cannot stop the replacement,
	// even in this fixture where both incarnations use the same process PID.
	if err := RequestProcessStop(before); err != nil {
		t.Fatal(err)
	}
	if stop, err := replacement.StopRequested(); err != nil || stop {
		t.Fatalf("stale request affected replacement: %v %v", stop, err)
	}
}

func TestWorkerRefusesMalformedOversizedAndWrongIncarnationStopRequests(t *testing.T) {
	for _, variant := range []string{"malformed", "oversized", "wrong-incarnation", "public-mode"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			worker, err := AcquireProcessLock(config.GitHubProjectConfig{Owner: "example", Number: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Release()
			if err := worker.EnableGracefulStop("/installed/runner", ""); err != nil {
				t.Fatal(err)
			}
			request := worker.controlState
			if variant == "wrong-incarnation" {
				request.PID++
			}
			data, _ := json.Marshal(request)
			mode := os.FileMode(0o600)
			switch variant {
			case "malformed":
				data = []byte("{")
			case "oversized":
				data = []byte(strings.Repeat(" ", maxProcessRecordBytes+1))
			case "public-mode":
				mode = 0o644
			}
			if err := os.WriteFile(stopRequestPath(worker.Path, worker.controlState), data, mode); err != nil {
				t.Fatal(err)
			}
			if requested, err := worker.StopRequested(); err == nil || requested {
				t.Fatalf("unsafe request accepted: %v %v", requested, err)
			}
		})
	}
}

func TestGracefulStopRefusesUnsafeAndUnsupportedRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	worker, err := AcquireProcessLock(config.GitHubProjectConfig{Owner: "example", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Release()
	workers, err := RunningProcesses()
	if err != nil || len(workers) != 1 {
		t.Fatalf("%v %v", workers, err)
	}
	if err := RequestProcessStop(workers[0]); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("old worker: %v", err)
	}
	if err := worker.EnableGracefulStop("/installed/runner", ""); err != nil {
		t.Fatal(err)
	}
	workers, err = RunningProcesses()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := stopRequestPath(worker.Path, workers[0])
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := RequestProcessStop(workers[0]); err == nil {
		t.Fatal("followed a substituted stop request")
	}
	if _, err := worker.StopRequested(); err == nil {
		t.Fatal("worker accepted substituted request")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "untouched" {
		t.Fatalf("unrelated file changed: %q %v", data, err)
	}
	if err := os.Chmod(worker.Path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := RunningProcesses(); err == nil {
		t.Fatal("accepted unsafe worker record")
	}
}
