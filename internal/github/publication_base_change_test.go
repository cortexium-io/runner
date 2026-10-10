package github

import (
	"errors"
	"strings"
	"testing"
)

func TestPublishedPullRequestClassifiesOnlyExactHeadBaseAdvances(t *testing.T) {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name        string
		change      func(*PullRequestDetails)
		baseChanged bool
	}{
		{name: "new base", baseChanged: true},
		{name: "changed head", change: func(d *PullRequestDetails) { d.HeadRefOID = strings.Repeat("d", 40) }},
		{name: "changed branch", change: func(d *PullRequestDetails) { d.HeadRefName = "other" }},
		{name: "changed destination", change: func(d *PullRequestDetails) { d.BaseRefName = "develop" }},
		{name: "foreign head repository", change: func(d *PullRequestDetails) { d.HeadRepository = "other/repo" }},
		{name: "unknown base", change: func(d *PullRequestDetails) { d.BaseRefOID = "" }},
		{name: "closed PR", change: func(d *PullRequestDetails) { d.State = "CLOSED" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			details := PullRequestDetails{Repository: "owner/repo", HeadRepository: "owner/repo", State: "OPEN", HeadRefName: "runner/task", HeadRefOID: head, BaseRefName: "main", BaseRefOID: strings.Repeat("c", 40)}
			if tc.change != nil {
				tc.change(&details)
			}
			err := validatePublishedPullRequest(details, "owner/repo", "runner/task", head, "main", base)
			if err == nil || errors.Is(err, ErrPublicationBaseChanged) != tc.baseChanged {
				t.Fatalf("classification=%v, want baseChanged=%t", err, tc.baseChanged)
			}
		})
	}
}
