package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

// Native containment qualification, opt-in, no paid model invocation.
func TestLiveQualityCheckBoundary(t *testing.T) {
	if os.Getenv("RUNNER_LOCAL_LIVE_CHECKS") == "" {
		t.Skip("set RUNNER_LOCAL_LIVE_CHECKS=1 for native Codex sandbox checks")
	}
	root, err := os.MkdirTemp("", "runner-quality-checks-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Retained native evidence:", root)
	t.Setenv("RUNNER_CHECK_SECRET", "must-not-inherit")
	var baseline core.CheckObservation
	for _, name := range []string{"baseline", "matching", "changed", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join(root, name)
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			message := "known failure"
			if name == "changed" {
				message = "new failure"
			}
			check := config.QualityCheck{ID: "legacy", Command: `test -z "$RUNNER_CHECK_SECRET" || exit 125; printf '%s\n' '` + message + `'; exit 1`, Directory: ".", Prerequisites: []string{"sh"}, Timeout: "30s", Path: os.Getenv("PATH")}
			if name == "unavailable" {
				check.Prerequisites = []string{"runner-deliberately-missing-tool"}
			}
			r := (Codex{}).Check(t.Context(), source, check, filepath.Join(root, name+".log"), func(p core.Process) error {
				data, _ := json.Marshal(p)
				return os.WriteFile(filepath.Join(root, name+"-process.json"), data, 0600)
			})
			data, _ := json.MarshalIndent(r, "", "  ")
			if err = os.WriteFile(filepath.Join(root, name+"-result.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
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
}
