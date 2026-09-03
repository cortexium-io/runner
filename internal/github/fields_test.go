package github

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type projectFieldTestRunner struct {
	args   []string
	calls  [][]string
	stdout string
}

func (r *projectFieldTestRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	if command != "gh" {
		return subprocess.Result{}, nil
	}
	r.args = append([]string{}, args...)
	r.calls = append(r.calls, append([]string{}, args...))
	return subprocess.Result{Stdout: r.stdout}, nil
}

func TestProjectAppliesTransitionFieldsInOneGraphQLMutation(t *testing.T) {
	run := &projectFieldTestRunner{stdout: `{"data":{}}`}
	project := NewProject(config.ProjectConfig{}, run)
	project.schema = githubProjectSchema{ProjectID: "PVT_test", Fields: map[string]githubProjectField{
		normalizeProjectKey("Status"): {
			ID: "F_status", Name: "Status", Type: "ProjectV2SingleSelectField",
			Options: map[string]githubProjectOption{normalizeProjectKey("Ready"): {ID: "O_ready", Name: "Ready"}},
		},
		normalizeProjectKey("Runner Phase"):    {ID: "F_phase", Name: "Runner Phase", Type: "ProjectV2Field"},
		normalizeProjectKey("Runner Approval"): {ID: "F_approval", Name: "Runner Approval", Type: "ProjectV2Field"},
	}}
	if err := project.applyFieldUpdates(t.Context(), "PVTI_test",
		textProjectField("Runner Phase", "ready"),
		textProjectField("Runner Approval", "signed"),
		statusProjectField("Status", "Ready"),
	); err != nil {
		t.Fatalf("apply fields: %v", err)
	}
	if len(run.calls) != 1 {
		t.Fatalf("field updates used %d commands, want one: %#v", len(run.calls), run.calls)
	}
	joined := strings.Join(run.calls[0], " ")
	for _, expected := range []string{"api graphql", "updateProjectV2ItemFieldValue", "item_id=PVTI_test", "field_0=F_phase", "text_0=ready", "field_1=F_approval", "text_1=signed", "field_2=F_status", "option_2=O_ready"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("batched field update omitted %q: %s", expected, joined)
		}
	}
}

func TestProjectRejectsGraphQLFieldUpdateErrors(t *testing.T) {
	run := &projectFieldTestRunner{stdout: `{"data":{"u0":null},"errors":[{"message":"field update failed"}]}`}
	project := NewProject(config.ProjectConfig{}, run)
	project.schema = githubProjectSchema{ProjectID: "PVT_test", Fields: map[string]githubProjectField{
		normalizeProjectKey("Runner Phase"): {ID: "F_phase", Name: "Runner Phase", Type: "ProjectV2Field"},
	}}
	err := project.applyFieldUpdates(t.Context(), "PVTI_test", textProjectField("Runner Phase", "ready"))
	if err == nil || !strings.Contains(err.Error(), "field update failed") {
		t.Fatalf("GraphQL field update error was not returned: %v", err)
	}
}

func TestLifecycleItemsByIDUsesOneNodesQueryAndPreservesOrder(t *testing.T) {
	run := &projectFieldTestRunner{stdout: `{"data":{"nodes":[{"id":"PVTI_two","content":{"title":"Two","body":"two"}},{"id":"PVTI_one","content":{"title":"One","body":"one"}}]}}`}
	project := NewProject(config.ProjectConfig{}, run)
	project.schema = githubProjectSchema{ProjectID: "PVT_test", Fields: map[string]githubProjectField{"status": {ID: "F_status"}}}
	items, err := project.LifecycleItemsByID(t.Context(), []string{"PVTI_one", "PVTI_two"})
	if err != nil {
		t.Fatalf("load exact items: %v", err)
	}
	if len(items) != 2 || items[0].ID != "PVTI_one" || items[1].ID != "PVTI_two" {
		t.Fatalf("exact item order changed: %#v", items)
	}
	if len(run.calls) != 1 || !strings.Contains(strings.Join(run.calls[0], " "), `nodes(ids:["PVTI_one","PVTI_two"])`) {
		t.Fatalf("exact items were not loaded in one nodes query: %#v", run.calls)
	}
}

func TestProjectResultFitsGitHubTextFieldAndPreservesUTF8(t *testing.T) {
	run := &projectFieldTestRunner{}
	project := NewProject(config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{ResultField: "Runner Result"}}, run)
	project.schema = githubProjectSchema{
		ProjectID: "PVT_test",
		Fields: map[string]githubProjectField{
			normalizeProjectKey("Runner Result"): {ID: "F_result", Name: "Runner Result", Type: "ProjectV2Field"},
		},
	}
	if err := project.setResult(t.Context(), "item_test", strings.Repeat("blocked ø ", 200)); err != nil {
		t.Fatalf("set long result: %v", err)
	}
	text := ""
	for index, arg := range run.args {
		if arg == "--text" && index+1 < len(run.args) {
			text = run.args[index+1]
			break
		}
	}
	if text == "" {
		t.Fatalf("GitHub Project update did not include result text: %#v", run.args)
	}
	if len(text) > maxProjectTextFieldBytes {
		t.Fatalf("result text has %d bytes, limit is %d", len(text), maxProjectTextFieldBytes)
	}
	if !utf8.ValidString(text) || !strings.HasSuffix(text, "...") {
		t.Fatalf("truncated result is not valid, visibly truncated UTF-8: %q", text)
	}
}
