package release

import (
	"errors"
	"regexp"
)

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
	if !delivery || p.PR < 1 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(p.PreviousPolicy) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.Head) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.Base) {
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
	r := saved.Receipt
	return r.Policy == p.PreviousPolicy && r.Action == "publish" && r.PR == p.PR && r.Head == p.Head && r.Base == p.Base && r.Source == p.Base && q.Source == p.Base &&
		saved.Request.Key == previous.Key && r.Key == previous.Key && saved.Request.RunID == previous.RunID && r.RunID == previous.RunID &&
		saved.Request.Action == "publish" && saved.Request.Source == p.Base && saved.Request.Candidate == p.Head
}
