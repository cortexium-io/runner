package runner

import (
	"errors"
	"maps"
	"reflect"
	"time"
)

// FrontendAssertionRecovery records one separately authorized correction after
// the diagnostic repair's fresh full check retained a single assertion failure.
// It neither replenishes a correction route nor changes the frozen attempt cap.
type FrontendAssertionRecovery struct {
	RequestID       string    `json:"request_id"`
	CheckAttemptID  string    `json:"check_attempt_id"`
	CheckID         string    `json:"check_id"`
	CheckSHA256     string    `json:"check_sha256"`
	PatchID         string    `json:"patch_id"`
	PatchSHA256     string    `json:"patch_sha256"`
	HelperSHA256    string    `json:"helper_sha256"`
	PublicationHead string    `json:"publication_head"`
	RepairVisit     int       `json:"repair_visit"`
	GrantedAt       time.Time `json:"granted_at"`
}

func (c *Core) validateFrontendAssertionRecovery(r Run, req RecoverFrontendCheckRequest, owner Attempt, report frontendTimeoutReport, executed map[frontendTimeoutCaseKey]int, timeouts map[frontendTimeoutCaseKey]float64) error {
	old := r.FrontendTimeoutRecovery
	if old == nil || old.RepairVisit == 0 || old.RepairVisit != owner.Visit || owner.Source == nil || owner.Source.InputPatchID != old.PatchID ||
		old.HelperSHA256 != req.HelperSHA256 || old.PublicationHead != req.PublicationHead || old.CheckStep != "check" ||
		r.RouteUses["check/frontend_timeout_repair"] != 1 || r.RouteUses["check/frontend_assertion_repair"] != 0 {
		return errors.New("frontend assertion recovery must follow the exact spent diagnostic repair")
	}
	if report.Failed != 1 || len(timeouts) != 0 || report.Timeouts.Schema != 0 || report.Timeouts.Status != "" || report.Timeouts.Method != "" || len(report.Timeouts.Cases) != 0 {
		return errors.New("frontend assertion recovery requires one conclusive assertion failure without timeout diagnostics")
	}
	if c.recoveryArtifact(r, old.RepairEvidenceID) != nil {
		return errors.New("frontend assertion recovery lacks its owned diagnostic evidence")
	}
	prior, err := c.readFrontendTimeoutReport(old.RepairEvidenceID, old.RepairEvidenceSHA)
	if err != nil {
		return err
	}
	priorExecuted, _, err := prior.execution(r.Inputs["tests"])
	if err != nil || prior.Failed < 1 || !maps.Equal(executed, priorExecuted) || !reflect.DeepEqual(report.Typecheck, prior.Typecheck) {
		return errors.New("frontend assertion recovery changed full-suite identities or compiler evidence")
	}
	return nil
}

// recoverFrontendAssertion runs under the Core lock, after the ordinary retained
// source/publication validator and a second revision check have both passed.
func (c *Core) recoverFrontendAssertion(plan frontendCheckRecoveryPlan, req RecoverFrontendCheckRequest, operation string, args []byte) (Run, error) {
	r := plan.run
	owner := r.Process.Steps[r.Step-1]
	inputs, err := bindings(r, owner.Inputs)
	if err != nil || inputs["checks"].ArtifactID != plan.priorCheck {
		return Run{}, errors.New("frontend assertion repair must bind the full failed check")
	}
	c.advance(&r, &StepResult{Outcome: "frontend_assertion_repair"}, Route{Next: owner.ID})
	if r.Status != "Queued" || !reflect.DeepEqual(r.StepInputs, inputs) {
		return Run{}, errors.New("frontend assertion repair lost its verified owner inputs")
	}
	r.ResourcesReleased = false
	r.FrontendAssertionRecovery = &FrontendAssertionRecovery{
		RequestID:       req.RequestID,
		CheckAttemptID:  req.AttemptID,
		CheckID:         plan.priorCheck,
		CheckSHA256:     req.AssertionSHA256,
		PatchID:         plan.source.PatchID,
		PatchSHA256:     req.PatchSHA256,
		HelperSHA256:    req.HelperSHA256,
		PublicationHead: req.PublicationHead,
		RepairVisit:     r.Visit,
		GrantedAt:       time.Now().UTC(),
	}
	state, definition, err := MarshalRunRecord(r)
	limit := int(c.disk.Policy.Database.MaxRecord)
	if err != nil || len(state)+2048 > limit || len(definition) > limit {
		return Run{}, errors.New("frontend assertion recovery lacks existing history headroom")
	}
	receipt := Receipt{Key: req.RequestID, Kind: operation, Args: args, RunID: r.ID}
	if err := c.persist(r, nil, &receipt); err != nil {
		return Run{}, err
	}
	c.Wake()
	return Clone(c.runs[r.ID]), nil
}
