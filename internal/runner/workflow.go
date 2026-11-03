package runner

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type DecisionRequest struct {
	RequestID  string `json:"request_id"`
	RunID      string `json:"run_id"`
	DecisionID string `json:"decision_id"`
	Choice     string `json:"choice"`
}

// Decide consumes a particular saved decision. Replays cannot approve a newer
// candidate, and retry cannot substitute for approval.
func (c *Core) Decide(req DecisionRequest) (Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	args, _ := json.Marshal(req)
	if r, ok, err := c.replay(req.RequestID, "decide", args); ok || err != nil {
		return r, err
	}
	r, ok := c.runs[req.RunID]
	if !ok {
		return Run{}, ErrNotFound
	}
	r = Clone(r)
	if r.Status != "Waiting" || r.Active() || r.Decision == nil || r.Decision.ID != req.DecisionID {
		return Run{}, errors.New("decision is not pending for this run/candidate")
	}
	if req.Choice != "approve" && req.Choice != "reject" {
		return Run{}, errors.New("choice must be approve or reject")
	}
	d := r.Decision
	if req.Choice == "reject" {
		r.Status, r.Reason = "Failed", "decision rejected: "+d.Step
		r.Decision = nil
	} else {
		a := r.Current()
		if a == nil || a.ID != d.AttemptID || a.End == nil || a.End.Result == nil || a.End.Result.Outcome != d.Outcome {
			return Run{}, errors.New("decision evidence is no longer current")
		}
		r.Decision = nil
		c.advance(&r, a.End.Result, r.Process.Steps[r.Step].Routes[d.Outcome])
	}
	rec := Receipt{Key: req.RequestID, Kind: "decide", Args: args, RunID: r.ID}
	if err := c.persist(r, nil, &rec); err != nil {
		return Run{}, err
	}
	c.Wake()
	return Clone(r), nil
}

func stepOutcomes(s Step) []string {
	out := []string{"ready"}
	for name := range s.Routes {
		if name != "ready" {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func (c *Core) route(r *Run, a *Attempt) {
	step := r.Process.Steps[r.Step]
	result := a.End.Result
	route, explicit := step.Routes[result.Outcome]
	if !explicit {
		if result.Outcome != "ready" {
			r.Status, r.Reason = "Failed", "no route for outcome "+result.Outcome
			return
		}
		route.Next = "$complete"
		if r.Step+1 < len(r.Process.Steps) {
			route.Next = r.Process.Steps[r.Step+1].ID
		}
	}
	key := step.ID + "/" + result.Outcome
	if route.MaxTimes > 0 && r.RouteUses[key] >= route.MaxTimes {
		r.Status, r.Reason = "Failed", "repetition allowance exhausted: "+key
		return
	}
	if route.Approval {
		r.Decision = &Decision{
			ID:        "decision_" + rand.Text(),
			AttemptID: a.ID,
			Step:      step.ID,
			Outcome:   result.Outcome,
			Next:      route.Next,
			Reason:    Clip(result.Reason),
		}
		r.Status, r.Reason = "Waiting", "approval required: "+step.ID+" → "+route.Next
		return
	}
	c.advance(r, result, route)
}

func (c *Core) advance(r *Run, result *StepResult, route Route) {
	key := r.Process.Steps[r.Step].ID + "/" + result.Outcome
	if r.RouteUses == nil {
		r.RouteUses = map[string]int{}
	}
	r.RouteUses[key]++
	r.NextProfile, r.Escalations = "", 0
	if route.Next == "$fail" {
		r.Status, r.Reason = "Failed", Clip(result.Reason)
		if r.Reason == "" {
			r.Reason = "process rejected outcome " + result.Outcome
		}
		return
	}
	if route.Next == "$complete" {
		outputs, err := bindings(*r, r.Process.Outputs)
		if err != nil {
			r.Status, r.Reason = "Failed", Clip(err.Error())
			return
		}
		r.Outputs, r.Status, r.Reason = outputs, "Completed", ""
		r.Step = len(r.Process.Steps)
		r.StepInputs = nil
		return
	}
	next := -1
	for i, step := range r.Process.Steps {
		if step.ID == route.Next {
			next = i
			break
		}
	}
	if next < 0 {
		r.Status, r.Reason = "Failed", "route target is unavailable"
		return
	}
	// Freeze feedback and the retained patch before invalidating downstream
	// results. A later finish cannot reuse proof for an earlier candidate.
	inputs, err := bindings(*r, r.Process.Steps[next].Inputs)
	if err != nil {
		r.Status, r.Reason = "Failed", fmt.Sprintf("route to %s: %v", route.Next, err)
		return
	}
	if next <= r.Step {
		for i := range r.Attempts {
			if r.Attempts[i].Step >= next {
				r.Attempts[i].Superseded = true
			}
		}
	}
	r.StepInputs = inputs
	r.Step, r.Visit = next, r.Visit+1
	r.Status, r.Reason = "Queued", ""
}

func visitAttempts(r Run) int {
	n := 0
	for _, a := range r.Attempts {
		if a.Step == r.Step && a.Visit == r.Visit {
			n++
		}
	}
	return n
}
