package release

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type observedService struct {
	ID      string `json:"id"`
	Image   string `json:"image"`
	Running bool   `json:"running"`
	Health  string `json:"health"`
}

func (b *Live) observeServices(ctx context.Context, j *Job) ([]observedService, error) {
	data, err := b.docker(ctx, "ps", "-a", "--filter", "label=com.docker.compose.project="+b.Config.DeployProject, "--filter", "label=com.docker.compose.service=games", "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(data))
	if len(ids) > 2 {
		return nil, errors.New("recovery found ambiguous service inventory")
	}
	observed := []observedService{}
	for _, id := range ids {
		c, err := b.inspect(ctx, id)
		if err != nil {
			return nil, err
		}
		if !b.dataMount(c) || (c.Image != j.Receipt.PriorImage && c.Image != j.Receipt.Image) {
			return nil, errors.New("recovery found an unexpected image/data mount")
		}
		observed = append(observed, observedService{ID: c.ID, Image: c.Image, Running: c.State.Running, Health: c.State.Health.Status})
	}
	return observed, nil
}

func (b *Live) inspectDeployment(ctx context.Context, j *Job) error {
	var status controllerJob
	if err := b.tool(ctx, "deployment_status", map[string]any{"job_id": j.Receipt.Deployment}, &status); err != nil {
		return err
	}
	if status.ID != j.Receipt.Deployment || status.Status != "failed" || !status.Started || status.Commit != j.Receipt.Commit {
		return fail(j, "failed deployment is not conclusively settled at the approved commit; retain its identity")
	}
	services, err := b.observeServices(ctx, j)
	if err != nil {
		return err
	}
	data, err := json.Marshal(services)
	if err != nil {
		return err
	}
	j.Receipt.Inspection = string(data)
	return done(j, "Failed deployment inspected. Approval authorizes one reapplication of this same approved image/report at the same commit, followed by live verification. Migration and database restoration are excluded. Observed services: "+string(data))
}

func (b *Live) redeploy(ctx context.Context, j *Job, save func() error) error {
	if j.Receipt.Inspection == "" {
		return fail(j, "deployment recovery requires its saved inspection and explicit process decision")
	}
	var original controllerJob
	if err := b.tool(ctx, "deployment_status", map[string]any{"job_id": j.Receipt.Deployment}, &original); err != nil {
		return err
	}
	if original.ID != j.Receipt.Deployment || (original.Status != "failed" && original.Status != "interrupted") || (original.Commit != j.Receipt.Commit && original.Inputs.ExpectedCommit != j.Receipt.Commit) {
		return fail(j, "original deployment settlement/commit differs; recovery is blocked")
	}
	if j.Resume == "safe" && original.Started {
		return fail(j, "deployment effects require the inspection approval before retry")
	}
	if err := b.currentMain(ctx, j); err != nil {
		return fail(j, err.Error())
	}
	if _, err := b.observeServices(ctx, j); err != nil {
		return err
	}
	data, err := readFile(b.Config.Environment, 64<<10)
	if err != nil {
		return err
	}
	if dotenvValue(data, "GAMES_IMAGE") != j.Receipt.Image || dotenvValue(data, "GAMES_NONOGRAM_QUALIFICATION_FILE") != j.Receipt.Report {
		return fail(j, "approved recovery environment binding changed")
	}
	ready, err := b.controller(ctx, j, save, false)
	var failed *deploymentFailure
	if errors.As(err, &failed) {
		j.Receipt.Deployment = failed.job
		j.Outcome = "recovery_required"
		if !failed.started {
			j.Outcome = "retry_safe"
		}
		return done(j, "Deployment recovery failed; original route limits still apply; retained job "+failed.job)
	}
	if err != nil || j.Failed {
		return err
	}
	if !ready {
		return nil
	}
	j.Receipt.Deployment = j.RemoteID
	return done(j, "Owner-approved exact-image deployment recovery completed; live verification follows")
}
