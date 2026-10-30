package execution

import (
	"context"
	"errors"
	"os"

	"github.com/cortexium-io/runner/internal/runner"
)

// Service routes deterministic, Codex, fixed Go-check and configured commands. Durable jobs retain
// routing across restart; no second workflow, dispatch queue or retry policy.
type Service struct {
	commands      *Commands
	disk          *runner.Disk
	deterministic *Deterministic
	workspaces    *Workspaces
	Codex         *Codex
}

func NewService(d *runner.Disk, read func(context.Context, string) ([]byte, error), cfg *runner.CodexSettings) *Service {
	s := &Service{disk: d, deterministic: New(d, read), workspaces: &Workspaces{disk: d}}
	s.commands = NewCommands(d, cfg, s.workspaces, read)
	if cfg != nil {
		s.Codex = NewCodex(d, cfg)
		s.Codex.workspaces = s.workspaces
	}
	return s
}
func (s *Service) SetWake(wake func(string)) {
	s.commands.SetWake(wake)
	if s.Codex != nil {
		s.Codex.SetWake(wake)
	}
}
func (s *Service) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	switch a.Setup.Profile.Harness {
	case "command":
		return s.commands.Start(ctx, a)
	case "deterministic":
		return s.deterministic.Start(ctx, a)
	case "codex", "go-check":
		if s.Codex != nil {
			o, e := s.Codex.Start(ctx, a)
			return s.collect(ctx, a.AttemptID, o, e)
		}
	}
	return runner.Observation{
		State: runner.Finished,
		End:   &runner.ExecutionEnd{Kind: "failed", Reason: "readiness: selected executor is not configured"},
	}, nil
}
func (s *Service) native(id string) (bool, error) {
	_, err := s.disk.Read(nativeName(id), s.disk.Policy.Database.MaxRecord)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return true, err
}
func (s *Service) Observe(ctx context.Context, id string) (runner.Observation, error) {
	if s.commands.Owns(id) {
		return s.commands.Observe(ctx, id)
	}
	native, err := s.native(id)
	if err != nil {
		return runner.Observation{}, err
	}
	if native {
		if s.Codex == nil {
			return runner.Observation{
				State:  runner.Unknown,
				Reason: "restore the instance Codex settings to reconcile saved native work",
			}, nil
		}
		o, e := s.Codex.Observe(ctx, id)
		return s.collect(ctx, id, o, e)
	}
	return s.deterministic.Observe(ctx, id)
}
func (s *Service) Stop(ctx context.Context, id string) error {
	if s.commands.Owns(id) {
		return s.commands.Stop(ctx, id)
	}
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
	if s.commands.Owns(id) {
		return s.commands.Release(ctx, id)
	}
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
	s.commands.Close()
	if s.Codex != nil {
		return s.Codex.Close(ctx)
	}
	return nil
}

func (s *Service) ReleaseRun(ctx context.Context, id string) error {
	return s.workspaces.ReleaseRun(ctx, id)
}
func (s *Service) ForgetRun(ctx context.Context, id string) error {
	return s.workspaces.ForgetRun(ctx, id)
}

func (s *Service) collect(ctx context.Context, id string, o runner.Observation, e error) (runner.Observation, error) {
	if e != nil || o.State != runner.Finished {
		return o, e
	}
	s.Codex.mu.Lock()
	j, err := s.Codex.loadLocked(id)
	s.Codex.mu.Unlock()
	if err != nil {
		return runner.Observation{}, err
	}
	if j.Assignment.Setup.Profile.Access == "workspace-write" && j.Workspace != nil {
		source, err := s.workspaces.Capture(ctx, j.Assignment)
		if err != nil {
			o.State = runner.Unknown
			o.End = nil
			o.Reason = "source collection: " + runner.Clip(err.Error())
			return o, nil
		}
		o.Source = source
		if o.End != nil && o.End.Kind == "returned" && o.End.Result != nil && o.End.Result.Outcome == "ready" && source.PatchID != "" {
			o.End = runner.Clone(o.End)
			o.End.Result.Outputs["patch"] = runner.Value{ArtifactID: source.PatchID}
		}
	}
	return o, nil
}
