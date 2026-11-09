package execution

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func addedSeedFile(name, mode string) []byte {
	return []byte("diff --git a/" + name + " b/" + name + "\nnew file mode " + mode + "\n--- /dev/null\n+++ b/" + name + "\n@@ -0,0 +1 @@\n+retained\n")
}

func seedWorkspaceFixture(t *testing.T, patch []byte) (*Workspaces, runner.Assignment) {
	t.Helper()
	d := workspaceDisk(t)
	w := &Workspaces{
		disk:          d,
		readOnlyPaths: []string{"locked.txt"},
		read: func(context.Context, string) ([]byte, error) {
			return bytes.Clone(patch), nil
		},
	}
	a := sourceAssignment(d, sourceRepository(t), "attempt_seed")
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: "retained/changes.patch"}}
	t.Cleanup(func() { _ = w.ForgetRun(context.Background(), a.RunID) })
	return w, a
}

// Failed preparation without a captured candidate is disposable only while
// source still matches its original base. Unique edits/files must be retained.
func TestWorkspaceFailedPreparationCleanup(t *testing.T) {
	for _, change := range []string{"unchanged", "tracked-edit", "uncaptured-file"} {
		t.Run(change, func(t *testing.T) {
			w, a := seedWorkspaceFixture(t, nil)
			a.Inputs = nil
			a.Setup.Profile = runner.Profile{Harness: "command", Access: "workspace-read"}
			ctx := context.Background()
			r, err := w.Prepare(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			if change != "unchanged" {
				name := "unique.txt"
				if change == "tracked-edit" {
					name = "doc.go"
				}
				if err := os.WriteFile(filepath.Join(r.WorkDir, name), []byte("unique source\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = w.releaseRun(ctx, a.RunID, true)
			if change != "unchanged" {
				if err == nil {
					t.Fatal("uncaptured source was discarded")
				}
				return
			}
			if err != nil {
				t.Fatal("unchanged preparation was retained", err)
			}
			if _, err := os.Stat(r.WorkDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unchanged checkout was not removed", err)
			}
			if _, err := w.Prepare(ctx, a); err != nil {
				t.Fatal("cleaned preparation could not be restored", err)
			}
		})
	}
}

func TestWorkspaceSeedCaptureIsCumulativeAndPreservesUserCheckout(t *testing.T) {
	ctx := context.Background()
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d, readOnlyPaths: []string{"go.mod", "doc.go"}}
	w.read = func(_ context.Context, id string) ([]byte, error) {
		return d.Read("artifacts/"+id, d.Policy.MaxArtifact)
	}
	first := sourceAssignment(d, repo, "attempt_retained")
	first.RunID = "run_retained"
	retained, err := w.Prepare(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(ctx, first.RunID)
	for name, data := range map[string][]byte{"seats.go": []byte("retained\n"), "binary": {0, 1, 2}} {
		if err := os.WriteFile(filepath.Join(retained.WorkDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seed, err := w.Capture(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "doc.go"), []byte("user staged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "doc.go")
	if err := os.WriteFile(filepath.Join(repo, "doc.go"), []byte("user unstaged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	index := fixtureGit(t, repo, "diff", "--cached", "--binary")
	checkout := fixtureGit(t, repo, "diff", "--binary")
	a := sourceAssignment(d, repo, "attempt_correction")
	a.RunID = "run_correction"
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: seed.PatchID}}
	r, err := w.Prepare(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(ctx, a.RunID)
	if !r.SeedReady || r.SeedPatchID != seed.PatchID || r.Base != seed.Base || r.Candidate != nil {
		t.Fatalf("seed provenance or acceptance changed: %+v", r)
	}
	if data, err := os.ReadFile(filepath.Join(r.WorkDir, "binary")); err != nil || !bytes.Equal(data, []byte{0, 1, 2}) {
		t.Fatal("binary seed not restored", err)
	}
	if err := os.WriteFile(filepath.Join(r.WorkDir, "correction.go"), []byte("correction\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := w.Capture(ctx, a)
	if err != nil || result.InputPatchID != seed.PatchID || result.Base != seed.Base {
		t.Fatalf("cumulative source provenance: %+v: %v", result, err)
	}
	patch, err := d.Read("artifacts/"+result.PatchID, d.Policy.MaxArtifact)
	if err != nil || !bytes.Contains(patch, []byte("seats.go")) || !bytes.Contains(patch, []byte("correction.go")) || !bytes.Contains(patch, []byte("GIT binary patch")) {
		t.Fatal("capture omitted seed or correction", err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	fixtureGit(t, repo, "worktree", "add", "--detach", restored, seed.Base)
	defer fixtureGit(t, repo, "worktree", "remove", "--force", restored)
	if _, err := git(ctx, restored, nil, patch, 4096, "apply", "--index", "--binary"); err != nil {
		t.Fatal("cumulative patch does not apply to the accepted base", err)
	}
	for name, want := range map[string][]byte{"seats.go": []byte("retained\n"), "correction.go": []byte("correction\n"), "binary": {0, 1, 2}} {
		if got, err := os.ReadFile(filepath.Join(restored, name)); err != nil || !bytes.Equal(got, want) {
			t.Fatal("cumulative restore differs", name, err)
		}
	}
	if !bytes.Equal(index, fixtureGit(t, repo, "diff", "--cached", "--binary")) || !bytes.Equal(checkout, fixtureGit(t, repo, "diff", "--binary")) {
		t.Fatal("seed changed the user's checkout or index")
	}
}

func TestWorkspaceSeedRejectsOversizedDecodedBinary(t *testing.T) {
	w, a := seedWorkspaceFixture(t, nil)
	r, err := w.Prepare(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.WorkDir, "binary"), make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	retained, err := w.Capture(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := w.disk.Read("artifacts/"+retained.PatchID, w.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	a.RunID = "run_decoded"
	a.AttemptID = "attempt_decoded"
	a.Limits.MaxArtifact = runner.Bytes(len(patch) + 1)
	a.Inputs["patch"] = runner.Value{ArtifactID: retained.PatchID}
	w.read = func(context.Context, string) ([]byte, error) { return patch, nil }
	defer w.ForgetRun(context.Background(), a.RunID)
	if _, err := w.Prepare(context.Background(), a); err == nil || !strings.Contains(err.Error(), "oversized file") {
		t.Fatal("small compressed seed expanded beyond the file allowance", err)
	}
}

func TestWorkspaceSeedPreparationReplayPreservesReadyEdits(t *testing.T) {
	for _, missingGitLink := range []bool{false, true} {
		name := "after-apply"
		if missingGitLink {
			name = "partial-checkout"
		}
		t.Run(name, func(t *testing.T) {
			w, a := seedWorkspaceFixture(t, addedSeedFile("seed.txt", "100644"))
			r, err := w.Prepare(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			r.SeedReady = false // The readiness save was lost before any model turn.
			if err := w.save(r); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(r.WorkDir, "partial.txt"), []byte("pre-model residue"), 0600); err != nil {
				t.Fatal(err)
			}
			if missingGitLink {
				if err := os.Remove(filepath.Join(r.WorkDir, ".git")); err != nil {
					t.Fatal(err)
				}
			}
			reopened := &Workspaces{disk: w.disk, read: w.read, readOnlyPaths: w.readOnlyPaths}
			r, err = reopened.Prepare(context.Background(), a)
			if err != nil || !r.SeedReady {
				t.Fatalf("seed replay: %+v: %v", r, err)
			}
			if _, err := os.Stat(filepath.Join(r.WorkDir, "partial.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pre-model residue survived rebuild", err)
			}
			if data, err := os.ReadFile(filepath.Join(r.WorkDir, "seed.txt")); err != nil || string(data) != "retained\n" {
				t.Fatal("seed replay lost source", err)
			}
			if err := os.WriteFile(filepath.Join(r.WorkDir, "seed.txt"), []byte("model correction\n"), 0600); err != nil {
				t.Fatal(err)
			}
			reopened.read = func(context.Context, string) ([]byte, error) { return nil, errors.New("must not replay a ready seed") }
			if _, err := reopened.Prepare(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(r.WorkDir, "seed.txt")); err != nil || string(data) != "model correction\n" {
				t.Fatal("ready workspace edits were reset", err)
			}
			a.Inputs["patch"] = runner.Value{ArtifactID: "another/patch"}
			if _, err := reopened.Prepare(context.Background(), a); err == nil {
				t.Fatal("existing workspace accepted another seed")
			}
		})
	}
}

func TestWorkspaceSeedRejectsUnsafeChanges(t *testing.T) {
	for _, name := range []string{".git/config", ".codex/config.toml", ".agents/skills/x", ".aws/config", ".runner-tmp/cache", ".gitattributes", "nested/.gitattributes", ".gitmodules", "locked.txt", "../escape", "/absolute"} {
		t.Run(name, func(t *testing.T) {
			w, a := seedWorkspaceFixture(t, addedSeedFile(name, "100644"))
			if _, err := w.Prepare(context.Background(), a); err == nil {
				t.Fatal("unsafe seed accepted")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		patch []byte
		limit runner.Bytes
	}{
		{"malformed", []byte("not a patch"), 4096},
		{"symlink", addedSeedFile("link", "120000"), 4096},
		{"oversized", []byte("oversized"), 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, a := seedWorkspaceFixture(t, tc.patch)
			a.Limits.MaxArtifact = tc.limit
			if _, err := w.Prepare(context.Background(), a); err == nil {
				t.Fatal("invalid seed accepted")
			}
		})
	}
	w, a := seedWorkspaceFixture(t, []byte("diff --git a/doc.go b/moved.go\nsimilarity index 100%\nrename from doc.go\nrename to moved.go\n"))
	w.readOnlyPaths = []string{"doc.go"}
	if _, err := w.Prepare(context.Background(), a); err == nil || !strings.Contains(err.Error(), "read-only source") {
		t.Fatal("rename of a protected source file accepted", err)
	}
}

func TestWorkspaceEmptySeedAndUnownedDirectory(t *testing.T) {
	w, a := seedWorkspaceFixture(t, nil)
	r, err := w.Prepare(context.Background(), a)
	if err != nil || !r.SeedReady || r.SeedPatchID == "" || r.Candidate != nil {
		t.Fatalf("empty seed must be an explicit ready baseline, not acceptance: %+v: %v", r, err)
	}
	w2, a2 := seedWorkspaceFixture(t, addedSeedFile("seed.txt", "100644"))
	foreign := filepath.Join(w2.disk.Root, "work", a2.RunID, "source")
	if err := os.MkdirAll(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(foreign, "user-file")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Prepare(context.Background(), a2); err == nil {
		t.Fatal("unowned source directory replaced")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatal("unowned source data lost", err)
	}
}

func TestNativePayloadDescribesRestoredSeed(t *testing.T) {
	a := runner.Assignment{
		Task:        "Correct the retained source.",
		Setup:       runner.ExecutionSetup{Profile: *nativeSnapshot().Projects["demo"].Roles["summary"].Execution},
		Inputs:      map[string]runner.Value{"patch": {ArtifactID: "retained/changes.patch"}},
		OutputTypes: map[string]string{"summary": "text", "patch": "artifact"},
	}
	a.Setup.Profile.Access = "workspace-write"
	payload, _, err := nativePayload(a)
	if err != nil || !strings.Contains(payload, "already restored") || strings.Contains(payload, "retained/changes.patch") {
		t.Fatal("seed payload must refer to restored source rather than patch contents", payload, err)
	}
	a.Setup.Profile.Access = "input-only"
	if _, _, err := nativePayload(a); err == nil {
		t.Fatal("input-only native payload accepted a seed artifact")
	}
	a.Inputs = map[string]runner.Value{"feedback": {Inline: []byte(`{"failure":"literal feedback"}`)}, "empty": {Inline: []byte(`null`)}}
	a.OutputTypes = map[string]string{"summary": "text"}
	payload, _, err = nativePayload(a)
	if err != nil || !strings.Contains(payload, `"feedback":{"failure":"literal feedback"}`) || !strings.Contains(payload, `"empty":null`) {
		t.Fatal("JSON feedback was changed or rejected", payload, err)
	}
}
