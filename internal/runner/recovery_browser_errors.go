package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"time"
)

type RecoverBrowserErrorsRequest struct {
	RequestID       string `json:"request_id"`
	RunID           string `json:"run_id"`
	AttemptID       string `json:"attempt_id"`
	Revision        int64  `json:"revision"`
	PublicationHead string `json:"publication_head"`
	Report          []byte `json:"report"`
}

// BrowserErrorRecovery records an evidence-only repair of the current owner
// handoff. The original qualification result and every execution limit remain.
type BrowserErrorRecovery struct {
	RequestID       string    `json:"request_id"`
	AttemptID       string    `json:"attempt_id"`
	ReportSHA256    string    `json:"report_sha256"`
	PublicationHead string    `json:"publication_head"`
	FeedbackSHA256  string    `json:"feedback_sha256"`
	GrantedAt       time.Time `json:"granted_at"`
}

type BrowserErrorRecoveryValidator interface {
	ValidateBrowserErrorRecovery(context.Context, Run, SourceResult, []byte, string) (json.RawMessage, error)
}

// RecoverBrowserErrors authenticates a retained report against the digest in
// the saved rejection, then restores only its bounded page-error diagnostic.
// It does not queue work, replenish a route, grant attempts, or rewrite history.
func (c *Core) RecoverBrowserErrors(req RecoverBrowserErrorsRequest) (Run, error) {
	if len(req.Report) == 0 || len(req.Report) > 128<<10 {
		return Run{}, errors.New("browser recovery requires a saved report of at most 128KiB")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(req.Report))
	// Persist the exact content identity, not a second copy of the private report.
	auditRequest := req
	auditRequest.Report = nil
	args, _ := json.Marshal(struct {
		RecoverBrowserErrorsRequest
		ReportSHA256 string `json:"report_sha256"`
	}{auditRequest, digest})
	c.mu.Lock()
	if r, ok, err := c.replay(req.RequestID, "recover_browser_errors", args); ok || err != nil {
		c.mu.Unlock()
		return r, err
	}
	r, source, revisions, err := c.browserErrorRecovery(req, digest)
	validator, supported := c.executor.(BrowserErrorRecoveryValidator)
	c.mu.Unlock()
	if err != nil {
		return Run{}, err
	}
	if !supported {
		return Run{}, errors.New("executor cannot authenticate browser error recovery")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	feedback, err := validator.ValidateBrowserErrorRecovery(ctx, Clone(r), source, req.Report, req.PublicationHead)
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if saved, ok, replayErr := c.replay(req.RequestID, "recover_browser_errors", args); ok || replayErr != nil {
		return saved, replayErr
	}
	if err != nil {
		return Run{}, fmt.Errorf("browser error recovery authority: %w", err)
	}
	current, _, currentRevisions, err := c.browserErrorRecovery(req, digest)
	if err != nil || !maps.Equal(revisions, currentRevisions) {
		return Run{}, errors.New("run or accepted cards changed during browser validation; inspect before recovery")
	}
	var identity struct {
		Status, Kind string
		ReportSHA256 string `json:"report_sha256"`
	}
	if len(feedback) > 768 || json.Unmarshal(feedback, &identity) != nil || identity.Status != "complete" || identity.Kind != "page-errors" || identity.ReportSHA256 != digest {
		return Run{}, errors.New("browser error recovery lacks complete bounded report-bound feedback")
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal(current.StepInputs["qualification"].Inline, &receipt); err != nil {
		return Run{}, err
	}
	receipt["browser_failure"] = feedback
	updated, err := json.Marshal(receipt)
	if err != nil {
		return Run{}, err
	}
	current.StepInputs["qualification"] = Value{Inline: updated}
	role := current.Roles[current.Process.Steps[current.Step].Role]
	if err := ValidateValues(current.StepInputs, role.Inputs); err != nil {
		return Run{}, err
	}
	current.BrowserErrorRecovery = &BrowserErrorRecovery{
		RequestID:       req.RequestID,
		AttemptID:       req.AttemptID,
		ReportSHA256:    digest,
		PublicationHead: req.PublicationHead,
		FeedbackSHA256:  fmt.Sprintf("%x", sha256.Sum256(feedback)),
		GrantedAt:       time.Now().UTC(),
	}
	state, definition, err := MarshalRunRecord(current)
	if err != nil || len(state)+2048 > int(c.disk.Policy.Database.MaxRecord) || len(definition) > int(c.disk.Policy.Database.MaxRecord) {
		return Run{}, errors.New("browser recovery lacks existing history headroom")
	}
	receiptAudit := Receipt{Key: req.RequestID, Kind: "recover_browser_errors", Args: args, RunID: current.ID}
	if err := c.persist(current, nil, &receiptAudit); err != nil {
		return Run{}, err
	}
	return Clone(current), nil
}

func (c *Core) browserErrorRecovery(req RecoverBrowserErrorsRequest, digest string) (Run, SourceResult, map[string]int64, error) {
	r, ok := c.runs[req.RunID]
	if !ok {
		return Run{}, SourceResult{}, nil, ErrNotFound
	}
	r = Clone(r)
	reject := func(reason string) (Run, SourceResult, map[string]int64, error) {
		return Run{}, SourceResult{}, nil, errors.New(reason)
	}
	if r.Process.ID != "deliver-planned-retained-frontend" || r.Task != nil || r.Source == nil ||
		r.Status != "Waiting" || r.CancelRequested || r.Active() || r.Decision != nil || c.inFlight[r.ID] ||
		r.CleanupBlocked || r.CleanupError != "" || r.BrowserErrorRecovery != nil || !frontendRecheckHex(req.PublicationHead, 40) {
		return reject("browser recovery requires the exact ungranted settled retained frontend root")
	}
	if r.Step < 0 || r.Step >= len(r.Process.Steps) || r.Process.Steps[r.Step].ID != "owner" || c.frontendRecheckSettledAttempts(r) != nil {
		return reject("browser recovery requires the existing settled source owner handoff")
	}
	if len(r.Attempts) == 0 {
		return reject("browser recovery lacks its saved qualification attempt")
	}
	// The source backedge has already advanced the visit. Current deliberately
	// excludes this superseded attempt, whose original rejection is the proof.
	a := &r.Attempts[len(r.Attempts)-1]
	if a.ID != req.AttemptID || a.Step <= r.Step || a.Step >= len(r.Process.Steps) || r.Process.Steps[a.Step].ID != "qualify" ||
		!a.Superseded || a.TimedOut || a.StopRequested || a.End == nil || a.End.Kind != "returned" || a.End.Retryable || a.End.Result == nil || a.End.Result.Outcome != "changes_requested" {
		return reject("browser recovery requires the exact saved returned qualification rejection")
	}
	route, eligible := sourceCorrectionRoute(r, a.Step)
	if !eligible || route.Next != "owner" || r.RouteUses["qualify/changes_requested"] < 1 {
		return reject("browser recovery cannot create a source correction route")
	}
	qualification := a.End.Result.Outputs["receipt"]
	var saved struct {
		Action, Key    string
		BrowserFailure struct {
			Status       string `json:"status"`
			ReportSHA256 string `json:"report_sha256"`
		} `json:"browser_failure"`
	}
	// Decode explicit wire names for the opaque identity; never accept an
	// enriched/replaced qualification or mutate the historical result.
	var identity struct {
		RunID  string `json:"run_id"`
		Source string `json:"source_ref"`
	}
	if qualification.ArtifactID != "" || !reflect.DeepEqual(qualification, r.StepInputs["qualification"]) ||
		json.Unmarshal(qualification.Inline, &saved) != nil || json.Unmarshal(qualification.Inline, &identity) != nil ||
		saved.Action != "qualify" || saved.Key != a.ID || identity.RunID != r.ID || identity.Source != r.Source.Ref ||
		saved.BrowserFailure.Status != "missing" || saved.BrowserFailure.ReportSHA256 != digest {
		return reject("saved browser report digest or current qualification ownership differs")
	}
	revisions, err := c.frontendRecheckCards(r)
	if err != nil {
		return reject(err.Error())
	}
	var observedRevision int64
	for _, revision := range revisions {
		observedRevision += revision
	}
	if req.Revision != observedRevision {
		return reject("browser recovery run or accepted card revision differs; inspect before recovery")
	}
	for i := len(r.Attempts) - 2; i >= 0; i-- {
		owner := r.Attempts[i]
		if owner.Step != r.Step || owner.Source == nil {
			continue
		}
		source := *owner.Source
		if owner.End == nil || owner.End.Kind != "returned" || owner.End.Result == nil || owner.End.Result.Outcome != "ready" ||
			source.AttemptID != owner.ID || source.PatchID != owner.ID+"/changes.patch" || owner.End.Result.Outputs["patch"].ArtifactID != source.PatchID ||
			source.Repository != r.Repository || source.Base != r.Source.Ref || !frontendRecheckHex(source.Tree, 40) || c.recoveryArtifact(r, source.PatchID) != nil {
			return reject("latest source owner lacks its accepted retained candidate")
		}
		if _, err := frontendRecheckOwnerPath(r, owner.Profile); err != nil {
			return reject(err.Error())
		}
		return r, source, revisions, nil
	}
	return reject("browser recovery lacks its preceding source owner")
}
