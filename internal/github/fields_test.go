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
	args []string
}

func (r *projectFieldTestRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	if command != "gh" {
		return subprocess.Result{}, nil
	}
	r.args = append([]string{}, args...)
	return subprocess.Result{}, nil
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
