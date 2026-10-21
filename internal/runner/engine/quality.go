package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
	"github.com/cortexium-io/runner/internal/securefs"
)

func (e *Engine) currentQuality(ctx context.Context) error {
	current, _, err := e.Store.Settings(ctx)
	if err != nil {
		return err
	}
	if !config.SameRepository(current, e.Settings) {
		return errors.New("repository authority changed")
	}
	e.Settings = current
	if err := current.Config.ValidateQuality(current.Quality); err != nil {
		return err
	}
	if current.Quality.Revision() == "" {
		return errors.New("apply a quality agreement before creating or approving work")
	}
	return nil
}

// taskEngine binds all Git checks and role assignments to immutable task history.
func (e *Engine) taskEngine(ctx context.Context, t core.Task) (*Engine, error) {
	worker := *e
	q, err := e.Store.Quality(ctx, t.Current().Quality)
	if err != nil {
		return nil, fmt.Errorf("pinned quality unavailable: %w", err)
	}
	worker.Settings.Quality = q
	return &worker, nil
}

type QualityPreview struct {
	Preview      string         `json:"preview"`
	RepositoryID string         `json:"repository_id"`
	Previous     string         `json:"previous_quality_revision"`
	Quality      config.Quality `json:"quality"`
	Environment  string         `json:"environment"`
	Blockers     []string       `json:"blockers"`
	ReportID     string         `json:"report_id,omitempty"`
}

func (e *Engine) PreviewQuality(ctx context.Context, spec config.QualitySpec) (QualityPreview, error) {
	var p QualityPreview
	if err := spec.Validate(); err != nil {
		return p, err
	}
	if err := e.validate(ctx); err != nil {
		return p, err
	}
	current, _, err := e.Store.Settings(ctx)
	if err != nil {
		return p, err
	}
	_, base, err := workspace.Observe(ctx, current.Config)
	if err != nil {
		return p, err
	}
	q, err := workspace.ResolveQuality(ctx, current.Config, base, spec)
	if err != nil {
		return p, err
	}
	if spec.Preparation != nil {
		root, err := workspace.BaselineCopy(ctx, current.Config, base)
		if err != nil {
			return p, err
		}
		defer os.RemoveAll(root)
		plan, err := execution.PreviewEnvironment(ctx, root, *spec.Preparation)
		if err != nil {
			return p, err
		}
		prepared, err := execution.PreparedEnvironment(current.Config.StateDir, plan)
		if err != nil {
			return p, err
		}
		q.Preparation = &prepared
		q.CheckImage = prepared.Image
	}
	q.Checks, err = execution.ResolveCheckCPUs(ctx, q.Checks)
	if err != nil {
		return p, err
	}
	if err = current.Config.ValidateQuality(q); err != nil {
		return p, err
	}
	if q.CheckImage == "" {
		q.Path = os.Getenv("PATH")
	}
	p = QualityPreview{RepositoryID: current.RepositoryID, Previous: current.Quality.Revision(), Quality: q, Environment: q.Environment()}
	p.Preview = core.Digest(p)
	return p, nil
}
func (e *Engine) RunBaseline(ctx context.Context, spec config.QualitySpec, preview string) (config.QualityReport, error) {
	var report config.QualityReport
	p, err := e.PreviewQuality(ctx, spec)
	if err != nil {
		return report, err
	}
	if preview == "" || preview != p.Preview {
		return report, errors.New("baseline preview changed; inspect quality preview again")
	}
	report = config.QualityReport{ID: core.NewID(), RepositoryID: p.RepositoryID, Quality: p.Quality}
	source, err := workspace.BaselineCopy(ctx, e.Settings.Config, p.Quality.Base)
	if err != nil {
		return report, err
	}
	report.Files, err = workspace.Manifest(source)
	if err != nil {
		return report, err
	}
	dir := filepath.Join(e.Settings.Config.StateDir, "quality", report.ID)
	if err = securefs.EnsurePrivateDir(dir); err != nil {
		return report, err
	}
	report.Quality.Baseline, err = e.runChecks(ctx, source, report.Files, p.Quality, dir, func(p core.Process) error {
		data, _ := json.Marshal(p)
		return securefs.WriteFileExclusive(filepath.Join(dir, core.NewID()+"-process.json"), data, 0600)
	})
	if err != nil {
		return report, err
	}
	if err = e.Store.SaveQualityReport(ctx, report); err != nil {
		return report, err
	}
	return report, nil
}
func (e *Engine) PreviewAgreement(ctx context.Context, id string, exceptions []config.ExceptionRequest) (QualityPreview, error) {
	var p QualityPreview
	if err := e.validate(ctx); err != nil {
		return p, err
	}
	report, err := e.Store.QualityReport(ctx, id)
	if err != nil {
		return p, err
	}
	current, _, err := e.Store.Settings(ctx)
	if err != nil {
		return p, err
	}
	repo, base, err := workspace.Observe(ctx, current.Config)
	if err != nil {
		return p, err
	}
	if repo != current.PhysicalRepository() || current.RepositoryID != report.RepositoryID || base != report.Quality.Base {
		return p, errors.New("baseline repository/base changed; collect a fresh baseline")
	}
	q, err := report.ResolveExceptions(exceptions)
	if err != nil {
		return p, err
	}
	p = QualityPreview{RepositoryID: current.RepositoryID, Previous: current.Quality.Revision(), Quality: q, Environment: q.Environment(), ReportID: id}
	if err = q.ValidateResults(q.Baseline, report.Files); err != nil {
		p.Blockers = append(p.Blockers, err.Error())
	}
	p.Preview = core.Digest(p)
	return p, nil
}
func (e *Engine) ApplyAgreement(ctx context.Context, id string, exceptions []config.ExceptionRequest, preview string) error {
	p, err := e.PreviewAgreement(ctx, id, exceptions)
	if err != nil {
		return err
	}
	if preview == "" || preview != p.Preview {
		return errors.New("agreement preview changed; inspect quality preview again")
	}
	if len(p.Blockers) > 0 {
		return fmt.Errorf("quality agreement blocked: %v", p.Blockers)
	}
	return e.Store.ApplyQuality(ctx, p.Previous, p.Quality)
}

// Each command receives its own disposable copy. Logs and failed copies remain
// inspectable; no repository command runs on the coordinator's event loop.
func (e *Engine) runChecks(ctx context.Context, source string, expected map[string]string, q config.Quality, dir string, started func(core.Process) error) ([]core.CheckObservation, error) {
	if e.Checker == nil {
		return nil, errors.New("quality check runner unavailable")
	}
	if err := securefs.EnsurePrivateDir(dir); err != nil {
		return nil, err
	}
	results := []core.CheckObservation{}
	for _, check := range q.Checks {
		if q.Preparation != nil {
			if err := execution.ValidateEnvironmentInputs(source, *q.Preparation); err != nil {
				return results, err
			}
		}
		copy, before, err := workspace.Copy(source, "")
		if err != nil {
			return results, err
		}
		if core.Digest(before.Source) != core.Digest(expected) {
			return results, errors.New("source changed before quality check copy")
		}
		check.Path = q.Path
		check.Image = q.CheckImage
		check.Outputs = q.Outputs
		if q.Preparation != nil {
			check.Prepared = &q.Preparation.Spec
		}
		if err = workspace.PrepareOutputs(copy, before.Source, q.Outputs); err != nil {
			return results, err
		}
		r := e.Checker.Check(ctx, copy, check, filepath.Join(dir, check.ID+".log"), started)
		if r.Stopped {
			if err = workspace.VerifyCopy(copy, before); err != nil {
				r.Status = "unavailable"
				r.Detail = err.Error()
			} else if r.Status != "unavailable" {
				r.Artifacts, err = execution.RetainCheckArtifacts(copy, config.OutputArtifacts(check.Artifacts, q.Outputs), core.Digest(expected), r)
				if err != nil {
					r.Status, r.Detail = "unavailable", err.Error()
				}
			}
		}
		results = append(results, r)
		if !r.Stopped {
			break
		}
	}
	return results, nil
}

func adoptionPreview(t core.Task, target string) string {
	return core.Digest(struct {
		ID                  string
		Version, Generation int
		Digest, Quality     string
	}{t.ID, t.Current().Number, t.Generation, t.Current().Digest, target})
}
func (e *Engine) AdoptQuality(ctx context.Context, id string, version int, preview string) (core.Task, error) {
	if err := e.validate(ctx); err != nil {
		return core.Task{}, err
	}
	if err := e.currentQuality(ctx); err != nil {
		return core.Task{}, err
	}
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return t, err
	}
	if t.Current().Number == version+1 && t.AdoptionPreview == preview && preview != "" {
		return t, nil
	}
	if t.Stage == core.Completed {
		return t, errors.New("completed tasks are immutable; add a new request")
	}
	if t.Release != nil || t.ParentID != "" {
		return t, errors.New("released plans are immutable; stop and create a new plan")
	}
	if t.Current().Number != version || preview == "" || preview != adoptionPreview(t, e.Settings.Quality.Revision()) {
		return t, store.ErrConflict
	}
	if t.Current().Quality == e.Settings.Quality.Revision() {
		return t, errors.New("task already uses the current quality agreement")
	}
	v, err := e.newVersion(t, version+1, t.Current().Body)
	if err != nil {
		return t, err
	}
	replaceVersion(&t, v)
	t.AdoptionPreview = preview
	return t, e.Store.AdoptQuality(ctx, t.Revision, t)
}

func exceptionFiles(q config.Quality, files map[string]string) map[string]string {
	out := map[string]string{}
	for _, ex := range q.Exceptions {
		for name := range ex.Files {
			out[name] = files[name]
		}
	}
	return out
}
