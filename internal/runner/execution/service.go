package execution

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Service routes deterministic, Codex, fixed Go-check and configured commands. Durable jobs retain
// routing across restart; no second workflow, dispatch queue or retry policy.
type Service struct {
	candidates    *Candidates
	tasks         *TaskSources
	commands      *Commands
	releases      *Releases
	disk          *runner.Disk
	deterministic *Deterministic
	workspaces    *Workspaces
	Codex         *Codex
	captureMu     sync.Mutex
	captures      map[string]*sourceCaptureWork
	captureWake   func(string)
	captureClosed bool
}

func NewService(d *runner.Disk, read func(context.Context, string) ([]byte, error), cfg *runner.CodexSettings, release ...*runner.ReleaseSettings) *Service {
	s := &Service{
		disk:          d,
		deterministic: New(d, read),
		workspaces:    &Workspaces{disk: d, read: read},
	}
	if cfg != nil {
		s.workspaces.readOnlyPaths = runner.Clone(cfg.ReadOnlyPaths)
		s.workspaces.nativeLimit = cfg.StorageLimit
	}
	s.candidates = &Candidates{disk: d, workspaces: s.workspaces}
	s.tasks = &TaskSources{disk: d, workspaces: s.workspaces}
	s.commands = NewCommands(d, cfg, s.workspaces, read)
	s.releases = &Releases{disk: d, workspaces: s.workspaces}
	if len(release) > 0 {
		s.releases.cfg = runner.Clone(release[0])
	}
	if cfg != nil {
		s.Codex = NewCodex(d, cfg)
		s.Codex.workspaces = s.workspaces
	}
	return s
}
func (s *Service) SetWake(wake func(string)) {
	s.captureMu.Lock()
	s.captureWake = wake
	s.captureMu.Unlock()
	s.commands.SetWake(wake)
	s.tasks.SetWake(wake)
	if s.Codex != nil {
		s.Codex.SetWake(wake)
	}
}
func (s *Service) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	switch a.Setup.Profile.Harness {
	case "release":
		return s.releases.Start(ctx, a)
	case "git-candidate":
		return s.candidates.Start(ctx, a)
	case "task-source":
		return s.tasks.Start(ctx, a)
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
	if s.tasks.Owns(id) {
		return s.tasks.Observe(ctx, id)
	}
	if s.releases.Owns(id) {
		return s.releases.Observe(ctx, id)
	}
	if s.candidates.Owns(id) {
		return s.candidates.Observe(ctx, id)
	}
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
	if s.tasks.Owns(id) {
		return s.tasks.Stop(ctx, id)
	}
	if s.releases.Owns(id) {
		return s.releases.Stop(ctx, id)
	}
	if s.candidates.Owns(id) {
		return s.candidates.Stop(ctx, id)
	}
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
		return errors.Join(s.Codex.Stop(ctx, id), s.stopCapture(ctx, id))
	}
	return s.deterministic.Stop(ctx, id)
}
func (s *Service) Release(ctx context.Context, id string) error {
	if s.tasks.Owns(id) {
		return s.tasks.Release(ctx, id)
	}
	if s.releases.Owns(id) {
		return s.releases.Release(ctx, id)
	}
	if s.candidates.Owns(id) {
		return s.candidates.Release(ctx, id)
	}
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
		if err := s.captureReleaseReady(id); err != nil {
			return err
		}
		return s.Codex.Release(ctx, id)
	}
	return s.deterministic.Release(ctx, id)
}
func (s *Service) Close(ctx context.Context) error {
	s.commands.Close()
	captureErr := s.closeCaptures(ctx)
	taskErr := s.tasks.Close(ctx)
	if s.Codex != nil {
		return errors.Join(captureErr, taskErr, s.Codex.Close(ctx))
	}
	return errors.Join(captureErr, taskErr)
}

func (s *Service) ReleaseRun(ctx context.Context, r runner.Run) error {
	if err := s.releases.recoverCancelledStop(ctx, r); err != nil {
		return err
	}
	return s.workspaces.releaseRun(ctx, r.ID, r.Status == "Failed")
}
func (s *Service) ForgetRun(ctx context.Context, id string) error {
	if err := s.workspaces.ReleaseRun(ctx, id); err != nil {
		return err
	}
	if err := s.tasks.ForgetRun(ctx, id); err != nil {
		return err
	}
	if err := s.workspaces.forgetAdoptedSource(ctx, id); err != nil {
		return err
	}
	if err := s.releases.ForgetRun(ctx, id); err != nil {
		return err
	}
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
		// Prelaunch readiness rejection is already settled and creates no job.
		// Preserve its reason instead of turning it into missing-job uncertainty.
		if errors.Is(err, os.ErrNotExist) && o.End != nil && o.End.Kind == "failed" && o.NativeHandle == "" && o.Source == nil {
			return o, nil
		}
		return runner.Observation{}, err
	}
	if j.Assignment.Setup.Profile.Access == "workspace-write" && j.Workspace != nil {
		source, err := s.captureSource(ctx, j.Assignment, j.StopRequested)
		if err != nil {
			o.State = runner.Unknown
			o.End = nil
			var pending *capturePending
			if errors.As(err, &pending) {
				o.Reconciling = j.Assignment.Deadline.IsZero() || j.Assignment.Deadline.After(time.Now())
				o.NextCheck = time.Now().Add(250 * time.Millisecond)
			}
			o.Reason = "source collection: " + runner.Clip(err.Error())
			return o, nil
		}
		o.Source = source
		if o.End != nil && o.End.Kind == "returned" && o.End.Result != nil && o.End.Result.Outcome != "needs_help" && source.PatchID != "" {
			o.End = runner.Clone(o.End)
			o.End.Result.Outputs["patch"] = runner.Value{ArtifactID: source.PatchID}
		}
	}
	return o, nil
}
