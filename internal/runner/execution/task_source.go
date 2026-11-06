package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cortexium-io/runner/internal/runner"
)

// TaskSources owns local, unreviewed task pins and source assembly. It never
// publishes a branch or substitutes for the final independent review.
type TaskSources struct {
	disk       *runner.Disk
	workspaces *Workspaces
	mu         sync.Mutex
	workers    map[string]*taskSourceWork
	wake       func(string)
	closed     bool
}

type taskSourceJob struct {
	Assignment runner.Assignment    `json:"assignment"`
	InputSHA   string               `json:"input_sha256,omitempty"`
	Tree       string               `json:"tree,omitempty"`
	Commit     string               `json:"commit,omitempty"`
	PatchID    string               `json:"patch_id,omitempty"`
	Conflicts  []string             `json:"conflicts,omitempty"`
	Source     *runner.SourceResult `json:"source,omitempty"`
	Capture    *captureReference    `json:"capture,omitempty"`
	Stopped    bool                 `json:"stopped,omitempty"`
	PinIntent  bool                 `json:"pin_intent,omitempty"`
	End        *runner.ExecutionEnd `json:"end,omitempty"`
}

type taskSourceEvidence struct {
	Mode            string                 `json:"mode"`
	Status          string                 `json:"status"`
	Identity        runner.TaskIdentity    `json:"identity"`
	WritePaths      []string               `json:"write_paths,omitempty"`
	Dependencies    []runner.TaskCandidate `json:"dependencies"`
	SeedCommit      string                 `json:"seed_commit,omitempty"`
	SeedTree        string                 `json:"seed_tree,omitempty"`
	Candidate       *runner.TaskCandidate  `json:"candidate,omitempty"`
	ScopeViolations []string               `json:"scope_violations,omitempty"`
	Conflicts       []string               `json:"conflicts,omitempty"`
	Plan            *runner.TaskManifest   `json:"plan,omitempty"`
}

func taskSourceName(id string) string          { return "jobs/" + id + ".task-source.json" }
func taskPinRef(id string) string              { return "refs/runner/tasks/" + id + "/head" }
func taskPinHistory(id, attempt string) string { return "refs/runner/tasks/" + id + "/pins/" + attempt }

func (s *TaskSources) Owns(id string) bool {
	_, err := os.Lstat(filepath.Join(s.disk.Root, taskSourceName(id)))
	return !errors.Is(err, os.ErrNotExist)
}
func (s *TaskSources) load(id string) (taskSourceJob, error) {
	var j taskSourceJob
	data, err := s.disk.Read(taskSourceName(id), s.disk.Policy.Database.MaxRecord)
	if err == nil {
		err = json.Unmarshal(data, &j)
	}
	if err == nil && j.Assignment.AttemptID != id {
		err = errors.New("task source job identity differs")
	}
	return j, err
}
func (s *TaskSources) save(j taskSourceJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stop is durable even when an in-flight worker holds an older job copy.
	if old, err := s.load(j.Assignment.AttemptID); err == nil && old.Stopped {
		j.Stopped = true
	}
	return s.saveLocked(j)
}
func (s *TaskSources) saveLocked(j taskSourceJob) error {
	if j.Stopped && j.End != nil {
		j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "task source operation settled after cancellation"}
	}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return s.disk.Write(taskSourceName(j.Assignment.AttemptID), data, s.disk.Policy.Database.MaxRecord)
}
func (s *TaskSources) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	if old, err := s.load(a.AttemptID); err == nil {
		before, _ := json.Marshal(old.Assignment)
		after, _ := json.Marshal(a)
		if !bytes.Equal(before, after) {
			return runner.Observation{}, errors.New("task source attempt arguments changed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return runner.Observation{}, err
	} else if err = s.save(taskSourceJob{Assignment: runner.Clone(a)}); err != nil {
		return runner.Observation{}, err
	}
	return s.Observe(ctx, a.AttemptID)
}
func (s *TaskSources) Observe(ctx context.Context, id string) (runner.Observation, error) {
	j, err := s.load(id)
	if err != nil {
		return runner.Observation{}, err
	}
	if j.End == nil {
		return s.joinWorker(ctx, j)
	}
	zero := int64(0)
	return runner.Observation{State: runner.Finished, End: runner.Clone(j.End), Source: runner.Clone(j.Source), Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}}, nil
}
func (s *TaskSources) Stop(ctx context.Context, id string) error {
	s.mu.Lock()
	j, err := s.load(id)
	if err != nil || j.End != nil {
		s.mu.Unlock()
		return err
	}
	j.Stopped = true
	work := s.workers[id]
	if work == nil && j.PatchID == "" && !j.PinIntent {
		j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "task source stopped before workspace mutation"}
	}
	err = s.saveLocked(j)
	if work != nil {
		work.cancel()
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if j.End == nil {
		_, err = s.Observe(ctx, id)
	}
	return err
}
func (s *TaskSources) Release(_ context.Context, id string) error {
	s.mu.Lock()
	if work := s.workers[id]; work != nil {
		select {
		case <-work.done:
			delete(s.workers, id)
		default:
			s.mu.Unlock()
			return errors.New("cannot release active task source worker")
		}
	}
	s.mu.Unlock()
	j, err := s.load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.End == nil {
		return errors.New("cannot release unsettled task source operation")
	}
	if err = s.workspaces.ReleaseAttempt(j.Assignment); err != nil {
		return err
	}
	if err = s.workspaces.releaseTaskScratch(j); err != nil {
		return err
	}
	return s.disk.Remove(taskSourceName(id))
}

func strictTaskJSON(data []byte, out any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("task source JSON has trailing data")
	}
	return nil
}
func taskComponent(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (s *TaskSources) authority(a runner.Assignment) error {
	t := a.TaskSource
	if t == nil || !taskComponent(a.RunID) || !taskComponent(a.AttemptID) || !taskComponent(t.Identity.RootRunID) || !taskComponent(t.Identity.GroupStep) {
		return errors.New("Core task source authority is missing or invalid")
	}
	if a.Setup.Profile.Harness != "task-source" || a.Setup.Profile.Access != "workspace-read" {
		return errors.New("task source requires its fixed workspace-read harness")
	}
	switch a.Setup.Profile.Mode {
	case "seed", "pin":
		if !taskComponent(t.Identity.ItemID) || a.RunID == t.Identity.RootRunID {
			return errors.New("task child identity differs")
		}
	case "join":
		if t.Identity.ItemID != "" || a.RunID != t.Identity.RootRunID {
			return errors.New("task root join identity differs")
		}
	default:
		return errors.New("unsupported task source mode")
	}
	return validateTaskPaths(t.WritePaths)
}
func (s *TaskSources) execute(ctx context.Context, j *taskSourceJob) error {
	a := j.Assignment
	if err := s.authority(a); err != nil {
		return err
	}
	if a.Setup.Profile.Mode == "pin" {
		if !j.PinIntent {
			j.PinIntent = true
			if err := s.save(*j); err != nil {
				return err
			}
		}
		return s.pin(ctx, j)
	}
	var plan *runner.TaskManifest
	var input []byte
	if a.Setup.Profile.Mode == "seed" {
		input = a.Inputs["dependencies"].Inline
		var dependencies []runner.TaskCandidate
		if err := strictTaskJSON(input, &dependencies); err != nil {
			return err
		}
		if len(dependencies) != len(a.TaskSource.Dependencies) {
			return errors.New("dependency input differs from Core authority")
		}
		for i := range dependencies {
			if dependencies[i] != a.TaskSource.Dependencies[i] {
				return errors.New("dependency candidate differs from Core authority")
			}
		}
	} else {
		if s.workspaces.read == nil {
			return errors.New("task manifest reader is unavailable")
		}
		var err error
		input, err = s.workspaces.read(ctx, a.Inputs["results"].ArtifactID)
		if err != nil {
			return err
		}
		if len(input) > int(a.Limits.MaxArtifact) {
			return errors.New("task manifest exceeds artifact limit")
		}
		plan = &runner.TaskManifest{}
		if err = strictTaskJSON(input, plan); err != nil {
			return err
		}
		if err = validateTaskManifest(a, *plan); err != nil {
			return err
		}
	}
	digest := captureDigest(input)
	if j.InputSHA != "" && j.InputSHA != digest {
		return errors.New("task source input changed after intent")
	}
	if j.PatchID == "" {
		tree, commit, patch, conflicts, err := s.workspaces.assembleTaskSource(ctx, a)
		if err != nil {
			return err
		}
		j.InputSHA, j.Tree, j.Commit, j.Conflicts = digest, tree, commit, conflicts
		j.PatchID = a.AttemptID + "/changes.patch"
		if err = s.disk.Write("artifacts/"+j.PatchID, patch, a.Limits.MaxArtifact); err != nil {
			return err
		}
		if err = s.save(*j); err != nil {
			return err
		}
	}
	if err := s.workspaces.prepareTaskSource(ctx, a, j.PatchID, j.Tree, j.Commit, j.InputSHA); err != nil {
		return err
	}
	if j.Source == nil {
		source, err := s.workspaces.Capture(ctx, a)
		if err != nil {
			return err
		}
		if source.Tree != j.Tree {
			return errors.New("task assembled tree differs from retained capture")
		}
		if err := s.workspaces.lockContext(ctx); err != nil {
			return err
		}
		j.Source = source
		record, loadErr := s.workspaces.load(a.RunID)
		if loadErr == nil && record.Capture != nil && record.Capture.AttemptID == a.AttemptID {
			j.Capture = runner.Clone(record.Capture)
		}
		s.workspaces.mu.Unlock()
		if loadErr != nil {
			return loadErr
		}
		if j.Capture == nil {
			return errors.New("task source capture binding is missing")
		}
		if err = s.save(*j); err != nil {
			return err
		}
	}
	outcome, feedback := "ready", ""
	if len(j.Conflicts) > 0 {
		outcome = "changes_requested"
		feedback = runner.Clip("Resolve retained task text conflicts: " + strings.Join(j.Conflicts, ", "))
	}
	evidence := taskSourceEvidence{Mode: a.Setup.Profile.Mode, Status: outcome, Identity: a.TaskSource.Identity, WritePaths: a.TaskSource.WritePaths, Dependencies: a.TaskSource.Dependencies, SeedCommit: j.Commit, SeedTree: j.Tree, Conflicts: j.Conflicts, Plan: plan}
	manifest, err := s.evidence(a, evidence)
	if err != nil {
		return err
	}
	outputs := map[string]runner.Value{"patch": {ArtifactID: j.Source.PatchID}, "manifest": {ArtifactID: manifest}}
	if a.Setup.Profile.Mode == "join" {
		outputs["feedback"] = runner.Text(feedback)
	}
	j.End = &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: outcome, Outputs: outputs, Reason: feedback}}
	return nil
}
func (s *TaskSources) evidence(a runner.Assignment, value taskSourceEvidence) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	id := a.AttemptID + "/task-manifest.json"
	if err = s.disk.Write("artifacts/"+id, data, a.Limits.MaxArtifact); err != nil {
		return "", err
	}
	return id, nil
}
func validateTaskManifest(a runner.Assignment, m runner.TaskManifest) error {
	t := a.TaskSource
	repo, err := filepath.EvalSymlinks(a.Repository)
	if err != nil {
		return err
	}
	if a.Source == nil || m.RootRunID != t.Identity.RootRunID || m.GroupStep != t.Identity.GroupStep || m.Repository != repo || m.Base != a.Source.Ref || m.RootPatchID != t.RootPatchID || len(m.Items) != len(t.Dependencies) {
		return errors.New("task manifest identity or source differs from Core authority")
	}
	seen := map[string]bool{}
	for _, item := range m.Items {
		if seen[item.ID] || item.ID != item.Candidate.Identity.ItemID || item.RunID != item.Candidate.RunID {
			return errors.New("task manifest item identity differs")
		}
		seen[item.ID] = true
		found := false
		for _, candidate := range t.Dependencies {
			found = found || candidate == item.Candidate
		}
		if !found {
			return errors.New("task manifest candidate was not supplied by Core")
		}
	}
	return nil
}
func (s *TaskSources) pin(ctx context.Context, j *taskSourceJob) error {
	a := j.Assignment
	candidate, violations, err := s.workspaces.pinTaskSource(ctx, a)
	if err != nil {
		return err
	}
	outcome, feedback := "ready", ""
	if len(violations) > 0 {
		outcome = "changes_requested"
		feedback = runner.TaskScopeFeedback
	}
	evidence := taskSourceEvidence{Mode: "pin", Status: outcome, Identity: a.TaskSource.Identity, WritePaths: a.TaskSource.WritePaths, Dependencies: a.TaskSource.Dependencies, Candidate: candidate, ScopeViolations: violations}
	manifest, err := s.evidence(a, evidence)
	if err != nil {
		return err
	}
	data := []byte(`{}`)
	if candidate != nil {
		data, err = json.Marshal(candidate)
		if err != nil {
			return err
		}
	}
	j.End = &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: outcome, Outputs: map[string]runner.Value{"candidate": {Inline: data}, "manifest": {ArtifactID: manifest}, "feedback": runner.Text(feedback)}, Reason: feedback}}
	return nil
}

func (s *TaskSources) ForgetRun(ctx context.Context, id string) error {
	return s.workspaces.forgetTaskPins(ctx, id)
}
