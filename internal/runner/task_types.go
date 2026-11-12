package runner

import (
	"encoding/json"
	"time"
)

// Task identities and assignments are created by Core from a frozen task group.
// They are never accepted from a start request or a model result.
type TaskIdentity struct {
	RootRunID string `json:"root_run_id"`
	GroupStep string `json:"group_step"`
	ItemID    string `json:"item_id"`
}

type TaskRunLink struct {
	Identity TaskIdentity `json:"identity"`
}

type TaskAssignment struct {
	Identity     TaskIdentity    `json:"identity"`
	WritePaths   []string        `json:"write_paths"`
	Dependencies []TaskCandidate `json:"dependencies"`
	RootPatchID  string          `json:"root_patch_id,omitempty"`
}

// A task candidate is an internal source pin, not an accepted publication.
type TaskCandidate struct {
	Identity   TaskIdentity `json:"identity"`
	RunID      string       `json:"run_id"`
	Repository string       `json:"repository"`
	Base       string       `json:"base"`
	Commit     string       `json:"commit"`
	Tree       string       `json:"tree"`
	PatchID    string       `json:"patch_id"`
}

type TaskManifestItem struct {
	ID         string                     `json:"id"`
	DependsOn  []string                   `json:"depends_on,omitempty"`
	Inputs     map[string]json.RawMessage `json:"inputs"`
	WritePaths []string                   `json:"write_paths"`
	RunID      string                     `json:"run_id"`
	Candidate  TaskCandidate              `json:"candidate"`
}

type TaskManifest struct {
	RootRunID   string             `json:"root_run_id"`
	GroupStep   string             `json:"group_step"`
	Repository  string             `json:"repository"`
	Base        string             `json:"base"`
	RootPatchID string             `json:"root_patch_id,omitempty"`
	Request     string             `json:"request"`
	Items       []TaskManifestItem `json:"items"`
	Evidence    map[string]Value   `json:"evidence,omitempty"`
}

type TaskChild struct {
	ItemID    string         `json:"item_id"`
	RunID     string         `json:"run_id"`
	Candidate *TaskCandidate `json:"candidate,omitempty"`
}

type TaskStatus struct {
	ID             string    `json:"id"`
	RunID          string    `json:"run_id"`
	DependsOn      []string  `json:"depends_on,omitempty"`
	Status         string    `json:"status"`
	Reason         string    `json:"reason,omitempty"`
	Attempts       int       `json:"attempts"`
	Active         bool      `json:"active"`
	Unsettled      bool      `json:"execution_unsettled,omitempty"`
	CleanupPending bool      `json:"cleanup_pending,omitempty"`
	CleanupError   string    `json:"cleanup_error,omitempty"`
	Reconciling    bool      `json:"reconciling,omitempty"`
	Progress       *Progress `json:"-"`
}

type TaskGroupState struct {
	StepID      string           `json:"step_id"`
	AttemptID   string           `json:"attempt_id"`
	Repository  string           `json:"repository"`
	Base        string           `json:"base"`
	Started     time.Time        `json:"started"`
	Items       []TaskItem       `json:"items"`
	Children    []TaskChild      `json:"children"`
	RootPatchID string           `json:"root_patch_id,omitempty"`
	Evidence    map[string]Value `json:"evidence,omitempty"`
	Done        bool             `json:"done,omitempty"`
	Failure     string           `json:"failure,omitempty"`
}
