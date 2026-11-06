package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func cleanupFixture(t *testing.T) (*Workspaces, workspaceRecord) {
	t.Helper()
	d, repo := workspaceDisk(t), sourceRepository(t)
	if err := os.Mkdir(filepath.Join(repo, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"nested/keep.txt": "baseline keep\n", "nested/delete.txt": "baseline delete\n", ".gitignore": "ignored*.bin\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "cleanup fixture")
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "cleanup_capture")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.WorkDir, "nested", "keep.txt"), []byte("retained keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Capture(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	r, err = w.load(a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return w, r
}

func interruptedCleanup(t *testing.T, w *Workspaces, r workspaceRecord, preserveCandidate ...bool) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	readyPath, holdPath := filepath.Join(dir, "ready"), filepath.Join(dir, "hold")
	for _, path := range []string{readyPath, holdPath} {
		if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	ready, err := os.OpenFile(readyPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	hold, err := os.OpenFile(holdPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := fmt.Sprintf(`#!/bin/sh
last=''
remove=false
for arg in "$@"; do
 if [ "$last" = worktree ] && [ "$arg" = remove ]; then remove=true; fi
 last=$arg
done
if [ "$remove" = true ] && [ ! -f %s ]; then
 : > %s
 /bin/rm -f %s %s
 printf ready > %s
 IFS= read -r release < %s
fi
exec %s "$@"
`, quote(filepath.Join(dir, "visited")), quote(filepath.Join(dir, "visited")), quote(filepath.Join(r.WorkDir, "nested", "delete.txt")), quote(filepath.Join(r.WorkDir, ".gitignore")), quote(readyPath), quote(holdPath), quote(real))
	if err = os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	original := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+original)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	preserve := len(preserveCandidate) == 0 || preserveCandidate[0]
	go func() { done <- w.releaseRun(ctx, r.RunID, preserve) }()
	signalled := make(chan error, 1)
	go func() { var b [5]byte; _, err := ready.Read(b[:]); signalled <- err }()
	select {
	case err := <-signalled:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatal("cleanup never reached deletion", err)
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup deletion did not start")
	}
	saved, err := w.load(r.RunID)
	if err != nil || saved.CleanupIntent == nil || saved.CleanupIntent.PatchSHA != r.Capture.PatchSHA {
		t.Fatal("source deletion started without committed intent", saved, err)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("interrupted deletion was reported complete")
	}
	t.Setenv("PATH", original)
	if _, err = os.Lstat(filepath.Join(r.WorkDir, "nested", "delete.txt")); !os.IsNotExist(err) {
		t.Fatal("fixture did not delete subset", err)
	}
}

func TestWorkspaceNormalCleanupPinsOnlyInitiallyIgnoredCaches(t *testing.T) {
	for _, scenario := range []string{"resume", "new ignored cache", "new source", "changed survivor", "ignored authority changed"} {
		t.Run(scenario, func(t *testing.T) {
			w, r := cleanupFixture(t)
			if err := os.WriteFile(filepath.Join(r.WorkDir, "ignored.bin"), []byte("ordinary ignored cache"), 0600); err != nil {
				t.Fatal(err)
			}
			interruptedCleanup(t, w, r, false)
			saved, err := w.load(r.RunID)
			if err != nil || saved.CleanupIntent == nil || !saved.CleanupIntent.AllowIgnored || saved.CleanupIntent.IgnoredSHA == "" {
				t.Fatal("normal cache authority was not pinned", err)
			}
			if _, err = os.Lstat(filepath.Join(r.WorkDir, ".gitignore")); !os.IsNotExist(err) {
				t.Fatal("fixture did not remove ignore policy during deletion", err)
			}
			err = nil
			switch scenario {
			case "new ignored cache":
				if err = os.WriteFile(filepath.Join(r.WorkDir, ".gitignore"), []byte("ignored*.bin\n"), 0600); err == nil {
					err = os.WriteFile(filepath.Join(r.WorkDir, "ignored-new.bin"), []byte("new unrecorded cache"), 0600)
				}
			case "new source":
				err = os.WriteFile(filepath.Join(r.WorkDir, "foreign.txt"), []byte("new source"), 0600)
			case "changed survivor":
				err = os.WriteFile(filepath.Join(r.WorkDir, "nested", "keep.txt"), []byte("changed after intent"), 0600)
			case "ignored authority changed":
				err = os.WriteFile(filepath.Join(w.disk.Root, "work", r.RunID, "cleanup", "ignored"), []byte("ignored-new.bin\x00"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = (&Workspaces{disk: w.disk}).releaseRun(t.Context(), r.RunID, false)
			if scenario == "resume" {
				if err != nil {
					t.Fatal("known ignored cache did not rejoin after ignore-file deletion", err)
				}
				return
			}
			var blocked *runner.CleanupBlockedError
			if !errors.As(err, &blocked) {
				t.Fatal("normal cleanup admitted uncaptured source or authority", scenario, err)
			}
			if _, err = os.Lstat(r.WorkDir); err != nil {
				t.Fatal("blocked normal source was deleted", err)
			}
		})
	}
}

func TestWorkspaceNormalCleanupRequiresUnchangedTrackedSourceBeforeIntent(t *testing.T) {
	for _, scenario := range []string{"changed", "deleted", "unignored", "strict failed ignored cache"} {
		t.Run(scenario, func(t *testing.T) {
			w, r := cleanupFixture(t)
			var err error
			switch scenario {
			case "changed":
				err = os.WriteFile(filepath.Join(r.WorkDir, "nested", "keep.txt"), []byte("uncaptured edit"), 0600)
			case "deleted":
				err = os.Remove(filepath.Join(r.WorkDir, "nested", "delete.txt"))
			case "unignored":
				err = os.WriteFile(filepath.Join(r.WorkDir, "untracked.txt"), []byte("uncaptured source"), 0600)
			case "strict failed ignored cache":
				err = os.WriteFile(filepath.Join(r.WorkDir, "ignored.bin"), []byte("failed-run cache remains forensic evidence"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			var blocked *runner.CleanupBlockedError
			if err = w.releaseRun(t.Context(), r.RunID, scenario == "strict failed ignored cache"); !errors.As(err, &blocked) {
				t.Fatal("unverified source entered cleanup", scenario, err)
			}
			saved, err := w.load(r.RunID)
			if err != nil || saved.CleanupIntent != nil {
				t.Fatal("unverified source acquired deletion authority", err)
			}
			if _, err = os.Lstat(r.WorkDir); err != nil {
				t.Fatal("unverified source was deleted", err)
			}
		})
	}
}

func TestWorkspaceCleanupIntentResumesOnlyOwnedDeletion(t *testing.T) {
	for _, scenario := range []string{"resume", "deletion finished acknowledgement lost", "modified survivor", "unexpected source", "unexpected sibling", "ignored source", "Git link changed", "root replaced", "index changed", "patch changed", "unsettled job", "busy cleanup lease"} {
		t.Run(scenario, func(t *testing.T) {
			w, r := cleanupFixture(t)
			patch, err := w.disk.Read("artifacts/"+r.Candidate.PatchID, w.disk.Policy.MaxArtifact)
			if err != nil {
				t.Fatal(err)
			}
			interruptedCleanup(t, w, r)
			var held *os.File
			switch scenario {
			case "deletion finished acknowledgement lost":
				fixtureGit(t, r.Repository, "worktree", "remove", "--force", r.WorkDir)
			case "modified survivor":
				err = os.WriteFile(filepath.Join(r.WorkDir, "nested", "keep.txt"), []byte("foreign edited bytes\n"), 0600)
			case "unexpected source":
				err = os.WriteFile(filepath.Join(r.WorkDir, "unexpected.txt"), []byte("uncaptured\n"), 0600)
			case "unexpected sibling":
				err = os.WriteFile(filepath.Join(filepath.Dir(r.WorkDir), "foreign.txt"), []byte("uncaptured sibling\n"), 0600)
			case "ignored source":
				err = os.WriteFile(filepath.Join(r.WorkDir, "ignored.bin"), []byte("uncaptured ignored\n"), 0600)
			case "Git link changed":
				err = os.WriteFile(filepath.Join(r.WorkDir, ".git"), []byte("gitdir: /unowned/admin\n"), 0600)
			case "root replaced":
				link, readErr := os.ReadFile(filepath.Join(r.WorkDir, ".git"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = os.Rename(r.WorkDir, r.WorkDir+"-preserved")
				if err == nil {
					err = os.Mkdir(r.WorkDir, 0700)
				}
				if err == nil {
					err = os.WriteFile(filepath.Join(r.WorkDir, ".git"), link, 0600)
				}
			case "index changed":
				err = os.WriteFile(filepath.Join(w.disk.Root, "work", r.RunID, "cleanup", "index"), []byte("foreign index"), 0600)
			case "patch changed":
				err = os.WriteFile(filepath.Join(w.disk.Root, "artifacts", filepath.FromSlash(r.Candidate.PatchID)), append(append([]byte{}, patch...), '\n'), 0600)
			case "unsettled job":
				err = w.disk.Write("jobs/active.command.json", []byte(fmt.Sprintf(`{"assignment":{"run_id":%q}}`, r.RunID)), w.disk.Policy.Database.MaxRecord)
			case "busy cleanup lease":
				held, err = w.lockCleanup(r)
			}
			if err != nil {
				t.Fatal(err)
			}
			if held != nil {
				defer held.Close()
			}
			err = (&Workspaces{disk: w.disk}).releaseRun(t.Context(), r.RunID, true)
			if scenario != "resume" && scenario != "deletion finished acknowledgement lost" {
				if err == nil {
					t.Fatal("ambiguous source was deleted", scenario)
				}
				if _, statErr := os.Lstat(r.WorkDir); statErr != nil {
					t.Fatal("blocked source was not preserved", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal("owned deletion did not resume", err)
			}
			saved, err := w.load(r.RunID)
			if err != nil || !saved.Released || saved.CleanupIntent != nil || saved.Candidate == nil || *saved.Candidate != *r.Candidate {
				t.Fatal("cleanup lost source record", saved, err)
			}
			actual, err := w.disk.Read("artifacts/"+r.Candidate.PatchID, w.disk.Policy.MaxArtifact)
			if err != nil || !bytes.Equal(actual, patch) {
				t.Fatal("cleanup changed retained source artifact", err)
			}
			if _, err = os.Lstat(r.WorkDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("owned checkout was not removed", err)
			}
		})
	}
}

func TestWorkspaceCleanupPreservesPendingSourcePreservation(t *testing.T) {
	w, r := cleanupFixture(t)
	r.SourcePreservation = &sourcePreservation{}
	if err := w.save(r); err != nil {
		t.Fatal(err)
	}
	var blocked *runner.CleanupBlockedError
	if err := w.releaseRun(t.Context(), r.RunID, true); !errors.As(err, &blocked) {
		t.Fatal("cleanup raced pending forensic preservation", err)
	}
	saved, err := w.load(r.RunID)
	if err != nil || saved.CleanupIntent != nil || saved.SourcePreservation == nil {
		t.Fatal("pending preservation record was changed", err)
	}
	if data, err := os.ReadFile(filepath.Join(r.WorkDir, "nested", "keep.txt")); err != nil || string(data) != "retained keep\n" {
		t.Fatal("pending source was changed", err)
	}
}

func TestWorkspaceCleanupStreamsLegalDeletionListBeyondOneMiB(t *testing.T) {
	repo := sourceRepository(t)
	ctx := t.Context()
	blob, err := git(ctx, repo, nil, []byte("x"), 256, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	const files = 11000
	var entries strings.Builder
	for i := 0; i < files; i++ {
		fmt.Fprintf(&entries, "100644 blob %s\tinventory-%05d-%s.txt%c", strings.TrimSpace(string(blob)), i, strings.Repeat("x", 80), 0)
	}
	tree, err := git(ctx, repo, nil, []byte(entries.String()), 256, "mktree", "-z")
	if err != nil {
		t.Fatal(err)
	}
	treeID := strings.TrimSpace(string(tree))
	if size, err := sourceTreeBytes(ctx, repo, nil, treeID, files, true); err != nil || size != files {
		t.Fatal("source is not admissible", size, err)
	}
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index")}
	if _, err = git(ctx, repo, env, nil, 4096, "read-tree", treeID); err != nil {
		t.Fatal(err)
	}
	metadata, err := git(ctx, repo, env, nil, 3<<20, "diff", "--name-status", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--")
	if err != nil || len(metadata) <= 1<<20 {
		t.Fatal("fixture did not exceed old deletion-output cap", len(metadata), err)
	}
	if err = cleanupDeletions(ctx, repo, env, true); err != nil {
		t.Fatal("legal deletion-only source was rejected", err)
	}
	if err = cleanupDeletions(ctx, repo, env, false); err == nil {
		t.Fatal("fresh unchanged proof accepted deleted source")
	}
}

func TestWorkspaceCleanupDeletionRecordsRemainStrictAndBounded(t *testing.T) {
	for _, raw := range []string{"M\x00file\x00", "D\x00", "D\x00file", "D\x00\x00", "D\x00../outside\x00", "D\x00.\x00", "D\x00file\x00trailing", "D\x00" + strings.Repeat("x", (1<<20)+1) + "\x00"} {
		if err := scanCleanupDeletions(strings.NewReader(raw), true); err == nil {
			t.Fatal("malformed deletion proof accepted", len(raw))
		}
	}
	if err := scanCleanupDeletions(strings.NewReader("D\x00safe/path\x00D\x00other\x00"), true); err != nil {
		t.Fatal(err)
	}
	if err := scanCleanupDeletions(strings.NewReader(""), false); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCleanupRequiresQuiescenceBeforeDeletionIntent(t *testing.T) {
	w, r := cleanupFixture(t)
	if err := w.disk.Write("jobs/active.codex.json", []byte(fmt.Sprintf(`{"assignment":{"run_id":%q}}`, r.RunID)), w.disk.Policy.Database.MaxRecord); err != nil {
		t.Fatal(err)
	}
	var blocked *runner.CleanupBlockedError
	if err := w.releaseRun(t.Context(), r.RunID, true); !errors.As(err, &blocked) {
		t.Fatal("unsettled execution did not block cleanup", err)
	}
	saved, err := w.load(r.RunID)
	if err != nil || saved.CleanupIntent != nil {
		t.Fatal("unsettled source acquired deletion intent", err)
	}
	if data, err := os.ReadFile(filepath.Join(r.WorkDir, "nested", "keep.txt")); err != nil || string(data) != "retained keep\n" {
		t.Fatal("cleanup changed active source", err)
	}
}
