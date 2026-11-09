package execution

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestReviewedReleaseRunsWithoutModelsAndKeepsBothApprovals(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "operator")
	build := exec.Command("go", "build", "-o", binary, "./testdata/release-operator")
	build.Dir = filepath.Join("..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	repo := sourceRepository(t)
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	head := strings.Repeat("b", 40)
	cfg := workspaceSnapshot(t, repo)
	cfg.Instance.Codex = nil
	cfg.Instance.Release = &runner.ReleaseSettings{Command: []string{binary}}
	p := cfg.Projects["demo"]
	p.Source, p.DefaultProcess = &runner.SourceSettings{Ref: base}, "release-reviewed"
	p.Roles, p.Profiles = map[string]runner.Role{}, map[string]string{}
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	data, err := os.ReadFile(filepath.Join(root, "release-reviewed.json"))
	var process runner.Process
	if err != nil || json.Unmarshal(data, &process) != nil {
		t.Fatal("reviewed release unavailable", err)
	}
	for _, step := range process.Steps {
		data, err = os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
		var role runner.Role
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal("role unavailable", step.Role, err)
		}
		p.Roles[role.ID] = role
	}
	p.Processes = map[string]runner.Process{process.ID: process}
	cfg.Projects["demo"] = p
	operator := t.TempDir()
	t.Setenv("RUNNER_RELEASE_FIXTURE_STATE", operator)
	t.Setenv("RUNNER_RELEASE_FIXTURE_CONNECTED", "1")
	t.Setenv("RUNNER_RELEASE_FIXTURE_HEAD", base)
	publication := release.Receipt{Key: "old_publication", RunID: "old_run", Action: "publish", Policy: "fixture", Source: base, Head: head, Base: base, PR: 332}
	old := release.Job{Request: release.Request{Project: "demo", RunID: publication.RunID, Key: publication.Key, Action: "publish", Source: base, Candidate: head}, Receipt: publication, Done: true}
	data, _ = json.Marshal(old)
	operations := filepath.Join(operator, "operations")
	if err = os.Mkdir(operations, 0700); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(operations, publication.Key+".json")
	if err = os.WriteFile(oldFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	handle, _ := json.Marshal(release.Receipt{Key: publication.Key, RunID: publication.RunID, Action: publication.Action, Policy: publication.Policy, Source: base, Head: head})
	dir := filepath.Join(t.TempDir(), "state")
	hook := func(s *Service) { s.releases.cfg = cfg.Instance.Release }
	rig := openNativeRig(t, dir, cfg, nil, hook)
	run, err := rig.core.Start(runner.StartRequest{RequestID: "resume-reviewed", Project: "demo", Inputs: map[string]runner.Value{"publication": {Inline: handle}}})
	if err != nil {
		rig.close()
		t.Fatal(err)
	}
	merge := waitSourceRun(t, rig.core, run.ID, func(r runner.Run) bool { return r.Decision != nil })
	if merge.Decision.Step != "prepare" || len(merge.Attempts) != 3 {
		rig.close()
		t.Fatal("qualification/merge approval skipped", merge)
	}
	rig.close()
	rig = openNativeRig(t, dir, cfg, nil, hook)
	defer rig.close()
	if _, err = rig.core.Decide(runner.DecisionRequest{RequestID: "merge", RunID: run.ID, DecisionID: merge.Decision.ID, Choice: "approve"}); err != nil {
		t.Fatal(err)
	}
	production := waitSourceRun(t, rig.core, run.ID, func(r runner.Run) bool { return r.Decision != nil && r.Decision.Step == "backup" })
	if len(production.Attempts) != 5 {
		t.Fatal("production ran before its own approval", production)
	}
	if _, err = rig.core.Decide(runner.DecisionRequest{RequestID: "production", RunID: run.ID, DecisionID: production.Decision.ID, Choice: "approve"}); err != nil {
		t.Fatal(err)
	}
	done := waitSourceRun(t, rig.core, run.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
	if done.Status != "Completed" || len(done.Attempts) != 10 {
		t.Fatal("reviewed release did not complete", done)
	}
	for _, attempt := range done.Attempts {
		if done.Roles[done.Process.Steps[attempt.Step].Role].Execution.Harness != "release" {
			t.Fatal("resume launched model work")
		}
	}
	after, _ := os.ReadFile(oldFile)
	if string(after) != string(data) {
		t.Fatal("resume rewrote original publication")
	}
}
