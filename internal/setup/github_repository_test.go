package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type repositoryInspectionRunner struct {
	metadata   string
	branch     string
	rules      string
	protection string
}

func (r repositoryInspectionRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	if command != "gh" || len(args) != 2 || args[0] != "api" {
		return subprocess.Result{}, errors.New("unexpected command")
	}
	endpoint := args[1]
	switch {
	case endpoint == "repos/owner/repo":
		return subprocess.Result{Stdout: r.metadata}, nil
	case endpoint == "repos/owner/repo/branches/develop":
		return subprocess.Result{Stdout: r.branch}, nil
	case endpoint == "repos/owner/repo/rules/branches/develop":
		return subprocess.Result{Stdout: r.rules}, nil
	case endpoint == "repos/owner/repo/branches/develop/protection" && r.protection != "":
		return subprocess.Result{Stdout: r.protection}, nil
	default:
		return subprocess.Result{Stderr: "not found", ExitCode: 1}, errors.New("exit status 1")
	}
}

func repositoryInspectionConfig() config.Config {
	return config.Config{GitHubProject: &config.GitHubProjectConfig{
		IntakeRepository: "owner/repo", BaseBranch: "develop", AutoMerge: true, MergeMethod: config.MergeMethodMerge,
	}}
}

func TestGitHubRepositoryInspectionAcceptsCompatibleAutomaticMerge(t *testing.T) {
	runner := repositoryInspectionRunner{
		metadata: `{"full_name":"owner/repo","allow_auto_merge":true,"allow_merge_commit":true,"permissions":{"push":true}}`,
		branch:   `{"name":"develop","protected":false}`,
		rules:    `[]`,
	}
	report := NewInspector(repositoryInspectionConfig(), runner).inspectGitHubRepository(t.Context())
	if report.Status != CapabilityAvailable || !report.WriteAccess || !report.AutoMergeAllowed || !report.MergeMethodAllowed {
		t.Fatalf("compatible repository was not ready: %#v", report)
	}
}

func TestGitHubRepositoryInspectionRejectsDisabledConfiguredMethod(t *testing.T) {
	runner := repositoryInspectionRunner{
		metadata: `{"full_name":"owner/repo","allow_auto_merge":true,"allow_merge_commit":false,"allow_rebase_merge":true,"permissions":{"push":true}}`,
		branch:   `{"name":"develop","protected":true}`,
		rules:    `[]`,
	}
	report := NewInspector(repositoryInspectionConfig(), runner).inspectGitHubRepository(t.Context())
	if report.Status != CapabilityBlocked || report.MergeMethodAllowed || !strings.Contains(report.Detail, "not allowed") || !strings.Contains(report.Recommendation, "merge_method") {
		t.Fatalf("disabled merge method was not actionable: %#v", report)
	}
}

func TestGitHubRepositoryInspectionFailsClosedWhenActiveRulesAreUnavailable(t *testing.T) {
	runner := repositoryInspectionRunner{
		metadata: `{"full_name":"owner/repo","allow_auto_merge":true,"allow_merge_commit":true,"permissions":{"push":true}}`,
		branch:   `{"name":"develop","protected":false}`,
	}
	report := NewInspector(repositoryInspectionConfig(), runner).inspectGitHubRepository(t.Context())
	if report.Status != CapabilityBlocked || report.ActiveRulesInspected || !strings.Contains(report.Detail, "active GitHub rules") {
		t.Fatalf("unavailable active rules were not blocked: %#v", report)
	}
}

func TestGitHubRepositoryInspectionRejectsRulesetLinearHistory(t *testing.T) {
	runner := repositoryInspectionRunner{
		metadata: `{"full_name":"owner/repo","allow_auto_merge":true,"allow_merge_commit":true,"permissions":{"push":true}}`,
		branch:   `{"name":"develop","protected":true}`,
		rules:    `[{"type":"required_linear_history"}]`,
	}
	report := NewInspector(repositoryInspectionConfig(), runner).inspectGitHubRepository(t.Context())
	if report.Status != CapabilityBlocked || !report.RequiresLinearHistory || !strings.Contains(report.Detail, "linear history") {
		t.Fatalf("linear-history conflict was not detected: %#v", report)
	}
}

func TestGitHubRepositoryInspectionRejectsClassicLinearHistoryWhenVisible(t *testing.T) {
	runner := repositoryInspectionRunner{
		metadata:   `{"full_name":"owner/repo","allow_auto_merge":true,"allow_merge_commit":true,"permissions":{"push":true}}`,
		branch:     `{"name":"develop","protected":true,"protection":{"enabled":true}}`,
		rules:      `[]`,
		protection: `{"required_linear_history":{"enabled":true}}`,
	}
	report := NewInspector(repositoryInspectionConfig(), runner).inspectGitHubRepository(t.Context())
	if report.Status != CapabilityBlocked || !report.ProtectionDetailsKnown || !report.RequiresLinearHistory {
		t.Fatalf("classic linear-history conflict was not detected: %#v", report)
	}
}

func TestGitHubRepositoryInspectionAllowsExplicitRebaseForLinearHistory(t *testing.T) {
	cfg := repositoryInspectionConfig()
	cfg.GitHubProject.MergeMethod = config.MergeMethodRebase
	runner := repositoryInspectionRunner{
		metadata: `{"full_name":"owner/repo","allow_auto_merge":true,"allow_rebase_merge":true,"permissions":{"push":true}}`,
		branch:   `{"name":"develop","protected":true}`,
		rules:    `[{"type":"required_linear_history"},{"type":"pull_request","parameters":{"allowed_merge_methods":["rebase"]}}]`,
	}
	report := NewInspector(cfg, runner).inspectGitHubRepository(t.Context())
	if report.Status != CapabilityAvailable || !report.RequiresLinearHistory || !report.MergeMethodAllowed {
		t.Fatalf("compatible rebase policy was rejected: %#v", report)
	}
}

func TestRulesetMergeMethodsUseMostRestrictiveLayer(t *testing.T) {
	rules := []repositoryRule{
		{Type: "pull_request", Parameters: struct {
			AllowedMergeMethods []string `json:"allowed_merge_methods"`
		}{AllowedMergeMethods: []string{"merge", "rebase"}}},
		{Type: "pull_request", Parameters: struct {
			AllowedMergeMethods []string `json:"allowed_merge_methods"`
		}{AllowedMergeMethods: []string{"rebase"}}},
	}
	if allowed, constrained := rulesetAllowsMergeMethod(rules, config.MergeMethodMerge); allowed || !constrained {
		t.Fatalf("layered rules unexpectedly allowed merge: allowed=%t constrained=%t", allowed, constrained)
	}
}
