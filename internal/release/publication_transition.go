package release

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

var policyDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PublicationTransition authorizes adopting one exact accepted publication into
// the current policy. It grants no old-policy qualification or release effects.
type PublicationTransition struct {
	PreviousPolicy string `json:"previous_policy_sha256"`
	PR             int    `json:"pr"`
	Head           string `json:"head"`
	Base           string `json:"base"`
}

func (p *PublicationTransition) validate(delivery bool) error {
	if p == nil {
		return nil
	}
	if !delivery || p.PR < 1 || !policyDigest.MatchString(p.PreviousPolicy) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.Head) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.Base) {
		return errors.New("publication transition requires delivery and one exact previous policy/PR/head/base")
	}
	return nil
}

// The caller has already authenticated the compact handle against its accepted
// private journal. PR/base are deliberately read from that full saved receipt,
// since the public publication handle does not contain them.
func (o *Operator) acceptsPreviousPolicy(q Request, previous Receipt, saved Job) bool {
	if previous.Policy == o.PolicyHash {
		return true
	}
	p := o.PublicationTransition
	if p == nil || p.validate(o.Delivery) != nil || q.Action != "adopt" || previous.Action != "publish" || previous.Policy != p.PreviousPolicy {
		return false
	}
	return p.matchesPublication(q, previous, saved)
}

func (p PublicationTransition) matchesPublication(q Request, previous Receipt, saved Job) bool {
	r := saved.Receipt
	return r.Policy == p.PreviousPolicy && r.Action == "publish" && r.PR == p.PR && r.Head == p.Head && r.Base == p.Base && r.Source == p.Base && q.Source == p.Base &&
		saved.Request.Key == previous.Key && r.Key == previous.Key && saved.Request.RunID == previous.RunID && r.RunID == previous.RunID &&
		saved.Request.Action == "publish" && saved.Request.Source == p.Base && saved.Request.Candidate == p.Head
}

// QualificationTransition grants one fresh qualification of a previously
// published candidate after a settled admission failure. The adoption grant is
// independent, and all later release actions need a current-policy result.
type QualificationTransition struct {
	PublicationTransition
	RunID                  string `json:"run_id"`
	PublicationKey         string `json:"publication_key"`
	FailedQualificationKey string `json:"failed_qualification_key"`
	FailureReportSHA256    string `json:"failure_report_sha256"`
}

func (t *QualificationTransition) validate(pinnedBrowser bool) error {
	if t == nil {
		return nil
	}
	if err := t.PublicationTransition.validate(pinnedBrowser); err != nil {
		return err
	}
	if !safeID.MatchString(t.RunID) || !safeID.MatchString(t.PublicationKey) || !safeID.MatchString(t.FailedQualificationKey) ||
		t.PublicationKey == t.FailedQualificationKey || !policyDigest.MatchString(t.FailureReportSHA256) {
		return errors.New("qualification transition requires the exact run, publication and failed browser report")
	}
	return nil
}

func (o *Operator) claimQualificationTransition(q Request, previous Receipt, saved Job) error {
	t := o.QualificationTransition
	if t == nil || t.validate(o.Delivery) != nil || q.Action != "qualify" || q.RunID != t.RunID || previous.RunID != t.RunID ||
		previous.Key != t.PublicationKey || previous.Policy != t.PreviousPolicy || q.Key == t.FailedQualificationKey || q.Key == t.PublicationKey ||
		q.Candidate != "" || len(q.Retry) != 0 || len(q.Recovery) != 0 || !t.matchesPublication(q, previous, saved) ||
		!acceptedSourcePredecessor(saved) || !o.sourceOriginAccepted(saved.Receipt, q.Project) {
		return errors.New("qualification transition is outside the approved publication and run")
	}
	data, err := readFile(filepath.Join(o.Directory, t.FailedQualificationKey+".json"), 32<<10)
	var failed Job
	var original Receipt
	if err != nil || json.Unmarshal(data, &failed) != nil || !failed.Done || !failed.Failed || failed.Retryable || failed.Outcome != "" ||
		failed.Request.Key != t.FailedQualificationKey || failed.Request.Project != q.Project || failed.Request.RunID != q.RunID ||
		failed.Request.Action != "qualify" || failed.Request.Source != q.Source || json.Unmarshal(failed.Request.Previous, &original) != nil || original != previous {
		return errors.New("qualification transition needs the settled failed attempt for this publication")
	}
	r := failed.Receipt
	p := saved.Receipt
	if r.Key != failed.Request.Key || r.RunID != q.RunID || r.Action != "qualify" || r.Policy != t.PreviousPolicy ||
		r.Head != p.Head || r.Base != p.Base || r.Source != p.Source || r.PR != p.PR ||
		r.PublicationRunID != p.PublicationRunID || r.PublicationKey != p.PublicationKey ||
		r.BrowserFailure.Kind != "admission" || r.BrowserFailure.ReportSHA256 != t.FailureReportSHA256 {
		return errors.New("qualification transition failed browser receipt differs")
	}

	// Different attempt keys share this lease. Persist the winner before any
	// worker can launch, including across a crash before the new job is saved.
	file := filepath.Join(o.Directory, "qualification-"+t.FailedQualificationKey+".claim")
	lease, err := os.OpenFile(file+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := lockOperator(lease); err != nil {
		return err
	}
	defer unlockOperator(lease)
	type claim struct{ Key, Policy string }
	want := claim{Key: q.Key, Policy: o.PolicyHash}
	data, err = readFile(file, 1024)
	if errors.Is(err, os.ErrNotExist) {
		return writeJSON(file, want)
	}
	var existing claim
	if err != nil || json.Unmarshal(data, &existing) != nil || existing != want {
		return errors.New("qualification transition was already claimed by another attempt or policy")
	}
	return nil
}
