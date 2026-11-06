package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Real Core family dispatch, SQLite, scopes, source adapter and Git ancestry
// meet at this seam. Only the two coding turns are scripted; no model or
// product test is invoked.
func TestTaskSourcesCoreDependentOverlapUsesIsolatedSourcesAndOneJoin(t *testing.T) {
	repo := sourceRepository(t)
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "shared.txt")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "shared source")
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	cfg := workspaceSnapshot(t, repo)
	cfg.Instance.TaskConcurrency = 2
	cfg.Instance.Storage.Database.MaxRecord = 40 << 10
	cfg.Instance.Storage.Database.MaxSize = 16 << 20
	project := cfg.Projects["demo"]
	owner := project.Roles["implement"]
	owner.ID, owner.Purpose = "owner", "Implement the accepted card only within its declared paths; read the supplied task manifest."
	owner.Inputs = map[string]string{"request": "text", "task": "text", "paths": "json", "manifest": "artifact", "feedback": "text"}
	owner.Execution.MaxOutput = 512
	role := func(mode string) runner.Role {
		var role runner.Role
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "jacks-break", "roles", "task-source-"+mode+".json"))
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal("fixed example role unavailable", mode, err)
		}
		return role
	}
	seedRole, pinRole, joinRole := role("seed"), role("pin"), role("join")
	project.Roles = map[string]runner.Role{
		"owner":     owner,
		seedRole.ID: seedRole, pinRole.ID: pinRole, joinRole.ID: joinRole,
	}
	empty := runner.Text("")
	children := []runner.Step{
		{ID: "seed", Role: seedRole.ID, Inputs: map[string]runner.Binding{"dependencies": {FromInput: "_task_dependencies"}}},
		{ID: "owner", Role: "owner", Inputs: map[string]runner.Binding{
			"request": {FromInput: "request"}, "task": {FromItem: "task"}, "paths": {FromItem: "paths"},
			"manifest": {FromStep: "pin", Output: "manifest", Previous: true, Fallback: &runner.Binding{FromStep: "seed", Output: "manifest"}},
			"feedback": {FromStep: "pin", Output: "feedback", Previous: true, Default: &empty},
		}},
		{ID: "pin", Role: pinRole.ID, Inputs: map[string]runner.Binding{"patch": {FromStep: "owner", Output: "patch"}, "summary": {FromStep: "owner", Output: "summary"}}, Routes: map[string]runner.Route{"changes_requested": {Next: "owner", MaxTimes: 3}}},
	}
	p := runner.Process{ID: "planned", Inputs: map[string]string{"request": "text", "tasks": "json"}, MaxAttempts: 20, MaxAttemptsPerStep: 1, AttemptTimeout: "1m",
		Steps: []runner.Step{
			{ID: "cards", ForEach: &runner.TaskGroup{Mode: "isolated", MaxParallel: 2, ScopeInput: "paths", Items: runner.Binding{FromInput: "tasks"}, Inputs: map[string]string{"task": "text", "paths": "json"}, Steps: children, Output: runner.Binding{FromStep: "pin", Output: "candidate"}}},
			{ID: "join", Role: joinRole.ID, Inputs: map[string]runner.Binding{"results": {FromStep: "cards", Output: "results"}}},
		}, Outputs: map[string]runner.Binding{"patch": {FromStep: "join", Output: "patch"}, "manifest": {FromStep: "cards", Output: "results"}},
	}
	project.Source = &runner.SourceSettings{Ref: base}
	project.DefaultProcess, project.Processes = "planned", map[string]runner.Process{"planned": p}
	cfg.Projects["demo"] = project
	dirs := []string{}
	script := &scriptedPeer{}
	script.onTurn = func(dir string) {
		i := script.starts
		if i > 1 {
			t.Error("unexpected coding turn", i)
			return
		}
		dirs = append(dirs, dir)
		want, value := "original\n", "first\n"
		if i == 1 {
			want, value = "first\n", "second\n"
		}
		data, err := os.ReadFile(filepath.Join(dir, "shared.txt"))
		if err != nil || string(data) != want {
			t.Error("dependency source was not exact", i, string(data), err)
		}
		if err = os.WriteFile(filepath.Join(dir, "shared.txt"), []byte(value), 0600); err != nil {
			t.Error(err)
		}
	}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script)
	defer rig.close()
	items := []byte("[{\"id\":\"first\",\"inputs\":{\"task\":\"first edit\",\"paths\":[\"shared.txt\"]}},{\"id\":\"second\",\"depends_on\":[\"first\"],\"inputs\":{\"task\":\"dependent edit\",\"paths\":[\"shared.txt\"]}}]")
	root, err := rig.core.Start(runner.StartRequest{RequestID: "real-dependent-overlap", Project: "demo", Inputs: map[string]runner.Value{"request": runner.Text("Make the two ordered edits"), "tasks": {Inline: items}}})
	if err != nil {
		t.Fatal(err)
	}
	done := waitSourceRun(t, rig.core, root.ID, func(r runner.Run) bool {
		if !r.Terminal() || !allReleased(r) {
			return false
		}
		if r.CleanupError != "" {
			t.Fatal("root cleanup failed", r.CleanupError, r.TaskStatuses)
		}
		for _, child := range r.TaskStatuses {
			if child.CleanupError != "" {
				t.Fatal("child cleanup failed", child)
			}
			if child.CleanupPending || child.Active {
				return false
			}
		}
		return r.ResourcesReleased || r.CleanupBlocked
	}, 30*time.Second)
	if done.Status != "Completed" || done.CleanupBlocked || len(done.TaskStatuses) != 2 || len(done.Attempts) != 2 {
		t.Fatal("combined source did not complete", done.Status, done.Reason, done.CleanupError, done.TaskStatuses)
	}
	script.mu.Lock()
	count := script.starts
	script.mu.Unlock()
	if count != 2 || len(dirs) != 2 || dirs[0] == dirs[1] {
		t.Fatal("child source ownership was shared or repeated", dirs, count)
	}
	patch, err := rig.disk.Read("artifacts/"+done.Outputs["patch"].ArtifactID, rig.disk.Policy.MaxArtifact)
	if err != nil || !strings.Contains(string(patch), "+second") || strings.Contains(string(patch), "+first") {
		t.Fatal("join lost the dependent update", string(patch), err)
	}
	raw, err := rig.disk.Read("artifacts/"+done.Outputs["manifest"].ArtifactID, rig.disk.Policy.MaxArtifact)
	var manifest runner.TaskManifest
	if err != nil || json.Unmarshal(raw, &manifest) != nil || len(manifest.Items) != 2 || manifest.Items[1].Candidate.Base != base {
		t.Fatal("missing accepted source manifest", string(raw), err)
	}
	if _, err := git(t.Context(), repo, nil, nil, 256, "merge-base", "--is-ancestor", manifest.Items[0].Candidate.Commit, manifest.Items[1].Candidate.Commit); err != nil {
		t.Fatal("dependency ancestry lost", err)
	}
	if !missingCandidateRef(t.Context(), repo, candidateRef(root.ID)) {
		t.Fatal("unreviewed join created a publication candidate")
	}
}
