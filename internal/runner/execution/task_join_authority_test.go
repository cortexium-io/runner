package execution

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestTaskJoinImmutableAuthoritySurvivesMovedRefAndRestart(t *testing.T) {
	f := newTaskSourceFixture(t, true)
	t.Cleanup(func() { _ = f.service.Close(context.Background()) })
	seed, pin := pinReleasedTaskAttempts(t, f, "first", "first task\n")
	if err := f.service.ReleaseRun(t.Context(), runner.Run{ID: seed.RunID, Status: "Completed"}); err != nil {
		t.Fatal(err)
	}
	a := f.joinAssignment(t, []runner.TaskCandidate{*pin})
	before, err := f.service.workspaces.load(pin.RunID)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := f.disk.Read("artifacts/"+pin.PatchID, f.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := taskTree(t.Context(), f.repo, f.base)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := taskCommit(t.Context(), f.repo, taskGitEnv(""), tree, "moving ref fixture", f.base)
	if err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, f.repo, "update-ref", "refs/remotes/origin/main", moved)
	legacy := runner.Clone(a)
	legacy.AttemptID = "attempt_old_symbolic_join"
	legacy.Source.Ref = "refs/remotes/origin/main"
	o, err := f.service.Start(t.Context(), legacy)
	if err != nil || o.End == nil || o.End.Kind != "failed" || o.Source != nil {
		t.Fatal("old unresolved assignment did not fail closed", o, err)
	}
	// Reopen an allocated immutable join while an inherited source helper still
	// owns its lease. Cancellation settles without rebuilding or accepting source.
	_, lease, err := f.service.workspaces.taskLease(a)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if o, err = f.service.Start(ctx, a); err != nil || o.State != runner.Running {
		t.Fatal("short observer failed to join durable attempt", o, err)
	}
	if err = f.service.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	f.service = NewService(f.disk, func(_ context.Context, id string) ([]byte, error) {
		return f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
	}, nil)
	o, err = f.service.Observe(t.Context(), a.AttemptID)
	taskReturned(t, o, err, "ready")
	if o.Source == nil || o.Source.Base != f.base || o.Source.Base == moved {
		t.Fatal("join followed today's ref", o.Source)
	}
	job, err := f.service.tasks.load(a.AttemptID)
	want, _ := json.Marshal(a)
	got, _ := json.Marshal(job.Assignment)
	if err != nil || string(got) != string(want) || !job.Assignment.Deadline.Equal(a.Deadline) {
		t.Fatal("restart changed the saved immutable assignment", err)
	}
	again, err := f.service.Start(t.Context(), a)
	if err != nil || !reflect.DeepEqual(again.Source, o.Source) {
		t.Fatal("join replay replaced its source", err)
	}
	changed := runner.Clone(a)
	changed.Source.Ref = moved
	if _, err = f.service.Start(t.Context(), changed); err == nil {
		t.Fatal("same attempt accepted a changed resolved base")
	}
	if err = f.service.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReleaseRun(t.Context(), runner.Run{ID: a.RunID, Status: "Cancelled", CancelRequested: true}); err != nil {
		t.Fatal(err)
	}
	assertReleasedTaskEvidence(t, f, before, *pin, patch)
}

func TestTaskJoinManifestAndPinChecksRemainExact(t *testing.T) {
	f := newTaskSourceFixture(t, true)
	t.Cleanup(func() { _ = f.service.Close(context.Background()) })
	_, pin := pinReleasedTaskAttempts(t, f, "first", "first task\n")
	a := f.joinAssignment(t, []runner.TaskCandidate{*pin})
	raw, err := f.disk.Read("artifacts/"+a.Inputs["results"].ArtifactID, f.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	var original runner.TaskManifest
	if err = json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*runner.TaskManifest){
		"root":       func(m *runner.TaskManifest) { m.RootRunID = "foreign" },
		"group":      func(m *runner.TaskManifest) { m.GroupStep = "foreign" },
		"repo":       func(m *runner.TaskManifest) { m.Repository = "/foreign" },
		"base":       func(m *runner.TaskManifest) { m.Base = strings.Repeat("b", 40) },
		"root patch": func(m *runner.TaskManifest) { m.RootPatchID = "foreign/patch" },
		"missing":    func(m *runner.TaskManifest) { m.Items = nil },
		"duplicate":  func(m *runner.TaskManifest) { m.Items = append(m.Items, m.Items[0]) },
		"item":       func(m *runner.TaskManifest) { m.Items[0].ID = "foreign" },
		"run":        func(m *runner.TaskManifest) { m.Items[0].RunID = "foreign" },
		"candidate":  func(m *runner.TaskManifest) { m.Items[0].Candidate.Tree = strings.Repeat("c", 40) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := runner.Clone(original)
			mutate(&m)
			if err := validateTaskManifest(a, m); err == nil {
				t.Fatal("changed manifest passed unchanged execution authority")
			}
		})
	}
	// Actual immutable pin lookup, tree and captured patch integrity remain
	// required independently of the matching Core/manifest candidate values.
	for _, mode := range []string{"missing pin", "moved pin", "changed patch"} {
		t.Run(mode, func(t *testing.T) {
			patch, e := f.disk.Read("artifacts/"+pin.PatchID, f.disk.Policy.MaxArtifact)
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "missing pin":
				fixtureGit(t, f.repo, "update-ref", "-d", taskPinRef(pin.RunID))
			case "moved pin":
				fixtureGit(t, f.repo, "update-ref", taskPinRef(pin.RunID), f.base)
			case "changed patch":
				if e = f.disk.Write("artifacts/"+pin.PatchID, []byte("changed"), f.disk.Policy.MaxArtifact); e != nil {
					t.Fatal(e)
				}
			}
			if e = f.service.workspaces.verifyTaskCandidate(t.Context(), a, f.repo, f.base, *pin); e == nil {
				t.Fatal("changed pin/captured bytes accepted")
			}
			fixtureGit(t, f.repo, "update-ref", taskPinRef(pin.RunID), pin.Commit)
			if e = f.disk.Write("artifacts/"+pin.PatchID, patch, f.disk.Policy.MaxArtifact); e != nil {
				t.Fatal(e)
			}
		})
	}
	if err = validateTaskManifest(a, original); err != nil {
		t.Fatal(err)
	}
	// A missing artifact is refused before source assembly, with evidence intact.
	a.AttemptID, a.Deadline = "attempt_missing_join_manifest", time.Now().Add(time.Minute)
	a.Inputs["results"] = runner.Value{ArtifactID: "absent/results"}
	o, err := f.service.Start(t.Context(), a)
	if err != nil || o.End == nil || o.End.Kind != "failed" || o.Source != nil {
		t.Fatal("missing manifest was accepted", o, err)
	}
	if _, err = os.Stat(f.disk.Root + "/work/" + a.RunID + "/source"); !os.IsNotExist(err) {
		t.Fatal("invalid manifest restored a root checkout", err)
	}
}
