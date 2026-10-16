package core

import "time"

// PublicationIntent is immutable authority for one exact draft handoff. Provider
// payloads and transport details do not belong in this local contract.
type PublicationIntent struct {
	Sync         *DraftSync `json:"sync,omitempty"`
	ID           string     `json:"id"`
	TaskID       string     `json:"task_id"`
	Version      int        `json:"version"`
	Generation   int        `json:"generation"`
	ApprovedBase string     `json:"approved_base"`
	Quality      string     `json:"quality_revision"`
	ReviewDigest string     `json:"review_digest"`
	Candidate    Candidate  `json:"candidate"`
	Repository   string     `json:"repository"`
	BaseBranch   string     `json:"base_branch"`
	Branch       string     `json:"branch"`
	Title        string     `json:"title"`
}

// DraftSync binds one leased update to its confirmed predecessor and the exact
// current-base proof. The original publication marker and PR identity stay fixed.
type DraftSync struct {
	PreviousID     string    `json:"previous_id"`
	PublicationID  string    `json:"publication_id"`
	PreviousCommit string    `json:"previous_commit"`
	Number         int       `json:"number"`
	URL            string    `json:"url"`
	Base           string    `json:"base"`
	ObservedAt     time.Time `json:"base_observed_at"`
}

func (p PublicationIntent) PublicationID() string {
	if p.Sync != nil {
		return p.Sync.PublicationID
	}
	return p.ID
}

func (p PublicationIntent) PreviousID() string {
	if p.Sync != nil {
		return p.Sync.PreviousID
	}
	return ""
}

func (p PublicationIntent) Authorized(t Task) bool {
	if p.Sync != nil && (t.BaseProof == nil || !t.BaseProof.Matches(t) || t.BaseProof.Base != p.Sync.Base) {
		return false
	}
	return t.ParentID == "" && t.ID == p.TaskID && t.Current().Number == p.Version && t.Generation == p.Generation &&
		t.Stage == AwaitingDelivery && t.NextRole == "" && t.Attention == nil && t.Approval != nil &&
		t.Approval.Generation == p.Generation && t.Approval.Base == p.ApprovedBase &&
		t.Candidate != nil && *t.Candidate == p.Candidate && t.Current().Quality == p.Quality &&
		Digest(t.Review) == p.ReviewDigest && ReviewOutcome(t.Review) == Accepted
}

type RemotePublication struct {
	BranchCommit string `json:"branch_commit,omitempty"`
	Number       int    `json:"number,omitempty"`
	URL          string `json:"url,omitempty"`
	State        string `json:"state,omitempty"`
	Draft        bool   `json:"draft"`
}

type Publication struct {
	MergeDispatch      *MergeDispatch         `json:"merge_dispatch,omitempty"`
	IntegrationRequest *IntegrationRequest    `json:"integration_request,omitempty"`
	Integration        *IntegrationAssessment `json:"integration,omitempty"`
	CIRequest          *BaseProof             `json:"ci_request,omitempty"`
	CI                 *CIAssessment          `json:"ci,omitempty"`
	Intent             PublicationIntent      `json:"intent"`
	Revision           int                    `json:"revision"`
	State              string                 `json:"state"` // queued, running, published, attention, revoked
	Occupying          bool                   `json:"occupying"`
	CreateRequested    bool                   `json:"create_requested"`
	Observation        *RemotePublication     `json:"observation,omitempty"`
	ObservedAt         *time.Time             `json:"observed_at,omitempty"`
	UpdatedAt          time.Time              `json:"updated_at"`
	Detail             string                 `json:"detail,omitempty"`
}
