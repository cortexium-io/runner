package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func (s *Engine) reconcilePullRequests(ctx context.Context, items []github.WorkItem) ([]RunResult, bool, error) {
	warnings := []RunResult{}
	changed := false
	manager := github.NewPullRequestManager(s.run, s.source)
	mergedEvent, hasMergedEvent := s.cfg.WorkflowEventFor(config.WorkflowEventPRMerged)
	closedEvent, hasClosedEvent := s.cfg.WorkflowEventFor(config.WorkflowEventPRClosed)
	outdatedEvent, hasOutdatedEvent := s.cfg.WorkflowEventFor(config.WorkflowEventPROutOfDate)
	publicationLane := s.cfg.PublicationLaneID()
	blockAutoMerge := func(action github.AuthorizedAction, laneID, summary string, cause error) error {
		if !hasOutdatedEvent {
			return cause
		}
		target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
		detail := summary + " The pull request remains open. Check the repository auto-merge setting and the GitHub CLI account's merge permission, then retry this card."
		if err := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), detail, s.retryPhase(laneID, target)); err != nil {
			return err
		}
		item := action.Item
		warnings = append(warnings, RunResult{Item: item, Outcome: execution.OutcomeBlocked, Summary: summary, Error: cause.Error()})
		changed = true
		return nil
	}
	cancelAutoMerge := func(action github.AuthorizedAction, details github.PullRequestDetails, laneID string) (bool, error) {
		if !details.AutoMergeEnabled {
			return false, nil
		}
		if err := manager.CancelAutoMergeAuthorized(ctx, action); err != nil {
			summary := "Automatic merge could not be disabled before Runner changed the pull request. Runner did not mutate the branch; disable auto-merge on the PR manually before retrying."
			return true, blockAutoMerge(action, laneID, summary, err)
		}
		return false, nil
	}
	prepareWorkspace := func(action github.AuthorizedAction, item github.WorkItem, delegatedContentDigest, repoRoot, laneID string, autoMergeEnabled bool) (workspace.Metadata, bool, error) {
		prepared, err := s.workspaceForItem(ctx, item, delegatedContentDigest, repoRoot)
		if err == nil {
			return prepared, false, nil
		}
		if !hasOutdatedEvent {
			return workspace.Metadata{}, false, err
		}
		if errors.Is(err, workspace.ErrIdentityMismatch) {
			if blocked, cancelErr := cancelAutoMerge(action, github.PullRequestDetails{AutoMergeEnabled: autoMergeEnabled}, laneID); cancelErr != nil || blocked {
				return workspace.Metadata{}, blocked, cancelErr
			}
			target := outdatedEvent.Transitions["updated"]
			detail := "Runner detected a workspace identity mismatch; implementation and QA must run again. Details are retained in local Runner output."
			if updateErr := s.transitionAfterBranchUpdate(ctx, action, s.cfg.LaneStatus(target), s.phaseForTargetLane(target), detail); updateErr != nil {
				return workspace.Metadata{}, false, updateErr
			}
			warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Implementation workspace identity changed.", Error: err.Error()})
			changed = true
			return workspace.Metadata{}, true, nil
		}
		target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
		if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner could not prepare the pull request worktree. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
			return workspace.Metadata{}, false, updateErr
		}
		warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request worktree preparation failed.", Error: err.Error()})
		changed = true
		return workspace.Metadata{}, true, nil
	}
	requestAutoMerge := func(action github.AuthorizedAction, laneID, headCommit string) (bool, error) {
		if !s.cfg.GitHubProject.AutoMerge {
			return false, nil
		}
		current, content, err := s.source.RefreshDelegatedContent(ctx, action)
		if err != nil {
			return false, err
		}
		item := current.Item
		repoRoot, err := s.repositoryDir(ctx, item.Repository)
		if err != nil {
			return false, err
		}
		prepared, blocked, err := prepareWorkspace(current, item, content.Digest, repoRoot, laneID, false)
		if err != nil || blocked {
			return blocked, err
		}
		if err := manager.RequestAutoMergeAuthorized(ctx, current, headCommit, s.baseBranch(), prepared.BaseRevision); err != nil {
			summary := "Automatic merge could not be enabled after Agent QA passed."
			return true, blockAutoMerge(current, laneID, summary, err)
		}
		changed = true
		return false, nil
	}
	for itemIndex := range items {
		item := items[itemIndex]
		if strings.TrimSpace(item.PullRequest) == "" {
			continue
		}
		laneBeforeAuthorization := s.cfg.LaneIDForStatus(item.Status)
		terminalBeforeAuthorization := terminalPullRequestLane(s.cfg, laneBeforeAuthorization, mergedEvent, hasMergedEvent, closedEvent, hasClosedEvent)
		action, authorizeErr := s.source.Authorize(ctx, github.WorkItem{ID: item.ID})
		if authorizeErr != nil {
			// A human transition to a terminal lane revokes the previous Runner
			// action assertion. Respect that terminal state without using the
			// unauthenticated PR or workspace fields for inspection or cleanup.
			if terminalBeforeAuthorization {
				continue
			}
			return warnings, changed, fmt.Errorf("authorize pull request action for item %s: %w", item.ID, authorizeErr)
		}
		item = action.Item
		delegatedContent, contentErr := action.DelegatedContent()
		if contentErr != nil {
			return warnings, changed, fmt.Errorf("resolve delegated content identity for item %s: %w", item.ID, contentErr)
		}
		items[itemIndex] = item
		laneID := s.cfg.LaneIDForStatus(item.Status)
		reworkRequested := s.reworkRequested(item, laneID)
		awaitingHuman := publicationLane != "" && laneID == publicationLane
		if terminalPullRequestLane(s.cfg, laneID, mergedEvent, hasMergedEvent, closedEvent, hasClosedEvent) {
			if _, cleanupErr := s.cleanupAuthorizedItemWorkspace(ctx, action); cleanupErr != nil {
				warnings = append(warnings, workspaceCleanupWarning(item, cleanupErr))
			}
			continue
		}
		details, err := manager.InspectAuthorized(ctx, action)
		if err != nil {
			if hasOutdatedEvent {
				target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
				if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner could not inspect the authorized pull request. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
					return warnings, changed, updateErr
				}
				warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request inspection failed.", Error: err.Error()})
				changed = true
				continue
			}
			return warnings, changed, err
		}
		handled, terminalChanged, cleanupWarning, terminalErr := s.reconcileTerminalPullRequest(ctx, action, details, mergedEvent, hasMergedEvent, closedEvent, hasClosedEvent)
		if terminalErr != nil {
			return warnings, changed, terminalErr
		}
		changed = changed || terminalChanged
		if cleanupWarning != nil {
			warnings = append(warnings, *cleanupWarning)
		}
		if handled {
			continue
		}
		if strings.TrimSpace(item.Branch) != "" && details.HeadRefName != strings.TrimSpace(item.Branch) {
			if reworkRequested || awaitingHuman {
				if blocked, cancelErr := cancelAutoMerge(action, details, laneID); cancelErr != nil {
					return warnings, changed, cancelErr
				} else if blocked {
					continue
				}
			}
			detail := fmt.Sprintf("Pull request head branch %q does not match Runner branch %q.", details.HeadRefName, strings.TrimSpace(item.Branch))
			if hasOutdatedEvent {
				target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
				if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner detected a pull request head-branch identity mismatch. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
					return warnings, changed, updateErr
				}
				warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request head-branch identity mismatch.", Error: detail})
				changed = true
				continue
			}
			return warnings, changed, errors.New(detail)
		}
		if details.BaseRefName != "" && details.BaseRefName != s.baseBranch() {
			if reworkRequested || awaitingHuman {
				if blocked, cancelErr := cancelAutoMerge(action, details, laneID); cancelErr != nil {
					return warnings, changed, cancelErr
				} else if blocked {
					continue
				}
			}
			detail := fmt.Sprintf("Pull request base branch %q does not match configured base branch %q.", details.BaseRefName, s.baseBranch())
			if hasOutdatedEvent {
				target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
				if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner detected a pull request base-branch identity mismatch. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
					return warnings, changed, updateErr
				}
				warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request base-branch identity mismatch.", Error: detail})
				changed = true
				continue
			}
			return warnings, changed, errors.New(detail)
		}
		if details.State != "OPEN" {
			continue
		}
		if !reworkRequested && !awaitingHuman {
			continue
		}
		if strings.TrimSpace(item.Branch) == "" {
			item.Branch = details.HeadRefName
		}
		if err := github.ValidateTrackedPullRequest(details, item.Repository, item.Branch, "", s.baseBranch(), ""); err != nil {
			if reworkRequested || awaitingHuman {
				if blocked, cancelErr := cancelAutoMerge(action, details, laneID); cancelErr != nil {
					return warnings, changed, cancelErr
				} else if blocked {
					continue
				}
			}
			detail := "Pull request identity no longer matches the authorized item: " + err.Error()
			if hasOutdatedEvent {
				target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
				if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), detail, s.retryPhase(laneID, target)); updateErr != nil {
					return warnings, changed, updateErr
				}
				changed = true
				continue
			}
			return warnings, changed, errors.New(detail)
		}
		headMatchesQA := strings.TrimSpace(item.QACommit) != "" && strings.EqualFold(strings.TrimSpace(item.QACommit), strings.TrimSpace(details.HeadRefOID))
		if reworkRequested || awaitingHuman && (strings.TrimSpace(details.HeadRefOID) == "" || !headMatchesQA) {
			if blocked, err := cancelAutoMerge(action, details, laneID); err != nil {
				return warnings, changed, err
			} else if blocked {
				continue
			}
		}
		if awaitingHuman && (strings.TrimSpace(details.HeadRefOID) == "" || !headMatchesQA) {
			if !hasOutdatedEvent {
				return warnings, changed, fmt.Errorf("pull request %s head no longer matches the QA snapshot and workflow has no %s event", details.URL, config.WorkflowEventPROutOfDate)
			}
			target := outdatedEvent.Transitions["updated"]
			detail := "Pull request head changed after agent QA; implementation and QA must run again. Exact commit details are retained in local Runner output."
			if err := s.transitionAfterBranchUpdate(ctx, action, s.cfg.LaneStatus(target), s.phaseForTargetLane(target), detail); err != nil {
				return warnings, changed, err
			}
			warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request head changed after Agent QA.", Error: fmt.Sprintf("approved %s, current %s", defaultString(strings.TrimSpace(item.QACommit), "missing"), defaultString(strings.TrimSpace(details.HeadRefOID), "missing"))})
			changed = true
			continue
		}
		repoRoot, err := s.repositoryDir(ctx, item.Repository)
		if err != nil {
			if !hasOutdatedEvent {
				return warnings, changed, err
			}
			target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
			if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner could not open the authorized pull request repository. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
				return warnings, changed, updateErr
			}
			warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request repository is unavailable.", Error: err.Error()})
			changed = true
			continue
		}
		needsRefresh, alreadyIntegrated, err := s.branchNeedsBaseRefresh(ctx, repoRoot, item.Branch, defaultString(details.BaseRefName, s.baseBranch()))
		if err != nil {
			if !hasOutdatedEvent {
				return warnings, changed, err
			}
			target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
			if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner could not compare the pull request branch with its base. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
				return warnings, changed, updateErr
			}
			warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request branch inspection failed.", Error: err.Error()})
			changed = true
			continue
		}
		if alreadyIntegrated {
			continue
		}
		if !needsRefresh {
			if reworkRequested {
				feedback := ""
				if strings.TrimSpace(details.Feedback) != "" {
					feedback = details.Feedback
				}
				if err := s.resetRejections(ctx, action, feedback, laneID); err != nil {
					return warnings, changed, err
				}
				changed = true
				items[itemIndex].QAFailures = 0
				items[itemIndex].Phase = laneID
				if strings.TrimSpace(feedback) != "" {
					items[itemIndex].Result = feedback
				}
			} else if awaitingHuman {
				if _, blocked, prepareErr := prepareWorkspace(action, item, delegatedContent.Digest, repoRoot, laneID, details.AutoMergeEnabled); prepareErr != nil {
					return warnings, changed, prepareErr
				} else if blocked {
					continue
				}
				if s.cfg.GitHubProject.AutoMerge && !details.AutoMergeEnabled {
					if blocked, err := requestAutoMerge(action, laneID, details.HeadRefOID); err != nil {
						return warnings, changed, err
					} else if blocked {
						continue
					}
				}
				if _, err := s.cleanupAuthorizedItemWorkspace(ctx, action); err != nil {
					warnings = append(warnings, workspaceCleanupWarning(item, err))
				}
			}
			continue
		}
		// The PR can be merged after the initial API inspection but before the
		// base ref is fetched above. Re-check before mutating its head so the
		// merge commit itself is never mistaken for an unrelated base update.
		details, err = manager.InspectAuthorized(ctx, action)
		if err != nil {
			if !hasOutdatedEvent {
				return warnings, changed, err
			}
			target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
			if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner could not reinspect the authorized pull request before base refresh. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
				return warnings, changed, updateErr
			}
			warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request reinspection before base refresh failed.", Error: err.Error()})
			changed = true
			continue
		}
		handled, terminalChanged, cleanupWarning, terminalErr = s.reconcileTerminalPullRequest(ctx, action, details, mergedEvent, hasMergedEvent, closedEvent, hasClosedEvent)
		if terminalErr != nil {
			return warnings, changed, terminalErr
		}
		changed = changed || terminalChanged
		if cleanupWarning != nil {
			warnings = append(warnings, *cleanupWarning)
		}
		if handled || details.State != "OPEN" {
			continue
		}
		preparedWorkspace, blocked, err := prepareWorkspace(action, item, delegatedContent.Digest, repoRoot, laneID, details.AutoMergeEnabled)
		if err != nil {
			return warnings, changed, err
		}
		if blocked {
			continue
		}
		if awaitingHuman || reworkRequested {
			if blocked, err := cancelAutoMerge(action, details, laneID); err != nil {
				return warnings, changed, err
			} else if blocked {
				continue
			}
		}
		refresh, err := manager.RefreshBranchAuthorized(ctx, action, preparedWorkspace, defaultString(details.BaseRefName, s.baseBranch()), s.remoteName())
		if err != nil {
			if !hasOutdatedEvent {
				return warnings, changed, err
			}
			target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
			if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner could not refresh the pull request branch. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
				return warnings, changed, updateErr
			}
			warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request branch refresh failed.", Error: err.Error()})
			changed = true
			continue
		}
		// A maintainer can merge while the local refresh is in flight. A final
		// state check makes terminal PR state win over the refresh transition.
		details, err = manager.InspectAuthorized(ctx, action)
		if err != nil {
			if !hasOutdatedEvent {
				return warnings, changed, err
			}
			target := outdatedEvent.Transitions[config.WorkflowOutcomeError]
			if updateErr := s.transitionProjectItem(ctx, action, s.cfg.LaneStatus(target), "Runner could not reinspect the authorized pull request after base refresh. Details are retained in local Runner output.", s.retryPhase(laneID, target)); updateErr != nil {
				return warnings, changed, updateErr
			}
			warnings = append(warnings, RunResult{Item: item, Outcome: "warning", Summary: "Pull request reinspection after base refresh failed.", Error: err.Error()})
			changed = true
			continue
		}
		handled, terminalChanged, cleanupWarning, terminalErr = s.reconcileTerminalPullRequest(ctx, action, details, mergedEvent, hasMergedEvent, closedEvent, hasClosedEvent)
		if terminalErr != nil {
			return warnings, changed, terminalErr
		}
		changed = changed || terminalChanged
		if cleanupWarning != nil {
			warnings = append(warnings, *cleanupWarning)
		}
		if handled || details.State != "OPEN" {
			continue
		}
		if refresh.Updated || refresh.Conflicted {
			if !hasOutdatedEvent {
				continue
			}
			detail := "Runner refreshed the pull request branch locally; implementation and QA must run again before publication."
			if refresh.Conflicted {
				detail = "Runner found conflicts while refreshing the pull request branch locally. The retained worktree requires resolution before implementation and QA continue."
			}
			if strings.TrimSpace(details.Feedback) != "" {
				detail += "\n\nHuman PR feedback:\n" + strings.TrimSpace(details.Feedback)
			}
			outcome := "updated"
			if refresh.Conflicted {
				outcome = "conflict"
			}
			target := outdatedEvent.Transitions[outcome]
			if err := s.transitionAfterBranchUpdate(ctx, action, s.cfg.LaneStatus(target), s.phaseForTargetLane(target), detail); err != nil {
				return warnings, changed, err
			}
			changed = true
			continue
		}
		if reworkRequested {
			feedback := ""
			if strings.TrimSpace(details.Feedback) != "" {
				feedback = details.Feedback
			}
			if err := s.resetRejections(ctx, action, feedback, laneID); err != nil {
				return warnings, changed, err
			}
			changed = true
			items[itemIndex].QAFailures = 0
			items[itemIndex].Phase = laneID
			if strings.TrimSpace(feedback) != "" {
				items[itemIndex].Result = feedback
			}
		} else if awaitingHuman {
			if blocked, err := requestAutoMerge(action, laneID, details.HeadRefOID); err != nil {
				return warnings, changed, err
			} else if blocked {
				continue
			}
			if _, err := s.cleanupAuthorizedItemWorkspace(ctx, action); err != nil {
				warnings = append(warnings, workspaceCleanupWarning(item, err))
			}
		}
	}
	return warnings, changed, nil
}

func (s *Engine) reconcileTerminalPullRequest(
	ctx context.Context,
	action github.AuthorizedAction,
	details github.PullRequestDetails,
	mergedEvent config.WorkflowEvent,
	hasMergedEvent bool,
	closedEvent config.WorkflowEvent,
	hasClosedEvent bool,
) (handled bool, changed bool, warning *RunResult, err error) {
	item := action.Item
	if details.State != "MERGED" && details.State != "CLOSED" {
		return false, false, nil, nil
	}
	if validationErr := github.ValidateTrackedPullRequest(details, item.Repository, item.Branch, item.QACommit, s.baseBranch(), ""); validationErr != nil {
		value := RunResult{
			Item: item, Outcome: "warning",
			Summary: "Terminal pull request no longer matches the exact reviewed item; Runner preserved the workspace for diagnosis.",
			Error:   validationErr.Error(),
		}
		return true, false, &value, nil
	}
	verb := "closed without merge"
	event := closedEvent
	hasEvent := hasClosedEvent
	if details.State == "MERGED" {
		verb = "merged"
		event = mergedEvent
		hasEvent = hasMergedEvent
	}
	if !hasEvent {
		return true, false, nil, nil
	}
	targetStatus := s.cfg.LaneStatus(event.To)
	if !strings.EqualFold(strings.TrimSpace(item.Status), strings.TrimSpace(targetStatus)) {
		if err := s.transitionProjectItem(ctx, action, targetStatus, fmt.Sprintf("Pull request %s was %s.", details.URL, verb), ""); err != nil {
			return true, false, nil, err
		}
		changed = true
		action, err = s.source.Authorize(ctx, github.WorkItem{ID: item.ID})
		if err != nil {
			return true, changed, nil, fmt.Errorf("refresh authority after terminal pull request transition: %w", err)
		}
		item = action.Item
	}
	if _, cleanupErr := s.cleanupAuthorizedItemWorkspace(ctx, action); cleanupErr != nil {
		value := workspaceCleanupWarning(item, cleanupErr)
		warning = &value
	}
	return true, changed, warning, nil
}

func workspaceCleanupWarning(item github.WorkItem, err error) RunResult {
	return RunResult{Item: item, Outcome: "warning", Summary: "Task workspace cleanup is pending and will be retried without stalling other work.", Error: err.Error()}
}

func terminalPullRequestLane(cfg config.RuntimeConfig, laneID string, merged config.WorkflowEvent, hasMerged bool, closed config.WorkflowEvent, hasClosed bool) bool {
	lane, ok := cfg.Lane(laneID)
	if !ok || strings.TrimSpace(lane.Role) != "" {
		return false
	}
	for _, terminal := range []struct {
		event config.WorkflowEvent
		ok    bool
	}{{merged, hasMerged}, {closed, hasClosed}} {
		if terminal.ok && laneID == terminal.event.To {
			return true
		}
	}
	return false
}

func (s *Engine) baseBranch() string {
	return strings.TrimSpace(s.cfg.GitHubProject.BaseBranch)
}

func (s *Engine) remoteName() string {
	return strings.TrimSpace(s.cfg.GitHubProject.RemoteName)
}

func (s *Engine) branchNeedsBaseRefresh(ctx context.Context, repoRoot, branch, baseBranch string) (needsRefresh bool, alreadyIntegrated bool, err error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false, false, errors.New("pull request branch is required")
	}
	remote := s.remoteName()
	baseBranch = defaultString(baseBranch, s.baseBranch())
	fetched, err := s.git(ctx, []string{"fetch", remote, remoteRefspec(remote, baseBranch), remoteRefspec(remote, branch)}, repoRoot, 2*time.Minute)
	if err != nil {
		return false, false, fmt.Errorf("fetch pull request branches: %w", commandFailure(err, fetched))
	}
	ancestor, err := s.git(ctx, []string{"merge-base", "--is-ancestor", remote + "/" + baseBranch, remote + "/" + branch}, repoRoot, 30*time.Second)
	if err == nil && ancestor.ExitCode == 0 {
		return false, false, nil
	}
	if ancestor.ExitCode == 1 {
		integrated, integratedErr := s.git(ctx, []string{"merge-base", "--is-ancestor", remote + "/" + branch, remote + "/" + baseBranch}, repoRoot, 30*time.Second)
		if integratedErr == nil && integrated.ExitCode == 0 {
			// The base already contains the complete task branch. This commonly
			// happens when the PR was merged while GitHub's API still reports the
			// earlier OPEN snapshot; refreshing would incorrectly requeue it.
			return false, true, nil
		}
		if integrated.ExitCode == 1 {
			return true, false, nil
		}
		if integratedErr == nil {
			integratedErr = fmt.Errorf("git merge-base exited with status %d", integrated.ExitCode)
		}
		return false, false, fmt.Errorf("check whether pull request branch is already integrated: %w", commandFailure(integratedErr, integrated))
	}
	if err == nil {
		err = fmt.Errorf("git merge-base exited with status %d", ancestor.ExitCode)
	}
	return false, false, fmt.Errorf("compare pull request branch with base: %w", commandFailure(err, ancestor))
}

func (s *Engine) fetchBase(ctx context.Context, repoRoot string) error {
	result, err := s.git(ctx, []string{"fetch", s.remoteName(), remoteRefspec(s.remoteName(), s.baseBranch())}, repoRoot, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("fetch %s/%s: %w", s.remoteName(), s.baseBranch(), commandFailure(err, result))
	}
	return nil
}

func (s *Engine) workspaceForItem(ctx context.Context, item github.WorkItem, delegatedContentDigest, repoRoot string) (workspace.Metadata, error) {
	return s.workspaceForItemMode(ctx, item, delegatedContentDigest, repoRoot, false)
}

func (s *Engine) implementationWorkspaceForItem(ctx context.Context, item github.WorkItem, delegatedContentDigest, repoRoot string) (workspace.Metadata, error) {
	return s.workspaceForItemMode(ctx, item, delegatedContentDigest, repoRoot, true)
}

func (s *Engine) workspaceForItemMode(ctx context.Context, item github.WorkItem, delegatedContentDigest, repoRoot string, quarantineMismatch bool) (workspace.Metadata, error) {
	if strings.TrimSpace(item.Branch) != "" {
		validated, validateErr := s.git(ctx, []string{"check-ref-format", "--branch", item.Branch}, repoRoot, 10*time.Second)
		if validateErr != nil {
			return workspace.Metadata{}, fmt.Errorf("invalid persisted implementation branch %q: %w", item.Branch, commandFailure(validateErr, validated))
		}
	}
	prepared, err := s.prepareWorkspaceForItem(ctx, item, delegatedContentDigest, repoRoot, quarantineMismatch)
	if err != nil {
		return workspace.Metadata{}, err
	}
	if strings.TrimSpace(item.Branch) != "" && prepared.BranchName != strings.TrimSpace(item.Branch) {
		return workspace.Metadata{}, fmt.Errorf("implementation branch mismatch: Project has %q, worktree has %q", item.Branch, prepared.BranchName)
	}
	if strings.TrimSpace(item.Branch) != "" {
		if err := s.syncWorkspaceBranch(ctx, prepared.WorktreePath, item.Branch, strings.TrimSpace(item.PullRequest) != ""); err != nil {
			return workspace.Metadata{}, err
		}
	}
	return prepared, nil
}

func (s *Engine) validateWorkspaceForItem(ctx context.Context, item github.WorkItem, delegatedContentDigest, repoRoot string) (workspace.Metadata, error) {
	return s.prepareWorkspaceForItem(ctx, item, delegatedContentDigest, repoRoot, false)
}

func (s *Engine) prepareWorkspaceForItem(ctx context.Context, item github.WorkItem, delegatedContentDigest, repoRoot string, quarantineMismatch bool) (workspace.Metadata, error) {
	repository := strings.TrimSpace(item.Repository)
	if repository == "" {
		repository = strings.TrimSpace(s.cfg.GitHubProject.IntakeRepository)
	}
	return workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).Prepare(ctx, workspace.Request{
		WorkingDir: repoRoot, WorktreeRoot: s.implementationWorkspaceRoot(), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: strings.TrimSpace(item.ID), DelegatedContentDigest: strings.TrimSpace(delegatedContentDigest), Repository: repository,
		BranchPrefix: "runner", BranchName: strings.TrimSpace(item.Branch), BaseRef: s.remoteName() + "/" + s.baseBranch(),
		QuarantineMismatch: quarantineMismatch,
	})
}

func (s *Engine) syncWorkspaceBranch(ctx context.Context, worktreePath, branch string, remoteRequired bool) error {
	remote := s.remoteName()
	remoteRef := remote + "/" + strings.TrimSpace(branch)
	fetched, fetchErr := s.git(ctx, []string{"fetch", remote, remoteRefspec(remote, branch)}, worktreePath, 2*time.Minute)
	if fetchErr != nil {
		if remoteRequired {
			return fmt.Errorf("fetch current pull request head: %w", commandFailure(fetchErr, fetched))
		}
		return nil
	}
	if contained, err := s.git(ctx, []string{"merge-base", "--is-ancestor", remoteRef, "HEAD"}, worktreePath, 30*time.Second); err == nil && contained.ExitCode == 0 {
		return nil
	}
	localBehind, behindErr := s.git(ctx, []string{"merge-base", "--is-ancestor", "HEAD", remoteRef}, worktreePath, 30*time.Second)
	if behindErr != nil || localBehind.ExitCode != 0 {
		return errors.New("local task branch and remote pull request head have diverged; refusing to review or overwrite either history")
	}
	status, statusErr := s.git(ctx, []string{"status", "--porcelain", "--untracked-files=all"}, worktreePath, 30*time.Second)
	if statusErr != nil {
		return fmt.Errorf("inspect task worktree before remote synchronization: %w", commandFailure(statusErr, status))
	}
	if strings.TrimSpace(status.Stdout) != "" {
		return errors.New("remote pull request head advanced while the local task workspace has uncommitted changes")
	}
	updated, updateErr := s.git(ctx, []string{"merge", "--ff-only", remoteRef}, worktreePath, 30*time.Second)
	if updateErr != nil {
		return fmt.Errorf("fast-forward task workspace to current pull request head: %w", commandFailure(updateErr, updated))
	}
	return nil
}

func remoteRefspec(remote, branch string) string {
	return "+refs/heads/" + strings.TrimSpace(branch) + ":refs/remotes/" + strings.TrimSpace(remote) + "/" + strings.TrimSpace(branch)
}

func (s *Engine) implementationWorkspaceRoot() string {
	implementerRole := s.cfg.RoleIDForContract(config.WorkRoleImplementer)
	if cfg, ok := s.cfg.Harness(s.roleHarness(implementerRole)); ok {
		return cfg.WorkspaceWriteRoot
	}
	return ""
}

func (s *Engine) laneForItem(item github.WorkItem) (string, config.WorkflowLane) {
	id := s.cfg.LaneIDForStatus(item.Status)
	if id == s.cfg.Workflow.ActiveLane && strings.TrimSpace(item.Phase) != "" {
		id = strings.TrimSpace(item.Phase)
	}
	lane, _ := s.cfg.Lane(id)
	return id, lane
}

func (s *Engine) phaseForTargetLane(laneID string) string {
	lane, ok := s.cfg.Lane(laneID)
	if ok && strings.TrimSpace(lane.Role) != "" {
		return laneID
	}
	return ""
}

func (s *Engine) retryPhase(sourceLaneID, targetLaneID string) string {
	if phase := s.phaseForTargetLane(targetLaneID); phase != "" {
		return phase
	}
	if strings.EqualFold(s.cfg.LaneStatus(targetLaneID), s.cfg.GitHubProject.BlockedStatus) {
		sourceLaneID = strings.TrimSpace(sourceLaneID)
		if sourceLaneID == s.cfg.PublicationLaneID() {
			for _, laneID := range s.cfg.AgentLaneIDs() {
				lane, _ := s.cfg.Lane(laneID)
				if s.cfg.RoleContract(lane.Role) == config.WorkRoleReviewer {
					return laneID
				}
			}
		}
		return sourceLaneID
	}
	return ""
}

func (s *Engine) reworkRequested(item github.WorkItem, laneID string) bool {
	lane, ok := s.cfg.Lane(laneID)
	return ok && s.cfg.RoleContract(lane.Role) == config.WorkRoleImplementer && strings.TrimSpace(item.PullRequest) != "" && strings.TrimSpace(item.Phase) == ""
}
