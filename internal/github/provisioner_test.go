package github

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type projectProvisionRunner struct {
	statusConfigured   bool
	boardConfigured    bool
	createdBoard       bool
	visibleFieldsReady bool
	phaseVisible       bool
	defaultViewDeleted bool
	createdFields      map[string]string
	expectPrune        bool
	pruned             bool
	statusUsageJSON    string
	statusUsagePages   []string
	statusUsageCalls   int
	emptyLabelList     bool
	calls              []string
}

func (r *projectProvisionRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	if command != "gh" {
		return subprocess.Result{}, errors.New("unexpected command")
	}
	joined := strings.Join(args, " ")
	r.calls = append(r.calls, joined)
	switch {
	case strings.HasPrefix(joined, "project create "):
		return subprocess.Result{Stdout: `{"id":"PVT_created","number":9,"owner":{"login":"example","type":"Organization"},"url":"https://github.com/orgs/example/projects/9"}`}, nil
	case strings.HasPrefix(joined, "repo view "):
		if strings.Contains(joined, "organization/runner") {
			return subprocess.Result{Stdout: `{"nameWithOwner":"organization/runner","hasIssuesEnabled":true}`}, nil
		}
		return subprocess.Result{Stdout: `{"nameWithOwner":"example/runner","hasIssuesEnabled":true}`}, nil
	case strings.HasPrefix(joined, "project view "):
		return subprocess.Result{Stdout: `{"id":"PVT_created"}`}, nil
	case strings.HasPrefix(joined, "api graphql ") && strings.Contains(joined, "fields(first:100,after:$after)"):
		additionalStatuses := ""
		if r.statusConfigured {
			additionalStatuses = `,{"id":"O_assessment","name":"Needs assessment"},{"id":"O_backlog","name":"Backlog"},{"id":"O_plan","name":"Plan"},{"id":"O_ready","name":"Ready"},{"id":"O_qa","name":"Agent QA"},{"id":"O_pr_ready","name":"PR Ready"},{"id":"O_blocked","name":"Blocked"}`
		}
		additionalFields := ""
		for _, field := range []struct{ name, id, dataType string }{
			{"Runner Result", "F_result", "TEXT"}, {"Runner Approval", "F_approval", "TEXT"},
			{"Runner Phase", "F_phase", "TEXT"}, {"Runner Activity", "F_activity", "TEXT"}, {"QA Failures", "F_qa", "NUMBER"},
			{"Runner Branch", "F_branch", "TEXT"}, {"Pull Request", "F_pr", "TEXT"}, {"QA Commit", "F_qa_commit", "TEXT"},
		} {
			if dataType, exists := r.createdFields[field.name]; exists {
				additionalFields += `,{"__typename":"ProjectV2Field","id":"` + field.id + `","name":"` + field.name + `","dataType":"` + dataType + `"}`
			}
		}
		return subprocess.Result{Stdout: `{"data":{"node":{"fields":{"nodes":[{"__typename":"ProjectV2SingleSelectField","id":"F_status","name":"Status","dataType":"SINGLE_SELECT","options":[{"id":"O_triage","name":"Triage"},{"id":"O_verify","name":"Verification"},{"id":"O_running","name":"In Progress"},{"id":"O_done","name":"Done"}` + additionalStatuses + `]}` + additionalFields + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}, nil
	case strings.HasPrefix(joined, "api graphql ") && strings.Contains(joined, "views(first:100)"):
		if strings.Count(joined, "{") != strings.Count(joined, "}") {
			return subprocess.Result{}, errors.New("GitHub Project views query has unbalanced braces")
		}
		layout := "TABLE_LAYOUT"
		if r.boardConfigured {
			layout = "BOARD_LAYOUT"
		}
		return subprocess.Result{Stdout: `{"data":{"node":{"views":{"nodes":[{"id":"PVTV_default","name":"View 1","layout":"` + layout + `",` + provisionViewConfiguration(r.visibleFieldsReady, r.phaseVisible) + `}]}}}}`}, nil
	case strings.HasPrefix(joined, "api graphql ") && strings.Contains(joined, "updateProjectV2View") && strings.Contains(joined, "visibleFieldIds"):
		viewID := "PVTV_default"
		if r.createdBoard {
			viewID = "PVTV_runner"
		}
		for _, required := range []string{"view_id=" + viewID, "visible_field_ids[]=F_title", "visible_field_ids[]=F_activity", "visible_field_ids[]=F_qa"} {
			if !strings.Contains(joined, required) {
				return subprocess.Result{}, errors.New("board-visible field update omitted " + required)
			}
		}
		if strings.Contains(joined, "visible_field_ids[]=F_phase") {
			return subprocess.Result{}, errors.New("internal Runner Phase remained board-visible")
		}
		r.visibleFieldsReady = true
		r.phaseVisible = false
		return subprocess.Result{Stdout: `{"data":{"updateProjectV2View":{"projectV2View":{"id":"` + viewID + `"}}}}`}, nil
	case strings.HasPrefix(joined, "api graphql ") && strings.Contains(joined, "updateProjectV2View"):
		if !strings.Contains(joined, "view_id=PVTV_default") || !strings.Contains(joined, "layout:BOARD_LAYOUT") {
			return subprocess.Result{}, errors.New("board update did not target the existing default view")
		}
		r.boardConfigured = true
		return subprocess.Result{Stdout: `{"data":{"updateProjectV2View":{"projectV2View":{"id":"PVTV_default","name":"Board","layout":"BOARD_LAYOUT",` + provisionViewConfiguration(r.visibleFieldsReady, r.phaseVisible) + `}}}}`}, nil
	case strings.HasPrefix(joined, "api graphql ") && strings.Contains(joined, "createProjectV2View"):
		if !strings.Contains(joined, "project_id=PVT_created") || !strings.Contains(joined, "layout:BOARD_LAYOUT") {
			return subprocess.Result{}, errors.New("board creation did not target the new Project")
		}
		r.boardConfigured = true
		r.createdBoard = true
		return subprocess.Result{Stdout: `{"data":{"createProjectV2View":{"projectV2View":{"id":"PVTV_runner","name":"Board","layout":"BOARD_LAYOUT",` + provisionViewConfiguration(r.visibleFieldsReady, r.phaseVisible) + `}}}}`}, nil
	case strings.HasPrefix(joined, "api graphql ") && strings.Contains(joined, "deleteProjectV2View"):
		if !strings.Contains(joined, "view_id=PVTV_default") {
			return subprocess.Result{}, errors.New("view deletion did not target the inherited default view")
		}
		r.defaultViewDeleted = true
		return subprocess.Result{Stdout: `{"data":{"deleteProjectV2View":{"projectV2View":{"id":"PVTV_default"}}}}`}, nil
	case strings.HasPrefix(joined, "api graphql ") && strings.Contains(joined, "archivedStates:[ARCHIVED,NOT_ARCHIVED]"):
		if len(r.statusUsagePages) > 0 {
			index := r.statusUsageCalls
			r.statusUsageCalls++
			if index >= len(r.statusUsagePages) {
				return subprocess.Result{}, errors.New("unexpected extra status usage page")
			}
			return subprocess.Result{Stdout: r.statusUsagePages[index]}, nil
		}
		if r.statusUsageJSON == "" {
			return subprocess.Result{Stdout: `{"data":{"node":{"items":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`}, nil
		}
		return subprocess.Result{Stdout: r.statusUsageJSON}, nil
	case strings.HasPrefix(joined, "api graphql "):
		if r.expectPrune {
			for _, removed := range []string{`name:"Triage"`, `name:"Verification"`} {
				if strings.Contains(joined, removed) {
					return subprocess.Result{}, errors.New("pruned status update retained " + removed)
				}
			}
			for _, preserved := range []string{`id:"O_ready",name:"Ready"`, `id:"O_done",name:"Done"`} {
				if !strings.Contains(joined, preserved) {
					return subprocess.Result{}, errors.New("pruned status update did not preserve " + preserved)
				}
			}
			r.pruned = true
			r.statusConfigured = true
			return subprocess.Result{Stdout: `{"data":{}}`}, nil
		}
		for _, required := range []string{`id:"O_triage",name:"Triage"`, `id:"O_verify",name:"Verification"`, `name:"Needs assessment"`, `name:"Backlog"`, `name:"Plan"`, `name:"Ready"`, `name:"Agent QA"`, `name:"PR Ready"`, `name:"Blocked"`} {
			if !strings.Contains(joined, required) {
				return subprocess.Result{}, errors.New("status update did not preserve or add " + required)
			}
		}
		for _, forbidden := range []string{`id:"O_triage",name:"Ready"`, `id:"O_verify",name:"Agent QA"`} {
			if strings.Contains(joined, forbidden) {
				return subprocess.Result{}, errors.New("status update reused an unrelated option as " + forbidden)
			}
		}
		r.statusConfigured = true
		return subprocess.Result{Stdout: `{"data":{}}`}, nil
	case strings.Contains(joined, "field-create 9"):
		if r.createdFields == nil {
			r.createdFields = map[string]string{}
		}
		r.createdFields[provisionArgValue(args, "--name")] = provisionArgValue(args, "--data-type")
		return subprocess.Result{Stdout: `{"id":"F_created"}`}, nil
	case strings.HasPrefix(joined, "label list "):
		if r.emptyLabelList {
			return subprocess.Result{}, nil
		}
		return subprocess.Result{Stdout: `[]`}, nil
	case strings.HasPrefix(joined, "label create "):
		return subprocess.Result{}, nil
	case strings.HasPrefix(joined, "project link "):
		return subprocess.Result{}, nil
	case strings.HasPrefix(joined, "project edit "):
		return subprocess.Result{}, nil
	default:
		return subprocess.Result{}, errors.New("unexpected gh invocation: " + joined)
	}
}

func provisionViewConfiguration(runnerFields, phaseVisible bool) string {
	nodes := `{"id":"F_title"},{"id":"F_assignees"},{"id":"F_status"}`
	if phaseVisible {
		nodes += `,{"id":"F_phase"}`
	}
	if runnerFields {
		nodes += `,{"id":"F_activity"},{"id":"F_qa"}`
	}
	return `"configuration":{"visibleFields":{"nodes":[` + nodes + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}}`
}

func provisionArgValue(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func provisionLifecycleFields() map[string]string {
	return map[string]string{
		"Runner Result": "TEXT", "Runner Approval": "TEXT", "Runner Phase": "TEXT", "Runner Activity": "TEXT",
		"QA Failures": "NUMBER", "Runner Branch": "TEXT", "Pull Request": "TEXT", "QA Commit": "TEXT",
	}
}

func TestGitHubProjectProvisionerCreatesSelfSufficientProject(t *testing.T) {
	run := &projectProvisionRunner{}
	request := provisionTestRequest("example", "example/runner")
	request.Title = "Runner development"
	request.Visibility = "public"
	created, err := NewProjectProvisioner(run).Create(t.Context(), request)
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	if created.Number != 9 || created.ID != "PVT_created" || !run.statusConfigured || !run.boardConfigured || !run.visibleFieldsReady || !run.defaultViewDeleted {
		t.Fatalf("unexpected provision result %#v", created)
	}
	joined := strings.Join(run.calls, "\n")
	for _, required := range []string{
		"createProjectV2View",
		"deleteProjectV2View",
		"visibleFieldIds",
		"visible_field_ids[]=F_activity",
		"visible_field_ids[]=F_qa",
		`name:"Needs assessment"`,
		`name:"Backlog"`,
		`name:"Plan"`,
		`name:"Ready"`,
		`name:"Agent QA"`,
		`name:"PR Ready"`,
		`name:"Blocked"`,
		"--name Runner Result --data-type TEXT",
		"--name Runner Approval --data-type TEXT",
		"--name Runner Phase --data-type TEXT",
		"--name Runner Activity --data-type TEXT",
		"--name QA Failures --data-type NUMBER",
		"--name Runner Branch --data-type TEXT",
		"--name Pull Request --data-type TEXT",
		"--name QA Commit --data-type TEXT",
		"label create needs-assessment --repo example/runner",
		"project link 9 --owner example --repo example/runner",
		"project edit 9 --owner example --visibility PUBLIC",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing provisioning call %q:\n%s", required, joined)
		}
	}
}

func TestGitHubProjectProvisionerPreservesViewsWhenAdoptingProject(t *testing.T) {
	run := &projectProvisionRunner{phaseVisible: true}
	if err := NewProjectProvisioner(run).Configure(t.Context(), 9, provisionTestRequest("example", "example/runner")); err != nil {
		t.Fatalf("configure existing Project: %v", err)
	}
	joined := strings.Join(run.calls, "\n")
	if strings.Contains(joined, "deleteProjectV2View") || strings.Contains(joined, "createProjectV2View") {
		t.Fatalf("adopted Project views were replaced:\n%s", joined)
	}
	if !strings.Contains(joined, "updateProjectV2View") {
		t.Fatalf("adopted Project did not reuse its existing view:\n%s", joined)
	}
	if !run.visibleFieldsReady || !strings.Contains(joined, "visible_field_ids[]=F_title") {
		t.Fatalf("adopted Project did not preserve visible fields and append Runner lifecycle fields:\n%s", joined)
	}
}

func TestGitHubProjectProvisionerLeavesConfiguredBoardFieldsUnchanged(t *testing.T) {
	run := &projectProvisionRunner{
		statusConfigured: true, boardConfigured: true, visibleFieldsReady: true,
		createdFields: provisionLifecycleFields(),
	}
	if err := NewProjectProvisioner(run).Configure(t.Context(), 9, provisionTestRequest("example", "example/runner")); err != nil {
		t.Fatalf("configure ready Project: %v", err)
	}
	if strings.Contains(strings.Join(run.calls, "\n"), "updateProjectV2View") {
		t.Fatalf("ready board configuration was mutated: %#v", run.calls)
	}
}

func TestGitHubProjectProvisionerRejectsWrongLifecycleFieldType(t *testing.T) {
	for _, test := range []struct {
		field, dataType string
	}{{"Runner Activity", "NUMBER"}, {"QA Failures", "TEXT"}} {
		t.Run(test.field, func(t *testing.T) {
			fields := provisionLifecycleFields()
			fields[test.field] = test.dataType
			run := &projectProvisionRunner{statusConfigured: true, boardConfigured: true, createdFields: fields}
			err := NewProjectProvisioner(run).Configure(t.Context(), 9, provisionTestRequest("example", "example/runner"))
			if err == nil || !strings.Contains(err.Error(), `Project field "`+test.field+`" exists but has an incompatible type`) {
				t.Fatalf("wrong %s type was not rejected clearly: %v", test.field, err)
			}
			if strings.Contains(strings.Join(run.calls, "\n"), "updateProjectV2View") {
				t.Fatalf("board was mutated after incompatible field detection: %#v", run.calls)
			}
		})
	}
}

func TestGitHubProjectProvisionerSkipsUnsupportedCrossOwnerRepositoryLink(t *testing.T) {
	run := &projectProvisionRunner{}
	if err := NewProjectProvisioner(run).Configure(t.Context(), 9, provisionTestRequest("person", "organization/runner")); err != nil {
		t.Fatalf("configure cross-owner Project: %v", err)
	}
	for _, call := range run.calls {
		if strings.HasPrefix(call, "project link ") {
			t.Fatalf("cross-owner Project attempted unsupported repository link: %s", call)
		}
	}
}

func TestGitHubProjectProvisionerTreatsSuccessfulEmptyLabelSearchAsNoMatches(t *testing.T) {
	run := &projectProvisionRunner{emptyLabelList: true}
	if err := NewProjectProvisioner(run).ensureIntakeLabel(t.Context(), "example/runner", "needs-assessment"); err != nil {
		t.Fatalf("ensure label after empty successful search: %v", err)
	}
	if !strings.Contains(strings.Join(run.calls, "\n"), "label create needs-assessment --repo example/runner") {
		t.Fatalf("missing label creation after empty search response: %#v", run.calls)
	}
}

func TestGitHubProjectInspectionTreatsSuccessfulEmptyLabelSearchAsNoMatches(t *testing.T) {
	run := &projectProvisionRunner{emptyLabelList: true}
	project := NewProject(config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{
		IntakeRepository: "example/runner",
		IntakeLabel:      "needs-assessment",
	}}, run)
	repositoryReady, labelReady, err := project.inspectIntake(t.Context())
	if err != nil {
		t.Fatalf("inspect intake after empty successful search: %v", err)
	}
	if !repositoryReady || labelReady {
		t.Fatalf("unexpected intake readiness: repository=%t label=%t", repositoryReady, labelReady)
	}
}

func TestGitHubProjectProvisionerPrunesOnlyStatusesUnusedByActiveAndArchivedItems(t *testing.T) {
	run := &projectProvisionRunner{statusConfigured: true, expectPrune: true}
	request := provisionTestRequest("example", "example/runner")
	request.Prune = true
	plan, err := NewProjectProvisioner(run).PlanConfigure(t.Context(), 9, request)
	if err != nil {
		t.Fatalf("plan empty status pruning: %v", err)
	}
	if len(plan.ExtraStatuses) != 2 || plan.ExtraStatuses[0].Name != "Triage" || plan.ExtraStatuses[1].Name != "Verification" {
		t.Fatalf("unexpected prune plan: %#v", plan.ExtraStatuses)
	}
	if run.pruned {
		t.Fatal("prune planning mutated the Status options")
	}
	if err := NewProjectProvisioner(run).Configure(t.Context(), 9, request); err != nil {
		t.Fatalf("apply empty status pruning: %v", err)
	}
	if !run.pruned {
		t.Fatal("empty extra statuses were not pruned")
	}
}

func TestGitHubProjectProvisionerRefusesToPruneOccupiedActiveOrArchivedStatuses(t *testing.T) {
	run := &projectProvisionRunner{
		statusConfigured: true,
		expectPrune:      true,
		statusUsageJSON: `{"data":{"node":{"items":{"nodes":[` +
			`{"isArchived":false,"fieldValueByName":{"name":"Triage"}},` +
			`{"isArchived":true,"fieldValueByName":{"name":"Verification"}}` +
			`],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
	}
	request := provisionTestRequest("example", "example/runner")
	request.Prune = true
	plan, err := NewProjectProvisioner(run).PlanConfigure(t.Context(), 9, request)
	if err != nil {
		t.Fatalf("plan occupied status pruning: %v", err)
	}
	if len(plan.ExtraStatuses) != 2 || plan.ExtraStatuses[0].Active != 1 || plan.ExtraStatuses[1].Archived != 1 {
		t.Fatalf("occupied status usage was not reported: %#v", plan.ExtraStatuses)
	}
	err = NewProjectProvisioner(run).Configure(t.Context(), 9, request)
	if err == nil || !strings.Contains(err.Error(), "Triage (1 active, 0 archived)") || !strings.Contains(err.Error(), "Verification (0 active, 1 archived)") {
		t.Fatalf("occupied statuses were not rejected clearly: %v", err)
	}
	if run.pruned {
		t.Fatal("occupied statuses were pruned")
	}
}

func TestGitHubProjectProvisionerChecksEveryStatusUsagePageBeforePruning(t *testing.T) {
	run := &projectProvisionRunner{
		statusConfigured: true,
		statusUsagePages: []string{
			`{"data":{"node":{"items":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"next-page"}}}}}`,
			`{"data":{"node":{"items":{"nodes":[{"isArchived":false,"fieldValueByName":{"name":"Triage"}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`,
		},
	}
	request := provisionTestRequest("example", "example/runner")
	request.Prune = true
	plan, err := NewProjectProvisioner(run).PlanConfigure(t.Context(), 9, request)
	if err != nil {
		t.Fatalf("plan paginated status pruning: %v", err)
	}
	if len(plan.ExtraStatuses) != 2 || plan.ExtraStatuses[0].Active != 1 || run.statusUsageCalls != 2 {
		t.Fatalf("later status usage page was missed: plan=%#v calls=%d", plan.ExtraStatuses, run.statusUsageCalls)
	}
	foundCursor := false
	for _, call := range run.calls {
		foundCursor = foundCursor || strings.Contains(call, "after=next-page")
	}
	if !foundCursor {
		t.Fatalf("status usage pagination did not pass its cursor: %#v", run.calls)
	}
}

func provisionTestRequest(owner, repository string) ProvisionRequest {
	workflow := config.WorkflowTemplate(true)
	statuses := make([]string, 0, len(workflow.Lanes))
	for _, lane := range workflow.Lanes {
		statuses = append(statuses, lane.Name)
	}
	return ProvisionRequest{
		Owner: owner, Repository: repository, Statuses: statuses,
		ResultField: "Runner Result", ApprovalField: "Runner Approval", PhaseField: "Runner Phase",
		QAFailuresField: "QA Failures", BranchField: "Runner Branch", PullRequestField: "Pull Request",
		QACommitField: "QA Commit", IntakeLabel: "needs-assessment",
	}
}
