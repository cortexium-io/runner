package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestReleaseAdapterTrustsOnlySafeInfrastructureRetryActions(t *testing.T) {
	for _, action := range []string{"readiness", "qualify", "merge", "migrate", "deploy"} {
		t.Run(action, func(t *testing.T) {
			disk := workspaceDisk(t)
			binary := filepath.Join(t.TempDir(), "operator")
			response, _ := json.Marshal(release.Response{Key: "attempt_safe", State: "finished", Failed: true, Retryable: true, Reason: "pre-dispatch connection unavailable"})
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '"+string(response)+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			r := &Releases{disk: disk}
			j := releaseJob{
				Assignment: runner.Assignment{
					AttemptID: "attempt_safe",
					ProjectID: "demo",
					RunID:     "run_safe",
					Source:    &runner.SourceSettings{Ref: strings.Repeat("a", 40)},
					Setup:     runner.ExecutionSetup{Profile: runner.Profile{Mode: action}},
				},
				Command:        []string{binary},
				SourcePrepared: true,
			}
			if err := r.save(j); err != nil {
				t.Fatal(err)
			}
			o, err := r.Observe(t.Context(), j.Assignment.AttemptID)
			wantRetry := action == "readiness" || action == "qualify"
			if err != nil || o.State != runner.Finished || o.End == nil || o.End.Retryable != wantRetry {
				t.Fatal("privileged operation acquired an automatic replacement allowance", o, err)
			}
		})
	}
}

func TestReleaseAdapterReconcilesTransportButSurfacesExplicitUnknown(t *testing.T) {
	disk := workspaceDisk(t)
	binary := filepath.Join(t.TempDir(), "operator")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := &Releases{disk: disk}
	j := releaseJob{
		Assignment: runner.Assignment{
			AttemptID: "same_operation",
			ProjectID: "demo",
			RunID:     "run_same",
			Source:    &runner.SourceSettings{Ref: strings.Repeat("a", 40)},
			Setup:     runner.ExecutionSetup{Profile: runner.Profile{Mode: "qualify"}},
		},
		Command:        []string{binary},
		SourcePrepared: true,
	}
	if err := r.save(j); err != nil {
		t.Fatal(err)
	}
	o, err := r.Observe(t.Context(), j.Assignment.AttemptID)
	if err != nil || o.State != runner.Unknown || !o.Reconciling || o.End != nil {
		t.Fatal("saved transport identity did not enter self-reconciliation", o, err)
	}
	response, _ := json.Marshal(release.Response{Key: j.Assignment.AttemptID, State: "unknown", Reason: "worker effect is not established"})
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\n", response)
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	o, err = r.Observe(t.Context(), j.Assignment.AttemptID)
	if err != nil || o.State != runner.Unknown || o.Reconciling || o.End != nil {
		t.Fatal("explicit unknown effect was hidden as automatic recovery", o, err)
	}
}

func TestReleaseCompleteWorkflowApprovalsRestartAndFailure(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "release-fixture")
	build := exec.Command("go", "build", "-o", binary, "./testdata/release-operator")
	build.Dir = filepath.Join("..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "migration failure"}[failure], func(t *testing.T) {
			repo := sourceRepository(t)
			head := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
			cfg := workspaceSnapshot(t, repo)
			cfg.Instance.Codex = nil
			cfg.Instance.Storage.Database.MaxRecord = 40 << 10
			cfg.Instance.Storage.Database.MaxSize = 48 << 20
			cfg.Instance.Storage.Database.MaxRuns = 128
			cfg.Instance.Release = &runner.ReleaseSettings{Command: []string{binary}}
			p := cfg.Projects["demo"]
			p.Source = &runner.SourceSettings{Ref: head}
			p.DefaultProcess = "release"
			p.Roles = map[string]runner.Role{}
			p.Profiles = map[string]string{}
			var process runner.Process
			data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "approved-release", "release.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &process); err != nil {
				t.Fatal(err)
			}
			for _, step := range process.Steps {
				var role runner.Role
				data, err = os.ReadFile(filepath.Join("..", "..", "..", "examples", "approved-release", "roles", step.Role+".json"))
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(data, &role); err != nil {
					t.Fatal(err)
				}
				p.Roles[step.Role] = role
			}
			p.Processes = map[string]runner.Process{"release": process}
			cfg.Projects["demo"] = p
			operator := t.TempDir()
			t.Setenv("RUNNER_RELEASE_FIXTURE_STATE", operator)
			t.Setenv("RUNNER_RELEASE_FIXTURE_HEAD", head)
			flag := "0"
			if failure {
				flag = "1"
			}
			t.Setenv("RUNNER_RELEASE_FIXTURE_FAIL", flag)
			state := filepath.Join(t.TempDir(), "state")
			hook := func(s *Service) { s.releases.cfg = cfg.Instance.Release }
			rig := openNativeRig(t, state, cfg, nil, hook)
			request := runner.StartRequest{RequestID: "release-proof", Project: "demo", SourceRef: head}
			r, err := rig.core.Start(request)
			if err != nil {
				rig.close()
				t.Fatal(err)
			}
			first := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Status == "Waiting" && r.Decision != nil })
			if first.Decision.Step != "prepare" || len(first.Attempts) != 1 {
				t.Fatal("merge gate was skipped", first)
			}
			if _, err = rig.core.Retry(runner.RetryRequest{RequestID: "bypass", RunID: r.ID, ExtraAttempts: 4}); err == nil {
				t.Fatal("retry bypassed approval")
			}
			if _, err = rig.core.Decide(runner.DecisionRequest{RequestID: "approve-merge", RunID: r.ID, DecisionID: first.Decision.ID, Choice: "approve"}); err != nil {
				t.Fatal(err)
			}
			second := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool {
				return r.Status == "Waiting" && r.Decision != nil && r.Decision.Step == "backup"
			})
			if len(second.Attempts) != 3 {
				t.Fatal("production action ran before approval")
			}
			rig.close()
			rig = openNativeRig(t, state, cfg, nil, hook)
			defer rig.close()
			rejoined, err := rig.core.Start(request)
			if err != nil || rejoined.ID != r.ID || rejoined.Decision == nil || rejoined.Decision.ID != second.Decision.ID {
				t.Fatal("restart/rejoin changed approval identity", err)
			}
			if _, err = rig.core.Decide(runner.DecisionRequest{RequestID: "approve-production", RunID: r.ID, DecisionID: second.Decision.ID, Choice: "approve"}); err != nil {
				t.Fatal(err)
			}
			end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
			calls, err := os.ReadFile(filepath.Join(operator, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			want := "prepare\nmerge\nbackup\nstop\nmigrate\npin\ndeploy\nverify\n"
			if failure {
				want = "prepare\nmerge\nbackup\nstop\nmigrate\n"
			}
			if string(calls) != want {
				t.Fatalf("repeated/skipped external action: %q", calls)
			}
			if failure && end.Status != "Failed" || !failure && (end.Status != "Completed" || len(end.Attempts) != 8) {
				t.Fatal("wrong terminal result", end.Status, end.Reason)
			}
			if end.Status == "Completed" {
				for _, a := range end.Attempts {
					if a.Usage.InputTokens == nil || *a.Usage.InputTokens != 0 {
						t.Fatal("release spent model tokens")
					}
				}
			}
		})
	}
}

func TestCancelledStopResultUsesConfiguredRecoveryOnce(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "release-fixture")
	build := exec.Command("go", "build", "-o", binary, "./testdata/release-operator")
	build.Dir = filepath.Join("..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	root := t.TempDir()
	journal := filepath.Join(root, "operations")
	os.Mkdir(journal, 0700)
	head := strings.Repeat("a", 40)
	t.Setenv("RUNNER_RELEASE_FIXTURE_STATE", root)
	t.Setenv("RUNNER_RELEASE_FIXTURE_HEAD", head)
	t.Setenv("RUNNER_RELEASE_FIXTURE_CONNECTED", "1")
	stop := release.Receipt{
		Key:     "attempt_stop",
		RunID:   "run_cancelled",
		Action:  "stop",
		Policy:  "fixture",
		Source:  head,
		DataSHA: strings.Repeat("b", 64),
	}
	data, _ := json.Marshal(release.Job{
		Request: release.Request{Project: "demo", RunID: stop.RunID, Key: stop.Key, Action: "stop", Source: head},
		Receipt: stop,
		Done:    true,
	})
	os.WriteFile(filepath.Join(journal, stop.Key+".json"), data, 0600)
	previous, _ := json.Marshal(stop)
	disk := workspaceDisk(t)
	r := &Releases{disk: disk, cfg: &runner.ReleaseSettings{Command: []string{binary}}}
	run := runner.Run{
		ID: stop.RunID, ProjectID: "demo", Source: &runner.SourceSettings{Ref: head}, CancelRequested: true, Status: "Cancelled",
		Process: runner.Process{Steps: []runner.Step{{ID: "stop", Role: "stop"}}},
		Roles:   map[string]runner.Role{"stop": {ID: "stop", Execution: &runner.Profile{Harness: "release", Access: "input-only", Mode: "stop"}}},
		Attempts: []runner.Attempt{{
			ID:       stop.Key,
			State:    runner.Finished,
			Released: true,
			End: &runner.ExecutionEnd{
				Kind:   "returned",
				Result: &runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"receipt": {Inline: previous}}},
			},
		}},
	}
	for range 2 {
		if err := r.recoverCancelledStop(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(root, "calls"))
	if string(calls) != "recover-stop\n" {
		t.Fatal("cancelled stop repeated recovery or advanced release", string(calls))
	}
}
