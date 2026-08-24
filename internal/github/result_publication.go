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

func runnerPullRequestBody(sourceURL string) (string, error) {
	var body strings.Builder
	body.WriteString("Created by the local Project Runner after agent QA passed.")
	if sourceURL = strings.TrimSpace(sourceURL); sourceURL != "" {
		body.WriteString("\n\nSource: ")
		body.WriteString(presentation.MarkdownInline(sourceURL))
	}
	body.WriteString("\n\n## Agent QA\n\nRunner recorded an accepted QA classification for the exact published commit. Detailed model-authored evidence remains local.")
	return presentation.PublishRemoteText(
		presentation.RemoteProvenanceRunnerClassification,
		presentation.RemoteDestinationPullRequestBody,
		body.String(),
		"",
	)
}
