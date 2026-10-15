package core

import "time"

// IntegrationRequest pins exact evidence. Merge is explicit one-shot operator authority.
type IntegrationRequest struct {
	Merge    bool      `json:"merge,omitempty"`
	Proof    BaseProof `json:"base_proof"`
	CIDigest string    `json:"ci_digest"`
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
	return p.CanInspectIntegration(t) && Digest(r.Proof) == Digest(t.BaseProof) && r.CIDigest == Digest(p.CI)
}
