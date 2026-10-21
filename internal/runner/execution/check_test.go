package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

// Native containment qualification, opt-in, no paid model invocation.
func TestLiveQualityCheckBoundary(t *testing.T) {
	if os.Getenv("RUNNER_LOCAL_LIVE_CHECKS") == "" {
		t.Skip("set RUNNER_LOCAL_LIVE_CHECKS=1 for native Codex sandbox checks")
	}
	// Keep the host fixture outside the workspace and system scratch directories.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, "runner-quality-checks-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Retained native evidence:", root)
	t.Setenv("RUNNER_CHECK_SECRET", "must-not-inherit")
	run := func(t *testing.T, name string, check config.QualityCheck) core.CheckObservation {
		t.Helper()
		source := filepath.Join(root, name)
		if err := os.MkdirAll(source, 0700); err != nil {
			t.Fatal(err)
		}
		r := (Codex{}).Check(t.Context(), source, check, filepath.Join(root, name+".log"), func(p core.Process) error {
			data, err := json.Marshal(p)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, name+"-process.json"), data, 0600)
		})
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+"-result.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		return r
	}
	var baseline core.CheckObservation
	for _, name := range []string{"baseline", "matching", "changed", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			message := "known failure"
			if name == "changed" {
				message = "new failure"
			}
			check := config.QualityCheck{ID: "legacy", Command: `test -z "$RUNNER_CHECK_SECRET" || exit 125; printf '%s\n' '` + message + `'; exit 1`, Directory: ".", Prerequisites: []string{"sh"}, Timeout: "30s", Path: os.Getenv("PATH")}
			if name == "unavailable" {
				check.Prerequisites = []string{"runner-deliberately-missing-tool"}
			}
			r := run(t, name, check)
			if name == "unavailable" {
				if r.Status != "unavailable" {
					t.Fatal(r)
				}
				return
			}
			if r.Status != "failed" || r.ExitCode != 1 || !r.Stopped || r.OutputDigest == "" {
				t.Fatal(r)
			}
			if name == "baseline" {
				baseline = r
			} else if (r.OutputDigest == baseline.OutputDigest) != (name == "matching") {
				t.Fatal("failure fingerprint mismatch", r)
			}
		})
	}
	t.Run("filesystem_and_network", func(t *testing.T) {
		// These fixtures are accessible to the same user outside the sandbox.
		protected := filepath.Join(root, "protected")
		created := filepath.Join(root, "must-not-exist")
		if err := os.WriteFile(protected, []byte("outside workspace\n"), 0600); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("reachable\n"))
		}))
		defer server.Close()
		if output, err := exec.Command("curl", "--noproxy", "*", "--max-time", "5", "-fsS", server.URL).CombinedOutput(); err != nil || string(output) != "reachable\n" {
			t.Fatalf("network fixture unavailable outside sandbox: %s, %v", output, err)
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
		check := config.QualityCheck{
			ID: "boundary", Directory: ".", Prerequisites: []string{"sh", "cat", "curl"}, Timeout: "30s", Path: os.Getenv("PATH"),
			Command: "cat " + quote(protected) + " && exit 1; " +
				"(printf changed > " + quote(protected) + "); " +
				"(printf created > " + quote(created) + "); " +
				"curl --noproxy '*' --max-time 2 -fsS " + quote(server.URL) + " && exit 3; " +
				"printf 'workspace write allowed\\n' > allowed; cat allowed",
		}
		r := run(t, "boundary", check)
		if r.Status != "passed" || r.ExitCode != 0 || !r.Stopped {
			t.Fatal(r)
		}
		// Linux may accept writes into a private filesystem at the same path.
		// Neither modifying an existing host file nor creating one is permitted.
		if data, err := os.ReadFile(protected); err != nil || string(data) != "outside workspace\n" {
			t.Fatalf("outside file changed: %q, %v", data, err)
		}
		if _, err := os.Stat(created); !os.IsNotExist(err) {
			t.Fatalf("outside file created: %v", err)
		}
		if data, err := os.ReadFile(filepath.Join(root, "boundary", "allowed")); err != nil || string(data) != "workspace write allowed\n" {
			t.Fatalf("workspace write failed: %q, %v", data, err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		r := run(t, "deadline", config.QualityCheck{
			ID: "deadline", Command: "printf started; exec sleep 30", Directory: ".",
			Prerequisites: []string{"sh", "sleep"}, Timeout: "2s", Path: os.Getenv("PATH"),
		})
		if r.Status != "unavailable" || !r.Stopped || !strings.Contains(r.Detail, "context deadline exceeded") {
			t.Fatal(r)
		}
		if data, err := os.ReadFile(r.Evidence); err != nil || !strings.Contains(string(data), "started") {
			t.Fatalf("deadline did not exercise a running sandbox command: %s, %v", data, err)
		}
	})
}

// No model call: test the same read-only grant builder used for audit/verification.
func TestLiveRetainedCheckArtifacts(t *testing.T) {
	if os.Getenv("RUNNER_LOCAL_LIVE_CHECKS") == "" {
		t.Skip("set RUNNER_LOCAL_LIVE_CHECKS=1 for native artifact access qualification")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, "runner-artifact-access-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Retained native artifact evidence:", root)
	source, fixture := artifactFixture(t, root)
	c := Codex{}
	check := c.Check(t.Context(), source, config.QualityCheck{ID: "native", Command: "test -s .runner-output/reports/shot.png && printf 'actual native check\\n'", Directory: ".", Prerequisites: []string{"sh"}, Path: os.Getenv("PATH"), Timeout: "30s"}, filepath.Join(root, "evidence/native.log"), func(p core.Process) error {
		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "evidence/process.json"), data, 0600)
	})
	if check.Status != "passed" || !check.Stopped {
		t.Fatal(check)
	}
	binding := core.Digest("native fixture")
	check.Artifacts, err = RetainCheckArtifacts(source, []string{".runner-output/reports"}, binding, check)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := CheckReadRoots(check, binding)
	if err != nil {
		t.Fatal(err)
	}
	neutral := filepath.Join(root, "reviewer")
	if err = os.Mkdir(neutral, 0700); err != nil {
		t.Fatal(err)
	}
	policy := c.policy(neutral, roots)
	args := append([]string{"sandbox", "-C", neutral, "-P", "runner_local"}, policy[1:]...)
	probe := `set -eu
cat "$1" >/dev/null
cat "$2" >/dev/null
if cat "$3" >/dev/null 2>&1; then exit 21; fi
if cat "$4" >/dev/null 2>&1; then exit 22; fi
if (printf changed >> "$1") 2>/dev/null; then exit 23; fi
printf 'PNG/report readable; original workspace, sibling evidence and writes denied\n'
`
	args = append(args, "--", "/bin/sh", "-c", probe, "artifact-probe",
		filepath.Join(check.Artifacts.Directory, ".runner-output/reports/shot.png"),
		filepath.Join(check.Artifacts.Directory, ".runner-output/reports/report.json"),
		filepath.Join(source, ".runner-output/reports/shot.png"), fixture.Evidence)
	command := exec.CommandContext(t.Context(), c.command(), args...)
	command.Dir = neutral
	output, err := command.CombinedOutput()
	if writeErr := os.WriteFile(filepath.Join(root, "evidence/native-policy.log"), output, 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil {
		t.Fatalf("native read-only artifact boundary: %s: %v", output, err)
	}
	if _, err = CheckReadRoots(check, binding); err != nil {
		t.Fatal("native review changed evidence", err)
	}
	data, err := json.MarshalIndent(check, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "evidence/observation.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// Actual container qualification is opt-in and uses a separately prepared image.
// No image pull, dependency download or model call occurs in this suite.
func TestLiveContainerCheckBoundary(t *testing.T) {
	image := os.Getenv("RUNNER_LOCAL_LIVE_CHECK_IMAGE")
	if image == "" {
		t.Skip("set RUNNER_LOCAL_LIVE_CHECK_IMAGE to a prepared local image ID with sh, go and python3")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, "runner-container-checks-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Retained container evidence:", root)
	t.Setenv("RUNNER_CHECK_SECRET", "must-not-inherit")
	var last core.Process
	run := func(t *testing.T, name, command string, prerequisites []string, timeout string, cpus ...int) core.CheckObservation {
		t.Helper()
		source := filepath.Join(root, name)
		if err := os.MkdirAll(source, 0700); err != nil {
			t.Fatal(err)
		}
		check := config.QualityCheck{ID: name, Image: image, Command: command, Directory: ".", Prerequisites: prerequisites, Timeout: timeout}
		if len(cpus) > 0 {
			check.CPUs = &cpus[0]
		}
		r := (Codex{}).Check(t.Context(), source, check, filepath.Join(root, name+".log"), func(p core.Process) error {
			last = p
			data, err := json.Marshal(p)
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, name+"-process.json"), data, 0600)
		})
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+"-result.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		return r
	}

	for _, cpus := range []int{0, 1, 4} {
		t.Run(fmt.Sprintf("cpu_budget_%d", cpus), func(t *testing.T) {
			want := cpus
			var configured []int
			if cpus == 0 {
				want = 2
			} else {
				configured = []int{cpus}
			}
			command := fmt.Sprintf(`python3 - <<'PYCPU'
from pathlib import Path
quota, period = map(int, Path('/sys/fs/cgroup/cpu.max').read_text().split())
assert quota == period * %d, (quota, period)
assert int(Path('/sys/fs/cgroup/memory.max').read_text()) == 4294967296
assert int(Path('/sys/fs/cgroup/pids.max').read_text()) == 512
print('approved CPU quota, memory and process limits verified')
PYCPU`, want)
			r := run(t, fmt.Sprintf("cpu-%d", want), command, []string{"python3"}, "30s", configured...)
			if r.Status != "passed" || !r.Stopped || !Gone(last) {
				t.Fatal(r, last)
			}
		})
	}

	t.Run("automatic_cpu_budget", func(t *testing.T) {
		checks, err := ResolveCheckCPUs(t.Context(), []config.QualityCheck{{AutoCPUs: true}})
		if err != nil {
			t.Fatal(err)
		}
		cpus := checks[0].CPULimit()
		r := run(t, "cpu-auto", fmt.Sprintf(`python3 -c 'from pathlib import Path; q,p=map(int,Path("/sys/fs/cgroup/cpu.max").read_text().split()); assert q==p*%d'`, cpus), []string{"python3"}, "30s", cpus)
		if r.Status != "passed" || !r.Stopped || !Gone(last) {
			t.Fatal(r, last)
		}
		t.Log("Resolved approved automatic quota:", cpus)
	})
	t.Run("tools_and_containment", func(t *testing.T) {
		// This host service must stay inaccessible even while fixture loopback works.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("host")) }))
		defer server.Close()
		port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
		command := `go version && printf 'package main\nfunc main() {}\n' > .runner-output/probe.go && go run .runner-output/probe.go && python3 - <<'PY'
import os, socket
assert not os.environ.get('RUNNER_CHECK_SECRET')
s=socket.socket(); s.settimeout(2); s.bind(('127.0.0.1',0)); s.listen(1)
c=socket.create_connection(s.getsockname(),timeout=2); a,_=s.accept()
c.sendall(b'fixture'); assert a.recv(7)==b'fixture'
a.close(); c.close(); s.close()
for address in [('127.0.0.1', ` + port + `), ('host.docker.internal', ` + port + `), ('1.1.1.1',443)]:
    try: connection=socket.create_connection(address,timeout=1)
    except OSError: pass
    else: connection.close(); raise AssertionError(('unexpected network access',address))
for path in ['/outside-created', '/var/run/docker.sock', ` + strconvQuote(filepath.Join(root, "host-created")) + `]:
    try:
        with open(path,'w') as f: f.write('escape')
    except OSError: pass
    else: raise AssertionError(('unexpected write',path))
with open('.runner-output/allowed','w') as f: f.write('workspace output')
print('private loopback, offline tools, filesystem and credential boundary passed')
PY`
		r := run(t, "boundary", command, []string{"go", "python3"}, "2m")
		if r.Status != "passed" || !r.Stopped || r.OutputDigest == "" {
			t.Fatal(r)
		}
		if !Gone(last) {
			t.Fatal("container/client ownership was not released", last)
		}
		if _, err := os.Stat(filepath.Join(root, "host-created")); !os.IsNotExist(err) {
			t.Fatal("host file escaped", err)
		}
	})
	t.Run("missing_image_tool", func(t *testing.T) {
		r := run(t, "missing", "exit 99", []string{"runner-deliberately-missing"}, "30s")
		if r.Status != "unavailable" || r.ExitCode != 127 || !r.Stopped {
			t.Fatal(r)
		}
	})
	t.Run("deadline_and_ownership", func(t *testing.T) {
		r := run(t, "deadline", "printf started; exec sleep 30", []string{"sleep"}, "2s")
		if r.Status != "unavailable" || !r.Stopped || !strings.Contains(r.Detail, "context deadline exceeded") {
			t.Fatal(r)
		}
		if !Gone(last) {
			t.Fatal("timed-out container remains", last)
		}
	})
	t.Run("durable_gate_and_unknown_daemon", func(t *testing.T) {
		host, err := dockerHost(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		name := "runner-check-" + core.NewID()
		p := core.Process{Container: name, DockerHost: host}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if data, err := dockerCommand(ctx, host, "create", "--name", name, "--entrypoint", "/bin/sh", image, "-c", "exit 0").CombinedOutput(); err != nil {
			t.Fatalf("create: %s %v", data, err)
		}
		defer dockerCommand(context.Background(), host, "rm", "--force", name).Run()
		if containerGone(p) {
			t.Fatal("existing stopped container released ownership")
		}
		p.DockerHost = "unix:///runner-deliberately-missing-docker.sock"
		if containerGone(p) {
			t.Fatal("unavailable daemon was treated as absence")
		}
		p.DockerHost = host
		if err := dockerCommand(ctx, host, "rm", "--force", name).Run(); err != nil {
			t.Fatal(err)
		}
		if !containerGone(p) {
			t.Fatal("removed container not observed absent")
		}
		source := filepath.Join(root, "gate")
		if err := os.MkdirAll(source, 0700); err != nil {
			t.Fatal(err)
		}
		var denied core.Process
		r := (Codex{}).Check(t.Context(), source, config.QualityCheck{ID: "gate", Image: image, Command: "touch unauthorized", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "30s"}, filepath.Join(root, "gate.log"), func(p core.Process) error { denied = p; return errors.New("approval revoked") })
		if r.Status != "unavailable" || !r.Stopped || denied.Container == "" || !containerGone(denied) {
			t.Fatal(r, denied)
		}
		if _, err := os.Stat(filepath.Join(source, "unauthorized")); !os.IsNotExist(err) {
			t.Fatal("command ran before authorization", err)
		}
	})
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestAutomaticCheckCPUsResolveOnceAndRejectUnavailableCapacity(t *testing.T) {
	bin := t.TempDir()
	count := filepath.Join(bin, "count")
	docker := "#!/bin/sh\ncat " + strconvQuote(count) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_HOST", "unix:///test-docker.sock")
	checks := []config.QualityCheck{{ID: "automatic", AutoCPUs: true}, {ID: "default"}, {ID: "explicit", CPUs: new(3)}}
	for _, tc := range []struct {
		count string
		want  int
	}{{"1", 1}, {"4", 1}, {"5", 2}, {"8", 2}, {"16", 4}, {"32", 4}, {"0", 0}, {"-1", 0}, {"invalid", 0}} {
		if err := os.WriteFile(count, []byte(tc.count), 0600); err != nil {
			t.Fatal(err)
		}
		resolved, err := ResolveCheckCPUs(t.Context(), checks)
		if tc.want == 0 {
			if err == nil {
				t.Fatal("invalid capacity accepted", tc.count)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if resolved[0].AutoCPUs || resolved[0].CPULimit() != tc.want || resolved[1].CPUs != nil || resolved[2].CPULimit() != 3 || checks[0].CPUs != nil || !checks[0].AutoCPUs {
			t.Fatal("resolution changed request or unrelated checks", resolved, checks)
		}
	}
	if err := os.Remove(count); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCheckCPUs(t.Context(), checks); err == nil {
		t.Fatal("unavailable daemon accepted")
	}
	if _, err := ResolveCheckCPUs(t.Context(), checks[1:]); err != nil {
		t.Fatal("fixed quotas should not query capacity", err)
	}
}

func TestUnresolvedOrUnenforceableCheckCPUsCannotExecute(t *testing.T) {
	for _, check := range []config.QualityCheck{{AutoCPUs: true}, {CPUs: new(2)}, {CPUs: new(0), Image: "prepared"}, {CPUs: new(5), Image: "prepared"}} {
		r := (Codex{}).Check(t.Context(), t.TempDir(), check, filepath.Join(t.TempDir(), "check.log"), func(core.Process) error { t.Fatal("invalid quota launched"); return nil })
		if r.Status != "unavailable" || !r.Stopped {
			t.Fatal(r)
		}
	}
}
