//go:build !windows

package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestConstructRebaseCandidateNormalizesContainedBaseMergeHistory(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		name := "clean"
		if dirty {
			name = "dirty"
		}
		t.Run(name, func(t *testing.T) {
			metadata := prepareCandidateWithMergeHistory(t)
			head := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
			wantTree := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD^{tree}"))
			if dirty {
				if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "README.md"), []byte("unstaged candidate bytes\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(metadata.WorktreePath, "feature.txt")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "new-script"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, metadata.WorktreePath, "add", "--all")
				wantTree = strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "write-tree"))
				runGitTest(t, metadata.WorktreePath, "reset", "--mixed", "HEAD")
			}

			provider := NewGitProvider(subprocess.OSRunner{})
			candidate, err := provider.ConstructCandidateForMergeMethod(t.Context(), metadata, "Rebase-compatible candidate", config.MergeMethodRebase)
			if err != nil {
				t.Fatal(err)
			}
			if candidate.CommitOID == head || candidate.TreeOID != wantTree {
				t.Fatalf("normalization did not replace history while retaining the exact staged tree: candidate=%#v old head=%s want tree=%s", candidate, head, wantTree)
			}
			parents := strings.Fields(runGitTest(t, metadata.WorktreePath, "rev-list", "--parents", "-n", "1", candidate.CommitOID))
			if len(parents) != 2 || parents[1] != metadata.BaseRevision {
				t.Fatalf("normalized parents = %v, want sole authenticated parent %s", parents, metadata.BaseRevision)
			}
			if got := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-list", "--count", metadata.BaseRevision+"..HEAD")); got != "1" {
				t.Fatalf("normalized candidate has %s commits beyond the authenticated base, want one", got)
			}
			if got := runGitTest(t, metadata.WorktreePath, "status", "--porcelain", "--untracked-files=all"); got != "" {
				t.Fatalf("normalized candidate is dirty: %q", got)
			}
			if got := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "symbolic-ref", "--short", "HEAD")); got != metadata.BranchName {
				t.Fatalf("normalization changed the task branch: %q", got)
			}
			again, err := provider.ConstructCandidateForMergeMethod(t.Context(), metadata, "Another message must not rewrite the clean candidate", config.MergeMethodRebase)
			if err != nil || again != candidate {
				t.Fatalf("normalized candidate is not idempotent: first=%#v second=%#v error=%v", candidate, again, err)
			}
			if needs, err := provider.CandidateNeedsRebaseNormalization(t.Context(), metadata, candidate); err != nil || needs {
				t.Fatalf("normalized candidate still requires history normalization: needs=%t error=%v", needs, err)
			}
		})
	}
}

func TestConstructCandidatePreservesNonRebaseMergeHistory(t *testing.T) {
	for _, method := range []string{config.MergeMethodMerge, config.MergeMethodSquash} {
		t.Run(method, func(t *testing.T) {
			metadata := prepareCandidateWithMergeHistory(t)
			head := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
			tree := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD^{tree}"))
			candidate, err := NewGitProvider(subprocess.OSRunner{}).ConstructCandidateForMergeMethod(t.Context(), metadata, "Keep supported history", method)
			if err != nil || candidate != (Candidate{CommitOID: head, TreeOID: tree}) {
				t.Fatalf("%s changed clean merge history: candidate=%#v want head=%s tree=%s error=%v", method, candidate, head, tree, err)
			}
		})
	}
}

func TestConstructRebaseCandidatePreservesLinearHistoryAfterMergedBase(t *testing.T) {
	repo := initGitRepo(t)
	runGitTest(t, repo, "checkout", "-b", "side")
	if err := os.WriteFile(filepath.Join(repo, "side.txt"), []byte("already merged into base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "Base side branch")
	side := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	runGitTest(t, repo, "checkout", "-")
	runGitTest(t, repo, "merge", "--no-ff", "--no-edit", side)
	provider := NewGitProvider(subprocess.OSRunner{})
	metadata, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "linear_rebase", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(metadata.WorktreePath, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, metadata.WorktreePath, "add", "--all")
		runGitTest(t, metadata.WorktreePath, "commit", "-m", name)
	}
	head := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD^{tree}"))
	if needs, err := provider.CandidateNeedsRebaseNormalization(t.Context(), metadata, Candidate{CommitOID: head, TreeOID: tree}); err != nil || needs {
		t.Fatalf("merge before authenticated base incorrectly requires normalization: needs=%t error=%v", needs, err)
	}
	candidate, err := provider.ConstructCandidateForMergeMethod(t.Context(), metadata, "Keep linear candidate", config.MergeMethodRebase)
	if err != nil || candidate != (Candidate{CommitOID: head, TreeOID: tree}) {
		t.Fatalf("merge before the authenticated base caused a linear candidate rewrite: candidate=%#v want head=%s tree=%s error=%v", candidate, head, tree, err)
	}
}

func TestConstructRebaseCandidateCompletesAuthenticatedBaseMergeWithSingleParent(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	metadata, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: "pending_rebase_merge", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, metadata.WorktreePath, "add", "--all")
	runGitTest(t, metadata.WorktreePath, "commit", "-m", "Candidate")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("new authenticated base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "Advance base")
	base := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	runGitTest(t, metadata.WorktreePath, "merge", "--no-commit", "--no-ff", base)
	wantTree := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "write-tree"))
	replacement := metadata.Identity
	replacement.BaseRevision = base
	if err := replaceIdentity(activeIdentityPath(root, "pending_rebase_merge"), metadata.Identity, replacement); err != nil {
		t.Fatal(err)
	}
	metadata, err = bindGitAdministration(metadataFor(metadata.RepoRoot, metadata.SourceSnapshot, replacement))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := provider.ConstructCandidateForMergeMethod(t.Context(), metadata, "Complete authenticated merge for rebase", config.MergeMethodRebase)
	if err != nil {
		t.Fatal(err)
	}
	parents := strings.Fields(runGitTest(t, metadata.WorktreePath, "rev-list", "--parents", "-n", "1", candidate.CommitOID))
	if candidate.TreeOID != wantTree || len(parents) != 2 || parents[1] != base {
		t.Fatalf("pending merge lost its exact tree or retained a merge parent: candidate=%#v want tree=%s parents=%v want parent=%s", candidate, wantTree, parents, base)
	}
	if _, err := os.Stat(filepath.Join(metadata.gitDirectory, "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("candidate retained merge state: %v", err)
	}
	if got := runGitTest(t, metadata.WorktreePath, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("completed merge candidate is dirty: %q", got)
	}
}

func TestCandidateNeedsRebaseNormalizationIsReadOnly(t *testing.T) {
	metadata := prepareCandidateWithMergeHistory(t)
	indexPath := filepath.Join(metadata.gitDirectory, "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := runGitTest(t, metadata.WorktreePath, "show-ref")
	objectsBefore := runGitTest(t, metadata.WorktreePath, "count-objects", "-v")
	before, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, metadata.WorktreePath, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runner := &normalizationInspectionRunner{}
	needs, err := NewGitProvider(runner).CandidateNeedsRebaseNormalization(t.Context(), metadata, Candidate{CommitOID: before.Head, TreeOID: before.Tree})
	if err != nil || !needs || runner.mutations != 0 {
		t.Fatalf("exact merged candidate inspection was not read-only: needs=%t mutations=%d error=%v", needs, runner.mutations, err)
	}
	after, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, metadata.WorktreePath, 30*time.Second)
	if err != nil || after.Fingerprint != before.Fingerprint {
		t.Fatalf("inspection changed candidate content or Git control state: before=%s after=%s error=%v", before.Fingerprint, after.Fingerprint, err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(indexAfter, indexBefore) {
		t.Fatalf("inspection changed raw index bytes: error=%v", err)
	}
	if refsAfter := runGitTest(t, metadata.WorktreePath, "show-ref"); refsAfter != refsBefore {
		t.Fatalf("inspection changed repository refs: before=%q after=%q", refsBefore, refsAfter)
	}
	if objectsAfter := runGitTest(t, metadata.WorktreePath, "count-objects", "-v"); objectsAfter != objectsBefore {
		t.Fatalf("inspection changed repository objects: before=%q after=%q", objectsBefore, objectsAfter)
	}
}

func TestCandidateNeedsRebaseNormalizationRejectsStaleOrDirtyCandidate(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *Metadata, *Candidate)
	}{
		{"stale head", func(t *testing.T, metadata *Metadata, candidate *Candidate) {
			candidate.CommitOID = metadata.BaseRevision
		}},
		{"stale tree", func(t *testing.T, metadata *Metadata, candidate *Candidate) {
			candidate.TreeOID = metadata.BaseRevision
		}},
		{"stale base binding", func(t *testing.T, metadata *Metadata, candidate *Candidate) {
			metadata.BaseRevision = candidate.CommitOID
			metadata.Identity.BaseRevision = candidate.CommitOID
		}},
		{"dirty worktree", func(t *testing.T, metadata *Metadata, candidate *Candidate) {
			if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "README.md"), []byte("uncommitted\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"hidden index state", func(t *testing.T, metadata *Metadata, candidate *Candidate) {
			runGitTest(t, metadata.WorktreePath, "update-index", "--skip-worktree", "README.md")
		}},
		{"different branch", func(t *testing.T, metadata *Metadata, candidate *Candidate) {
			runGitTest(t, metadata.WorktreePath, "checkout", "-b", "different-task")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := prepareCandidateWithMergeHistory(t)
			candidate := Candidate{
				CommitOID: strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD")),
				TreeOID:   strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD^{tree}")),
			}
			test.change(t, &metadata, &candidate)
			runner := &normalizationInspectionRunner{}
			if needs, err := NewGitProvider(runner).CandidateNeedsRebaseNormalization(t.Context(), metadata, candidate); err == nil || needs || runner.mutations != 0 {
				t.Fatalf("invalid inspection input accepted or mutated: needs=%t mutations=%d error=%v", needs, runner.mutations, err)
			}
		})
	}
}

type normalizationInspectionRunner struct {
	mutations int
}

func (r *normalizationInspectionRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && (slices.Contains(args, "update-index") || slices.Contains(args, "write-tree") || slices.Contains(args, "commit-tree") ||
		slices.Contains(args, "update-ref") || slices.Contains(args, "reset") || slices.Contains(args, "hash-object") && slices.Contains(args, "-w")) {
		r.mutations++
		return subprocess.Result{ExitCode: 1}, errors.New("inspection attempted Git mutation")
	}
	return (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
}

func prepareCandidateWithMergeHistory(t *testing.T) Metadata {
	t.Helper()
	repo := initGitRepo(t)
	metadata, err := NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "merged_history", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("candidate feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, metadata.WorktreePath, "add", "--all")
	runGitTest(t, metadata.WorktreePath, "commit", "-m", "Candidate feature")
	if err := os.WriteFile(filepath.Join(repo, "side.txt"), []byte("parallel work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "Parallel work")
	side := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	runGitTest(t, metadata.WorktreePath, "merge", "--no-ff", "--no-edit", side)
	// A linear HEAD above the merge prevents a HEAD-only parent check from
	// overlooking the non-linear history GitHub must rebase.
	runGitTest(t, metadata.WorktreePath, "commit", "--allow-empty", "-m", "After merge")
	runGitTest(t, metadata.WorktreePath, "merge-base", "--is-ancestor", metadata.BaseRevision, "HEAD")
	if got := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-list", "--count", "--merges", metadata.BaseRevision+"..HEAD")); got != "1" {
		t.Fatalf("fixture has %s merge commits beyond the authenticated base, want one", got)
	}
	return metadata
}
