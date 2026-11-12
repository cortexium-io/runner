package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestGuidesExactAssetsWholeSourceFlow(t *testing.T) {
	if os.Getenv("RUNNER_GUIDE_MEDIA_SOURCE") != "1" {
		t.Skip("exact approved raw assets in disposable synthetic source only")
	}
	f := newTaskSourceFixture(t, false)
	helper := strings.Repeat("a", 64)
	work := t.TempDir()
	attempt := "attempt_media_import"
	output := filepath.Join(work, ".runner-tmp", attempt, "media")
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	x, err := guidesmedia.Import(t.Context(), output, "run_media_import", attempt, helper)
	if err != nil {
		t.Fatal(err)
	}
	p := runner.Profile{
		Harness:            "command",
		Mode:               "guides-import",
		Access:             "workspace-read",
		GuidesImportSHA256: helper,
		Command: &runner.Command{Args: []string{
			"/owned/runner-guides-import",
			"--output",
			"{scratch}/media",
			"--run",
			"{run_id}",
			"--attempt",
			"{attempt_id}",
		}},
	}
	a := sourceAssignment(f.disk, f.repo, attempt)
	a.RunID = "run_media_import"
	a.ProjectID = "jacks-break"
	a.Setup.Profile = p
	a.Inputs = map[string]runner.Value{"request": runner.Text(guidesmedia.RequestID)}
	a.OutputTypes = guidesmedia.OutputContract()
	job := commandJob{Assignment: a, Workspace: &workspaceRecord{WorkDir: work}}
	end, err := collectGuidesImport(f.disk, job, commandResult{Kind: "returned"})
	if err != nil || end.Result.Outcome != "ready" || len(end.Result.Outputs) != 38 {
		t.Fatal(end, err)
	}
	again, err := collectGuidesImport(f.disk, job, commandResult{Kind: "returned"})
	if err != nil || !bytes.Equal(again.Result.Outputs["check"].Inline, end.Result.Outputs["check"].Inline) {
		t.Fatal("lost acknowledgement collection", err)
	}
	data, err := f.disk.Read("artifacts/"+attempt+"/media-index.json", 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := x.Bind(data)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"frontend/src/components/guides/LearningWalkthrough.vue", "docs/printed-learning.md"}
	candidates := []runner.TaskCandidate{}
	for i, item := range []string{"player", "transcript"} {
		seed := f.assignment(item, "run_"+item, "attempt_"+item+"_seed", "seed", paths[i:i+1], candidates...)
		seed.GuidesMedia = &b
		deps, _ := json.Marshal(seed.TaskSource.Dependencies)
		seed.Inputs["dependencies"] = runner.Value{Inline: deps}
		o, e := f.service.Start(t.Context(), seed)
		taskReturned(t, o, e, "ready")
		source := f.edit(t, seed, "one", map[string]string{paths[i]: "synthetic media-only source\n"})
		if source.MediaIndexID != b.IndexID || source.Base != f.base {
			t.Fatal("source authority lost", source)
		}
		patch, e := f.disk.Read("artifacts/"+source.PatchID, f.disk.Policy.MaxArtifact)
		if e != nil {
			t.Fatal(e)
		}
		if len(patch) > 16<<20 || bytes.Contains(patch, []byte("GIT binary patch")) {
			t.Fatal("raw media embedded in code envelope")
		}
		candidates = append(candidates, *f.pin(t, seed, source, "one", "ready"))
	}
	join := f.joinAssignment(t, candidates)
	join.GuidesMedia = &b
	manifestID := join.Inputs["results"].ArtifactID
	md, _ := f.disk.Read("artifacts/"+manifestID, f.disk.Policy.MaxArtifact)
	var m runner.TaskManifest
	json.Unmarshal(md, &m)
	m.MediaIndexID = b.IndexID
	m.MediaIndexSHA256 = b.IndexSHA256
	md, _ = json.Marshal(m)
	f.disk.Write("artifacts/"+manifestID, md, f.disk.Policy.MaxArtifact)
	o, err := f.service.Start(t.Context(), join)
	taskReturned(t, o, err, "ready")
	root, err := f.service.workspaces.load(join.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Candidate.MediaIndexID != b.IndexID || root.Candidate.Base != f.base {
		t.Fatal("join authority lost")
	}
	if err := f.service.workspaces.validateMediaTree(t.Context(), root, nil, root.Candidate.Tree); err != nil {
		t.Fatal(err)
	}
	approved := sourceAssignment(f.disk, f.repo, "attempt_media_reviewed")
	approved.RunID = join.RunID
	approved.GuidesMedia = &b
	approved.Deadline = time.Now().Add(time.Minute)
	approved.Setup.Profile = runner.Profile{Harness: "git-candidate", Access: "workspace-read"}
	approved.Inputs = map[string]runner.Value{
		"patch":   {ArtifactID: o.Source.PatchID},
		"verdict": runner.Text("APPROVE"),
		"review":  runner.Text("synthetic independent gate result; no product proof"),
	}
	if _, err := f.service.workspaces.retainCandidate(t.Context(), approved); err != nil {
		t.Fatal(err)
	}
	parent := strings.TrimSpace(string(fixtureGit(t, f.repo, "rev-parse", candidateRef(join.RunID)+"^")))
	if parent != f.base {
		t.Fatal("original base replaced")
	}
	f.service = NewService(f.disk, func(ctx context.Context, id string) ([]byte, error) {
		return f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
	}, nil)
	repeat, err := f.service.Start(t.Context(), join)
	taskReturned(t, repeat, err, "ready")
	if *repeat.Source != *o.Source {
		t.Fatal("restart changed join")
	}

	// Metadata, scope and immutable source authority are consumer-owned.
	foreign := approved
	foreign.GuidesMedia = nil
	if _, err := f.service.workspaces.retainCandidate(t.Context(), foreign); err == nil {
		t.Fatal("candidate accepted missing whole-set authority")
	}
	wrong := runner.Clone(join)
	wrong.GuidesMedia.IndexSHA256 = strings.Repeat("b", 64)
	if _, err := f.service.workspaces.Prepare(t.Context(), wrong); err == nil {
		t.Fatal("foreign binding replaced ready source")
	}
	ctx, stop := context.WithCancel(t.Context())
	stop()
	if err := f.service.workspaces.mediaCheckout(ctx, root, false); err == nil {
		t.Fatal("media validation ignored cancellation")
	}
	alternate := filepath.Join(t.TempDir(), "index")
	env := taskGitEnv(alternate)
	if _, err := git(t.Context(), f.repo, env, nil, 4096, "read-tree", root.Candidate.Tree); err != nil {
		t.Fatal(err)
	}
	assetPath := x.Assets[0].Path
	if _, err := git(t.Context(), f.repo, env, nil, 4096, "update-index", "--force-remove", "--", assetPath); err != nil {
		t.Fatal(err)
	}
	missingTree := strings.TrimSpace(string(fixtureGitEnv(t, f.repo, env, "write-tree")))
	if err := f.service.workspaces.validateMediaTree(t.Context(), root, env, missingTree); err == nil {
		t.Fatal("full tree missing raw asset accepted")
	}
	if _, err := git(t.Context(), f.repo, env, nil, 4096, "read-tree", root.Candidate.Tree); err != nil {
		t.Fatal(err)
	}
	object := strings.TrimSpace(string(fixtureGitEnv(t, f.repo, env, "rev-parse", root.Candidate.Tree+":"+assetPath)))
	if _, err := git(t.Context(), f.repo, env, nil, 4096, "update-index", "--add", "--cacheinfo", "100644,"+object+",frontend/src/engines/nonogram.ts"); err != nil {
		t.Fatal(err)
	}
	expanded := strings.TrimSpace(string(fixtureGitEnv(t, f.repo, env, "write-tree")))
	if err := f.service.workspaces.validateMediaTree(t.Context(), root, env, expanded); err == nil {
		t.Fatal("puzzle engine scope expansion accepted")
	}
	badAssignment := f.assignment("bad", "run_bad", "attempt_bad_seed", "seed", []string{"frontend/src"})
	badAssignment.GuidesMedia = &b
	badAssignment.Inputs["dependencies"] = runner.Value{Inline: json.RawMessage("[]")}
	rejected, e := f.service.Start(t.Context(), badAssignment)
	if e != nil || rejected.End == nil || rejected.End.Kind != "failed" {
		t.Fatal("broad write scope admitted", rejected, e)
	}
	// Collector replay does not overwrite changed existing artifacts or accept
	// an unlisted output. These are disposable fixture bytes, not source originals.
	scratchExtra := filepath.Join(output, "extra")
	os.WriteFile(scratchExtra, []byte("unlisted"), 0600)
	if _, err := collectGuidesImport(f.disk, job, commandResult{Kind: "returned"}); err == nil {
		t.Fatal("unlisted importer output accepted")
	}
	os.Remove(scratchExtra)
	changedJob := runner.Clone(job)
	changedJob.Assignment.RunID = "foreign"
	if _, err := collectGuidesImport(f.disk, changedJob, commandResult{Kind: "returned"}); err == nil {
		t.Fatal("foreign importer run accepted")
	}
	if _, err := collectGuidesImport(f.disk, job, commandResult{Kind: "failed"}); err == nil {
		t.Fatal("failed importer accepted")
	}
	// Drift is refused without repairing or replacing retained immutable bytes.
	asset := x.Assets[0]
	raw, _ := f.disk.Read("artifacts/"+asset.ArtifactID, f.disk.Policy.MaxArtifact)
	bad := append([]byte{}, raw...)
	bad[0] ^= 1
	f.disk.Write("artifacts/"+asset.ArtifactID, bad, f.disk.Policy.MaxArtifact)
	if _, err := f.service.workspaces.mediaBlob(t.Context(), asset); err == nil {
		t.Fatal("changed raw accepted")
	}
	f.disk.Write("artifacts/"+asset.ArtifactID, raw, f.disk.Policy.MaxArtifact)
	if err := f.service.workspaces.mediaCheckout(t.Context(), root, false); err != nil {
		t.Fatal(err)
	}
	before := string(fixtureGit(t, f.repo, "status", "--porcelain"))
	if before != "" {
		t.Fatal("fixture original working source mutated", before)
	}
	t.Logf("synthetic exact36 blobs=%d; original base=%s; full tree=%s; joined patch=%s; all pins and original parent preserved; no product checks", guidesmedia.AssetBytes, f.base, root.Candidate.Tree, root.Candidate.PatchID)
}

func fixtureGitEnv(t *testing.T, repo string, env []string, args ...string) []byte {
	t.Helper()
	data, err := git(t.Context(), repo, env, nil, 4096, args...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
