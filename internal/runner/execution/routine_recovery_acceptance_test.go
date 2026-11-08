package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

// The subprocess wraps the existing model-free provider. Its first qualification
// never dispatches; the next one commits normally but withholds its reply until
// the parent opens a durable test barrier. It invokes no native service.
func TestRoutineRecoveryOperatorProcess(t *testing.T) {
	if os.Getenv("RUNNER_ROUTINE_RECOVERY_OPERATOR") != "1" {
		t.Skip("internal model-free release transport fixture")
	}
	if err := routineRecoveryOperator(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(255)
	}
	os.Exit(0)
}

func routineRecoveryOperator() error {
	var q release.Request
	if err := json.NewDecoder(os.Stdin).Decode(&q); err != nil {
		return err
	}
	root := os.Getenv("RUNNER_RELEASE_FIXTURE_STATE")
	first, accepted := filepath.Join(root, "first-qualification"), filepath.Join(root, "accepted-qualification")
	if q.Action == "qualify" {
		file, err := os.OpenFile(first, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = file.WriteString(q.Key)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
		key, err := os.ReadFile(first)
		if err != nil {
			return err
		}
		if string(key) == q.Key {
			return json.NewEncoder(os.Stdout).Encode(release.Response{
				Key:       q.Key,
				State:     "finished",
				Failed:    true,
				Retryable: true,
				Reason:    "fixture connection failed before provider dispatch",
			})
		}
		if key, err := os.ReadFile(accepted); err == nil {
			if string(key) != q.Key {
				return errors.New("fixture rejected replacement of an accepted qualification")
			}
			if _, err := os.Stat(filepath.Join(root, "release-reply")); err != nil {
				return errors.New("fixture accepted operation reply remains disconnected")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	input, err := json.Marshal(q)
	if err != nil {
		return err
	}
	cmd := exec.Command(os.Getenv("RUNNER_ROUTINE_RECOVERY_PROVIDER"))
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.Output()
	if err != nil {
		return err
	}
	if q.Action == "qualify" {
		var response release.Response
		if json.Unmarshal(output, &response) != nil || response.Key != q.Key || response.State != "finished" || response.Failed {
			return errors.New("fixture provider did not commit the expected qualification")
		}
		if err := os.WriteFile(accepted, []byte(q.Key), 0600); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(root, "release-reply")); err != nil {
			return errors.New("fixture lost the accepted qualification reply")
		}
	}
	_, err = os.Stdout.Write(output)
	return err
}

func TestFreshRoutineRecoveryRejoinsAcceptedOperationAndPreservesProtectedGates(t *testing.T) {
	provider := evidenceBinary(t, "./testdata/release-operator")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo := sourceRepository(t)
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	cfg := workspaceSnapshot(t, repo)
	cfg.Instance.Codex = nil
	cfg.Instance.Storage.Database.MaxRecord = 40 << 10
	cfg.Instance.Storage.Database.MaxSize = 48 << 20
	cfg.Instance.Storage.Database.MaxRuns = 128
	cfg.Instance.Release = &runner.ReleaseSettings{Command: []string{
		binary,
		"-test.run=^TestRoutineRecoveryOperatorProcess$",
		"-test.timeout=20s",
	}}
	project := cfg.Projects["demo"]
	project.Roles, project.Profiles = map[string]runner.Role{}, map[string]string{}
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	var process runner.Process
	data, err := os.ReadFile(filepath.Join(root, "release-reviewed.json"))
	if err != nil || json.Unmarshal(data, &process) != nil || process.InfrastructureRetries != 1 || process.MaxAttemptsPerStep != 1 || process.MaxAttempts != 16 {
		t.Fatal("fresh configured recovery policy changed", err)
	}
	for _, step := range process.Steps {
		data, err = os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
		var role runner.Role
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal("configured release role unavailable", step.Role, err)
		}
		project.Roles[role.ID] = role
	}
	project.Source, project.DefaultProcess = &runner.SourceSettings{Ref: base}, process.ID
	project.Processes = map[string]runner.Process{process.ID: process}
	cfg.Projects["demo"] = project
	operator := t.TempDir()
	t.Setenv("RUNNER_ROUTINE_RECOVERY_OPERATOR", "1")
	t.Setenv("RUNNER_ROUTINE_RECOVERY_PROVIDER", provider)
	t.Setenv("RUNNER_RELEASE_FIXTURE_STATE", operator)
	t.Setenv("RUNNER_RELEASE_FIXTURE_CONNECTED", "1")
	t.Setenv("RUNNER_RELEASE_FIXTURE_HEAD", base)
	publication := release.Receipt{
		Key:    "accepted-publication",
		RunID:  "original-run",
		Action: "publish",
		Policy: "fixture",
		Source: base,
		Head:   strings.Repeat("b", 40),
		Base:   base,
		PR:     334,
	}
	old := release.Job{
		Request: release.Request{Project: "demo", RunID: publication.RunID, Key: publication.Key, Action: "publish", Source: base, Candidate: publication.Head},
		Receipt: publication,
		Done:    true,
	}
	journal, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(operator, "operations", publication.Key+".json")
	if err := os.MkdirAll(filepath.Dir(oldFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldFile, journal, 0600); err != nil {
		t.Fatal(err)
	}
	handle, _ := json.Marshal(release.Receipt{Key: publication.Key, RunID: publication.RunID, Action: publication.Action, Policy: publication.Policy, Source: base, Head: publication.Head})
	dir := filepath.Join(t.TempDir(), "state")
	hook := func(s *Service) { s.releases.cfg = cfg.Instance.Release }
	rig := openNativeRig(t, dir, cfg, nil, hook)
	defer func() { rig.close() }()
	request := runner.StartRequest{RequestID: "fresh-routine-recovery", Project: "demo", SourceRef: base, Inputs: map[string]runner.Value{"publication": {Inline: handle}}}
	r, err := rig.core.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	_, definition, err := runner.MarshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	pending := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool {
		return r.Status == "Waiting" && r.Current() != nil && r.Current().State == runner.Unknown && r.Current().Reconciling
	})
	if len(pending.Attempts) != 3 || pending.Decision != nil || pending.Attempts[1].End == nil || !pending.Attempts[1].End.Retryable || pending.Attempts[1].End.Kind != "failed" || pending.VisitExtraAttempts != 0 {
		t.Fatal("safe retry did not retain its failure and rejoin one replacement", pending.Status, pending.Reason, len(pending.Attempts))
	}
	accepted, err := os.ReadFile(filepath.Join(operator, "accepted-qualification"))
	if err != nil || string(accepted) != pending.Current().ID {
		t.Fatal("lost reply was not bound to its accepted operator operation", err)
	}
	savedAttempt, deadline := pending.Current().ID, pending.Current().Deadline
	failed, _ := json.Marshal(pending.Attempts[1].End)
	rig.close()
	rig = openNativeRig(t, dir, cfg, nil, hook)
	rejoined, err := rig.core.Start(request)
	if err != nil || rejoined.ID != r.ID || len(rejoined.Attempts) != 3 || rejoined.Current().ID != savedAttempt || !rejoined.Current().Deadline.Equal(deadline) {
		t.Fatal("restart replaced accepted work, its request or its original deadline", err)
	}
	if err := os.WriteFile(filepath.Join(operator, "release-reply"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	rig.core.ExecutionChanged(savedAttempt)
	gate := func(step string) runner.Run {
		t.Helper()
		return waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool {
			if r.Decision == nil || r.Decision.Step != step {
				return false
			}
			for _, a := range r.Attempts {
				if !a.Released {
					return false
				}
			}
			return true
		})
	}
	merge := gate("prepare")
	if len(merge.Attempts) != 4 || merge.Attempts[2].ID != savedAttempt || !merge.Attempts[2].Deadline.Equal(deadline) {
		t.Fatal("reconciliation spent a third qualification attempt")
	}
	if _, err := rig.core.Retry(runner.RetryRequest{RequestID: "not-an-approval", RunID: r.ID, ExtraAttempts: 4}); err == nil {
		t.Fatal("routine recovery bypassed merge approval or granted allowance")
	}
	calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
	if strings.Count(string(calls), "qualify\n") != 1 || strings.Contains(string(calls), "merge\n") || strings.Contains(string(calls), "stop\n") {
		t.Fatal("accepted qualification replayed or protected effects ran before approval", string(calls))
	}
	mergeApproval := runner.DecisionRequest{RequestID: "fixture-merge-only", RunID: r.ID, DecisionID: merge.Decision.ID, Choice: "approve"}
	if _, err := rig.core.Decide(mergeApproval); err != nil {
		t.Fatal(err)
	}
	production := gate("backup")
	if production.Decision.ID == merge.Decision.ID || len(production.Attempts) != 6 {
		t.Fatal("merge approval was inherited by production")
	}
	rig.close()
	rig = openNativeRig(t, dir, cfg, nil, hook)
	rejoined, err = rig.core.Start(request)
	if err != nil || rejoined.Decision == nil || rejoined.Decision.ID != production.Decision.ID {
		t.Fatal("restart changed the protected production decision", err)
	}
	if _, err := rig.core.Decide(mergeApproval); err != nil {
		t.Fatal("idempotent old merge approval replay failed", err)
	}
	if _, err := rig.core.Decide(runner.DecisionRequest{RequestID: "stale-decision", RunID: r.ID, DecisionID: merge.Decision.ID, Choice: "approve"}); err == nil {
		t.Fatal("an old decision ID approved production")
	}
	calls, _ = os.ReadFile(filepath.Join(operator, "calls"))
	if strings.Count(string(calls), "merge\n") != 1 || strings.Contains(string(calls), "stop\n") {
		t.Fatal("approval replay repeated merge or advanced production", string(calls))
	}
	if _, err := rig.core.Decide(runner.DecisionRequest{RequestID: "fixture-production-only", RunID: r.ID, DecisionID: production.Decision.ID, Choice: "approve"}); err != nil {
		t.Fatal(err)
	}
	done := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return allReleased(r) && r.ResourcesReleased })
	_, finalDefinition, err := runner.MarshalRunRecord(done)
	finalFailure, _ := json.Marshal(done.Attempts[1].End)
	if err != nil || done.Status != "Completed" || len(done.Attempts) != 11 || !bytes.Equal(finalDefinition, definition) || !bytes.Equal(failed, finalFailure) || done.VisitExtraAttempts != 0 {
		t.Fatal("routine recovery changed frozen policy/history or left resources", done.Status, done.Reason, len(done.Attempts), err)
	}
	after, err := os.ReadFile(oldFile)
	if err != nil || !bytes.Equal(after, journal) {
		t.Fatal("continuation rewrote accepted publication history", err)
	}
	calls, err = os.ReadFile(filepath.Join(operator, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"qualify", "prepare", "merge", "backup", "stop", "migrate", "pin", "deploy", "verify"} {
		if strings.Count(string(calls), action+"\n") != 1 {
			t.Fatal("recovery repeated or skipped a settled provider operation", action, string(calls))
		}
	}
	for _, a := range done.Attempts {
		if a.Usage.InputTokens == nil || *a.Usage.InputTokens != 0 || a.Usage.OutputTokens == nil || *a.Usage.OutputTokens != 0 {
			t.Fatal("model-free acceptance dispatched a native turn")
		}
	}
}
