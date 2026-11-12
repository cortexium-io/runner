package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestGuidesExactAssetsCollectionRejoinRefusals(t *testing.T) {
	if os.Getenv("RUNNER_GUIDE_MEDIA_COLLECTOR") != "1" {
		t.Skip("closed raw importer in disposable state only")
	}
	policy := runner.DefaultStorage()
	policy.MaxManaged = 8 << 30
	policy.Database.MaxRecord = 40 << 10
	d, err := runner.OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	a := runner.Assignment{
		ProjectID: "jacks-break", RunID: "run_import", AttemptID: "attempt_import",
		Inputs:      map[string]runner.Value{"request": runner.Text(guidesmedia.RequestID)},
		OutputTypes: guidesmedia.OutputContract(), Limits: runner.ExecutionLimits{MaxArtifact: 16 << 20, MaxRecord: 40 << 10},
		Setup: runner.ExecutionSetup{Profile: runner.Profile{
			Harness: "command", Mode: "guides-import", Access: "workspace-read", GuidesImportSHA256: strings.Repeat("a", 64),
			Command: &runner.Command{Args: []string{
				"/owned/runner-guides-import",
				"--output",
				"{scratch}/media",
				"--run",
				"{run_id}",
				"--attempt",
				"{attempt_id}",
			}},
		}},
	}
	output := filepath.Join(work, ".runner-tmp", a.AttemptID, "media")
	os.MkdirAll(output, 0700)
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if _, err = guidesmedia.Import(canceled, output, a.RunID, a.AttemptID, a.Setup.Profile.GuidesImportSHA256); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled import accepted", err)
	}
	if entries, err := os.ReadDir(output); err != nil || len(entries) != 0 {
		t.Fatal("canceled import published output", entries, err)
	}
	x, err := guidesmedia.Import(t.Context(), output, a.RunID, a.AttemptID, a.Setup.Profile.GuidesImportSHA256)
	if err != nil {
		t.Fatal(err)
	}
	job := commandJob{Assignment: a, Workspace: &workspaceRecord{WorkDir: work}}
	missing := filepath.Join(output, guidesmedia.OutputName(x.Assets[1].Game, x.Assets[1].Role))
	raw, err := os.ReadFile(missing)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[0] ^= 1
	os.WriteFile(missing, corrupt, 0600)
	if _, err = collectGuidesImport(d, job, commandResult{Kind: "returned"}); err == nil {
		t.Fatal("changed output accepted")
	}
	if first, err := d.Read("artifacts/"+x.Assets[0].ArtifactID, 16<<20); err != nil || guidesmedia.Digest(first) != x.Assets[0].SHA256 {
		t.Fatal("fixture did not reach partial raw collection", err)
	}
	if _, err = d.Read("artifacts/"+a.AttemptID+"/media-index.json", 32<<10); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial collection published index", err)
	}
	os.WriteFile(missing, raw, 0600)
	accepted, err := collectGuidesImport(d, job, commandResult{Kind: "returned"})
	if err != nil || accepted.Result.Outcome != "ready" {
		t.Fatal("same collection did not rejoin", accepted, err)
	}
	if _, err = collectGuidesImport(d, job, commandResult{Kind: "returned"}); err != nil {
		t.Fatal("lost acknowledgement did not replay", err)
	}
	artifact := "artifacts/" + x.Assets[0].ArtifactID
	prior, err := d.Read(artifact, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), prior...)
	changed[0] ^= 1
	d.Write(artifact, changed, 16<<20)
	if _, err = collectGuidesImport(d, job, commandResult{Kind: "returned"}); err == nil {
		t.Fatal("changed retained artifact overwritten")
	}
	retained, _ := d.Read(artifact, 16<<20)
	if guidesmedia.Digest(retained) != guidesmedia.Digest(changed) {
		t.Fatal("refusal replaced changed artifact")
	}
	t.Log("partial collection cannot publish an index; same immutable collection rejoined and replayed without helper/source redispatch; changed bytes preserved on refusal; canceled import created no output")
}
