package github

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type PlanCancellationMember struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	Branch     string `json:"branch,omitempty"`
	QACommit   string `json:"qa_commit,omitempty"`
	QAFailures int    `json:"qa_failures"`
}

type PlanCancellation struct {
	ID               string                   `json:"id"`
	Revision         string                   `json:"revision"`
	Repository       string                   `json:"repository"`
	Branch           string                   `json:"branch"`
	QAFailures       int                      `json:"qa_failures"`
	AlreadyCancelled bool                     `json:"already_cancelled"`
	Members          []PlanCancellationMember `json:"retained_members"`
	delivery         PlanDelivery
}

func (s *Project) PlanCancellation(ctx context.Context, selector string) (PlanCancellation, error) {
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return PlanCancellation{}, err
	}
	parent, err := selectProjectItem(items, selector)
	if err != nil {
		return PlanCancellation{}, err
	}
	delivery, err := s.validatePlanDeliveryState(parent, items, true)
	if err != nil {
		return PlanCancellation{}, fmt.Errorf("cancellation requires intact exact plan authority: %w", err)
	}
	if parent.PullRequest != "" || strings.EqualFold(parent.Status, s.doneStatus()) {
		return PlanCancellation{}, errors.New("published or delivered plans require explicit PR/merge coordination; cancellation does not close PRs or undo delivery")
	}
	for _, item := range append([]WorkItem{parent}, delivery.AllChildren()...) {
		if strings.EqualFold(item.Status, s.runningStatus()) || item.Transition != "" || item.PullRequest != "" {
			return PlanCancellation{}, fmt.Errorf("plan item %s is active, transition-locked, or published; gracefully drain Runner and resolve interrupted/published state before cancellation", item.ID)
		}
	}
	// A lost publication response may leave the Project PR field empty. Refuse
	// any existing PR on the exact deterministic branch, including terminal PRs.
	manager := NewPullRequestManager(s.run, s)
	if _, found, err := manager.findPlanPublication(ctx, parent.Repository, parent.Branch, delivery.Manifest.DestinationBranch); err != nil {
		return PlanCancellation{}, fmt.Errorf("inspect publication before cancellation: %w", err)
	} else if found {
		return PlanCancellation{}, errors.New("plan branch already has a pull request; recover its exact publication before deciding how to cancel")
	}
	retained := delivery.AllChildren()
	sort.Slice(retained, func(i, j int) bool { return retained[i].ID < retained[j].ID })
	plan := PlanCancellation{ID: parent.ID, Revision: delivery.Revision, Repository: parent.Repository, Branch: parent.Branch,
		QAFailures: parent.QAFailures, AlreadyCancelled: parent.Phase == PlanCancelledPhase, delivery: delivery}
	for _, item := range retained {
		plan.Members = append(plan.Members, PlanCancellationMember{ID: item.ID, Status: item.Status, Phase: item.Phase, Branch: item.Branch, QACommit: item.QACommit, QAFailures: item.QAFailures})
	}
	return plan, nil
}

// ApplyPlanCancellation is called only under the engine's offline operator
// locks. One signed parent transition fences admission and all subsequent
// authority refreshes. Child state and immutable release authority are untouched.
func (s *Project) ApplyPlanCancellation(ctx context.Context, plan PlanCancellation) (PlanCancellation, error) {
	fresh, err := s.PlanCancellation(ctx, plan.ID)
	if err != nil {
		return PlanCancellation{}, err
	}
	if !reflect.DeepEqual(fresh, plan) {
		return PlanCancellation{}, errors.New("plan authority, members or publication changed after cancellation preview; inspect a fresh preview")
	}
	if plan.AlreadyCancelled {
		return fresh, nil
	}
	action, err := s.validateAction(fresh.delivery.Parent)
	if err != nil {
		return PlanCancellation{}, err
	}
	// Preserve the previous result as well as rejection counts and child proof.
	// Phase supplies the durable cancellation signal without overwriting feedback.
	if err := s.transition(ctx, action, s.backlogStatus(), "", PlanCancelledPhase, false,
		func(next *WorkItem) { next.Activity = "Cancelled — retained work; no new admission" }, nil); err != nil {
		return PlanCancellation{}, fmt.Errorf("cancellation did not complete cleanly; inspect/recover the existing parent transition, do not retry card work: %w", err)
	}
	return s.PlanCancellation(ctx, plan.ID)
}
