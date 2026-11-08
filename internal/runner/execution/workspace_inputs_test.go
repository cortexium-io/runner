package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestOverflowInlineInputsRejoinAndCleanWithExistingOwnedFiles(t *testing.T) {
	d := workspaceDisk(t)
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
