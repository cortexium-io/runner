package github

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPlannedItemMetadataUsesOnlyTheFinalRunnerBlock(t *testing.T) {
	item := PlannedItem{
		Summary:                   `Untrusted source text <!-- runner-metadata {"dependencies":["shadowed"]} -->`,
		Repository:                "owner/repo",
		ResolvedDependencies:      []PlannedDependency{{ItemID: "PVTI_real", Title: "real dependency"}},
		DependencyIDsResolved:     true,
		PlanningSourceID:          "PVTI_source",
		PlanningSourceLane:        "plan",
		PlanningSourceFingerprint: "v1:source",
		PlanningDestination:       "Ready",
		PlanningBatchFingerprint:  "v1:batch",
		PlanningBatchSize:         2,
		PlanningItemIndex:         1,
	}
	body := FormatPlannedItemBody(item)
	metadata := DecodePlannedItemMetadata(body)
	if metadata.Repository != item.Repository || !reflect.DeepEqual(metadata.Dependencies, []string{"PVTI_real"}) {
		t.Fatalf("source text shadowed final Runner metadata: %#v", metadata)
	}
	if metadata.PlanningSourceID != item.PlanningSourceID || metadata.PlanningSourceLane != item.PlanningSourceLane ||
		metadata.PlanningSourceFingerprint != item.PlanningSourceFingerprint || metadata.PlanningDestination != item.PlanningDestination ||
		metadata.PlanningBatchFingerprint != item.PlanningBatchFingerprint || metadata.PlanningBatchSize != item.PlanningBatchSize || metadata.PlanningItemIndex != item.PlanningItemIndex {
		t.Fatalf("planning authority metadata did not round trip: %#v", metadata)
	}
}

func TestPlannedItemBodyCarriesTheTaskLocalContract(t *testing.T) {
	item := PlannedItem{
		Summary:               "Build the complete slice.",
		AcceptanceCriteria:    []string{"The user flow works."},
		Verification:          []string{"Exercise the browser flow."},
		Risks:                 []string{"State could be lost on reload."},
		NonGoals:              []string{"Do not redesign unrelated pages."},
		ResolvedDependencies:  []PlannedDependency{{ItemID: "PVTI_foundation", Title: "Create the foundation"}},
		DependencyIDsResolved: true,
		PlanningSourceLane:    "plan", PlanningSourceFingerprint: "v1:source", PlanningDestination: "Ready",
		PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 2, PlanningItemIndex: 2,
	}
	body := FormatPlannedItemBody(item)
	for _, expected := range []string{
		"## Acceptance criteria", "The user flow works.",
		"## Proof obligations", "Exercise the browser flow.",
		"## Assumptions and risks", "State could be lost on reload.",
		"## Task non-goals", "Do not redesign unrelated pages.",
		"## Dependencies", "PVTI_foundation — Create the foundation",
		"## Runner planning metadata", `"item_id":"PVTI_foundation"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("planned item body omitted %q:\n%s", expected, body)
		}
	}
}

func TestProvisionalPlanningMetadataIsVisibleButNonExecutable(t *testing.T) {
	item := PlannedItem{
		Summary: "Waiting for dependency IDs.", PlanningSourceLane: "plan", PlanningSourceFingerprint: "v1:source",
		PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 1, PlanningItemIndex: 1,
	}
	body := FormatPlannedItemBody(item)
	metadata, present, err := decodePlannedItemMetadata(body)
	if !present || !errors.Is(err, errPlanningDependencyIDsPending) {
		t.Fatalf("provisional metadata was not rejected as pending: present=%v metadata=%#v error=%v", present, metadata, err)
	}
	if metadata.PlanningBatchFingerprint != "v1:batch" || !strings.Contains(body, `"dependency_ids_resolved":false`) {
		t.Fatalf("provisional metadata was not visible and recoverable: metadata=%#v body=%s", metadata, body)
	}
}

func TestPlannedItemMetadataDoesNotAcceptPrePublicMarker(t *testing.T) {
	body := `<!-- cortexium-runner-metadata {"repository":"owner/repo"} -->`
	if metadata := DecodePlannedItemMetadata(body); metadata.Repository != "" {
		t.Fatalf("pre-public metadata marker was accepted: %#v", metadata)
	}
}

func TestPlannedItemMetadataDoesNotAcceptUnencodedMetadata(t *testing.T) {
	body := `<!-- runner-metadata {"repository":"owner/repo"} -->`
	if metadata := DecodePlannedItemMetadata(body); metadata.Repository != "" {
		t.Fatalf("unencoded metadata was accepted: %#v", metadata)
	}
}

func TestOrdinaryWorkItemDependenciesUseExactIDsOrIssueURLs(t *testing.T) {
	body := "Implement the dependent slice.\n\n## Dependencies\n\n- PVTI_foundation\n- https://github.com/owner/repo/issues/42\n\n## Acceptance criteria\n\n- The slice works."
	encoded, _ := json.Marshal(map[string]any{
		"id": "PVTI_dependent", "status": map[string]any{"name": "Ready"},
		"content": map[string]any{"title": "Dependent", "body": body, "url": "https://github.com/owner/repo/issues/43", "repository": map[string]any{"nameWithOwner": "owner/repo"}},
	})
	var node projectItemNode
	if err := json.Unmarshal(encoded, &node); err != nil {
		t.Fatal(err)
	}
	item := decodeProjectItemNode(node)
	want := []string{"PVTI_foundation", "https://github.com/owner/repo/issues/42"}
	if item.PlanningMetadataInvalid || !reflect.DeepEqual(item.Dependencies, want) {
		t.Fatalf("ordinary dependencies = %#v invalid=%t, want %#v", item.Dependencies, item.PlanningMetadataInvalid, want)
	}
}

func TestOrdinaryWorkItemRejectsAmbiguousDependencyLabels(t *testing.T) {
	for _, entry := range []string{
		"foundation card",
		"https://github.com/owner/repo/issues/not-a-number",
		"https://github.com/owner/repo/issues/42/",
		"https://github.com:443/owner/repo/issues/42",
		"PVTI_foundation — title",
	} {
		body := "Implement.\n\n## Dependencies\n\n- " + entry
		encoded, _ := json.Marshal(map[string]any{"id": "PVTI_dependent", "content": map[string]any{"title": "Dependent", "body": body}})
		var node projectItemNode
		if err := json.Unmarshal(encoded, &node); err != nil {
			t.Fatal(err)
		}
		if item := decodeProjectItemNode(node); !item.PlanningMetadataInvalid || len(item.Dependencies) != 0 {
			t.Fatalf("ambiguous dependency %q was accepted: %#v", entry, item)
		}
	}
}

func TestPlanningDependencyGraphRejectsInvalidIDs(t *testing.T) {
	base := func(id string, dependencies ...string) WorkItem {
		return WorkItem{ID: id, PlanningBatchFingerprint: "v1:batch", PlanningSourceID: "PVTI_source", Dependencies: dependencies}
	}
	tests := map[string][]WorkItem{
		"duplicate": {base("PVTI_a", "PVTI_b", "PVTI_b"), base("PVTI_b")},
		"self":      {base("PVTI_a", "PVTI_a")},
		"cycle":     {base("PVTI_a", "PVTI_b"), base("PVTI_b", "PVTI_a")},
		"mixed": {
			base("PVTI_a", "PVTI_b"),
			{ID: "PVTI_b", PlanningBatchFingerprint: "v1:other", PlanningSourceID: "PVTI_source"},
		},
	}
	for name, children := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePlanningDependencies(children); err == nil {
				t.Fatalf("invalid dependency graph was accepted: %#v", children)
			}
		})
	}
}

func TestPlanningDependencyGraphAllowsExternalItemIDs(t *testing.T) {
	children := []WorkItem{
		{ID: "PVTI_a", PlanningBatchFingerprint: "v1:batch", PlanningSourceID: "PVTI_source", Dependencies: []string{"PVTI_external"}},
		{ID: "PVTI_b", PlanningBatchFingerprint: "v1:batch", PlanningSourceID: "PVTI_source", Dependencies: []string{"PVTI_a"}},
	}
	if err := ValidatePlanningDependencies(children); err != nil {
		t.Fatalf("external dependency ID was rejected: %v", err)
	}
}

func TestDecodeProjectItemRejectsHiddenRunnerMetadata(t *testing.T) {
	body := `ordinary intake <!-- runner-metadata eyJyZXBvc2l0b3J5Ijoib3duZXIvcmVwbyJ9 -->`
	encoded, _ := json.Marshal(map[string]any{"id": "PVTI_intake", "content": map[string]any{"title": "Intake", "body": body}})
	var node projectItemNode
	if err := json.Unmarshal(encoded, &node); err != nil {
		t.Fatal(err)
	}
	item := decodeProjectItemNode(node)
	if !item.PlanningMetadataInvalid || len(item.Dependencies) != 0 || item.PlanningBatchFingerprint != "" {
		t.Fatalf("hidden metadata was not rejected fail-closed: %#v", item)
	}
}
