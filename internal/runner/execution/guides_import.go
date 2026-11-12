package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

func guidesImportHelperBytes(path, expected string) ([]byte, error) {
	return readPinnedCommandHelper(path, expected, "runner-guides-import", 16<<20)
}

func validateGuidesImportAssignment(a runner.Assignment) error {
	p := a.Setup.Profile
	var request string
	if a.ProjectID != "jacks-break" || p.Harness != "command" || p.Mode != "guides-import" || p.Access != "workspace-read" || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(p.GuidesImportSHA256) || a.FrontendCheckPins != nil || p.Command == nil || len(a.Inputs) != 1 || json.Unmarshal(a.Inputs["request"].Inline, &request) != nil || request != guidesmedia.RequestID || !reflect.DeepEqual(a.OutputTypes, guidesmedia.OutputContract()) {
		return errors.New("guide import assignment/source/profile contract differs")
	}
	c := p.Command
	if len(c.Args) != 7 || !filepath.IsAbs(c.Args[0]) || !reflect.DeepEqual(c.Args[1:], []string{"--output", "{scratch}/media", "--run", "{run_id}", "--attempt", "{attempt_id}"}) || c.Network || c.Directory != "" || c.Artifact != "" || c.Result != "" || len(c.Env) != 0 || len(c.ExitOutcomes) != 0 {
		return errors.New("guide import cannot select another source root/argv/network/output contract")
	}
	return nil
}

func collectGuidesImport(d *runner.Disk, job commandJob, result commandResult) (*runner.ExecutionEnd, error) {
	a := job.Assignment
	if err := validateGuidesImportAssignment(a); err != nil {
		return nil, err
	}
	if result.ExitCode != 0 || result.Kind != "returned" || job.Workspace == nil {
		return nil, errors.New("guide import lacks its settled successful owned command")
	}
	root := filepath.Join(job.Workspace.WorkDir, ".runner-tmp", a.AttemptID, "media")
	data, index, err := guidesmedia.ReadOutputIndex(root)
	if err != nil {
		return nil, err
	}
	if index.RunID != a.RunID || index.AttemptID != a.AttemptID || index.HelperSHA256 != a.Setup.Profile.GuidesImportSHA256 {
		return nil, errors.New("guide import run/attempt/helper authority differs")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != guidesmedia.AssetCount+1 {
		return nil, errors.New("guide import output contains missing or unlisted files")
	}
	wanted := map[string]bool{"media-index.json": true}
	for _, asset := range index.Assets {
		wanted[guidesmedia.OutputName(asset.Game, asset.Role)] = true
	}
	for _, entry := range entries {
		if !wanted[entry.Name()] || !entry.Type().IsRegular() {
			return nil, errors.New("guide import output contains foreign paths or links")
		}
	}
	outputs := map[string]runner.Value{"check": runner.Text("Authenticated exact36-file/42,362,476-byte immutable media set; publication not performed")}
	write := func(id string, content []byte) error {
		previous, err := d.Read("artifacts/"+id, a.Limits.MaxArtifact)
		if err == nil {
			if !bytes.Equal(previous, content) {
				return errors.New("guide import artifact changed; preserve existing bytes")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return d.Write("artifacts/"+id, content, a.Limits.MaxArtifact)
	}
	for _, asset := range index.Assets {
		content, err := guidesmedia.ReadOutputAsset(context.Background(), root, asset)
		if err != nil {
			return nil, err
		}
		if err := write(asset.ArtifactID, content); err != nil {
			return nil, err
		}
		outputs[asset.Game+"_"+asset.Role] = runner.Value{ArtifactID: asset.ArtifactID}
	}
	// The index is registered last. A partial collection is not an accepted set.
	id := a.AttemptID + "/media-index.json"
	if err := write(id, data); err != nil {
		return nil, err
	}
	outputs["media_index"] = runner.Value{ArtifactID: id}
	return &runner.ExecutionEnd{
		Kind: "returned",
		Result: &runner.StepResult{
			Outcome: "ready",
			Reason:  "complete immutable guide media import authenticated",
			Outputs: outputs,
		},
	}, nil
}
