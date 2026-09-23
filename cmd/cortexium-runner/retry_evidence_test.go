package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/engine"
)

func retryEvidencePreviewFixture(t *testing.T) engine.RetryPlan {
	t.Helper()
	// Decode the public preview shape, including the manifest's private file
	// element type. These tests never grant or reconstruct an engine seal.
	const preview = `{
		"item":{"id":"parent-1","title":"Deliver reports","status":"Blocked"},
		"target_lane_id":"agent_qa","target_status":"Agent QA",
		"evidence_recovery":{
			"parent_id":"parent-1","plan_revision":"revision-1",
			"parent_candidate":{"CommitOID":"parent-commit","TreeOID":"parent-tree"},
			"members":[{
				"member_id":"member-1","source_workspace":"/retained/member-1",
				"acceptance_digest":"acceptance-1","recovery_required":true,
				"evidence":{"manifest_digest":"manifest-1","manifest":{
					"candidate_commit":"member-commit","candidate_tree":"member-tree",
					"source_root":"/retained/member-1",
					"selected_paths":["reports/receipt.json"],"missing_paths":[],
					"files":[{"path":"reports/receipt.json","bytes":123,"sha256":"receipt-hash"}],
					"provenance":{
						"kind":"recovered_historical_requires_parent_review",
						"parent_id":"parent-1","member_id":"member-1",
						"approved_content_digest":"content-1","plan_revision":"revision-1",
						"repository":"owner/repo","source_base_ref":"origin/plan",
						"source_base_oid":"original-base","historical_acceptance_digest":"acceptance-1"
					}
				}}
			}]
		}
	}`
	var plan engine.RetryPlan
	if err := json.Unmarshal([]byte(preview), &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestRetryEvidenceJSONAlwaysPreviewsNewCapture(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		plan := retryEvidencePreviewFixture(t)
		var output bytes.Buffer
		// No engine is supplied: neither preview mode may reach application.
		if err := runRetryPlan(t.Context(), nil, plan, dryRun, true, strings.NewReader("yes\n"), &output); err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Applied *bool            `json:"applied"`
			Retry   engine.RetryPlan `json:"retry"`
		}
		if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Applied == nil || *payload.Applied || !reflect.DeepEqual(payload.Retry, plan) {
			t.Fatalf("JSON changed or applied the recovery preview: %s", output.String())
		}
	}
}

func TestRetryEvidenceDryRunShowsExactCaptureAndDoesNotAuthorizePipedRetry(t *testing.T) {
	plan := retryEvidencePreviewFixture(t)
	var output bytes.Buffer
	if err := runRetryPlan(t.Context(), nil, plan, true, false, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"parent-1", "member-1", "revision-1", "parent-commit", "parent-tree",
		"member-commit", "member-tree", "/retained/member-1", "acceptance-1", "manifest-1",
		`"selected_paths"`, "reports/receipt.json", `"missing_paths": []`, `"bytes": 123`, `"sha256": "receipt-hash"`,
		"recovered_historical_requires_parent_review", "content-1", "owner/repo", "origin/plan", "original-base",
		"Fresh whole-plan review is required", "Dry run only",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("preview omitted %q:\n%s", want, output.String())
		}
	}
	output.Reset()
	err := runRetryPlan(t.Context(), nil, plan, false, false, strings.NewReader("yes\n"), &output)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("prior dry run or piped input authorized capture: %v", err)
	}
	if !strings.Contains(output.String(), "receipt-hash") || strings.Contains(output.String(), "Returning the item") {
		t.Fatalf("retry did not stop at the fresh preview: %s", output.String())
	}
}

func TestRetryEvidenceMissingCaptureRefusesBeforeApplication(t *testing.T) {
	plan := retryEvidencePreviewFixture(t)
	plan.EvidenceRecovery.Members[0].Evidence.Manifest.MissingPaths = []string{"reports/missing.json"}
	var output bytes.Buffer
	err := runRetryPlan(t.Context(), nil, plan, false, false, strings.NewReader("yes\n"), &output)
	if err == nil || !strings.Contains(err.Error(), "member member-1 is missing selected evidence") {
		t.Fatalf("missing selected evidence did not block capture: %v", err)
	}
	if !strings.Contains(output.String(), "reports/missing.json") || strings.Contains(output.String(), "Returning the item") {
		t.Fatalf("missing evidence was hidden or retry began: %s", output.String())
	}
}

func TestConfirmRetryEvidenceRecoveryDefaultsToNo(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  bool
	}{
		{name: "default no", input: "\n", want: false},
		{name: "explicit no", input: "2\n", want: false},
		{name: "explicit yes", input: "1\n", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			confirmed, err := confirmRetryEvidenceRecovery(newInitPrompter(strings.NewReader(test.input), &output))
			if err != nil || confirmed != test.want {
				t.Fatalf("confirmation = %t, %v; want %t", confirmed, err, test.want)
			}
			if !strings.Contains(output.String(), "Capture this exact historical evidence and retry the displayed parent?") {
				t.Fatalf("confirmation did not describe the authorized action: %s", output.String())
			}
		})
	}
}
