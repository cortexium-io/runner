package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
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
	ReasonSHA256       string               `json:"reason_sha256"`
	Scope              string               `json:"scope"`
	ProposedStateBytes map[string]int       `json:"proposed_state_bytes"`
	Proof              CardRecoveryArtifact `json:"proof"`
}

type SourceHandoffAudit struct {
	Request              AdjudicateSourceHandoffRequest `json:"request"`
	Assessment           *UnpublishedCardAssessment     `json:"assessment,omitempty"`
	Proof                *CardRecoveryArtifact          `json:"proof,omitempty"`
	Scope                string                         `json:"scope"`
	OriginalRootReason   string                         `json:"original_root_reason"`
	OriginalGroupFailure string                         `json:"original_group_failure"`
	OriginalChildReason  string                         `json:"original_child_reason"`
	GrantedAt            time.Time                      `json:"granted_at"`
}

type SourceHandoffStore interface {
	SaveSourceHandoff(context.Context, Run, Run, Run, UnpublishedCardAssessment, *Receipt) error
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
	// A missing request ID is permitted for read-only discovery. Reserve its
	// maximum supported length so admission cannot add unbudgeted receipt data.
	proposed := req
	if proposed.RequestID == "" {
		proposed.RequestID = strings.Repeat("\x00", 128) // Worst-case JSON escaping.
	}
	if len(proposed.RequestID) > 128 {
		return SourceHandoffAssessment{}, errors.New("source handoff request_id exceeds 128 bytes")
	}
	proposed.BindingsSHA256 = a.BindingsSHA256
	root, child, pending, proof, err := c.proposeSourceHandoff(proposed, a, handoffSizeTime)
	if err != nil {
		return SourceHandoffAssessment{}, err
	}
	if err = c.checkSourceHandoffProofStorage(root, proof); err != nil {
		return SourceHandoffAssessment{}, err
	}
	if validator, ok := c.executor.(AssignmentValidator); ok {
		pin, err := c.assignment(child, Attempt{
			ID:       "attempt_" + strings.Repeat("A", 26),
			Step:     child.Step,
			Visit:    child.Visit,
			Profile:  child.Profiles[child.Process.Steps[child.Step].Role],
			Deadline: handoffSizeTime,
		})
		if err != nil {
			return SourceHandoffAssessment{}, err
		}
		pin.SourceHandoff = &a
		if err = validator.ValidateAssignment(pin); err != nil {
			return SourceHandoffAssessment{}, fmt.Errorf("source handoff pin record: %w", err)
		}
	}
	sizes := map[string]int{}
	for _, r := range []Run{root, child, pending} {
		state, _, _ := MarshalRunRecord(r)
		sizes[r.ID] = len(state)
	}
	return SourceHandoffAssessment{
		UnpublishedCardAssessment: a,
		ReasonSHA256:              req.ReasonSHA256,
		Scope:                     SourceHandoffScope,
		ProposedStateBytes:        sizes,
		Proof:                     *root.SourceHandoff.Proof,
	}, nil
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
	root, child, pending, proof, err := c.proposeSourceHandoff(req, current, time.Now().UTC())
	if err != nil {
		return Run{}, err
	}
	if err = c.checkSourceHandoffProofStorage(root, proof); err != nil {
		return Run{}, err
	}
	name := "artifacts/" + root.SourceHandoff.Proof.ID
	if existing, readErr := c.disk.Read(name, c.disk.Policy.MaxArtifact); errors.Is(readErr, os.ErrNotExist) {
		if err = c.disk.Write(name, proof, c.disk.Policy.MaxArtifact); err != nil {
			return Run{}, err
		}
	} else if readErr != nil || !bytes.Equal(existing, proof) {
		return Run{}, errors.New("source handoff proof changed before commit")
	}
	receipt := &Receipt{Key: req.RequestID, Kind: "adjudicate_source_handoff", Args: args, RunID: root.ID}
	if err = saver.SaveSourceHandoff(c.ctx, root, child, pending, current, receipt); err != nil {
		return Run{}, err
	}
	c.runs[root.ID], c.runs[child.ID], c.runs[pending.ID] = root, child, pending
	close(c.changed)
	c.changed = make(chan struct{})
	c.Wake()
	return Clone(root), nil
}

// The maximum serialized UTC timestamp length makes read-only sizing at least
// as strict as the eventual grant; no clock-dependent proof bytes are stored.
var handoffSizeTime = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)

func (c *Core) proposeSourceHandoff(req AdjudicateSourceHandoffRequest, a UnpublishedCardAssessment, now time.Time) (root, child, pending Run, proof []byte, err error) {
	root, child = Clone(c.runs[req.RunID]), Clone(c.runs[req.ChildRunID])
	group := root.TaskGroups[child.Task.Identity.GroupStep]
	proof, err = json.Marshal(a)
	if err != nil {
		return
	}
	digest := recoveryDigestRawText(string(proof))
	root.SourceHandoff = &SourceHandoffAudit{
		Request: req,
		Scope:   SourceHandoffScope,
		Proof: &CardRecoveryArtifact{
			ID:     group.AttemptID + "/source-handoff-" + digest + ".json",
			RunID:  root.ID,
			Bytes:  int64(len(proof)),
			SHA256: digest,
		},
		OriginalRootReason:   root.Reason,
		OriginalGroupFailure: group.Failure,
		OriginalChildReason:  child.Reason,
		GrantedAt:            now,
	}
	root.RecordFormat = 8
	root.Status, root.Reason = "Running", "owner-adjudicated source handoff"
	root.ResourcesReleased = false
	group.Done, group.Failure = false, ""
	// Freeze ordinary pin inputs from the original blocked completion. Its
	// result and attempt remain blocked and unchanged; no model success is made up.
	child.StepInputs, err = bindings(child, child.Process.Steps[2].Inputs)
	if err != nil {
		return
	}
	child.Step, child.Visit = 2, child.Visit+1
	child.Status, child.Reason = "Queued", "owner-adjudicated source; normal pin required"
	child.ResourcesReleased = false
	pending = Clone(c.runs[a.Unstarted[0].RunID])
	pending.Status, pending.Reason = "Waiting", "waiting for declared task prerequisites"
	pending.CancelRequested, pending.ResourcesReleased = false, false
	for _, r := range []*Run{&root, &child, &pending} {
		r.Revision++
		r.Updated = now
		if err = admitRunRecord(*r, c.disk.Policy.Database.MaxRecord); err != nil {
			err = fmt.Errorf("source handoff record %s: %w", r.ID, err)
			return
		}
	}
	return
}

func (c *Core) checkSourceHandoffProofStorage(root Run, proof []byte) error {
	ref := root.SourceHandoff.Proof
	if len(proof) > int(c.disk.Policy.MaxArtifact) {
		return fmt.Errorf("%w: source handoff proof exceeds max_artifact", ErrCapacity)
	}
	if old, err := c.store.Artifact(c.ctx, ref.ID); err == nil {
		if old.RunID != root.ID || old.ProjectID != root.ProjectID || old.Bytes != ref.Bytes {
			return errors.New("source handoff proof artifact ownership differs")
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if old, err := c.disk.Read("artifacts/"+ref.ID, c.disk.Policy.MaxArtifact); err == nil {
		if _, err := c.disk.ArtifactSize(ref.ID); err != nil {
			return err
		}
		if !bytes.Equal(old, proof) {
			return errors.New("source handoff proof artifact differs; preserve it")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	free, err := FreeBytes(c.disk.Root)
	if err != nil {
		return err
	}
	if free < int64(c.disk.Policy.MinFree)+int64(len(proof)) || c.disk.Used()+int64(len(proof)) > int64(c.disk.Policy.MaxManaged-c.disk.Policy.Database.MaxSize)-4096 {
		return fmt.Errorf("%w: source handoff proof storage reserve", ErrCapacity)
	}
	return nil
}

func (c *Core) readSourceHandoffProof(root Run) (UnpublishedCardAssessment, error) {
	audit := root.SourceHandoff
	if root.RecordFormat == 7 && audit.Assessment != nil {
		return Clone(*audit.Assessment), nil
	}
	if root.RecordFormat != 8 || audit.Proof == nil || audit.Assessment != nil {
		return UnpublishedCardAssessment{}, errors.New("source handoff proof representation differs")
	}
	ref := audit.Proof
	registered, err := c.store.Artifact(c.ctx, ref.ID)
	if err != nil || registered.RunID != root.ID || registered.ProjectID != root.ProjectID || registered.Bytes != ref.Bytes {
		return UnpublishedCardAssessment{}, errors.New("source handoff proof ownership differs or is missing")
	}
	if _, err := c.disk.ArtifactSize(ref.ID); err != nil {
		return UnpublishedCardAssessment{}, err
	}
	raw, err := c.disk.Read("artifacts/"+ref.ID, c.disk.Policy.MaxArtifact)
	if err != nil || int64(len(raw)) != ref.Bytes || recoveryDigestRawText(string(raw)) != ref.SHA256 {
		return UnpublishedCardAssessment{}, errors.New("source handoff proof bytes differ or are missing")
	}
	var a UnpublishedCardAssessment
	if json.Unmarshal(raw, &a) != nil || a.RunID != root.ID || a.ChildRunID != audit.Request.ChildRunID || a.AttemptID != audit.Request.AttemptID || a.BindingsSHA256 != audit.Request.BindingsSHA256 || ref.ID != a.GroupAttemptID+"/source-handoff-"+ref.SHA256+".json" {
		return a, errors.New("source handoff proof identity differs")
	}
	return a, nil
}

func (c *Core) sourceHandoffCard(r Run) *SourceHandoffAudit {
	if r.Task != nil {
		if root, ok := c.runs[r.Task.Identity.RootRunID]; ok && root.SourceHandoff != nil && root.SourceHandoff.Request.ChildRunID == r.ID {
			return root.SourceHandoff
		}
	}
	return nil
}
