package core

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Dependency struct {
	TaskID  string `json:"task_id"`
	Version int    `json:"version"`
}
type PlannedCard struct {
	Key          string   `json:"key"`
	Body         string   `json:"body"`
	Dependencies []string `json:"dependencies"`
}
type PlanningResult struct {
	Cards        []PlannedCard `json:"cards"`
	Verification string        `json:"verification"`
	Questions    []string      `json:"questions"`
}
type PreparedVerification struct {
	Requirement string `json:"requirement"`
	Attempt     string `json:"attempt"`
}
type PlanRelease struct {
	Preview  string   `json:"preview"`
	Children []string `json:"children"`
}

func PlanningRole(role string) bool { return role == "plan" || role == "prepare" }
func NewPlanVersion(number int, body, repository, policy, quality string) (Version, error) {
	if strings.TrimSpace(body) == "" {
		return Version{}, errors.New("planning request must not be empty")
	}
	return Version{Number: number, Body: body, Quality: quality, Digest: Digest([]string{body, repository, policy, quality, "plan"})}, nil
}
func (v Version) NeedsVerification() bool {
	for _, p := range v.Proofs {
		if p.ID == "verification" {
			return false
		}
	}
	return true
}
func (t Task) Proofs() []Proof {
	proofs := append([]Proof(nil), t.Current().Proofs...)
	if t.Prepared != nil && t.Current().NeedsVerification() {
		proofs = append(proofs, Proof{ID: "verification", Requirement: t.Prepared.Requirement})
	}
	return proofs
}

func ValidateRoleResult(role string, r Result, proofs []Proof) error {
	if !PlanningRole(role) {
		return ValidateResult(r, proofs)
	}
	if r.Planning == nil || len(r.Checks) != 0 || strings.TrimSpace(r.Summary) == "" {
		return errors.New("planner must return a planning result and summary, not execution checks")
	}
	p := r.Planning
	if r.Outcome != "succeeded" && r.Outcome != "needs_input" && r.Outcome != "blocked" {
		return errors.New("invalid planning outcome")
	}
	for _, q := range p.Questions {
		if strings.TrimSpace(q) == "" {
			return errors.New("empty planning question")
		}
	}
	if r.Outcome == "needs_input" && len(p.Questions) == 0 {
		return errors.New("planner decision request must identify its questions")
	}
	if role == "prepare" {
		if len(p.Cards) != 0 {
			return errors.New("verification preparation cannot decompose or rewrite the task")
		}
		if r.Outcome == "succeeded" && len(p.Questions) == 0 && strings.TrimSpace(p.Verification) == "" {
			return errors.New("missing prepared verification brief")
		}
	} else {
		if p.Verification != "" {
			return errors.New("plan verification belongs in each card")
		}
		if len(p.Cards) != 0 || (r.Outcome == "succeeded" && len(p.Questions) == 0) {
			return ValidateProposal(*p)
		}
	}
	return nil
}

var cardKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func ValidateProposal(p PlanningResult) error {
	if len(p.Cards) == 0 {
		return errors.New("proposal must contain at least one complete card")
	}
	cards := map[string]PlannedCard{}
	for _, c := range p.Cards {
		if !cardKey.MatchString(c.Key) {
			return fmt.Errorf("invalid card key %q", c.Key)
		}
		if _, exists := cards[c.Key]; exists {
			return fmt.Errorf("duplicate card key %s", c.Key)
		}
		v, err := NewVersion(1, c.Body, "", "", "")
		if err != nil {
			return fmt.Errorf("card %s: %w", c.Key, err)
		}
		if v.NeedsVerification() {
			return fmt.Errorf("card %s needs a verification brief", c.Key)
		}
		cards[c.Key] = c
	}
	visited, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		c, exists := cards[key]
		if !exists {
			return fmt.Errorf("missing dependency %s", key)
		}
		if visiting[key] {
			return fmt.Errorf("dependency cycle at %s", key)
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		seen := map[string]bool{}
		for _, dep := range c.Dependencies {
			if seen[dep] {
				return fmt.Errorf("duplicate dependency %s on %s", dep, key)
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		return nil
	}
	for _, c := range p.Cards {
		if err := visit(c.Key); err != nil {
			return err
		}
	}
	return nil
}
func ReleasePreview(t Task, repository, policy, quality, base string) string {
	return Digest(struct {
		Approval   string
		Generation int
		Proposal   *PlanningResult
	}{Preview(t, repository, policy, quality, base), t.Generation, t.Proposal})
}

// PrerequisiteWait never treats reviewed work as integrated work. Dependency
// versions and parent release authority are rechecked in the admission transaction.
func PrerequisiteWait(t Task, tasks []Task) string {
	byID := map[string]Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	if t.ParentID != "" {
		parent, ok := byID[t.ParentID]
		if !ok || parent.Stage == Stopped || parent.Release == nil {
			return "parent plan is missing or revoked"
		}
		member := false
		for _, id := range parent.Release.Children {
			if id == t.ID {
				member = true
			}
		}
		if !member {
			return "task is not in its parent's approved release"
		}
	}
	for _, dep := range t.Current().Dependencies {
		task, ok := byID[dep.TaskID]
		if !ok {
			return "missing dependency " + dep.TaskID
		}
		if task.Current().Number != dep.Version {
			return "dependency version changed: " + dep.TaskID + "; create a new plan"
		}
		if task.Stage != Completed {
			return fmt.Sprintf("dependency %s v%d awaits confirmed integration (%s)", dep.TaskID, dep.Version, task.Stage)
		}
	}
	return ""
}
