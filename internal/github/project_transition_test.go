package github

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type transitionTestRunner struct {
	calls []string
}

func (r *transitionTestRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	r.calls = append(r.calls, command+" "+strings.Join(args, " "))
	return subprocess.Result{}, nil
}

func TestRecoverTransitionLockClearsCommittedStateWithoutAssessment(t *testing.T) {
	project, run := transitionTestProject()
	item := WorkItem{ID: "PVTI_ready", Body: "approved work", Repository: "owner/repo", Status: "Ready"}
	action, err := project.signAction(item, config.WorkRoleImplementer, "ready")
	if err != nil {
		t.Fatal(err)
	}
	locked := action.Item
	locked.Transition = transitionLockValue

	recovered, err := project.RecoverInterruptedFrom(t.Context(), []WorkItem{locked})
	if err != nil {
		t.Fatalf("recover committed transition: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	calls := strings.Join(run.calls, "\n")
	if !strings.Contains(calls, "--field-id F_transition --clear") {
		t.Fatalf("transition lock was not cleared: %s", calls)
	}
	if strings.Contains(calls, "--single-select-option-id O_assessment") {
		t.Fatalf("valid committed state was moved to assessment: %s", calls)
	}
}

func TestRecoverTransitionLockFailsClosedAfterPartialWrite(t *testing.T) {
	project, run := transitionTestProject()
	item := WorkItem{ID: "PVTI_ready", Body: "approved work", Repository: "owner/repo", Status: "Ready"}
	action, err := project.signAction(item, config.WorkRoleImplementer, "ready")
	if err != nil {
		t.Fatal(err)
	}
	locked := action.Item
	locked.Transition = transitionLockValue
	locked.Result = "partially written result"

	recovered, err := project.RecoverInterruptedFrom(t.Context(), []WorkItem{locked})
	if err != nil {
		t.Fatalf("recover partial transition: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	calls := strings.Join(run.calls, "\n")
	assessmentAt := strings.Index(calls, "--single-select-option-id O_assessment")
	approvalAt := strings.Index(calls, "--field-id F_approval --clear")
	unlockAt := strings.Index(calls, "--field-id F_transition --clear")
	if assessmentAt < 0 || approvalAt <= assessmentAt || unlockAt <= approvalAt {
		t.Fatalf("partial transition was not parked and deauthorized before unlock: %s", calls)
	}
}

func TestReadyItemsDoNotExecuteTransitionLockedCard(t *testing.T) {
	project, run := transitionTestProject()
	item := WorkItem{ID: "PVTI_ready", Body: "approved work", Repository: "owner/repo", Status: "Ready"}
	action, err := project.signAction(item, config.WorkRoleImplementer, "ready")
	if err != nil {
		t.Fatal(err)
	}
	locked := action.Item
	locked.Transition = transitionLockValue

	ready, err := project.ReadyItems(t.Context(), []WorkItem{locked}, 1)
	if err != nil {
		t.Fatalf("poll locked card: %v", err)
	}
	if len(ready) != 0 || len(run.calls) != 0 {
		t.Fatalf("locked card became executable: ready=%#v calls=%#v", ready, run.calls)
	}
}

func transitionTestProject() (*Project, *transitionTestRunner) {
	run := &transitionTestRunner{}
	project := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{
			Owner: "owner", Number: 1, ResultField: "Runner Result", ApprovalField: "Runner Approval",
			PhaseField: "Runner Phase", TransitionField: config.RunnerTransitionFieldName,
		},
		ApprovalAuthorityKey: []byte("transition-test-authority-key-32"),
		AssessmentStatus:     "Needs assessment",
		AgentStatuses:        []string{"Ready"},
		LaneStatuses:         map[string]string{"needs_assessment": "Needs assessment", "ready": "Ready"},
		LaneRoles:            map[string]string{"ready": config.WorkRoleImplementer},
	}, run)
	project.schema = githubProjectSchema{ProjectID: "PVT_test", Fields: map[string]githubProjectField{
		normalizeProjectKey("Status"): {
			ID: "F_status", Name: "Status", Type: "ProjectV2SingleSelectField",
			Options: map[string]githubProjectOption{normalizeProjectKey("Needs assessment"): {ID: "O_assessment", Name: "Needs assessment"}},
		},
		normalizeProjectKey("Runner Result"):     {ID: "F_result", Name: "Runner Result", Type: "ProjectV2Field"},
		normalizeProjectKey("Runner Approval"):   {ID: "F_approval", Name: "Runner Approval", Type: "ProjectV2Field"},
		normalizeProjectKey("Runner Transition"): {ID: "F_transition", Name: "Runner Transition", Type: "ProjectV2Field"},
	}}
	return project, run
}
