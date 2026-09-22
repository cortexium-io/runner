package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
)

// PlanFieldMigration is deliberately narrower than init: no lanes, cards,
// views, historical state or other fields are provisioned by this migration.
type PlanFieldMigration struct {
	ProjectID   string `json:"project_id"`
	Owner       string `json:"owner"`
	Number      int    `json:"number"`
	FieldID     string `json:"existing_field_id,omitempty"`
	CreateField bool   `json:"create_release_text_field"`
	Snapshot    string `json:"project_snapshot"`
	fields      []projectFieldNode
	items       []WorkItem
}

// ItemIDs identifies the exact snapshot whose standalone review locks must be
// held at apply. The engine revalidates membership after acquiring those locks.
func (plan PlanFieldMigration) ItemIDs() []string {
	ids := make([]string, 0, len(plan.items))
	for _, item := range plan.items {
		ids = append(ids, item.ID)
	}
	return ids
}

func (s *Project) PlanDeliveryMigration(ctx context.Context) (PlanFieldMigration, error) {
	schema, err := s.loadSchema(ctx)
	if err != nil {
		return PlanFieldMigration{}, err
	}
	// loadSchema's normalized lookup is useful for ordinary reads but cannot
	// disambiguate duplicate/case-variant operator fields for a migration.
	fields, err := s.loadFields(ctx, schema.ProjectID)
	if err != nil {
		return PlanFieldMigration{}, err
	}
	plan := PlanFieldMigration{ProjectID: schema.ProjectID, Owner: s.cfg.Owner, Number: s.cfg.Number, CreateField: true}
	for _, field := range fields {
		if normalizeProjectKey(field.Name) != normalizeProjectKey(config.RunnerPlanReleaseFieldName) {
			continue
		}
		if plan.FieldID != "" || field.Name != config.RunnerPlanReleaseFieldName || field.TypeName != "ProjectV2Field" || field.DataType != "TEXT" || field.ID == "" {
			return PlanFieldMigration{}, errors.New("Runner Plan Release must be one exact TEXT field; preserve and resolve conflicting fields before migration")
		}
		plan.FieldID, plan.CreateField = field.ID, false
	}
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return PlanFieldMigration{}, err
	}
	if err := s.requireCompletedPreRolloutWork(items); err != nil {
		return PlanFieldMigration{}, err
	}
	// Bind the entire observed Project schema and work snapshot. No approval
	// assertions or bodies need to be printed in the operator preview.
	sort.Slice(fields, func(i, j int) bool { return fields[i].ID < fields[j].ID })
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	plan.fields, plan.items = fields, items
	encoded, err := json.Marshal(struct {
		ProjectID string
		Fields    []projectFieldNode
		Items     []WorkItem
	}{schema.ProjectID, fields, items})
	if err != nil {
		return PlanFieldMigration{}, err
	}
	plan.Snapshot = fmt.Sprintf("v1:%x", sha256.Sum256(encoded))
	return plan, nil
}

func (s *Project) requireCompletedPreRolloutWork(items []WorkItem) error {
	index := newWorkItemIndex(items)
	for _, item := range items {
		if item.Transition != "" || strings.EqualFold(item.Status, s.runningStatus()) {
			return fmt.Errorf("item %s is active or transition-locked; gracefully drain and recover Runner before delivery migration", item.ID)
		}
		_, manifest, _ := ParsePlanManifest(item.Body)
		planned := manifest || item.PlanRelease != "" || item.PlanningSourceID != "" || item.PlanningBatchFingerprint != "" || len(index.childrenBySource[item.ID]) > 0
		if planned {
			if !s.planningSourceWorkCompletedIn(item, index) && !s.hasSuccessfulOutcomeIn(item, index) {
				return fmt.Errorf("plan or member %s is not completely delivered; finish existing batches before enabling new-plan delivery", item.ID)
			}
			continue
		}
		if s.agentStatus(item.Status) || strings.EqualFold(item.Status, s.prReadyStatus()) {
			return fmt.Errorf("item %s remains scheduled or awaiting publication; finish existing work before delivery migration", item.ID)
		}
	}
	return nil
}

// ApplyDeliveryMigration revalidates the reviewed snapshot, adds at most one
// field, and reads it back. It never deletes a field after an ambiguous result.
func (s *Project) ApplyDeliveryMigration(ctx context.Context, plan PlanFieldMigration) error {
	fresh, err := s.PlanDeliveryMigration(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(fresh, plan) {
		return errors.New("Project schema or work changed after migration preview; inspect a fresh preview")
	}
	if !plan.CreateField {
		return nil
	}
	result, createErr := s.gh(ctx, "project", "field-create", strconv.Itoa(plan.Number), "--owner", plan.Owner, "--name", config.RunnerPlanReleaseFieldName, "--data-type", "TEXT", "--format", "json")
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	current, verifyErr := s.PlanDeliveryMigration(verifyCtx)
	if verifyErr != nil || current.ProjectID != plan.ProjectID || current.CreateField || current.FieldID == "" {
		if createErr != nil {
			createErr = commandFailure(createErr, result)
		}
		return errors.Join(errors.New("release field creation is unconfirmed; inspect the Project before retrying; configuration is unchanged"), createErr, verifyErr)
	}
	remaining := make([]projectFieldNode, 0, len(current.fields))
	for _, field := range current.fields {
		if field.ID != current.FieldID {
			remaining = append(remaining, field)
		}
	}
	if !reflect.DeepEqual(remaining, plan.fields) || !reflect.DeepEqual(current.items, plan.items) {
		return errors.New("release field exists but other Project state changed during migration; preserve it, leave configuration unchanged and review a fresh preview")
	}
	return nil
}
