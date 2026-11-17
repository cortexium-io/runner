package runner

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type RecoverCorrectionsRequest struct {
	RequestID      string                  `json:"request_id"`
	RunID          string                  `json:"run_id"`
	AttemptID      string                  `json:"attempt_id"`
	MaxUses        int                     `json:"max_uses"`
	PreserveSource bool                    `json:"preserve_source,omitempty"`
	WithinBudget   bool                    `json:"within_budget,omitempty"`
	CurrentRoute   bool                    `json:"current_route,omitempty"`
	Revision       int64                   `json:"revision,omitempty"`
	PatchSHA256    string                  `json:"patch_sha256,omitempty"`
	CheckSHA256    string                  `json:"check_sha256,omitempty"`
	Context        *CorrectionContextInput `json:"context,omitempty"`
}

// CorrectionRecovery is a single owner-authorized operational escalation. The
// frozen process, cumulative route uses and all original attempt limits remain
// unchanged. Legacy recovery permits three total uses per recorded backedge;
// budget mode extends only the current backedge while protecting completion.
type CorrectionRecovery struct {
	RequestID         string                     `json:"request_id"`
	AttemptID         string                     `json:"attempt_id"`
	Reason            string                     `json:"reason"`
	MaxUses           int                        `json:"max_uses"`
	RouteLimits       map[string]int             `json:"route_limits"`
	PatchID           string                     `json:"patch_id"`
	GrantedAt         time.Time                  `json:"granted_at"`
	Preservation      *SourcePreservationReceipt `json:"preservation,omitempty"`
	WithinBudget      bool                       `json:"within_budget,omitempty"`
	RemainingAttempts int                        `json:"remaining_attempts,omitempty"`
	CurrentRoute      bool                       `json:"current_route,omitempty"`
	Revision          int64                      `json:"revision,omitempty"`
	Route             string                     `json:"route,omitempty"`
	PreviousUses      int                        `json:"previous_uses,omitempty"`
	PatchSHA256       string                     `json:"patch_sha256,omitempty"`
	CheckID           string                     `json:"check_id,omitempty"`
	CheckSHA256       string                     `json:"check_sha256,omitempty"`
	DefinitionSHA256  string                     `json:"definition_sha256,omitempty"`
	Context           *CorrectionContext         `json:"context,omitempty"`
	FromVisit         int                        `json:"from_visit,omitempty"`
}

type SourcePreservationReceipt struct {
	Path           string `json:"path"`
	ManifestSHA256 string `json:"manifest_sha256"`
	PatchSHA256    string `json:"patch_sha256"`
}

// SourceRecoveryPreparer is invoked only by the explicit preserve_source owner
// action. It preserves an authenticated owned checkout, never its unaccepted
// changes, before normal source preparation may restore the retained candidate.
type SourceRecoveryPreparer interface {
	PrepareSourceRecovery(context.Context, Run, SourceResult) (SourcePreservationReceipt, error)
}

// RecoverCorrections routes an already validated, returned source rejection.
// It never repeats the rejected check, changes a definition, or grants attempts.
func (c *Core) RecoverCorrections(req RecoverCorrectionsRequest) (Run, error) {
	req = Clone(req)
	args, err := correctionRecoveryArgs(req)
	if err != nil {
		return Run{}, err
	}
	c.mu.Lock()
	if r, ok, err := c.replay(req.RequestID, "recover_corrections", args); ok || err != nil {
		if ok && err == nil {
			r, err = c.reconcileCorrectionReplay(r, req)
		}
		c.mu.Unlock()
		return r, err
	}
	r, source, limits, err := c.correctionRecovery(req)
	validator, supported := c.executor.(SourceRecoveryValidator)
	preparer, canPreserve := c.executor.(SourceRecoveryPreparer)
	c.mu.Unlock()
	if err != nil {
		return Run{}, err
	}
	if !supported || req.PreserveSource && !canPreserve {
		return Run{}, errors.New("executor cannot validate retained source recovery")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	var preservation *SourcePreservationReceipt
	if req.PreserveSource {
		var proof SourcePreservationReceipt
		proof, err = preparer.PrepareSourceRecovery(ctx, Clone(r), Clone(source))
		if err == nil && (proof.Path == "" || proof.ManifestSHA256 == "" || proof.PatchSHA256 == "") {
			err = errors.New("source preservation returned incomplete evidence")
		}
		preservation = &proof
	} else {
		err = validator.ValidateSourceRecovery(ctx, Clone(r), Clone(source))
	}
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	// Source validation runs without holding the coordinator lock. Cancellation,
	// another control action or cleanup changes make this snapshot stale.
	if replay, ok, err := c.replay(req.RequestID, "recover_corrections", args); ok || err != nil {
		if ok && err == nil {
			replay, err = c.reconcileCorrectionReplay(replay, req)
		}
		return replay, err
	}
	if err != nil {
		return Run{}, fmt.Errorf("retained source recovery: %w", err)
	}
	if current, ok := c.runs[r.ID]; !ok || current.Revision != r.Revision {
		return Run{}, errors.New("run changed during source validation; inspect before recovery")
	}
	// Recheck the public family revision and all candidate/check/context bindings
	// after source validation. A child's change need not change the private root.
	if _, _, _, err := c.correctionRecovery(req); err != nil {
		return Run{}, err
	}
	r.CorrectionRecovery = &CorrectionRecovery{
		RequestID:    req.RequestID,
		AttemptID:    req.AttemptID,
		Reason:       r.Reason,
		MaxUses:      req.MaxUses,
		RouteLimits:  limits,
		PatchID:      source.PatchID,
		GrantedAt:    time.Now().UTC(),
		Preservation: preservation,
		WithinBudget: req.WithinBudget,
		CurrentRoute: req.CurrentRoute,
	}
	var artifacts []Artifact
	if req.CurrentRoute {
		grant := r.CorrectionRecovery
		grant.Revision = req.Revision
		grant.Route = r.Process.Steps[r.Step].ID + "/changes_requested"
		grant.PreviousUses = r.RouteUses[grant.Route]
		grant.PatchSHA256, grant.CheckSHA256 = req.PatchSHA256, req.CheckSHA256
		grant.CheckID = r.Current().End.Result.Outputs["artifact"].ArtifactID
		_, definition, err := MarshalRunRecord(r)
		if err != nil {
			return Run{}, err
		}
		grant.DefinitionSHA256 = correctionDigest(definition)
		if req.Context != nil {
			grant.Context = &CorrectionContext{
				ID:     r.ID + "/correction-context-" + req.Context.SHA256 + ".json",
				Bytes:  req.Context.Bytes,
				SHA256: req.Context.SHA256,
				Roles:  Clone(req.Context.Roles),
			}
			if err := c.disk.Write("artifacts/"+grant.Context.ID, []byte(req.Context.Content), c.disk.Policy.MaxArtifact); err != nil {
				return Run{}, err
			}
			artifacts = append(artifacts, Artifact{ID: grant.Context.ID, RunID: r.ID, ProjectID: r.ProjectID, Bytes: grant.Context.Bytes})
		}
	}
	if req.WithinBudget {
		r.CorrectionRecovery.RemainingAttempts = c.familyRemaining(r)
	}
	c.route(&r, r.Current())
	if r.Status != "Queued" {
		return Run{}, fmt.Errorf("saved correction cannot advance: %s", r.Reason)
	}
	if req.CurrentRoute {
		r.CorrectionRecovery.FromVisit = r.Visit
	}
	r.ResourcesReleased = false // Normal owner preparation validates/restores source.
	if req.PreserveSource {
		r.CleanupBlocked, r.CleanupError = false, ""
	}
	receipt := Receipt{Key: req.RequestID, Kind: "recover_corrections", Args: args, RunID: r.ID}
	if err := c.persist(r, artifacts, &receipt); err != nil {
		if replay, committed, replayErr := c.replay(req.RequestID, "recover_corrections", args); committed && replayErr == nil {
			return c.reconcileCorrectionReplay(replay, req)
		}
		return Run{}, err
	}
	c.Wake()
	if req.CurrentRoute {
		return c.outputRun(c.runs[r.ID]), nil
	}
	return Clone(r), nil
}

func (c *Core) correctionRecovery(req RecoverCorrectionsRequest) (Run, SourceResult, map[string]int, error) {
	r, ok := c.runs[req.RunID]
	if !ok {
		return Run{}, SourceResult{}, nil, ErrNotFound
	}
	if err := c.taskContinuationFrozen(r); err != nil {
		return Run{}, SourceResult{}, nil, err
	}
	r = Clone(r)
	reject := func(reason string) (Run, SourceResult, map[string]int, error) {
		return Run{}, SourceResult{}, nil, errors.New(reason)
	}
	if req.CurrentRoute {
		if req.WithinBudget || req.PreserveSource || req.Revision < 1 || req.Revision != c.outputRun(r).Revision || !validCorrectionSHA(req.PatchSHA256) || !validCorrectionSHA(req.CheckSHA256) {
			return reject("current_route requires exact observed family revision, patch/check SHA256 and settled source, without other recovery modes")
		}
	} else if req.Revision != 0 || req.PatchSHA256 != "" || req.CheckSHA256 != "" || req.Context != nil {
		return reject("exact correction bindings and supplemental context require current_route")
	} else if req.WithinBudget {
		if req.MaxUses != 0 || req.PreserveSource || r.Task != nil {
			return reject("within_budget requires settled root recovery without max_uses or preserve_source")
		}
		if !c.workLimitsEnabled() {
			return reject("within_budget correction recovery needs an enforced aggregate allowance; use the bounded local correction grant")
		}
	} else if req.MaxUses != 3 {
		return reject("max_uses must be three total uses per source-correction route")
	}
	if r.Status != "Failed" || r.CancelRequested || r.Active() || r.Decision != nil || r.CorrectionRecovery != nil || c.inFlight[r.ID] {
		return reject("correction recovery needs an ungranted settled failed run without cancellation or approval")
	}
	if r.Step < 0 || r.Step >= len(r.Process.Steps) {
		return reject("saved correction step is unavailable")
	}
	a := r.Current()
	if a == nil || a.ID != req.AttemptID || a.State != Finished || a.Superseded || a.StopRequested || a.TimedOut || a.End == nil || a.End.Kind != "returned" || a.End.Retryable || a.End.Result == nil || a.End.Result.Outcome != "changes_requested" {
		return reject("correction recovery needs the exact saved returned source rejection")
	}
	key := r.Process.Steps[r.Step].ID + "/changes_requested"
	route, eligible := sourceCorrectionRoute(r, r.Step)
	if !eligible || !req.WithinBudget && !req.CurrentRoute && route.MaxTimes >= 3 || r.RouteUses[key] < route.MaxTimes || r.Reason != "repetition allowance exhausted: "+key {
		return reject("current failure is not an exhausted eligible source-correction route")
	}
	if req.CurrentRoute && req.MaxUses != r.RouteUses[key]+1 {
		return reject("current_route permits exactly one additional use of only the exhausted route")
	}
	if c.workLimitsEnabled() && len(r.Attempts) >= r.Process.MaxAttempts {
		return reject("original total attempt allowance is exhausted")
	}
	if req.PreserveSource && (!r.CleanupBlocked || r.CleanupError == "") {
		return reject("preserve_source needs an explicitly blocked owned source cleanup")
	}
	if !req.PreserveSource && (r.CleanupBlocked || r.CleanupError != "" || !r.ResourcesReleased) {
		return reject("retained run cleanup must settle before correction recovery")
	}
	for _, old := range r.Attempts {
		if old.State != Finished || !old.Released || c.inFlight[old.ID] {
			return reject("all execution and attempt cleanup must settle before correction recovery")
		}
	}
	role := r.Roles[r.Process.Steps[r.Step].Role]
	profile, profileErr := role.Profile(a.Profile)
	selected, selectedErr := role.Profile(r.Profiles[r.Process.Steps[r.Step].Role])
	if profileErr != nil || selectedErr != nil || !reflect.DeepEqual(profile, selected) {
		return reject("saved source rejection setup differs from the selected profile")
	}
	if err := ValidateValues(a.End.Result.Outputs, role.Outputs); err != nil {
		return reject("saved source rejection does not satisfy its output contract")
	}
	for _, value := range a.End.Result.Outputs {
		if value.ArtifactID != "" {
			if err := c.recoveryArtifact(r, value.ArtifactID); err != nil {
				return reject(err.Error())
			}
		}
	}
	var source *SourceResult
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		old := r.Attempts[i]
		if old.Step < 0 || old.Step >= len(r.Process.Steps) {
			return reject("saved source attempt step is unavailable")
		}
		owner, err := r.Roles[r.Process.Steps[old.Step].Role].Profile(old.Profile)
		if !old.Superseded && err == nil && owner.Harness == "codex" && owner.Access == "workspace-write" {
			if old.Source == nil {
				return reject("latest owner has no retained source handoff")
			}
			if old.Source.AttemptID != old.ID || old.End == nil || old.End.Kind != "returned" || old.End.Result == nil || old.End.Result.Outcome != "ready" || old.TimedOut || old.StopRequested {
				return reject("latest retained source is not a settled accepted owner handoff")
			}
			source = old.Source
			break
		}
	}
	if source == nil || source.PatchID == "" || !strings.HasPrefix(source.PatchID, source.AttemptID+"/") || source.Repository == "" || source.Base == "" || source.Tree == "" {
		return reject("latest retained source candidate is unavailable")
	}
	if err := c.recoveryArtifact(r, source.PatchID); err != nil {
		return reject(err.Error())
	}
	inputs := r.StepInputs
	if inputs == nil {
		var err error
		inputs, err = bindings(r, r.Process.Steps[r.Step].Inputs)
		if err != nil {
			return reject("saved source rejection inputs are unavailable")
		}
	}
	if profile.Harness != "release" && inputs["patch"].ArtifactID != source.PatchID {
		return reject("source rejection does not identify the latest retained candidate")
	}
	if a.Source != nil && (a.Source.InputPatchID != source.PatchID || a.Source.Repository != source.Repository || a.Source.Base != source.Base || a.Source.WorkDir != source.WorkDir) {
		return reject("source rejection provenance differs from the latest retained candidate")
	}
	limits := map[string]int{}
	if req.CurrentRoute {
		if err := c.correctionArtifactDigest(r, source.PatchID, req.PatchSHA256); err != nil {
			return reject(err.Error())
		}
		checkID := a.End.Result.Outputs["artifact"].ArtifactID
		if checkID == "" {
			return reject("current_route needs its saved check artifact")
		}
		if err := c.correctionArtifactDigest(r, checkID, req.CheckSHA256); err != nil {
			return reject(err.Error())
		}
		if req.Context != nil {
			if err := validateCorrectionContext(r, req.Context, c.disk.Policy.MaxArtifact); err != nil {
				return reject(err.Error())
			}
		}
		limits[key] = req.MaxUses
		return r, *source, limits, nil
	}
	if req.WithinBudget {
		if err := c.correctionBudgetReserve(r, taskStepIndex(r.Process, route.Next)); err != nil {
			return reject(err.Error())
		}
		limits[key] = r.RouteUses[key] + c.familyRemaining(r)
		return r, *source, limits, nil
	}
	for i, step := range r.Process.Steps {
		if route, ok := sourceCorrectionRoute(r, i); ok && route.MaxTimes < 3 {
			limits[step.ID+"/changes_requested"] = 3
		}
	}
	return r, *source, limits, nil
}

func (c *Core) recoveryArtifact(r Run, id string) error {
	artifact, err := c.store.Artifact(c.ctx, id)
	if err != nil || artifact.RunID != r.ID || artifact.ProjectID != r.ProjectID {
		return errors.New("retained correction artifact ownership is unavailable or differs")
	}
	size, err := c.disk.ArtifactSize(id)
	if err != nil || size != artifact.Bytes {
		return errors.New("retained correction artifact is missing or changed")
	}
	return nil
}

// Only the configured nonapproval source-rejection edge may receive the grant.
// Release effects, infrastructure retries, loops and arbitrary outcomes cannot.
func sourceCorrectionRoute(r Run, index int) (Route, bool) {
	if index < 0 || index >= len(r.Process.Steps) {
		return Route{}, false
	}
	step := r.Process.Steps[index]
	route, ok := step.Routes["changes_requested"]
	if !ok || route.Approval || route.MaxTimes < 1 || route.MaxTimes > 3 || step.ForEach != nil || step.Escalation != nil {
		return Route{}, false
	}
	profile, err := r.Roles[step.Role].Profile(r.Profiles[step.Role])
	if err != nil {
		return Route{}, false
	}
	allowed := profile.Harness == "release" && profile.Mode == "qualify"
	if (profile.Harness == "codex" || profile.Harness == "command") && profile.Access == "workspace-read" && step.Inputs["patch"].FromCandidate {
		allowed = profile.Harness == "codex" || profile.Command != nil && !profile.Command.Network
	}
	if !allowed {
		return Route{}, false
	}
	for i := 0; i < index; i++ {
		owner := r.Process.Steps[i]
		if owner.ID == route.Next {
			profile, err := r.Roles[owner.Role].Profile(r.Profiles[owner.Role])
			return route, err == nil && profile.Harness == "codex" && profile.Access == "workspace-write" && owner.ForEach == nil
		}
	}
	return Route{}, false
}

func routeLimit(r Run, index int, outcome string, route Route) int {
	if outcome == "changes_requested" && r.CorrectionRecovery != nil {
		key := r.Process.Steps[index].ID + "/" + outcome
		if recovery := r.CorrectionRecovery; recovery.CurrentRoute && !recovery.WithinBudget && recovery.Route == key && recovery.MaxUses == recovery.PreviousUses+1 && recovery.RouteLimits[key] == recovery.MaxUses {
			if _, ok := sourceCorrectionRoute(r, index); ok {
				return recovery.MaxUses
			}
		}
		if recovery := r.CorrectionRecovery; recovery.WithinBudget && recovery.MaxUses == 0 {
			if limit := recovery.RouteLimits[key]; limit > 0 && limit <= r.Process.MaxAttempts {
				if _, ok := sourceCorrectionRoute(r, index); ok {
					return limit
				}
			}
		}
		if r.CorrectionRecovery.MaxUses == 3 && r.CorrectionRecovery.RouteLimits[key] == 3 {
			if _, ok := sourceCorrectionRoute(r, index); ok {
				return 3
			}
		}
	}
	return route.MaxTimes
}

func (c *Core) correctionBudgetReserve(r Run, ownerIndex int) error {
	need, err := readyTail(r, ownerIndex)
	if err != nil {
		return err
	}
	return c.familyReserve(r, 1, need-1)
}
