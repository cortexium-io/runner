package engine

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

func TestPreparePollPrioritizesReviewsWithoutStarvingImplementation(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	var encoded []string
	for _, item := range []github.WorkItem{
		{ID: "PVTI_build", Title: "Implement", Status: "Ready"},
		{ID: "PVTI_review_one", Title: "Review one", Status: "Agent QA"},
		{ID: "PVTI_review_two", Title: "Review two", Status: "Agent QA"},
		{ID: "PVTI_review_three", Title: "Review three", Status: "Agent QA"},
	} {
		item.Repository, item.Body = "owner/repo", "Approved criteria"
		item.Approval = testApproval(item)
		encoded = append(encoded, projectItemJSON(item))
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + strings.Join(encoded, ",") + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, MaxParallelism: 4,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	// Admit one action per poll while capacity remains available. Successful
	// claims carry the fairness preference across coordinator polls.
	inFlight := map[string][]string{}
	for _, want := range []string{"PVTI_review_one", "PVTI_review_two", "PVTI_build", "PVTI_review_three"} {
		poll, err := service.preparePoll(t.Context(), 1, false, inFlight)
		if err != nil || len(poll.claimed) != 1 {
			t.Fatalf("claim %s: %d actions, error %v", want, len(poll.claimed), err)
		}
		admitted := poll.claimed[0]
		if got := admitted.action.Item.ID; got != want {
			t.Fatalf("claim = %s, want %s", got, want)
		}
		inFlight[want] = admitted.resources
	}
}
