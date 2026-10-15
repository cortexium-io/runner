package core

import "time"

// CIRequirement is part of the owner's pinned quality agreement. Check names
// include their producer identity; legacy status contexts have no app identity.
type CIRequirement struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	AppID int64  `json:"app_id,omitempty"`
}

type RemoteCheck struct {
	CIRequirement
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

type CIObservation struct {
	Remote RemotePublication `json:"remote"`
	Base   string            `json:"base"`
	Commit string            `json:"commit"`
	Source string            `json:"source"` // head or test-merge
	Checks []RemoteCheck     `json:"checks"`
}

type CIResult struct {
	RemoteCheck
	State string `json:"state"`
}

type CIAssessment struct {
	Proof       BaseProof      `json:"base_proof"`
	ObservedAt  time.Time      `json:"observed_at"`
	State       string         `json:"state"`
	Detail      string         `json:"detail"`
	Observation *CIObservation `json:"observation,omitempty"`
	Results     []CIResult     `json:"results"`
}

// CIRepair retains the exact failure that admitted a repair. Provider text is
// diagnostic data, never task or quality authority.
type CIRepair struct {
	PublicationID         string       `json:"publication_id"`
	Assessment            CIAssessment `json:"assessment"`
	SourceWorkspaceRepair int          `json:"source_workspace_repair"`
}

func (p Publication) CanRepairCI(t Task) bool {
	if !p.CurrentCI(t) || p.CI.State != "failed" || len(p.CI.Results) == 0 {
		return false
	}
	failed := false
	for _, r := range p.CI.Results {
		if r.State == "passed" {
			continue
		}
		if r.State != "failed" || !((r.Kind == "check" && r.Status == "COMPLETED" && r.Conclusion == "FAILURE") || (r.Kind == "status" && r.Status == "FAILURE")) {
			return false
		}
		failed = true
	}
	return failed
}

func (p Publication) CanAssessCI(t Task) bool {
	return p.State == "published" && p.Intent.Authorized(t) && t.BaseProof != nil && t.BaseProof.Matches(t) &&
		p.Observation != nil && p.Observation.Number > 0 && p.Observation.State == "OPEN" && p.Observation.Draft && p.Observation.BranchCommit == p.Intent.Candidate.Commit
}

func (p Publication) CurrentCI(t Task) bool {
	return p.CI != nil && p.CIRequest == nil && !p.Occupying && p.CanAssessCI(t) && Digest(p.CI.Proof) == Digest(t.BaseProof)
}

// AssessCI never infers the required set from the jobs that happened to report.
func AssessCI(required []CIRequirement, checks []RemoteCheck) (string, []CIResult) {
	if len(required) == 0 {
		return "unconfigured", nil
	}
	state := "passed"
	rank := map[string]int{"passed": 0, "pending": 1, "missing": 2, "failed": 3, "unavailable": 4}
	results := make([]CIResult, 0, len(required))
	for _, req := range required {
		r := CIResult{RemoteCheck: RemoteCheck{CIRequirement: req}, State: "missing"}
		count := 0
		for _, check := range checks {
			if check.CIRequirement != req {
				continue
			}
			count++
			r.RemoteCheck, r.State = check, checkState(check)
		}
		if count > 1 {
			r.State = "unavailable"
		}
		if rank[r.State] > rank[state] {
			state = r.State
		}
		results = append(results, r)
	}
	return state, results
}

func checkState(c RemoteCheck) string {
	if c.Kind == "status" {
		switch c.Status {
		case "SUCCESS":
			return "passed"
		case "PENDING", "EXPECTED":
			return "pending"
		case "ERROR", "FAILURE":
			return "failed"
		default:
			return "unavailable"
		}
	}
	if c.Kind != "check" {
		return "unavailable"
	}
	switch c.Status {
	case "QUEUED", "IN_PROGRESS", "PENDING", "REQUESTED", "WAITING":
		return "pending"
	case "COMPLETED":
		switch c.Conclusion {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			return "passed"
		case "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
			return "failed"
		}
	}
	return "unavailable"
}
