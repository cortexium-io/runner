package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

// PrepareQuality does not configure quality, approve work or start admission.
func (e *Engine) PrepareQuality(ctx context.Context, spec config.QualitySpec, apply bool, preview, moduleCache string) (execution.EnvironmentPreview, *config.PreparedEnvironment, error) {
	var p execution.EnvironmentPreview
	if err := spec.Validate(); err != nil {
		return p, nil, err
	}
	if spec.Preparation == nil {
		return p, nil, errors.New("quality prepare requires an environment declaration")
	}
	if err := e.validate(ctx); err != nil {
		return p, nil, err
	}
	current, drained, err := e.Store.Settings(ctx)
	if err != nil {
		return p, nil, err
	}
	_, base, err := workspace.Observe(ctx, current.Config)
	if err != nil {
		return p, nil, err
	}
	root, err := workspace.BaselineCopy(ctx, current.Config, base)
	if err != nil {
		return p, nil, err
	}
	defer os.RemoveAll(root)
	files, err := workspace.Manifest(root)
	if err != nil {
		return p, nil, err
	}
	if err = workspace.ValidateOutputs(files, spec.Outputs); err != nil {
		return p, nil, err
	}
	p, err = execution.PreviewEnvironment(ctx, root, *spec.Preparation)
	if err != nil {
		return p, nil, err
	}
	if !apply {
		return p, nil, nil
	}
	if preview == "" || preview != p.Preview {
		return p, nil, errors.New("environment preview changed; inspect quality prepare again")
	}
	if !drained {
		return p, nil, errors.New("drain before environment preparation")
	}
	lock, err := execution.Lock(filepath.Join(current.Config.StateDir, "coordinator.lock"))
	if err != nil {
		return p, nil, err
	}
	defer lock.Close()
	if err = execution.CheckEnvironmentOwnership(current.Config.StateDir); err != nil {
		return p, nil, err
	}
	prepared, err := execution.PrepareEnvironment(ctx, current.Config.StateDir, root, p, moduleCache)
	return p, &prepared, err
}
