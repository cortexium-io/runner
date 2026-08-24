package presentation

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type RemoteProvenance string

const (
	RemoteProvenanceRunnerClassification RemoteProvenance = "runner_classification"
	RemoteProvenancePullRequestFeedback  RemoteProvenance = "pull_request_feedback"
	RemoteProvenanceModelText            RemoteProvenance = "model_text"
	RemoteProvenanceLocalDiagnostics     RemoteProvenance = "local_diagnostics"
)

type RemoteDestination string

const (
	RemoteDestinationProjectField    RemoteDestination = "project_field"
	RemoteDestinationPullRequestBody RemoteDestination = "pull_request_body"
)

// PublishRemoteText is the single remote publication policy for Runner-owned
// reports, GitHub-originated feedback references, and rejected local/model data.
func PublishRemoteText(provenance RemoteProvenance, destination RemoteDestination, text, referenceURL string) (string, error) {
	switch provenance {
	case RemoteProvenanceRunnerClassification:
		switch destination {
		case RemoteDestinationProjectField, RemoteDestinationPullRequestBody:
			text = strings.TrimSpace(text)
			if text == "" {
				return "", errors.New("runner classification publication requires non-empty text")
			}
			return text, nil
		default:
			return "", fmt.Errorf("runner classification does not support remote destination %q", destination)
		}
	case RemoteProvenancePullRequestFeedback:
		if destination != RemoteDestinationProjectField {
			return "", fmt.Errorf("pull request feedback cannot be published to remote destination %q", destination)
		}
		pullRequestURL, err := canonicalGitHubPullRequestURL(referenceURL)
		if err != nil {
			return "", err
		}
		return "Inspect the pull request discussion at " + pullRequestURL + " locally before continuing.", nil
	case RemoteProvenanceModelText, RemoteProvenanceLocalDiagnostics:
		return "", fmt.Errorf("%s cannot be published to %s", provenance, destination)
	default:
		return "", fmt.Errorf("unsupported remote publication provenance %q", provenance)
	}
}

// TerminalText preserves visible content while escaping control and formatting
// characters that could rewrite prior output, create OSC hyperlinks, or reorder
// displayed text.
func TerminalText(value string) string {
	var safe strings.Builder
	for _, character := range value {
		if character == '\\' {
			safe.WriteString(`\\`)
			continue
		}
		if !unicode.IsControl(character) && !unicode.In(character, unicode.Cf) {
			safe.WriteRune(character)
			continue
		}
		quoted := strconv.QuoteRuneToGraphic(character)
		safe.WriteString(quoted[1 : len(quoted)-1])
	}
	return safe.String()
}

// MarkdownInline escapes untrusted inline text before it is embedded in a
// Runner-owned Markdown template.
func MarkdownInline(value string) string {
	var safe strings.Builder
	for _, character := range value {
		if character == '\\' {
			safe.WriteString(`\\`)
			continue
		}
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			quoted := strconv.QuoteRuneToGraphic(character)
			safe.WriteString(quoted[1 : len(quoted)-1])
			continue
		}
		switch character {
		case '[', ']', '(', ')', '<', '>', '`', '*', '_', '#', '!':
			safe.WriteByte('\\')
		}
		safe.WriteRune(character)
	}
	return safe.String()
}

func canonicalGitHubPullRequestURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("pull request feedback requires a canonical GitHub pull request URL")
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return "", errors.New("pull request feedback requires a canonical GitHub pull request URL")
	}
	number, numberErr := strconv.Atoi(parts[3])
	if numberErr != nil || number <= 0 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("pull request feedback requires a canonical GitHub pull request URL")
	}
	return "https://github.com/" + parts[0] + "/" + parts[1] + "/pull/" + strconv.Itoa(number), nil
}
