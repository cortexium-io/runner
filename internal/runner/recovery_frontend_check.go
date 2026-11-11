package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type RecoverFrontendCheckRequest struct {
	RequestID       string `json:"request_id"`
	RunID           string `json:"run_id"`
	AttemptID       string `json:"attempt_id"`
	Revision        int64  `json:"revision"`
	PatchSHA256     string `json:"patch_sha256"`
	HelperSHA256    string `json:"helper_sha256"`
	PublicationHead string `json:"publication_head"`
	Serial          bool   `json:"serial,omitempty"`
}

// FrontendCheckRecovery is a single owner-authorized handoff from a settled
// blocked correction owner to the existing check visit. It grants no attempts.
type FrontendCheckRecovery struct {
	RequestID       string    `json:"request_id"`
	AttemptID       string    `json:"attempt_id"`
	CheckStep       string    `json:"check_step"`
	CheckVisit      int       `json:"check_visit"`
	PatchID         string    `json:"patch_id"`
	PatchSHA256     string    `json:"patch_sha256"`
	PriorCheckID    string    `json:"prior_check_id"`
	HelperSHA256    string    `json:"helper_sha256"`
	PublicationHead string    `json:"publication_head"`
	GrantedAt       time.Time `json:"granted_at"`
	Serial          bool      `json:"serial,omitempty"`
}

type RecoverFrontendTimeoutsRequest struct {
	RecoverFrontendCheckRequest
	CheckAttemptID string `json:"check_attempt_id"`
	CheckSHA256    string `json:"check_sha256"`
}

// FrontendTimeoutRecovery binds a single diagnostic check and, only if its
// evidence is complete, a single distinct correction visit within the cap.
type FrontendTimeoutRecovery struct {
	RequestID           string    `json:"request_id"`
	OwnerAttemptID      string    `json:"owner_attempt_id"`
	PriorCheckAttemptID string    `json:"prior_check_attempt_id"`
	PriorCheckID        string    `json:"prior_check_id"`
	PriorCheckSHA256    string    `json:"prior_check_sha256"`
	CheckStep           string    `json:"check_step"`
	CheckVisit          int       `json:"check_visit"`
	PatchID             string    `json:"patch_id"`
	PatchSHA256         string    `json:"patch_sha256"`
	HelperSHA256        string    `json:"helper_sha256"`
	PublicationHead     string    `json:"publication_head"`
	GrantedAt           time.Time `json:"granted_at"`
	RepairVisit         int       `json:"repair_visit,omitempty"`
	RepairEvidenceID    string    `json:"repair_evidence_id,omitempty"`
	RepairEvidenceSHA   string    `json:"repair_evidence_sha256,omitempty"`
}

// FrontendCheckRecoveryValidator establishes retained capture/lease ownership,
// the fixed helper digest and the authenticated publication without mutation.
type FrontendCheckRecoveryValidator interface {
	ValidateFrontendCheckRecovery(ctx context.Context, run Run, source SourceResult, check Profile, helperSHA256, publicationHead string) error
}

type frontendCheckRecoveryPlan struct {
	run        Run
	source     SourceResult
	check      Profile
	priorCheck string
	checkStep  string
	tests      Value
	revisions  map[string]int64
}

func (c *Core) RecoverFrontendCheck(req RecoverFrontendCheckRequest) (Run, error) {
	return c.recoverFrontendCheck(req, nil)
}

func (c *Core) RecoverFrontendTimeouts(req RecoverFrontendTimeoutsRequest) (Run, error) {
	return c.recoverFrontendCheck(req.RecoverFrontendCheckRequest, &req)
}

func (c *Core) recoverFrontendCheck(req RecoverFrontendCheckRequest, timeout *RecoverFrontendTimeoutsRequest) (Run, error) {
	operation := "recover_frontend_check"
	request := any(req)
	if timeout != nil {
		operation, request = "recover_frontend_timeouts", timeout
	}
	args, _ := json.Marshal(request)
	c.mu.Lock()
	if r, ok, err := c.replay(req.RequestID, operation, args); ok || err != nil {
		c.mu.Unlock()
		return r, err
	}
	plan, err := c.planFrontendCheckRecovery(req, timeout)
	validator, supported := c.executor.(FrontendCheckRecoveryValidator)
	c.mu.Unlock()
	if err != nil {
		return Run{}, err
	}
	if !supported {
		return Run{}, errors.New("executor cannot validate frontend source and publication recovery")
	}
	patch, err := c.disk.Read("artifacts/"+plan.source.PatchID, c.disk.Policy.MaxArtifact)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(patch)) != req.PatchSHA256 {
		return Run{}, errors.New("retained frontend patch differs from the authorized digest")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	err = validator.ValidateFrontendCheckRecovery(ctx, Clone(plan.run), plan.source, Clone(plan.check), req.HelperSHA256, req.PublicationHead)
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok, replayErr := c.replay(req.RequestID, operation, args); ok || replayErr != nil {
		return r, replayErr
	}
	if err != nil {
		return Run{}, fmt.Errorf("frontend recheck authority: %w", err)
	}
	current, err := c.planFrontendCheckRecovery(req, timeout)
	if err != nil || !maps.Equal(current.revisions, plan.revisions) {
		return Run{}, errors.New("frontend run or accepted cards changed during validation; inspect before recovery")
	}
	r := current.run
	// This is a forward handoff. The blocked owner result and every earlier
	// attempt remain as recorded. Normal check outcomes choose the next step.
	outcome := "frontend_recheck"
	if timeout != nil {
		outcome = "frontend_timeout_diagnostic"
	}
	c.advance(&r, &StepResult{Outcome: outcome}, Route{Next: current.checkStep})
	if r.Status != "Queued" || !reflect.DeepEqual(r.StepInputs, map[string]Value{
		"patch": {ArtifactID: plan.source.PatchID},
		"tests": current.tests,
	}) {
		return Run{}, errors.New("retained frontend check inputs do not bind the blocked owner's patch and original selectors")
	}
	r.ResourcesReleased = false
	grant := FrontendCheckRecovery{
		RequestID:       req.RequestID,
		AttemptID:       req.AttemptID,
		CheckStep:       current.checkStep,
		CheckVisit:      r.Visit,
		PatchID:         plan.source.PatchID,
		PatchSHA256:     req.PatchSHA256,
		PriorCheckID:    plan.priorCheck,
		HelperSHA256:    req.HelperSHA256,
		PublicationHead: req.PublicationHead,
		GrantedAt:       time.Now().UTC(),
		Serial:          req.Serial,
	}
	if timeout == nil {
		r.FrontendCheckRecovery = &grant
	} else {
		r.FrontendTimeoutRecovery = &FrontendTimeoutRecovery{
			RequestID:           grant.RequestID,
			OwnerAttemptID:      grant.AttemptID,
			PriorCheckAttemptID: timeout.CheckAttemptID,
			PriorCheckID:        grant.PriorCheckID,
			PriorCheckSHA256:    timeout.CheckSHA256,
			CheckStep:           grant.CheckStep,
			CheckVisit:          grant.CheckVisit,
			PatchID:             grant.PatchID,
			PatchSHA256:         grant.PatchSHA256,
			HelperSHA256:        grant.HelperSHA256,
			PublicationHead:     grant.PublicationHead,
			GrantedAt:           grant.GrantedAt,
		}
	}
	state, definition, err := MarshalRunRecord(r)
	limit := int(c.disk.Policy.Database.MaxRecord)
	if err != nil || len(state)+2048 > limit || len(definition) > limit {
		return Run{}, errors.New("frontend recovery lacks existing history headroom")
	}
	receipt := Receipt{Key: req.RequestID, Kind: operation, Args: args, RunID: r.ID}
	if err := c.persist(r, nil, &receipt); err != nil {
		return Run{}, err
	}
	c.Wake()
	return Clone(c.runs[r.ID]), nil
}

func (c *Core) planFrontendCheckRecovery(req RecoverFrontendCheckRequest, timeout *RecoverFrontendTimeoutsRequest) (frontendCheckRecoveryPlan, error) {
	var plan frontendCheckRecoveryPlan
	if req.Revision < 1 || !frontendRecheckHex(req.PatchSHA256, 64) || !frontendRecheckHex(req.HelperSHA256, 64) || !frontendRecheckHex(req.PublicationHead, 40) {
		return plan, errors.New("frontend recovery requires the observed revision and exact patch, helper and publication identities")
	}
	saved, exists := c.runs[req.RunID]
	if !exists {
		return plan, ErrNotFound
	}
	r := Clone(saved)
	if r.Task != nil || r.Status != "Failed" || r.CancelRequested || r.Active() || r.Decision != nil || c.inFlight[r.ID] || r.CleanupBlocked || r.CleanupError != "" || !r.ResourcesReleased || r.FrontendTimeoutRecovery != nil || (r.FrontendCheckRecovery != nil) != (timeout != nil) {
		return plan, errors.New("frontend recovery needs ungranted settled failed root work and confirmed cleanup")
	}
	if err := c.frontendRecheckSettledAttempts(r); err != nil {
		return plan, err
	}
	if req.Serial {
		if timeout != nil {
			return plan, errors.New("serial frontend recovery cannot request timeout diagnostics")
		}
		return c.planSerialFrontendRecovery(r, req)
	}
	owner := r.Current()
	if len(r.Attempts) < 2 || owner == nil || owner.ID != req.AttemptID || owner.Superseded || owner.TimedOut || owner.StopRequested || owner.End == nil || owner.End.Kind != "returned" || owner.End.Retryable || owner.End.Result == nil || owner.End.Result.Outcome != "blocked" {
		return plan, errors.New("frontend recovery needs the exact saved returned blocked owner")
	}
	reason := Clip(owner.End.Result.Reason)
	if reason == "" {
		reason = "process rejected outcome blocked"
	}
	if r.Reason != reason {
		return plan, errors.New("frontend run did not stop on the saved owner blocked route")
	}
	check, err := frontendRecheckPath(r)
	if err != nil {
		return plan, err
	}
	checkIndex := r.Step + 1
	previous := r.Attempts[len(r.Attempts)-2]
	profile, profileErr := r.Roles[r.Process.Steps[checkIndex].Role].Profile(previous.Profile)
	if previous.Step != checkIndex || previous.Visit+1 != owner.Visit || !previous.Superseded || previous.TimedOut || previous.StopRequested || previous.End == nil || previous.End.Kind != "returned" || previous.End.Retryable || previous.End.Result == nil || previous.End.Result.Outcome != "changes_requested" || profileErr != nil || !reflect.DeepEqual(profile, check) || r.RouteUses["check/changes_requested"] < 1 {
		return plan, errors.New("blocked owner did not follow a retained configured frontend check rejection")
	}
	prior := previous.End.Result.Outputs["artifact"]
	if ValidateValues(previous.End.Result.Outputs, r.Roles[r.Process.Steps[checkIndex].Role].Outputs) != nil || prior.ArtifactID != previous.ID+"/result" || !reflect.DeepEqual(r.StepInputs["checks"], prior) || c.recoveryArtifact(r, prior.ArtifactID) != nil {
		return plan, errors.New("frontend recovery lacks the exact preceding check receipt supplied to its owner")
	}
	source := owner.Source
	if source == nil || source.AttemptID != owner.ID || source.PatchID != owner.ID+"/changes.patch" || source.Repository != r.Repository || source.WorkDir == "" || r.Source == nil || source.Base != r.Source.Ref || !frontendRecheckHex(source.Base, 40) || !frontendRecheckHex(source.Tree, 40) || source.InputPatchID == "" ||
		ValidateValues(owner.End.Result.Outputs, r.Roles[r.Process.Steps[r.Step].Role].Outputs) != nil || owner.End.Result.Outputs["patch"].ArtifactID != source.PatchID || c.recoveryArtifact(r, source.PatchID) != nil || c.recoveryArtifact(r, source.InputPatchID) != nil {
		return plan, errors.New("blocked owner lacks the exact captured frontend patch and source identity")
	}
	if checked := previous.Source; checked == nil || checked.AttemptID != previous.ID || checked.InputPatchID != source.InputPatchID || checked.Repository != source.Repository || checked.Base != source.Base || checked.WorkDir != source.WorkDir {
		return plan, errors.New("preceding check and blocked owner do not share the same retained input source")
	}
	if ValidateValues(map[string]Value{"tests": r.Inputs["tests"]}, map[string]string{"tests": "text"}) != nil {
		return plan, errors.New("frontend recovery requires the original selected tests")
	}
	revisions, err := c.frontendRecheckCards(r)
	if err != nil {
		return plan, err
	}
	var revision int64
	for _, n := range revisions {
		revision += n
	}
	if revision != req.Revision {
		return plan, errors.New("frontend run or accepted card revision differs; inspect before recovery")
	}
	need, err := readyTail(r, checkIndex)
	if err != nil {
		return plan, err
	}
	if timeout != nil {
		if err := c.validatePriorFrontendTimeout(r, *timeout, previous, *source); err != nil {
			return plan, err
		}
		need += 2 // One diagnostic visit and at most one repair before the full ready path.
	}
	if err := c.familyReserve(r, 1, need-1); err != nil {
		return plan, err
	}
	return frontendCheckRecoveryPlan{run: r, source: *source, check: check, priorCheck: prior.ArtifactID, checkStep: "check", tests: r.Inputs["tests"], revisions: revisions}, nil
}

func (c *Core) planSerialFrontendRecovery(r Run, req RecoverFrontendCheckRequest) (frontendCheckRecoveryPlan, error) {
	var plan frontendCheckRecoveryPlan
	if r.Process.ID != "deliver-retained-catalogue" || len(r.TaskGroups) != 0 || r.Revision != req.Revision ||
		r.Step < 1 || r.Step >= len(r.Process.Steps) || len(r.Attempts) < 2 || r.CorrectionRecovery == nil {
		return plan, errors.New("serial frontend recovery requires the exact settled catalogue run after correction recovery")
	}
	step := r.Process.Steps[r.Step]
	failed := r.Current()
	routeKey := step.ID + "/changes_requested"
	route, eligible := sourceCorrectionRoute(r, r.Step)
	if step.ID != "frontend-check" || !eligible || route.Next != "owner" ||
		r.Reason != "repetition allowance exhausted: "+routeKey || r.RouteUses[routeKey] != routeLimit(r, r.Step, "changes_requested", route) ||
		failed == nil || failed.ID != req.AttemptID || failed.Superseded || failed.TimedOut || failed.StopRequested ||
		failed.End == nil || failed.End.Kind != "returned" || failed.End.Retryable || failed.End.Result == nil || failed.End.Result.Outcome != "changes_requested" {
		return plan, errors.New("serial frontend recovery needs the exact check that exhausted its correction route")
	}
	role := r.Roles[step.Role]
	profile, err := releaseBudgetProfile(r, r.Step)
	checkedProfile, checkedErr := role.Profile(failed.Profile)
	tests := r.Inputs["frontend_tests"]
	if err != nil || checkedErr != nil || !reflect.DeepEqual(profile, checkedProfile) || !frontendRecheckProfile(profile) ||
		!reflect.DeepEqual(step.Inputs, map[string]Binding{"patch": {FromCandidate: true}, "tests": {FromInput: "frontend_tests"}}) ||
		ValidateValues(map[string]Value{"tests": tests}, map[string]string{"tests": "text"}) != nil {
		return plan, errors.New("serial frontend recovery needs the frozen frontend checker and its original selectors")
	}
	prior := failed.End.Result.Outputs["artifact"].ArtifactID
	if ValidateValues(failed.End.Result.Outputs, role.Outputs) != nil || prior != failed.ID+"/result" || c.recoveryArtifact(r, prior) != nil {
		return plan, errors.New("serial frontend recovery needs the saved check report")
	}
	owner := r.Attempts[len(r.Attempts)-2]
	source := owner.Source
	if owner.Step != taskStepIndex(r.Process, route.Next) || owner.Superseded || owner.Visit+1 != failed.Visit || owner.End == nil ||
		owner.End.Kind != "returned" || owner.End.Result == nil || owner.End.Result.Outcome != "ready" || source == nil ||
		source.AttemptID != owner.ID || source.PatchID != owner.ID+"/changes.patch" || owner.End.Result.Outputs["patch"].ArtifactID != source.PatchID ||
		source.Repository != r.Repository || source.WorkDir == "" || r.Source == nil || source.Base != r.Source.Ref ||
		!frontendRecheckHex(source.Base, 40) || !frontendRecheckHex(source.Tree, 40) || c.recoveryArtifact(r, source.PatchID) != nil {
		return plan, errors.New("serial frontend recovery needs the preceding retained candidate")
	}
	if check := failed.Source; check == nil || check.AttemptID != failed.ID || check.InputPatchID != source.PatchID ||
		check.Base != source.Base || check.Repository != source.Repository || check.WorkDir != source.WorkDir ||
		!reflect.DeepEqual(r.StepInputs, map[string]Value{"patch": {ArtifactID: source.PatchID}, "tests": tests}) {
		return plan, errors.New("failed frontend check did not use the retained candidate and original selectors")
	}
	need, err := readyTail(r, r.Step)
	if err != nil {
		return plan, err
	}
	if err = c.familyReserve(r, 1, need-1); err != nil {
		return plan, err
	}
	return frontendCheckRecoveryPlan{run: r, source: *source, check: profile, priorCheck: prior, checkStep: step.ID, tests: tests, revisions: map[string]int64{r.ID: r.Revision}}, nil
}

func (c *Core) frontendRecheckSettledAttempts(r Run) error {
	for _, a := range r.Attempts {
		if a.State != Finished || !a.Released || c.inFlight[a.ID] {
			return errors.New("frontend recovery requires settled execution and released attempt resources")
		}
	}
	return nil
}

func (c *Core) frontendRecheckCards(r Run) (map[string]int64, error) {
	group := r.TaskGroups["cards"]
	manifest, ok := stepOutput(r, Binding{FromStep: "join", Output: "manifest"})
	if len(r.TaskGroups) != 1 || group == nil || !group.Done || group.Failure != "" || len(group.Children) == 0 || len(group.Children) != len(group.Items) || !ok || manifest.ArtifactID == "" || !reflect.DeepEqual(r.StepInputs["manifest"], manifest) || c.recoveryArtifact(r, manifest.ArtifactID) != nil {
		return nil, errors.New("frontend recovery requires the settled task group and its retained join manifest")
	}
	revisions := map[string]int64{r.ID: r.Revision}
	for _, entry := range group.Children {
		child, exists := c.runs[entry.RunID]
		_, duplicate := revisions[entry.RunID]
		if !exists || duplicate || child.Task == nil || child.Task.Identity != (TaskIdentity{RootRunID: r.ID, GroupStep: group.StepID, ItemID: entry.ItemID}) || child.Status != "Completed" || child.Active() || child.CancelRequested || child.Decision != nil || !child.ResourcesReleased || child.CleanupBlocked || child.CleanupError != "" || c.inFlight[child.ID] || c.frontendRecheckSettledAttempts(child) != nil || entry.Candidate == nil {
			return nil, errors.New("frontend recovery requires every accepted card and its cleanup to settle")
		}
		var candidate TaskCandidate
		if json.Unmarshal(child.Outputs["result"].Inline, &candidate) != nil || candidate != *entry.Candidate || !c.validTaskCandidate(child, group, candidate) || c.recoveryArtifact(child, candidate.PatchID) != nil {
			return nil, errors.New("accepted frontend card pin or patch ownership differs")
		}
		revisions[child.ID] = child.Revision
	}
	return revisions, nil
}

func frontendRecheckPath(r Run) (Profile, error) {
	bad := errors.New("frontend recovery requires the frozen owner, fixed check and protected retained delivery path")
	path := []struct{ id, harness, mode, access string }{
		{"owner", "codex", "", "workspace-write"},
		{"check", "command", "", "workspace-read"},
		{"measure", "command", "", "workspace-read"},
		{"review", "codex", "", "workspace-read"},
		{"integrate", "git-candidate", "", "workspace-read"},
		{"publish", "release", "publish", "input-only"},
		{"qualify", "release", "qualify", "input-only"},
		{"prepare", "release", "prepare", "input-only"},
		{"merge", "release", "merge", "input-only"},
	}
	if r.Process.ID != "deliver-planned-retained-frontend" || r.Step < 1 || r.Step+len(path) > len(r.Process.Steps) {
		return Profile{}, bad
	}
	steps := r.Process.Steps[r.Step : r.Step+len(path)]
	for i, step := range steps {
		p, err := releaseBudgetProfile(r, r.Step+i)
		want := path[i]
		if step.ID != want.id || err != nil || p.Harness != want.harness || p.Mode != want.mode || p.Access != want.access {
			return Profile{}, bad
		}
	}
	ownerRole, checkRole := r.Roles[steps[0].Role], r.Roles[steps[1].Role]
	owner, _ := releaseBudgetProfile(r, r.Step)
	actual, err := ownerRole.Profile(r.Current().Profile)
	check, _ := releaseBudgetProfile(r, r.Step+1)
	patch := Binding{FromCandidate: true}
	if err != nil || !reflect.DeepEqual(owner, actual) || !reflect.DeepEqual(ownerRole.Outputs, map[string]string{"summary": "text", "patch": "artifact"}) || !frontendRecheckProfile(check) ||
		!reflect.DeepEqual(checkRole.Inputs, map[string]string{"patch": "artifact", "tests": "text"}) || !reflect.DeepEqual(checkRole.Outputs, map[string]string{"check": "text", "artifact": "artifact"}) ||
		!reflect.DeepEqual(steps[1].Inputs, map[string]Binding{"patch": patch, "tests": {FromInput: "tests"}}) ||
		steps[3].Inputs["patch"] != patch || steps[3].Inputs["checks"] != (Binding{FromStep: "check", Output: "artifact"}) ||
		steps[4].Inputs["patch"] != patch || steps[4].Inputs["verdict"] != (Binding{FromStep: "review", Output: "verdict"}) || steps[5].Inputs["candidate"] != (Binding{FromStep: "integrate", Output: "candidate"}) ||
		steps[6].Inputs["receipt"] != (Binding{FromStep: "publish", Output: "receipt"}) || steps[7].Inputs["receipt"] != (Binding{FromStep: "qualify", Output: "receipt"}) {
		return Profile{}, bad
	}
	previous := Binding{FromStep: "publish", Output: "receipt", Previous: true, Fallback: &Binding{FromStep: "source-adopt", Output: "receipt"}}
	adopt, err := releaseBudgetProfile(r, 0)
	if err != nil || r.Process.Steps[0].ID != "source-adopt" || adopt.Harness != "release" || adopt.Mode != "source-adopt" || adopt.Access != "input-only" || !reflect.DeepEqual(steps[5].Inputs["previous"], previous) {
		return Profile{}, bad
	}
	blocked := Route{Next: "$fail"}
	for _, edge := range []struct{ from, to int }{{1, 2}, {3, 4}, {6, 7}} {
		step := steps[edge.from]
		route, ok := sourceCorrectionRoute(r, r.Step+edge.from)
		if !ok || route.Next != "owner" || len(step.Routes) != 3 || step.Routes["ready"] != (Route{Next: steps[edge.to].ID}) || step.Routes["blocked"] != blocked {
			return Profile{}, bad
		}
	}
	if !reflect.DeepEqual(steps[0].Routes, map[string]Route{"blocked": blocked}) || !reflect.DeepEqual(steps[2].Routes, map[string]Route{"ready": {Next: "review"}, "continue": {Next: "owner", MaxTimes: 1}}) || len(steps[4].Routes) != 0 ||
		!reflect.DeepEqual(steps[5].Routes, map[string]Route{"ready": {Next: "qualify"}, "blocked": blocked}) || !reflect.DeepEqual(steps[7].Routes, map[string]Route{"ready": {Next: "merge", Approval: true}, "blocked": blocked}) {
		return Profile{}, bad
	}
	return check, nil
}

func frontendRecheckProfile(p Profile) bool {
	cmd := p.Command
	if p.Harness != "command" || p.Access != "workspace-read" || cmd == nil || cmd.Network || cmd.Directory != "" || cmd.Result != "domain.json" || cmd.Artifact != "frontend-check.json" || len(cmd.ExitOutcomes) != 0 {
		return false
	}
	for key, value := range cmd.Env {
		switch key {
		case "OPENSSL_CONF", "RUNNER_GO_MODULES", "RUNNER_NODE_MODULES":
			if !filepath.IsAbs(value) {
				return false
			}
		case "PATH":
			for _, dir := range filepath.SplitList(value) {
				if !filepath.IsAbs(dir) {
					return false
				}
			}
		default:
			return false
		}
	}
	args := cmd.Args
	return len(args) == 11 && filepath.IsAbs(args[0]) && filepath.Base(args[0]) == "runner-frontend-check" && filepath.IsAbs(args[2]) && filepath.IsAbs(args[4]) && args[1] == "--node" && args[3] == "--modules" &&
		reflect.DeepEqual(args[5:10], []string{"--source", "{workspace}", "--scratch", "{scratch}", "--tests"}) &&
		(args[10] == "{text.tests}" || args[10] == FrontendCandidateTests)
}

// FrontendCandidateTests expands an explicit test selection with changed tests
// from the owned candidate before the read-only verifier is dispatched.
const FrontendCandidateTests = "{frontend.tests}"

func frontendRecheckHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (c *Core) validatePriorFrontendTimeout(r Run, req RecoverFrontendTimeoutsRequest, check Attempt, source SourceResult) error {
	bad := errors.New("frontend timeout recovery requires the consumed recheck, exhausted correction route and exact unchanged timeout source")
	old := r.FrontendCheckRecovery
	route := r.Process.Steps[check.Step].Routes["changes_requested"]
	if old == nil || old.CheckStep != "check" || old.CheckVisit >= check.Visit || old.PublicationHead != req.PublicationHead || r.RouteUses["owner/frontend_recheck"] != 1 ||
		r.RouteUses["check/changes_requested"] != routeLimit(r, check.Step, "changes_requested", route) || check.ID != req.CheckAttemptID || !frontendRecheckHex(req.CheckSHA256, 64) || len(r.Attempts) < 3 {
		return bad
	}
	consumed := false
	for _, a := range r.Attempts {
		if a.Step == check.Step && a.Visit == old.CheckVisit && a.Source != nil && a.Source.InputPatchID == old.PatchID && a.End != nil && a.End.Kind == "returned" && a.End.Result != nil && a.End.Result.Outcome == "changes_requested" {
			consumed = true
			break
		}
	}
	checked := r.Attempts[len(r.Attempts)-3]
	if !consumed || checked.Step != r.Step || checked.Visit+1 != check.Visit || checked.End == nil || checked.End.Kind != "returned" || checked.End.Result == nil || checked.End.Result.Outcome != "ready" ||
		checked.Source == nil || checked.Source.AttemptID != checked.ID || checked.Source.PatchID != source.InputPatchID || checked.Source.Repository != source.Repository || checked.Source.Base != source.Base || checked.Source.WorkDir != source.WorkDir || checked.Source.Tree != source.Tree {
		return bad
	}
	_, cases, err := c.readPriorFrontendTimeouts(r, check.ID+"/result", req.CheckSHA256)
	if err != nil || len(cases) != 7 {
		return errors.New("the exact preceding frontend report does not establish seven 5000ms timeouts")
	}
	return nil
}

type frontendTimeoutCaseKey struct{ file, name string }

type frontendTimeoutReport struct {
	Schema           int      `json:"schema"`
	Status           string   `json:"status"`
	Tests            []string `json:"tests"`
	Executed         int      `json:"executed"`
	Failed           int      `json:"failed"`
	Skipped          int      `json:"skipped"`
	ExitCode         *int     `json:"exit_code"`
	VitestIncomplete bool     `json:"vitest_incomplete"`
	Typecheck        struct {
		Status   string `json:"status"`
		Complete bool   `json:"complete"`
	} `json:"typecheck"`
	Files []struct {
		Selected string   `json:"selected"`
		Reported []string `json:"reported"`
	} `json:"files"`
	Vitest struct {
		Total   int  `json:"numTotalTests"`
		Passed  int  `json:"numPassedTests"`
		Failed  int  `json:"numFailedTests"`
		Pending int  `json:"numPendingTests"`
		Todo    int  `json:"numTodoTests"`
		Success bool `json:"success"`
		Results []struct {
			Name       string `json:"name"`
			Assertions []struct {
				FullName string   `json:"fullName"`
				Status   string   `json:"status"`
				Duration *float64 `json:"duration"`
				Messages []string `json:"failureMessages"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	} `json:"vitest"`
	Diagnostics struct {
		Failures []struct {
			File       string  `json:"file"`
			FullName   string  `json:"full_name"`
			DurationMS float64 `json:"duration_ms"`
			ErrorIndex int     `json:"error_index"`
			Message    string  `json:"message"`
		} `json:"failures"`
	} `json:"vitest_diagnostics"`
	Timeouts frontendTimeoutDiagnostics `json:"frontend_timeout_diagnostics"`
}

type frontendTimeoutDiagnostics struct {
	Schema int    `json:"schema"`
	Status string `json:"status"`
	Method string `json:"method"`
	Cases  []struct {
		File                 string  `json:"file"`
		FullName             string  `json:"full_name"`
		TimeoutMS            int     `json:"timeout_ms"`
		OriginalDurationMS   float64 `json:"original_duration_ms"`
		DiagnosticDurationMS float64 `json:"diagnostic_duration_ms"`
		Status               string  `json:"status"`
		Profiles             []struct {
			Process        string `json:"process"`
			Samples        int    `json:"samples"`
			IdleSamples    int    `json:"idle_samples"`
			ProjectSamples int    `json:"project_samples"`
			Stacks         []struct {
				Samples int `json:"samples"`
				Frames  []struct {
					Kind string `json:"kind"`
					File string `json:"file"`
					Line int    `json:"line"`
				} `json:"frames"`
			} `json:"stacks"`
		} `json:"profiles"`
	} `json:"cases"`
}

func (c *Core) readPriorFrontendTimeouts(r Run, id, digest string) (map[frontendTimeoutCaseKey]bool, map[frontendTimeoutCaseKey]float64, error) {
	data, err := c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
		return nil, nil, errors.New("retained frontend timeout report digest differs")
	}
	var report frontendTimeoutReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, nil, err
	}
	return report.execution(r.Inputs["tests"])
}

func (p frontendTimeoutReport) execution(selection Value) (map[frontendTimeoutCaseKey]bool, map[frontendTimeoutCaseKey]float64, error) {
	bad := errors.New("frontend timeout report does not establish complete matching full-suite execution")
	var selectedJSON string
	var selected []string
	if json.Unmarshal(selection.Inline, &selectedJSON) != nil || json.Unmarshal([]byte(selectedJSON), &selected) != nil || len(selected) == 0 || !reflect.DeepEqual(selected, p.Tests) ||
		p.Schema != 2 || p.Executed < 1 || p.Failed < 0 || p.Skipped != 0 || p.ExitCode == nil || p.VitestIncomplete || p.Typecheck.Status != "passed" || !p.Typecheck.Complete ||
		p.Vitest.Total != p.Executed || p.Vitest.Passed != p.Executed-p.Failed || p.Vitest.Failed != p.Failed || p.Vitest.Pending != 0 || p.Vitest.Todo != 0 ||
		(p.Failed == 0 && (p.Status != "passed" || *p.ExitCode != 0 || !p.Vitest.Success)) || (p.Failed > 0 && (p.Status != "failed" || *p.ExitCode != 1 || p.Vitest.Success)) {
		return nil, nil, bad
	}
	byPath := map[string]string{}
	files := map[string]bool{}
	for _, file := range p.Files {
		if file.Selected == "" || files[file.Selected] || len(file.Reported) != 1 || file.Reported[0] == "" || byPath[file.Reported[0]] != "" {
			return nil, nil, bad
		}
		files[file.Selected] = true
		byPath[file.Reported[0]] = file.Selected
	}
	if len(files) != len(selected) || len(p.Vitest.Results) != len(selected) {
		return nil, nil, bad
	}
	for _, file := range selected {
		if !files[file] {
			return nil, nil, bad
		}
	}
	const timeoutMessage = "Test timed out in 5000ms."
	restored := map[frontendTimeoutCaseKey]float64{}
	for _, f := range p.Diagnostics.Failures {
		key := frontendTimeoutCaseKey{f.File, f.FullName}
		first, _, _ := strings.Cut(f.Message, "\n")
		if !files[f.File] || f.FullName == "" || restored[key] != 0 || f.DurationMS <= 0 || f.ErrorIndex != 0 || strings.TrimPrefix(first, "Error: ") != timeoutMessage {
			return nil, nil, bad
		}
		restored[key] = f.DurationMS
	}
	executed := map[frontendTimeoutCaseKey]bool{}
	timeouts := map[frontendTimeoutCaseKey]float64{}
	for _, result := range p.Vitest.Results {
		file := byPath[result.Name]
		if file == "" || len(result.Assertions) == 0 {
			return nil, nil, bad
		}
		delete(byPath, result.Name)
		for _, assertion := range result.Assertions {
			key := frontendTimeoutCaseKey{file, assertion.FullName}
			if assertion.FullName == "" || executed[key] || assertion.Status != "passed" && assertion.Status != "failed" {
				return nil, nil, bad
			}
			executed[key] = true
			if assertion.Status == "passed" {
				continue
			}
			if assertion.Duration == nil || *assertion.Duration <= 0 || len(assertion.Messages) != 1 {
				return nil, nil, bad
			}
			first, _, _ := strings.Cut(assertion.Messages[0], "\n")
			first = strings.TrimPrefix(first, "Error: ")
			if first != timeoutMessage && (first != "STACK_TRACE_ERROR" || restored[key] != *assertion.Duration) {
				return nil, nil, bad
			}
			delete(restored, key)
			timeouts[key] = *assertion.Duration
		}
	}
	if len(executed) != p.Executed || len(timeouts) != p.Failed || len(restored) != 0 {
		return nil, nil, bad
	}
	return executed, timeouts, nil
}

func (d frontendTimeoutDiagnostics) complete(timeouts map[frontendTimeoutCaseKey]float64) bool {
	if d.Schema != 1 || d.Status != "complete" || d.Method != "node-v8-cpu-sampling-isolated-case-v1" || len(d.Cases) == 0 || len(d.Cases) > 8 || len(d.Cases) != len(timeouts) {
		return false
	}
	seen := map[frontendTimeoutCaseKey]bool{}
	for _, sample := range d.Cases {
		key := frontendTimeoutCaseKey{sample.File, sample.FullName}
		if seen[key] || len(sample.FullName) > 512 || len(sample.Profiles) > 8 || sample.TimeoutMS != 5000 || timeouts[key] <= 0 || sample.OriginalDurationMS != timeouts[key] || sample.DiagnosticDurationMS <= 0 ||
			sample.Status != "timeout_reproduced" && (sample.Status != "isolated_pass" || sample.DiagnosticDurationMS >= sample.OriginalDurationMS) {
			return false
		}
		seen[key] = true
		workerStack, supervisors := false, 0
		for _, profile := range sample.Profiles {
			if profile.Process != "worker" && profile.Process != "supervisor" || profile.Samples < 1 || profile.Samples > 32768 || len(profile.Stacks) > 6 || profile.IdleSamples < 0 || profile.ProjectSamples < 0 || profile.IdleSamples > profile.Samples || profile.ProjectSamples > profile.Samples-profile.IdleSamples {
				return false
			}
			if profile.Process == "supervisor" {
				supervisors++
			}
			stackSamples := 0
			for _, stack := range profile.Stacks {
				if stack.Samples < 1 || stack.Samples > profile.Samples || len(stack.Frames) == 0 || len(stack.Frames) > 4 {
					return false
				}
				stackSamples += stack.Samples
				for _, frame := range stack.Frames {
					if (frame.Kind != "project" && frame.Kind != "dependency") || frame.File == "" || len(frame.File) > 384 || !filepath.IsLocal(frame.File) || filepath.Clean(frame.File) != frame.File || frame.Line < 1 {
						return false
					}
					workerStack = workerStack || profile.Process == "worker"
				}
			}
			if stackSamples > profile.Samples-profile.IdleSamples {
				return false
			}
		}
		if supervisors != 1 || !workerStack {
			return false
		}
	}
	return true
}

// Only this one owned diagnostic visit can earn the distinct repair edge.
// Ready still means the full uninstrumented acceptance, never a profiled pass.
func (c *Core) routeFrontendTimeoutDiagnostic(r *Run, a *Attempt) bool {
	grant := r.FrontendTimeoutRecovery
	if grant == nil || grant.CheckVisit != a.Visit || r.Process.Steps[a.Step].ID != grant.CheckStep {
		return false
	}
	stop := func(reason string) bool {
		r.Status, r.Reason = "Failed", reason
		return true
	}
	if a.End == nil || a.End.Kind != "returned" || a.End.Result == nil {
		if a.End != nil && a.End.Reason != "" {
			return stop(a.End.Reason)
		}
		return stop("frontend timeout diagnostic did not return a settled result")
	}
	result := a.End.Result
	if result.Outcome == "blocked" {
		return false // Preserve the configured blocked route and its reason.
	}
	priorOwner := SourceResult{}
	for _, old := range r.Attempts {
		if old.ID == grant.OwnerAttemptID && old.Source != nil {
			priorOwner = *old.Source
			break
		}
	}
	if a.Source == nil || a.Source.AttemptID != a.ID || a.Source.InputPatchID != grant.PatchID || priorOwner.PatchID != grant.PatchID || a.Source.Repository != priorOwner.Repository || a.Source.Base != priorOwner.Base || a.Source.WorkDir != priorOwner.WorkDir {
		return stop("frontend timeout diagnostic source differs from the authorized capture")
	}
	id := result.Outputs["artifact"].ArtifactID
	if id != a.ID+"/result" {
		return stop("frontend timeout diagnostic is missing its owned result artifact")
	}
	data, err := c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
	var report frontendTimeoutReport
	if err != nil || json.Unmarshal(data, &report) != nil {
		return stop("frontend timeout diagnostic result is unreadable or incomplete")
	}
	executed, timeouts, err := report.execution(r.Inputs["tests"])
	priorExecuted, oldTimeouts, priorErr := c.readPriorFrontendTimeouts(*r, grant.PriorCheckID, grant.PriorCheckSHA256)
	if err != nil || priorErr != nil || len(oldTimeouts) != 7 {
		return stop("frontend timeout diagnostic could not verify full-suite execution and its retained prior report")
	}
	if !maps.Equal(executed, priorExecuted) {
		return stop("frontend timeout diagnostic changed the retained full-suite assertion identities")
	}
	if result.Outcome == "ready" && len(timeouts) == 0 {
		return false
	}
	if result.Outcome != "changes_requested" || grant.RepairVisit != 0 || !report.Timeouts.complete(timeouts) {
		return stop("frontend timeout diagnostic lacks complete matched profile evidence for repair")
	}
	route, ok := sourceCorrectionRoute(*r, a.Step)
	if !ok || route.Next != "owner" || r.RouteUses["check/changes_requested"] != routeLimit(*r, a.Step, "changes_requested", route) || r.RouteUses["check/frontend_timeout_repair"] != 0 {
		return stop("frontend timeout diagnostic does not own a distinct repair transition")
	}
	need, err := readyTail(*r, a.Step)
	if err != nil || c.familyReserve(*r, 1, need) != nil {
		return stop("frontend timeout repair lacks the original budget for checked and protected completion")
	}
	owner := r.Process.Steps[a.Step-1]
	inputs, err := bindings(*r, owner.Inputs)
	if err != nil || inputs["checks"].ArtifactID != id {
		return stop("frontend timeout repair did not bind the verified check evidence")
	}
	c.advance(r, &StepResult{Outcome: "frontend_timeout_repair"}, Route{Next: route.Next})
	grant.RepairVisit = r.Visit
	grant.RepairEvidenceID = id
	grant.RepairEvidenceSHA = fmt.Sprintf("%x", sha256.Sum256(data))
	return true
}
