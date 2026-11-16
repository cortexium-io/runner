package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"time"
)

// TaskFollowupRequest starts a new configured goal from a completed general
// task's retained workspace and explicitly selected evidence. It does not reopen
// that result, clone its tasks or carry a new budget into the completed family.
type TaskFollowupRequest struct {
	RunID    string `json:"run_id"`
	Revision int64  `json:"revision"`
}

type TaskFollowupLineage struct {
	RunID     string `json:"run_id"`
	Revision  int64  `json:"revision"`
	RequestID string `json:"request_id"`
}

type TaskFollowupStore interface {
	SaveTaskFollowup(context.Context, Run, []Run, *Receipt) error
}

// startTaskFollowup runs under Core.mu and uses the ordinary Start receipt.
// Parent-family comparison is repeated in the new-run insertion transaction.
func (c *Core) startTaskFollowup(req StartRequest, args []byte) (Run, error) {
	parent, exists := c.runs[req.Followup.RunID]
	if !exists {
		return Run{}, ErrNotFound
	}
	_, family, reserved, err := c.graphFamily(parent)
	if err != nil || reserved != 0 {
		return Run{}, errors.New("follow-up needs a complete retained general-task family")
	}
	if err := validateTaskFollowupParent(parent, family, req); err != nil {
		return Run{}, err
	}
	for _, member := range family {
		if !c.graphOwnSettled(member) {
			return Run{}, errors.New("follow-up requires settled family execution")
		}
	}
	if _, err := c.disk.Read(familyCollectionDirectory+"/"+parent.ID+".json", c.disk.Policy.Database.MaxRecord); !errors.Is(err, os.ErrNotExist) {
		return Run{}, errors.New("follow-up parent has pending retention ownership; reconcile it first")
	}
	imports := map[string]Artifact{}
	for _, value := range req.Inputs {
		if value.ArtifactID == "" {
			continue
		}
		artifact, err := c.store.Artifact(c.ctx, value.ArtifactID)
		if err != nil {
			return Run{}, errors.New("follow-up input artifact is unavailable")
		}
		if artifact.ProjectID != req.Project {
			if !TaskFollowupAllowsImport(parent, artifact) || !c.graphArtifactAllowed(parent, artifact.ID) {
				return Run{}, errors.New("follow-up may inherit only the parent's authenticated input artifacts")
			}
			imports[artifact.ID] = artifact
		}
	}
	r, err := c.newRun(req, imports)
	if err != nil {
		return Run{}, err
	}
	if r.TaskGraph == nil || r.Repository != parent.Repository {
		return Run{}, errors.New("follow-up requires a configured general process in the same canonical project workspace")
	}
	r.Followup = &TaskFollowupLineage{
		RunID:     parent.ID,
		Revision:  req.Followup.Revision,
		RequestID: req.RequestID,
	}
	r.Revision, r.Updated = 1, time.Now().UTC()
	if err := admitRunRecord(r, c.disk.Policy.Database.MaxRecord); err != nil {
		return Run{}, err
	}
	receipt := &Receipt{Key: req.RequestID, Kind: "start", Args: args, RunID: r.ID}
	if err := ValidateTaskFollowup(r, family, receipt); err != nil {
		return Run{}, err
	}
	store, supported := c.store.(TaskFollowupStore)
	if !supported {
		return Run{}, errors.New("store cannot atomically admit a linked task follow-up")
	}
	if err := store.SaveTaskFollowup(c.ctx, r, family, receipt); err != nil {
		return c.reconcileStartFailure(req, args, err)
	}
	c.runs[r.ID] = r
	close(c.changed)
	c.changed = make(chan struct{})
	c.Wake()
	return c.outputRun(r), nil
}

func (c *Core) reconcileStartFailure(req StartRequest, args []byte, original error) (Run, error) {
	if saved, replay, err := c.replay(req.RequestID, "start", args); replay {
		return c.outputRun(saved), nil
	} else if err != nil {
		return Run{}, fmt.Errorf("start commit failed: %w; reconciliation: %v", original, err)
	}
	return Run{}, original
}

// A committed Start may lose its acknowledgement before Core adopts the new
// root. Rejoin that exact pristine admission; never replace current in-memory
// executions or dispatch a second root from the same owner request.
func (c *Core) restoreStartedRun(receipt Receipt) (Run, error) {
	saved, err := c.store.Load(c.ctx)
	if err != nil {
		return Run{}, err
	}
	var r Run
	for _, candidate := range saved {
		if candidate.ID == receipt.RunID {
			r = candidate
			break
		}
	}
	var req StartRequest
	if r.ID == "" || receipt.Kind != "start" || json.Unmarshal(receipt.Args, &req) != nil || req.RequestID != receipt.Key || req.Project != r.ProjectID || req.Process != "" && req.Process != r.Process.ID || !reflect.DeepEqual(req.Inputs, r.Inputs) || r.Status != "Queued" || r.Revision != 1 || r.Step != 0 || r.Visit != 0 || r.Task != nil || len(r.TaskGroups) != 0 || len(r.Attempts) != 0 || r.Decision != nil || r.ResourcesReleased || r.CleanupBlocked || r.CleanupError != "" || r.SourceHandoff != nil || r.ContinuedBy != nil {
		return Run{}, errors.New("saved start receipt has no matching pristine root to reconcile")
	}
	if req.SourceRef != "" && (r.Source == nil || r.Source.Ref != req.SourceRef) {
		return Run{}, errors.New("saved start source identity differs")
	}
	if r.TaskGraph != nil && (r.TaskGraph.RootRunID != r.ID || r.TaskGraph.ParentRunID != "" || len(r.TaskGraph.Children) != 0 || r.TaskGraph.Phase != "" || r.TaskGraph.Paused) {
		return Run{}, errors.New("saved start root already has task execution state; reopen for reconciliation")
	}
	if req.Followup != nil {
		parent, exists := c.runs[req.Followup.RunID]
		if !exists {
			return Run{}, errors.New("saved follow-up parent is not loaded")
		}
		_, family, reserved, err := c.graphFamily(parent)
		if err != nil || reserved != 0 || ValidateTaskFollowup(r, family, &receipt) != nil {
			return Run{}, errors.New("saved follow-up parent or admission differs")
		}
	} else if r.Followup != nil || r.TaskGraph != nil && r.TaskGraph.Lineage != nil {
		return Run{}, errors.New("ordinary start receipt cannot introduce follow-up or continuation authority")
	}
	if err := c.readTaskDefinitionProofs(r); err != nil {
		return Run{}, err
	}
	c.runs[r.ID] = r
	c.maintenanceError = ""
	close(c.changed)
	c.changed = make(chan struct{})
	c.Wake()
	return r, nil
}

func validateTaskFollowupParent(parent Run, family []Run, req StartRequest) error {
	if req.Followup == nil || req.Followup.RunID != parent.ID || req.Followup.Revision < 1 || req.Project == "" || req.Process == "" || req.SourceRef != "" {
		return errors.New("follow-up needs the exact parent family revision and an explicit configured target without source_ref")
	}
	if parent.Status != "Completed" || parent.TaskGraph == nil || parent.TaskGraph.RootRunID != parent.ID || parent.TaskGraph.ParentRunID != "" || parent.Task != nil || parent.ProjectID != req.Project {
		return errors.New("follow-up requires a completed general-task root in the same project")
	}
	view := &Core{runs: map[string]Run{}}
	var revision int64
	for _, member := range family {
		if view.runs[member.ID].ID != "" || member.Revision < 0 || revision > math.MaxInt64-member.Revision {
			return errors.New("follow-up family identity or revision differs")
		}
		view.runs[member.ID] = member
		revision += member.Revision
		if !member.Terminal() || member.Decision != nil || member.Active() || !allAttemptsReleased(member) || !member.ResourcesReleased || member.CleanupBlocked || member.CleanupError != "" {
			return errors.New("follow-up requires complete family execution and cleanup settlement")
		}
	}
	_, members, reserved, err := view.graphFamily(parent)
	if err != nil || reserved != 0 || len(members) != len(family) || revision != req.Followup.Revision {
		return errors.New("follow-up parent family changed; inspect the current completed result")
	}
	return nil
}

// TaskFollowupAllowsImport preserves a parent's existing cross-project grants;
// it never turns an arbitrary artifact in that project into follow-up input.
// A parent's own follow-up imports were checked atomically at its admission and
// its input map is immutable, so the same exact references can be carried on.
func TaskFollowupAllowsImport(parent Run, artifact Artifact) bool {
	selected := false
	for _, value := range parent.Inputs {
		selected = selected || value.ArtifactID == artifact.ID
	}
	if !selected || artifact.ID == "" {
		return false
	}
	if artifact.ProjectID == parent.ProjectID || parent.Followup != nil {
		return true
	}
	if parent.TaskGraph == nil || parent.TaskGraph.Lineage == nil {
		return false
	}
	lineage := parent.TaskGraph.Lineage
	return artifact.RunID == lineage.OriginRunID && artifact.ProjectID == lineage.OriginProjectID && (artifact.ID == lineage.Source.PatchID || artifact.ID == lineage.CheckID)
}

func (c *Core) taskFollowupArtifact(r Run, artifact Artifact) bool {
	root, err := c.graphRoot(r)
	if err != nil || root.Followup == nil {
		return false
	}
	parent, exists := c.runs[root.Followup.RunID]
	return exists && TaskFollowupAllowsImport(parent, artifact) && TaskFollowupAllowsImport(root, artifact)
}

func taskFollowupRecord(r Run) error {
	if r.Followup == nil {
		return nil
	}
	link := r.Followup
	if r.RecordFormat != 9 || r.TaskGraph == nil || r.TaskGraph.RootRunID != r.ID || r.TaskGraph.ParentRunID != "" || r.TaskGraph.TaskID != "root" || r.TaskGraph.Lineage != nil || r.Task != nil || r.ContinuedBy != nil || link.RunID == "" || link.RunID == r.ID || link.Revision < 1 || len(link.RequestID) < 1 || len(link.RequestID) > 128 || r.Process.Tasks == nil || len(r.Process.Steps) != 1 || r.TaskGraph.Role != r.Process.Steps[0].Role || r.TaskGraph.Brief != graphGoal(r.Inputs) {
		return errors.New("follow-up lineage needs an immutable general-root parent and start receipt")
	}
	return nil
}

// ValidateTaskFollowup authenticates the owner request and its complete settled
// source family. The store repeats this against retained rows in its transaction.
func ValidateTaskFollowup(target Run, family []Run, receipt *Receipt) error {
	bad := errors.New("follow-up admission differs from its completed parent or owner start request")
	if taskFollowupRecord(target) != nil || target.Followup == nil || receipt == nil || receipt.Kind != "start" || receipt.Key != target.Followup.RequestID || receipt.RunID != target.ID || target.Status != "Queued" || target.Revision != 1 || target.Step != 0 || target.Visit != 0 || len(target.Attempts) != 0 || len(target.TaskGraph.Children) != 0 || target.Process.Tasks == nil || target.Created.After(target.Updated) {
		return bad
	}
	var req StartRequest
	if json.Unmarshal(receipt.Args, &req) != nil || req.RequestID != receipt.Key || req.Followup == nil || req.Followup.RunID != target.Followup.RunID || req.Followup.Revision != target.Followup.Revision || req.Project != target.ProjectID || req.Process != target.Process.ID || !reflect.DeepEqual(req.Inputs, target.Inputs) {
		return bad
	}
	var parent Run
	for _, member := range family {
		if member.ID == target.Followup.RunID {
			parent = member
		}
	}
	if parent.ID == "" || parent.ID == target.ID || parent.Repository != target.Repository || validateTaskFollowupParent(parent, family, req) != nil {
		return bad
	}
	return nil
}

func (c *Core) validateTaskFollowupLinks() error {
	for _, r := range c.runs {
		if r.Followup == nil {
			continue
		}
		if err := taskFollowupRecord(r); err != nil {
			return err
		}
		parent, exists := c.runs[r.Followup.RunID]
		if !exists || parent.Repository != r.Repository {
			return errors.New("follow-up parent or canonical workspace is unavailable")
		}
		_, family, reserved, err := c.graphFamily(parent)
		if err != nil || reserved != 0 {
			return errors.New("follow-up parent family is incomplete")
		}
		receipt, err := c.store.FindReceipt(c.ctx, r.Followup.RequestID)
		var req StartRequest
		if err != nil || receipt.Kind != "start" || receipt.RunID != r.ID || json.Unmarshal(receipt.Args, &req) != nil || req.RequestID != receipt.Key || req.Project != r.ProjectID || req.Followup == nil || *req.Followup != (TaskFollowupRequest{RunID: parent.ID, Revision: r.Followup.Revision}) || req.Process != r.Process.ID || !reflect.DeepEqual(req.Inputs, r.Inputs) || validateTaskFollowupParent(parent, family, req) != nil {
			return errors.New("follow-up owner receipt or completed parent changed")
		}
		seen := map[string]bool{r.ID: true}
		for ancestor := parent; ; {
			if seen[ancestor.ID] {
				return errors.New("follow-up ancestry is cyclic")
			}
			seen[ancestor.ID] = true
			if ancestor.Followup == nil {
				break
			}
			var ok bool
			ancestor, ok = c.runs[ancestor.Followup.RunID]
			if !ok {
				return errors.New("follow-up ancestor is unavailable")
			}
		}
		for _, value := range r.Inputs {
			if value.ArtifactID == "" {
				continue
			}
			artifact, err := c.store.Artifact(c.ctx, value.ArtifactID)
			if err != nil || artifact.ProjectID != r.ProjectID && !TaskFollowupAllowsImport(parent, artifact) {
				return fmt.Errorf("follow-up %s input authority is unavailable", r.ID)
			}
		}
	}
	return nil
}
