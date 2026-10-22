package core

import "time"

// AuditReceipt is immutable. SourceDigest attributes this observation to the
// snapshot queried; later reuse carries a separate, explicit assessment binding.
type AuditReceipt struct {
	ID           string         `json:"id"`
	Inputs       string         `json:"inputs"`
	Policy       string         `json:"policy"`
	Adapter      string         `json:"adapter"`
	Tool         string         `json:"tool"`
	Image        string         `json:"image"`
	Registry     string         `json:"registry"`
	QueriedAt    time.Time      `json:"queried_at"`
	FinishedAt   time.Time      `json:"finished_at"`
	SourceDigest string         `json:"source_digest"`
	Status       string         `json:"status"`
	Findings     map[string]int `json:"findings"`
	Report       string         `json:"report"`
	ReportDigest string         `json:"report_digest"`
}

func (r AuditReceipt) Fresh(now time.Time, age time.Duration) bool {
	return age > 0 && !r.QueriedAt.IsZero() && !r.FinishedAt.Before(r.QueriedAt) &&
		!now.Before(r.FinishedAt) && now.Before(r.QueriedAt.Add(age)) && (r.Status == "passed" || r.Status == "failed")
}

type AuditAssessment struct {
	Receipt      AuditReceipt `json:"receipt"`
	SourceDigest string       `json:"source_digest"`
}

type AuditProof struct {
	Candidate Candidate          `json:"candidate"`
	Checks    []CheckObservation `json:"checks"`
}
