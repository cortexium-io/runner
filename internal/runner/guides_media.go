package runner

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/guidesmedia"
)

// Only the new explicitly selected complete process can consume an accepted
// whole import. A supplied path/index alone grants no source authority.
func (c *Core) bindGuidesMedia(r *Run) error {
	if r.Process.ID == "stage-guides-media" {
		if r.ProjectID != "jacks-break" || r.Process.GuidesMedia != "" || len(r.Process.Steps) != 1 || r.Process.Steps[0].ForEach != nil {
			return errors.New("guide import process scope differs")
		}
		var request string
		if len(r.Inputs) != 1 || json.Unmarshal(r.Inputs["request"].Inline, &request) != nil || request != guidesmedia.RequestID {
			return errors.New("guide import request differs from the approved immutable set")
		}
		roleID := r.Process.Steps[0].Role
		role := r.Roles[roleID]
		profile, err := role.Profile(r.Profiles[roleID])
		if err != nil || profile.Mode != "guides-import" || profile.Harness != "command" || profile.Access != "workspace-read" || !reflect.DeepEqual(role.Outputs, guidesmedia.OutputContract()) {
			return errors.New("guide import requires its closed trusted profile")
		}
		r.RecordFormat = 6
		return nil
	}
	if r.Process.GuidesMedia == "" {
		for id, role := range r.Roles {
			p, err := role.Profile(r.Profiles[id])
			if err == nil && p.Mode == "guides-import" {
				return errors.New("guide importer is restricted to its separate declared import process")
			}
		}
		return nil
	}
	if r.ProjectID != "jacks-break" || r.Process.ID != "publish-guides-media" || r.Process.GuidesMedia != guidesmedia.AssetFormat || r.Process.Inputs["media_index"] != "artifact" {
		return errors.New("guide media source is restricted to its new complete process")
	}
	id := r.Inputs["media_index"].ArtifactID
	owner, err := c.store.Artifact(c.ctx, id)
	if err != nil || owner.ProjectID != r.ProjectID {
		return errors.New("guide media index has no owning project artifact")
	}
	data, err := c.disk.Read("artifacts/"+id, 32<<10)
	if err != nil {
		return err
	}
	index, err := guidesmedia.DecodeIndex(data)
	if err != nil || id != index.AttemptID+"/media-index.json" || owner.RunID != index.RunID {
		return errors.New("guide media index provenance differs")
	}
	importRun, ok := c.runs[index.RunID]
	if !ok || importRun.Status != "Completed" || importRun.ProjectID != r.ProjectID || importRun.Process.ID != "stage-guides-media" || importRun.RecordFormat != 6 || !importRun.ResourcesReleased || !allAttemptsReleased(importRun) || importRun.CleanupError != "" || importRun.CleanupBlocked || len(importRun.Process.Steps) != 1 {
		return errors.New("guide media import is not completed and settled")
	}
	a := importRun.Current()
	if a == nil || a.ID != index.AttemptID || a.End == nil || a.End.Kind != "returned" || a.End.Result == nil || a.End.Result.Outcome != "ready" || a.End.Result.Outputs["media_index"].ArtifactID != id {
		return errors.New("guide media index is not its accepted import output")
	}
	role := importRun.Roles[importRun.Process.Steps[0].Role]
	profile, err := role.Profile(a.Profile)
	if err != nil || profile.Mode != "guides-import" || profile.Harness != "command" || profile.Access != "workspace-read" || profile.GuidesImportSHA256 != index.HelperSHA256 || !reflect.DeepEqual(role.Outputs, guidesmedia.OutputContract()) {
		return errors.New("guide media import profile/helper differs")
	}
	for _, asset := range index.Assets {
		art, err := c.store.Artifact(c.ctx, asset.ArtifactID)
		if err != nil || art.RunID != importRun.ID || art.ProjectID != r.ProjectID || art.Bytes != asset.Bytes || a.End.Result.Outputs[asset.Game+"_"+asset.Role].ArtifactID != asset.ArtifactID {
			return errors.New("guide media raw artifact ownership differs")
		}
		content, err := c.disk.Read("artifacts/"+asset.ArtifactID, c.disk.Policy.MaxArtifact)
		if err != nil || int64(len(content)) != asset.Bytes || guidesmedia.Digest(content) != asset.SHA256 {
			return errors.New("guide media raw artifact changed before admission")
		}
	}
	binding, err := index.Bind(data)
	if err != nil {
		return err
	}
	r.GuidesMedia, r.RecordFormat = &binding, 6
	return nil
}

func validateGuidesMediaRecord(r Run) error {
	if r.RecordFormat != 6 {
		if r.GuidesMedia != nil || r.Process.GuidesMedia != "" || r.Process.ID == "stage-guides-media" {
			return errors.New("guide media authority requires record format6")
		}
		for id, role := range r.Roles {
			p, err := role.Profile(r.Profiles[id])
			if err == nil && p.Mode == "guides-import" {
				return errors.New("guide import requires record format6")
			}
		}
		return nil
	}
	if r.UnpublishedCardRecovery != nil || r.ProjectID != "jacks-break" {
		return errors.New("guide media record cannot grant historical recovery authority")
	}
	if r.Process.ID == "stage-guides-media" && r.GuidesMedia == nil && r.Process.GuidesMedia == "" {
		return nil
	}
	if r.GuidesMedia == nil || r.GuidesMedia.ValidateShape() != nil || r.Process.GuidesMedia != guidesmedia.AssetFormat || r.Process.ID != "publish-guides-media" && (r.Process.ID != "isolated-card" || r.Task == nil) || r.Inputs["media_index"].ArtifactID != r.GuidesMedia.IndexID {
		return errors.New("guide media record lacks its immutable whole-set authority")
	}
	return nil
}
