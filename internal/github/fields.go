package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const maxProjectTextFieldBytes = 1000

func canonicalProjectResult(value string) string {
	return truncate(strings.TrimSpace(value), maxProjectTextFieldBytes)
}

func (s *Project) ListItems(ctx context.Context) ([]WorkItem, error) {
	schema, err := s.ensureSchema(ctx)
	if err != nil {
		return nil, err
	}
	query := s.lifecycleItemsQuery()
	items := make([]WorkItem, 0)
	inspected := 0
	after := ""
	pages := newPaginationGuard("GitHub Project item")
	for {
		if err := pages.startPage(); err != nil {
			return nil, err
		}
		args := []string{"api", "graphql", "-f", "query=" + query, "-F", "project_id=" + schema.ProjectID}
		if after != "" {
			args = append(args, "-F", "after="+after)
		}
		result, err := s.gh(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("list GitHub Project items: %w", commandFailure(err, result))
		}
		var payload projectItemsPayload
		if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
			return nil, fmt.Errorf("decode GitHub Project items: %w", err)
		}
		page := payload.Data.Node.Items
		if len(page.Nodes) > MaxProjectItems-inspected || len(page.Nodes) == MaxProjectItems-inspected && page.PageInfo.HasNextPage {
			return nil, fmt.Errorf("GitHub Project exceeds the supported limit of %d items; archive completed items or partition the workflow before running", MaxProjectItems)
		}
		inspected += len(page.Nodes)
		for _, raw := range page.Nodes {
			item := decodeProjectItemNode(raw)
			if item.ID == "" || item.Title == "" {
				continue
			}
			items = append(items, item)
		}
		if !page.PageInfo.HasNextPage {
			return items, nil
		}
		after, err = pages.advance(after, page.PageInfo.EndCursor)
		if err != nil {
			return nil, err
		}
	}
}

// itemByID reads a newly created Project item through GitHub's node lookup.
// Project item connections can lag behind item-create, while the returned node
// identity is immediately addressable by its global ID.
func (s *Project) itemByID(ctx context.Context, itemID string) (WorkItem, error) {
	if _, err := s.ensureSchema(ctx); err != nil {
		return WorkItem{}, err
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return WorkItem{}, errors.New("GitHub Project item id is empty")
	}
	query := `query($item_id:ID!){node(id:$item_id){... on ProjectV2Item{` + s.lifecycleItemSelection() + `}}}`
	result, err := s.gh(ctx, "api", "graphql", "-f", "query="+query, "-F", "item_id="+itemID)
	if err != nil {
		return WorkItem{}, fmt.Errorf("load GitHub Project item %q: %w", itemID, commandFailure(err, result))
	}
	var payload struct {
		Data struct {
			Node *projectItemNode `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return WorkItem{}, fmt.Errorf("decode GitHub Project item %q: %w", itemID, err)
	}
	if payload.Data.Node == nil {
		return WorkItem{}, fmt.Errorf("GitHub Project item %q was not found", itemID)
	}
	item := decodeProjectItemNode(*payload.Data.Node)
	if item.ID != itemID {
		return WorkItem{}, fmt.Errorf("GitHub Project item lookup returned %q instead of %q", item.ID, itemID)
	}
	return item, nil
}

// LifecycleItemsByID reloads exact Project items without depending on the
// eventually consistent Project item connection. The returned order matches
// itemIDs.
func (s *Project) LifecycleItemsByID(ctx context.Context, itemIDs []string) ([]WorkItem, error) {
	items := make([]WorkItem, len(itemIDs))
	seen := make(map[string]struct{}, len(itemIDs))
	for index, itemID := range itemIDs {
		itemID = strings.TrimSpace(itemID)
		if _, exists := seen[itemID]; exists {
			return nil, fmt.Errorf("GitHub Project item id %q is duplicated", itemID)
		}
		seen[itemID] = struct{}{}
		item, err := s.itemByID(ctx, itemID)
		if err != nil {
			return nil, err
		}
		items[index] = item
	}
	return items, nil
}

type projectItemsPayload struct {
	Data struct {
		Node struct {
			Items struct {
				Nodes    []projectItemNode `json:"nodes"`
				PageInfo projectPageInfo   `json:"pageInfo"`
			} `json:"items"`
		} `json:"node"`
	} `json:"data"`
}

type projectPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type projectItemNode struct {
	ID     string `json:"id"`
	Status *struct {
		Name string `json:"name"`
	} `json:"status"`
	Approval *struct {
		Text string `json:"text"`
	} `json:"approval"`
	Result *struct {
		Text string `json:"text"`
	} `json:"result"`
	Phase *struct {
		Text string `json:"text"`
	} `json:"phase"`
	Transition *struct {
		Text string `json:"text"`
	} `json:"transition"`
	Activity *struct {
		Text string `json:"text"`
	} `json:"activity"`
	QAFailures *struct {
		Number float64 `json:"number"`
	} `json:"qaFailures"`
	Branch *struct {
		Text string `json:"text"`
	} `json:"branch"`
	PullRequest *struct {
		Text string `json:"text"`
	} `json:"pullRequest"`
	QACommit *struct {
		Text string `json:"text"`
	} `json:"qaCommit"`
	Content *struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Body       string `json:"body"`
		URL        string `json:"url"`
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	} `json:"content"`
}

func (s *Project) lifecycleItemsQuery() string {
	return `query($project_id:ID!,$after:String){node(id:$project_id){... on ProjectV2{items(first:100,after:$after,archivedStates:[NOT_ARCHIVED]){nodes{` +
		s.lifecycleItemSelection() +
		`}pageInfo{hasNextPage endCursor}}}}}`
}

func (s *Project) lifecycleItemSelection() string {
	return `id ` +
		`status:fieldValueByName(name:` + graphQLString(s.statusFieldName()) + `){... on ProjectV2ItemFieldSingleSelectValue{name}} ` +
		`approval:fieldValueByName(name:` + graphQLString(s.approvalFieldName()) + `){... on ProjectV2ItemFieldTextValue{text}} ` +
		`result:fieldValueByName(name:` + graphQLString(s.resultFieldName()) + `){... on ProjectV2ItemFieldTextValue{text}} ` +
		`phase:fieldValueByName(name:` + graphQLString(s.phaseFieldName()) + `){... on ProjectV2ItemFieldTextValue{text}} ` +
		`transition:fieldValueByName(name:` + graphQLString(s.transitionFieldName()) + `){... on ProjectV2ItemFieldTextValue{text}} ` +
		`activity:fieldValueByName(name:` + graphQLString(s.activityFieldName()) + `){... on ProjectV2ItemFieldTextValue{text}} ` +
		`qaFailures:fieldValueByName(name:` + graphQLString(s.qaFailuresFieldName()) + `){... on ProjectV2ItemFieldNumberValue{number}} ` +
		`branch:fieldValueByName(name:` + graphQLString(s.branchFieldName()) + `){... on ProjectV2ItemFieldTextValue{text}} ` +
		`pullRequest:fieldValueByName(name:` + graphQLString(s.pullRequestFieldName()) + `){... on ProjectV2ItemFieldTextValue{text}} ` +
		`qaCommit:fieldValueByName(name:` + graphQLString(s.qaCommitFieldName()) + `){... on ProjectV2ItemFieldTextValue{text}} ` +
		`content{... on DraftIssue{id title body} ... on Issue{title body url repository{nameWithOwner}} ... on PullRequest{title body url repository{nameWithOwner}}}`
}

func decodeProjectItemNode(raw projectItemNode) WorkItem {
	item := WorkItem{ID: strings.TrimSpace(raw.ID)}
	if raw.Status != nil {
		item.Status = strings.TrimSpace(raw.Status.Name)
	}
	if raw.Approval != nil {
		item.Approval = strings.TrimSpace(raw.Approval.Text)
	}
	if raw.Result != nil {
		item.Result = canonicalProjectResult(raw.Result.Text)
	}
	if raw.Phase != nil {
		item.Phase = strings.TrimSpace(raw.Phase.Text)
	}
	if raw.Transition != nil {
		item.Transition = strings.TrimSpace(raw.Transition.Text)
	}
	if raw.Activity != nil {
		item.Activity = strings.TrimSpace(raw.Activity.Text)
	}
	if raw.QAFailures != nil {
		item.QAFailures = int(raw.QAFailures.Number)
	}
	if raw.Branch != nil {
		item.Branch = strings.TrimSpace(raw.Branch.Text)
	}
	if raw.PullRequest != nil {
		item.PullRequest = strings.TrimSpace(raw.PullRequest.Text)
	}
	if raw.QACommit != nil {
		item.QACommit = strings.TrimSpace(raw.QACommit.Text)
	}
	if raw.Content != nil {
		item.DraftContentID = strings.TrimSpace(raw.Content.ID)
		item.Title = strings.TrimSpace(raw.Content.Title)
		item.Body = strings.TrimSpace(raw.Content.Body)
		item.URL = strings.TrimSpace(raw.Content.URL)
		if raw.Content.Repository != nil {
			item.Repository = strings.TrimSpace(raw.Content.Repository.NameWithOwner)
		}
	}
	metadata, metadataPresent, metadataErr := decodePlannedItemMetadata(item.Body)
	item.PlanningMetadataInvalid = metadataPresent && metadataErr != nil
	if item.Repository == "" {
		item.Repository = metadata.Repository
	}
	item.Dependencies = append([]string{}, metadata.Dependencies...)
	if !metadataPresent {
		manualDependencies, dependenciesPresent, dependenciesErr := decodeManualDependencies(item.Body)
		item.PlanningMetadataInvalid = dependenciesPresent && dependenciesErr != nil
		if dependenciesErr == nil {
			item.Dependencies = append([]string{}, manualDependencies...)
		}
	}
	item.PlanningSourceID = metadata.PlanningSourceID
	item.PlanningSourceLane = metadata.PlanningSourceLane
	item.PlanningSourceFingerprint = metadata.PlanningSourceFingerprint
	item.PlanningDestination = metadata.PlanningDestination
	item.PlanningBatchFingerprint = metadata.PlanningBatchFingerprint
	item.PlanningBatchSize = metadata.PlanningBatchSize
	item.PlanningItemIndex = metadata.PlanningItemIndex
	return item
}

func (s *Project) loadSchema(ctx context.Context) (githubProjectSchema, error) {
	projectResult, err := s.gh(ctx, "project", "view", strconv.Itoa(s.cfg.Number), "--owner", strings.TrimSpace(s.cfg.Owner), "--format", "json")
	if err != nil {
		return githubProjectSchema{}, fmt.Errorf("inspect GitHub Project: %w", commandFailure(err, projectResult))
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(projectResult.Stdout), &project); err != nil || strings.TrimSpace(project.ID) == "" {
		return githubProjectSchema{}, errors.New("GitHub Project view did not return a project id")
	}
	fields, err := s.loadFields(ctx, strings.TrimSpace(project.ID))
	if err != nil {
		return githubProjectSchema{}, err
	}
	schema := githubProjectSchema{ProjectID: strings.TrimSpace(project.ID), Fields: map[string]githubProjectField{}}
	for _, raw := range fields {
		field := githubProjectField{ID: raw.ID, Name: raw.Name, Type: raw.TypeName, DataType: raw.DataType, Options: map[string]githubProjectOption{}}
		for _, option := range raw.Options {
			field.Options[normalizeProjectKey(option.Name)] = githubProjectOption{ID: option.ID, Name: option.Name}
		}
		schema.Fields[normalizeProjectKey(raw.Name)] = field
	}
	s.mu.Lock()
	s.schema = schema
	s.mu.Unlock()
	return schema, nil
}

type projectFieldNode struct {
	TypeName string `json:"__typename"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	Options  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"options"`
}

func (s *Project) loadFields(ctx context.Context, projectID string) ([]projectFieldNode, error) {
	query := `query($project_id:ID!,$after:String){node(id:$project_id){... on ProjectV2{fields(first:100,after:$after){nodes{__typename ... on ProjectV2FieldCommon{id name dataType} ... on ProjectV2SingleSelectField{options{id name}}}pageInfo{hasNextPage endCursor}}}}}`
	fields := make([]projectFieldNode, 0)
	after := ""
	pages := newPaginationGuard("GitHub Project field")
	for {
		if err := pages.startPage(); err != nil {
			return nil, err
		}
		args := []string{"api", "graphql", "-f", "query=" + query, "-F", "project_id=" + projectID}
		if after != "" {
			args = append(args, "-F", "after="+after)
		}
		result, err := s.gh(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("inspect GitHub Project fields: %w", commandFailure(err, result))
		}
		var payload struct {
			Data struct {
				Node struct {
					Fields struct {
						Nodes    []projectFieldNode `json:"nodes"`
						PageInfo projectPageInfo    `json:"pageInfo"`
					} `json:"fields"`
				} `json:"node"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
			return nil, fmt.Errorf("decode GitHub Project fields: %w", err)
		}
		page := payload.Data.Node.Fields
		if len(page.Nodes) > 100-len(fields) || len(page.Nodes) == 100-len(fields) && page.PageInfo.HasNextPage {
			return nil, errors.New("GitHub Project exceeds the supported limit of 100 fields")
		}
		fields = append(fields, page.Nodes...)
		if !page.PageInfo.HasNextPage {
			return fields, nil
		}
		after, err = pages.advance(after, page.PageInfo.EndCursor)
		if err != nil {
			return nil, err
		}
	}
}

func (s *Project) ensureSchema(ctx context.Context) (githubProjectSchema, error) {
	s.mu.Lock()
	cached := s.schema
	s.mu.Unlock()
	if strings.TrimSpace(cached.ProjectID) != "" && len(cached.Fields) > 0 {
		return cached, nil
	}
	return s.loadSchema(ctx)
}

func (s *Project) setStatus(ctx context.Context, itemID, statusName string) error {
	schema := s.currentSchema()
	field, ok := schema.field(s.statusFieldName())
	if !ok {
		return errors.New("GitHub Project has no Status field")
	}
	optionID := field.Options[normalizeProjectKey(statusName)].ID
	if optionID == "" {
		return fmt.Errorf("GitHub Project Status has no %q option", statusName)
	}
	result, err := s.gh(ctx, "project", "item-edit", "--id", itemID, "--project-id", schema.ProjectID, "--field-id", field.ID, "--single-select-option-id", optionID)
	if err != nil {
		return fmt.Errorf("update GitHub Project status: %w", commandFailure(err, result))
	}
	return nil
}

func (s *Project) setResult(ctx context.Context, itemID, summary string) error {
	summary = canonicalProjectResult(summary)
	if summary == "" {
		return nil
	}
	schema := s.currentSchema()
	field, ok := schema.field(s.resultFieldName())
	if !ok || field.Type != "ProjectV2Field" {
		return nil
	}
	result, err := s.gh(ctx, "project", "item-edit", "--id", itemID, "--project-id", schema.ProjectID, "--field-id", field.ID, "--text", summary)
	if err != nil {
		return fmt.Errorf("update GitHub Project result: %w", commandFailure(err, result))
	}
	return nil
}

func (s *Project) setTextField(ctx context.Context, itemID, fieldName, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return s.clearField(ctx, itemID, fieldName)
	}
	schema := s.currentSchema()
	field, ok := schema.field(fieldName)
	if !ok || field.Type != "ProjectV2Field" {
		return fmt.Errorf("GitHub Project has no text field %q", fieldName)
	}
	result, err := s.gh(ctx, "project", "item-edit", "--id", itemID, "--project-id", schema.ProjectID, "--field-id", field.ID, "--text", value)
	if err != nil {
		return fmt.Errorf("update GitHub Project field %q: %w", fieldName, commandFailure(err, result))
	}
	return nil
}

func (s *Project) beginTransition(ctx context.Context, itemID string) error {
	return s.setTextField(ctx, itemID, s.transitionFieldName(), transitionLockValue)
}

func (s *Project) finishTransition(ctx context.Context, itemID string) error {
	return s.clearField(ctx, itemID, s.transitionFieldName())
}

func (s *Project) setNumberField(ctx context.Context, itemID, fieldName string, value int) error {
	schema := s.currentSchema()
	field, ok := schema.field(fieldName)
	if !ok || field.Type != "ProjectV2Field" {
		return fmt.Errorf("GitHub Project has no number field %q", fieldName)
	}
	result, err := s.gh(ctx, "project", "item-edit", "--id", itemID, "--project-id", schema.ProjectID, "--field-id", field.ID, "--number", strconv.Itoa(value))
	if err != nil {
		return fmt.Errorf("update GitHub Project field %q: %w", fieldName, commandFailure(err, result))
	}
	return nil
}

func (s *Project) clearField(ctx context.Context, itemID, fieldName string) error {
	schema := s.currentSchema()
	field, ok := schema.field(fieldName)
	if !ok || field.Type != "ProjectV2Field" {
		return fmt.Errorf("GitHub Project has no field %q", fieldName)
	}
	result, err := s.gh(ctx, "project", "item-edit", "--id", itemID, "--project-id", schema.ProjectID, "--field-id", field.ID, "--clear")
	if err != nil {
		return fmt.Errorf("clear GitHub Project field %q: %w", fieldName, commandFailure(err, result))
	}
	return nil
}

func (s *Project) currentSchema() githubProjectSchema {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schema
}

func (s *Project) gh(ctx context.Context, args ...string) (subprocess.Result, error) {
	if isIntakeMutation(args) {
		if err := intakeMutationBudgetFromContext(ctx).consume(); err != nil {
			return subprocess.Result{ExitCode: -1}, err
		}
	}
	return subprocess.RunGitHub(ctx, s.run, args, "", 30*time.Second)
}

func isIntakeMutation(args []string) bool {
	return len(args) >= 2 && args[0] == "project" && (args[1] == "item-add" || args[1] == "item-edit")
}

func (s *Project) statusFieldName() string { return "Status" }
func (s *Project) assessmentStatus() string {
	return strings.TrimSpace(s.cfg.AssessmentStatus)
}

func (s *Project) backlogStatus() string {
	return strings.TrimSpace(s.cfg.BacklogStatus)
}

func (s *Project) readyStatus() string { return strings.TrimSpace(s.cfg.ReadyStatus) }
func (s *Project) runningStatus() string {
	return strings.TrimSpace(s.cfg.RunningStatus)
}

func (s *Project) qaStatus() string {
	return strings.TrimSpace(s.cfg.QAStatus)
}

func (s *Project) prReadyStatus() string {
	return strings.TrimSpace(s.cfg.PRReadyStatus)
}

func (s *Project) blockedStatus() string {
	return strings.TrimSpace(s.cfg.BlockedStatus)
}

func (s *Project) doneStatus() string { return strings.TrimSpace(s.cfg.DoneStatus) }
func (s *Project) intakeLabel() string {
	return strings.TrimSpace(s.cfg.IntakeLabel)
}
func (s *Project) resultFieldName() string {
	return strings.TrimSpace(s.cfg.ResultField)
}
func (s *Project) approvalFieldName() string {
	return strings.TrimSpace(s.cfg.ApprovalField)
}
func (s *Project) phaseFieldName() string {
	return strings.TrimSpace(s.cfg.PhaseField)
}
func (s *Project) transitionFieldName() string {
	return s.cfg.TransitionFieldName()
}
func (s *Project) activityFieldName() string {
	if name := strings.TrimSpace(s.cfg.ActivityField); name != "" {
		return name
	}
	return config.RunnerActivityFieldName
}
func (s *Project) qaFailuresFieldName() string {
	return strings.TrimSpace(s.cfg.QAFailuresField)
}
func (s *Project) branchFieldName() string {
	return strings.TrimSpace(s.cfg.BranchField)
}
func (s *Project) pullRequestFieldName() string {
	return strings.TrimSpace(s.cfg.PullRequestField)
}
func (s *Project) qaCommitFieldName() string {
	return strings.TrimSpace(s.cfg.QACommitField)
}
func (s *Project) requiredStatuses() []string {
	return append([]string(nil), s.cfg.RequiredStatuses...)
}

func (s *Project) agentStatus(status string) bool {
	statuses := s.cfg.AgentStatuses
	for _, candidate := range statuses {
		if strings.EqualFold(strings.TrimSpace(status), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func (s githubProjectSchema) field(name string) (githubProjectField, bool) {
	field, ok := s.Fields[normalizeProjectKey(name)]
	return field, ok
}

func (f githubProjectField) hasOption(name string) bool {
	return f.Options[normalizeProjectKey(name)].ID != ""
}
