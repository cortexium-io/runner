package core

import "time"

// Source is a captured request, never external approval or live task content.
type Source struct {
	Provider   string `json:"provider"`
	Connection string `json:"connection"`
	ID         string `json:"id"`
	URL        string `json:"url"`
	Revision   string `json:"revision"`
	Title      string `json:"title"`
	Body       string `json:"body"`
}

func (s Source) Key() string { return Digest([]string{s.Provider, s.Connection, s.ID}) }

type SourceRequest struct {
	Source Source `json:"source"`
	Open   bool   `json:"open"`
}

// ProjectionStatus concerns only the optional connection, never task eligibility.
type ProjectionStatus struct {
	LastAttempt time.Time        `json:"last_attempt"`
	LastSuccess *time.Time       `json:"last_success,omitempty"`
	SnapshotAt  time.Time        `json:"snapshot_at"`
	Revision    string           `json:"revision"`
	Error       string           `json:"error,omitempty"`
	Pending     *ProjectionWrite `json:"pending,omitempty"`
}

// ProjectionWrite is the one outstanding view update, retained before dispatch.
// A lost response must be observed before a newer snapshot can replace it.
type ProjectionWrite struct {
	Body       string    `json:"body"`
	Revision   string    `json:"revision"`
	SnapshotAt time.Time `json:"snapshot_at"`
}
