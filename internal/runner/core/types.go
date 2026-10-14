// Package core defines local work and its invariants without I/O or provider types.
package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Stage string

const (
	Draft            Stage = "Draft"
	Ready            Stage = "Ready"
	Implementing     Stage = "Implementing"
	Reviewing        Stage = "Reviewing"
	AwaitingDelivery Stage = "Awaiting delivery"
	Stopped          Stage = "Stopped"
)

type Profile struct {
	Harness   string `json:"harness"`
	Model     string `json:"model"`
	Reasoning string `json:"reasoning"`
	Timeout   string `json:"timeout"`
}

type Proof struct {
	ID          string `json:"id"`
	Requirement string `json:"requirement"`
}

type Version struct {
	Number  int     `json:"number"`
	Body    string  `json:"body"`
	Digest  string  `json:"digest"`
	Quality string  `json:"quality_revision"`
	Proofs  []Proof `json:"proofs"`
}

type Approval struct {
	Preview    string `json:"preview"`
	Base       string `json:"base"`
	Generation int    `json:"generation"`
}

type Candidate struct {
	Commit      string `json:"commit"`
	Tree        string `json:"tree"`
	Fingerprint string `json:"fingerprint"`
	Workspace   string `json:"workspace"`
}

type Attention struct {
	Kind    string `json:"kind"`
	Detail  string `json:"detail"`
	Attempt string `json:"attempt,omitempty"`
}

type Task struct {
	ID         string     `json:"id"`
	Revision   int        `json:"revision"`
	Generation int        `json:"generation"`
	Stage      Stage      `json:"stage"`
	Versions   []Version  `json:"versions"`
	Approval   *Approval  `json:"approval,omitempty"`
	Candidate  *Candidate `json:"candidate,omitempty"`
	Attention  *Attention `json:"attention,omitempty"`
	NextRole   string     `json:"next_role,omitempty"`
	Review     []Check    `json:"review,omitempty"`
}

func (t Task) Current() Version { return t.Versions[len(t.Versions)-1] }

type Process struct {
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
}

type Attempt struct {
	ID             string     `json:"id"`
	TaskID         string     `json:"task_id"`
	Version        int        `json:"version"`
	Generation     int        `json:"generation"`
	Role           string     `json:"role"`
	ProfileName    string     `json:"profile_name"`
	Profile        Profile    `json:"profile"`
	PolicyRevision string     `json:"policy_revision"`
	Quality        string     `json:"quality_revision"`
	Base           string     `json:"base"`
	State          string     `json:"state"`
	Occupying      bool       `json:"occupying"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Process        *Process   `json:"process,omitempty"`
	Result         *Result    `json:"result,omitempty"`
	SourceDigest   string     `json:"source_digest,omitempty"`
	Constructing   bool       `json:"constructing,omitempty"`
	SourceHead     string     `json:"source_head,omitempty"`
	Candidate      *Candidate `json:"candidate,omitempty"`
	Error          string     `json:"error,omitempty"`
	ResumedFrom    string     `json:"resumed_from,omitempty"`
	Resume         string     `json:"resume,omitempty"`
	ExecutionDir   string     `json:"execution_dir,omitempty"`
	ReadRoots      []string   `json:"read_roots,omitempty"`
	EvidenceDir    string     `json:"evidence_dir"`
}

func (a Attempt) Authorized(t Task) bool {
	return t.Approval != nil && t.Stage != Stopped && t.Current().Number == a.Version && t.Generation == a.Generation && t.Approval.Generation == a.Generation && t.Approval.Base == a.Base
}

type Check struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

type Result struct {
	Outcome string  `json:"outcome"`
	Summary string  `json:"summary"`
	Checks  []Check `json:"checks"`
}

// ValidateResult enforces completeness; a model verdict cannot waive a proof.
func ValidateResult(r Result, proofs []Proof) error {
	if r.Outcome != "succeeded" && r.Outcome != "blocked" && r.Outcome != "needs_input" {
		return errors.New("invalid result outcome")
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("result needs a summary")
	}
	expected := map[string]bool{}
	for _, p := range proofs {
		expected[p.ID] = true
	}
	for _, c := range r.Checks {
		if !expected[c.ID] {
			return fmt.Errorf("unknown or duplicate proof %q", c.ID)
		}
		delete(expected, c.ID)
		if strings.TrimSpace(c.Evidence) == "" {
			return fmt.Errorf("proof %s has no evidence", c.ID)
		}
		switch c.Status {
		case "pass", "gap", "defect", "needs_input":
		default:
			return fmt.Errorf("invalid proof status %q", c.Status)
		}
	}
	if len(expected) != 0 {
		return errors.New("result omits required proofs")
	}
	return nil
}

func ReviewOutcome(checks []Check) string {
	result := "accepted"
	for _, c := range checks {
		switch c.Status {
		case "needs_input":
			return "needs_input"
		case "defect":
			result = "defect"
		case "gap":
			if result == "accepted" {
				result = "gap"
			}
		}
	}
	return result
}

func NewVersion(number int, body, repository, basePolicy, quality string) (Version, error) {
	sections := map[string]string{}
	var section string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			section = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			if section == "dependencies" {
				return Version{}, errors.New("dependencies are not supported in slice 1")
			}
			continue
		}
		if section != "" {
			sections[section] += line + "\n"
		}
	}
	for _, name := range []string{"outcome", "scope", "acceptance", "verification"} {
		if strings.TrimSpace(sections[name]) == "" {
			return Version{}, fmt.Errorf("task requires a nonempty ## %s section", name)
		}
	}
	proofs := []Proof{{ID: "change", Requirement: "The complete approved outcome, scope and acceptance criteria are satisfied; the code and tests are correct and proportionate."}}
	verification := strings.TrimSpace(sections["verification"])
	proofs = append(proofs, Proof{ID: "verification", Requirement: verification})
	v := Version{Number: number, Body: body, Quality: quality, Proofs: proofs}
	v.Digest = Digest(struct{ Body, Repository, BasePolicy, Quality string }{body, repository, basePolicy, quality})
	return v, nil
}

func Preview(t Task, repository, basePolicy, quality, base string) string {
	return Digest(struct {
		ID                                         string
		Version                                    int
		Content, Repository, Policy, Quality, Base string
	}{t.ID, t.Current().Number, t.Current().Digest, repository, basePolicy, quality, base})
}

func Digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
