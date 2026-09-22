package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

type DeliveryMigration struct {
	Configuration config.DeliveryConfigMigration `json:"configuration"`
	Project       github.PlanFieldMigration      `json:"project"`
}

func (s *Engine) PlanDeliveryMigration(ctx context.Context, path, entrypoint string) (DeliveryMigration, error) {
	configuration, err := config.PlanDeliveryConfigMigration(path, entrypoint)
	if err != nil {
		return DeliveryMigration{}, err
	}
	current, err := config.LoadTrustedConfig(path)
	if err != nil {
		return DeliveryMigration{}, err
	}
	resolved, err := current.Resolve()
	if err != nil || !reflect.DeepEqual(resolved, s.cfg) {
		return DeliveryMigration{}, errors.New("migration engine/config identity changed; reload the trusted configuration")
	}
	project, err := s.source.PlanDeliveryMigration(ctx)
	return DeliveryMigration{Configuration: configuration, Project: project}, err
}

func (s *Engine) ApplyDeliveryMigration(ctx context.Context, plan DeliveryMigration) error {
	return s.withOfflineDeliveryOperator(plan.Project.ItemIDs(), func() error {
		fresh, err := s.PlanDeliveryMigration(ctx, plan.Configuration.Path, plan.Configuration.After.CompleteVerification)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(fresh, plan) {
			return errors.New("migration configuration or Project changed since preview; inspect a fresh preview")
		}
		if err := s.source.ApplyDeliveryMigration(ctx, plan.Project); err != nil {
			return err
		}
		if err := plan.Configuration.Apply(); err != nil {
			return fmt.Errorf("Runner Plan Release TEXT field is ready but configuration activation failed; field retained, no service started: %w", err)
		}
		return nil
	})
}

func (s *Engine) PlanDeliveryCancellation(ctx context.Context, selector string) (github.PlanCancellation, error) {
	return s.source.PlanCancellation(ctx, selector)
}

func (s *Engine) ApplyDeliveryCancellation(ctx context.Context, plan github.PlanCancellation) (result github.PlanCancellation, err error) {
	ids := []string{plan.ID}
	for _, member := range plan.Members {
		ids = append(ids, member.ID)
	}
	err = s.withOfflineDeliveryOperator(ids, func() error {
		result, err = s.source.ApplyPlanCancellation(ctx, plan)
		return err
	})
	return result, err
}

func (s *Engine) withOfflineDeliveryOperator(itemIDs []string, apply func() error) error {
	project := s.cfg.GitHubProject.GitHubProjectConfig
	worker, err := github.AcquireProcessLock(project)
	if err != nil {
		return fmt.Errorf("gracefully stop Runner and wait for all assignments/descendants to finish before this delivery operation; no stop or kill was requested: %w", err)
	}
	defer worker.Release()
	planning, err := github.AcquirePlanningLock(project)
	if err != nil {
		return fmt.Errorf("finish the standalone planning operation before this delivery change: %w", err)
	}
	defer planning.Release()
	mutation, err := github.AcquirePlanningMutationLock(project)
	if err != nil {
		return err
	}
	defer mutation.Release()
	entries := make([]string, 0, len(s.cfg.Verification))
	for id := range s.cfg.Verification {
		entries = append(entries, id)
	}
	sort.Strings(entries)
	for _, id := range entries {
		verification, err := github.AcquireVerificationOperationLock(project, id)
		if err != nil {
			return fmt.Errorf("finish standalone verification %s before this delivery change: %w", id, err)
		}
		defer verification.Release()
	}
	for _, id := range itemIDs {
		qa, err := github.AcquireQAReviewLock(project, id)
		if err != nil {
			return fmt.Errorf("finish the standalone review of %s before this delivery change: %w", id, err)
		}
		defer qa.Release()
	}
	// A crashed standalone launcher can release its OS lock before owned work
	// is safely resolved. Its durable heavy claim, bound to this same Project
	// scope, must still quarantine operator changes. Never clear it here.
	if err := s.processOwnership.CheckAdmission(); err != nil {
		return fmt.Errorf("process ownership is not quiescent; inspect and explicitly recover retained work before this delivery change: %w", err)
	}
	return apply()
}
