package execution

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/workspace"
)

func deliveryReviewAssignment() Assignment {
	a := reviewerAssignment()
	a.Spec.ItemID = "parent"
	a.Spec.PlanContext = &PlanContext{
		ID: "parent", Revision: "approved-revision", ApprovedBody: "Deliver the combined record editing journey.",
		Repository: a.Spec.Repository, DestinationBranch: "develop", Branch: "runner/plan-parent", MemberIDs: []string{"child-a", "child-b"},
	}
	a.Spec.ReviewScope = ReviewScopePlan
	a.Spec.PlanMemberBriefs = []PlanMemberBrief{{ID: "child-a", ApprovedBody: "Implement record editing"}, {ID: "child-b", ApprovedBody: "Use record editing"}}
	a.Spec.VerificationBoundary = VerificationComplete
	return a
}

func TestAssignmentDeliveryContextValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Spec)
		valid  bool
	}{
		{"whole plan", func(*Spec) {}, true},
		{"focused member", func(s *Spec) {
			s.PlanMemberBriefs = nil
			s.ItemID = "child-a"
			s.ReviewScope = ReviewScopeCard
			s.VerificationBoundary = VerificationFocused
		}, true},
		{"missing parent", func(s *Spec) { s.PlanContext = nil }, false},
		{"wrong repository", func(s *Spec) { s.Repository = "other/repo" }, false},
		{"missing revision", func(s *Spec) { s.PlanContext.Revision = "" }, false},
		{"missing shared body", func(s *Spec) { s.PlanContext.ApprovedBody = " " }, false},
		{"empty membership", func(s *Spec) { s.PlanContext.MemberIDs = nil }, false},
		{"duplicate membership", func(s *Spec) { s.PlanContext.MemberIDs = []string{"child-a", "child-a"} }, false},
		{"parent as member", func(s *Spec) { s.PlanContext.MemberIDs = []string{"parent"} }, false},
		{"destination as plan branch", func(s *Spec) { s.PlanContext.Branch = "develop" }, false},
		{"child claims plan review", func(s *Spec) { s.ItemID = "child-a" }, false},
		{"plan review without reviewer", func(s *Spec) { s.ReviewRequired = false }, false},
		{"focused review claimed complete", func(s *Spec) { s.ReviewScope = ReviewScopeCard; s.ItemID = "child-a" }, false},
		{"unknown boundary", func(s *Spec) { s.VerificationBoundary = "best-effort" }, false},
		{"unlisted member", func(s *Spec) {
			s.ReviewScope = ReviewScopeCard
			s.VerificationBoundary = VerificationFocused
			s.ItemID = "outsider"
		}, false},
		{"legacy standalone", func(s *Spec) {
			s.PlanMemberBriefs = nil
			s.PlanContext = nil
			s.ReviewScope = ""
			s.VerificationBoundary = ""
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := deliveryReviewAssignment()
			tc.change(&a.Spec)
			if err := ValidateAssignmentContext(a.Spec); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestInvalidPlanContextCannotLaunchHarnessOrPrepareWorkspace(t *testing.T) {
	a := deliveryReviewAssignment()
	a.Spec.PlanContext.Revision = ""
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		for _, write := range []bool{false, true} {
			t.Run(kind+"/write="+strconv.FormatBool(write), func(t *testing.T) {
				run := &sharedReviewerHarnessRunner{}
				cfg := testCodexConfig(t)
				cfg.Harness.Kind, cfg.Harness.Command = kind, kind
				var output Output
				var err error
				if kind == config.HarnessCodexCLI {
					executor := NewCodexExecutor(cfg, run)
					if write {
						output, err = executor.ExecuteWorkspaceWrite(t.Context(), a, nil)
					} else {
						output, err = executor.Execute(t.Context(), a)
					}
				} else {
					executor := NewAgentExecutor(kind, cfg, run)
					if write {
						output, err = executor.ExecuteWorkspaceWrite(t.Context(), a, nil)
					} else {
						output, err = executor.Execute(t.Context(), a)
					}
				}
				if err == nil || output.FailureClass != FailureInvalidContract || len(run.inputs) != 0 {
					t.Fatalf("invalid plan crossed execution boundary: %#v, %v, calls=%d", output, err, len(run.inputs))
				}
			})
		}
	}
}

func TestWorkspaceWriteRejectsValidReviewBeforeProbesOrWorkspace(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		for _, scope := range []ReviewScope{ReviewScopePlan, ReviewScopeCard} {
			t.Run(kind+"/"+string(scope), func(t *testing.T) {
				a := deliveryReviewAssignment()
				if scope == ReviewScopeCard {
					a.Spec.PlanMemberBriefs = nil
					a.Spec.ItemID = "child-a"
					a.Spec.ReviewScope = ReviewScopeCard
					a.Spec.VerificationBoundary = VerificationFocused
				}
				if !a.Spec.ReviewRequired {
					t.Fatal("fixture must request review")
				}
				if err := ValidateAssignmentContext(a.Spec); err != nil {
					t.Fatalf("fixture must be a valid review assignment: %v", err)
				}
				cfg := testWorkspaceWriteConfig(t)
				cfg.Harness.Kind, cfg.Harness.Command = kind, kind
				if kind == config.HarnessPiCLI {
					cfg.RoleAccess = config.RoleAccessHost
				}
				calls := 0
				run := &sharedReviewerHarnessRunner{onRun: func(string) error {
					calls++
					return errors.New("unexpected subprocess call")
				}}
				capture := &workspaceRequestCapture{err: errors.New("unexpected workspace preparation")}
				prepared := false
				onPrepared := func(workspace.Metadata) error { prepared = true; return nil }
				var output Output
				var err error
				if kind == config.HarnessCodexCLI {
					executor := NewCodexExecutor(cfg, run)
					executor.workspaceProvider = capture
					output, err = executor.ExecuteWorkspaceWrite(t.Context(), a, onPrepared)
				} else {
					executor := NewAgentExecutor(kind, cfg, run)
					executor.workspaceProvider = capture
					output, err = executor.ExecuteWorkspaceWrite(t.Context(), a, onPrepared)
				}
				if err == nil || output.Outcome != OutcomeBlocked || output.FailureClass != FailureInvalidContract || output.RetryDisposition != RetryNone {
					t.Errorf("review misroute was not rejected as an invalid contract: %#v, %v", output, err)
				}
				if calls != 0 || len(capture.requests) != 0 || prepared {
					t.Errorf("review crossed write boundary: subprocess calls=%d, workspace requests=%d, prepared=%v", calls, len(capture.requests), prepared)
				}
			})
		}
	}
}

func TestPlanContextIsRenderedOnceAsVariableAssignmentData(t *testing.T) {
	a := deliveryReviewAssignment()
	for name, prompt := range map[string]string{
		"implementation": buildHarnessPrompt(a, true, "Codex CLI"),
		"audit":          reviewerAuditPrompt(a, "Codex CLI"),
		"focused":        reviewerResolutionPrompt(a, "Codex CLI", []reviewerUnresolvedCheck{{Key: "P1", ProofObligation: a.Spec.RequiredVerification[0], Question: "Does the combined flow retain ownership?"}}),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Count(prompt, a.Spec.PlanContext.ApprovedBody) != 1 {
				t.Fatal("shared plan brief duplicated or lost")
			}
			_, data, found := strings.Cut(prompt, "Runner-verified delivery context (fixed for this assignment; does not authorize sibling work or new requirements):\n")
			if !found {
				t.Fatal("delivery context absent")
			}
			line, _, _ := strings.Cut(data, "\n")
			var decoded struct {
				Plan     *PlanContext         `json:"plan"`
				Scope    ReviewScope          `json:"review_scope"`
				Boundary VerificationBoundary `json:"verification_boundary"`
			}
			if err := json.Unmarshal([]byte(line), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Plan.Revision != a.Spec.PlanContext.Revision || decoded.Plan.MemberIDs[1] != "child-b" || decoded.Scope != ReviewScopePlan || decoded.Boundary != VerificationComplete {
				t.Fatalf("lost delivery bindings: %#v", decoded)
			}
			if strings.Index(prompt, "Title: ") > strings.Index(prompt, line) {
				t.Fatal("plan data entered stable prompt prefix")
			}
		})
	}
}
