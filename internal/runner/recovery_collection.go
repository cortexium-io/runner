package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type RecoverCollectionRequest struct {
	RequestID string `json:"request_id"`
	RunID     string `json:"run_id"`
	AttemptID string `json:"attempt_id"`
}

// Earlier grants remain in the ordinary idempotent receipt history. This is
// the latest operational handoff, not a changed process or an attempt grant.
type CollectionRecovery struct {
	RequestID string    `json:"request_id"`
	AttemptID string    `json:"attempt_id"`
	Owner     string    `json:"owner"`
	Artifact  string    `json:"artifact"`
	PatchID   string    `json:"patch_id"`
	GrantedAt time.Time `json:"granted_at"`
}

const collectionOverflowReason = "command exited 1: measurement output exceeds 256KiB"

// RecoverCollection is an explicit owner action for the known, settled legacy
// collector overflow. It routes the preserved candidate through its configured
// repair owner without rewriting the failed measurement or increasing limits.
func (c *Core) RecoverCollection(req RecoverCollectionRequest) (Run, error) {
	args, _ := json.Marshal(req)
	c.mu.Lock()
	if r, ok, err := c.replay(req.RequestID, "recover_collection", args); ok || err != nil {
		c.mu.Unlock()
		return r, err
	}
	r, source, owner, err := c.collectionRecovery(req)
	validator, supported := c.executor.(SourceRecoveryValidator)
	c.mu.Unlock()
	if err != nil {
		return Run{}, err
	}
	if !supported {
		return Run{}, errors.New("executor cannot validate retained collection source")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	err = validator.ValidateSourceRecovery(ctx, Clone(r), Clone(source))
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if replay, ok, err := c.replay(req.RequestID, "recover_collection", args); ok || err != nil {
		return replay, err
	}
	if err != nil {
		return Run{}, fmt.Errorf("retained collection source: %w", err)
	}
	if current, ok := c.runs[r.ID]; !ok || current.Revision != r.Revision {
		return Run{}, errors.New("run changed during collection validation; inspect before recovery")
	}
	artifactID := req.AttemptID + "/collection-failure.json"
	diagnostic, _ := json.Marshal(struct {
		Schema            int    `json:"schema"`
		Status            string `json:"status"`
		EvidenceStatus    string `json:"evidence_status"`
		Incomplete        bool   `json:"incomplete"`
		FailedAttempt     string `json:"failed_attempt"`
		Reason            string `json:"reason"`
		CandidatePatch    string `json:"candidate_patch"`
		MeasurementNotice string `json:"measurement_notice"`
	}{
		Schema:            1,
		Status:            "collection_failure",
		EvidenceStatus:    "incomplete",
		Incomplete:        true,
		FailedAttempt:     req.AttemptID,
		Reason:            collectionOverflowReason,
		CandidatePatch:    source.PatchID,
		MeasurementNotice: "No complete measurement report was retained; no timing, sample or seed result is established.",
	})

	// Resolve existing evidence before normal backward invalidation, then replace
	// only the explicitly declared measurement input with the truthful diagnostic.
	c.advance(&r, &StepResult{Outcome: "collection_failure"}, Route{Next: owner})
	if r.Status != "Queued" {
		return Run{}, fmt.Errorf("saved collection cannot advance: %s", r.Reason)
	}
	r.StepInputs["measurement"] = Value{ArtifactID: artifactID}
	role := r.Roles[r.Process.Steps[r.Step].Role]
	if err := ValidateValues(r.StepInputs, role.Inputs); err != nil {
		return Run{}, err
	}
	r.ResourcesReleased = false
	r.CollectionRecovery = &CollectionRecovery{
		RequestID: req.RequestID,
		AttemptID: req.AttemptID,
		Owner:     owner,
		Artifact:  artifactID,
		PatchID:   source.PatchID,
		GrantedAt: time.Now().UTC(),
	}
	if err := c.collectionRecoveryAdmission(r); err != nil {
		return Run{}, err
	}
	if prior, err := c.store.Artifact(c.ctx, artifactID); err == nil {
		if prior.RunID != r.ID || prior.ProjectID != r.ProjectID || prior.Bytes != int64(len(diagnostic)) {
			return Run{}, errors.New("collection diagnostic artifact ownership differs")
		}
	} else if !errors.Is(err, ErrNotFound) {
		return Run{}, err
	}
	name := "artifacts/" + artifactID
	if prior, err := c.disk.Read(name, c.disk.Policy.MaxArtifact); err == nil {
		if !bytes.Equal(prior, diagnostic) {
			return Run{}, errors.New("retained collection diagnostic differs; preserve it")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Run{}, err
	} else if err = c.disk.Write(name, diagnostic, c.disk.Policy.MaxArtifact); err != nil {
		return Run{}, err
	}
	artifact := Artifact{ID: artifactID, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(diagnostic))}
	receipt := Receipt{Key: req.RequestID, Kind: "recover_collection", Args: args, RunID: r.ID}
	if err := c.persist(r, []Artifact{artifact}, &receipt); err != nil {
		return Run{}, err
	}
	c.Wake()
	return Clone(r), nil
}

func (c *Core) collectionRecovery(req RecoverCollectionRequest) (Run, SourceResult, string, error) {
	r, ok := c.runs[req.RunID]
	if !ok {
		return Run{}, SourceResult{}, "", ErrNotFound
	}
	r = Clone(r)
	reject := func(reason string) (Run, SourceResult, string, error) {
		return Run{}, SourceResult{}, "", errors.New(reason)
	}
	if r.Task != nil || r.Status != "Failed" || r.CancelRequested || r.Active() || r.Decision != nil || c.inFlight[r.ID] || r.CleanupBlocked || r.CleanupError != "" || !r.ResourcesReleased {
		return reject("collection recovery needs settled failed root work and confirmed cleanup")
	}
	a := r.Current()
	if a == nil || a.ID != req.AttemptID || a.State != Finished || a.Superseded || a.TimedOut || a.StopRequested || a.End == nil || a.End.Kind != "failed" || a.End.Retryable || a.End.Result != nil || strings.TrimSpace(a.End.Reason) != collectionOverflowReason || r.Reason != a.End.Reason {
		return reject("collection recovery needs the exact saved stdout-overflow failure")
	}
	if r.CollectionRecovery != nil && r.CollectionRecovery.AttemptID == req.AttemptID {
		return reject("this failed measurement already received collection recovery")
	}
	for _, old := range r.Attempts {
		if old.State != Finished || !old.Released || c.inFlight[old.ID] {
			return reject("all execution and attempt cleanup must settle before collection recovery")
		}
	}
	for _, group := range r.TaskGroups {
		if group == nil || !group.Done || group.Failure != "" {
			return reject("accepted task group must settle before collection recovery")
		}
		for _, reservation := range group.Children {
			child, ok := c.runs[reservation.RunID]
			if !ok || child.Status != "Completed" || child.Active() || !allAttemptsReleased(child) || !child.ResourcesReleased || c.inFlight[child.ID] || reservation.Candidate == nil {
				return reject("accepted child work must settle before collection recovery")
			}
			var candidate TaskCandidate
			if json.Unmarshal(child.Outputs["result"].Inline, &candidate) != nil || candidate != *reservation.Candidate || !c.validTaskCandidate(child, group, candidate) {
				return reject("accepted child candidate ownership differs")
			}
		}
	}
	if c.familyRemaining(r) < 1 || r.Step < 1 || r.Step >= len(r.Process.Steps) {
		return reject("original collection completion budget is unavailable")
	}
	measure := r.Process.Steps[r.Step]
	role := r.Roles[measure.Role]
	profile, err := role.Profile(a.Profile)
	selected, selectedErr := role.Profile(r.Profiles[measure.Role])
	inputs := map[string]string{
		"patch":     "artifact",
		"package":   "text",
		"benchmark": "text",
		"previous":  "text",
	}
	if err != nil || selectedErr != nil || !reflect.DeepEqual(profile, selected) || !collectionMeasureProfile(profile) || measure.ForEach != nil || measure.Escalation != nil || !measure.Inputs["patch"].FromCandidate ||
		!reflect.DeepEqual(role.Inputs, inputs) || !reflect.DeepEqual(role.Outputs, map[string]string{"check": "text", "artifact": "artifact"}) {
		return reject("failed step is not the frozen read-only measurement collector")
	}
	checkIndex := r.Step - 1
	check := r.Process.Steps[checkIndex]
	route, eligible := sourceCorrectionRoute(r, checkIndex)
	ownerIndex := taskStepIndex(r.Process, route.Next)
	if !eligible || ownerIndex < 0 || readyNext(r.Process, ownerIndex) != checkIndex || readyNext(r.Process, checkIndex) != r.Step || check.Routes["ready"].Approval || r.Process.Steps[ownerIndex].Routes["ready"].Approval {
		return reject("collection repair needs the frozen owner/check/measure loop")
	}
	owner := r.Process.Steps[ownerIndex]
	ownerRole := r.Roles[owner.Role]
	binding := owner.Inputs["measurement"]
	if owner.Escalation != nil || ownerRole.Inputs["measurement"] != "artifact" || binding.FromStep != measure.ID || binding.Output != "artifact" || !binding.Previous || ownerRole.Inputs["request"] != "text" || owner.Inputs["request"].FromInput != "request" {
		return reject("configured owner lacks the explicit original-request/measurement handoff")
	}
	for _, name := range []string{"baseline", "manifest", "checks"} {
		if ownerRole.Inputs[name] != "artifact" || owner.Inputs[name].FromStep == "" {
			return reject("configured owner lacks retained baseline/plan/check evidence")
		}
	}
	var source *SourceResult
	var checked bool
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		old := r.Attempts[i]
		if old.Superseded || old.End == nil || old.End.Kind != "returned" || old.End.Result == nil {
			continue
		}
		if old.Step == checkIndex && !checked {
			if old.End.Result.Outcome != "ready" || old.End.Result.Outputs["artifact"].ArtifactID == "" || c.recoveryArtifact(r, old.End.Result.Outputs["artifact"].ArtifactID) != nil {
				return reject("preceding scoped check lacks its retained successful receipt")
			}
			checked = true
		}
		if source == nil && old.Source != nil && old.Source.PatchID != "" {
			if old.Source.AttemptID != old.ID || old.End.Result.Outcome != "ready" {
				return reject("latest captured source is not a settled accepted handoff")
			}
			source = old.Source
		}
	}
	if !checked || source == nil || source.Repository != r.Repository || source.Tree == "" || source.Base == "" || source.PatchID == "" || !strings.HasPrefix(source.PatchID, source.AttemptID+"/") || c.recoveryArtifact(r, source.PatchID) != nil || a.Source == nil || a.Source.InputPatchID != source.PatchID || a.Source.Repository != source.Repository || a.Source.Base != source.Base || a.Source.WorkDir != source.WorkDir || r.StepInputs["patch"].ArtifactID != source.PatchID {
		return reject("failed collection does not identify its captured current candidate")
	}
	return r, *source, owner.ID, nil
}

func collectionMeasureProfile(p Profile) bool {
	if p.Harness != "command" || p.Access != "workspace-read" || p.Command == nil {
		return false
	}
	cmd := p.Command
	args := cmd.Args
	return !cmd.Network && cmd.Directory == "" && cmd.Result == "domain.json" && cmd.Artifact == "measurements.json" && len(cmd.ExitOutcomes) == 0 && len(args) == 13 && filepath.IsAbs(args[0]) && filepath.Base(args[0]) == "runner-measure" && filepath.IsAbs(args[2]) &&
		args[1] == "--go" && reflect.DeepEqual(args[3:], []string{
		"--source",
		"{workspace}",
		"--scratch",
		"{scratch}",
		"--package",
		"{text.package}",
		"--benchmark",
		"{text.benchmark}",
		"--previous",
		"{text.previous}",
	})
}

func (c *Core) collectionRecoveryAdmission(r Run) error {
	reserve, err := c.taskCompletionReserve(r)
	if err != nil {
		return err
	}
	if err = c.familyReserve(r, 1, reserve); err != nil {
		return err
	}
	state, _, err := MarshalRunRecord(r)
	if err != nil || len(state)+2048 > int(c.disk.Policy.Database.MaxRecord) {
		return errors.New("collection recovery lacks existing history headroom")
	}
	if validator, ok := c.executor.(AssignmentValidator); ok {
		step := r.Process.Steps[r.Step]
		timeout := r.Process.AttemptTimeout
		if step.AttemptTimeout != "" {
			timeout = step.AttemptTimeout
		}
		duration, err := time.ParseDuration(timeout)
		if err != nil || duration <= 0 {
			return errors.New("collection owner deadline is invalid")
		}
		a, err := c.assignment(r, Attempt{ID: "attempt_" + strings.Repeat("A", 26), Step: r.Step, Profile: r.Profiles[step.Role], Deadline: time.Now().UTC().Add(duration)})
		if err != nil {
			return err
		}
		return validator.ValidateAssignment(a)
	}
	return nil
}
