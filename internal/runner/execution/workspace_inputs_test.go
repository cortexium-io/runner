package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

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
