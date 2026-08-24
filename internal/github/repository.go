package github

import (
	"errors"
	"net/url"
	"strings"
)

// RepositoryFromRemote returns an owner/repository identity for supported
// GitHub HTTPS and SSH remote URLs.
func RepositoryFromRemote(remote string) (string, error) {
	value := strings.TrimSpace(remote)
	if value == "" {
		return "", errors.New("remote URL is empty")
	}
	if strings.HasPrefix(value, "git@github.com:") {
		return normalizeGitHubRepository(strings.TrimPrefix(value, "git@github.com:"))
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return "", errors.New("unsupported GitHub remote URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.User != nil {
			return "", errors.New("GitHub HTTPS remote URL must not contain credentials")
		}
	case "ssh":
		if parsed.User == nil {
			return "", errors.New("GitHub SSH remote URL must use the git user")
		}
		_, hasPassword := parsed.User.Password()
		if parsed.User.Username() != "git" || hasPassword {
			return "", errors.New("GitHub SSH remote URL must use the git user without a password")
		}
	default:
		return "", errors.New("unsupported GitHub remote URL")
	}
	return normalizeGitHubRepository(parsed.Path)
}

func normalizeGitHubRepository(path string) (string, error) {
	path = strings.Trim(strings.TrimSpace(path), "/")
	path = strings.TrimSuffix(path, ".git")
	owner, repository, found := strings.Cut(path, "/")
	if !found || owner == "" || repository == "" || strings.Contains(repository, "/") || strings.ContainsAny(path, " \t\r\n\\") {
		return "", errors.New("unsupported GitHub remote URL")
	}
	return owner + "/" + repository, nil
}
