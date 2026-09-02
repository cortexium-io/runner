package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type PullRequestDetails struct {
	Repository       string
	URL              string
	Number           int
	State            string
	HeadRepository   string
	HeadRefName      string
	HeadRefOID       string
	BaseRefName      string
	BaseRefOID       string
	MergeStateStatus string
	Feedback         string
	AutoMergeEnabled bool
}

const (
	MaxPullRequestFeedbackEntries = 100
	maxPullRequestFeedbackBytes   = 10_000
)

var ErrPublicationBaseChanged = workspace.ErrPublicationBaseChanged

type PublishedPullRequest struct {
	URL       string
	Number    int
	Branch    string
	CommitSHA string
}

type BranchRefreshResult struct {
	Updated       bool
	Conflicted    bool
	CommitSHA     string
	ConflictFiles []string
	Summary       string
}

type ActionRefresher interface {
	RefreshAction(context.Context, AuthorizedAction) (AuthorizedAction, error)
}

type PullRequestManager struct {
	run             subprocess.Runner
	timeout         time.Duration
	actionRefresher ActionRefresher
}

func NewPullRequestManager(run subprocess.Runner, actionRefresher ActionRefresher) PullRequestManager {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	return PullRequestManager{run: run, timeout: 2 * time.Minute, actionRefresher: actionRefresher}
}

func requireAuthorizedAction(action AuthorizedAction) (WorkItem, error) {
	item, err := action.authorizedItem()
	if err != nil {
		return WorkItem{}, fmt.Errorf("pull request operation requires validated Runner authority: %w", err)
	}
	return item, nil
}

func (m PullRequestManager) refreshAuthorizedAction(ctx context.Context, expected AuthorizedAction) (AuthorizedAction, error) {
	if m.actionRefresher == nil {
		return AuthorizedAction{}, errors.New("pull request mutation requires a Project authority refresher")
	}
	current, err := m.actionRefresher.RefreshAction(ctx, expected)
	if err != nil {
		return AuthorizedAction{}, fmt.Errorf("refresh Project authority before pull request mutation: %w", err)
	}
	if !sameAuthorizedAction(expected, current) {
		return AuthorizedAction{}, errors.New("Project action changed before pull request mutation; reload the item and try again")
	}
	return current, nil
}

func (m PullRequestManager) InspectAuthorized(ctx context.Context, action AuthorizedAction) (PullRequestDetails, error) {
	item, err := requireAuthorizedAction(action)
	if err != nil {
		return PullRequestDetails{}, err
	}
	return m.inspect(ctx, item.Repository, item.PullRequest)
}

func (m PullRequestManager) inspect(ctx context.Context, repository, selector string) (PullRequestDetails, error) {
	selector, err := validatedPullRequestSelector(repository, selector)
	if err != nil {
		return PullRequestDetails{}, err
	}
	result, err := subprocess.RunGitHub(ctx, m.run, []string{"pr", "view", selector, "--repo", repository, "--json", "url,number,state,headRepository,headRefName,headRefOid,baseRefName,baseRefOid,mergeStateStatus,autoMergeRequest,comments,reviews"}, "", 30*time.Second)
	if err != nil {
		return PullRequestDetails{}, fmt.Errorf("inspect pull request: %w", commandFailure(err, result))
	}
	var payload struct {
		URL            string `json:"url"`
		Number         int    `json:"number"`
		State          string `json:"state"`
		HeadRepository *struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"headRepository"`
		HeadRefName      string `json:"headRefName"`
		HeadRefOID       string `json:"headRefOid"`
		BaseRefName      string `json:"baseRefName"`
		BaseRefOID       string `json:"baseRefOid"`
		MergeStateStatus string `json:"mergeStateStatus"`
		AutoMergeRequest *struct {
			EnabledAt string `json:"enabledAt"`
		} `json:"autoMergeRequest"`
		Comments []struct {
			Body              string `json:"body"`
			AuthorAssociation string `json:"authorAssociation"`
			Author            struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"comments"`
		Reviews []struct {
			Body              string `json:"body"`
			State             string `json:"state"`
			AuthorAssociation string `json:"authorAssociation"`
			Author            struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return PullRequestDetails{}, fmt.Errorf("decode pull request: %w", err)
	}
	if strings.TrimSpace(payload.URL) == "" || payload.Number <= 0 {
		return PullRequestDetails{}, errors.New("GitHub CLI did not return a pull request URL and number")
	}
	if _, err := validatedPullRequestSelector(repository, payload.URL); err != nil {
		return PullRequestDetails{}, fmt.Errorf("GitHub CLI returned a pull request outside the approved repository: %w", err)
	}
	feedbackCount := 0
	feedbackActors := make([]string, 0, len(payload.Reviews)+len(payload.Comments))
	for _, review := range payload.Reviews {
		if body := strings.TrimSpace(review.Body); body != "" {
			feedbackCount++
			if feedbackCount > MaxPullRequestFeedbackEntries {
				return PullRequestDetails{}, fmt.Errorf("pull request feedback exceeds fixed limit of %d combined non-empty comments and reviews (next count %d)", MaxPullRequestFeedbackEntries, feedbackCount)
			}
			feedbackActors = append(feedbackActors, review.Author.Login)
		}
	}
	for _, comment := range payload.Comments {
		if body := strings.TrimSpace(comment.Body); body != "" {
			feedbackCount++
			if feedbackCount > MaxPullRequestFeedbackEntries {
				return PullRequestDetails{}, fmt.Errorf("pull request feedback exceeds fixed limit of %d combined non-empty comments and reviews (next count %d)", MaxPullRequestFeedbackEntries, feedbackCount)
			}
			feedbackActors = append(feedbackActors, comment.Author.Login)
		}
	}
	details := PullRequestDetails{
		Repository: strings.TrimSpace(repository), URL: strings.TrimSpace(payload.URL), Number: payload.Number, State: strings.ToUpper(strings.TrimSpace(payload.State)),
		HeadRefName: strings.TrimSpace(payload.HeadRefName), HeadRefOID: strings.TrimSpace(payload.HeadRefOID),
		BaseRefName: strings.TrimSpace(payload.BaseRefName), BaseRefOID: strings.TrimSpace(payload.BaseRefOID),
		MergeStateStatus: strings.ToUpper(strings.TrimSpace(payload.MergeStateStatus)), AutoMergeEnabled: payload.AutoMergeRequest != nil,
	}
	trustedFeedback := false
	if len(feedbackActors) > 0 {
		configuredActor, actorErr := m.configuredFeedbackActor(ctx)
		if actorErr != nil {
			return PullRequestDetails{}, actorErr
		}
		for _, actor := range feedbackActors {
			trustedFeedback = trustedFeedback || trustedPullRequestActor(actor, configuredActor)
		}
	}
	if trustedFeedback {
		feedback, err := pullRequestFeedbackProjectResult(repository, details.URL)
		if err != nil {
			return PullRequestDetails{}, err
		}
		details.Feedback = feedback
	}
	if payload.HeadRepository != nil {
		details.HeadRepository = strings.TrimSpace(payload.HeadRepository.NameWithOwner)
	}
	return details, nil
}

// RequestAutoMerge asks GitHub to merge the pull request once all repository
// requirements pass. It deliberately never uses an administrative bypass.
func (m PullRequestManager) requestAutoMerge(ctx context.Context, repository, selector, headCommit, mergeMethod string) error {
	selector, err := validatedPullRequestSelector(repository, selector)
	if err != nil {
		return err
	}
	headCommit = strings.TrimSpace(headCommit)
	if !validGitObjectID(headCommit) {
		return errors.New("automatic pull request merge requires the full reviewed head commit")
	}
	mergeMethod = config.EffectiveMergeMethod(mergeMethod)
	if !config.ValidMergeMethod(mergeMethod) {
		return fmt.Errorf("automatic pull request merge method %q is unsupported", mergeMethod)
	}
	result, err := subprocess.RunGitHub(ctx, m.run, []string{"pr", "merge", selector, "--repo", repository, "--auto", "--" + mergeMethod, "--match-head-commit", headCommit}, "", 30*time.Second)
	if err != nil {
		return fmt.Errorf("request automatic pull request merge: %w", commandFailure(err, result))
	}
	return nil
}

func (m PullRequestManager) RequestAutoMergeAuthorized(ctx context.Context, action AuthorizedAction, headCommit, baseBranch, baseRevision, mergeMethod string) error {
	item, err := requireAuthorizedAction(action)
	if err != nil {
		return err
	}
	headCommit = strings.TrimSpace(headCommit)
	if !strings.EqualFold(headCommit, strings.TrimSpace(item.QACommit)) {
		return errors.New("automatic merge head commit is not part of the authorized Project action")
	}
	action, err = m.refreshAuthorizedAction(ctx, action)
	if err != nil {
		return err
	}
	item, err = requireAuthorizedAction(action)
	if err != nil {
		return err
	}
	if !strings.EqualFold(headCommit, strings.TrimSpace(item.QACommit)) {
		return errors.New("automatic merge head commit is no longer the QA-reviewed commit")
	}
	details, err := m.inspect(ctx, item.Repository, item.PullRequest)
	if err != nil {
		return err
	}
	if err := ValidateTrackedPullRequest(details, item.Repository, item.Branch, headCommit, baseBranch, ""); err != nil {
		return fmt.Errorf("automatic merge pull request identity changed: %w", err)
	}
	if baseRevision = strings.TrimSpace(baseRevision); baseRevision != "" && !strings.EqualFold(strings.TrimSpace(details.BaseRefOID), baseRevision) {
		return fmt.Errorf("automatic merge pull request identity changed: pull request base commit %q does not match %q: %w", details.BaseRefOID, baseRevision, ErrPublicationBaseChanged)
	}
	return m.requestAutoMerge(ctx, item.Repository, item.PullRequest, headCommit, mergeMethod)
}

// CancelAutoMerge disarms a previously requested automatic merge before the
// pull request is changed or sent back through agent work.
func (m PullRequestManager) cancelAutoMerge(ctx context.Context, repository, selector string) error {
	selector, err := validatedPullRequestSelector(repository, selector)
	if err != nil {
		return err
	}
	result, err := subprocess.RunGitHub(ctx, m.run, []string{"pr", "merge", selector, "--repo", repository, "--disable-auto"}, "", 30*time.Second)
	if err != nil {
		return fmt.Errorf("cancel automatic pull request merge: %w", commandFailure(err, result))
	}
	return nil
}

func (m PullRequestManager) CancelAutoMergeAuthorized(ctx context.Context, action AuthorizedAction) error {
	if _, err := requireAuthorizedAction(action); err != nil {
		return err
	}
	action, err := m.refreshAuthorizedAction(ctx, action)
	if err != nil {
		return err
	}
	item, err := requireAuthorizedAction(action)
	if err != nil {
		return err
	}
	return m.cancelAutoMerge(ctx, item.Repository, item.PullRequest)
}

func validatedPullRequestSelector(repository, selector string) (string, error) {
	repository = strings.TrimSpace(repository)
	if !config.ValidRepositoryName(repository) {
		return "", errors.New("pull request repository must use owner/repository format")
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", errors.New("pull request selector is required")
	}
	if number, err := strconv.Atoi(selector); err == nil && number > 0 {
		return selector, nil
	}
	parsed, err := url.Parse(selector)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("pull request selector must be a positive number or canonical https://github.com/owner/repository/pull/number URL")
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" || !config.ValidRepositoryName(parts[0]+"/"+parts[1]) {
		return "", errors.New("pull request selector must be a positive number or canonical https://github.com/owner/repository/pull/number URL")
	}
	number, numberErr := strconv.Atoi(parts[3])
	if numberErr != nil || number <= 0 {
		return "", errors.New("pull request selector must be a positive number or canonical https://github.com/owner/repository/pull/number URL")
	}
	if !strings.EqualFold(parts[0]+"/"+parts[1], repository) {
		return "", fmt.Errorf("pull request selector must belong to approved repository %q", repository)
	}
	return selector, nil
}

func validGitObjectID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func (m PullRequestManager) publish(ctx context.Context, action AuthorizedAction, metadata workspace.Metadata, record workspace.PublicationRecord, baseBranch, remoteName, mergeMethod, qaReport string) (PublishedPullRequest, error) {
	item, err := requireAuthorizedAction(action)
	if err != nil {
		return PublishedPullRequest{}, err
	}
	baseBranch = strings.TrimSpace(baseBranch)
	remoteName = strings.TrimSpace(remoteName)
	mergeMethod = config.EffectiveMergeMethod(mergeMethod)
	if baseBranch == "" || remoteName == "" {
		return PublishedPullRequest{}, errors.New("publication requires an explicit base branch and Git remote")
	}
	if !config.ValidMergeMethod(mergeMethod) {
		return PublishedPullRequest{}, errors.New("publication requires merge, rebase, or squash merge method")
	}
	branch := strings.TrimPrefix(record.DestinationRef, "refs/heads/")
	if err := validatePublicationAuthority(action, record); err != nil {
		return PublishedPullRequest{}, err
	}
	pushPolicy := workspace.PublicationPushPolicy{MergeMethod: mergeMethod}
	if mergeMethod == config.MergeMethodRebase && strings.TrimSpace(item.PullRequest) != "" {
		if !validGitObjectID(item.QACommit) {
			return PublishedPullRequest{}, errors.New("rebase-mode pull request publication requires the exact previously accepted remote commit")
		}
		pushPolicy.ExpectedRemoteOID = strings.TrimSpace(item.QACommit)
	}
	provider := workspace.NewGitProvider(m.run)
	if err := provider.PublishAccepted(ctx, metadata, record, remoteName, baseBranch, pushPolicy, func() error {
		refreshed, refreshErr := m.refreshAuthorizedAction(ctx, action)
		if refreshErr != nil {
			return refreshErr
		}
		if authorityErr := validatePublicationAuthority(refreshed, record); authorityErr != nil {
			return authorityErr
		}
		if remoteErr := m.validateRemoteRepository(ctx, metadata.RepoRoot, remoteName, record.Repository); remoteErr != nil {
			return remoteErr
		}
		if remoteErr := m.validateRemoteRepository(ctx, metadata.WorktreePath, remoteName, record.Repository); remoteErr != nil {
			return remoteErr
		}
		action = refreshed
		item = refreshed.Item
		return nil
	}); err != nil {
		return PublishedPullRequest{}, err
	}
	action, err = m.refreshAuthorizedAction(ctx, action)
	if err != nil {
		return PublishedPullRequest{}, err
	}
	if err := validatePublicationAuthority(action, record); err != nil {
		return PublishedPullRequest{}, err
	}
	item = action.Item
	if strings.TrimSpace(item.PullRequest) != "" {
		details, err := m.inspect(ctx, item.Repository, item.PullRequest)
		if err != nil {
			return PublishedPullRequest{}, err
		}
		if err := validatePublishedPullRequest(details, item.Repository, branch, record.CommitOID, baseBranch, record.ApprovedBaseOID); err != nil {
			return PublishedPullRequest{}, err
		}
		return PublishedPullRequest{URL: details.URL, Number: details.Number, Branch: branch, CommitSHA: record.CommitOID}, nil
	}
	if existing, found, findErr := m.findOpen(ctx, item.Repository, branch, baseBranch); findErr != nil {
		return PublishedPullRequest{}, findErr
	} else if found {
		details, err := m.inspect(ctx, item.Repository, existing.URL)
		if err != nil {
			return PublishedPullRequest{}, err
		}
		if err := validatePublishedPullRequest(details, item.Repository, branch, record.CommitOID, baseBranch, record.ApprovedBaseOID); err != nil {
			return PublishedPullRequest{}, err
		}
		existing.Branch = branch
		existing.CommitSHA = record.CommitOID
		return existing, nil
	}
	action, err = m.refreshAuthorizedAction(ctx, action)
	if err != nil {
		return PublishedPullRequest{}, err
	}
	if err := validatePublicationAuthority(action, record); err != nil {
		return PublishedPullRequest{}, err
	}
	item = action.Item
	body, err := runnerPullRequestBody(item.URL)
	if err != nil {
		return PublishedPullRequest{}, err
	}
	result, err := subprocess.RunGitHub(ctx, m.run, []string{"pr", "create", "--repo", item.Repository, "--base", baseBranch, "--head", branch, "--title", strings.TrimSpace(item.Title), "--body", body}, metadata.WorktreePath, m.timeout)
	if err != nil {
		return PublishedPullRequest{}, fmt.Errorf("create pull request: %w", commandFailure(err, result))
	}
	url := firstNonEmptyLine(result.Stdout)
	if url == "" {
		return PublishedPullRequest{}, errors.New("GitHub CLI did not return the created pull request URL")
	}
	details, err := m.inspect(ctx, item.Repository, url)
	if err != nil {
		return PublishedPullRequest{}, err
	}
	if err := validatePublishedPullRequest(details, item.Repository, branch, record.CommitOID, baseBranch, record.ApprovedBaseOID); err != nil {
		return PublishedPullRequest{}, err
	}
	return PublishedPullRequest{URL: details.URL, Number: details.Number, Branch: branch, CommitSHA: record.CommitOID}, nil
}

func (m PullRequestManager) configuredFeedbackActor(ctx context.Context) (string, error) {
	result, err := subprocess.RunGitHub(ctx, m.run, []string{"api", "user", "--jq", ".login"}, "", 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("resolve configured GitHub feedback actor: %w", commandFailure(err, result))
	}
	actor := strings.TrimSpace(result.Stdout)
	if actor == "" {
		return "", errors.New("resolve configured GitHub feedback actor: GitHub CLI returned an empty login")
	}
	return actor, nil
}

func trustedPullRequestActor(actor, configuredActor string) bool {
	return strings.EqualFold(strings.TrimSpace(actor), strings.TrimSpace(configuredActor))
}

func (m PullRequestManager) PublishAuthorized(ctx context.Context, action AuthorizedAction, metadata workspace.Metadata, record workspace.PublicationRecord, baseBranch, remoteName, mergeMethod, qaReport string) (PublishedPullRequest, error) {
	return m.publish(ctx, action, metadata, record, baseBranch, remoteName, mergeMethod, qaReport)
}

func validatePublicationAuthority(action AuthorizedAction, record workspace.PublicationRecord) error {
	item, err := requireAuthorizedAction(action)
	if err != nil {
		return err
	}
	content, err := action.DelegatedContent()
	if err != nil {
		return fmt.Errorf("resolve authorized publication content: %w", err)
	}
	branch := strings.TrimPrefix(record.DestinationRef, "refs/heads/")
	if item.ID != record.ItemID || content.Digest != record.DelegatedContentDigest ||
		!strings.EqualFold(strings.TrimSpace(item.Repository), record.Repository) || strings.TrimSpace(item.Branch) != branch ||
		branch == record.DestinationRef || !config.ValidRepositoryName(record.Repository) {
		return errors.New("publication tuple is not part of the authorized Project action")
	}
	return nil
}

func validatePublishedPullRequest(details PullRequestDetails, repository, branch, headCommit, baseBranch, baseRevision string) error {
	if details.State != "OPEN" {
		return fmt.Errorf("pull request is %s", strings.ToLower(details.State))
	}
	if err := ValidateTrackedPullRequest(details, repository, branch, headCommit, baseBranch, baseRevision); err != nil {
		return fmt.Errorf("pull request does not match the accepted publication tuple: %w", err)
	}
	return nil
}

func ValidateTrackedPullRequest(details PullRequestDetails, repository, branch, headCommit, baseBranch, baseRevision string) error {
	repository = strings.TrimSpace(repository)
	if !strings.EqualFold(strings.TrimSpace(details.Repository), repository) {
		return fmt.Errorf("pull request repository %q does not match %q", details.Repository, repository)
	}
	if headRepository := strings.TrimSpace(details.HeadRepository); headRepository == "" || !strings.EqualFold(headRepository, repository) {
		return fmt.Errorf("pull request head repository %q does not match %q", details.HeadRepository, repository)
	}
	if branch = strings.TrimSpace(branch); branch != "" && details.HeadRefName != branch {
		return fmt.Errorf("pull request head branch %q does not match %q", details.HeadRefName, branch)
	}
	if headCommit = strings.TrimSpace(headCommit); headCommit != "" && !strings.EqualFold(strings.TrimSpace(details.HeadRefOID), headCommit) {
		return fmt.Errorf("pull request head commit %q does not match %q", details.HeadRefOID, headCommit)
	}
	if baseBranch = strings.TrimSpace(baseBranch); baseBranch != "" && details.BaseRefName != baseBranch {
		return fmt.Errorf("pull request base branch %q does not match %q", details.BaseRefName, baseBranch)
	}
	if baseRevision = strings.TrimSpace(baseRevision); baseRevision != "" && !strings.EqualFold(strings.TrimSpace(details.BaseRefOID), baseRevision) {
		return fmt.Errorf("pull request base commit %q does not match %q", details.BaseRefOID, baseRevision)
	}
	return nil
}

func (m PullRequestManager) findOpen(ctx context.Context, repository, branch, baseBranch string) (PublishedPullRequest, bool, error) {
	result, err := subprocess.RunGitHub(ctx, m.run, []string{
		"pr", "list", "--repo", repository, "--state", "open", "--head", branch, "--base", baseBranch,
		"--limit", "100", "--json", "url,number,headRefName,baseRefName",
	}, "", 30*time.Second)
	if err != nil {
		return PublishedPullRequest{}, false, fmt.Errorf("find existing pull request: %w", commandFailure(err, result))
	}
	var payload []struct {
		URL         string `json:"url"`
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return PublishedPullRequest{}, false, fmt.Errorf("decode existing pull requests: %w", err)
	}
	if len(payload) == 0 {
		return PublishedPullRequest{}, false, nil
	}
	if len(payload) != 1 {
		return PublishedPullRequest{}, false, fmt.Errorf("GitHub CLI returned %d open pull requests for the publication branch; expected at most one", len(payload))
	}
	match := payload[0]
	if match.Number <= 0 || match.HeadRefName != branch || match.BaseRefName != baseBranch {
		return PublishedPullRequest{}, false, errors.New("GitHub CLI returned a mismatched pull request for the publication branch")
	}
	url, err := validatedPullRequestSelector(repository, match.URL)
	if err != nil {
		return PublishedPullRequest{}, false, fmt.Errorf("GitHub CLI returned an invalid pull request URL: %w", err)
	}
	return PublishedPullRequest{URL: url, Number: match.Number}, true, nil
}

func (m PullRequestManager) refreshBranch(ctx context.Context, action AuthorizedAction, metadata workspace.Metadata, baseBranch, remoteName, mergeMethod string) (BranchRefreshResult, error) {
	return m.refreshBranchMode(ctx, action, metadata, baseBranch, remoteName, mergeMethod, true)
}

func (m PullRequestManager) refreshUnpublishedBranch(ctx context.Context, action AuthorizedAction, metadata workspace.Metadata, baseBranch, remoteName, mergeMethod string) (BranchRefreshResult, error) {
	return m.refreshBranchMode(ctx, action, metadata, baseBranch, remoteName, mergeMethod, false)
}

func (m PullRequestManager) refreshBranchMode(ctx context.Context, action AuthorizedAction, metadata workspace.Metadata, baseBranch, remoteName, mergeMethod string, published bool) (BranchRefreshResult, error) {
	item, err := requireAuthorizedAction(action)
	if err != nil {
		return BranchRefreshResult{}, err
	}
	branch := strings.TrimSpace(item.Branch)
	repository := strings.TrimSpace(item.Repository)
	branch = strings.TrimSpace(branch)
	baseBranch = strings.TrimSpace(baseBranch)
	remoteName = strings.TrimSpace(remoteName)
	mergeMethod = config.EffectiveMergeMethod(mergeMethod)
	if branch == "" || strings.TrimSpace(metadata.WorktreePath) == "" {
		return BranchRefreshResult{}, errors.New("branch refresh requires a worktree and branch")
	}
	if baseBranch == "" || remoteName == "" {
		return BranchRefreshResult{}, errors.New("branch refresh requires an explicit base branch and Git remote")
	}
	if !config.ValidMergeMethod(mergeMethod) {
		return BranchRefreshResult{}, errors.New("branch refresh requires merge, rebase, or squash merge method")
	}
	if branch != metadata.BranchName || !strings.EqualFold(repository, metadata.Identity.Repository) {
		return BranchRefreshResult{}, errors.New("branch refresh workspace does not match the authorized repository and branch")
	}
	if err := m.validateRemoteRepository(ctx, metadata.WorktreePath, remoteName, repository); err != nil {
		return BranchRefreshResult{}, err
	}
	provider := workspace.NewGitProvider(m.run)
	var refreshed workspace.BaseRefresh
	if published {
		refreshed, err = provider.RefreshBaseForMergeMethod(ctx, metadata, remoteName, baseBranch, mergeMethod)
	} else {
		refreshed, err = provider.RefreshLocalBaseForMergeMethod(ctx, metadata, remoteName, baseBranch, mergeMethod)
	}
	if err != nil {
		return BranchRefreshResult{}, err
	}
	return BranchRefreshResult{Updated: refreshed.Updated, Conflicted: refreshed.Conflicted, CommitSHA: refreshed.CommitOID, ConflictFiles: refreshed.ConflictFiles, Summary: refreshed.Summary}, nil
}

func (m PullRequestManager) RefreshBranchAuthorized(ctx context.Context, action AuthorizedAction, metadata workspace.Metadata, baseBranch, remoteName, mergeMethod string) (BranchRefreshResult, error) {
	return m.refreshBranch(ctx, action, metadata, baseBranch, remoteName, mergeMethod)
}

func (m PullRequestManager) RefreshUnpublishedBranchAuthorized(ctx context.Context, action AuthorizedAction, metadata workspace.Metadata, baseBranch, remoteName, mergeMethod string) (BranchRefreshResult, error) {
	return m.refreshUnpublishedBranch(ctx, action, metadata, baseBranch, remoteName, mergeMethod)
}

func (m PullRequestManager) validateRemoteRepository(ctx context.Context, workingDir, remoteName, expectedRepository string) error {
	remote, err := m.git(ctx, workingDir, "config", "--get", "remote."+remoteName+".url")
	if err != nil {
		return fmt.Errorf("inspect publication remote %q: %w", remoteName, err)
	}
	repository, err := RepositoryFromRemote(remote.Stdout)
	if err != nil {
		return fmt.Errorf("publication remote %q is not a supported GitHub repository: %w", remoteName, err)
	}
	if strings.TrimSpace(expectedRepository) == "" || !strings.EqualFold(repository, strings.TrimSpace(expectedRepository)) {
		return fmt.Errorf("publication repository %q does not match Git remote %q", expectedRepository, repository)
	}
	return nil
}

func (m PullRequestManager) git(ctx context.Context, dir string, args ...string) (subprocess.Result, error) {
	result, err := subprocess.RunGit(ctx, m.run, args, dir, m.timeout)
	if err != nil {
		return result, fmt.Errorf("git %s: %w", strings.Join(args, " "), commandFailure(err, result))
	}
	return result, nil
}
