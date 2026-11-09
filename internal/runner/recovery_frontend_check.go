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
	revisions  map[string]int64
}

func (c *Core) RecoverFrontendCheck(req RecoverFrontendCheckRequest) (Run, error) {
	const operation = "recover_frontend_check"
	args, _ := json.Marshal(req)
	c.mu.Lock()
	if r, ok, err := c.replay(req.RequestID, operation, args); ok || err != nil {
		c.mu.Unlock()
		return r, err
	}
	plan, err := c.planFrontendCheckRecovery(req)
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
	current, err := c.planFrontendCheckRecovery(req)
	if err != nil || !maps.Equal(current.revisions, plan.revisions) {
		return Run{}, errors.New("frontend run or accepted cards changed during validation; inspect before recovery")
	}
	r := current.run
	// This is a forward handoff. The blocked owner result and every earlier
	// attempt remain as recorded. Normal check outcomes choose the next step.
	c.advance(&r, &StepResult{Outcome: "frontend_recheck"}, Route{Next: "check"})
	if r.Status != "Queued" || !reflect.DeepEqual(r.StepInputs, map[string]Value{
		"patch": {ArtifactID: plan.source.PatchID},
		"tests": r.Inputs["tests"],
	}) {
		return Run{}, errors.New("retained frontend check inputs do not bind the blocked owner's patch and original selectors")
	}
	r.ResourcesReleased = false
	r.FrontendCheckRecovery = &FrontendCheckRecovery{
		RequestID:       req.RequestID,
		AttemptID:       req.AttemptID,
		CheckStep:       "check",
		CheckVisit:      r.Visit,
		PatchID:         plan.source.PatchID,
		PatchSHA256:     req.PatchSHA256,
		PriorCheckID:    plan.priorCheck,
		HelperSHA256:    req.HelperSHA256,
		PublicationHead: req.PublicationHead,
		GrantedAt:       time.Now().UTC(),
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

func (c *Core) planFrontendCheckRecovery(req RecoverFrontendCheckRequest) (frontendCheckRecoveryPlan, error) {
	var plan frontendCheckRecoveryPlan
	if req.Revision < 1 || !frontendRecheckHex(req.PatchSHA256, 64) || !frontendRecheckHex(req.HelperSHA256, 64) || !frontendRecheckHex(req.PublicationHead, 40) {
		return plan, errors.New("frontend recovery requires the observed revision and exact patch, helper and publication identities")
	}
	saved, exists := c.runs[req.RunID]
	if !exists {
		return plan, ErrNotFound
	}
	r := Clone(saved)
	if r.Task != nil || r.Status != "Failed" || r.CancelRequested || r.Active() || r.Decision != nil || c.inFlight[r.ID] || r.CleanupBlocked || r.CleanupError != "" || !r.ResourcesReleased || r.FrontendCheckRecovery != nil {
		return plan, errors.New("frontend recovery needs ungranted settled failed root work and confirmed cleanup")
	}
	if err := c.frontendRecheckSettledAttempts(r); err != nil {
		return plan, err
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
	if err := c.familyReserve(r, 1, need-1); err != nil {
		return plan, err
	}
	return frontendCheckRecoveryPlan{run: r, source: *source, check: check, priorCheck: prior.ArtifactID, revisions: revisions}, nil
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
		reflect.DeepEqual(args[5:], []string{"--source", "{workspace}", "--scratch", "{scratch}", "--tests", "{text.tests}"})
}

func frontendRecheckHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
