package github

import (
	"os"
	"runtime"
	"strings"
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

func TestReadyItemsAdoptsDirectPlanAndReadyIntake(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		role   string
	}{
		{name: "planner request", status: "Plan", role: config.WorkRolePlanner},
		{name: "specified implementation", status: "Ready", role: config.WorkRoleImplementer},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := &transitionTestRunner{}
			project := NewProject(config.ProjectConfig{
				GitHubProjectConfig: config.GitHubProjectConfig{
					Owner: "owner", Number: 1, IntakeRepository: "owner/repo", ApprovalField: "Runner Approval",
				},
				ApprovalAuthorityKey: []byte("manual-intake-test-authority-key"),
				ReadyStatus:          "Ready",
				AgentStatuses:        []string{"Plan", "Ready"},
				InitialLaneID:        "plan",
				InitialRole:          config.WorkRolePlanner,
				LaneStatuses:         map[string]string{"plan": "Plan", "ready": "Ready"},
				LaneRoles:            map[string]string{"plan": config.WorkRolePlanner, "ready": config.WorkRoleImplementer},
			}, run)
			project.schema = githubProjectSchema{ProjectID: "PVT_test", Fields: map[string]githubProjectField{
				normalizeProjectKey("Runner Approval"): {ID: "F_approval", Name: "Runner Approval", Type: "ProjectV2Field"},
			}}
			item := WorkItem{
				ID: "PVTI_manual", Title: "Human request", Body: "Exact human-authored request.",
				URL: "https://github.com/owner/repo/issues/1", Repository: "owner/repo", Status: test.status,
			}

			ready, err := project.ReadyItems(t.Context(), []WorkItem{item}, 1)
			if err != nil {
				t.Fatalf("adopt %s: %v", test.status, err)
			}
			if len(ready) != 1 || ready[0].Item.Status != test.status || ready[0].Role != test.role || ready[0].Item.Approval == "" {
				t.Fatalf("unexpected adopted action: %#v", ready)
			}
			if calls := strings.Join(run.calls, "\n"); !strings.Contains(calls, "--field-id F_approval --text") {
				t.Fatalf("adoption did not persist exact authority: %s", calls)
			}
		})
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

func TestReadyItemsAcceptsAuthenticatedSuccessfulBatchDependency(t *testing.T) {
	project := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 1},
		ReadyStatus:         "Ready", DoneStatus: "Done", AgentStatuses: []string{"Ready"},
		InitialLaneID: "ready", InitialRole: "implementer", ApprovalLaneID: "ready",
		LaneStatuses: map[string]string{"ready": "Ready", "done": "Done"}, LaneRoles: map[string]string{"ready": "implementer"},
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
	done, err := project.signAction(foundation, "implementer", "done")
	if err != nil {
		t.Fatalf("record successful dependency outcome: %v", err)
	}
	foundation.Approval = done.Item.Approval
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

func TestReadyItemsRejectsManualDoneWithoutAuthenticatedSuccess(t *testing.T) {
	project := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 1},
		ReadyStatus:         "Ready", DoneStatus: "Done", AgentStatuses: []string{"Ready"},
		InitialLaneID: "ready", InitialRole: "implementer", ApprovalLaneID: "ready",
		LaneStatuses: map[string]string{"ready": "Ready", "done": "Done"}, LaneRoles: map[string]string{"ready": "implementer"},
	}, nil)
	foundation := WorkItem{ID: "PVTI_foundation", Body: "Build the foundation.", URL: "https://github.com/owner/repo/issues/1", Repository: "owner/repo", Status: "Ready"}
	dependent := WorkItem{ID: "PVTI_dependent", Body: "Use the foundation.", URL: "https://github.com/owner/repo/issues/2", Repository: "owner/repo", Status: "Ready", Dependencies: []string{foundation.URL}}
	for _, item := range []*WorkItem{&foundation, &dependent} {
		action, err := project.signAction(*item, "implementer", "ready")
		if err != nil {
			t.Fatalf("sign %s: %v", item.ID, err)
		}
		item.Approval = action.Item.Approval
	}

	foundation.Status = "Done"
	ready, err := project.ReadyItems(t.Context(), []WorkItem{foundation, dependent}, 2)
	if err != nil {
		t.Fatalf("check manual Done dependency: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("manual Done status released dependent work: %#v", ready)
	}

	succeeded, err := project.signAction(foundation, "implementer", "done")
	if err != nil {
		t.Fatalf("authenticate successful outcome: %v", err)
	}
	foundation.Approval = succeeded.Item.Approval
	ready, err = project.ReadyItems(t.Context(), []WorkItem{foundation, dependent}, 2)
	if err != nil || len(ready) != 1 || ready[0].Item.ID != dependent.ID {
		t.Fatalf("authenticated success did not release direct Ready work: ready=%#v error=%v", ready, err)
	}
}

func TestReadyItemsAllowsAuthenticatedCrossBatchDependency(t *testing.T) {
	project := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 1},
		ReadyStatus:         "Ready", DoneStatus: "Done", AgentStatuses: []string{"Ready"},
		InitialLaneID: "ready", InitialRole: "implementer", ApprovalLaneID: "ready",
		LaneStatuses: map[string]string{"ready": "Ready", "done": "Done"}, LaneRoles: map[string]string{"ready": "implementer"},
	}, nil)
	batchItem := func(id, fingerprint string) WorkItem {
		return WorkItem{
			ID: id, Body: "Approved " + id, Repository: "owner/repo", Status: "Ready",
			PlanningSourceLane: "local_plan", PlanningSourceFingerprint: "v1:source:" + id, PlanningDestination: "Ready",
			PlanningBatchFingerprint: fingerprint, PlanningBatchSize: 1, PlanningItemIndex: 1,
		}
	}
	foundation := batchItem("PVTI_batch_a", "v1:batch:a")
	foundation.Status = "Done"
	done, err := project.signAction(foundation, "implementer", "done")
	if err != nil {
		t.Fatalf("sign successful first batch: %v", err)
	}
	foundation.Approval = done.Item.Approval
	dependent := batchItem("PVTI_batch_b", "v1:batch:b")
	dependent.Dependencies = []string{foundation.ID}
	action, err := project.signAction(dependent, "implementer", "ready")
	if err != nil {
		t.Fatalf("sign dependent second batch: %v", err)
	}
	dependent.Approval = action.Item.Approval

	ready, err := project.ReadyItems(t.Context(), []WorkItem{foundation, dependent}, 2)
	if err != nil || len(ready) != 1 || ready[0].Item.ID != dependent.ID {
		t.Fatalf("authenticated cross-batch dependency was not released: ready=%#v error=%v", ready, err)
	}
}

func TestSuccessfulOutcomeAcceptsAuthenticatedPlanningBatchRelease(t *testing.T) {
	project := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 1},
		DoneStatus:          "Done",
	}, nil)
	source := WorkItem{
		ID: "PVTI_plan", Title: "Plan the work", Body: "Create a reviewable implementation plan.",
		Repository: "owner/repo", Status: "Done",
	}
	child := WorkItem{
		ID: "PVTI_child", Title: "Implement the plan", Body: "Deliver the planned work.", Repository: "owner/repo", Status: "Assessment",
		PlanningSourceID: source.ID, PlanningSourceLane: "local_plan", PlanningSourceFingerprint: PlanningSourceFingerprint(source),
		PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch:plan", PlanningBatchSize: 1, PlanningItemIndex: 1,
	}
	release, err := project.signPlanningBatch(source, []WorkItem{child}, batchReleasedState, "")
	if err != nil {
		t.Fatalf("authenticate planning batch release: %v", err)
	}
	source.Approval = release
	if !project.hasSuccessfulOutcome(source, []WorkItem{source, child}) {
		t.Fatal("authenticated successful planning outcome was not recognized")
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
