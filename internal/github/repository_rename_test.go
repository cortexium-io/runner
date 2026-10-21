package github

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestRepositoryRenameBindingIsRestrictedToTheExactReleasedBatch(t *testing.T) {
	for _, change := range []string{"none", "body", "source body", "source identity", "child identity", "missing child", "dependency", "profile", "repository", "project", "runner", "unsigned rename", "ordinary release", "staged", "delivery"} {
		t.Run(change, func(t *testing.T) {
			project := NewProject(config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/new"}}, nil)
			source := WorkItem{ID: "PVTI_parent", Body: "Approved source", Repository: "owner/old"}
			child := WorkItem{ID: "PVTI_child", Title: "Exact child", Body: "Approved requirements", Repository: "owner/old", PlanningSourceID: source.ID,
				PlanningSourceLane: "plan", PlanningSourceFingerprint: PlanningSourceFingerprint(source), PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 1, PlanningItemIndex: 1}
			key, authority, err := project.authority.load()
			if err != nil {
				t.Fatal(err)
			}
			payload, err := project.planningBatchPayload(source, []WorkItem{child}, batchReleasedState, "generation", authority)
			if err != nil {
				t.Fatal(err)
			}
			payload.RepositoryRename = &repositoryRenameBinding{From: "owner/old", To: "owner/new", ID: 1234, NodeID: "R_same_repository"}
			source.Repository, child.Repository = "owner/new", "owner/new"
			state := batchReleasedState
			if change == "ordinary release" {
				payload.RepositoryRename = nil
			}
			if change == "staged" {
				payload.State, state = batchStagedState, batchStagedState
			}
			if change == "delivery" {
				payload.PlanRevision = "v1:delivery"
			}
			assertion, err := signPlanningBatchAssertion(payload, key)
			if err != nil {
				t.Fatal(err)
			}
			children := []WorkItem{child}
			switch change {
			case "body":
				children[0].Body += " Unapproved requirement"
			case "source body":
				source.Body += " Unapproved source"
			case "source identity":
				source.ID = "PVTI_other_parent"
			case "child identity":
				children[0].ID = "PVTI_other_child"
			case "missing child":
				children = nil
			case "dependency":
				children[0].Dependencies = []string{"PVTI_other"}
			case "profile":
				children[0].ImplementationProfile = "different"
			case "repository":
				source.Repository, children[0].Repository = "owner/other", "owner/other"
			case "project":
				project.cfg.Number++
			case "runner":
				project.authority = newEphemeralOperatorAuthority()
			case "unsigned rename":
				parts := strings.Split(assertion, ":")
				payload.RepositoryRename.To = "owner/unapproved"
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				parts[1] = base64.RawURLEncoding.EncodeToString(encoded)
				assertion = strings.Join(parts, ":")
			}
			_, err = project.validatePlanningBatch(assertion, source, children, state)
			if change == "none" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("rename binding widened authority after %s changed", change)
			}
			if change == "unsigned rename" && !strings.Contains(err.Error(), "invalid signature") {
				t.Fatalf("rename fields were not authenticated: %v", err)
			}
		})
	}
}
