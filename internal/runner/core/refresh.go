package core

import (
	"fmt"
	"time"
)

// BaseBinding identifies one retained workspace, without changing approval.
type BaseBinding struct {
	Commit string `json:"commit"`
	Cycle  int    `json:"cycle"`
}

type BaseProof struct {
	Base         string    `json:"base"`
	Candidate    Candidate `json:"candidate"`
	Quality      string    `json:"quality_revision"`
	ReviewDigest string    `json:"review_digest"`
	ObservedAt   time.Time `json:"observed_at"`
}

func (t Task) Base() string {
	if t.WorkingBase != nil {
		return t.WorkingBase.Commit
	}
	if t.Approval != nil {
		return t.Approval.Base
	}
	return ""
}

func (t Task) WorkspaceKey() string {
	key := fmt.Sprintf("%s-v%d", t.ID, t.Current().Number)
	if t.WorkingBase != nil {
		key += fmt.Sprintf("-r%d", t.WorkingBase.Cycle)
	}
	return key
}

func (p BaseProof) Matches(t Task) bool {
	return t.Approval != nil && t.Stage == AwaitingDelivery && t.Attention == nil &&
		t.Candidate != nil && *t.Candidate == p.Candidate && t.Base() == p.Base &&
		t.Current().Quality == p.Quality && Digest(t.Review) == p.ReviewDigest && ReviewOutcome(t.Review) == Accepted
}
