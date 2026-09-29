package github

import (
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/presentation"
)

func runnerProjectResult(detail string) (string, error) {
	return presentation.PublishRemoteText(
		presentation.RemoteProvenanceRunnerClassification,
		presentation.RemoteDestinationProjectField,
		detail,
		"",
	)
}

func pullRequestFeedbackProjectResult(repository, pullRequest string) (string, error) {
	if _, err := validatedPullRequestSelector(repository, pullRequest); err != nil {
		return "", fmt.Errorf("persist pull request feedback: %w", err)
	}
	return presentation.PublishRemoteText(
		presentation.RemoteProvenancePullRequestFeedback,
		presentation.RemoteDestinationProjectField,
		"",
		pullRequest,
	)
}

func runnerPullRequestBody(sourceURL, candidateCommit string) (string, error) {
	candidateCommit = strings.TrimSpace(candidateCommit)
	if !validGitObjectID(candidateCommit) {
		return "", fmt.Errorf("pull request report requires a valid reviewed commit")
	}
	var body strings.Builder
	body.WriteString("Created by the local Project Runner after agent QA passed.")
	if sourceURL = strings.TrimSpace(sourceURL); sourceURL != "" {
		body.WriteString("\n\nSource: ")
		body.WriteString(presentation.MarkdownInline(sourceURL))
	}
	fmt.Fprintf(&body, "\n\n## Agent QA\n\nAccepted for reviewed commit `%s`. QA acceptance does not establish merge, CI, or deployment status.", candidateCommit)
	if _, _, _, supported := issueReference(sourceURL); supported {
		body.WriteString("\n\nSee the source issue's Agent QA completion report for this commit: delivered result, requested outcomes, reviewer-reported evidence, assumptions, and verification limits. If the comment is unavailable, the accepted report is retained locally.")
	} else {
		body.WriteString("\n\nThis card has no supported source issue. Its completion report is retained locally.")
	}
	body.WriteString("\n\nRaw harness diagnostics remain local.")
	return presentation.PublishRemoteText(
		presentation.RemoteProvenanceRunnerClassification,
		presentation.RemoteDestinationPullRequestBody,
		body.String(),
		"",
	)
}
