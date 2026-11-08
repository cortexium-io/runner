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

	"github.com/cortexium-io/runner/internal/runner"
)

func TestReadOnlyReviewStagesOnlyTheSamePreparedOwnedSeed(t *testing.T) {
	for _, change := range []string{"ready", "tracked drift", "untracked drift", "unprepared", "wrong seed", "generation", "foreign project", "foreign repository", "foreign base"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			patch := addedSeedFile("retained.txt", "100644")
			w, a := seedWorkspaceFixture(t, patch)
			if err := w.disk.Write("artifacts/"+a.Inputs["patch"].ArtifactID, patch, a.Limits.MaxArtifact); err != nil {
				t.Fatal(err)
			}
			a.Setup.Profile = runner.Profile{Harness: "command", Access: "workspace-read"}
			r, err := w.Prepare(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			a.AttemptID, a.ProjectID, a.Source.Ref = "review_seed", "demo", r.Base
			a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-read", Model: "gpt-6.1-sol", Reasoning: "xhigh", MaxInput: 8192, MaxOutput: 512}
			a.Task, a.OutputTypes = "Independently review the retained seed", map[string]string{"verdict": "text", "summary": "text"}
			a.Deadline = time.Now().Add(20 * time.Minute)
			a = runner.Clone(a)
			snapshot := runner.Clone(r)
			j := nativeJob{Assignment: a, Workspace: &snapshot, Stage: "allocated"}
			c := &Codex{disk: w.disk, workspaces: w, ctx: context.Background(), jobs: map[string]nativeJob{}, changed: make(chan struct{})}
			if err = c.saveLocked(j); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "tracked drift", "untracked drift":
				name := "retained.txt"
				if change == "untracked drift" {
					name = "unexpected.txt"
				}
				err = os.WriteFile(filepath.Join(r.WorkDir, name), []byte("unique source\n"), 0600)
			case "unprepared":
				r.SeedReady = false
				err = w.save(r)
			case "wrong seed":
				r.SeedPatchID = "other/changes.patch"
				err = w.save(r)
			case "generation":
				r.ReadyGeneration = "another-generation"
				err = w.save(r)
			case "foreign project":
				j.Assignment.ProjectID = "foreign"
			case "foreign repository":
				j.Workspace = &workspaceRecord{}
				*j.Workspace = r
				j.Workspace.Repository = t.TempDir()
			case "foreign base":
				j.Workspace = &workspaceRecord{}
				*j.Workspace = r
				j.Workspace.Base = strings.Repeat("b", 40)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = c.workspaceInputs(j)
			if change != "ready" {
				if err == nil {
					t.Fatal("unowned, stale or changed source reached read-only review")
				}
				return
			}
			if err != nil {
				t.Fatal("verified prepared seed could not be reviewed", err)
			}
			got, err := os.ReadFile(filepath.Join(r.WorkDir, ".runner-tmp", "inputs", a.AttemptID, "patch"))
			if err != nil || !bytes.Equal(got, patch) {
				t.Fatal("review did not retain the supplied literal patch", err)
			}
			current, err := w.load(a.RunID)
			if err != nil || current.Candidate != nil || current.Capture != nil {
				t.Fatal("read-only seed review created a candidate or capture", err)
			}
		})
	}
}

func TestOverflowInlineInputsRejoinAndCleanWithExistingOwnedFiles(t *testing.T) {
	d := workspaceDisk(t)
	// This fixture uses the pilot's full retained-owner instructions. Its saved
	// assignment uses the pilot's existing record budget; the native input
	// budget and overflow/rejoin behavior below remain independently bounded.
	d.Policy.Database.MaxRecord = 40 << 10
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, sourceRepository(t), "inline_owner")
	template := retainedInputAssignment(t)
	a.Setup, a.Task, a.OutputTypes, a.Outcomes, a.Deadline = template.Setup, template.Task, template.OutputTypes, template.Outcomes, template.Deadline
	a.Inputs = map[string]runner.Value{
		"request":       runner.Text(strings.Repeat("bounded request ", 160)),
		"qualification": {Inline: json.RawMessage(` { "note": "` + strings.Repeat("<", 1500) + `", "count": 2 } `)},
	}
	a = runner.Clone(a)
	before, _ := json.Marshal(a)
	workspace, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	j := nativeJob{Assignment: a, Workspace: &workspace}
	c := &Codex{disk: d}
	if err = c.workspaceInputs(j); err != nil {
		t.Fatal(err)
	}
	files, err := stagedInputData(d, a)
	if err != nil || len(files) == 0 {
		t.Fatal("inline evidence was not staged", err)
	}
	// A fresh adapter reconstructs the same files from the saved assignment,
	// without a per-process plan, clock dependency or rewriting complete bytes.
	j.Stage = "finished"
	j.End = &runner.ExecutionEnd{Kind: "failed", Reason: "settled model-free staging fixture"}
	saved, err := json.Marshal(j)
	if err != nil || json.Unmarshal(saved, &j) != nil {
		t.Fatal("saved input job cannot be reopened", err)
	}
	if err = d.Write(nativeName(a.AttemptID), saved, a.Limits.MaxRecord); err != nil {
		t.Fatal(err)
	}
	c = NewCodex(d, &runner.CodexSettings{StorageLimit: 32 << 20})
	defer func() { _ = c.Close(context.Background()) }()
	observation, err := c.Observe(t.Context(), a.AttemptID)
	if err != nil || observation.State != runner.Finished || observation.End == nil || observation.End.Kind != "failed" || observation.NativeHandle != "" {
		t.Fatal("observation replaced or dispatched a settled saved input job", observation, err)
	}
	if err = c.workspaceInputs(j); err != nil {
		t.Fatal("restart failed to rejoin typed input files", err)
	}
	dir := filepath.Join(workspace.WorkDir, ".runner-tmp", "inputs", a.AttemptID)
	var selected string
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) || !bytes.Equal(got, a.Inputs[name].Inline) {
			t.Fatal("inline JSON lost its original bytes/type", name, err)
		}
		selected = name
	}
	other := filepath.Join(workspace.WorkDir, ".runner-tmp", "inputs", "other-attempt")
	if err = os.Mkdir(other, 0700); err == nil {
		err = os.WriteFile(filepath.Join(other, "keep"), []byte("retained"), 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, selected), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if c.workspaceInputs(j) == nil || w.ReleaseAttempt(t.Context(), a) == nil {
		t.Fatal("changed inline evidence was overwritten or silently removed")
	}
	if err = os.WriteFile(filepath.Join(dir, selected), files[selected], 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.ReleaseAttempt(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("owned inline input files survived release", err)
	}
	if _, err = os.Stat(filepath.Join(other, "keep")); err != nil {
		t.Fatal("release touched another attempt", err)
	}
	after, _ := json.Marshal(a)
	if !bytes.Equal(before, after) {
		t.Fatal("staging changed the persisted assignment")
	}
}

func TestWritableEvidenceInputsAreExplicitStagedCopiesAndCleanOnlyTheirAttempt(t *testing.T) {
	d := workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, sourceRepository(t), "evidence_owner")
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write", Model: "gpt-6.1-sol", Reasoning: "xhigh", MaxInput: 8192, MaxOutput: 512}
	a.Task, a.OutputTypes = "Use supplied evidence", map[string]string{"summary": "text", "patch": "artifact"}
	a.Inputs = map[string]runner.Value{"baseline": {ArtifactID: "baseline/report"}}
	data := []byte(`{"samples":[{"seed":1},{"seed":131},{"seed":3489660928}]}`)
	if err := d.Write("artifacts/baseline/report", data, a.Limits.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	workspace, err := w.Prepare(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	c := &Codex{disk: d}
	j := nativeJob{Assignment: a, Workspace: &workspace}
	for range 2 {
		if err = c.workspaceInputs(j); err != nil {
			t.Fatal("idempotent staging failed", err)
		}
	}
	payload, _, err := nativePayload(a)
	if err != nil || !strings.Contains(payload, ".runner-tmp/inputs/evidence_owner/baseline") || strings.Contains(payload, "baseline/report") {
		t.Fatal("worker must receive only its staged input path", payload, err)
	}
	file := filepath.Join(workspace.WorkDir, ".runner-tmp", "inputs", a.AttemptID, "baseline")
	got, err := os.ReadFile(file)
	if err != nil || string(got) != string(data) {
		t.Fatal("complete retained evidence was not staged", string(got), err)
	}
	other := filepath.Join(workspace.WorkDir, ".runner-tmp", "inputs", "other-attempt")
	if err = os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(other, "keep"), []byte("other owned attempt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	if c.workspaceInputs(j) == nil || w.ReleaseAttempt(t.Context(), a) == nil {
		t.Fatal("changed input was overwritten or deleted without ownership proof")
	}
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.ReleaseAttempt(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Fatal("owned input directory not cleaned", err)
	}
	if _, err = os.Stat(filepath.Join(other, "keep")); err != nil {
		t.Fatal("cleanup touched another attempt", err)
	}
}

func TestStagedEvidenceRefusesLinksAndUnexpectedFiles(t *testing.T) {
	for _, kind := range []string{"parent link", "file link", "unexpected file", "oversized drift"} {
		t.Run(kind, func(t *testing.T) {
			d := workspaceDisk(t)
			a := sourceAssignment(d, "", "evidence_owner")
			a.Setup.Profile.Access = "workspace-write"
			a.Inputs = map[string]runner.Value{"evidence": {ArtifactID: "baseline/report"}}
			if err := d.Write("artifacts/baseline/report", []byte(`{"retained":true}`), a.Limits.MaxArtifact); err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(d.Root, "work", a.RunID, "source")
			if err := os.MkdirAll(work, 0700); err != nil {
				t.Fatal(err)
			}
			dir, err := stagedInputDirectory(work, a.AttemptID, true)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "parent link":
				if err = os.Remove(dir); err == nil {
					err = os.Symlink(t.TempDir(), dir)
				}
			case "file link":
				err = os.Symlink(filepath.Join(d.Root, "artifacts", "baseline", "report"), filepath.Join(dir, "evidence"))
			case "unexpected file":
				err = os.WriteFile(filepath.Join(dir, "unowned"), []byte("preserve"), 0600)
			case "oversized drift":
				var file *os.File
				file, err = os.Create(filepath.Join(dir, "evidence"))
				if err == nil {
					err = file.Truncate(int64(a.Limits.MaxArtifact) * 2)
					file.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			c := &Codex{disk: d}
			if err = c.workspaceInputs(nativeJob{Assignment: a, Workspace: &workspaceRecord{WorkDir: work}}); err == nil {
				t.Fatal("untrusted evidence path accepted")
			}
		})
	}
}
