package execution

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func (r *Releases) experienceFeedback(j releaseJob, o runner.Observation) (runner.Observation, error) {
	var source, experience string
	if json.Unmarshal(j.Assignment.Inputs["source_review"].Inline, &source) != nil || json.Unmarshal(j.Assignment.Inputs["experience_review"].Inline, &experience) != nil {
		return o, errors.New("review summaries are missing")
	}
	summary := "Source review: " + strings.TrimSpace(source) + "\nProduct experience review: " + strings.TrimSpace(experience)
	if len(summary) > 1024 {
		return o, errors.New("review feedback exceeds its bounded handoff")
	}
	j.End = &runner.ExecutionEnd{
		Kind: "returned",
		Result: &runner.StepResult{
			Outcome: "ready",
			Outputs: map[string]runner.Value{"summary": runner.Text(summary)},
		},
	}
	if err := r.save(j); err != nil {
		return o, err
	}
	o.State, o.End = runner.Finished, j.End
	return o, nil
}

func experienceData(disk *runner.Disk, a runner.Assignment) (release.ExperienceManifest, map[string][]byte, error) {
	var receipt release.Receipt
	if a.Inputs["evidence"].ArtifactID == "" || json.Unmarshal(a.Inputs["receipt"].Inline, &receipt) != nil {
		return release.ExperienceManifest{}, nil, errors.New("experience review needs a declared evidence artifact and exact qualification receipt")
	}
	data, err := disk.Read("artifacts/"+a.Inputs["evidence"].ArtifactID, a.Limits.MaxArtifact)
	if err != nil {
		return release.ExperienceManifest{}, nil, err
	}
	return release.DecodeExperienceBundle(data, receipt)
}

func (r *Releases) finishExperience(j releaseJob, o runner.Observation, response release.Response) (runner.Observation, error) {
	var receipt release.Receipt
	if json.Unmarshal(j.Assignment.Inputs["receipt"].Inline, &receipt) != nil || release.Digest(response.Evidence) != response.EvidenceSHA256 || len(response.Evidence) > int(j.Assignment.Limits.MaxArtifact) {
		return o, errors.New("operator experience evidence identity or bound differs")
	}
	if _, _, err := release.DecodeExperienceBundle(response.Evidence, receipt); err != nil {
		return o, err
	}
	canonical, _ := json.Marshal(receipt)
	var returned release.Receipt
	if json.Unmarshal(response.Receipt, &returned) != nil || returned != receipt {
		return o, errors.New("operator exported evidence for another qualification")
	}
	id := j.Assignment.AttemptID + "/experience.tar.gz"
	if err := r.disk.Write("artifacts/"+id, response.Evidence, j.Assignment.Limits.MaxArtifact); err != nil {
		return o, err
	}
	j.End = &runner.ExecutionEnd{
		Kind: "returned",
		Result: &runner.StepResult{
			Outcome: "ready",
			Outputs: map[string]runner.Value{
				"receipt":  {Inline: canonical},
				"artifact": {ArtifactID: id},
			},
		},
	}
	if err := r.save(j); err != nil {
		return o, err
	}
	o.State, o.End = runner.Finished, j.End
	return o, nil
}

func (r *Releases) experienceGate(j releaseJob, o runner.Observation) (runner.Observation, error) {
	manifest, _, err := experienceData(r.disk, j.Assignment)
	var verdict string
	if err == nil {
		if json.Unmarshal(j.Assignment.Inputs["verdict"].Inline, &verdict) != nil {
			err = errors.New("independent experience verdict is missing")
		} else {
			err = release.ExperienceAcceptance(manifest, verdict)
		}
	}
	// No native Safari producer is qualified in this build. This gate must stay
	// closed even if a model incorrectly approves incomplete evidence.
	if err == nil {
		err = errors.New("native Safari producer is not qualified")
	}
	j.End = &runner.ExecutionEnd{
		Kind: "returned",
		Result: &runner.StepResult{
			Outcome: "blocked",
			Reason:  runner.Clip(err.Error()),
			Outputs: map[string]runner.Value{"receipt": j.Assignment.Inputs["receipt"]},
		},
	}
	if err := r.save(j); err != nil {
		return o, err
	}
	o.State, o.End = runner.Finished, j.End
	return o, nil
}

// Images are attached explicitly by the trusted adapter; the model gets no
// image tool, browser, operator command, private artifact store or extra access.
func experienceImageInputs(disk *runner.Disk, j nativeJob) ([]any, error) {
	if j.Assignment.Setup.Profile.Mode != "experience-review" {
		return nil, nil
	}
	if j.Workspace == nil {
		return nil, errors.New("experience images need the prepared read-only workspace")
	}
	_, files, err := experienceData(disk, j.Assignment)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, 36)
	for name := range files {
		if filepath.Ext(name) == ".png" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	inputs := []any{map[string]any{
		"type": "text",
		"text": "Authenticated candidate viewport images follow in lexicographic filename order. Match them to images[].name in the protected experience-experience.json manifest. Historical reference evidence does not establish this candidate's behavior.",
	}}
	for _, name := range names {
		inputs = append(inputs, map[string]any{
			"type": "localImage",
			"path": filepath.Join(j.Workspace.WorkDir, ".runner-tmp", "inputs", j.Assignment.AttemptID, "experience-"+name),
		})
	}
	return inputs, nil
}
