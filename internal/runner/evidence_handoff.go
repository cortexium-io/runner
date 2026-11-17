package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// RecoverEvidenceHandoffRequest admits an already retained evidence archive to a
// blocked source owner. It spends existing correction credit, never grants it.
type RecoverEvidenceHandoffRequest struct {
	RequestID       string            `json:"request_id"`
	RunID           string            `json:"run_id"`
	Revision        int64             `json:"revision"`
	PhaseRequestID  string            `json:"phase_request_id"`
	AttemptID       string            `json:"attempt_id"`
	SourceAttemptID string            `json:"source_attempt_id"`
	PatchSHA256     string            `json:"patch_sha256"`
	CheckAttemptID  string            `json:"check_attempt_id"`
	CheckSHA256     string            `json:"check_sha256"`
	Evidence        CorrectionContext `json:"evidence"`
}

type EvidenceHandoff struct {
	Request   RecoverEvidenceHandoffRequest `json:"request"`
	GrantedAt time.Time                     `json:"granted_at"`
	FromVisit int                           `json:"from_visit"`
	Previous  *EvidenceHandoff              `json:"previous,omitempty"`
}

// EvidenceHandoffValidator authenticates the archive's format and its reference
// in the saved check. Core owns attempt, source and artifact ownership checks.
type EvidenceHandoffValidator interface {
	ValidateEvidenceHandoff(context.Context, Run, RecoverEvidenceHandoffRequest) error
}

func (c *Core) RecoverEvidenceHandoff(req RecoverEvidenceHandoffRequest) (Run, error) {
	req = Clone(req)
	args, _ := json.Marshal(req)
	c.mu.Lock()
	if r, ok, err := c.replayEvidenceHandoff(req, args); ok || err != nil {
		c.mu.Unlock()
		return r, err
	}
	r, check, err := c.evidenceHandoff(req)
	sourceValidator, sourceOK := c.executor.(SourceRecoveryValidator)
	evidenceValidator, evidenceOK := c.executor.(EvidenceHandoffValidator)
	c.mu.Unlock()
	if err != nil {
		return Run{}, err
	}
	if !sourceOK || !evidenceOK {
		return Run{}, errors.New("executor cannot validate retained evidence handoff")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	// The blocked owner has a real capture. Validate that exact workspace identity;
	// Core separately proves it has the accepted candidate's unchanged bytes/tree.
	err = sourceValidator.ValidateSourceRecovery(ctx, Clone(r), Clone(*r.Current().Source))
	if err == nil {
		err = evidenceValidator.ValidateEvidenceHandoff(ctx, Clone(r), req)
	}
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if replay, ok, replayErr := c.replayEvidenceHandoff(req, args); ok || replayErr != nil {
		return replay, replayErr
	}
	if err != nil {
		return Run{}, fmt.Errorf("retained evidence handoff: %w", err)
	}
	if current, ok := c.runs[r.ID]; !ok || current.Revision != r.Revision {
		return Run{}, errors.New("run changed during evidence validation")
	}
	r, check, err = c.evidenceHandoff(req)
	if err != nil {
		return Run{}, err
	}
	inputs := Clone(r.StepInputs)
	// Re-enter the saved nonapproval correction edge using its original result.
	// A new owner visit spends the phase's next route use and resets no spending.
	// Its inputs stay exactly those frozen before the blocked owner executed.
	r.Step = check.Step
	route, _ := sourceCorrectionRoute(r, check.Step)
	c.advanceInputs(&r, check.End.Result, route, inputs)
	if r.Status != "Queued" {
		return Run{}, fmt.Errorf("saved correction cannot advance: %s", r.Reason)
	}
	r.CorrectionRecovery.EvidenceHandoff = &EvidenceHandoff{
		Request:   req,
		GrantedAt: time.Now().UTC(),
		FromVisit: r.Visit,
		Previous:  r.CorrectionRecovery.EvidenceHandoff,
	}
	r.ResourcesReleased = false
	receipt := Receipt{Key: req.RequestID, Kind: "recover_evidence_handoff", Args: args, RunID: r.ID}
	if err := c.persist(r, nil, &receipt); err != nil {
		if replay, ok, replayErr := c.replayEvidenceHandoff(req, args); ok && replayErr == nil {
			return replay, nil
		}
		return Run{}, err
	}
	c.Wake()
	return c.outputRun(c.runs[r.ID]), nil
}

func (c *Core) replayEvidenceHandoff(req RecoverEvidenceHandoffRequest, args []byte) (Run, bool, error) {
	r, ok, err := c.replay(req.RequestID, "recover_evidence_handoff", args)
	if err != nil {
		return r, ok, err
	}
	rows, err := c.store.Load(c.ctx)
	if err != nil {
		return Run{}, true, fmt.Errorf("committed evidence handoff needs reconciliation: %w", err)
	}
	for _, saved := range rows {
		if saved.ID != req.RunID {
			continue
		}
		if !ok {
			if current, exists := c.runs[saved.ID]; exists && saved.Revision > current.Revision {
				c.runs[saved.ID] = saved
				c.maintenanceError = ""
				close(c.changed)
				c.changed = make(chan struct{})
				c.Wake()
			}
			return Run{}, false, nil
		}
		phase := correctionGrant(saved.CorrectionRecovery, req.PhaseRequestID)
		if phase != nil {
			for audit := phase.EvidenceHandoff; audit != nil; audit = audit.Previous {
				if reflect.DeepEqual(audit.Request, req) && saved.Revision >= r.Revision {
					if saved.Revision > r.Revision {
						c.runs[saved.ID] = saved
						c.maintenanceError = ""
						close(c.changed)
						c.changed = make(chan struct{})
						c.Wake()
					}
					return c.outputRun(saved), true, nil
				}
			}
		}
		return Run{}, true, errors.New("committed evidence handoff differs from its receipt")
	}
	if !ok {
		return Run{}, false, nil
	}
	return Run{}, true, errors.New("committed evidence handoff is unavailable; reopen the same state")
}

func settledHandoffOutcome(a Attempt, outcome string) bool {
	return a.State == Finished && a.Released && !a.TimedOut && !a.StopRequested && a.End != nil && a.End.Kind == "returned" && !a.End.Retryable && a.End.Result != nil && a.End.Result.Outcome == outcome
}

func (c *Core) evidenceHandoff(req RecoverEvidenceHandoffRequest) (Run, Attempt, error) {
	r, ok := c.runs[req.RunID]
	if !ok {
		return Run{}, Attempt{}, ErrNotFound
	}
	if err := c.taskContinuationFrozen(r); err != nil {
		return Run{}, Attempt{}, err
	}
	r = Clone(r)
	reject := func(message string) (Run, Attempt, error) { return Run{}, Attempt{}, errors.New(message) }
	if req.Revision < 1 || req.Revision != c.outputRun(r).Revision || r.Task != nil || r.Status != "Failed" || r.CancelRequested || r.Active() || r.Decision != nil || c.inFlight[r.ID] || r.CleanupBlocked || r.CleanupError != "" || !r.ResourcesReleased {
		return reject("evidence handoff requires the exact settled failed root revision without cancellation or cleanup")
	}
	if r.Step < 0 || r.Step >= len(r.Process.Steps) || len(r.Attempts) < 3 {
		return reject("saved source/check history is unavailable")
	}
	for _, a := range r.Attempts {
		if a.State != Finished || !a.Released || c.inFlight[a.ID] {
			return reject("all attempts must settle before evidence handoff")
		}
	}
	blocked := r.Current()
	if blocked == nil || blocked.ID != req.AttemptID || blocked.Superseded || !settledHandoffOutcome(*blocked, "blocked") || Clip(blocked.End.Result.Reason) != r.Reason || blocked.Source == nil || r.StepInputs == nil {
		return reject("evidence handoff needs the exact returned blocked source owner")
	}
	profile, err := r.Roles[r.Process.Steps[r.Step].Role].Profile(r.Profiles[r.Process.Steps[r.Step].Role])
	actual, actualErr := r.Roles[r.Process.Steps[r.Step].Role].Profile(blocked.Profile)
	if err != nil || actualErr != nil || !reflect.DeepEqual(profile, actual) || profile.Harness != "codex" || profile.Access != "workspace-write" || r.Process.Steps[r.Step].Routes["blocked"] != (Route{Next: "$fail"}) || ValidateValues(blocked.End.Result.Outputs, r.Roles[r.Process.Steps[r.Step].Role].Outputs) != nil {
		return reject("blocked source owner/profile/output contract differs")
	}
	phase := r.CorrectionRecovery
	if phase == nil || phase.RequestID != req.PhaseRequestID || !phase.CurrentRoute || phase.WithinBudget || !validCorrectionUseCeiling(phase) || len(phase.RouteLimits) != 1 || phase.RouteLimits[phase.Route] != phase.MaxUses || r.Visit < phase.FromVisit || r.RouteUses[phase.Route] <= phase.PreviousUses || r.RouteUses[phase.Route] >= phase.MaxUses {
		return reject("exact active correction phase has no remaining route use")
	}
	if c.workLimitsEnabled() && len(r.Attempts) >= r.Process.MaxAttempts {
		return reject("original total attempt allowance is exhausted")
	}
	if c.workLimitsEnabled() {
		if err := c.correctionBudgetReserve(r, r.Step); err != nil {
			return reject("evidence handoff preserves completion reserve: " + err.Error())
		}
	}
	// The final execution before the blocked owner must be its original rejection.
	check := r.Attempts[len(r.Attempts)-2]
	route, eligible := sourceCorrectionRoute(r, check.Step)
	if check.ID != req.CheckAttemptID || !settledHandoffOutcome(check, "changes_requested") || !eligible || route.Next != r.Process.Steps[r.Step].ID || check.Visit+1 != blocked.Visit || r.Process.Steps[check.Step].ID+"/changes_requested" != phase.Route || check.Source == nil {
		return reject("saved check is not the blocked owner's immediately preceding source rejection")
	}
	checkRole := r.Roles[r.Process.Steps[check.Step].Role]
	checkProfile, checkErr := checkRole.Profile(check.Profile)
	selectedCheck, selectedErr := checkRole.Profile(r.Profiles[r.Process.Steps[check.Step].Role])
	if checkErr != nil || selectedErr != nil || !reflect.DeepEqual(checkProfile, selectedCheck) || ValidateValues(check.End.Result.Outputs, checkRole.Outputs) != nil {
		return reject("saved check profile/output contract differs")
	}
	var accepted *Attempt
	for i := len(r.Attempts) - 3; i >= 0; i-- {
		a := &r.Attempts[i]
		if a.Step < 0 || a.Step >= len(r.Process.Steps) {
			return reject("saved source step is unavailable")
		}
		p, err := r.Roles[r.Process.Steps[a.Step].Role].Profile(a.Profile)
		if err == nil && p.Harness == "codex" && p.Access == "workspace-write" {
			accepted = a
			break
		}
	}
	if accepted == nil || accepted.ID != req.SourceAttemptID || !settledHandoffOutcome(*accepted, "ready") || accepted.Source == nil || accepted.Step != blocked.Step || accepted.Profile != blocked.Profile {
		return reject("latest accepted owner source is unavailable")
	}
	source, capture, observed := accepted.Source, blocked.Source, check.Source
	if blocked.End.Result.Outputs["patch"].ArtifactID != capture.PatchID || accepted.End.Result.Outputs["patch"].ArtifactID != source.PatchID || source.AttemptID != accepted.ID || source.PatchID != accepted.ID+"/changes.patch" || source.Repository != r.Repository || source.Base == "" || source.Tree == "" || source.WorkDir == "" || capture.AttemptID != blocked.ID || capture.PatchID != blocked.ID+"/changes.patch" || capture.InputPatchID != source.PatchID || capture.Repository != source.Repository || capture.Base != source.Base || capture.Tree != source.Tree || capture.WorkDir != source.WorkDir || observed.AttemptID != check.ID || observed.InputPatchID != source.PatchID && !historicalSharedSource(r, check) || observed.Repository != source.Repository || observed.Base != source.Base || observed.WorkDir != source.WorkDir || observed.Tree != "" && observed.Tree != source.Tree {
		return reject("blocked source differs from the accepted candidate or saved check provenance")
	}
	for _, id := range []string{source.PatchID, capture.PatchID} {
		if !validCorrectionSHA(req.PatchSHA256) {
			return reject("exact candidate patch SHA256 is required")
		}
		if err := c.correctionArtifactDigest(r, id, req.PatchSHA256); err != nil {
			return reject(err.Error())
		}
	}
	checkID := check.End.Result.Outputs["artifact"].ArtifactID
	foundFeedback := false
	for name, b := range r.Process.Steps[r.Step].Inputs {
		if b.FromStep == r.Process.Steps[check.Step].ID && b.Output == "artifact" && b.Previous && r.StepInputs[name].ArtifactID == checkID {
			foundFeedback = true
		}
	}
	if !foundFeedback {
		return reject("blocked owner does not retain the saved check artifact")
	}
	if checkID != check.ID+"/result" || !validCorrectionSHA(req.CheckSHA256) {
		return reject("exact prior check artifact SHA256 is required")
	}
	if err := c.correctionArtifactDigest(r, checkID, req.CheckSHA256); err != nil {
		return reject(err.Error())
	}
	// Existing declared root inputs may include a reference from another run of
	// this project. They remain declared inputs, never a directory-wide grant.
	for _, input := range r.Inputs {
		if input.ArtifactID == "" {
			continue
		}
		artifact, err := c.store.Artifact(c.ctx, input.ArtifactID)
		size, sizeErr := c.disk.ArtifactSize(input.ArtifactID)
		if err != nil || sizeErr != nil || artifact.ProjectID != r.ProjectID || artifact.Bytes != size {
			return reject("declared root input artifact ownership differs")
		}
	}
	proof := req.Evidence
	if proof.ID == "" || proof.Bytes < 1 || proof.Bytes > int64(c.disk.Policy.MaxArtifact) || !validCorrectionSHA(proof.SHA256) {
		return reject("exact bounded retained evidence identity is required")
	}
	if err := validateCorrectionContext(r, &CorrectionContextInput{Bytes: proof.Bytes, Roles: proof.Roles}, c.disk.Policy.MaxArtifact); err != nil {
		return reject(err.Error())
	}
	if !slices.Contains(proof.Roles, r.Process.Steps[r.Step].Role) {
		return reject("evidence recipients must include the blocked source owner")
	}
	if err := c.correctionArtifactDigest(r, proof.ID, proof.SHA256); err != nil {
		return reject(err.Error())
	}
	artifact, _ := c.store.Artifact(c.ctx, proof.ID)
	if artifact.Bytes != proof.Bytes {
		return reject("retained evidence size differs")
	}
	producer := false
	for _, a := range r.Attempts {
		if a.ID+"/result" != proof.ID {
			continue
		}
		if a.Step < 0 || a.Step >= len(r.Process.Steps) {
			return reject("retained evidence producer step is unavailable")
		}
		role := r.Roles[r.Process.Steps[a.Step].Role]
		profile, profileErr := role.Profile(a.Profile)
		selected, selectedErr := role.Profile(r.Profiles[r.Process.Steps[a.Step].Role])
		if profileErr != nil || selectedErr != nil || !reflect.DeepEqual(profile, selected) || profile.Access != "workspace-read" {
			return reject("retained evidence producer profile differs from the selected read-only role")
		}
		if a.Visit >= check.Visit || a.Visit <= accepted.Visit || a.Source == nil || a.Source.AttemptID != a.ID || a.Source.InputPatchID != source.PatchID && !historicalSharedSource(r, a) || a.Source.Repository != source.Repository || a.Source.Base != source.Base || a.Source.WorkDir != source.WorkDir || a.Source.Tree != "" && a.Source.Tree != source.Tree || a.End == nil || a.End.Result == nil || !settledHandoffOutcome(a, a.End.Result.Outcome) || a.End.Result.Outputs["artifact"].ArtifactID != proof.ID {
			return reject("retained evidence producer differs from the saved candidate")
		}
		if !slices.Contains([]string{"ready", "changes_requested", "blocked"}, a.End.Result.Outcome) || ValidateValues(a.End.Result.Outputs, role.Outputs) != nil {
			return reject("retained evidence producer output contract differs")
		}
		producer = true
	}
	if !producer {
		return reject("retained evidence lacks its same-root producer")
	}
	return r, check, nil
}

func (c *Core) evidenceHandoffAssignment(r Run, a Attempt) (*CorrectionContext, error) {
	phase := r.CorrectionRecovery
	if phase == nil {
		return nil, nil
	}
	for audit := phase.EvidenceHandoff; audit != nil; audit = audit.Previous {
		proof := audit.Request.Evidence
		if a.Visit < audit.FromVisit || !slices.Contains(proof.Roles, r.Process.Steps[a.Step].Role) {
			continue
		}
		if err := c.correctionArtifactDigest(r, proof.ID, proof.SHA256); err != nil {
			return nil, err
		}
		artifact, err := c.store.Artifact(c.ctx, proof.ID)
		if err != nil || artifact.Bytes != proof.Bytes {
			return nil, errors.New("admitted evidence size differs")
		}
		return Clone(&proof), nil
	}
	return nil, nil
}

// Older shared-command observations omitted an input stamp. Only their frozen
// FromCandidate contract may supply that one missing field; archive digests are
// independently checked by the execution validator.
func historicalSharedSource(r Run, a Attempt) bool {
	if a.Source == nil || a.Source.InputPatchID != "" || a.Step < 0 || a.Step >= len(r.Process.Steps) {
		return false
	}
	step := r.Process.Steps[a.Step]
	p, err := r.Roles[step.Role].Profile(a.Profile)
	return err == nil && p.Harness == "command" && p.Mode == "shared" && p.Access == "workspace-read" && p.Command != nil && step.Inputs["patch"] == (Binding{FromCandidate: true})
}
