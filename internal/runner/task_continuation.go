package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// ContinueTaskRequest transfers one settled retained source candidate to a
// configured general-task process. It grants no new attempts or approvals.
// Inputs must explicitly contain patch/checks; their identities are checked
// against the exact retained attempts, never supplied as arbitrary grants.
type ContinueTaskRequest struct {
	RequestID       string           `json:"request_id"`
	RunID           string           `json:"run_id"`
	Revision        int64            `json:"revision"`
	Project         string           `json:"project"`
	Process         string           `json:"process"`
	SourceAttemptID string           `json:"source_attempt_id"`
	PatchSHA256     string           `json:"patch_sha256"`
	CheckAttemptID  string           `json:"check_attempt_id"`
	CheckSHA256     string           `json:"check_sha256"`
	Inputs          map[string]Value `json:"inputs"`
}

type TaskContinuationLineage struct {
	OriginRunID          string       `json:"origin_run_id"`
	OriginRevision       int64        `json:"origin_revision"`
	OriginProjectID      string       `json:"origin_project_id"`
	Source               SourceResult `json:"source"`
	PatchSHA256          string       `json:"patch_sha256"`
	CheckAttemptID       string       `json:"check_attempt_id"`
	CheckID              string       `json:"check_id"`
	CheckSHA256          string       `json:"check_sha256"`
	SerialGrantRequestID string       `json:"serial_grant_request_id"`
	HelperSHA256         string       `json:"helper_sha256"`
	PriorAttempts        int          `json:"prior_attempts"`
	MaxAttempts          int          `json:"max_attempts"`
}

type TaskContinuationAudit struct {
	RequestID string                  `json:"request_id"`
	RunID     string                  `json:"run_id"`
	GrantedAt time.Time               `json:"granted_at"`
	Lineage   TaskContinuationLineage `json:"lineage"`
}

type TaskAttemptBudget struct {
	Limit         int `json:"limit"`
	PriorAttempts int `json:"prior_attempts"`
	Spent         int `json:"spent"`
	Remaining     int `json:"remaining"`
}

type TaskContinuationStore interface {
	SaveTaskContinuation(context.Context, Run, Run, []Run, *Receipt) error
}

type taskContinuationPlan struct {
	origin  Run
	family  []Run
	source  SourceResult
	lineage TaskContinuationLineage
	imports map[string]Artifact
}

func (c *Core) ContinueTask(req ContinueTaskRequest) (Run, error) {
	args, err := json.Marshal(req)
	if err != nil {
		return Run{}, err
	}
	c.mu.Lock()
	if r, replay, err := c.replay(req.RequestID, "continue_task", args); replay || err != nil {
		if replay {
			r = c.outputRun(r)
		}
		c.mu.Unlock()
		return r, err
	}
	plan, err := c.planTaskContinuation(req)
	validator, supported := c.executor.(SourceRecoveryValidator)
	c.mu.Unlock()
	if err != nil {
		return Run{}, err
	}
	if !supported {
		return Run{}, errors.New("executor cannot authenticate retained source recovery")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	err = validator.ValidateSourceRecovery(ctx, Clone(plan.origin), plan.source)
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, replay, replayErr := c.replay(req.RequestID, "continue_task", args); replay || replayErr != nil {
		if replay {
			r = c.outputRun(r)
		}
		return r, replayErr
	}
	if err != nil {
		return Run{}, fmt.Errorf("retained task continuation source authority: %w", err)
	}
	current, err := c.planTaskContinuation(req)
	if err != nil || !reflect.DeepEqual(plan.family, current.family) || !reflect.DeepEqual(plan.lineage, current.lineage) {
		return Run{}, errors.New("retained source family changed during continuation validation; inspect before retrying")
	}
	store, supported := c.store.(TaskContinuationStore)
	if !supported {
		return Run{}, errors.New("store cannot atomically link a retained task continuation")
	}
	r, err := c.newRun(StartRequest{Project: req.Project, Process: req.Process, Inputs: req.Inputs}, plan.imports)
	if err != nil {
		return Run{}, err
	}
	if r.Process.Tasks == nil || r.Process.MaxAttempts != plan.lineage.MaxAttempts || r.Process.Inputs["patch"] != "artifact" || r.Process.Inputs["checks"] != "artifact" {
		return Run{}, errors.New("continuation needs a configured general-task process declaring patch/checks artifacts and the unchanged original total attempt allowance")
	}
	r.TaskGraph.Lineage = Clone(&plan.lineage)
	r.TaskGraph.Brief = graphGoal(r.Inputs)
	if err = admitRunRecord(r, c.disk.Policy.Database.MaxRecord); err != nil {
		return Run{}, err
	}
	now := time.Now().UTC()
	origin := Clone(plan.origin)
	origin.ContinuedBy = &TaskContinuationAudit{RequestID: req.RequestID, RunID: r.ID, GrantedAt: now, Lineage: plan.lineage}
	origin.Revision++
	origin.Updated = now
	r.Revision, r.Updated = 1, now
	receipt := Receipt{Key: req.RequestID, Kind: "continue_task", Args: args, RunID: r.ID}
	if err = ValidateTaskContinuation(origin, r, plan.family, &receipt); err != nil {
		return Run{}, err
	}
	if err = store.SaveTaskContinuation(c.ctx, origin, r, plan.family, &receipt); err != nil {
		return Run{}, err
	}
	c.runs[origin.ID], c.runs[r.ID] = origin, r
	close(c.changed)
	c.changed = make(chan struct{})
	c.Wake()
	return c.outputRun(r), nil
}

func (c *Core) planTaskContinuation(req ContinueTaskRequest) (taskContinuationPlan, error) {
	var plan taskContinuationPlan
	r, exists := c.runs[req.RunID]
	if !exists {
		return plan, ErrNotFound
	}
	if err := c.taskContinuationFrozen(r); err != nil {
		return plan, err
	}
	if req.Revision < 1 || c.outputRun(r).Revision != req.Revision || !frontendRecheckHex(req.PatchSHA256, 64) || !frontendRecheckHex(req.CheckSHA256, 64) || req.SourceAttemptID == "" || req.CheckAttemptID == "" || req.Project == "" || req.Process == "" {
		return plan, errors.New("continuation requires the exact observed family revision, source/check attempts and patch/check digests, and an explicit configured target")
	}
	if r.Task != nil || r.TaskGraph != nil || r.Status != "Failed" || r.CancelRequested || r.Decision != nil || r.ContinuedBy != nil || r.Process.ID != "deliver-planned-frontend" {
		return plan, errors.New("continuation requires an untransferred settled failed retained frontend root")
	}
	if _, err := c.disk.Read(familyCollectionDirectory+"/"+r.ID+".json", c.disk.Policy.Database.MaxRecord); !errors.Is(err, os.ErrNotExist) {
		return plan, errors.New("continuation source family has pending retention ownership; reconcile it before transfer")
	}
	family, err := c.legacyContinuationFamily(r)
	if err != nil {
		return plan, err
	}
	for _, member := range family {
		if !c.collectionSettled(member) || !member.ResourcesReleased || member.CleanupBlocked || member.CleanupError != "" {
			return plan, errors.New("continuation requires the complete old family settled, released and free of cleanup errors")
		}
	}
	used := c.familyAttempts(r)
	if used <= 0 || used >= r.Process.MaxAttempts {
		return plan, errors.New("continuation has no authenticated remaining original family allowance")
	}
	source, check, err := TaskContinuationEvidence(r, req.SourceAttemptID, req.CheckAttemptID)
	if err != nil {
		return plan, err
	}
	if _, err = c.serialFrontendReviewProvenance(r, map[string]Value{"patch": {ArtifactID: source.PatchID}, "checks": {ArtifactID: check.ID + "/result"}}); err != nil {
		return plan, err
	}
	imports := map[string]Artifact{}
	for _, proof := range []struct{ id, digest string }{{source.PatchID, req.PatchSHA256}, {check.ID + "/result", req.CheckSHA256}} {
		artifact, err := c.store.Artifact(c.ctx, proof.id)
		if err != nil || artifact.RunID != r.ID || artifact.ProjectID != r.ProjectID {
			return plan, errors.New("continuation source/check artifact registration differs from its original owner")
		}
		data, err := c.disk.Read("artifacts/"+proof.id, c.disk.Policy.MaxArtifact)
		if err != nil || int64(len(data)) != artifact.Bytes || fmt.Sprintf("%x", sha256.Sum256(data)) != proof.digest {
			return plan, errors.New("continuation source/check bytes differ from the exact registered digest")
		}
		imports[proof.id] = artifact
	}
	grant := r.FrontendCheckRecovery
	if grant.PatchSHA256 != req.PatchSHA256 {
		return plan, errors.New("continuation patch differs from its serial check grant")
	}
	report, err := c.readFrontendTimeoutReport(check.ID+"/result", req.CheckSHA256)
	selection, errSelection := bindings(r, r.Process.Steps[check.Step].Inputs)
	if err != nil || errSelection != nil || report.Status != "passed" || report.Failed != 0 {
		return plan, errors.New("continuation requires a complete passing check for the exact original selectors")
	}
	executedSelection, err := continuationFrontendSelection(selection["tests"], report.Tests)
	if err != nil {
		return plan, err
	}
	if _, _, err = report.execution(executedSelection); err != nil {
		return plan, err
	}
	if req.Inputs["patch"].ArtifactID != source.PatchID || req.Inputs["checks"].ArtifactID != check.ID+"/result" {
		return plan, errors.New("continuation patch/checks inputs must identify its two authenticated retained artifacts")
	}
	lineage := TaskContinuationLineage{
		OriginRunID: r.ID, OriginRevision: r.Revision, OriginProjectID: r.ProjectID,
		Source: source, PatchSHA256: req.PatchSHA256,
		CheckAttemptID: check.ID, CheckID: check.ID + "/result", CheckSHA256: req.CheckSHA256,
		SerialGrantRequestID: grant.RequestID, HelperSHA256: grant.HelperSHA256,
		PriorAttempts: used, MaxAttempts: r.Process.MaxAttempts,
	}
	lineage.Source.WorkDir = "" // Retained workspaces are never transferred as an access grant.
	return taskContinuationPlan{origin: Clone(r), family: family, source: source, lineage: lineage, imports: imports}, nil
}

var continuationFrontendTest = regexp.MustCompile(`^(src|scripts|private)/[A-Za-z0-9_./-]+\.test\.(ts|js)$`)

func continuationFrontendSelection(required Value, executed []string) (Value, error) {
	bad := errors.New("continuation check must retain the original ordered selectors and a bounded unique literal frontend test inventory")
	var raw string
	var original []string
	if json.Unmarshal(required.Inline, &raw) != nil || len(raw) > 4096 || json.Unmarshal([]byte(raw), &original) != nil || len(original) == 0 || len(original) > len(executed) || len(executed) > 32 || !slices.Equal(original, executed[:len(original)]) {
		return Value{}, bad
	}
	seen := make(map[string]bool, len(executed))
	for _, name := range executed {
		if !continuationFrontendTest.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "/./") || seen[name] {
			return Value{}, bad
		}
		seen[name] = true
	}
	encoded, err := json.Marshal(executed)
	if err != nil || len(encoded) > 4096 {
		return Value{}, bad
	}
	// The already authenticated fixed worker owns changed-regression expansion.
	// Preserve every requested file, then use the unchanged execution validator
	// for every reported file/assertion; do not duplicate Git diff interpretation
	// in Core or relax the exact selection contract of legacy timeout recovery.
	return Text(string(encoded)), nil
}

// TaskContinuationEvidence validates saved execution facts only. File digests,
// source capture ownership and complete report coverage are checked by Core.
func TaskContinuationEvidence(r Run, sourceAttemptID, checkAttemptID string) (SourceResult, Attempt, error) {
	bad := errors.New("continuation lacks the exact latest retained candidate and passing serial full check")
	var source *SourceResult
	var check Attempt
	for i := range r.Attempts {
		a := &r.Attempts[i]
		if a.ID == sourceAttemptID && !a.Superseded && !a.TimedOut && !a.StopRequested && a.State == Finished && a.Released && a.Source != nil && a.Source.AttemptID == a.ID && strings.HasPrefix(a.Source.PatchID, a.ID+"/") && a.End != nil && a.End.Kind == "returned" && !a.End.Retryable && a.End.Result != nil && a.End.Result.Outcome == "ready" && a.End.Result.Outputs["patch"].ArtifactID == a.Source.PatchID {
			source = a.Source
		}
		if a.ID == checkAttemptID {
			check = *a
		}
	}
	grant := r.FrontendCheckRecovery
	if source == nil || !frontendRecheckHex(source.Base, 40) || !frontendRecheckHex(source.Tree, 40) || source.Repository != r.Repository || grant == nil || !grant.Serial || grant.RequestID == "" || grant.PatchID != source.PatchID || !frontendRecheckHex(grant.HelperSHA256, 64) || check.ID == "" || check.Step != taskStepIndex(r.Process, grant.CheckStep) || check.Visit < grant.CheckVisit || check.Superseded || check.State != Finished || !check.Released || check.TimedOut || check.StopRequested || check.End == nil || check.End.Kind != "returned" || check.End.Retryable || check.End.Result == nil || check.End.Result.Outcome != "ready" || check.End.Result.Outputs["artifact"].ArtifactID != check.ID+"/result" || check.Source == nil || check.Source.AttemptID != check.ID || check.Source.InputPatchID != source.PatchID || check.Source.Base != source.Base || check.Source.Repository != source.Repository || check.Source.WorkDir != source.WorkDir || check.Source.Tree != "" && check.Source.Tree != source.Tree {
		return SourceResult{}, Attempt{}, bad
	}
	latest, err := bindings(r, map[string]Binding{"patch": {FromCandidate: true}, "checks": {FromStep: grant.CheckStep, Output: "artifact"}})
	if err != nil || latest["patch"].ArtifactID != source.PatchID || latest["checks"].ArtifactID != check.ID+"/result" {
		return SourceResult{}, Attempt{}, bad
	}
	return *source, check, nil
}

func (c *Core) legacyContinuationFamily(root Run) ([]Run, error) {
	if root.Task != nil || root.TaskGraph != nil || c.familyAttempts(root) == invalidFamilyAttempts {
		return nil, errors.New("continuation source family ownership differs")
	}
	family := []Run{Clone(root)}
	for _, group := range root.TaskGroups {
		if group == nil {
			return nil, errors.New("continuation source task group is unavailable")
		}
		for _, child := range group.Children {
			member, exists := c.runs[child.RunID]
			if !exists {
				return nil, errors.New("continuation cannot transfer an unmaterialized source reservation")
			}
			family = append(family, Clone(member))
		}
	}
	sort.Slice(family[1:], func(i, j int) bool { return family[i+1].ID < family[j+1].ID })
	return family, nil
}

func (c *Core) taskContinuationFrozen(r Run) error {
	if saved, exists := c.runs[r.ID]; exists {
		r = saved // Mutation cannot discard a stored child's ownership link.
	}
	root := r
	if r.Task != nil {
		var exists bool
		root, exists = c.runs[r.Task.Identity.RootRunID]
		if !exists {
			return errors.New("source family root is unavailable")
		}
	}
	if root.ContinuedBy != nil {
		return fmt.Errorf("retained source family is frozen by task continuation %s; use that run", root.ContinuedBy.RunID)
	}
	return nil
}

func (c *Core) taskContinuationArtifact(r Run, artifact Artifact) bool {
	root, err := c.graphRoot(r)
	if err != nil || root.TaskGraph.Lineage == nil {
		return false
	}
	lineage := root.TaskGraph.Lineage
	origin, exists := c.runs[lineage.OriginRunID]
	if !exists || origin.ContinuedBy == nil || origin.ContinuedBy.RunID != root.ID || !reflect.DeepEqual(origin.ContinuedBy.Lineage, *lineage) || artifact.RunID != origin.ID || artifact.ProjectID != origin.ProjectID {
		return false
	}
	return artifact.ID == lineage.Source.PatchID || artifact.ID == lineage.CheckID
}

func taskContinuationRecord(r Run) error {
	if audit := r.ContinuedBy; audit != nil {
		if r.Task != nil || r.TaskGraph != nil || r.Status != "Failed" || !r.ResourcesReleased || audit.RunID == "" || audit.RunID == r.ID || audit.RequestID == "" || audit.GrantedAt.IsZero() || audit.Lineage.OriginRunID != r.ID || audit.Lineage.OriginProjectID != r.ProjectID || audit.Lineage.OriginRevision+1 != r.Revision || audit.Lineage.MaxAttempts != r.Process.MaxAttempts && r.Process.MaxAttempts != 0 {
			return errors.New("retained task continuation audit differs from its frozen root")
		}
	}
	if graph := r.TaskGraph; graph != nil && graph.Lineage != nil {
		lineage := graph.Lineage
		if graph.RootRunID != r.ID || graph.ParentRunID != "" || lineage.OriginRunID == "" || lineage.OriginRunID == r.ID || lineage.OriginProjectID == "" || lineage.OriginRevision < 1 || lineage.PriorAttempts < 1 || lineage.PriorAttempts >= lineage.MaxAttempts || lineage.Source.WorkDir != "" || !frontendRecheckHex(lineage.PatchSHA256, 64) || !frontendRecheckHex(lineage.CheckSHA256, 64) || r.Process.MaxAttempts != 0 && r.Process.MaxAttempts != lineage.MaxAttempts {
			return errors.New("retained task lineage differs from its general-task root")
		}
	}
	return nil
}

func (c *Core) validateTaskContinuationLinks() error {
	for _, r := range c.runs {
		if r.ContinuedBy != nil {
			target, exists := c.runs[r.ContinuedBy.RunID]
			if !exists || target.TaskGraph == nil || target.TaskGraph.Lineage == nil || !reflect.DeepEqual(*target.TaskGraph.Lineage, r.ContinuedBy.Lineage) {
				return errors.New("retained task continuation target or immutable lineage is unavailable")
			}
			family, err := c.legacyContinuationFamily(r)
			if err != nil || c.familyAttempts(r) != r.ContinuedBy.Lineage.PriorAttempts {
				return errors.New("retained task continuation historical debit differs")
			}
			for _, member := range family {
				if !c.collectionSettled(member) || !member.ResourcesReleased || member.CleanupError != "" || member.CleanupBlocked {
					return errors.New("retained task continuation origin is no longer settled")
				}
			}
		}
		if r.TaskGraph != nil && r.TaskGraph.Lineage != nil {
			origin, exists := c.runs[r.TaskGraph.Lineage.OriginRunID]
			if !exists || origin.ContinuedBy == nil || origin.ContinuedBy.RunID != r.ID || !reflect.DeepEqual(origin.ContinuedBy.Lineage, *r.TaskGraph.Lineage) || r.Process.MaxAttempts != origin.Process.MaxAttempts {
				return errors.New("general-task continuation origin is unavailable or its allowance differs")
			}
		}
	}
	return nil
}

// ValidateTaskContinuation authenticates the atomic write against the complete
// authoritative old family. The transaction supplies its saved rows, not a
// caller's claimed attempt count. Original definitions/history remain exact.
func ValidateTaskContinuation(origin, target Run, family []Run, receipt *Receipt) error {
	bad := errors.New("invalid atomic retained task continuation")
	audit, graph := origin.ContinuedBy, target.TaskGraph
	if audit == nil || graph == nil || graph.Lineage == nil || graph.RootRunID != target.ID || graph.ParentRunID != "" || target.Process.Tasks == nil || len(target.Attempts) != 0 || target.Status != "Queued" || target.Revision != 1 || target.ID == origin.ID || receipt == nil || receipt.Kind != "continue_task" || receipt.RunID != target.ID || receipt.Key != audit.RequestID || audit.RunID != target.ID || !reflect.DeepEqual(audit.Lineage, *graph.Lineage) || len(family) == 0 || family[0].ID != origin.ID {
		return bad
	}
	prior := family[0]
	lineage := audit.Lineage
	var req ContinueTaskRequest
	observedRevision := int64(0)
	for _, member := range family {
		observedRevision += member.Revision
	}
	if json.Unmarshal(receipt.Args, &req) != nil || req.RequestID != receipt.Key || req.RunID != prior.ID || req.Revision != observedRevision || req.Project != target.ProjectID || req.Process != target.Process.ID || req.SourceAttemptID != lineage.Source.AttemptID || req.CheckAttemptID != lineage.CheckAttemptID || req.PatchSHA256 != lineage.PatchSHA256 || req.CheckSHA256 != lineage.CheckSHA256 || !reflect.DeepEqual(req.Inputs, target.Inputs) {
		return bad
	}
	if prior.ContinuedBy != nil || prior.Task != nil || prior.TaskGraph != nil || prior.Status != "Failed" || prior.CancelRequested || prior.Decision != nil || prior.Process.ID != "deliver-planned-frontend" || prior.Revision != lineage.OriginRevision || lineage.OriginRunID != prior.ID || lineage.OriginProjectID != prior.ProjectID || target.Process.MaxAttempts != prior.Process.MaxAttempts || lineage.MaxAttempts != prior.Process.MaxAttempts || target.Process.Inputs["patch"] != "artifact" || target.Process.Inputs["checks"] != "artifact" || target.Inputs["patch"].ArtifactID != lineage.Source.PatchID || target.Inputs["checks"].ArtifactID != lineage.CheckID || !frontendRecheckHex(lineage.PatchSHA256, 64) || !frontendRecheckHex(lineage.CheckSHA256, 64) || lineage.Source.WorkDir != "" || audit.GrantedAt.IsZero() || origin.Revision != prior.Revision+1 {
		return bad
	}
	unchanged := Clone(origin)
	unchanged.ContinuedBy, unchanged.Revision, unchanged.Updated = nil, prior.Revision, prior.Updated
	if !reflect.DeepEqual(unchanged, prior) {
		return errors.New("continuation would modify original source work beyond its immutable audit")
	}
	view := &Core{runs: map[string]Run{}}
	seen := map[string]bool{}
	for _, member := range family {
		if seen[member.ID] || !member.Terminal() || !allAttemptsReleased(member) || !member.ResourcesReleased || member.Active() || member.CleanupBlocked || member.CleanupError != "" {
			return bad
		}
		seen[member.ID] = true
		view.runs[member.ID] = member
	}
	matched, err := view.legacyContinuationFamily(prior)
	if err != nil || len(matched) != len(family) || view.familyAttempts(prior) != lineage.PriorAttempts || lineage.PriorAttempts <= 0 || lineage.PriorAttempts >= lineage.MaxAttempts {
		return bad
	}
	source, check, err := TaskContinuationEvidence(prior, lineage.Source.AttemptID, lineage.CheckAttemptID)
	source.WorkDir = ""
	grant := prior.FrontendCheckRecovery
	if err != nil || source != lineage.Source || check.ID+"/result" != lineage.CheckID || grant == nil || grant.RequestID != lineage.SerialGrantRequestID || grant.HelperSHA256 != lineage.HelperSHA256 || grant.PatchSHA256 != lineage.PatchSHA256 {
		return bad
	}
	return nil
}
