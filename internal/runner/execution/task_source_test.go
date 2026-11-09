package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestTaskSourcesReadOnlySeedNeedsNoPrematureArtifactRegistration(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	f.service.workspaces.read = func(context.Context, string) ([]byte, error) { return nil, runner.ErrNotFound }
	a := f.seed(t, "a", "run_a", []string{"a.txt"})
	source := f.edit(t, a, "one", map[string]string{"a.txt": "owned\n"})
	f.pin(t, a, source, "one", "ready")
}

func TestTaskSourcesRootPatchIsBoundToOriginalCapture(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	root := f.assignment("", "run_plan", "attempt_root", "seed", []string{"inherited.txt"})
	root.TaskSource = nil
	root.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	r, err := f.service.workspaces.Prepare(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.WorkDir, "inherited.txt"), []byte("trusted-root\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := f.service.workspaces.Capture(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := f.disk.Read("artifacts/"+source.PatchID, f.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.ReplaceAll(patch, []byte("trusted-root"), []byte("changed-root"))
	if len(tampered) != len(patch) || bytes.Equal(tampered, patch) {
		t.Fatal("bad tamper fixture")
	}
	if err = f.disk.Write("artifacts/"+source.PatchID, tampered, f.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	a := f.assignment("a", "run_a", "attempt_a_seed", "seed", []string{"a.txt"})
	a.TaskSource.RootPatchID = source.PatchID
	a.Inputs["dependencies"] = runner.Value{Inline: json.RawMessage("[]")}
	o, err := f.service.Start(t.Context(), a)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "failed" || o.Source != nil || !strings.Contains(o.End.Reason, "root task patch differs") {
		t.Fatal("root tampering accepted", o, err)
	}
	if _, err = f.service.workspaces.load(a.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid root installed", err)
	}
	data, _ := os.ReadFile(filepath.Join(r.WorkDir, "inherited.txt"))
	if string(data) != "trusted-root\n" {
		t.Fatal("retained root source changed")
	}
	if err = f.disk.Write("artifacts/"+source.PatchID, patch, f.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	a.AttemptID = "attempt_valid_seed"
	o, err = f.service.Start(t.Context(), a)
	taskReturned(t, o, err, "ready")
	child, _ := f.service.workspaces.load(a.RunID)
	data, _ = os.ReadFile(filepath.Join(child.WorkDir, "inherited.txt"))
	if string(data) != "trusted-root\n" {
		t.Fatal("authenticated root source lost")
	}
}

func TestTaskSourcesReleaseUsesOwnCaptureAfterLaterOwnerAndPreservesUnknownScratch(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	a := f.seed(t, "a", "run_a", []string{"a.txt"})
	source := f.edit(t, a, "one", map[string]string{"a.txt": "owned\n"})
	unknown := filepath.Join(f.disk.Root, "work", a.AttemptID, "personal.txt")
	if err := os.WriteFile(unknown, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Release(t.Context(), a.AttemptID); err == nil {
		t.Fatal("unknown scratch deleted")
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "preserve" {
		t.Fatal("unknown file lost", err)
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.disk.Root, "work", a.AttemptID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("settled helper scratch remains", err)
	}
	current, err := f.service.workspaces.load(a.RunID)
	if err != nil || current.Candidate == nil || *current.Candidate != *source {
		t.Fatal("cleanup changed later source", err)
	}
	f.pin(t, a, source, "one", "ready")
}

type taskSourceFixture struct {
	service    *Service
	disk       *runner.Disk
	repo, base string
}

func newTaskSourceFixture(t *testing.T, shared bool) *taskSourceFixture {
	t.Helper()
	d := workspaceDisk(t)
	repo := sourceRepository(t)
	if shared {
		if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("original\n"), 0600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, repo, "add", "shared.txt")
		fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "shared source")
	}
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	s := NewService(d, func(ctx context.Context, id string) ([]byte, error) {
		return d.Read("artifacts/"+id, d.Policy.MaxArtifact)
	}, nil)
	return &taskSourceFixture{s, d, repo, base}
}
func (f *taskSourceFixture) assignment(item, run, attempt, mode string, paths []string, deps ...runner.TaskCandidate) runner.Assignment {
	if deps == nil {
		deps = []runner.TaskCandidate{}
	}
	a := sourceAssignment(f.disk, f.repo, attempt)
	a.RunID = run
	a.Source.Ref = f.base
	a.Deadline = time.Now().Add(time.Minute)
	a.Setup.Profile = runner.Profile{Harness: "task-source", Mode: mode, Access: "workspace-read"}
	a.TaskSource = &runner.TaskAssignment{Identity: runner.TaskIdentity{RootRunID: "run_plan", GroupStep: "cards", ItemID: item}, WritePaths: paths, Dependencies: deps}
	a.Inputs = map[string]runner.Value{}
	return a
}
func taskReturned(t *testing.T, o runner.Observation, err error, outcome string) runner.StepResult {
	t.Helper()
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "returned" || o.End.Result == nil || o.End.Result.Outcome != outcome {
		t.Fatalf("task result %s: end=%+v source=%+v, %v", outcome, o.End, o.Source, err)
	}
	return *o.End.Result
}
func (f *taskSourceFixture) seed(t *testing.T, item, run string, paths []string, deps ...runner.TaskCandidate) runner.Assignment {
	t.Helper()
	a := f.assignment(item, run, "attempt_"+item+"_seed", "seed", paths, deps...)
	data, _ := json.Marshal(a.TaskSource.Dependencies)
	a.Inputs["dependencies"] = runner.Value{Inline: data}
	o, err := f.service.Start(t.Context(), a)
	taskReturned(t, o, err, "ready")
	if o.Source == nil || o.Source.Tree == "" {
		t.Fatal("seed did not establish candidate source")
	}
	return a
}
func (f *taskSourceFixture) edit(t *testing.T, seed runner.Assignment, suffix string, files map[string]string, remove ...string) *runner.SourceResult {
	t.Helper()
	a := runner.Clone(seed)
	a.AttemptID = "attempt_" + seed.TaskSource.Identity.ItemID + "_owner_" + suffix
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	a.Inputs = map[string]runner.Value{}
	r, err := f.service.workspaces.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range files {
		path := filepath.Join(r.WorkDir, name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range remove {
		if err = os.Remove(filepath.Join(r.WorkDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	source, err := f.service.workspaces.Capture(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	return source
}
func (f *taskSourceFixture) pin(t *testing.T, seed runner.Assignment, source *runner.SourceResult, suffix, outcome string) *runner.TaskCandidate {
	t.Helper()
	a := runner.Clone(seed)
	a.AttemptID = "attempt_" + seed.TaskSource.Identity.ItemID + "_pin_" + suffix
	a.Setup.Profile.Mode = "pin"
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: source.PatchID}, "summary": runner.Text("bounded task fixture")}
	o, err := f.service.Start(t.Context(), a)
	result := taskReturned(t, o, err, outcome)
	if outcome != "ready" {
		if !bytes.Equal(result.Outputs["candidate"].Inline, []byte(`{}`)) {
			t.Fatal("scope violation invented candidate")
		}
		return nil
	}
	var candidate runner.TaskCandidate
	if err = json.Unmarshal(result.Outputs["candidate"].Inline, &candidate); err != nil {
		t.Fatal(err)
	}
	return &candidate
}
func (f *taskSourceFixture) join(t *testing.T, candidates []runner.TaskCandidate, outcome string) (runner.Assignment, runner.Observation) {
	t.Helper()
	paths := []string{}
	m := runner.TaskManifest{RootRunID: "run_plan", GroupStep: "cards", Repository: f.repo, Base: f.base, Request: "Preserve every authorized task"}
	for _, c := range candidates {
		r, err := f.service.workspaces.load(c.RunID)
		if err != nil {
			t.Fatal(err)
		}
		scope := r.TaskSources["cards"].Authority.WritePaths
		paths = append(paths, scope...)
		m.Items = append(m.Items, runner.TaskManifestItem{ID: c.Identity.ItemID, RunID: c.RunID, Candidate: c, WritePaths: scope, Inputs: map[string]json.RawMessage{"task": json.RawMessage(`"accepted brief"`)}})
	}
	// A union may contain a path owned by dependent or conflicting cards once.
	seen := map[string]bool{}
	unique := []string{}
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}
	a := f.assignment("", "run_plan", "attempt_join", "join", unique, candidates...)
	data, _ := json.Marshal(m)
	if err := f.disk.Write("artifacts/attempt_group/results.json", data, f.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	a.Inputs["results"] = runner.Value{ArtifactID: "attempt_group/results.json"}
	o, err := f.service.Start(t.Context(), a)
	taskReturned(t, o, err, outcome)
	return a, o
}

func TestTaskSourcesIsolateSiblingsAndJoinOneRootCandidate(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	a := f.seed(t, "a", "run_a", []string{"a.txt"})
	b := f.seed(t, "b", "run_b", []string{"b.txt"})
	sa := f.edit(t, a, "one", map[string]string{"a.txt": "first task\n"})
	sb := f.edit(t, b, "one", map[string]string{"b.txt": "second task\n"})
	wa, _ := f.service.workspaces.load(a.RunID)
	wb, _ := f.service.workspaces.load(b.RunID)
	if wa.WorkDir == wb.WorkDir {
		t.Fatal("tasks shared a source workspace")
	}
	if _, err := os.Stat(filepath.Join(wb.WorkDir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("sibling source leaked before join", err)
	}
	ca := f.pin(t, a, sa, "one", "ready")
	cb := f.pin(t, b, sb, "one", "ready")
	if !missingCandidateRef(t.Context(), f.repo, candidateRef(a.RunID)) || !missingCandidateRef(t.Context(), f.repo, candidateRef(b.RunID)) {
		t.Fatal("unreviewed task created a publication candidate branch")
	}
	join, o := f.join(t, []runner.TaskCandidate{*ca, *cb}, "ready")
	root, err := f.service.workspaces.load(join.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.txt": "first task\n", "b.txt": "second task\n"} {
		data, e := os.ReadFile(filepath.Join(root.WorkDir, name))
		if e != nil || string(data) != want {
			t.Fatal("combined source omitted task", name, e)
		}
	}
	if o.Source == nil || o.Source.Tree != root.Candidate.Tree {
		t.Fatal("join did not establish authoritative root source")
	}
	// Restart/replay reuses its source and immutable pins without a model turn.
	f.service = NewService(f.disk, func(ctx context.Context, id string) ([]byte, error) {
		return f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
	}, nil)
	again, err := f.service.Start(t.Context(), join)
	taskReturned(t, again, err, "ready")
	if again.Source.Tree != o.Source.Tree || again.Source.PatchID != o.Source.PatchID {
		t.Fatal("join replay changed retained source")
	}
	approved := sourceAssignment(f.disk, f.repo, "attempt_root_reviewed")
	approved.RunID = join.RunID
	approved.Setup.Profile = runner.Profile{Harness: "git-candidate", Access: "workspace-read"}
	approved.Inputs = map[string]runner.Value{"patch": {ArtifactID: o.Source.PatchID}, "verdict": runner.Text("APPROVE"), "review": runner.Text("Independent fixture combined review")}
	if _, err = f.service.workspaces.retainCandidate(t.Context(), approved); err != nil {
		t.Fatal(err)
	}
	parent := strings.TrimSpace(string(fixtureGit(t, f.repo, "rev-parse", candidateRef(join.RunID)+"^")))
	if parent != f.base {
		t.Fatal("final publication candidate lost original root base")
	}
}

func TestTaskSourcesDependenciesSeedAndScopeAreRelativeToInheritedWork(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	a := f.seed(t, "a", "run_a", []string{"shared.txt"})
	sa := f.edit(t, a, "one", map[string]string{"shared.txt": "dependency\n"})
	ca := f.pin(t, a, sa, "one", "ready")
	b := f.seed(t, "b", "run_b", []string{"other.txt"})
	sb := f.edit(t, b, "one", map[string]string{"other.txt": "independent\n"})
	cb := f.pin(t, b, sb, "one", "ready")
	d := f.seed(t, "d", "run_d", []string{"shared.txt"}, *ca, *cb)
	wd, _ := f.service.workspaces.load(d.RunID)
	if data, err := os.ReadFile(filepath.Join(wd.WorkDir, "other.txt")); err != nil || string(data) != "independent\n" {
		t.Fatal("dependency seed omitted inherited scope", err)
	}
	sd := f.edit(t, d, "one", map[string]string{"shared.txt": "dependent extension\n"})
	cd := f.pin(t, d, sd, "one", "ready")
	if _, err := git(t.Context(), f.repo, nil, nil, 256, "merge-base", "--is-ancestor", ca.Commit, cd.Commit); err != nil {
		t.Fatal("task pin lost dependency ancestry", err)
	}
	_, o := f.join(t, []runner.TaskCandidate{*ca, *cb, *cd}, "ready")
	patch, err := f.disk.Read("artifacts/"+o.Source.PatchID, f.disk.Policy.MaxArtifact)
	if err != nil || !bytes.Contains(patch, []byte("dependent extension")) || !bytes.Contains(patch, []byte("independent")) {
		t.Fatal("dependency join lost cumulative source", err)
	}
}

func TestTaskSourcesScopeCorrectionRetainsEvidenceWithoutPinning(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	a := f.seed(t, "a", "run_a", []string{"owned"})
	source := f.edit(t, a, "bad", map[string]string{"owned/a.txt": "valid\n", "outside.txt": "must restore\n"})
	f.pin(t, a, source, "bad", "changes_requested")
	if !missingCandidateRef(t.Context(), f.repo, taskPinRef(a.RunID)) {
		t.Fatal("scope failure created an internal task pin")
	}
	if _, err := f.disk.Read("artifacts/"+source.PatchID, f.disk.Policy.MaxArtifact); err != nil {
		t.Fatal("scope rejection discarded literal patch", err)
	}
	corrected := f.edit(t, a, "fixed", map[string]string{}, "outside.txt")
	if f.pin(t, a, corrected, "fixed", "ready") == nil {
		t.Fatal("scope correction failed")
	}
}

func TestTaskSourcesTextConflictIsRetainedForCombinedRepair(t *testing.T) {
	f := newTaskSourceFixture(t, true)
	a := f.seed(t, "a", "run_a", []string{"shared.txt"})
	b := f.seed(t, "b", "run_b", []string{"shared.txt"})
	sa := f.edit(t, a, "one", map[string]string{"shared.txt": "first version\n"})
	sb := f.edit(t, b, "one", map[string]string{"shared.txt": "second version\n"})
	ca := f.pin(t, a, sa, "one", "ready")
	cb := f.pin(t, b, sb, "one", "ready")
	join, o := f.join(t, []runner.TaskCandidate{*ca, *cb}, "changes_requested")
	r, err := f.service.workspaces.load(join.RunID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.WorkDir, "shared.txt"))
	if err != nil || !bytes.Contains(data, []byte("<<<<<<< current")) || !bytes.Contains(data, []byte("first version")) || !bytes.Contains(data, []byte("second version")) || o.Source == nil {
		t.Fatal("conflict did not preserve editable source evidence", string(data), err)
	}
	for _, c := range []*runner.TaskCandidate{ca, cb} {
		head := strings.TrimSpace(string(fixtureGit(t, f.repo, "rev-parse", taskPinRef(c.RunID))))
		if head != c.Commit {
			t.Fatal("join changed a child pin")
		}
	}
}
