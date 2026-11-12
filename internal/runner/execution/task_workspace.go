package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/runner"
)

type taskWorkspaceRecord struct {
	Authority   runner.TaskAssignment `json:"authority"`
	InputSHA    string                `json:"input_sha256"`
	SeedTree    string                `json:"seed_tree"`
	SeedCommit  string                `json:"seed_commit"`
	SeedPatchID string                `json:"seed_patch_id"`
	SeedSHA     string                `json:"seed_sha256"`
	Installed   bool                  `json:"installed,omitempty"`
	PinCommit   string                `json:"pin_commit,omitempty"`
	Pins        map[string]string     `json:"pins,omitempty"`
}

func sameTaskAuthority(a, b runner.TaskAssignment) bool {
	if a.Identity != b.Identity || a.RootPatchID != b.RootPatchID || !reflect.DeepEqual(a.WritePaths, b.WritePaths) || len(a.Dependencies) != len(b.Dependencies) {
		return false
	}
	for i := range a.Dependencies {
		if a.Dependencies[i] != b.Dependencies[i] {
			return false
		}
	}
	return true
}

func taskSeedRef(run, group string) string { return "refs/runner/task-seeds/" + run + "/" + group }
func taskHash(value string) bool           { return regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(value) }
func validateTaskPaths(paths []string) error {
	return runner.ValidateTaskPaths(paths)
}
func taskPathOwned(name string, paths []string) bool {
	for _, scope := range paths {
		if scope == name || strings.HasPrefix(name, scope+"/") {
			return true
		}
	}
	return false
}
func taskGitEnv(index string) []string {
	return []string{"GIT_INDEX_FILE=" + index, "GIT_AUTHOR_NAME=Cortexium Runner", "GIT_AUTHOR_EMAIL=runner@localhost", "GIT_COMMITTER_NAME=Cortexium Runner", "GIT_COMMITTER_EMAIL=runner@localhost", "GIT_AUTHOR_DATE=1970-01-01T00:00:00Z", "GIT_COMMITTER_DATE=1970-01-01T00:00:00Z"}
}
func taskCommit(ctx context.Context, repo string, env []string, tree, message string, parents ...string) (string, error) {
	args := []string{"-c", "commit.gpgSign=false", "commit-tree", tree}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	data, err := git(ctx, repo, env, []byte(message+"\n"), 256, args...)
	return strings.TrimSpace(string(data)), err
}
func taskTree(ctx context.Context, repo, commit string) (string, error) {
	if !taskHash(commit) {
		return "", errors.New("task source requires exact commit identity")
	}
	out, err := git(ctx, repo, nil, nil, 256, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	return strings.TrimSpace(string(out)), err
}
func (w *Workspaces) taskRepository(ctx context.Context, a runner.Assignment) (string, string, error) {
	if a.Source == nil || !taskHash(a.Source.Ref) {
		return "", "", errors.New("task source needs the resolved root commit")
	}
	repo, err := filepath.EvalSymlinks(a.Repository)
	if err != nil {
		return "", "", err
	}
	top, err := git(ctx, repo, nil, nil, 4096, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(string(top)) != repo {
		return "", "", errors.New("task repository ownership differs")
	}
	base, err := git(ctx, repo, nil, nil, 256, "rev-parse", "--verify", "--end-of-options", a.Source.Ref+"^{commit}")
	if err != nil || strings.TrimSpace(string(base)) != a.Source.Ref {
		return "", "", errors.New("task root source commit differs")
	}
	return repo, a.Source.Ref, nil
}
func (w *Workspaces) taskLease(a runner.Assignment) (string, *os.File, error) {
	if err := captureDirectory(w.disk.Root, "work", a.AttemptID, "task-source"); err != nil {
		return "", nil, err
	}
	root := filepath.Join(w.disk.Root, "work", a.AttemptID, "task-source")
	encoded, _ := json.Marshal(a)
	want := captureDigest(encoded)
	owner := "work/" + a.AttemptID + "/task-source/owner"
	if prior, e := w.disk.Read(owner, 128); e == nil {
		if string(prior) != want {
			return "", nil, errors.New("task source scratch owner changed")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", nil, e
	} else {
		entries, e := os.ReadDir(root)
		if e != nil {
			return "", nil, e
		}
		for _, entry := range entries {
			if entry.Name() != "lease" {
				return "", nil, errors.New("task source scratch has unowned data; preserve it")
			}
		}
		if e = w.disk.Write(owner, []byte(want), 128); e != nil {
			return "", nil, e
		}
	}
	file := filepath.Join(root, "lease")
	info, err := os.Lstat(file)
	flags := os.O_RDWR
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("task source lease ownership differs")
	}
	lease, err := os.OpenFile(file, flags, 0600)
	if err != nil {
		return "", nil, err
	}
	if info != nil {
		actual, e := lease.Stat()
		if e != nil || !os.SameFile(info, actual) {
			lease.Close()
			return "", nil, errors.New("task source lease identity changed")
		}
	}
	owned, err := tryCaptureLease(lease)
	if err != nil {
		lease.Close()
		return "", nil, err
	}
	if !owned {
		lease.Close()
		return "", nil, &capturePending{reason: "same owned task source writer is still active"}
	}
	return root, lease, nil
}

func removeTaskIndex(index string) error {
	for _, name := range []string{index, index + ".lock"} {
		info, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("task index ownership changed; preserve it")
		}
		if err = os.Remove(name); err != nil {
			return err
		}
	}
	return nil
}

// Assembly touches only a disposable index and Git objects. The ordinary seed
// preparation owns all checkout mutation and interruption recovery afterwards.
func (w *Workspaces) assembleTaskSource(ctx context.Context, a runner.Assignment) (string, string, []byte, []string, error) {
	if err := w.lockContext(ctx); err != nil {
		return "", "", nil, nil, err
	}
	defer w.mu.Unlock()
	repo, base, err := w.taskRepository(ctx, a)
	if err != nil {
		return "", "", nil, nil, err
	}
	root, lease, err := w.taskLease(a)
	if err != nil {
		return "", "", nil, nil, err
	}
	defer lease.Close()
	ctx = context.WithValue(ctx, captureLeaseKey{}, lease)
	index := filepath.Join(root, "index")
	if err = removeTaskIndex(index); err != nil {
		return "", "", nil, nil, err
	}
	env := taskGitEnv(index)
	head := base
	if id := a.TaskSource.RootPatchID; id != "" {
		if w.read == nil {
			return "", "", nil, nil, errors.New("root task patch reader missing")
		}
		patch, e := w.read(ctx, id)
		if e != nil {
			return "", "", nil, nil, e
		}
		if len(patch) > int(a.Limits.MaxArtifact) {
			return "", "", nil, nil, runner.ErrCapacity
		}
		rootRecord, e := w.load(a.TaskSource.Identity.RootRunID)
		if e != nil {
			return "", "", nil, nil, e
		}
		if rootRecord.Repository != repo || rootRecord.Base != base || rootRecord.Candidate == nil || rootRecord.Candidate.PatchID != id || rootRecord.Capture == nil || rootRecord.Capture.AttemptID != rootRecord.Candidate.AttemptID || rootRecord.Capture.Tree != rootRecord.Candidate.Tree || rootRecord.Capture.PatchSHA == "" || captureDigest(patch) != rootRecord.Capture.PatchSHA {
			return "", "", nil, nil, errors.New("root task patch differs from its retained source capture")
		}
		if _, e = w.validateSeed(ctx, rootRecord, patch, a.Limits.MaxArtifact, false); e != nil {
			return "", "", nil, nil, e
		}
		if _, e = git(ctx, repo, env, nil, 4096, "read-tree", base); e != nil {
			return "", "", nil, nil, e
		}
		if len(patch) > 0 {
			if _, e = git(ctx, repo, env, patch, 4096, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); e != nil {
				return "", "", nil, nil, e
			}
		}
		tree, e := git(ctx, repo, env, nil, 256, "write-tree")
		if e != nil {
			return "", "", nil, nil, e
		}
		head, e = taskCommit(ctx, repo, env, strings.TrimSpace(string(tree)), "Runner task root "+a.TaskSource.Identity.RootRunID+" "+a.TaskSource.Identity.GroupStep+" "+captureDigest(patch), base)
		if e != nil {
			return "", "", nil, nil, e
		}
	}
	dependencies := runner.Clone(a.TaskSource.Dependencies)
	sort.Slice(dependencies, func(i, j int) bool { return dependencies[i].Identity.ItemID < dependencies[j].Identity.ItemID })
	seen := map[string]bool{}
	var conflicts []string
	for _, candidate := range dependencies {
		if seen[candidate.Identity.ItemID] {
			return "", "", nil, nil, errors.New("duplicate task dependency")
		}
		seen[candidate.Identity.ItemID] = true
		if err = w.verifyTaskCandidate(ctx, a, repo, base, candidate); err != nil {
			return "", "", nil, nil, err
		}
		var paths []string
		head, paths, err = w.mergeTaskCommit(ctx, a, repo, root, env, head, candidate.Commit)
		if err != nil {
			return "", "", nil, nil, err
		}
		conflicts = append(conflicts, paths...)
	}
	if len(conflicts) > 0 && a.Setup.Profile.Mode != "join" {
		return "", "", nil, nil, errors.New("dependency source contains text conflicts; preserve task pins for root repair")
	}
	tree, err := taskTree(ctx, repo, head)
	if err != nil {
		return "", "", nil, nil, err
	}
	patch, err := git(ctx, repo, nil, nil, int(a.Limits.MaxArtifact), "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", base, tree, "--")
	if err != nil {
		return "", "", nil, nil, err
	}
	if _, err = w.validateSeed(ctx, workspaceRecord{RunID: a.RunID, Repository: repo, Base: base}, patch, a.Limits.MaxArtifact, false); err != nil {
		return "", "", nil, nil, err
	}
	sort.Strings(conflicts)
	return tree, head, patch, conflicts, nil
}

func (w *Workspaces) verifyTaskCandidate(ctx context.Context, a runner.Assignment, repo, base string, c runner.TaskCandidate) error {
	identity := a.TaskSource.Identity
	if c.Identity.RootRunID != identity.RootRunID || c.Identity.GroupStep != identity.GroupStep || !taskComponent(c.Identity.ItemID) || c.Identity.ItemID == identity.ItemID || !taskComponent(c.RunID) || c.Repository != repo || c.Base != base || !taskHash(c.Tree) || !taskHash(c.Commit) {
		return errors.New("dependency candidate authority differs")
	}
	r, err := w.load(c.RunID)
	if err != nil {
		return err
	}
	t := r.TaskSources[identity.GroupStep]
	if t == nil || t.Authority.Identity != c.Identity || t.PinCommit != c.Commit || r.Candidate == nil || r.Candidate.Tree != c.Tree || r.Candidate.PatchID != c.PatchID || r.Capture == nil || r.Capture.PatchSHA == "" {
		return errors.New("dependency candidate is not its retained internal task pin")
	}
	head, err := git(ctx, repo, nil, nil, 256, "rev-parse", "--verify", taskPinRef(c.RunID))
	if err != nil || strings.TrimSpace(string(head)) != c.Commit {
		return errors.New("dependency task pin changed; preserve it")
	}
	tree, err := taskTree(ctx, repo, c.Commit)
	if err != nil || tree != c.Tree {
		return errors.New("dependency commit tree differs")
	}
	patch, err := w.disk.Read("artifacts/"+c.PatchID, a.Limits.MaxArtifact)
	if err != nil {
		return err
	}
	if captureDigest(patch) != r.Capture.PatchSHA {
		return errors.New("dependency patch changed after capture")
	}
	_, err = w.validateSeed(ctx, r, patch, a.Limits.MaxArtifact, false)
	return err
}

func (w *Workspaces) mergeTaskCommit(ctx context.Context, a runner.Assignment, repo, root string, env []string, ours, theirs string) (string, []string, error) {
	if ours == theirs {
		return ours, nil, nil
	}
	ancestor := func(left, right string) (bool, error) {
		_, err := git(ctx, repo, nil, nil, 256, "merge-base", "--is-ancestor", left, right)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return err == nil, err
	}
	if yes, err := ancestor(theirs, ours); err != nil {
		return "", nil, err
	} else if yes {
		return ours, nil, nil
	}
	if yes, err := ancestor(ours, theirs); err != nil {
		return "", nil, err
	} else if yes {
		return theirs, nil, nil
	}
	bases, err := git(ctx, repo, nil, nil, 256, "merge-base", "--all", ours, theirs)
	if err != nil {
		return "", nil, err
	}
	fields := strings.Fields(string(bases))
	if len(fields) != 1 {
		return "", nil, errors.New("task merge base is ambiguous")
	}
	if _, err = git(ctx, repo, env, nil, 4096, "read-tree", "--reset", ours); err != nil {
		return "", nil, err
	}
	if _, err = git(ctx, repo, env, nil, 4096, "read-tree", "-i", "-m", fields[0], ours, theirs); err != nil {
		return "", nil, err
	}
	unmerged, err := git(ctx, repo, env, nil, 1<<20, "ls-files", "--unmerged", "-z")
	if err != nil {
		return "", nil, err
	}
	type stage struct{ mode, blob string }
	entries := map[string]map[int]stage{}
	for _, entry := range bytes.Split(unmerged, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		pair := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(pair) != 2 || !fs.ValidPath(string(pair[1])) {
			return "", nil, errors.New("task merge index has unsafe entries")
		}
		meta := strings.Fields(string(pair[0]))
		if len(meta) != 3 {
			return "", nil, errors.New("task merge index metadata differs")
		}
		n, e := strconv.Atoi(meta[2])
		if e != nil || n < 1 || n > 3 {
			return "", nil, errors.New("task merge stage differs")
		}
		name := string(pair[1])
		if entries[name] == nil {
			entries[name] = map[int]stage{}
		}
		if entries[name][n].blob != "" {
			return "", nil, errors.New("task merge stage duplicated")
		}
		entries[name][n] = stage{meta[0], meta[1]}
	}
	paths := make([]string, 0, len(entries))
	for name := range entries {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	var conflicts []string
	for _, name := range paths {
		row := entries[name]
		if len(row) != 3 || row[1].mode != row[2].mode || row[1].mode != row[3].mode || (row[1].mode != "100644" && row[1].mode != "100755") {
			return "", nil, fmt.Errorf("task conflict for %s is not a regular text edit", name)
		}
		files := make([]string, 3)
		for i, n := range []int{2, 1, 3} {
			data, e := git(ctx, repo, nil, nil, int(a.Limits.MaxArtifact), "cat-file", "blob", row[n].blob)
			if e != nil {
				return "", nil, e
			}
			if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				return "", nil, fmt.Errorf("task conflict for %s is binary", name)
			}
			files[i] = filepath.Join(root, fmt.Sprintf("merge-%d", i))
			if e = w.disk.Write("work/"+a.AttemptID+"/task-source/"+filepath.Base(files[i]), data, a.Limits.MaxArtifact); e != nil {
				return "", nil, e
			}
		}
		command := gitCommand(ctx, repo, nil, nil, "merge-file", "--stdout", "--diff3", "-L", "current", "-L", "base", "-L", "task", files[0], files[1], files[2])
		output := &boundedBuffer{limit: int(a.Limits.MaxArtifact)}
		command.Stdout = output
		command.Stderr = &diagnosticTail{limit: 1024}
		e := command.Run()
		if output.err != nil {
			return "", nil, output.err
		}
		merged := output.Bytes()
		if e != nil {
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() < 1 || exit.ExitCode() > 127 {
				return "", nil, e
			}
			if !bytes.Contains(merged, []byte("<<<<<<< current\n")) || !bytes.Contains(merged, []byte(">>>>>>> task\n")) {
				return "", nil, errors.New("task text conflict lacks retained markers")
			}
			conflicts = append(conflicts, name)
		}
		blob, e := git(ctx, repo, nil, merged, 256, "hash-object", "-w", "--stdin")
		if e != nil {
			return "", nil, e
		}
		if _, e = git(ctx, repo, env, nil, 4096, "update-index", "--add", "--cacheinfo", row[1].mode+","+strings.TrimSpace(string(blob))+","+name); e != nil {
			return "", nil, e
		}
	}
	tree, err := git(ctx, repo, env, nil, 256, "write-tree")
	if err != nil {
		return "", nil, err
	}
	commit, err := taskCommit(ctx, repo, env, strings.TrimSpace(string(tree)), "Runner task source join "+a.TaskSource.Identity.RootRunID+" "+a.TaskSource.Identity.GroupStep, ours, theirs)
	return commit, conflicts, err
}

// A trusted source operation may install a new seed only after proving the
// prior checkout is completely retained. Prepare owns rebuilding an interrupted
// pre-model seed; no shared or user checkout is reset by this transition.
func (w *Workspaces) prepareTaskSource(ctx context.Context, a runner.Assignment, patchID, tree, commit, inputSHA string) error {
	repo, base, err := w.taskRepository(ctx, a)
	if err != nil {
		return err
	}
	if !taskHash(tree) || !taskHash(commit) {
		return errors.New("task seed lacks exact source objects")
	}
	patch, err := w.disk.Read("artifacts/"+patchID, a.Limits.MaxArtifact)
	if err != nil {
		return err
	}
	seedSHA := captureDigest(patch)
	preparing := taskSourceJob{
		Assignment: a,
		InputSHA:   inputSHA,
		Tree:       tree,
		Commit:     commit,
		PatchID:    patchID,
	}
	_, lease, err := w.taskLease(a)
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx = context.WithValue(ctx, captureLeaseKey{}, lease)
	if err = w.lockContext(ctx); err != nil {
		return err
	}
	r, err := w.load(a.RunID)
	if errors.Is(err, os.ErrNotExist) {
		r = workspaceRecord{RunID: a.RunID, Repository: repo, Base: base, WorkDir: filepath.Join(w.disk.Root, "work", a.RunID, "source")}
		err = nil
	}
	if err == nil && (r.CleanupIntent != nil || r.SourcePreservation != nil && !r.SourcePreservation.Complete) {
		err = errors.New("task seed cannot replace unresolved workspace cleanup or preservation")
	}
	if err == nil {
		settled, settleErr := w.cleanupQuiescentForTaskSeed(r, preparing)
		if settleErr != nil {
			err = settleErr
		} else {
			defer settled()
		}
	}
	// A saved assembled patch is an intent, not permission to trust stale pins.
	// Pending restarts skip assembly, so revalidate every dependency before any
	// seed transition and retain its settled capture lease through installation.
	if err == nil {
		for _, candidate := range a.TaskSource.Dependencies {
			if err = w.verifyTaskCandidate(ctx, a, repo, base, candidate); err != nil {
				break
			}
			var child workspaceRecord
			child, err = w.load(candidate.RunID)
			if err != nil {
				break
			}
			var settled func()
			settled, err = w.cleanupQuiescent(child)
			if err != nil {
				break
			}
			defer settled()
		}
	}
	if err == nil && r.TaskSources == nil {
		r.TaskSources = map[string]*taskWorkspaceRecord{}
	}
	if err == nil {
		key := a.TaskSource.Identity.GroupStep
		if old := r.TaskSources[key]; old != nil {
			if !sameTaskAuthority(old.Authority, *a.TaskSource) || old.InputSHA != inputSHA || old.SeedTree != tree || old.SeedCommit != commit || old.SeedSHA != seedSHA {
				err = errors.New("task seed identity changed; preserve it")
			}
		} else {
			if r.Repository != repo || r.Base != base {
				err = errors.New("task seed repository/base changed")
			}
			if err == nil && r.Candidate != nil && r.Candidate.PatchID != a.TaskSource.RootPatchID {
				err = errors.New("task seed would replace unrelated captured source")
			}
			if err == nil {
				_, e := os.Lstat(r.WorkDir)
				if e != nil && !errors.Is(e, os.ErrNotExist) {
					err = e
				} else if e == nil || r.Candidate != nil || r.SeedPatchID != "" {
					_, _, err = w.retainedCandidate(ctx, r, e == nil)
				}
			}
			if err == nil {
				validation := r
				validation.Candidate = &runner.SourceResult{Tree: tree}
				_, err = w.validateSeed(ctx, validation, patch, a.Limits.MaxArtifact, false)
			}
			if err == nil {
				r.TaskSources[key] = &taskWorkspaceRecord{Authority: runner.Clone(*a.TaskSource), InputSHA: inputSHA, SeedTree: tree, SeedCommit: commit, SeedPatchID: patchID, SeedSHA: seedSHA}
				r.SeedPatchID, r.SeedReady, r.ReadyGeneration = patchID, false, ""
				r.Candidate, r.Capture = nil, nil
				err = w.save(r)
			}
		}
	}
	var prepared runner.Assignment
	resumeCapture := false
	if err == nil {
		prepared = runner.Clone(a)
		if prepared.Inputs == nil {
			prepared.Inputs = map[string]runner.Value{}
		}
		prepared.Inputs["patch"] = runner.Value{ArtifactID: r.TaskSources[a.TaskSource.Identity.GroupStep].SeedPatchID}
		resumeCapture = pendingTaskSeedCapture(r, preparing)
		if resumeCapture {
			err = w.validateTaskSeedPreparation(ctx, prepared, r, r.TaskSources[a.TaskSource.Identity.GroupStep])
		}
	}
	w.mu.Unlock()
	if err != nil {
		return err
	}
	if resumeCapture {
		// The seed is already installed. Rejoin its literal capture through
		// Capture's existing manifest/source/lease checks, without preparing or
		// replacing a checkout underneath that unfinished snapshot.
		return nil
	}
	if _, err = w.prepare(ctx, prepared, runner.Clone(r.TaskSources[a.TaskSource.Identity.GroupStep])); err != nil {
		return err
	}
	if err = w.lockContext(ctx); err != nil {
		return err
	}
	defer w.mu.Unlock()
	r, err = w.load(a.RunID)
	if err != nil {
		return err
	}
	t := r.TaskSources[a.TaskSource.Identity.GroupStep]
	if t == nil || t.SeedCommit != commit {
		return errors.New("task seed ownership changed during preparation")
	}
	ref := taskSeedRef(a.RunID, a.TaskSource.Identity.GroupStep)
	if err = taskUpdateRef(ctx, repo, ref, commit, ""); err != nil {
		return err
	}
	t.Installed = true
	return w.save(r)
}

func pendingTaskSeedCapture(r workspaceRecord, seed taskSourceJob) bool {
	a := seed.Assignment
	if a.TaskSource == nil || r.Capture == nil || r.Capture.AttemptID != a.AttemptID || r.Candidate != nil || r.Released || !r.SeedReady {
		return false
	}
	t := r.TaskSources[a.TaskSource.Identity.GroupStep]
	return t != nil && t.Installed && sameTaskAuthority(t.Authority, *a.TaskSource) && t.InputSHA == seed.InputSHA && t.SeedTree == seed.Tree && t.SeedCommit == seed.Commit
}

// Only the authenticated task-source installer can use this handoff. Ordinary
// restoration must continue to refuse an unready seed. Released remains durable
// until Prepare has applied the exact seed and saved readiness atomically.
func (w *Workspaces) validateTaskSeedPreparation(ctx context.Context, a runner.Assignment, r workspaceRecord, expected *taskWorkspaceRecord) error {
	if a.TaskSource == nil || a.Setup.Profile.Harness != "task-source" || a.Setup.Profile.Access != "workspace-read" || (a.Setup.Profile.Mode != "seed" && a.Setup.Profile.Mode != "join") || !sameTaskAuthority(expected.Authority, *a.TaskSource) {
		return errors.New("task seed installation authority differs")
	}
	actual := r.TaskSources[a.TaskSource.Identity.GroupStep]
	repo, err := filepath.EvalSymlinks(a.Repository)
	if err != nil || r.Repository != repo || a.Source == nil || r.Base != a.Source.Ref || !reflect.DeepEqual(actual, expected) || r.SeedPatchID != expected.SeedPatchID || a.Inputs["patch"].ArtifactID != r.SeedPatchID {
		return errors.New("task seed installation differs from its durable intent")
	}
	if !r.SeedReady && (expected.Installed || r.Candidate != nil || r.ReadyGeneration != "") {
		return errors.New("pending task seed conflicts with later source ownership")
	}
	tree, err := taskTree(ctx, r.Repository, expected.SeedCommit)
	if err != nil || tree != expected.SeedTree || expected.SeedSHA == "" || expected.InputSHA == "" {
		return errors.New("task seed commit/tree or authentication is missing or changed")
	}
	ref := taskSeedRef(a.RunID, a.TaskSource.Identity.GroupStep)
	head, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
	if err == nil && strings.TrimSpace(string(head)) != expected.SeedCommit || err != nil && !missingCandidateRef(ctx, r.Repository, ref) {
		return errors.New("task seed ref changed before installation; preserve it")
	}
	return nil
}

func taskUpdateRef(ctx context.Context, repo, ref, hash, previous string) error {
	current, err := git(ctx, repo, nil, nil, 256, "rev-parse", "--verify", ref)
	if err == nil {
		actual := strings.TrimSpace(string(current))
		if actual == hash {
			return nil
		}
		if previous == "" || actual != previous {
			return errors.New("owned task ref changed; preserve it")
		}
	} else if !missingCandidateRef(ctx, repo, ref) {
		return err
	} else if previous != "" {
		return errors.New("owned task ref disappeared; preserve it")
	}
	if previous == "" {
		previous = strings.Repeat("0", 40)
	}
	_, err = git(ctx, repo, nil, nil, 1024, "update-ref", ref, hash, previous)
	return err
}

func (w *Workspaces) pinTaskSource(ctx context.Context, a runner.Assignment) (*runner.TaskCandidate, []string, error) {
	if err := w.lockContext(ctx); err != nil {
		return nil, nil, err
	}
	defer w.mu.Unlock()
	r, err := w.load(a.RunID)
	if err != nil {
		return nil, nil, err
	}
	t := r.TaskSources[a.TaskSource.Identity.GroupStep]
	if t == nil || !t.Installed || !sameTaskAuthority(t.Authority, *a.TaskSource) || r.Candidate == nil || r.Candidate.PatchID != a.Inputs["patch"].ArtifactID {
		return nil, nil, errors.New("task pin source or scope differs from its immutable seed")
	}
	if _, _, err = w.retainedCandidate(ctx, r, true); err != nil {
		return nil, nil, err
	}
	root, lease, err := w.taskLease(a)
	if err != nil {
		return nil, nil, err
	}
	defer lease.Close()
	ctx = context.WithValue(ctx, captureLeaseKey{}, lease)
	index := filepath.Join(root, "index")
	if err = removeTaskIndex(index); err != nil {
		return nil, nil, err
	}
	indexEnv := taskGitEnv(index)
	patch, err := w.disk.Read("artifacts/"+r.Candidate.PatchID, a.Limits.MaxArtifact)
	if err != nil {
		return nil, nil, err
	}
	if r.Capture == nil || r.Capture.PatchSHA == "" || captureDigest(patch) != r.Capture.PatchSHA {
		return nil, nil, errors.New("task pin literal patch changed")
	}
	if _, err = git(ctx, r.Repository, indexEnv, nil, 4096, "read-tree", r.Base); err != nil {
		return nil, nil, err
	}
	if len(patch) > 0 {
		if _, err = git(ctx, r.Repository, indexEnv, patch, 4096, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return nil, nil, err
		}
	}
	rebuilt, err := git(ctx, r.Repository, indexEnv, nil, 256, "write-tree")
	if err != nil || strings.TrimSpace(string(rebuilt)) != r.Candidate.Tree {
		return nil, nil, errors.New("task pin patch does not reconstruct captured tree")
	}
	changed, err := git(ctx, r.Repository, nil, nil, 1<<20, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", t.SeedTree, r.Candidate.Tree, "--")
	if err != nil {
		return nil, nil, err
	}
	var violations []string
	for _, name := range bytes.Split(changed, []byte{0}) {
		if len(name) > 0 && !taskPathOwned(string(name), t.Authority.WritePaths) {
			violations = append(violations, string(name))
		}
	}
	if len(violations) > 0 {
		return nil, violations, nil
	}
	env := taskGitEnv("")
	env = env[1:]
	stamp := a.Deadline
	if stamp.IsZero() {
		stamp = time.Unix(0, 0)
	}
	for i, value := range env {
		if strings.HasPrefix(value, "GIT_AUTHOR_DATE=") {
			env[i] = "GIT_AUTHOR_DATE=" + stamp.UTC().Format(time.RFC3339)
		}
		if strings.HasPrefix(value, "GIT_COMMITTER_DATE=") {
			env[i] = "GIT_COMMITTER_DATE=" + stamp.UTC().Format(time.RFC3339)
		}
	}
	hash, err := taskCommit(ctx, r.Repository, env, r.Candidate.Tree, "Runner internal unreviewed task "+a.RunID, t.SeedCommit)
	if err != nil {
		return nil, nil, err
	}
	if t.Pins == nil {
		t.Pins = map[string]string{}
	}
	if old := t.Pins[a.AttemptID]; old != "" && old != hash {
		return nil, nil, errors.New("task pin intent changed")
	}
	t.Pins[a.AttemptID] = hash
	if err = w.save(r); err != nil {
		return nil, nil, err
	}
	if err = taskUpdateRef(ctx, r.Repository, taskPinHistory(a.RunID, a.AttemptID), hash, ""); err != nil {
		return nil, nil, err
	}
	if err = taskUpdateRef(ctx, r.Repository, taskPinRef(a.RunID), hash, t.PinCommit); err != nil {
		return nil, nil, err
	}
	t.PinCommit = hash
	if err = w.save(r); err != nil {
		return nil, nil, err
	}
	return &runner.TaskCandidate{Identity: a.TaskSource.Identity, RunID: a.RunID, Repository: r.Repository, Base: r.Base, Commit: hash, Tree: r.Candidate.Tree, PatchID: r.Candidate.PatchID}, nil, nil
}

func (w *Workspaces) forgetTaskPins(ctx context.Context, id string) error {
	if err := w.lockContext(ctx); err != nil {
		return err
	}
	defer w.mu.Unlock()
	r, err := w.load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !r.Released {
		return errors.New("task source resources must settle before forgetting pins")
	}
	remove := func(ref, hash string) error {
		current, e := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
		if e != nil {
			if missingCandidateRef(ctx, r.Repository, ref) {
				return nil
			}
			return e
		}
		if strings.TrimSpace(string(current)) != hash {
			return errors.New("task source pin changed; preserve it")
		}
		_, e = git(ctx, r.Repository, nil, nil, 1024, "update-ref", "-d", ref, hash)
		return e
	}
	for group, t := range r.TaskSources {
		if err = remove(taskSeedRef(id, group), t.SeedCommit); err != nil {
			return err
		}
		for attempt, hash := range t.Pins {
			if err = remove(taskPinHistory(id, attempt), hash); err != nil {
				return err
			}
		}
		if t.PinCommit != "" {
			if err = remove(taskPinRef(id), t.PinCommit); err != nil {
				return err
			}
		}
	}
	return nil
}
