package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type intakeBudgetRunner struct {
	issues    string
	mutations int
	added     map[string]string
}

func intakeArgumentValue(args []string, name string) string {
	for index := range args {
		if args[index] == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func (r *intakeBudgetRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	if command != "gh" {
		return subprocess.Result{}, fmt.Errorf("unexpected command %q", command)
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "issue list "):
		return subprocess.Result{Stdout: r.issues}, nil
	case strings.HasPrefix(joined, "project item-add "):
		r.mutations++
		url := intakeArgumentValue(args, "--url")
		id := "PVTI_" + strconv.Itoa(len(r.added)+1)
		r.added[url] = id
		return subprocess.Result{Stdout: `{"id":"` + id + `"}`}, nil
	case strings.HasPrefix(joined, "project item-edit "):
		r.mutations++
		return subprocess.Result{}, nil
	default:
		return subprocess.Result{}, fmt.Errorf("unexpected GitHub command %q", joined)
	}
}

func TestAssessmentIntakeMutationBudgetStopsAndResumesWithoutDuplicateAdds(t *testing.T) {
	issues := make([]map[string]string, 51)
	for index := range issues {
		issues[index] = map[string]string{"url": fmt.Sprintf("https://github.com/owner/repo/issues/%d", index+1)}
	}
	encoded, err := json.Marshal(issues)
	if err != nil {
		t.Fatal(err)
	}
	runner := &intakeBudgetRunner{issues: string(encoded), added: map[string]string{}}
	project := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo", IntakeLabel: "needs-assessment"},
		AssessmentStatus:    "Needs assessment",
	}, runner)
	project.schema = githubProjectSchema{ProjectID: "PVT_test", Fields: map[string]githubProjectField{
		normalizeProjectKey("Status"): {ID: "F_status", Name: "Status", Type: "ProjectV2SingleSelectField", Options: map[string]githubProjectOption{normalizeProjectKey("Needs assessment"): {ID: "O_assessment"}}},
	}}

	first, err := project.SyncAssessmentIssuesFrom(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "mutation request limit of 100") || first.Added != 50 || runner.mutations != 100 || len(runner.added) != 50 {
		t.Fatalf("first bounded intake cycle = %#v, mutations=%d adds=%d error=%v", first, runner.mutations, len(runner.added), err)
	}
	existing := make([]WorkItem, 0, len(runner.added))
	for url, id := range runner.added {
		existing = append(existing, WorkItem{ID: id, URL: url, Status: "Needs assessment"})
	}
	second, err := project.SyncAssessmentIssuesFrom(t.Context(), existing)
	if err != nil || second.Added != 1 || len(runner.added) != 51 || runner.mutations != 102 {
		t.Fatalf("resumed intake cycle = %#v, mutations=%d adds=%d error=%v", second, runner.mutations, len(runner.added), err)
	}
}
