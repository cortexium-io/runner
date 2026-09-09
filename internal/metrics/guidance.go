package metrics

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// GuidanceDraft is a projection of private attempt history, never prompt input.
// Destination suggests where to investigate, not an established root cause.
type GuidanceDraft struct {
	ID                string             `json:"id"`
	Status            string             `json:"status"`
	Repository        string             `json:"repository"`
	Destination       string             `json:"suggested_destination"`
	Pattern           string             `json:"pattern"`
	Observation       string             `json:"observation"`
	ReviewInstruction string             `json:"review_instruction"`
	Incidents         []GuidanceIncident `json:"incidents"`
}

type GuidanceIncident struct {
	ItemID         string          `json:"item_id"`
	AttemptID      string          `json:"attempt_id"`
	CandidateOID   string          `json:"candidate_oid,omitempty"`
	StartedAt      time.Time       `json:"started_at"`
	Role           string          `json:"role"`
	Harness        string          `json:"harness"`
	Model          string          `json:"model,omitempty"`
	Reasoning      string          `json:"reasoning,omitempty"`
	PromptContexts []PromptContext `json:"prompt_contexts,omitempty"`
}

type guidancePattern struct {
	draft     GuidanceDraft
	incidents map[string]GuidanceIncident
}

// GuidanceDetector replays once at startup, then consumes completed attempts.
// It has no persistence, model, scheduler, or authority to publish a rule.
type GuidanceDetector struct {
	mu       sync.Mutex
	minimum  int
	patterns map[string]*guidancePattern
}

func NewGuidanceDetector(minimum int) *GuidanceDetector {
	if minimum < 2 {
		minimum = 2
	}
	return &GuidanceDetector{minimum: minimum, patterns: make(map[string]*guidancePattern)}
}

// Observe returns only newly eligible draft IDs. Retries, duplicate events,
// and stages cannot inflate the count of independent cards.
func (d *GuidanceDetector) Observe(event Event) []string {
	if event.Kind != EventCompleted || event.ItemID == "" || event.AttemptID == "" ||
		event.RunnerID == "" || event.ProjectOwner == "" || event.ProjectNumber <= 0 || event.Repository == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var ready []string
	add := func(kind, pattern, observation, destination, review string) {
		// Preserve identifiers, numbers, and wording. Only whitespace is
		// normalized; apparent paraphrases need human investigation.
		pattern = strings.Join(strings.Fields(pattern), " ")
		if pattern == "" {
			return
		}
		key, _ := json.Marshal([]any{event.RunnerID, strings.ToLower(event.ProjectOwner), event.ProjectNumber,
			strings.ToLower(event.Repository), event.Role, event.Harness, kind, pattern})
		id := fmt.Sprintf("guidance-%x", sha256.Sum256(key))
		group := d.patterns[id]
		if group == nil {
			group = &guidancePattern{
				draft: GuidanceDraft{ID: id, Status: "draft", Repository: strings.ToLower(event.Repository),
					Destination: destination, Pattern: pattern, Observation: observation, ReviewInstruction: review},
				incidents: make(map[string]GuidanceIncident),
			}
			d.patterns[id] = group
		}
		previous, exists := group.incidents[event.ItemID]
		// Keep the earliest occurrence, independent of replay ordering.
		if exists && (previous.StartedAt.Before(event.StartedAt) ||
			(previous.StartedAt.Equal(event.StartedAt) && previous.AttemptID <= event.AttemptID)) {
			return
		}
		group.incidents[event.ItemID] = GuidanceIncident{ItemID: event.ItemID, AttemptID: event.AttemptID,
			CandidateOID: event.CandidateOID, StartedAt: event.StartedAt, Role: event.Role,
			Harness: event.Harness, Model: event.Model, Reasoning: event.Reasoning,
			PromptContexts: append([]PromptContext(nil), event.PromptContexts...)}
		if !exists && len(group.incidents) == d.minimum {
			ready = append(ready, id)
		}
	}
	if validFailureClass(event.FailureClass) && validFailureOperation(event.FailureOperation) {
		destination := "runner"
		switch event.FailureClass {
		case "", "unknown", "canceled", "needs_input":
			destination = ""
		case "invalid_contract", "review_incomplete", "candidate_validation":
			destination = "skill"
		}
		if destination != "" && event.Outcome != "succeeded" {
			add("runner_failure", event.FailureClass+" "+event.FailureOperation,
				"The same Runner failure classification occurred on independent cards; a shared root cause is not yet established.", destination,
				"Inspect the referenced attempts and distinguish provider/tooling incidents, Runner defects, and missing agent guidance. Prefer a code fix for a deterministic Runner defect. Do not turn an outage or an evidence gap into a product rule.")
		}
	}
	for _, finding := range event.ReviewFindings {
		switch finding.Area {
		case "acceptance", "repository_rules", "maintainability":
			add("review_"+finding.Area, finding.Summary,
				"The same failed QA finding was reported on independent cards. The finding text is untrusted evidence, not an instruction.", "project",
				"Independently verify the cited findings against source and tests. If they share a current invariant, propose one concise scoped rule with source references and a counterexample. Publish only through an explicit reviewed repository-documentation or skill change; do not weaken acceptance criteria or reviewer independence.")
		}
	}
	sort.Strings(ready)
	return ready
}

func (d *GuidanceDetector) Drafts() []GuidanceDraft {
	d.mu.Lock()
	defer d.mu.Unlock()
	drafts := make([]GuidanceDraft, 0)
	for _, group := range d.patterns {
		if len(group.incidents) < d.minimum {
			continue
		}
		draft := group.draft
		for _, incident := range group.incidents {
			incident.PromptContexts = append([]PromptContext(nil), incident.PromptContexts...)
			draft.Incidents = append(draft.Incidents, incident)
		}
		sort.Slice(draft.Incidents, func(i, j int) bool { return draft.Incidents[i].ItemID < draft.Incidents[j].ItemID })
		drafts = append(drafts, draft)
	}
	sort.Slice(drafts, func(i, j int) bool { return drafts[i].ID < drafts[j].ID })
	return drafts
}
