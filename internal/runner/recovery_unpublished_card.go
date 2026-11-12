package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

type RecoverUnpublishedCardRequest struct {
	RequestID      string `json:"request_id"`
	RunID          string `json:"run_id"`
	Revision       int64  `json:"revision"`
	GroupAttemptID string `json:"group_attempt_id"`
	ChildRunID     string `json:"child_run_id"`
	ChildRevision  int64  `json:"child_revision"`
	AttemptID      string `json:"attempt_id"`
	PatchSHA256    string `json:"patch_sha256"`
	BindingsSHA256 string `json:"bindings_sha256,omitempty"`
}

type CardRecoveryArtifact struct {
	ID     string `json:"id"`
	RunID  string `json:"run_id"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type UnpublishedCardAssessment struct {
	RunID              string                 `json:"run_id"`
	Revision           int64                  `json:"revision"`
	StoredRevision     int64                  `json:"stored_revision"`
	ChildRunID         string                 `json:"child_run_id"`
	ChildRevision      int64                  `json:"child_revision"`
	GroupAttemptID     string                 `json:"group_attempt_id"`
	AttemptID          string                 `json:"attempt_id"`
	Source             SourceResult           `json:"source"`
	Authority          TaskAssignment         `json:"authority"`
	Pins               []TaskCandidate        `json:"pins"`
	Artifacts          []CardRecoveryArtifact `json:"artifacts"`
	BindingsSHA256     string                 `json:"bindings_sha256"`
	Spent              int                    `json:"spent"`
	Ceiling            int                    `json:"ceiling"`
	ReservedCompletion int                    `json:"reserved_completion"`
	OwnerVisit         int                    `json:"owner_visit"`
}

// This audit retains the failed group facts. Original attempts, definitions and
// pins remain in their ordinary records; only current operational state rejoins.
type UnpublishedCardRecovery struct {
	Artifacts            []CardRecoveryArtifact        `json:"artifacts"`
	Request              RecoverUnpublishedCardRequest `json:"request"`
	OriginalRootReason   string                        `json:"original_root_reason"`
	OriginalGroupFailure string                        `json:"original_group_failure"`
	OriginalGroupDone    bool                          `json:"original_group_done"`
	OriginalChildReason  string                        `json:"original_child_reason"`
	OwnerVisit           int                           `json:"owner_visit"`
	Spent                int                           `json:"spent"`
	ReservedCompletion   int                           `json:"reserved_completion"`
	GrantedAt            time.Time                     `json:"granted_at"`
}

// Both records and the one-use receipt must commit together. No private-store
// patch, partial root reset, replacement child or scheduler is supported.
type UnpublishedCardRecoveryStore interface {
	SaveUnpublishedCardRecovery(context.Context, Run, Run, *Receipt) error
}
type UnpublishedCardRecoveryValidator interface {
	ValidateUnpublishedCardRecovery(context.Context, Run, UnpublishedCardAssessment) error
}

func recoveryDigest(v any) string {
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// CheckUnpublishedCardRecovery observes retained authority; it never prepares a
// checkout, grants a visit, writes a receipt or wakes product work.
func (c *Core) CheckUnpublishedCardRecovery(req RecoverUnpublishedCardRequest) (UnpublishedCardAssessment, error) {
	c.mu.Lock()
	assessment, child, err := c.planUnpublishedCardRecovery(req)
	validator, ok := c.executor.(UnpublishedCardRecoveryValidator)
	c.mu.Unlock()
	if err != nil {
		return assessment, err
	}
	if !ok {
		return assessment, errors.New("executor cannot authenticate unpublished card recovery")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	if err = validator.ValidateUnpublishedCardRecovery(ctx, child, assessment); err != nil {
		return UnpublishedCardAssessment{}, fmt.Errorf("unpublished card source: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current, _, err := c.planUnpublishedCardRecovery(req)
	if err != nil {
		return UnpublishedCardAssessment{}, err
	}
	if !reflect.DeepEqual(current, assessment) {
		return UnpublishedCardAssessment{}, errors.New("card family changed during validation")
	}
	return assessment, nil
}

func (c *Core) RecoverUnpublishedCard(req RecoverUnpublishedCardRequest) (Run, error) {
	args, _ := json.Marshal(req)
	c.mu.Lock()
	replay, found, err := c.replay(req.RequestID, "recover_unpublished_card", args)
	c.mu.Unlock()
	if found || err != nil {
		return replay, err
	}
	if req.BindingsSHA256 == "" {
		return Run{}, errors.New("exact preflight bindings_sha256 is required")
	}
	assessment, err := c.CheckUnpublishedCardRecovery(req)
	if err != nil {
		c.mu.Lock()
		replay, found, replayErr := c.replay(req.RequestID, "recover_unpublished_card", args)
		c.mu.Unlock()
		if found || replayErr != nil {
			return replay, replayErr
		}
		return Run{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if replay, found, err := c.replay(req.RequestID, "recover_unpublished_card", args); found || err != nil {
		return replay, err
	}
	current, child, err := c.planUnpublishedCardRecovery(req)
	if err != nil {
		return Run{}, err
	}
	if !reflect.DeepEqual(current, assessment) {
		return Run{}, errors.New("card family changed before recovery commit")
	}
	saver, ok := c.store.(UnpublishedCardRecoveryStore)
	if !ok {
		return Run{}, errors.New("store cannot atomically save the one-use card recovery")
	}
	root := Clone(c.runs[req.RunID])
	group := root.TaskGroups[child.Task.Identity.GroupStep]
	root.UnpublishedCardRecovery = &UnpublishedCardRecovery{
		Artifacts:            Clone(assessment.Artifacts),
		Request:              req,
		OriginalRootReason:   root.Reason,
		OriginalGroupFailure: group.Failure,
		OriginalGroupDone:    group.Done,
		OriginalChildReason:  child.Reason,
		OwnerVisit:           child.Visit + 1,
		Spent:                assessment.Spent,
		ReservedCompletion:   assessment.ReservedCompletion,
		GrantedAt:            time.Now().UTC(),
	}
	// Format four changes no SQLite schema. Older readers reject this root rather
	// than silently losing the one-use audit and allowing another card visit.
	root.RecordFormat = 4
	group.Done, group.Failure = false, ""
	root.Status, root.Reason = "Running", "one-use unpublished card recovery"
	root.ResourcesReleased = false
	child.Visit++
	child.Status, child.Reason = "Queued", "one-use recovery of captured unreviewed source"
	child.ResourcesReleased = false
	if err = admitRunRecord(root, c.disk.Policy.Database.MaxRecord); err != nil {
		return Run{}, err
	}
	if err = admitRunRecord(child, c.disk.Policy.Database.MaxRecord); err != nil {
		return Run{}, err
	}
	now := time.Now().UTC()
	root.Revision++
	child.Revision++
	root.Updated, child.Updated = now, now
	receipt := &Receipt{Key: req.RequestID, Kind: "recover_unpublished_card", Args: args, RunID: root.ID}
	if err = saver.SaveUnpublishedCardRecovery(c.ctx, root, child, receipt); err != nil {
		return Run{}, err
	}
	c.runs[root.ID], c.runs[child.ID] = root, child
	close(c.changed)
	c.changed = make(chan struct{})
	c.Wake()
	return Clone(root), nil
}

func (c *Core) planUnpublishedCardRecovery(req RecoverUnpublishedCardRequest) (UnpublishedCardAssessment, Run, error) {
	var out UnpublishedCardAssessment
	reject := func(reason string) (UnpublishedCardAssessment, Run, error) { return out, Run{}, errors.New(reason) }
	root, ok := c.runs[req.RunID]
	if !ok {
		return out, Run{}, ErrNotFound
	}
	if root.UnpublishedCardRecovery != nil {
		return reject("this family already consumed its one-use unpublished card recovery")
	}
	if root.RecordFormat != 3 || root.Task != nil || root.Status != "Failed" || !isolatedStep(root) || len(root.TaskGroups) != 1 || !c.cardRecoverySettled(root) {
		return reject("exact settled failed task-group root required")
	}
	group := root.TaskGroups[root.Process.Steps[root.Step].ID]
	if group == nil || !group.Done || group.AttemptID != req.GroupAttemptID || group.Failure == "" || group.Failure != root.Reason || len(group.Children) < 2 || len(group.Children) != len(group.Items) {
		return reject("exact settled failed group and retained reservations required")
	}
	// Like the other planned-family owner recoveries, bind the revision exposed
	// by Get/inspect/result. Status exposes only the stored root contribution.
	observedRevision := c.outputRun(root).Revision
	if req.Revision < 1 || req.Revision != observedRevision {
		return reject("exact observed family revision from Get/inspect/result required; status exposes the stored root revision")
	}
	child, ok := c.runs[req.ChildRunID]
	if !ok {
		return out, Run{}, ErrNotFound
	}
	if child.Revision != req.ChildRevision || child.Task == nil || child.Task.Identity.RootRunID != root.ID || child.Task.Identity.GroupStep != group.StepID || child.Status != "Failed" || child.Reason != "attempt deadline reached" || root.Reason != "task "+child.Task.Identity.ItemID+": "+child.Reason || !c.cardRecoverySettled(child) {
		return reject("exact settled deadline-failed child revision required")
	}
	if _, err := c.taskRoot(child); err != nil {
		return out, Run{}, err
	}
	if child.Step != 1 || len(child.Process.Steps) != 3 || len(child.Attempts) != 2 || child.VisitExtraAttempts != 0 || child.NextProfile != "" || child.Escalations != 0 {
		return reject("recovery requires the initial seed/owner/pin card, without prior corrections or overrides")
	}
	failed := child.Current()
	if failed == nil || failed.ID != req.AttemptID || failed.Superseded || !failed.TimedOut || !failed.StopRequested || failed.Source == nil || failed.End == nil || failed.End.Kind != "returned" || failed.End.Result == nil || failed.End.Result.Outcome != "ready" || failed.End.Retryable {
		return reject("deadline failure must retain its exact unaccepted ready capture")
	}
	source := *failed.Source
	if source.AttemptID != failed.ID || source.PatchID != failed.ID+"/changes.patch" || source.InputPatchID == "" || source.Base != group.Base || source.Repository != group.Repository || failed.End.Result.Outputs["patch"].ArtifactID != source.PatchID {
		return reject("captured source identity differs from the failed owner")
	}
	owner := child.Process.Steps[1]
	role := child.Roles[owner.Role]
	profile, err := role.Profile(child.Profiles[owner.Role])
	timeout := owner.AttemptTimeout
	if timeout == "" {
		timeout = child.Process.AttemptTimeout
	}
	duration, errTime := time.ParseDuration(timeout)
	if err != nil || errTime != nil || duration != 20*time.Minute || failed.Deadline.Sub(failed.Started) != duration || profile.Harness != "codex" || profile.Access != "workspace-write" || failed.Profile != child.Profiles[owner.Role] || owner.Escalation != nil || readyNext(child.Process, 1) != 2 || !reflect.DeepEqual(role.Outputs, map[string]string{"patch": "artifact", "summary": "text"}) {
		return reject("frozen ordinary twenty-minute source owner contract differs")
	}
	if err = ValidateValues(failed.End.Result.Outputs, role.Outputs); err != nil {
		return reject("captured late result differs from the original source output contract")
	}
	for i, mode := range []string{"seed", "pin"} {
		index := i * 2
		p, e := child.Roles[child.Process.Steps[index].Role].Profile(child.Profiles[child.Process.Steps[index].Role])
		if e != nil || p.Harness != "task-source" || p.Mode != mode {
			return reject("frozen source seed/pin contract differs")
		}
	}
	if !planningOutcome(child.Attempts[0], "ready") || child.Attempts[0].Step != 0 {
		return reject("original dependency seed did not complete normally")
	}
	authority, err := c.taskAssignment(child, owner)
	if err != nil {
		return out, Run{}, err
	}
	var item TaskItem
	for _, v := range group.Items {
		if v.ID == child.Task.Identity.ItemID {
			item = v
		}
	}
	expected := childForItem(root, group, item, child.ID)
	deps, _ := json.Marshal(authority.Dependencies)
	expected.Inputs["_task_dependencies"] = Value{Inline: deps}
	if !reflect.DeepEqual(expected.Process, child.Process) || !reflect.DeepEqual(expected.Roles, child.Roles) || !reflect.DeepEqual(expected.Profiles, child.Profiles) || !reflect.DeepEqual(expected.Inputs, child.Inputs) {
		return reject("frozen child request, scope, dependencies or profile differ from its accepted card")
	}
	inputs, err := bindings(child, owner.Inputs)
	if err != nil || !reflect.DeepEqual(inputs, child.StepInputs) {
		return reject("failed owner inputs differ from frozen bindings")
	}
	out = UnpublishedCardAssessment{
		RunID:          root.ID,
		Revision:       observedRevision,
		StoredRevision: root.Revision,
		ChildRunID:     child.ID,
		ChildRevision:  child.Revision,
		GroupAttemptID: group.AttemptID,
		AttemptID:      failed.ID,
		Source:         source,
		Authority:      *authority,
		Spent:          c.familyAttempts(root),
		Ceiling:        root.Process.MaxAttempts,
		OwnerVisit:     child.Visit + 1,
	}
	runs := []Run{root, child}
	addArtifact := func(id, owner string) error {
		a, e := c.store.Artifact(c.ctx, id)
		if e != nil || a.ProjectID != root.ProjectID || owner != "" && a.RunID != owner {
			return errors.New("retained recovery artifact ownership differs")
		}
		data, e := c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
		if e != nil || int64(len(data)) != a.Bytes {
			return errors.New("retained recovery artifact is missing or changed size")
		}
		out.Artifacts = append(out.Artifacts, CardRecoveryArtifact{ID: id, RunID: a.RunID, Bytes: a.Bytes, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
		return nil
	}
	if err = addArtifact(source.PatchID, child.ID); err != nil {
		return out, Run{}, err
	}
	if req.PatchSHA256 != out.Artifacts[0].SHA256 {
		return reject("captured patch sha256 differs")
	}
	if err = addArtifact(source.InputPatchID, child.ID); err != nil {
		return out, Run{}, err
	}
	seen := map[string]bool{}
	for _, reservation := range group.Children {
		if seen[reservation.RunID] {
			return reject("duplicate child reservation")
		}
		seen[reservation.RunID] = true
		if reservation.RunID == child.ID {
			if reservation.Candidate != nil {
				return reject("failed source was already pinned")
			}
			continue
		}
		other, exists := c.runs[reservation.RunID]
		if !exists || !c.cardRecoverySettled(other) || other.Status != "Completed" || reservation.Candidate == nil || other.Task == nil {
			return reject("every other reserved card must retain its settled accepted pin")
		}
		var candidate TaskCandidate
		if json.Unmarshal(other.Outputs["result"].Inline, &candidate) != nil || candidate != *reservation.Candidate || !c.validTaskCandidate(other, group, candidate) {
			return reject("completed card pin differs from its authoritative result")
		}
		if err = addArtifact(candidate.PatchID, other.ID); err != nil {
			return out, Run{}, err
		}
		out.Pins = append(out.Pins, candidate)
		runs = append(runs, other)
	}
	names := make([]string, 0, len(child.StepInputs))
	for name := range child.StepInputs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		value := child.StepInputs[name]
		if value.ArtifactID != "" {
			if err = addArtifact(value.ArtifactID, ""); err != nil {
				return out, Run{}, err
			}
		}
	}
	reserve, err := c.taskCompletionReserve(child)
	if err != nil {
		return out, Run{}, err
	}
	out.ReservedCompletion = reserve
	if err = c.familyReserve(child, 1, reserve); err != nil {
		return out, Run{}, err
	}
	out.BindingsSHA256 = recoveryDigest(struct {
		Runs      []Run
		Artifacts []CardRecoveryArtifact
		Authority TaskAssignment
		Limits    ExecutionLimits
	}{runs, out.Artifacts, *authority, c.executionLimits()})
	if req.BindingsSHA256 != "" && req.BindingsSHA256 != out.BindingsSHA256 {
		return reject("preflight family/source bindings changed")
	}
	return out, Clone(child), nil
}

func (c *Core) cardRecoverySettled(r Run) bool {
	if r.Active() || r.CancelRequested || r.Decision != nil || !r.ResourcesReleased || r.CleanupBlocked || r.CleanupError != "" || c.inFlight[r.ID] || !allAttemptsReleased(r) {
		return false
	}
	for _, a := range r.Attempts {
		if c.inFlight[a.ID] {
			return false
		}
	}
	return true
}
func (c *Core) recoveredCard(r Run) *UnpublishedCardRecovery {
	if r.Task != nil {
		root, ok := c.runs[r.Task.Identity.RootRunID]
		if ok && root.UnpublishedCardRecovery != nil && root.UnpublishedCardRecovery.Request.ChildRunID == r.ID {
			return root.UnpublishedCardRecovery
		}
	}
	return nil
}
