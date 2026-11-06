package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func sourceRecoveryFixture(t *testing.T) (*Workspaces, runner.Assignment, runner.Run, runner.SourceResult) {
	t.Helper()
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "attempt_source")
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.WorkDir, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := w.Capture(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	run := runner.Run{
		ID: a.RunID, Repository: repo, Status: "Failed", Step: 1,
		Source: &runner.SourceSettings{Ref: source.Base}, CleanupBlocked: true, CleanupError: "workspace differs from retained candidate",
		Attempts: []runner.Attempt{
			{ID: a.AttemptID, State: runner.Finished, Released: true, Source: source},
			{ID: "attempt_rejected_check", Step: 1, State: runner.Finished, Released: true},
		},
	}
	return w, a, run, *source
}

func TestSourceRecoveryValidatesPresentAndConfirmedReleasedCandidate(t *testing.T) {
	w, a, run, source := sourceRecoveryFixture(t)
	if err := w.validateSourceRecovery(t.Context(), run, source); err != nil {
		t.Fatal("matching owned checkout rejected", err)
	}
	if err := w.releaseRun(t.Context(), run.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := w.validateSourceRecovery(t.Context(), run, source); err == nil {
		t.Fatal("absent checkout accepted without Core cleanup confirmation")
	}
	run.ResourcesReleased = true
	if err := w.validateSourceRecovery(t.Context(), run, source); err != nil {
		t.Fatal("confirmed cleanup lost recoverable candidate", err)
	}
	if _, err := w.Prepare(t.Context(), a); err != nil {
		t.Fatal("normal preparation could not reconstruct verified candidate", err)
	}
	if data, err := os.ReadFile(filepath.Join(source.WorkDir, "seats.go")); err != nil || string(data) != seatsImplementation {
		t.Fatal("restoration did not use exact saved candidate", err)
	}
}

func TestRealExecutionPreparationRejectsTamperedRetainedPatchOrCheckout(t *testing.T) {
	for _, kind := range []string{"patch", "checkout"} {
		t.Run(kind, func(t *testing.T) {
			w, a, run, source := sourceRecoveryFixture(t)
			if kind == "patch" {
				if err := w.disk.Write("artifacts/"+source.PatchID, []byte("unaccepted patch bytes"), w.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(source.WorkDir, "seats.go"), []byte("unaccepted checkout bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := w.validateSourceRecovery(t.Context(), run, source); err == nil {
				t.Fatal("source integrity drift passed recovery validation")
			}
			for _, harness := range []string{"codex", "command"} {
				a.Setup.Profile.Harness = harness
				if _, err := w.Prepare(t.Context(), a); err == nil {
					t.Fatal("real execution preparation admitted unaccepted source", harness)
				}
			}
		})
	}
}

func preservationGitFailure(t *testing.T, moveBeforeFailure bool) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	move := ""
	if moveBeforeFailure {
		move = `/bin/mv "$previous" "$last"`
	}
	body := fmt.Sprintf(`#!/bin/sh
previous=''; last=''; selected=false
for arg in "$@"; do
 if [ "$last" = worktree ] && [ "$arg" = move ]; then selected=true; fi
 previous="$last"; last="$arg"
done
if [ "$selected" = true ]; then
 %s
 exit 7
fi
exec %s "$@"
`, move, quote(real))
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestExplicitSourcePreservationRejoinsInterruptedMoveAndRetainsAllEvidence(t *testing.T) {
	for _, test := range []struct {
		moved, pendingCleanup bool
	}{{false, false}, {true, false}, {true, true}} {
		t.Run(fmt.Sprint("move-completed-before-reply=", test.moved, "/cleanup-intent=", test.pendingCleanup), func(t *testing.T) {
			w, a, run, source := sourceRecoveryFixture(t)
			if test.pendingCleanup {
				r, err := w.load(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				patch, err := w.disk.Read("artifacts/"+source.PatchID, w.disk.Policy.MaxArtifact)
				if err != nil {
					t.Fatal(err)
				}
				r.CleanupIntent, err = w.cleanupIndex(t.Context(), r, patch)
				if err != nil {
					t.Fatal(err)
				}
				if err := w.save(r); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(filepath.Join(source.WorkDir, "seats.go")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source.WorkDir, "unaccepted.txt"), []byte("retain this evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			patch, err := w.disk.Read("artifacts/"+source.PatchID, w.disk.Policy.MaxArtifact)
			if err != nil {
				t.Fatal(err)
			}
			originalPath := os.Getenv("PATH")
			t.Setenv("PATH", preservationGitFailure(t, test.moved))
			if _, err := w.preserveRecoverySource(t.Context(), run, source); err == nil {
				t.Fatal("interrupted move was reported complete")
			}
			r, err := w.load(run.ID)
			if err != nil || r.SourcePreservation == nil || r.SourcePreservation.Complete {
				t.Fatal("move did not retain its durable intent", err)
			}
			if _, err := w.Prepare(t.Context(), a); err == nil {
				t.Fatal("an unresolved forensic move admitted execution")
			}
			t.Setenv("PATH", originalPath)
			reopened := &Workspaces{disk: w.disk}
			receipt, err := reopened.preserveRecoverySource(t.Context(), run, source)
			if err != nil {
				t.Fatal("same owned move could not reconcile", err)
			}
			data, err := os.ReadFile(filepath.Join(filepath.Dir(receipt.Path), "manifest.json"))
			if err != nil || captureDigest(data) != receipt.ManifestSHA256 || receipt.PatchSHA256 != captureDigest(patch) {
				t.Fatal("forensic provenance is missing or changed", err)
			}
			if _, err := os.Stat(filepath.Join(receipt.Path, "seats.go")); !os.IsNotExist(err) {
				t.Fatal("missing source was silently restored into forensic evidence", err)
			}
			if data, err := os.ReadFile(filepath.Join(receipt.Path, "unaccepted.txt")); err != nil || string(data) != "retain this evidence" {
				t.Fatal("unaccepted source was discarded", err)
			}
			if _, err := reopened.Prepare(t.Context(), a); err != nil {
				t.Fatal("trusted source did not restore normally", err)
			}
			if data, err := os.ReadFile(filepath.Join(source.WorkDir, "seats.go")); err != nil || string(data) != seatsImplementation {
				t.Fatal("restoration accepted suspect contents", err)
			}
			if _, err := os.Stat(filepath.Join(source.WorkDir, "unaccepted.txt")); !os.IsNotExist(err) {
				t.Fatal("unaccepted source entered restored candidate", err)
			}
			got, err := w.disk.Read("artifacts/"+source.PatchID, w.disk.Policy.MaxArtifact)
			if err != nil || !bytes.Equal(got, patch) {
				t.Fatal("preservation changed retained patch bytes", err)
			}
		})
	}
}

func TestSourcePreservationRefusesAmbiguousPathsChangedManifestAndActiveLease(t *testing.T) {
	for _, kind := range []string{"both paths", "manifest", "active lease", "unknown backlink", "unowned Git link", "unsettled execution"} {
		t.Run(kind, func(t *testing.T) {
			w, _, run, source := sourceRecoveryFixture(t)
			originalPath := os.Getenv("PATH")
			t.Setenv("PATH", preservationGitFailure(t, true))
			if _, err := w.preserveRecoverySource(t.Context(), run, source); err == nil {
				t.Fatal("move fault did not interrupt")
			}
			t.Setenv("PATH", originalPath)
			r, err := w.load(run.ID)
			if err != nil || r.SourcePreservation == nil {
				t.Fatal(err)
			}
			path := r.SourcePreservation.Manifest.Path
			switch kind {
			case "both paths":
				err = os.Mkdir(source.WorkDir, 0700)
			case "manifest":
				err = os.WriteFile(filepath.Join(filepath.Dir(path), "manifest.json"), []byte(`{"unaccepted":true}`), 0600)
			case "active lease":
				lease, lockErr := sourcePreservationLease(filepath.Join(filepath.Dir(path), "lease"))
				if lockErr != nil {
					t.Fatal(lockErr)
				}
				defer lease.Close()
			case "unknown backlink":
				err = os.WriteFile(filepath.Join(r.SourcePreservation.Manifest.Identity.GitDir, "gitdir"), []byte("/unknown/.git\n"), 0600)
			case "unowned Git link":
				err = os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /unknown\n"), 0600)
			case "unsettled execution":
				data, marshalErr := json.Marshal(struct{ Assignment runner.Assignment }{runner.Assignment{RunID: run.ID}})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = w.disk.Write("jobs/unknown.codex.json", data, w.disk.Policy.Database.MaxRecord)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(r)
			if _, err := w.preserveRecoverySource(context.Background(), run, source); err == nil {
				t.Fatal("ambiguous/unowned/active preservation was accepted", kind)
			}
			after, _ := w.load(run.ID)
			encoded, _ := json.Marshal(after)
			if !bytes.Equal(before, encoded) {
				t.Fatal("failed preservation advanced its durable evidence", kind)
			}
		})
	}
}
