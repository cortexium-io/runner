package execution

import (
	"context"
	"errors"
	"os"

	"github.com/cortexium-io/runner/internal/runner"
)

// Service routes the two supported executors. Durable native job records retain
// routing across restart; no second workflow, dispatch queue or retry policy.
type Service struct {
	disk          *runner.Disk
	deterministic *Deterministic
	Codex         *Codex
}

func NewService(d *runner.Disk, read func(context.Context, string) ([]byte, error), cfg *runner.CodexSettings) *Service {
	s := &Service{disk: d, deterministic: New(d, read)}
	if cfg != nil {
		s.Codex = NewCodex(d, cfg)
	}
	return s
}
func (s *Service) SetWake(wake func(string)) {
	if s.Codex != nil {
		s.Codex.SetWake(wake)
	}
}
func (s *Service) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	switch a.Setup.Profile.Harness {
	case "deterministic":
		return s.deterministic.Start(ctx, a)
	case "codex":
		if s.Codex != nil {
			return s.Codex.Start(ctx, a)
		}
	}
	return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: "readiness: selected executor is not configured"}}, nil
}
func (s *Service) native(id string) (bool, error) {
	_, err := s.disk.Read(nativeName(id), s.disk.Policy.Database.MaxRecord)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return true, err
}
func (s *Service) Observe(ctx context.Context, id string) (runner.Observation, error) {
	native, err := s.native(id)
	if err != nil {
		return runner.Observation{}, err
	}
	if native {
		if s.Codex == nil {
			return runner.Observation{State: runner.Unknown, Reason: "restore the instance Codex settings to reconcile saved native work"}, nil
		}
		return s.Codex.Observe(ctx, id)
	}
	return s.deterministic.Observe(ctx, id)
}
func (s *Service) Stop(ctx context.Context, id string) error {
	native, err := s.native(id)
	if err != nil {
		return err
	}
	if native {
		if s.Codex == nil {
			return errors.New("Codex settings are required to stop saved native work")
		}
		return s.Codex.Stop(ctx, id)
	}
	return s.deterministic.Stop(ctx, id)
}
func (s *Service) Release(ctx context.Context, id string) error {
	native, err := s.native(id)
	if err != nil {
		return err
	}
	if native {
		if s.Codex == nil {
			return errors.New("Codex settings are required to release saved native work")
		}
		return s.Codex.Release(ctx, id)
	}
	return s.deterministic.Release(ctx, id)
}
func (s *Service) Close(ctx context.Context) error {
	if s.Codex != nil {
		return s.Codex.Close(ctx)
	}
	return nil
}
