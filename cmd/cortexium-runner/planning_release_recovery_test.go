package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/github"
)

func TestAuthenticatedReleaseCompletionPreviewAndConfirmation(t *testing.T) {
	batch := github.BatchApprovalPlan{Released: true, Source: github.WorkItem{ID: "source", Approval: "signed"}, Children: []github.BatchApprovalItem{{Item: github.WorkItem{ID: "child", Body: "Exact reviewed work", Status: "In Progress", Branch: "runner/retained"}}}}
	var output bytes.Buffer
	writeBatchApprovalPreview(&output, batch)
	if !strings.Contains(output.String(), "Release is already authenticated") || !strings.Contains(output.String(), "runner/retained") || !strings.Contains(output.String(), "Exact reviewed work") {
		t.Fatal("preview omitted release state or retained work")
	}
	for _, input := range []string{"\n", "1\n"} {
		output.Reset()
		accepted, err := confirmBatchApproval(newInitPrompter(strings.NewReader(input), &output), batch)
		if err != nil {
			t.Fatal(err)
		}
		if accepted != (input == "1\n") {
			t.Fatal("completion did not require explicit Yes")
		}
		if !strings.Contains(output.String(), "Finish recording the already authenticated complete batch release?") {
			t.Fatal("confirmation proposed releasing child actions again")
		}
	}
}
