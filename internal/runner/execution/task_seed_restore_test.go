package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func releasedRootTaskSeed(t *testing.T, pending bool) (*taskSourceFixture, runner.Assignment, taskSourceJob, workspaceRecord) {
	t.Helper()
	f := newTaskSourceFixture(t, true)
	t.Cleanup(func() { _ = f.service.Close(context.Background()) })
	seed, candidate := pinReleasedTaskAttempts(t, f, "first", "retained card\n")
	if err := f.service.ReleaseRun(t.Context(), runner.Run{ID: seed.RunID, Status: "Completed"}); err != nil {
		t.Fatal(err)
	}
	a := f.joinAssignment(t, []runner.TaskCandidate{*candidate})
	baseline := runner.Clone(a)
	baseline.TaskSource = nil
	baseline.Setup.Profile = runner.Profile{Harness: "command", Access: "workspace-read"}
	baseline.Inputs = nil
	if _, err := f.service.workspaces.Prepare(t.Context(), baseline); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReleaseRun(t.Context(), runner.Run{ID: a.RunID, Status: "Failed"}); err != nil {
		t.Fatal(err)
	}
	r, err := f.service.workspaces.load(a.RunID)
	if err != nil || !r.Released || r.Candidate != nil {
		t.Fatal("fixture did not establish a released read-only root", err)
	}
	var j taskSourceJob
	if pending {
		tree, commit, patch, conflicts, err := f.service.workspaces.assembleTaskSource(t.Context(), a)
		if err != nil || len(conflicts) != 0 {
			t.Fatal(err)
		}
		raw, err := f.disk.Read("artifacts/"+a.Inputs["results"].ArtifactID, f.disk.Policy.MaxArtifact)
		if err != nil {
			t.Fatal(err)
		}
		j = taskSourceJob{
			Assignment: runner.Clone(a),
			InputSHA:   captureDigest(raw),
			Tree:       tree,
			Commit:     commit,
			PatchID:    a.AttemptID + "/changes.patch",
		}
		if err := f.disk.Write("artifacts/"+j.PatchID, patch, f.disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		r.TaskSources = map[string]*taskWorkspaceRecord{"cards": {
			Authority:   runner.Clone(*a.TaskSource),
			InputSHA:    j.InputSHA,
			SeedTree:    tree,
			SeedCommit:  commit,
			SeedPatchID: j.PatchID,
			SeedSHA:     captureDigest(patch),
		}}
		r.SeedPatchID = j.PatchID
		if err := f.service.workspaces.save(r); err != nil {
			t.Fatal(err)
		}
		if err := f.service.tasks.save(j); err != nil {
			t.Fatal(err)
		}
	}
	return f, a, j, r
}

func TestTaskPendingSeedWaitsForInheritedLeaseAndCancellationPreservesSource(t *testing.T) {
	f, a, _, before := releasedRootTaskSeed(t, true)
	_, lease, err := f.service.workspaces.taskLease(a)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	o, err := f.service.Observe(ctx, a.AttemptID)
	cancel()
	if err != nil || o.State != runner.Running || !o.Reconciling {
		t.Fatal("inherited writer was bypassed", o, err)
	}
	current, err := f.service.workspaces.load(a.RunID)
	if err != nil || !reflect.DeepEqual(current, before) {
		t.Fatal("active writer changed the pending seed", err)
	}
	if err := f.service.Release(t.Context(), a.AttemptID); err == nil {
		t.Fatal("unsettled writer released")
	}
	if err := f.service.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	f.service = NewService(f.disk, func(_ context.Context, id string) ([]byte, error) {
		return f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
	}, nil)
	// Stop the saved operation before it resumes. Existing cleanup completes only
	// this same source operation, preserving its captured edits as stopped proof.
	if err := f.service.Stop(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	o, err = f.service.Observe(t.Context(), a.AttemptID)
	if err != nil || o.End == nil || o.End.Kind != "stopped" {
		t.Fatal("cancelled pending seed was accepted or lost", o, err)
	}
	patch, err := f.disk.Read("artifacts/"+before.SeedPatchID, f.disk.Policy.MaxArtifact)
	if err != nil || captureDigest(patch) != before.TaskSources["cards"].SeedSHA {
		t.Fatal("cancellation discarded the authenticated assembled source", err)
	}
	if err := f.service.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ReleaseRun(t.Context(), runner.Run{ID: a.RunID, Status: "Cancelled", CancelRequested: true}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskSeedInstallsOnReleasedRootAndRejoinsPendingIntent(t *testing.T) {
	for _, mode := range []string{"fresh", "pending restart", "pending new visit"} {
		t.Run(mode, func(t *testing.T) {
			f, a, j, before := releasedRootTaskSeed(t, mode != "fresh")
			if mode != "fresh" {
				// General restoration remains fail-closed for an unready seed.
				ordinary := runner.Clone(a)
				ordinary.TaskSource = nil
				ordinary.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
				ordinary.Inputs = map[string]runner.Value{"patch": {ArtifactID: before.SeedPatchID}}
				if _, err := f.service.workspaces.Prepare(t.Context(), ordinary); err == nil {
					t.Fatal("general restoration accepted a pending seed")
				}
				if mode == "pending new visit" {
					j.End = &runner.ExecutionEnd{Kind: "failed", Reason: "historical seed refusal"}
					if err := f.service.tasks.save(j); err != nil {
						t.Fatal(err)
					}
					if err := f.service.Release(t.Context(), a.AttemptID); err != nil {
						t.Fatal(err)
					}
					a.AttemptID = "attempt_next_join"
				}
				if err := f.service.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				f.service = NewService(f.disk, func(_ context.Context, id string) ([]byte, error) {
					return f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
				}, nil)
			}
			o, err := f.service.Start(t.Context(), a)
			taskReturned(t, o, err, "ready")
			r, err := f.service.workspaces.load(a.RunID)
			if err != nil || r.Released || !r.SeedReady || !r.TaskSources["cards"].Installed || r.Base != before.Base || r.Repository != before.Repository || o.Source == nil || o.Source.Tree != a.TaskSource.Dependencies[0].Tree {
				t.Fatal("task seed did not finish authenticated installation", err)
			}
			if mode != "fresh" && r.SeedPatchID != before.SeedPatchID {
				t.Fatal("pending seed was silently replaced")
			}
			again, err := f.service.Start(t.Context(), a)
			if err != nil || !reflect.DeepEqual(again.Source, o.Source) {
				t.Fatal("same seed operation was replayed", err)
			}
			if err := f.service.Release(t.Context(), a.AttemptID); err != nil {
				t.Fatal(err)
			}
			if err := f.service.ReleaseRun(t.Context(), runner.Run{ID: a.RunID, Status: "Cancelled", CancelRequested: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(before.WorkDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("owned checkout remained after cancellation cleanup", err)
			}
		})
	}
}

func TestTaskPendingSeedRefusesChangedAuthorityAndPreservesIntent(t *testing.T) {
	for _, mode := range []string{"seed bytes", "missing seed", "manifest", "authority", "tree", "seed ref", "foreign job", "unknown lease", "dependency missing", "dependency ref", "dependency patch", "foreign dependency", "dependency writer"} {
		t.Run(mode, func(t *testing.T) {
			f, a, j, r := releasedRootTaskSeed(t, true)
			switch mode {
			case "dependency writer":
				child, err := f.service.workspaces.load(a.TaskSource.Dependencies[0].RunID)
				if err != nil {
					t.Fatal(err)
				}
				if err := captureDirectory(f.disk.Root, "work", child.Capture.AttemptID, "capture-v1"); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(f.disk.Root, "work", child.Capture.AttemptID, "capture-v1", "lease")
				lease, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				if owned, err := tryCaptureLease(lease); err != nil || !owned {
					t.Fatal("fixture did not hold the dependency lease", err)
				}
			case "dependency missing":
				fixtureGit(t, f.repo, "update-ref", "-d", taskPinRef(a.TaskSource.Dependencies[0].RunID))
			case "dependency ref":
				fixtureGit(t, f.repo, "update-ref", taskPinRef(a.TaskSource.Dependencies[0].RunID), r.Base)
			case "dependency patch":
				_ = f.disk.Write("artifacts/"+a.TaskSource.Dependencies[0].PatchID, []byte("changed"), f.disk.Policy.MaxArtifact)
			case "foreign dependency":
				child, err := f.service.workspaces.load(a.TaskSource.Dependencies[0].RunID)
				if err != nil {
					t.Fatal(err)
				}
				child.TaskSources["cards"].Authority.Identity.RootRunID = "foreign"
				if err := f.service.workspaces.save(child); err != nil {
					t.Fatal(err)
				}
			case "seed bytes":
				_ = f.disk.Write("artifacts/"+j.PatchID, []byte("changed"), f.disk.Policy.MaxArtifact)
			case "missing seed":
				_ = f.disk.RemoveFile("artifacts/" + j.PatchID)
			case "manifest":
				_ = f.disk.Write("artifacts/"+a.Inputs["results"].ArtifactID, []byte(`{}`), f.disk.Policy.MaxArtifact)
			case "authority":
				r.TaskSources["cards"].Authority.Identity.RootRunID = "foreign"
			case "tree":
				r.TaskSources["cards"].SeedTree = r.Base
			case "seed ref":
				fixtureGit(t, f.repo, "update-ref", taskSeedRef(a.RunID, "cards"), r.Base)
			case "foreign job":
				data, _ := json.Marshal(taskSourceJob{Assignment: runner.Assignment{RunID: a.RunID}})
				_ = f.disk.Write("jobs/foreign.task-source.json", data, f.disk.Policy.Database.MaxRecord)
			case "unknown lease":
				lease := filepath.Join(f.disk.Root, "work", a.AttemptID, "task-source", "lease")
				_ = os.Remove(lease)
				if err := os.Symlink(t.TempDir(), lease); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.service.workspaces.save(r); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(r)
			o, err := f.service.Observe(t.Context(), a.AttemptID)
			if err != nil || o.End == nil || o.End.Kind != "failed" || o.Source != nil {
				t.Fatal("untrusted seed was installed", mode, o, err)
			}
			after, err := f.service.workspaces.load(a.RunID)
			raw, _ := json.Marshal(after)
			if err != nil || string(raw) != string(before) {
				t.Fatal("refusal changed the durable seed/ownership intent", err)
			}
			if _, err := os.Stat(r.WorkDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused seed created a checkout", err)
			}
		})
	}
}

func TestTaskInstalledSeedCaptureRejoinKeepsManifestAndWriterGuards(t *testing.T) {
	for _, mode := range []string{"foreign capture", "changed manifest", "unready seed", "uninstalled seed", "active own writer"} {
		t.Run(mode, func(t *testing.T) {
			f := newTaskSourceFixture(t, false)
			t.Cleanup(func() { _ = f.service.Close(context.Background()) })
			gate := captureGitGate(t, "tree", false)
			a := startBlockedTaskSeed(t, f, gate)
			if err := f.service.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			r, err := f.service.workspaces.load(a.RunID)
			if err != nil || !r.SeedReady || !r.TaskSources["cards"].Installed || r.Capture == nil || r.Candidate != nil {
				t.Fatal("fixture did not stop in its own unfinished literal capture", err)
			}
			var lease *os.File
			switch mode {
			case "foreign capture":
				r.Capture.AttemptID = "attempt_foreign"
			case "changed manifest":
				if err := f.disk.Write("work/"+a.AttemptID+"/capture-v1/manifest.json", []byte(`{}`), f.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			case "unready seed":
				r.SeedReady = false
			case "uninstalled seed":
				r.TaskSources["cards"].Installed = false
			case "active own writer":
				lease, err = os.OpenFile(filepath.Join(f.disk.Root, "work", a.AttemptID, "capture-v1", "lease"), os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				if owned, err := tryCaptureLease(lease); err != nil || !owned {
					t.Fatal("fixture did not hold its own capture writer lease", err)
				}
			}
			if err := f.service.workspaces.save(r); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(r)
			f.service = NewService(f.disk, func(_ context.Context, id string) ([]byte, error) {
				return f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
			}, nil)
			ctx := t.Context()
			cancel := func() {}
			if mode == "active own writer" {
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
			}
			o, err := f.service.Observe(ctx, a.AttemptID)
			cancel()
			if mode == "active own writer" {
				if err != nil || o.State != runner.Running || !o.Reconciling {
					t.Fatal("inherited own capture writer was bypassed", o, err)
				}
			} else if err != nil || o.End == nil || o.End.Kind != "failed" || o.Source != nil {
				t.Fatal("untrusted capture was accepted", o, err)
			}
			after, err := f.service.workspaces.load(a.RunID)
			raw, _ := json.Marshal(after)
			if err != nil || string(before) != string(raw) {
				t.Fatal("capture refusal/wait changed saved source ownership", err)
			}
			if lease != nil {
				lease.Close()
				o, err = f.service.Observe(t.Context(), a.AttemptID)
				taskReturned(t, o, err, "ready")
				if o.Source == nil || o.Source.AttemptID != a.AttemptID {
					t.Fatal("settled capture did not rejoin its original attempt", o)
				}
			}
		})
	}
}
