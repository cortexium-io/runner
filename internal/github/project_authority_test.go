package github

import (
	"os"
	"runtime"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestDelegatedContentDigestBindsExecutionDefiningApprovedContent(t *testing.T) {
	base := WorkItem{
		ID: "PVTI_child", Title: "Presentation title", Body: "  exact approved body  ", URL: "https://github.com/owner/repo/issues/1",
		Repository: "owner/repo", Dependencies: []string{"PVTI_b", "PVTI_a"}, Status: "Ready", Result: "mutable lifecycle result",
		PlanningSourceID: "PVTI_source", PlanningSourceLane: "plan", PlanningSourceFingerprint: "v1:source",
		PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 2, PlanningItemIndex: 1,
	}
	original := DelegatedContentFor(base)
	if original.BodySnapshot != "exact approved body" || len(original.Digest) != len("v1:")+64 {
		t.Fatalf("unexpected delegated content: %#v", original)
	}

	presentation := base
	presentation.Title = "Renamed presentation title"
	presentation.URL = "https://github.com/owner/repo/issues/renamed-provenance"
	presentation.Status = "Agent QA"
	presentation.Result = "different lifecycle result"
	presentation.Dependencies = []string{"PVTI_a", "PVTI_b"}
	if got := DelegatedContentFor(presentation); got != original {
		t.Fatalf("presentation, lifecycle, URL, or dependency ordering changed delegated identity: original=%#v got=%#v", original, got)
	}

	mutations := map[string]func(*WorkItem){
		"body":                        func(item *WorkItem) { item.Body = "changed body" },
		"repository":                  func(item *WorkItem) { item.Repository = "owner/other" },
		"dependencies":                func(item *WorkItem) { item.Dependencies = []string{"PVTI_a"} },
		"planning source id":          func(item *WorkItem) { item.PlanningSourceID = "PVTI_other" },
		"planning source lane":        func(item *WorkItem) { item.PlanningSourceLane = "other" },
		"planning source fingerprint": func(item *WorkItem) { item.PlanningSourceFingerprint = "v1:other" },
		"planning destination":        func(item *WorkItem) { item.PlanningDestination = "Agent QA" },
		"planning batch fingerprint":  func(item *WorkItem) { item.PlanningBatchFingerprint = "v1:other" },
		"planning batch size":         func(item *WorkItem) { item.PlanningBatchSize++ },
		"planning item index":         func(item *WorkItem) { item.PlanningItemIndex++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Dependencies = append([]string(nil), base.Dependencies...)
			mutate(&changed)
			if got := DelegatedContentFor(changed).Digest; got == original.Digest {
				t.Fatalf("%s mutation retained delegated content digest %q", name, got)
			}
		})
	}
}

func TestPlanningSourceFingerprintIgnoresPresentationAndProvenance(t *testing.T) {
	item := WorkItem{ID: "PVTI_source", Title: "Original", Body: "approved plan", URL: "https://github.com/owner/repo/issues/1", Repository: "owner/repo"}
	original := PlanningSourceFingerprint(item)
	item.Title = "Renamed"
	item.URL = "https://github.com/owner/repo/issues/2"
	if got := PlanningSourceFingerprint(item); got != original {
		t.Fatalf("title or provenance URL changed planning source identity: original=%q got=%q", original, got)
	}
	item.Body = "changed plan"
	if got := PlanningSourceFingerprint(item); got == original {
		t.Fatalf("changed planning body retained source identity %q", got)
	}
}

func TestPlanningBatchAuthorityBindsDelegatedContentDigest(t *testing.T) {
	project := &Project{}
	source := WorkItem{ID: "PVTI_source", Body: "approved planning request", Repository: "owner/repo"}
	child := WorkItem{
		ID: "PVTI_child", Title: "Presentation", Body: "approved child", URL: "https://github.com/owner/repo/issues/1", Repository: "owner/repo",
		PlanningSourceID: source.ID, PlanningSourceLane: "plan", PlanningSourceFingerprint: PlanningSourceFingerprint(source),
		PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 1, PlanningItemIndex: 1,
	}
	original, err := project.planningBatchPayload(source, []WorkItem{child}, batchStagedState, "generation", "authority")
	if err != nil {
		t.Fatalf("construct planning batch authority: %v", err)
	}
	presentation := child
	presentation.Title = "Renamed"
	presentation.URL = "https://github.com/owner/repo/issues/2"
	presented, err := project.planningBatchPayload(source, []WorkItem{presentation}, batchStagedState, "generation", "authority")
	if err != nil || presented.ChildrenDigest != original.ChildrenDigest {
		t.Fatalf("title or issue provenance changed staged content identity: original=%#v presented=%#v error=%v", original, presented, err)
	}
	changed := child
	changed.Repository = "owner/other"
	modified, err := project.planningBatchPayload(source, []WorkItem{changed}, batchStagedState, "generation", "authority")
	if err != nil {
		t.Fatalf("construct changed planning batch authority: %v", err)
	}
	if modified.ChildrenDigest == original.ChildrenDigest || DelegatedContentFor(changed).Digest == DelegatedContentFor(child).Digest {
		t.Fatal("changed delegated content retained staged-batch authority identity")
	}
}

func TestReadyItemsAcceptsAuthenticatedBatchSiblingMovedToDone(t *testing.T) {
	project := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 1},
		ReadyStatus:         "Ready", DoneStatus: "Done", AgentStatuses: []string{"Ready"},
		InitialLaneID: "implementer", InitialRole: "implementer", ApprovalLaneID: "implementer",
		LaneStatuses: map[string]string{"implementer": "Ready"}, LaneRoles: map[string]string{"implementer": "implementer"},
	}, nil)
	batch := func(id string, index int, dependencies []string) WorkItem {
		return WorkItem{
			ID: id, Body: "approved " + id, Repository: "owner/repo", Dependencies: dependencies, Status: "Ready",
			PlanningSourceLane: "local_plan", PlanningSourceFingerprint: "v1:source", PlanningDestination: "Ready",
			PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 2, PlanningItemIndex: index,
		}
	}
	foundation := batch("PVTI_foundation", 1, nil)
	dependent := batch("PVTI_dependent", 2, []string{foundation.ID})
	for _, item := range []*WorkItem{&foundation, &dependent} {
		action, err := project.signAction(*item, "implementer", approvalReadyState)
		if err != nil {
			t.Fatalf("sign %s: %v", item.ID, err)
		}
		item.Approval = action.Item.Approval
	}

	foundation.Status = "Done"
	ready, err := project.ReadyItems(t.Context(), []WorkItem{foundation, dependent}, 2)
	if err != nil || len(ready) != 1 || ready[0].Item.ID != dependent.ID {
		t.Fatalf("authenticated Done sibling blocked remaining batch work: ready=%#v error=%v", ready, err)
	}

	foundation.Body = "edited after completion"
	ready, err = project.ReadyItems(t.Context(), []WorkItem{foundation, dependent}, 2)
	if err != nil {
		t.Fatalf("check edited Done sibling: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("edited Done sibling retained batch authority: %#v", ready)
	}
}

func TestPersistentOperatorAuthorityIsPrivateAndStable(t *testing.T) {
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", t.TempDir())
	first, err := newPersistentOperatorAuthority("runner_test")
	if err != nil {
		t.Fatalf("create authority: %v", err)
	}
	firstKey, firstID, err := first.load()
	if err != nil {
		t.Fatalf("load authority: %v", err)
	}
	info, err := os.Stat(first.keyPath)
	if err != nil {
		t.Fatalf("stat authority key: %v", err)
	}
	privateMode := runtime.GOOS == "windows" || info.Mode().Perm() == 0o600
	if !privateMode || len(firstKey) != authorityKeyBytes || firstID == "" {
		t.Fatalf("authority key is not private and complete: mode=%o bytes=%d id=%q", info.Mode().Perm(), len(firstKey), firstID)
	}

	second, err := newPersistentOperatorAuthority("runner_test")
	if err != nil {
		t.Fatalf("reopen authority: %v", err)
	}
	secondKey, secondID, err := second.load()
	if err != nil {
		t.Fatalf("reload authority: %v", err)
	}
	if secondID != firstID || string(secondKey) != string(firstKey) {
		t.Fatal("the same runner id did not reuse its local approval authority")
	}
}
