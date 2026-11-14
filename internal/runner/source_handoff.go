package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// Scope clarification is an owner-adjudicated handoff fact, not a change to the
// frozen request, role, permissions or the caller's later review obligation.
const SourceHandoffScope = "Perform only this assigned source task. The original request is requirements and context for it. A review explicitly assigned to the caller after implementation remains the caller's task; do not treat that later review or its named tools/skills as a prerequisite for this source-only handoff. Report unexecuted checks accurately; ordinary downstream checks and independent review still decide acceptance."

type AdjudicateSourceHandoffRequest struct {
	RecoverUnpublishedCardRequest
	ReasonSHA256 string `json:"reason_sha256"`
}

type SourceHandoffAssessment struct {
	UnpublishedCardAssessment
	ReasonSHA256 string `json:"reason_sha256"`
	Scope        string `json:"scope"`
}

type SourceHandoffAudit struct {
	Request              AdjudicateSourceHandoffRequest `json:"request"`
	Assessment           UnpublishedCardAssessment      `json:"assessment"`
	Scope                string                         `json:"scope"`
	OriginalRootReason   string                         `json:"original_root_reason"`
	OriginalGroupFailure string                         `json:"original_group_failure"`
	OriginalChildReason  string                         `json:"original_child_reason"`
	GrantedAt            time.Time                      `json:"granted_at"`
}

type SourceHandoffStore interface {
	SaveSourceHandoff(context.Context, Run, Run, Run, *Receipt) error
}

func recoveryDigestRawText(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

// Preflight observes and authenticates source only. It grants no source quality
// acceptance, editing visit, checkout restoration or dispatch.
func (c *Core) CheckSourceHandoff(req AdjudicateSourceHandoffRequest) (SourceHandoffAssessment, error) {
	c.mu.Lock()
	a, child, err := c.planCardSourceRecovery(req.RecoverUnpublishedCardRequest, &req)
	validator, ok := c.executor.(UnpublishedCardRecoveryValidator)
	c.mu.Unlock()
	if err != nil {
		return SourceHandoffAssessment{}, err
	}
	if !ok {
		return SourceHandoffAssessment{}, errors.New("executor cannot authenticate source handoff")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()
	if err = validator.ValidateUnpublishedCardRecovery(ctx, child, a); err != nil {
		return SourceHandoffAssessment{}, fmt.Errorf("source handoff: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current, _, err := c.planCardSourceRecovery(req.RecoverUnpublishedCardRequest, &req)
	if err != nil {
		return SourceHandoffAssessment{}, err
	}
	if !reflect.DeepEqual(current, a) {
		return SourceHandoffAssessment{}, errors.New("source handoff family changed during validation")
	}
	return SourceHandoffAssessment{UnpublishedCardAssessment: a, ReasonSHA256: req.ReasonSHA256, Scope: SourceHandoffScope}, nil
}

func (c *Core) AdjudicateSourceHandoff(req AdjudicateSourceHandoffRequest) (Run, error) {
	args, _ := json.Marshal(req)
	c.mu.Lock()
	replay, found, err := c.replay(req.RequestID, "adjudicate_source_handoff", args)
	c.mu.Unlock()
	if found || err != nil {
		return replay, err
	}
	if req.BindingsSHA256 == "" {
		return Run{}, errors.New("exact source handoff preflight bindings_sha256 is required")
	}
	assessment, err := c.CheckSourceHandoff(req)
	if err != nil {
		c.mu.Lock()
		replay, found, replayErr := c.replay(req.RequestID, "adjudicate_source_handoff", args)
		c.mu.Unlock()
		if found || replayErr != nil {
			return replay, replayErr
		}
		return Run{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if replay, found, err := c.replay(req.RequestID, "adjudicate_source_handoff", args); found || err != nil {
		return replay, err
	}
	current, child, err := c.planCardSourceRecovery(req.RecoverUnpublishedCardRequest, &req)
	if err != nil {
		return Run{}, err
	}
	if !reflect.DeepEqual(current, assessment.UnpublishedCardAssessment) {
		return Run{}, errors.New("source handoff family changed before commit")
	}
	saver, ok := c.store.(SourceHandoffStore)
	if !ok {
		return Run{}, errors.New("store cannot atomically save source handoff")
	}
	root := Clone(c.runs[req.RunID])
	group := root.TaskGroups[child.Task.Identity.GroupStep]
	root.SourceHandoff = &SourceHandoffAudit{
		Request: req, Assessment: Clone(current), Scope: SourceHandoffScope,
		OriginalRootReason: root.Reason, OriginalGroupFailure: group.Failure,
		OriginalChildReason: child.Reason, GrantedAt: time.Now().UTC(),
	}
	root.RecordFormat = 7
	root.Status, root.Reason = "Running", "owner-adjudicated source handoff"
	root.ResourcesReleased = false
	group.Done, group.Failure = false, ""
	// Freeze ordinary pin inputs from the original blocked completion. Its
	// result and attempt remain blocked and unchanged; no model success is made up.
	child.StepInputs, err = bindings(child, child.Process.Steps[2].Inputs)
	if err != nil {
		return Run{}, err
	}
	child.Step, child.Visit = 2, child.Visit+1
	child.Status, child.Reason = "Queued", "owner-adjudicated source; normal pin required"
	child.ResourcesReleased = false
	pending := Clone(c.runs[current.Unstarted[0].RunID])
	pending.Status, pending.Reason = "Waiting", "waiting for declared task prerequisites"
	pending.CancelRequested, pending.ResourcesReleased = false, false
	now := time.Now().UTC()
	for _, r := range []*Run{&root, &child, &pending} {
		r.Revision++
		r.Updated = now
		if err = admitRunRecord(*r, c.disk.Policy.Database.MaxRecord); err != nil {
			return Run{}, err
		}
	}
	receipt := &Receipt{Key: req.RequestID, Kind: "adjudicate_source_handoff", Args: args, RunID: root.ID}
	if err = saver.SaveSourceHandoff(c.ctx, root, child, pending, receipt); err != nil {
		return Run{}, err
	}
	c.runs[root.ID], c.runs[child.ID], c.runs[pending.ID] = root, child, pending
	close(c.changed)
	c.changed = make(chan struct{})
	c.Wake()
	return Clone(root), nil
}

func (c *Core) sourceHandoffCard(r Run) *SourceHandoffAudit {
	if r.Task != nil {
		if root, ok := c.runs[r.Task.Identity.RootRunID]; ok && root.SourceHandoff != nil && root.SourceHandoff.Request.ChildRunID == r.ID {
			return root.SourceHandoff
		}
	}
	return nil
}
