package core

import "time"

// IntegrationRequest pins exact evidence. Merge is authorized by an operator
// request or the explicitly applied automatic policy.
type IntegrationRequest struct {
	AutomaticPolicy string    `json:"automatic_policy,omitempty"` // origin revision, empty for explicit commands
	Merge           bool      `json:"merge,omitempty"`
	Proof           BaseProof `json:"base_proof"`
	CIDigest        string    `json:"ci_digest"`
}

type IntegrationObservation struct {
	Remote      RemotePublication `json:"remote"`
	Base        string            `json:"base"`
	MergeCommit string            `json:"merge_commit,omitempty"`
	MergedAt    time.Time         `json:"merged_at,omitempty"`
	Checks      []RemoteCheck     `json:"checks"`
}

// MergeAdmission records the host gate; it cannot grant local task authority.
type MergeAdmission struct {
	Eligible         bool   `json:"eligible"`
	Detail           string `json:"detail"`
	PullRequestID    string `json:"pull_request_id"`
	ProtectionRuleID string `json:"protection_rule_id"`
}

// MergeDispatch is immutable evidence that a mutation may have reached the host.
// Its presence permanently consumes write authority for this publication.
type MergeDispatch struct {
	Request     IntegrationRequest `json:"request"`
	Admission   MergeAdmission     `json:"admission"`
	RequestedAt time.Time          `json:"requested_at"`
}

type MergeObservation struct {
	IntegrationObservation
	Admission MergeAdmission `json:"admission"`
}

type IntegrationAssessment struct {
	Admission   *MergeAdmission         `json:"admission,omitempty"`
	Request     IntegrationRequest      `json:"request"`
	State       string                  `json:"state"` // open, closed, unavailable, stale, blocked, uncertain, confirmed
	Detail      string                  `json:"detail"`
	ObservedAt  time.Time               `json:"observed_at"`
	BaseTip     string                  `json:"base_tip,omitempty"`
	Observation *IntegrationObservation `json:"observation,omitempty"`
}

type IntegrationReceipt struct {
	PublicationID string                `json:"publication_id"`
	Assessment    IntegrationAssessment `json:"assessment"`
}

func (p Publication) CanInspectIntegration(t Task) bool {
	return (p.State == "published" || p.State == "attention") && p.Intent.Authorized(t) &&
		t.BaseProof != nil && t.BaseProof.Matches(t) && p.CIRequest == nil && p.CI != nil && p.CI.State == "passed" &&
		Digest(p.CI.Proof) == Digest(t.BaseProof) && p.Observation != nil && p.Observation.Number > 0 &&
		p.CI.Observation != nil && p.CI.Observation.Remote.Number == p.Observation.Number &&
		p.CI.Observation.Remote.URL == p.Observation.URL && p.CI.Observation.Remote.BranchCommit == p.Intent.Candidate.Commit
}

func (r IntegrationRequest) Matches(t Task, p Publication) bool {
	return (r.AutomaticPolicy == "" || r.Merge) && p.CanInspectIntegration(t) && Digest(r.Proof) == Digest(t.BaseProof) && r.CIDigest == Digest(p.CI)
}

// AutomaticMergeReady consumes an observation, never another merge assessment.
// New CI evidence or an explicit inspection supplies the next bounded trigger.
func (p Publication) AutomaticMergeReady(t Task) bool {
	r := p.Integration
	if p.State != "published" || p.Occupying || p.IntegrationRequest != nil || p.MergeDispatch != nil || r == nil || r.Request.Merge || r.State != "open" || !r.Request.Matches(t, p) || r.Observation == nil {
		return false
	}
	o := r.Observation
	return !o.Remote.Draft && o.Remote.State == "OPEN" && o.Base == r.Request.Proof.Base &&
		o.Remote.Number == p.Observation.Number && o.Remote.URL == p.Observation.URL && o.Remote.BranchCommit == p.Intent.Candidate.Commit
}
