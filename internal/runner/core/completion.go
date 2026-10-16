package core

import "time"

// RefWrite is an owned, single-dispatch Git effect. Proof and authority are
// immutable; state records whether replay is safe or observation is required.
type RefWrite struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"` // plan, inclusion, result, base, manual
	TaskID          string     `json:"task_id"`
	Version         int        `json:"version"`
	Generation      int        `json:"generation"`
	Release         string     `json:"release,omitempty"`
	Ref             string     `json:"ref"`
	Old             string     `json:"old,omitempty"`
	Commit          string     `json:"commit"`
	VerifyRef       string     `json:"verify_ref,omitempty"`
	VerifyCommit    string     `json:"verify_commit,omitempty"`
	Proof           *BaseProof `json:"proof,omitempty"`
	AutomaticPolicy string     `json:"automatic_policy,omitempty"`
	State           string     `json:"state"` // prepared, dispatched, confirmed, rejected, uncertain, stale
	Detail          string     `json:"detail,omitempty"`
	ObservedAt      time.Time  `json:"observed_at,omitempty"`
}

func (w RefWrite) Pending() bool {
	return w.State == "prepared" || w.State == "dispatched" || w.State == "uncertain"
}
func (w RefWrite) Binding() string {
	w.State, w.Detail, w.ObservedAt = "", "", time.Time{}
	return Digest(w)
}
func (w RefWrite) Authorized(t Task) bool {
	if w.TaskID != t.ID || w.Version != t.Current().Number || w.Generation != t.Generation || t.Approval == nil || t.Approval.Generation != t.Generation || t.Stage == Stopped || t.Stage == Completed {
		return false
	}
	if w.Kind == "plan" {
		return t.Release != nil && w.Release == t.Release.Preview && w.Commit == t.Approval.Base && t.PlanHead == ""
	}
	return w.Proof != nil && w.Proof.Matches(t) && w.Commit == w.Proof.Candidate.Commit && t.NextRole == "" &&
		(t.Release == nil || w.Release == t.Release.Preview)
}

type PlanInclusion struct {
	WriteID    string    `json:"write_id"`
	Release    string    `json:"release"`
	Version    int       `json:"version"`
	Generation int       `json:"generation"`
	Previous   string    `json:"previous"`
	Candidate  Candidate `json:"candidate"`
}
type CompletionReceipt struct {
	Kind          string `json:"kind"`
	WriteID       string `json:"write_id,omitempty"`
	PublicationID string `json:"publication_id,omitempty"`
	ParentID      string `json:"parent_id,omitempty"`
	Ref           string `json:"ref,omitempty"`
	Commit        string `json:"commit"`
	Tree          string `json:"tree"`
}

func Included(child, parent Task) bool {
	r := child.Inclusion
	if parent.Release == nil || parent.Approval == nil || parent.Approval.Generation != parent.Generation || parent.Stage == Stopped || child.Approval == nil || child.Approval.Generation != child.Generation || (child.Stage != AwaitingDelivery && child.Stage != Completed) || child.Current().Number != 1 || child.ParentID != parent.ID || r == nil || r.Release != parent.Release.Preview || r.Version != child.Current().Number || r.Generation != child.Generation || child.Candidate == nil || *child.Candidate != r.Candidate {
		return false
	}
	for _, id := range parent.Release.Children {
		if id == child.ID {
			for _, w := range child.RefWrites {
				if w.ID == r.WriteID && w.Kind == "inclusion" && w.State == "confirmed" && w.Proof != nil && w.Commit == r.Candidate.Commit && w.Proof.Candidate == r.Candidate && w.Old == r.Previous && w.Proof.Base == r.Previous && w.Proof.Quality == child.Current().Quality && w.Release == r.Release && w.Generation == r.Generation && w.Version == r.Version && w.Proof.ReviewDigest == Digest(child.Review) && ReviewOutcome(child.Review) == Accepted {
					return true
				}
			}
			return false
		}
	}
	return false
}
func AllIncluded(parent Task, tasks []Task) bool {
	if parent.Release == nil || len(parent.Release.Children) == 0 {
		return false
	}
	for _, id := range parent.Release.Children {
		found := false
		for _, child := range tasks {
			if child.ID == id {
				found = Included(child, parent)
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (t Task) PlanRef() string           { return "refs/runner/plans/" + t.ID }
func DeterministicRole(role string) bool { return role == "refresh" || role == "combine" }
