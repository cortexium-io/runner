package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// RetainedPublicationTransition grants one source adoption of an authenticated
// publication from a previous policy, independently of other adoption grants.
type RetainedPublicationTransition struct {
	PublicationTransition
	PreviousRunID string `json:"previous_run_id"`
	PreviousKey   string `json:"previous_key"`
}

func (t *RetainedPublicationTransition) validate(delivery bool) error {
	if t == nil {
		return nil
	}
	if err := t.PublicationTransition.validate(delivery); err != nil {
		return err
	}
	if !safeID.MatchString(t.PreviousRunID) || !safeID.MatchString(t.PreviousKey) {
		return errors.New("retained publication transition requires the exact previous run and publication key")
	}
	return nil
}

func (t *RetainedPublicationTransition) matches(q Request, previous Receipt, saved Job, delivery bool) bool {
	return t != nil && t.validate(delivery) == nil && q.Action == "adopt" && q.SourceContinuation &&
		previous.RunID == t.PreviousRunID && previous.Key == t.PreviousKey && previous.Policy == t.PreviousPolicy &&
		q.RunID != previous.RunID && q.Key != previous.Key && q.Candidate == "" && len(q.Retry) == 0 && len(q.Recovery) == 0 &&
		t.matchesPublication(q, previous, saved)
}

// RetainedPublicationFollowup permits one fresh adoption of the same publication
// after its original retained transition was consumed by the named attempt.
type RetainedPublicationFollowup struct {
	RetainedPublicationTransition
	ConsumedRunID  string `json:"consumed_run_id"`
	ConsumedKey    string `json:"consumed_key"`
	ConsumedPolicy string `json:"consumed_policy_sha256"`
}

func (f *RetainedPublicationFollowup) validate(delivery bool, original *RetainedPublicationTransition) error {
	if f == nil {
		return nil
	}
	if err := f.RetainedPublicationTransition.validate(delivery); err != nil {
		return err
	}
	if original == nil || *original != f.RetainedPublicationTransition {
		return errors.New("retained publication follow-up must match the original transition")
	}
	if !safeID.MatchString(f.ConsumedRunID) || !safeID.MatchString(f.ConsumedKey) ||
		f.ConsumedRunID == f.PreviousRunID || f.ConsumedKey == f.PreviousKey ||
		!policyDigest.MatchString(f.ConsumedPolicy) || f.ConsumedPolicy == f.PreviousPolicy {
		return errors.New("retained publication follow-up requires the exact consumed run, attempt and policy")
	}
	return nil
}

func (f *RetainedPublicationFollowup) claim(directory string, q Request, policy string) error {
	if q.RunID == f.ConsumedRunID || q.Key == f.ConsumedKey {
		return errors.New("retained publication follow-up requires a fresh run and attempt")
	}
	prior := filepath.Join(directory, "retained-publication-"+f.PreviousKey+".claim")
	want, err := json.Marshal(transitionClaim{Key: f.ConsumedKey, Policy: f.ConsumedPolicy, RunID: f.ConsumedRunID})
	if err != nil {
		return err
	}
	info, err := os.Lstat(prior)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("retained publication follow-up needs the exact original consumed claim")
	}
	data, err := readFile(prior, 1024)
	if err != nil || !bytes.Equal(data, want) {
		return errors.New("retained publication follow-up needs the exact original consumed claim")
	}
	file := filepath.Join(directory, "retained-publication-followup-"+f.PreviousKey+".claim")
	if err := claimTransition(file, transitionClaim{Key: q.Key, Policy: policy, RunID: q.RunID}); err != nil {
		return fmt.Errorf("retained publication follow-up: %w", err)
	}
	return nil
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

	file := filepath.Join(o.Directory, "qualification-"+t.FailedQualificationKey+".claim")
	if err := claimTransition(file, transitionClaim{Key: q.Key, Policy: o.PolicyHash}); err != nil {
		return fmt.Errorf("qualification transition: %w", err)
	}
	return nil
}

type transitionClaim struct {
	Key, Policy string
	RunID       string `json:",omitempty"`
}

// Different attempt keys share this lease. Persist the winner before any
// worker can launch, including across a crash before the new job is saved.
func claimTransition(file string, want transitionClaim) error {
	lease, err := os.OpenFile(file+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := lockOperator(lease); err != nil {
		return err
	}
	defer unlockOperator(lease)
	data, err := readFile(file, 1024)
	if errors.Is(err, os.ErrNotExist) {
		return writeJSON(file, want)
	}
	var existing transitionClaim
	if err != nil || json.Unmarshal(data, &existing) != nil || existing != want {
		return errors.New("already claimed by another attempt, run or policy")
	}
	return nil
}
