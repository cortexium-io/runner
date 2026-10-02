package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestImplementationPublishesRecordedAgentProfileOnCard(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_agents", Title: "Record model attribution", Body: "Acceptance criteria", URL: "https://github.com/owner/repo/issues/82", Repository: "owner/repo", Status: "Ready", Role: config.WorkRoleImplementer}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &successfulImplementationRunner{project: project}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	profile := cfg.Roles[config.WorkRoleImplementer]
	profile.Model, profile.Reasoning = stringPtr("gpt-6.1-sol"), "high"
	cfg.Roles[config.WorkRoleImplementer] = profile
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	store := metrics.NewStore(t.TempDir() + "/metrics/metrics.jsonl")
	service.SetMetricsObserver(store.Append)
	service.SetMetricsHistoryReader(store.Read)
	results, err := service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != "succeeded" || project.status != "Agent QA" {
		t.Fatalf("implementation did not complete: results=%#v error=%v status=%q", results, err, project.status)
	}
	if project.agents != "Implementation: gpt-6.1-sol (reasoning: high)" {
		history, historyErr := store.Read()
		t.Fatalf("card attribution = %q; current projection=%q; history=%#v error=%v; metrics error=%s", project.agents, service.cardAgentAttribution(item), history, historyErr, results[0].MetricsError)
	}
	if strings.Contains(project.result, "gpt-6.1-sol") {
		t.Fatalf("model attribution changed the result field: %q", project.result)
	}
	history, err := store.Read()
	if err != nil || len(history.Attempts) != 1 || history.Attempts[0].RoleContract != config.WorkRoleImplementer {
		t.Fatalf("selected work role not recorded: %#v error=%v", history, err)
	}
}

func TestCardAgentAttributionUsesRecordedSettingsAndParts(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	attempts := []metrics.Attempt{
		{Event: metrics.Event{AttemptID: "old", RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 4, ItemID: "card", Role: "small", RoleContract: config.WorkRoleImplementer, Model: "qwen3.8", Reasoning: "medium"}, Stages: []metrics.Stage{{Name: metrics.StageHarnessRun, StartedAt: now}}},
		{Event: metrics.Event{AttemptID: "new", RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 4, ItemID: "card", Role: "large", RoleContract: config.WorkRoleImplementer, Model: "gpt-6.1-sol", Reasoning: "high", Summary: "PRIVATE SUMMARY", Verification: []string{"PRIVATE COMMAND"}}, Stages: []metrics.Stage{{Name: metrics.StageHarnessRun, StartedAt: now.Add(time.Minute)}}},
		{Event: metrics.Event{AttemptID: "qa", RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 4, ItemID: "card", Role: "reviewer", Model: "gpt-6.1-sol", Reasoning: "xhigh"}, Stages: []metrics.Stage{{Name: metrics.StageReviewerAudit, StartedAt: now}, {Name: metrics.StageReviewerVerify, StartedAt: now.Add(time.Minute)}}},
		{Event: metrics.Event{AttemptID: "plan", RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 4, ItemID: "parent", Role: "planner", Model: "planning-model", Reasoning: "high"}, Stages: []metrics.Stage{{Name: metrics.StagePlannerOutline, StartedAt: now}, {Name: metrics.StagePlannerDetails, StartedAt: now.Add(time.Minute)}, {Name: metrics.StageReviewerAudit, StartedAt: now.Add(2 * time.Minute)}}},
		{Event: metrics.Event{AttemptID: "specialist", RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 4, ItemID: "card", Model: "gpt-6.1-sol", Reasoning: "high"}, Stages: []metrics.Stage{{Name: metrics.StageTestSpecialist, StartedAt: now}}},
	}
	service := &Engine{cfg: config.RuntimeConfig{RunnerID: "runner", GitHubProject: config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 4}}, Roles: map[string]config.RoleConfig{"large": {Model: stringPtr("WRONG CURRENT MODEL"), Reasoning: "low"}}, RoleContracts: map[string]string{"large": config.WorkRoleReviewer}}}
	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) { return metrics.ReadResult{Attempts: attempts}, nil })
	report := service.cardAgentAttribution(github.WorkItem{ID: "card", PlanningSourceID: "parent"})
	want := "Implementation: gpt-6.1-sol (reasoning: high); Planning: planning-model (reasoning: high); QA: gpt-6.1-sol (reasoning: xhigh); Test specialist: gpt-6.1-sol (reasoning: high)"
	if report != want {
		t.Fatalf("attribution = %q, want %q", report, want)
	}
}

func TestCardAgentAttributionScopesHistoryAndRequiresHarnessStage(t *testing.T) {
	base := metrics.Event{RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 4, ItemID: "card", Role: "implementer", Model: "private-other-model", Reasoning: "high"}
	cases := []struct {
		name  string
		event metrics.Event
		stage string
	}{
		{name: "other card", event: func() metrics.Event { e := base; e.ItemID = "other"; return e }(), stage: metrics.StageHarnessRun},
		{name: "other runner", event: func() metrics.Event { e := base; e.RunnerID = "other"; return e }(), stage: metrics.StageHarnessRun},
		{name: "other project", event: func() metrics.Event { e := base; e.ProjectNumber = 5; return e }(), stage: metrics.StageHarnessRun},
		{name: "other owner", event: func() metrics.Event { e := base; e.ProjectOwner = "other"; return e }(), stage: metrics.StageHarnessRun},
		{name: "preparation only", event: base, stage: metrics.StageWorkspacePrepare},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service := &Engine{cfg: config.RuntimeConfig{RunnerID: "runner", GitHubProject: config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 4}}}}
			service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) {
				return metrics.ReadResult{Attempts: []metrics.Attempt{{Event: test.event, Stages: []metrics.Stage{{Name: test.stage}}}}}, nil
			})
			if got := service.cardAgentAttribution(github.WorkItem{ID: "card"}); got != "" {
				t.Fatalf("unrelated or unexecuted model published: %q", got)
			}
		})
	}
}

func TestCardAgentAttributionMissingSettingsAndUnavailableHistory(t *testing.T) {
	service := &Engine{cfg: config.RuntimeConfig{RunnerID: "runner", GitHubProject: config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 4}}}}
	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) {
		return metrics.ReadResult{Attempts: []metrics.Attempt{{Event: metrics.Event{RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 4, ItemID: "card", Role: "implementer"}, Stages: []metrics.Stage{{Name: metrics.StageHarnessRun}}}}}, nil
	})
	if got := service.cardAgentAttribution(github.WorkItem{ID: "card"}); !strings.Contains(got, "Implementation: not recorded (reasoning: harness default)") {
		t.Fatalf("missing settings were inferred: %q", got)
	}
	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) { return metrics.ReadResult{}, errors.New("PRIVATE FILE PATH") })
	if got := service.cardAgentAttribution(github.WorkItem{ID: "card"}); got != "Agent attribution unavailable; inspect local metrics." {
		t.Fatalf("history error was not classified safely: %q", got)
	}
}
