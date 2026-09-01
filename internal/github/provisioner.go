package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type ProvisionRequest struct {
	Owner            string
	Title            string
	Repository       string
	Visibility       string
	Statuses         []string
	ResultField      string
	ApprovalField    string
	PhaseField       string
	TransitionField  string
	ActivityField    string
	QAFailuresField  string
	BranchField      string
	PullRequestField string
	QACommitField    string
	IntakeLabel      string
	Prune            bool
}

type StatusOptionUsage struct {
	Name     string `json:"name"`
	Active   int    `json:"active"`
	Archived int    `json:"archived"`
}

type ConfigurePlan struct {
	Inspection    ProjectInspection   `json:"inspection"`
	ExtraStatuses []StatusOptionUsage `json:"extra_statuses,omitempty"`
}

type ProvisionResult struct {
	Owner  string `json:"owner"`
	Number int    `json:"number"`
	ID     string `json:"id"`
	URL    string `json:"url,omitempty"`
}

type ProjectProvisioner struct {
	run subprocess.Runner
}

func NewProjectProvisioner(run subprocess.Runner) *ProjectProvisioner {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	return &ProjectProvisioner{run: run}
}

// Preflight performs every read-only GitHub prerequisite check required before
// init is allowed to create or reconfigure a Project.
func (p *ProjectProvisioner) Preflight(ctx context.Context, request ProvisionRequest, creating bool) error {
	request = normalizeProvisionRequest(request)
	if err := validateProvisionRequest(request, creating); err != nil {
		return err
	}
	result, err := p.gh(ctx, "auth", "status", "--hostname", "github.com")
	if err != nil {
		return fmt.Errorf("verify GitHub CLI authentication: %w", commandFailure(err, result))
	}
	result, err = p.gh(ctx, "project", "list", "--owner", request.Owner, "--limit", "1", "--format", "json")
	if err != nil {
		return fmt.Errorf("verify GitHub Project access for %s: %w", request.Owner, commandFailure(err, result))
	}
	if request.Repository != "" {
		if err := p.validateRepository(ctx, request.Repository); err != nil {
			return err
		}
	}
	return nil
}

func (p *ProjectProvisioner) Create(ctx context.Context, request ProvisionRequest) (ProvisionResult, error) {
	request = normalizeProvisionRequest(request)
	if err := validateProvisionRequest(request, true); err != nil {
		return ProvisionResult{}, err
	}
	if request.Repository != "" {
		if err := p.validateRepository(ctx, request.Repository); err != nil {
			return ProvisionResult{}, err
		}
	}
	result, err := p.gh(ctx, "project", "create", "--owner", request.Owner, "--title", request.Title, "--format", "json")
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("create GitHub Project: %w", commandFailure(err, result))
	}
	var payload struct {
		Number int    `json:"number"`
		ID     string `json:"id"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return ProvisionResult{}, fmt.Errorf("decode created GitHub Project: %w", err)
	}
	created := ProvisionResult{Owner: request.Owner, Number: payload.Number, ID: payload.ID, URL: payload.URL}
	if created.Number <= 0 || strings.TrimSpace(created.ID) == "" {
		return ProvisionResult{}, errors.New("GitHub Project creation did not return an id and number")
	}
	if err := p.configure(ctx, created.Number, request, true); err != nil {
		return created, fmt.Errorf("Project %d was created but setup is incomplete: %w; rerun init with --project-number %d to resume this Project instead of creating another one", created.Number, err, created.Number)
	}
	return created, nil
}

func (p *ProjectProvisioner) Configure(ctx context.Context, number int, request ProvisionRequest) error {
	return p.configure(ctx, number, request, false)
}

func (p *ProjectProvisioner) configure(ctx context.Context, number int, request ProvisionRequest, freshProject bool) error {
	request = normalizeProvisionRequest(request)
	if number <= 0 {
		return errors.New("project number must be positive")
	}
	if err := validateProvisionRequest(request, false); err != nil {
		return err
	}
	if request.Repository != "" {
		if err := p.validateRepository(ctx, request.Repository); err != nil {
			return err
		}
	}
	source := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{
			Owner: request.Owner, Number: number, ResultField: request.ResultField, ApprovalField: request.ApprovalField,
			PhaseField: request.PhaseField, TransitionField: request.TransitionField, QAFailuresField: request.QAFailuresField, BranchField: request.BranchField, PullRequestField: request.PullRequestField, QACommitField: request.QACommitField,
		},
		ActivityField:    request.ActivityField,
		RequiredStatuses: request.Statuses,
	}, p.run)
	schema, err := source.loadSchema(ctx)
	if err != nil {
		return err
	}
	status, ok := schema.field("Status")
	if !ok || status.Type != "ProjectV2SingleSelectField" {
		return errors.New("GitHub Project requires a single-select Status field")
	}
	if result, exists := schema.field(request.ResultField); exists && !projectFieldHasDataType(result, "TEXT") {
		return fmt.Errorf("Project field %q exists but is not text", request.ResultField)
	}
	if approval, exists := schema.field(request.ApprovalField); exists && !projectFieldHasDataType(approval, "TEXT") {
		return fmt.Errorf("Project field %q exists but is not text", request.ApprovalField)
	}
	for _, name := range []string{request.PhaseField, request.TransitionField, request.ActivityField, request.BranchField, request.PullRequestField, request.QACommitField} {
		if field, exists := schema.field(name); exists && !projectFieldHasDataType(field, "TEXT") {
			return fmt.Errorf("Project field %q exists but has an incompatible type", name)
		}
	}
	if field, exists := schema.field(request.QAFailuresField); exists && !projectFieldHasDataType(field, "NUMBER") {
		return fmt.Errorf("Project field %q exists but has an incompatible type", request.QAFailuresField)
	}
	statuses := request.Statuses
	updateStatuses := missingOptions(status, statuses)
	if request.Prune {
		prunePlan, err := p.statusPrunePlan(ctx, schema.ProjectID, status, statuses)
		if err != nil {
			return err
		}
		occupied := occupiedStatusOptions(prunePlan)
		if len(occupied) > 0 {
			return fmt.Errorf("cannot prune occupied Project statuses: %s", formatStatusUsage(occupied))
		}
		updateStatuses = updateStatuses || len(prunePlan) > 0
	}
	if updateStatuses {
		if err := p.updateSingleSelectField(ctx, status, statuses, !request.Prune); err != nil {
			return fmt.Errorf("configure Project Status options: %w", err)
		}
		schema, err = source.loadSchema(ctx)
		if err != nil {
			return err
		}
	}
	if err := p.ensureTextField(ctx, number, request.Owner, request.ResultField, schema); err != nil {
		return err
	}
	if err := p.ensureTextField(ctx, number, request.Owner, request.ApprovalField, schema); err != nil {
		return err
	}
	if err := p.ensureTextField(ctx, number, request.Owner, request.PhaseField, schema); err != nil {
		return err
	}
	if err := p.ensureTextField(ctx, number, request.Owner, request.TransitionField, schema); err != nil {
		return err
	}
	if err := p.ensureTextField(ctx, number, request.Owner, request.ActivityField, schema); err != nil {
		return err
	}
	if err := p.ensureNumberField(ctx, number, request.Owner, request.QAFailuresField, schema); err != nil {
		return err
	}
	if err := p.ensureTextField(ctx, number, request.Owner, request.BranchField, schema); err != nil {
		return err
	}
	if err := p.ensureTextField(ctx, number, request.Owner, request.PullRequestField, schema); err != nil {
		return err
	}
	if err := p.ensureTextField(ctx, number, request.Owner, request.QACommitField, schema); err != nil {
		return err
	}
	schema, err = source.loadSchema(ctx)
	if err != nil {
		return err
	}
	phase, phaseOK := schema.field(request.PhaseField)
	transition, transitionOK := schema.field(request.TransitionField)
	activity, activityOK := schema.field(request.ActivityField)
	qaFailures, qaFailuresOK := schema.field(request.QAFailuresField)
	if !phaseOK || !projectFieldHasDataType(phase, "TEXT") || !transitionOK || !projectFieldHasDataType(transition, "TEXT") || !activityOK || !projectFieldHasDataType(activity, "TEXT") || !qaFailuresOK || !projectFieldHasDataType(qaFailures, "NUMBER") {
		return errors.New("Runner Phase, Runner Transition, Runner Activity, and QA Failures fields are not ready for board configuration")
	}
	var board githubProjectView
	if freshProject {
		board, err = p.replaceDefaultViewsWithBoard(ctx, source, schema.ProjectID)
	} else {
		board, err = p.ensureBoardView(ctx, source, schema.ProjectID)
	}
	if err != nil {
		return err
	}
	if err := p.ensureBoardLifecycleFields(ctx, board, []string{phase.ID, transition.ID}, activity.ID, qaFailures.ID); err != nil {
		return err
	}
	if request.Repository != "" {
		if err := p.ensureIntakeLabel(ctx, request.Repository, request.IntakeLabel); err != nil {
			return err
		}
		repositoryOwner, _, _ := strings.Cut(request.Repository, "/")
		if strings.EqualFold(request.Owner, repositoryOwner) {
			result, err := p.gh(ctx, "project", "link", strconv.Itoa(number), "--owner", request.Owner, "--repo", request.Repository)
			if err != nil && !strings.Contains(strings.ToLower(result.Stderr+result.Stdout), "already") {
				return fmt.Errorf("link GitHub Project to repository: %w", commandFailure(err, result))
			}
		}
	}
	if request.Visibility != "" {
		result, err := p.gh(ctx, "project", "edit", strconv.Itoa(number), "--owner", request.Owner, "--visibility", request.Visibility)
		if err != nil {
			return fmt.Errorf("set GitHub Project visibility: %w", commandFailure(err, result))
		}
	}
	return nil
}

func (p *ProjectProvisioner) PlanConfigure(ctx context.Context, number int, request ProvisionRequest) (ConfigurePlan, error) {
	request = normalizeProvisionRequest(request)
	if number <= 0 {
		return ConfigurePlan{}, errors.New("project number must be positive")
	}
	if err := validateProvisionRequest(request, false); err != nil {
		return ConfigurePlan{}, err
	}
	source := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{
			Owner: request.Owner, Number: number, IntakeRepository: request.Repository, IntakeLabel: request.IntakeLabel,
			ResultField: request.ResultField, ApprovalField: request.ApprovalField, PhaseField: request.PhaseField, TransitionField: request.TransitionField,
			QAFailuresField: request.QAFailuresField, BranchField: request.BranchField, PullRequestField: request.PullRequestField, QACommitField: request.QACommitField,
		},
		ActivityField:    request.ActivityField,
		RequiredStatuses: request.Statuses,
	}, p.run)
	inspection, err := source.Inspect(ctx)
	if err != nil {
		return ConfigurePlan{}, err
	}
	plan := ConfigurePlan{Inspection: inspection}
	if !request.Prune {
		return plan, nil
	}
	status, ok := source.currentSchema().field("Status")
	if !ok || status.Type != "ProjectV2SingleSelectField" {
		return ConfigurePlan{}, errors.New("GitHub Project requires a single-select Status field")
	}
	plan.ExtraStatuses, err = p.statusPrunePlan(ctx, inspection.ProjectID, status, request.Statuses)
	if err != nil {
		return ConfigurePlan{}, err
	}
	return plan, nil
}

func (p *ProjectProvisioner) ensureBoardView(ctx context.Context, source *Project, projectID string) (githubProjectView, error) {
	views, err := source.loadViews(ctx, projectID)
	if err != nil {
		return githubProjectView{}, err
	}
	for _, view := range views {
		if strings.EqualFold(strings.TrimSpace(view.Layout), "BOARD_LAYOUT") {
			return view, nil
		}
	}
	if len(views) == 0 {
		return p.createBoardView(ctx, projectID)
	}
	query := `mutation($view_id:ID!){updateProjectV2View(input:{viewId:$view_id,name:"Board",layout:BOARD_LAYOUT}){projectV2View{` + projectViewSelection + `}}}`
	result, err := p.gh(ctx, "api", "graphql", "-f", "query="+query, "-F", "view_id="+strings.TrimSpace(views[0].ID))
	if err != nil {
		return githubProjectView{}, fmt.Errorf("configure GitHub Project board view: %w", commandFailure(err, result))
	}
	var payload struct {
		Data struct {
			Update struct {
				View githubProjectView `json:"projectV2View"`
			} `json:"updateProjectV2View"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return githubProjectView{}, fmt.Errorf("decode configured GitHub Project board view: %w", err)
	}
	if strings.TrimSpace(payload.Data.Update.View.ID) == "" {
		return githubProjectView{}, errors.New("GitHub Project board update returned no view id")
	}
	return payload.Data.Update.View, nil
}

// replaceDefaultViewsWithBoard removes view-level settings inherited from
// GitHub's newly-created Project surface, including board column limits that
// the public API cannot inspect or clear. It is called only for a Project that
// this provisioner just created; adopted Projects keep their existing views.
func (p *ProjectProvisioner) replaceDefaultViewsWithBoard(ctx context.Context, source *Project, projectID string) (githubProjectView, error) {
	views, err := source.loadViews(ctx, projectID)
	if err != nil {
		return githubProjectView{}, err
	}
	board, err := p.createBoardView(ctx, projectID)
	if err != nil {
		return githubProjectView{}, err
	}
	for _, view := range views {
		viewID := strings.TrimSpace(view.ID)
		if viewID == "" || viewID == board.ID {
			continue
		}
		query := `mutation($view_id:ID!){deleteProjectV2View(input:{viewId:$view_id}){projectV2View{id}}}`
		result, deleteErr := p.gh(ctx, "api", "graphql", "-f", "query="+query, "-F", "view_id="+viewID)
		if deleteErr != nil {
			return githubProjectView{}, fmt.Errorf("remove default GitHub Project view %q: %w", strings.TrimSpace(view.Name), commandFailure(deleteErr, result))
		}
	}
	return board, nil
}

func (p *ProjectProvisioner) createBoardView(ctx context.Context, projectID string) (githubProjectView, error) {
	query := `mutation($project_id:ID!){createProjectV2View(input:{projectId:$project_id,name:"Board",layout:BOARD_LAYOUT}){projectV2View{` + projectViewSelection + `}}}`
	result, err := p.gh(ctx, "api", "graphql", "-f", "query="+query, "-F", "project_id="+strings.TrimSpace(projectID))
	if err != nil {
		return githubProjectView{}, fmt.Errorf("create GitHub Project board view: %w", commandFailure(err, result))
	}
	var payload struct {
		Data struct {
			Create struct {
				View githubProjectView `json:"projectV2View"`
			} `json:"createProjectV2View"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return githubProjectView{}, fmt.Errorf("decode created GitHub Project board view: %w", err)
	}
	if strings.TrimSpace(payload.Data.Create.View.ID) == "" {
		return githubProjectView{}, errors.New("GitHub Project board creation returned no view id")
	}
	return payload.Data.Create.View, nil
}

func (p *ProjectProvisioner) ensureBoardLifecycleFields(ctx context.Context, view githubProjectView, hiddenFieldIDs []string, requiredFieldIDs ...string) error {
	viewID := strings.TrimSpace(view.ID)
	if viewID == "" {
		return errors.New("GitHub Project board view id is empty")
	}
	if view.Configuration.VisibleFields.PageInfo.HasNextPage {
		return fmt.Errorf("GitHub Project view %q exceeds the supported limit of 100 visible fields", strings.TrimSpace(view.Name))
	}
	visible := make([]string, 0, len(view.Configuration.VisibleFields.Nodes)+len(requiredFieldIDs))
	seen := map[string]bool{}
	hidden := make(map[string]bool, len(hiddenFieldIDs))
	for _, fieldID := range hiddenFieldIDs {
		fieldID = strings.TrimSpace(fieldID)
		if fieldID == "" {
			return errors.New("hidden Runner field id is empty")
		}
		hidden[fieldID] = true
	}
	changed := false
	for _, field := range view.Configuration.VisibleFields.Nodes {
		fieldID := strings.TrimSpace(field.ID)
		if fieldID == "" || seen[fieldID] {
			continue
		}
		if hidden[fieldID] {
			changed = true
			continue
		}
		seen[fieldID] = true
		visible = append(visible, fieldID)
	}
	for _, fieldID := range requiredFieldIDs {
		fieldID = strings.TrimSpace(fieldID)
		if fieldID == "" {
			return errors.New("board-visible Runner field id is empty")
		}
		if seen[fieldID] {
			continue
		}
		seen[fieldID] = true
		visible = append(visible, fieldID)
		changed = true
	}
	if !changed {
		return nil
	}
	query := `mutation($view_id:ID!,$visible_field_ids:[ID!]!){updateProjectV2View(input:{viewId:$view_id,configuration:{visibleFieldIds:$visible_field_ids}}){projectV2View{id}}}`
	args := []string{"api", "graphql", "-f", "query=" + query, "-F", "view_id=" + viewID}
	for _, fieldID := range visible {
		args = append(args, "-F", "visible_field_ids[]="+fieldID)
	}
	result, err := p.gh(ctx, args...)
	if err != nil {
		return fmt.Errorf("show Runner lifecycle fields on GitHub Project board: %w", commandFailure(err, result))
	}
	return nil
}

func (p *ProjectProvisioner) validateRepository(ctx context.Context, repository string) error {
	result, err := p.gh(ctx, "repo", "view", repository, "--json", "nameWithOwner,hasIssuesEnabled")
	if err != nil {
		return fmt.Errorf("inspect intake repository before Project changes: %w", commandFailure(err, result))
	}
	var repo struct {
		NameWithOwner    string `json:"nameWithOwner"`
		HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &repo); err != nil {
		return fmt.Errorf("decode intake repository: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(repo.NameWithOwner), repository) || !repo.HasIssuesEnabled {
		return errors.New("intake repository must exist and have GitHub Issues enabled")
	}
	return nil
}

func (p *ProjectProvisioner) ensureTextField(ctx context.Context, number int, owner, name string, schema githubProjectSchema) error {
	field, exists := schema.field(name)
	if exists {
		if !projectFieldHasDataType(field, "TEXT") {
			return fmt.Errorf("Project field %q exists but is not text", name)
		}
		return nil
	}
	result, err := p.gh(ctx, "project", "field-create", strconv.Itoa(number), "--owner", owner, "--name", name, "--data-type", "TEXT", "--format", "json")
	if err != nil {
		return fmt.Errorf("create Project field %q: %w", name, commandFailure(err, result))
	}
	return nil
}

func (p *ProjectProvisioner) ensureNumberField(ctx context.Context, number int, owner, name string, schema githubProjectSchema) error {
	if field, ok := schema.field(name); ok {
		if !projectFieldHasDataType(field, "NUMBER") {
			return fmt.Errorf("Project field %q exists but is not a number field", name)
		}
		return nil
	}
	result, err := p.gh(ctx, "project", "field-create", strconv.Itoa(number), "--owner", owner, "--name", name, "--data-type", "NUMBER", "--format", "json")
	if err != nil {
		return fmt.Errorf("create Project number field %q: %w", name, commandFailure(err, result))
	}
	return nil
}

func (p *ProjectProvisioner) updateSingleSelectField(ctx context.Context, field githubProjectField, required []string, preserveExtra bool) error {
	ordered := append([]string{}, required...)
	if preserveExtra {
		extra := make([]string, 0, len(field.Options))
		for _, option := range field.Options {
			if !containsNormalized(ordered, option.Name) {
				extra = append(extra, option.Name)
			}
		}
		sort.Strings(extra)
		ordered = append(ordered, extra...)
	}
	var options strings.Builder
	for index, name := range ordered {
		if index > 0 {
			options.WriteByte(',')
		}
		option := field.Options[normalizeProjectKey(name)]
		options.WriteString("{")
		if option.ID != "" {
			options.WriteString("id:")
			options.WriteString(graphQLString(option.ID))
			options.WriteByte(',')
		}
		options.WriteString("name:")
		options.WriteString(graphQLString(name))
		options.WriteString(",color:")
		options.WriteString(projectOptionColor(name))
		options.WriteString(",description:")
		options.WriteString(graphQLString(projectOptionDescription(name)))
		options.WriteString("}")
	}
	query := "mutation { updateProjectV2Field(input:{fieldId:" + graphQLString(field.ID) + ",singleSelectOptions:[" + options.String() + "]}) { projectV2Field { ... on ProjectV2SingleSelectField { id name } } } }"
	result, err := p.gh(ctx, "api", "graphql", "-f", "query="+query)
	if err != nil {
		return commandFailure(err, result)
	}
	return nil
}

func (p *ProjectProvisioner) statusPrunePlan(ctx context.Context, projectID string, field githubProjectField, required []string) ([]StatusOptionUsage, error) {
	extras := make([]StatusOptionUsage, 0)
	byName := map[string]int{}
	for _, option := range field.Options {
		if containsNormalized(required, option.Name) {
			continue
		}
		byName[normalizeProjectKey(option.Name)] = len(extras)
		extras = append(extras, StatusOptionUsage{Name: option.Name})
	}
	if len(extras) == 0 {
		return nil, nil
	}
	sort.Slice(extras, func(i, j int) bool { return strings.ToLower(extras[i].Name) < strings.ToLower(extras[j].Name) })
	byName = map[string]int{}
	for index, option := range extras {
		byName[normalizeProjectKey(option.Name)] = index
	}

	query := `query($project_id:ID!,$after:String){node(id:$project_id){... on ProjectV2{items(first:100,after:$after,archivedStates:[ARCHIVED,NOT_ARCHIVED]){nodes{isArchived fieldValueByName(name:"Status"){... on ProjectV2ItemFieldSingleSelectValue{name}}}pageInfo{hasNextPage endCursor}}}}}`
	after := ""
	total := 0
	pages := newPaginationGuard("Project status usage")
	for {
		if err := pages.startPage(); err != nil {
			return nil, err
		}
		args := []string{"api", "graphql", "-f", "query=" + query, "-F", "project_id=" + strings.TrimSpace(projectID)}
		if after != "" {
			args = append(args, "-F", "after="+after)
		}
		result, err := p.gh(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("inspect active and archived Project status usage: %w", commandFailure(err, result))
		}
		var payload struct {
			Data struct {
				Node struct {
					Items struct {
						Nodes []struct {
							IsArchived bool `json:"isArchived"`
							Status     *struct {
								Name string `json:"name"`
							} `json:"fieldValueByName"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"items"`
				} `json:"node"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
			return nil, fmt.Errorf("decode Project status usage: %w", err)
		}
		for _, item := range payload.Data.Node.Items.Nodes {
			total++
			if total > MaxProjectItems {
				return nil, fmt.Errorf("Project exceeds the supported prune inspection limit of %d active and archived items", MaxProjectItems)
			}
			if item.Status == nil {
				continue
			}
			index, ok := byName[normalizeProjectKey(item.Status.Name)]
			if !ok {
				continue
			}
			if item.IsArchived {
				extras[index].Archived++
			} else {
				extras[index].Active++
			}
		}
		if !payload.Data.Node.Items.PageInfo.HasNextPage {
			break
		}
		after, err = pages.advance(after, payload.Data.Node.Items.PageInfo.EndCursor)
		if err != nil {
			return nil, err
		}
	}
	return extras, nil
}

func occupiedStatusOptions(options []StatusOptionUsage) []StatusOptionUsage {
	occupied := make([]StatusOptionUsage, 0)
	for _, option := range options {
		if option.Active > 0 || option.Archived > 0 {
			occupied = append(occupied, option)
		}
	}
	return occupied
}

func formatStatusUsage(options []StatusOptionUsage) string {
	formatted := make([]string, 0, len(options))
	for _, option := range options {
		formatted = append(formatted, fmt.Sprintf("%s (%d active, %d archived)", option.Name, option.Active, option.Archived))
	}
	return strings.Join(formatted, ", ")
}

func (p *ProjectProvisioner) ensureIntakeLabel(ctx context.Context, repository, label string) error {
	result, err := p.gh(ctx, "label", "list", "--repo", repository, "--search", label, "--limit", "100", "--json", "name")
	if err != nil {
		return fmt.Errorf("inspect assessment label: %w", commandFailure(err, result))
	}
	var labels []struct {
		Name string `json:"name"`
	}
	if output := strings.TrimSpace(result.Stdout); output != "" {
		if err := json.Unmarshal([]byte(output), &labels); err != nil {
			return fmt.Errorf("decode assessment labels: %w", err)
		}
	}
	for _, existing := range labels {
		if strings.EqualFold(strings.TrimSpace(existing.Name), label) {
			return nil
		}
	}
	result, err = p.gh(ctx, "label", "create", label, "--repo", repository, "--description", "Public issue awaiting maintainer assessment", "--color", "D4C5F9")
	if err != nil {
		return fmt.Errorf("create assessment label: %w", commandFailure(err, result))
	}
	return nil
}

func (p *ProjectProvisioner) gh(ctx context.Context, args ...string) (subprocess.Result, error) {
	return subprocess.RunGitHub(ctx, p.run, args, "", 30*time.Second)
}

func normalizeProvisionRequest(request ProvisionRequest) ProvisionRequest {
	request.Owner = strings.TrimSpace(request.Owner)
	request.Title = strings.TrimSpace(request.Title)
	request.Repository = strings.TrimSpace(request.Repository)
	request.Visibility = strings.ToUpper(strings.TrimSpace(request.Visibility))
	request.ResultField = strings.TrimSpace(request.ResultField)
	request.ApprovalField = strings.TrimSpace(request.ApprovalField)
	request.PhaseField = strings.TrimSpace(request.PhaseField)
	request.TransitionField = strings.TrimSpace(request.TransitionField)
	if request.TransitionField == "" {
		request.TransitionField = config.RunnerTransitionFieldName
	}
	request.ActivityField = strings.TrimSpace(request.ActivityField)
	if request.ActivityField == "" {
		request.ActivityField = config.RunnerActivityFieldName
	}
	request.QAFailuresField = strings.TrimSpace(request.QAFailuresField)
	request.BranchField = strings.TrimSpace(request.BranchField)
	request.PullRequestField = strings.TrimSpace(request.PullRequestField)
	request.QACommitField = strings.TrimSpace(request.QACommitField)
	request.IntakeLabel = strings.TrimSpace(request.IntakeLabel)
	return request
}

func validateProvisionRequest(request ProvisionRequest, requireTitle bool) error {
	if request.Owner == "" {
		return errors.New("GitHub Project owner is required")
	}
	if requireTitle && request.Title == "" {
		return errors.New("GitHub Project title is required")
	}
	if request.Repository != "" && !config.ValidRepositoryName(request.Repository) {
		return errors.New("repository must use owner/repository format")
	}
	if request.Visibility != "" && request.Visibility != "PUBLIC" && request.Visibility != "PRIVATE" {
		return errors.New("Project visibility must be public or private")
	}
	statuses := request.Statuses
	if len(statuses) == 0 {
		return errors.New("Project statuses are required")
	}
	seen := map[string]bool{}
	for _, status := range statuses {
		key := normalizeProjectKey(status)
		if key == "" || seen[key] {
			return errors.New("Project status names must be non-empty and distinct")
		}
		seen[key] = true
	}
	fields := []string{request.ResultField, request.ApprovalField, request.PhaseField, request.TransitionField, request.ActivityField, request.QAFailuresField, request.BranchField, request.PullRequestField, request.QACommitField}
	seenFields := map[string]bool{}
	for _, field := range fields {
		key := normalizeProjectKey(field)
		if key == "" || seenFields[key] {
			return errors.New("Project field names must be non-empty and distinct")
		}
		seenFields[key] = true
	}
	if request.Repository != "" && request.IntakeLabel == "" {
		return errors.New("assessment label is required when a repository is configured")
	}
	return nil
}

func missingOptions(field githubProjectField, required []string) bool {
	for _, name := range required {
		if !field.hasOption(name) {
			return true
		}
	}
	return false
}

func containsNormalized(values []string, wanted string) bool {
	for _, value := range values {
		if normalizeProjectKey(value) == normalizeProjectKey(wanted) {
			return true
		}
	}
	return false
}

func graphQLString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func projectOptionColor(name string) string {
	switch normalizeProjectKey(name) {
	case "needsassessment":
		return "GRAY"
	case "backlog", "ready", "planner":
		return "BLUE"
	case "plan":
		return "BLUE"
	case "inprogress", "implementer":
		return "YELLOW"
	case "agentqa", "prready":
		return "PURPLE"
	case "blocked":
		return "RED"
	case "done", "reviewer":
		return "GREEN"
	default:
		return "PURPLE"
	}
}

func projectOptionDescription(name string) string {
	switch normalizeProjectKey(name) {
	case "needsassessment":
		return "Awaiting maintainer assessment; the Runner will not execute this item."
	case "backlog":
		return "Approved and retained for future scheduling; this lane is not executable."
	case "plan":
		return "Approved work awaiting or undergoing agent planning."
	case "ready":
		return "Approved and ready for the Runner."
	case "inprogress":
		return "Claimed by the Runner."
	case "agentqa":
		return "Implementation is awaiting agent QA."
	case "prready":
		return "Agent QA passed and the pull request awaits a human decision."
	case "blocked":
		return "The attempt failed, needs input, or cannot safely continue."
	case "done":
		return "Reviewed and accepted."
	default:
		return name
	}
}
