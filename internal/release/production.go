package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type controllerJob struct {
	ID      string `json:"job_id"`
	Status  string `json:"status"`
	Commit  string `json:"commit"`
	Started bool   `json:"deployment_started"`
	Inputs  struct {
		ExpectedCommit string `json:"expected_commit"`
	} `json:"inputs"`
}

type deploymentFailure struct {
	job     string
	started bool
}

func (e *deploymentFailure) Error() string {
	return "controller failed after deployment started; retain job " + e.job + " for observed recovery"
}

func (b *Live) controller(ctx context.Context, j *Job, save func() error, validate bool) (bool, error) {
	name := "deploy"
	suffix := "-deploy"
	if validate {
		name = "validate_deployment"
		suffix = "-validate"
	}
	if j.RemoteID == "" {
		if err := intent(j, name+"_requested", save); err != nil {
			return false, err
		}
		var started controllerJob
		if err := b.tool(ctx, name, map[string]any{
			"project":         b.Config.DeployProject,
			"branch":          b.Config.Branch,
			"expected_commit": j.Receipt.Commit,
			"request_id":      j.Request.Key + suffix,
		}, &started); err != nil {
			return false, err
		}
		if started.ID == "" {
			return false, errors.New("controller returned no durable job identity")
		}
		j.RemoteID = started.ID
		if err := save(); err != nil {
			return false, err
		}
	}
	var status controllerJob
	if err := b.tool(ctx, "deployment_status", map[string]any{"job_id": j.RemoteID}, &status); err != nil {
		return false, err
	}
	if status.ID != j.RemoteID {
		return false, errors.New("controller job identity differs")
	}
	if status.Status == "failed" || status.Status == "interrupted" {
		if b.Config.Delivery != nil && (status.Status == "failed" || !status.Started) {
			return false, &deploymentFailure{job: j.RemoteID, started: status.Started}
		}
		if status.Started {
			return false, errors.New("controller interrupted after deployment started; remote settlement needs inspection; retain job " + j.RemoteID)
		}
		return false, fail(j, fmt.Sprintf("controller %s; deployment_started=%t; inspect remote state before a new operation (job %s)", status.Status, status.Started, j.RemoteID))
	}
	ready := status.Status == "succeeded" && !validate || status.Status == "validated" && validate
	if ready && (status.Commit != j.Receipt.Commit || validate && status.Started) {
		return false, fail(j, "controller commit/mutation evidence differs")
	}
	return ready, nil
}

func (b *Live) backup(ctx context.Context, j *Job, save func() error) error {
	if j.Receipt.Commit == "" {
		return fail(j, "backup requires the confirmed protected merge")
	}
	if err := b.currentMain(ctx, j); err != nil {
		return fail(j, err.Error())
	}
	if err := b.snapshot(ctx, j, save, false); err != nil {
		return err
	}
	if j.Failed {
		return nil
	}
	if err := validateMigrationInput(j.Receipt.Backup); err != nil {
		return fail(j, "fresh database is outside the qualified migration contract; production remains unchanged")
	}
	ready, err := b.controller(ctx, j, save, true)
	if err != nil || j.Failed {
		return err
	}
	if !ready {
		j.Reason = "waiting for exact-main deployment validation"
		return nil
	}
	return done(j, "Fresh consistent off-host backup and exact-main controller validation passed. Approval authorizes stopping Games, a final stopped-data backup, the qualified migration check, image/report pinning, deployment and verification. Database restoration needs a separate decision.")
}

func (b *Live) currentMain(ctx context.Context, j *Job) error {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := b.gh(ctx, "repos/"+b.Config.Repository+"/git/ref/heads/"+b.Config.Branch, &ref); err != nil {
		return err
	}
	if ref.Object.SHA != j.Receipt.Commit {
		return errors.New("main changed after protected merge; requalification and owner decision required")
	}
	return nil
}

func (b *Live) snapshot(ctx context.Context, j *Job, save func() error, final bool) error {
	c := b.Config
	suffix := ""
	if final {
		suffix = "-stopped"
	}
	stem := j.Request.RunID + suffix
	if final {
		stem = j.Request.Key + suffix
	}
	remote := filepath.Join(c.BackupDir, stem+".db")
	local := filepath.Join(c.OffsiteBackupDir, stem+".db")
	if !final && j.Receipt.Backup != "" || final && j.Receipt.FinalBackup != "" {
		return nil
	}
	hash, err := b.capture(ctx, stem, final, j.Receipt.PriorContainer, j.Receipt.PriorImage)
	if err != nil {
		return err
	}
	if final {
		j.Receipt.FinalBackup, j.Receipt.FinalBackupSHA = local, hash
	} else {
		j.Receipt.Backup, j.Receipt.BackupSHA, j.Receipt.RemoteBackup = local, hash, remote
		environment, err := readFile(c.Environment, 64<<10)
		if err != nil {
			return err
		}
		if dotenvValue(environment, "GAMES_IMAGE") != j.Receipt.PriorImage {
			return fail(j, "production image binding changed since preparation")
		}
		backup := filepath.Join(c.StateDir, j.Request.RunID+".environment")
		if err = writeExclusive(backup, environment); err != nil {
			return err
		}
		j.Receipt.EnvironmentBackup = backup
	}
	return save()
}

func (b *Live) capture(ctx context.Context, stem string, quiesced bool, containerID, image string) (string, error) {
	c := b.Config
	if err := os.MkdirAll(c.OffsiteBackupDir, 0700); err != nil {
		return "", err
	}
	remote := filepath.Join(c.BackupDir, stem+".db")
	if _, err := b.host(ctx, nil, "test", "-f", remote); err != nil {
		args := []string{"python3", c.BackupHelper, "backup", filepath.Join(c.DataDir, "games.db"), remote}
		if quiesced {
			args = []string{
				filepath.Join(filepath.Dir(c.BackupHelper), "runner-snapshot"),
				"--source", filepath.Join(c.DataDir, "games.db"),
				"--destination", remote,
				"--engine", c.Engine,
				"--project", c.DeployProject,
				"--container", containerID,
				"--image", image,
			}
		}
		if _, err = b.host(ctx, nil, args...); err != nil {
			return "", err
		}
	}
	var proof SnapshotProof
	if quiesced {
		marker, err := b.host(ctx, nil, "cat", remote+".verified")
		if err != nil {
			return "", &proofFailure{"quiesced backup completion receipt is not available"}
		}
		if json.Unmarshal(marker, &proof) != nil || proof.Source != filepath.Join(c.DataDir, "games.db") || proof.Destination != remote || len(proof.SnapshotSHA) != 64 {
			return "", &proofFailure{"quiesced backup completion identity differs"}
		}
	}
	if _, err := b.host(ctx, nil, "python3", c.BackupHelper, "verify", remote); err != nil {
		if knownFailure(err) {
			return "", &proofFailure{"backup integrity/restore proof failed; preserve partial files for the owner"}
		}
		return "", err
	}
	data, err := b.host(ctx, nil, "cat", remote)
	if err != nil {
		return "", err
	}
	if quiesced && proof.SnapshotSHA != Digest(data) {
		return "", &proofFailure{"quiesced backup bytes differ from completion receipt"}
	}
	if err = writeExclusive(filepath.Join(c.OffsiteBackupDir, stem+".db"), data); err != nil {
		return "", err
	}
	return Digest(data), nil
}

func writeExclusive(file string, data []byte) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		previous, readErr := readFile(file, int64(len(data))+1)
		if readErr == nil && string(previous) == string(data) {
			return nil
		}
		return errors.New("retained operator file differs; do not overwrite it")
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

type container struct {
	ID     string `json:"Id"`
	Image  string `json:"Image"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	Config struct {
		Labels map[string]string `json:"Labels"`
		Env    []string          `json:"Env"`
	} `json:"Config"`
	State struct {
		Status   string `json:"Status"`
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
		Health   struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

func (b *Live) dataMount(c container) bool {
	count := 0
	for _, m := range c.Mounts {
		if m.Destination == "/data" {
			count++
			if m.Type != "bind" || m.Source != b.Config.DataDir || !m.RW {
				return false
			}
		}
	}
	return count == 1
}

func (b *Live) inspect(ctx context.Context, id string) (container, error) {
	data, err := b.docker(ctx, "inspect", id)
	var result []container
	if err != nil {
		return container{}, err
	}
	if err = json.Unmarshal(data, &result); err != nil || len(result) != 1 {
		return container{}, errors.New("container inspection identity is ambiguous")
	}
	return result[0], nil
}
func (b *Live) existing(ctx context.Context, j *Job) (container, error) {
	data, err := b.docker(ctx, "ps", "-a", "--no-trunc", "--filter", "label=com.docker.compose.project="+b.Config.DeployProject, "--filter", "label=com.docker.compose.service=games", "--format", "{{.ID}}")
	if err != nil {
		return container{}, err
	}
	ids := strings.Fields(string(data))
	if len(ids) != 1 || ids[0] != j.Receipt.PriorContainer {
		return container{}, errors.New("managed Games container changed after approval")
	}
	c, err := b.inspect(ctx, ids[0])
	if err != nil {
		return c, err
	}
	if c.ID != j.Receipt.PriorContainer || c.Image != j.Receipt.PriorImage || c.Config.Labels["com.docker.compose.project"] != b.Config.DeployProject || c.Config.Labels["com.docker.compose.service"] != "games" || !b.dataMount(c) {
		return c, errors.New("managed Games identity differs")
	}
	return c, nil
}
func (b *Live) stop(ctx context.Context, j *Job, save func() error) error {
	if err := b.currentMain(ctx, j); err != nil {
		return fail(j, err.Error())
	}
	data, err := readFile(j.Receipt.Backup, 256<<20)
	if err != nil || Digest(data) != j.Receipt.BackupSHA {
		return fail(j, "approved recovery snapshot is missing or changed")
	}
	c, err := b.existing(ctx, j)
	if err != nil {
		return err
	}
	if c.ID != j.Receipt.PriorContainer || c.Image != j.Receipt.PriorImage || c.Config.Labels["com.docker.compose.project"] != b.Config.DeployProject || c.Config.Labels["com.docker.compose.service"] != "games" {
		return fail(j, "existing Games container identity changed; do not stop it")
	}
	if c.State.Running {
		if j.Phase == "stop_requested" {
			return errors.New("stop acknowledgement uncertain; observe the same container")
		}
		if err = intent(j, "stop_requested", save); err != nil {
			return err
		}
		if _, err = b.docker(ctx, "stop", "--time", "30", c.ID); err != nil {
			return err
		}
		return nil
	}
	if b.Config.RecoverBeforeMigration && j.StoppedSHA == "" {
		hash, err := b.closedFingerprint(ctx, j)
		if err != nil {
			return err
		}
		j.StoppedSHA = hash
		j.Receipt.DataSHA = hash
		if err = save(); err != nil {
			return err
		}
	}
	if err = b.snapshot(ctx, j, save, true); err != nil || j.Failed {
		if err != nil && knownFailure(err) && b.Config.RecoverBeforeMigration {
			j.Failure = "final stopped-data backup failed"
			j.Recovery = "needed"
			if err = save(); err != nil {
				return err
			}
			return b.recoverBeforeMigration(ctx, j, save)
		}
		return err
	}
	return done(j, "Games stopped; final consistent off-host snapshot preserved for explicit recovery. Migration may now run.")
}

func (b *Live) migrate(ctx context.Context, j *Job, save func() error) error {
	before, err := readFile(j.Receipt.FinalBackup, 256<<20)
	if err != nil || Digest(before) != j.Receipt.FinalBackupSHA {
		return fail(j, "final stopped-data recovery snapshot differs")
	}
	old, err := b.existing(ctx, j)
	if err != nil {
		return err
	}
	if old.State.Running {
		return fail(j, "old application is still running; migration is blocked")
	}
	name := "runner-migration-" + j.Request.RunID
	if j.Phase == "" {
		environment, err := migrationEnvironment(old.Config.Env)
		if err != nil {
			return err
		}
		envFile := filepath.Join(b.Config.StateDir, j.Request.RunID+".migration.env")
		if err = writeExclusive(envFile, environment); err != nil {
			return err
		}
		if err = intent(j, "create_requested", save); err != nil {
			return err
		}
		c := b.Config
		_, err = b.docker(ctx, "create", "--name", name, "--label", "cortexium.runner.run="+j.Request.RunID, "--init", "--pull=never", "--user", "65532:65532", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--network", "none", "--cpus", "2", "--memory", "1g", "--pids-limit", "64", "--mount", "type=bind,src="+c.DataDir+",dst=/data", "--mount", "type=bind,src="+j.Receipt.Report+",dst=/run/nonogram/held-out.json,readonly", "--tmpfs", "/tmp:rw,size=16m,mode=1777,noexec,nosuid", "--env-file", envFile, j.Receipt.Image, "--migrate-only")
		if err != nil {
			return err
		}
	}
	c, err := b.inspect(ctx, name)
	if err != nil {
		return err
	}
	if c.Image != j.Receipt.Image || c.Config.Labels["cortexium.runner.run"] != j.Request.RunID {
		return fail(j, "migration container identity differs")
	}
	if c.State.Status == "created" {
		if j.Phase == "start_requested" {
			return errors.New("migration start acknowledgement uncertain; preserve container and inspect")
		}
		if err = intent(j, "start_requested", save); err != nil {
			return err
		}
		_, err = b.docker(ctx, "start", name)
		return err
	}
	if c.State.Running {
		return nil
	}
	if c.State.Status != "exited" || c.State.ExitCode != 0 {
		if b.Config.Delivery != nil && c.State.Status == "exited" {
			j.Outcome = "recovery_required"
			return done(j, "Migration exited unsuccessfully; Runner will inspect stopped data and present an explicit backup restoration decision")
		}
		return fail(j, "migration failed; Games remains stopped, data restore requires a separate owner decision")
	}
	if _, err = b.capture(ctx, j.Request.Key+"-migrated", true, j.Receipt.PriorContainer, j.Receipt.PriorImage); err != nil {
		return err
	}
	if err = verifyMigration(j.Receipt.FinalBackup, filepath.Join(b.Config.OffsiteBackupDir, j.Request.Key+"-migrated.db")); err != nil {
		if b.Config.Delivery != nil {
			j.Outcome = "recovery_required"
			return done(j, "Migration data contract failed; stopped data and accepted snapshot retained for an explicit restoration decision")
		}
		return fail(j, "migration changed data outside its accepted contract; application stays stopped and recovery requires an owner decision")
	}
	return done(j, "Pinned-image migration completed successfully. Recovery snapshots retained; no automatic database restore.")
}

func migrationEnvironment(existing []string) ([]byte, error) {
	values := map[string]string{}
	for _, entry := range existing {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.ContainsAny(entry, "\r\n\x00") {
			return nil, errors.New("live environment cannot be safely represented for migration")
		}
		if _, exists := values[key]; exists {
			return nil, errors.New("duplicate live environment key")
		}
		values[key] = value
	}
	for _, key := range []string{"DB_DRIVER", "DATABASE_URL", "GENNY_ENV", "GAMES_PUBLIC_ORIGIN"} {
		if values[key] == "" {
			return nil, errors.New("live migration environment missing " + key)
		}
	}
	values["DB_DRIVER"] = "sqlite"
	values["DATABASE_URL"] = "/data/games.db"
	values["GAMES_NONOGRAM_QUALIFICATION"] = "/run/nonogram/held-out.json"
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var result strings.Builder
	for _, key := range keys {
		result.WriteString(key + "=" + values[key] + "\n")
	}
	return []byte(result.String()), nil
}

func dotenvValue(data []byte, key string) string {
	value := ""
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, key+"=") {
			count++
			value = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, key+"=")), "\"'")
		}
	}
	if count != 1 {
		return ""
	}
	return value
}
func replaceBinding(data []byte, key, value string) ([]byte, error) {
	if dotenvValue(data, key) == "" {
		return nil, errors.New("operator environment has missing or duplicated " + key)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			lines[i] = key + "=" + value
		}
	}
	return []byte(strings.Join(lines, "\n")), nil
}
func (b *Live) pin(ctx context.Context, j *Job, save func() error) error {
	c := b.Config
	previous, err := b.existing(ctx, j)
	if err != nil {
		return err
	}
	if previous.State.Running {
		return fail(j, "Games resumed outside the approved migration sequence; pinning is blocked")
	}
	old, err := readFile(j.Receipt.EnvironmentBackup, 64<<10)
	if err != nil {
		return err
	}
	next, err := replaceBinding(old, "GAMES_IMAGE", j.Receipt.Image)
	if err != nil {
		return err
	}
	next, err = replaceBinding(next, "GAMES_NONOGRAM_QUALIFICATION_FILE", j.Receipt.Report)
	if err != nil {
		return err
	}
	current, err := readFile(c.Environment, 64<<10)
	if err != nil {
		return err
	}
	if string(current) == string(next) {
		return done(j, "Only image and qualification-report environment bindings updated; original private environment retained")
	}
	if string(current) != string(old) {
		return fail(j, "operator environment changed after approval; preserve it and ask the owner")
	}
	if err = intent(j, "pin_requested", save); err != nil {
		return err
	}
	if err = writeAtomic(c.Environment, next); err != nil {
		return err
	}
	return done(j, "Only image and qualification-report environment bindings updated; original private environment retained")
}
func writeAtomic(file string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(file), ".release-binding-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}
func (b *Live) deploy(ctx context.Context, j *Job, save func() error) error {
	if j.Resume != "" {
		return b.redeploy(ctx, j, save)
	}
	if err := b.currentMain(ctx, j); err != nil {
		return fail(j, err.Error())
	}
	if j.Phase == "" {
		previous, err := b.existing(ctx, j)
		if err != nil {
			return err
		}
		if previous.State.Running {
			return fail(j, "Games resumed before the approved deployment")
		}
	}
	data, err := readFile(b.Config.Environment, 64<<10)
	if err != nil {
		return err
	}
	if dotenvValue(data, "GAMES_IMAGE") != j.Receipt.Image || dotenvValue(data, "GAMES_NONOGRAM_QUALIFICATION_FILE") != j.Receipt.Report {
		return fail(j, "approved image/report environment binding differs")
	}
	ready, err := b.controller(ctx, j, save, false)
	var failure *deploymentFailure
	if b.Config.Delivery != nil && errors.As(err, &failure) {
		j.Receipt.Deployment = failure.job
		j.Outcome = "recovery_required"
		if !failure.started {
			j.Outcome = "retry_safe"
			j.Receipt.Inspection = "controller conclusively failed before deployment_started; one configured retry is permitted"
		}
		return done(j, "Deployment failed after effects began; Runner will inspect the same job and present a bounded recovery approval")
	}
	if err != nil || j.Failed {
		return err
	}
	if !ready {
		return nil
	}
	j.Receipt.Deployment = j.RemoteID
	return done(j, "Exact-main Compose deployment completed; independent live verification follows")
}
func (b *Live) verify(ctx context.Context, j *Job) error {
	var health struct {
		Status  string `json:"status"`
		Healthy bool   `json:"healthy"`
	}
	if err := b.tool(ctx, "service_health", map[string]any{"project": b.Config.DeployProject}, &health); err != nil {
		return err
	}
	if health.Status != "healthy" {
		return fail(j, "fresh controller service health is not healthy")
	}
	containers, err := b.docker(ctx, "ps", "--filter", "label=com.docker.compose.project="+b.Config.DeployProject, "--filter", "label=com.docker.compose.service=games", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	ids := strings.Fields(string(containers))
	if len(ids) != 1 {
		return fail(j, "fresh live container identity is ambiguous")
	}
	c, err := b.inspect(ctx, ids[0])
	if err != nil {
		return err
	}
	if c.Image != j.Receipt.Image || !c.State.Running || c.State.Health.Status != "healthy" {
		return fail(j, "live image or container health differs from the approved release")
	}
	tmp, err := os.MkdirTemp(b.Config.StateDir, "verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if _, err = b.docker(ctx, "cp", c.ID+":/app/server", filepath.Join(tmp, "server")); err != nil {
		return err
	}
	server, err := os.Open(filepath.Join(tmp, "server"))
	if err != nil {
		return err
	}
	defer server.Close()
	hash, err := hashReader(server)
	if err != nil {
		return err
	}
	if hash != j.Receipt.ServerSHA {
		return fail(j, "live executable differs from the independently qualified executable")
	}
	for path, want := range map[string]string{"/api/health": "ok", "/api/ready": "ready"} {
		data, err := get(ctx, b.Config.PublicURL+path)
		if err != nil {
			return err
		}
		var body struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(data, &body) != nil || body.Status != want {
			return fail(j, "public health/readiness contract differs")
		}
	}
	return done(j, "Protected merge, verified backups, explicit migration, pinned image/report deployment and fresh live executable/health checks passed. No database restore was performed.")
}
