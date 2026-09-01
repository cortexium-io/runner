package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type GitHubRepositoryInspection struct {
	Repository             string `json:"repository"`
	BaseBranch             string `json:"base_branch"`
	Status                 string `json:"status"`
	Detail                 string `json:"detail"`
	WriteAccess            bool   `json:"write_access"`
	AutoMergeRequested     bool   `json:"auto_merge_requested"`
	AutoMergeAllowed       bool   `json:"auto_merge_allowed"`
	MergeMethod            string `json:"merge_method"`
	MergeMethodAllowed     bool   `json:"merge_method_allowed"`
	BaseBranchProtected    bool   `json:"base_branch_protected"`
	ClassicProtection      bool   `json:"classic_protection"`
	ActiveRulesInspected   bool   `json:"active_rules_inspected"`
	ProtectionDetailsKnown bool   `json:"protection_details_known"`
	RequiresLinearHistory  bool   `json:"requires_linear_history"`
	Recommendation         string `json:"recommendation,omitempty"`
}

type repositoryMetadata struct {
	FullName         string `json:"full_name"`
	AllowAutoMerge   bool   `json:"allow_auto_merge"`
	AllowMergeCommit bool   `json:"allow_merge_commit"`
	AllowRebaseMerge bool   `json:"allow_rebase_merge"`
	AllowSquashMerge bool   `json:"allow_squash_merge"`
	Permissions      struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

type repositoryBranch struct {
	Name       string `json:"name"`
	Protected  bool   `json:"protected"`
	Protection *struct {
		Enabled bool `json:"enabled"`
	} `json:"protection"`
}

type repositoryRule struct {
	Type       string `json:"type"`
	Parameters struct {
		AllowedMergeMethods []string `json:"allowed_merge_methods"`
	} `json:"parameters"`
}

func (i *Inspector) inspectGitHubRepository(ctx context.Context) *GitHubRepositoryInspection {
	project := i.cfg.GitHubProject
	inspection := &GitHubRepositoryInspection{Status: CapabilityBlocked}
	if project == nil {
		inspection.Detail = "GitHub repository configuration is unavailable"
		return inspection
	}
	repository := strings.TrimSpace(project.IntakeRepository)
	baseBranch := strings.TrimSpace(project.BaseBranch)
	mergeMethod := config.EffectiveMergeMethod(project.MergeMethod)
	inspection.Repository = repository
	inspection.BaseBranch = baseBranch
	inspection.AutoMergeRequested = project.AutoMerge
	inspection.MergeMethod = mergeMethod
	if !config.ValidRepositoryName(repository) || baseBranch == "" {
		inspection.Detail = "GitHub repository and base branch must be configured before repository policy can be checked"
		return inspection
	}

	var metadata repositoryMetadata
	if err := i.runGitHubAPIJSON(ctx, "repos/"+repository, &metadata); err != nil {
		inspection.Detail = "GitHub repository settings are unavailable: " + err.Error()
		return inspection
	}
	inspection.WriteAccess = metadata.Permissions.Push
	inspection.AutoMergeAllowed = metadata.AllowAutoMerge
	inspection.MergeMethodAllowed = repositoryAllowsMergeMethod(metadata, mergeMethod)
	if !inspection.WriteAccess {
		inspection.Detail = "the GitHub CLI account does not have write access to the configured repository"
		inspection.Recommendation = "Grant the Runner GitHub account write access to " + repository + ", then run doctor again."
		return inspection
	}

	branchPath := "repos/" + repository + "/branches/" + url.PathEscape(baseBranch)
	var branch repositoryBranch
	if err := i.runGitHubAPIJSON(ctx, branchPath, &branch); err != nil {
		inspection.Detail = fmt.Sprintf("configured base branch %q is unavailable through the GitHub API: %v", baseBranch, err)
		return inspection
	}
	inspection.BaseBranchProtected = branch.Protected
	inspection.ClassicProtection = branch.Protection != nil && branch.Protection.Enabled

	rules := []repositoryRule{}
	if err := i.runGitHubAPIJSON(ctx, "repos/"+repository+"/rules/branches/"+url.PathEscape(baseBranch), &rules); err == nil {
		inspection.ActiveRulesInspected = true
		if ruleRequiresLinearHistory(rules) {
			inspection.RequiresLinearHistory = true
		}
		if allowed, constrained := rulesetAllowsMergeMethod(rules, mergeMethod); constrained && !allowed {
			inspection.MergeMethodAllowed = false
		}
	} else if project.AutoMerge {
		inspection.Detail = "active GitHub rules for the configured base branch could not be inspected: " + err.Error()
		inspection.Recommendation = "Restore GitHub API access to the repository's active branch rules, then run doctor again."
		return inspection
	}
	if inspection.ClassicProtection {
		var protection struct {
			RequiredLinearHistory *struct {
				Enabled bool `json:"enabled"`
			} `json:"required_linear_history"`
		}
		if err := i.runGitHubAPIJSON(ctx, branchPath+"/protection", &protection); err == nil {
			inspection.ProtectionDetailsKnown = true
			if protection.RequiredLinearHistory != nil && protection.RequiredLinearHistory.Enabled {
				inspection.RequiresLinearHistory = true
			}
		}
	}

	if project.AutoMerge && !metadata.AllowAutoMerge {
		inspection.Detail = "Runner automatic merge is enabled, but the repository does not allow auto-merge"
		inspection.Recommendation = "Enable Allow auto-merge in the repository Pull Requests settings, or set github_project.auto_merge to false."
		return inspection
	}
	if project.AutoMerge && !inspection.MergeMethodAllowed {
		inspection.Detail = fmt.Sprintf("Runner merge method %q is not allowed by the repository or an active ruleset", mergeMethod)
		inspection.Recommendation = fmt.Sprintf("Allow %s merges in the repository policy, or set github_project.merge_method to a permitted method.", mergeMethod)
		return inspection
	}
	if project.AutoMerge && inspection.RequiresLinearHistory && mergeMethod == config.MergeMethodMerge {
		inspection.Detail = "Runner merge method \"merge\" conflicts with the base branch requirement for linear history"
		inspection.Recommendation = "Disable Require linear history for the base branch, or explicitly use github_project.merge_method \"rebase\" or \"squash\"."
		return inspection
	}
	inspection.Status = CapabilityAvailable
	inspection.Detail = fmt.Sprintf("%s/%s allows Runner publication", repository, baseBranch)
	return inspection
}

func (i *Inspector) runGitHubAPIJSON(ctx context.Context, endpoint string, target any) error {
	result, err := subprocess.RunGitHub(ctx, i.run, []string{"api", endpoint}, "", 15*time.Second)
	if err != nil {
		return fmt.Errorf("%s: %w", endpoint, err)
	}
	if err := json.Unmarshal([]byte(result.Stdout), target); err != nil {
		return fmt.Errorf("decode %s response: %w", endpoint, err)
	}
	return nil
}

func repositoryAllowsMergeMethod(metadata repositoryMetadata, method string) bool {
	switch config.EffectiveMergeMethod(method) {
	case config.MergeMethodMerge:
		return metadata.AllowMergeCommit
	case config.MergeMethodRebase:
		return metadata.AllowRebaseMerge
	case config.MergeMethodSquash:
		return metadata.AllowSquashMerge
	default:
		return false
	}
}

func ruleRequiresLinearHistory(rules []repositoryRule) bool {
	for _, rule := range rules {
		if strings.EqualFold(strings.TrimSpace(rule.Type), "required_linear_history") {
			return true
		}
	}
	return false
}

func rulesetAllowsMergeMethod(rules []repositoryRule, method string) (bool, bool) {
	method = config.EffectiveMergeMethod(method)
	constrained := false
	for _, rule := range rules {
		if !strings.EqualFold(strings.TrimSpace(rule.Type), "pull_request") || len(rule.Parameters.AllowedMergeMethods) == 0 {
			continue
		}
		constrained = true
		allowedByRule := false
		for _, allowed := range rule.Parameters.AllowedMergeMethods {
			if strings.EqualFold(strings.TrimSpace(allowed), method) {
				allowedByRule = true
				break
			}
		}
		if !allowedByRule {
			return false, true
		}
	}
	return true, constrained
}
