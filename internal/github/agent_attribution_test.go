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

type attributionMutationRunner struct{ args []string }

func (r *attributionMutationRunner) Run(_ context.Context, _ string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	r.args = append([]string(nil), args...)
	return subprocess.Result{Stdout: `{}`}, nil
}

func TestAgentAttributionKeepsFeedbackIndependentAndBounded(t *testing.T) {
	project, run := transitionTestProject()
	project.schema.Fields[normalizeProjectKey(config.RunnerAgentsFieldName)] = githubProjectField{ID: "F_agents", Name: config.RunnerAgentsFieldName, Type: "ProjectV2Field", DataType: "TEXT"}
	project.SetAgentAttributionReader(func(item WorkItem) string {
		if item.ID != "card" {
			t.Fatalf("attribution card = %q", item.ID)
		}
		return "Implementation: qwen3.8 / high\nQA: gpt-6.1-sol / xhigh"
	})
	feedback := strings.Repeat("ø", 500)
	updates := project.agentAttributionUpdates(WorkItem{ID: "card", Result: feedback})
	if len(updates) != 1 || updates[0].fieldName != config.RunnerAgentsFieldName || updates[0].text != "Implementation: qwen3.8 / high\nQA: gpt-6.1-sol / xhigh" {
		t.Fatalf("wrong attribution field update: %#v", updates)
	}
	mutation := &attributionMutationRunner{}
	project.run = mutation
	if err := project.setResult(t.Context(), "card", feedback); err != nil {
		t.Fatal(err)
	}
	wroteFullFeedback := false
	for _, arg := range mutation.args {
		if arg == "text_1="+feedback {
			wroteFullFeedback = true
		}
	}
	if !wroteFullFeedback {
		t.Fatalf("attribution clipped full retry feedback: %#v", mutation.args)
	}
	// Missing fields on an older Project must not suppress or clip retry feedback.
	project.run = run
	delete(project.schema.Fields, normalizeProjectKey(config.RunnerAgentsFieldName))
	if err := project.setResult(t.Context(), "card", feedback); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 1 || !strings.HasSuffix(run.calls[0], "--text "+feedback) {
		t.Fatalf("full feedback changed: %#v", run.calls)
	}
}

func TestAgentAttributionBoundsUnicodeWithoutAffectingOtherFields(t *testing.T) {
	project, _ := transitionTestProject()
	project.schema.Fields[normalizeProjectKey(config.RunnerAgentsFieldName)] = githubProjectField{ID: "F_agents", Name: config.RunnerAgentsFieldName, Type: "ProjectV2Field", DataType: "TEXT"}
	project.SetAgentAttributionReader(func(WorkItem) string { return strings.Repeat("ø", 1_000) })
	updates := project.agentAttributionUpdates(WorkItem{ID: "card"})
	if len(updates) != 1 || len(updates[0].text) > 1_000 || !utf8.ValidString(updates[0].text) {
		t.Fatalf("attribution exceeded field bounds: %#v", updates)
	}
}
