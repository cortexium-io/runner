package execution

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
