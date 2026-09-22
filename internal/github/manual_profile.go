package github

import (
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
)

const manualProfileHeading = "## Runner implementation profile"

// ManualImplementationProfile reads the visible selection on an ordinary Ready
// card. Its body and decoded profile are covered by the existing content approval;
// selection never confers planning/batch authority or changes the allowed profiles.
func ManualImplementationProfile(body string) (string, error) {
	if _, present, _ := decodePlannedItemMetadata(body); present {
		return "", errors.New("ordinary Ready input must not contain Runner planning metadata")
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	found, profile := false, ""
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, manualProfileHeading) {
			continue
		}
		if line != manualProfileHeading || found {
			return "", errors.New("ordinary work item requires exactly one canonical Runner implementation profile section")
		}
		found = true
		for i++; i < len(lines); i++ {
			line = strings.TrimSpace(lines[i])
			if strings.HasPrefix(line, "## ") {
				i--
				break
			}
			if line == "" {
				continue
			}
			if profile != "" || !config.ValidRoleID(line) {
				return "", errors.New("Runner implementation profile section must contain only one exact profile ID")
			}
			profile = line
		}
	}
	if found && profile == "" {
		return "", errors.New("Runner implementation profile section is empty")
	}
	return profile, nil
}

// WithManualImplementationProfile appends a selection without rewriting an
// existing contract. A conflicting or malformed selection needs operator repair.
func WithManualImplementationProfile(body, profile string) (string, error) {
	if !config.ValidRoleID(profile) {
		return "", errors.New("implementation profile must be an exact configured profile ID")
	}
	current, err := ManualImplementationProfile(body)
	if err != nil {
		return "", err
	}
	if current != "" {
		if current != profile {
			return "", errors.New("--profile conflicts with the card's Runner implementation profile section")
		}
		return body, nil
	}
	return strings.TrimSpace(body) + "\n\n" + manualProfileHeading + "\n\n" + profile, nil
}
