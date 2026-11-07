package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

// The edits are fixture data. Seed, capture, pin, attempt release, checkout
// cleanup and dependency/join authentication use the real Service and Git.
func pinReleasedTaskAttempts(t *testing.T, f *taskSourceFixture, item, text string, dependencies ...runner.TaskCandidate) (runner.Assignment, *runner.TaskCandidate) {
	t.Helper()
	a := f.seed(t, item, "run_"+item, []string{"shared.txt"}, dependencies...)
	source := f.edit(t, a, "one", map[string]string{"shared.txt": text})
	candidate := f.pin(t, a, source, "one", "ready")
	if err := f.service.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal("seed attempt did not release", err)
	}
	if err := f.service.workspaces.ReleaseAttempt(t.Context(), runner.Assignment{RunID: a.RunID, AttemptID: source.AttemptID}); err != nil {
		t.Fatal("settled fixture edit scratch did not release", err)
	}
	if err := f.service.Release(t.Context(), "attempt_"+item+"_pin_one"); err != nil {
		t.Fatal("pin attempt did not release", err)
	}
	return a, candidate
}

func assertReleasedTaskEvidence(t *testing.T, f *taskSourceFixture, before workspaceRecord, candidate runner.TaskCandidate, patch []byte) {
	t.Helper()
	after, err := f.service.workspaces.load(candidate.RunID)
	if err != nil || !after.Released || !reflect.DeepEqual(after.Candidate, before.Candidate) || !reflect.DeepEqual(after.Capture, before.Capture) || !reflect.DeepEqual(after.TaskSources, before.TaskSources) {
		t.Fatal("checkout release discarded source authentication metadata", after, err)
	}
	if _, err = os.Lstat(before.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("released task checkout was retained or recreated", err)
	}
	data, err := f.disk.Read("artifacts/"+candidate.PatchID, f.disk.Policy.MaxArtifact)
	if err != nil || !bytes.Equal(data, patch) {
		t.Fatal("checkout release changed retained patch evidence", err)
	}
	task := before.TaskSources[candidate.Identity.GroupStep]
	refs := map[string]string{
		taskPinRef(candidate.RunID):                                candidate.Commit,
		taskSeedRef(candidate.RunID, candidate.Identity.GroupStep): task.SeedCommit,
		pin(candidate.RunID):                                       before.Base,
	}
	for attempt, hash := range task.Pins {
		refs[taskPinHistory(candidate.RunID, attempt)] = hash
	}
	for ref, hash := range refs {
		got := strings.TrimSpace(string(fixtureGit(t, f.repo, "rev-parse", "--verify", ref)))
		if got != hash {
			t.Fatal("checkout release forgot an immutable source pin", ref, got, hash)
		}
	}
	if tree, err := taskTree(t.Context(), f.repo, candidate.Commit); err != nil || tree != candidate.Tree {
		t.Fatal("released task lost its common-repository source tree", tree, err)
	}
}

func TestTaskReleasedCheckoutSupportsRestartDependencyJoinAndParentCancellation(t *testing.T) {
	f := newTaskSourceFixture(t, true)
	t.Cleanup(func() { _ = f.service.Close(context.Background()) })
	a, first := pinReleasedTaskAttempts(t, f, "first", "first task\n")
	before, err := f.service.workspaces.load(a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := f.disk.Read("artifacts/"+first.PatchID, f.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReleaseRun(t.Context(), runner.Run{ID: a.RunID, Status: "Completed"}); err != nil {
		t.Fatal("accepted child checkout could not release", err)
	}
	assertReleasedTaskEvidence(t, f, before, *first, patch)
	if err = f.service.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.service = NewService(f.disk, func(_ context.Context, id string) ([]byte, error) {
		return f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
	}, nil)
	assertReleasedTaskEvidence(t, f, before, *first, patch)

	dependent := f.seed(t, "dependent", "run_dependent", []string{"shared.txt"}, *first)
	prepared, err := f.service.workspaces.load(dependent.RunID)
	data, readErr := os.ReadFile(filepath.Join(prepared.WorkDir, "shared.txt"))
	if err != nil || readErr != nil || string(data) != "first task\n" {
		t.Fatal("dependency needed its deleted checkout rather than retained source", string(data), err, readErr)
	}
	source := f.edit(t, dependent, "one", map[string]string{"shared.txt": "dependent task\n"})
	second := f.pin(t, dependent, source, "one", "ready")
	if err = f.service.Release(t.Context(), dependent.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.workspaces.ReleaseAttempt(t.Context(), runner.Assignment{RunID: dependent.RunID, AttemptID: source.AttemptID}); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Release(t.Context(), "attempt_dependent_pin_one"); err != nil {
		t.Fatal(err)
	}
	if err = f.service.ReleaseRun(t.Context(), runner.Run{ID: dependent.RunID, Status: "Completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(prepared.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dependent checkout did not release before join", err)
	}
	join, result := f.join(t, []runner.TaskCandidate{*first, *second}, "ready")
	combined, err := f.service.workspaces.load(join.RunID)
	data, readErr = os.ReadFile(filepath.Join(combined.WorkDir, "shared.txt"))
	if err != nil || readErr != nil || string(data) != "dependent task\n" || result.Source == nil || result.Source.Tree != combined.Candidate.Tree {
		t.Fatal("join failed after both child checkouts were released", string(data), err, readErr)
	}
	if _, err = git(t.Context(), f.repo, nil, nil, 256, "merge-base", "--is-ancestor", first.Commit, second.Commit); err != nil {
		t.Fatal("released dependency lost source ancestry", err)
	}
	if err = f.service.Release(t.Context(), join.AttemptID); err != nil {
		t.Fatal(err)
	}
	// A parent cancellation after the owned join settles can clean its checkout
	// and idempotently rejoin prior child cleanup without forgetting task pins.
	if err = f.service.ReleaseRun(t.Context(), runner.Run{ID: join.RunID, Status: "Cancelled", CancelRequested: true}); err != nil {
		t.Fatal("parent cancellation cleanup failed after early child release", err)
	}
	for _, id := range []string{a.RunID, dependent.RunID} {
		if err = f.service.ReleaseRun(t.Context(), runner.Run{ID: id, Status: "Completed"}); err != nil {
			t.Fatal("released child cleanup was not idempotent", err)
		}
	}
	if _, err = os.Lstat(combined.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled parent retained its settled checkout", err)
	}
	assertReleasedTaskEvidence(t, f, before, *first, patch)
	if _, err = f.disk.Read("artifacts/"+result.Source.PatchID, f.disk.Policy.MaxArtifact); err != nil {
		t.Fatal("cancellation forgot the joined source evidence", err)
	}
}

func TestTaskReleasedCheckoutStillRejectsTamperedOrUnknownCandidates(t *testing.T) {
	for _, mode := range []string{"patch changed", "unknown candidate"} {
		t.Run(mode, func(t *testing.T) {
			f := newTaskSourceFixture(t, true)
			t.Cleanup(func() { _ = f.service.Close(context.Background()) })
			a, candidate := pinReleasedTaskAttempts(t, f, "first", "first task\n")
			if err := f.service.ReleaseRun(t.Context(), runner.Run{ID: a.RunID, Status: "Completed"}); err != nil {
				t.Fatal(err)
			}
			data, err := f.disk.Read("artifacts/"+candidate.PatchID, f.disk.Policy.MaxArtifact)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "patch changed" {
				changed := bytes.ReplaceAll(data, []byte("first task"), []byte("other task"))
				if bytes.Equal(changed, data) || len(changed) != len(data) {
					t.Fatal("invalid same-sized patch tamper fixture")
				}
				if err = f.disk.Write("artifacts/"+candidate.PatchID, changed, f.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
				data = changed
			} else {
				candidate.RunID = "run_unknown"
			}
			dependent := f.assignment("dependent", "run_dependent", "attempt_dependent_seed", "seed", []string{"shared.txt"}, *candidate)
			raw, _ := json.Marshal(dependent.TaskSource.Dependencies)
			dependent.Inputs["dependencies"] = runner.Value{Inline: raw}
			result, err := f.service.Start(t.Context(), dependent)
			if err != nil || result.State != runner.Finished || result.End == nil || result.End.Kind != "failed" || result.Source != nil {
				t.Fatal("released candidate bypassed dependency authentication", result, err)
			}
			if _, err = f.service.workspaces.load(dependent.RunID); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("untrusted dependency created a checkout", err)
			}
			after, err := f.disk.Read("artifacts/"+candidate.PatchID, f.disk.Policy.MaxArtifact)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("rejected dependency destroyed retained evidence", err)
			}
		})
	}
}

func TestTaskReleasedCheckoutPreservesUnknownSourceInsteadOfDeletingIt(t *testing.T) {
	f := newTaskSourceFixture(t, true)
	t.Cleanup(func() { _ = f.service.Close(context.Background()) })
	a, candidate := pinReleasedTaskAttempts(t, f, "first", "first task\n")
	record, err := f.service.workspaces.load(a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(record.WorkDir, "uncollected.txt")
	if err = os.WriteFile(unknown, []byte("preserve unique source"), 0600); err != nil {
		t.Fatal(err)
	}
	var blocked *runner.CleanupBlockedError
	err = f.service.ReleaseRun(t.Context(), runner.Run{ID: a.RunID, Status: "Completed"})
	if !errors.As(err, &blocked) {
		t.Fatal("early cleanup accepted unknown source", err)
	}
	data, readErr := os.ReadFile(unknown)
	if readErr != nil || string(data) != "preserve unique source" {
		t.Fatal("cleanup deleted unretained source", readErr)
	}
	if _, err = f.disk.Read("artifacts/"+candidate.PatchID, f.disk.Policy.MaxArtifact); err != nil {
		t.Fatal("blocked cleanup lost the accepted patch", err)
	}
}
