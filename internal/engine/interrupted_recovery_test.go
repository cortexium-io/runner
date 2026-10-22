package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type interruptedReadFailureRunner struct {
	delegate subprocess.Runner
	failRead func(string) bool
}

func (r *interruptedReadFailureRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && r.failRead != nil && r.failRead(strings.Join(args, " ")) {
		r.failRead = nil
		return subprocess.Result{}, errors.New("temporary GitHub read failure: " + strings.Repeat("x", 4000))
	}
	return r.delegate.Run(ctx, command, args, dir, timeout)
}

func TestInterruptedRecoveryDefersReadFailuresWithoutRevokingAuthority(t *testing.T) {
	for _, stage := range []string{"fresh item", "delivery cohort"} {
		t.Run(stage, func(t *testing.T) {
			service, project, runner, _ := branchlessReauthorizationFixture(t)
			item := &project.remoteItems[3]
			item.Status, item.Activity = "In Progress", "Implementing"
			item.Approval = testApproval(*item)
			failing := &interruptedReadFailureRunner{delegate: runner}
			source, err := github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, failing)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.LifecycleItems(t.Context()); err != nil {
				t.Fatal(err)
			}
			if stage == "fresh item" {
				failing.failRead = isDirectProjectItemCall
			} else {
				failing.failRead = isLifecycleItemsCall
			}
			before := append([]github.WorkItem(nil), project.remoteItems...)
			recovered, err := source.RecoverInterruptedFrom(t.Context(), []github.WorkItem{*item})
			if recovered != 0 || err == nil || !strings.Contains(err.Error(), "temporary GitHub read failure") || len(err.Error()) > 2100 {
				t.Fatalf("failure lost its bounded local cause: recovered=%d err=%v", recovered, err)
			}
			if !reflect.DeepEqual(before, project.remoteItems) {
				t.Fatal("temporary read failure revoked approval or changed runtime fields")
			}
			recovered, err = source.RecoverInterruptedFrom(t.Context(), []github.WorkItem{*item})
			if err != nil || recovered != 1 {
				t.Fatalf("normal next poll did not recover: %d %v", recovered, err)
			}
			if item.Status != "Ready" || item.Approval == "" || item.Phase != "" || item.QAFailures != 0 {
				t.Fatal("normal recovery lost existing action state")
			}
			if _, err := source.Authorize(t.Context(), *item); err != nil {
				t.Fatalf("recovered authority invalid: %v", err)
			}
			for i, current := range project.remoteItems {
				if i != 3 && !reflect.DeepEqual(current, before[i]) {
					t.Fatal("recovery changed sibling or independent active work")
				}
			}
		})
	}
}

func TestInterruptedRecoveryStillQuarantinesInvalidLocalAuthority(t *testing.T) {
	service, project, _, _ := branchlessReauthorizationFixture(t)
	item := &project.remoteItems[3]
	item.Status, item.Activity = "In Progress", "Implementing"
	item.Approval = testApproval(*item)
	item.Body += "\nUnsigned scope change"
	before := append([]github.WorkItem(nil), project.remoteItems...)
	recovered, err := service.source.RecoverInterruptedFrom(t.Context(), []github.WorkItem{*item})
	if recovered != 0 || err == nil || !strings.Contains(err.Error(), "recovery:") {
		t.Fatalf("invalid signature did not return a local cause: %d %v", recovered, err)
	}
	if item.Status != "Needs assessment" || item.Approval != "" || item.Activity != "" || item.Phase != "ready" || item.QAFailures != 0 || !strings.Contains(item.Result, "invalid Runner authority") {
		t.Fatal("invalid authority was not parked safely")
	}
	for i, current := range project.remoteItems {
		if i != 3 && !reflect.DeepEqual(current, before[i]) {
			t.Fatal("invalid selected authority changed a sibling")
		}
	}
}
