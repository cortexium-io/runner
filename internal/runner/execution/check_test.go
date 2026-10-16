package execution

import (
	"context"
	"encoding/json"
	"errors"
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
	run := func(t *testing.T, name, command string, prerequisites []string, timeout string) core.CheckObservation {
		t.Helper()
		source := filepath.Join(root, name)
		if err := os.MkdirAll(source, 0700); err != nil {
			t.Fatal(err)
		}
		r := (Codex{}).Check(t.Context(), source, config.QualityCheck{ID: name, Image: image, Command: command, Directory: ".", Prerequisites: prerequisites, Timeout: timeout}, filepath.Join(root, name+".log"), func(p core.Process) error {
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
