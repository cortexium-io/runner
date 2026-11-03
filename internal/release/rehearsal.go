package release

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

func rehearsalEnvironment(database, report, origin string) []string {
	return []string{
		"GENNY_ENV=production",
		"DB_DRIVER=sqlite",
		"DATABASE_URL=" + database,
		"ADDR=:8080",
		"GAMES_NONOGRAM_QUALIFICATION=" + report,
		"GAMES_PUBLIC_ORIGIN=" + origin,
		"GAMES_TRUSTED_PROXIES=127.0.0.1/32",
		"GAMES_GOOGLE_ENABLED=0",
		"GENNY_AUTH_ACCEPT_NOOP_MAILER=1",
		"GENNY_AUTH_ACCEPT_LOCAL_RATE_LIMITER=1",
		"GOMAXPROCS=2",
		"GOMEMLIMIT=768MiB",
	}
}

func (b *Live) trialArgs(j *Job, name, dir, report, env string) []string {
	return []string{
		"--name",
		name,
		"--label",
		"cortexium.runner.run=" + j.Request.RunID,
		"--init",
		"--pull=never",
		"--network",
		"none",
		"--read-only",
		"--cap-drop",
		"ALL",
		"--security-opt",
		"no-new-privileges:true",
		"--cpus",
		"2",
		"--memory",
		"1g",
		"--pids-limit",
		"64",
		"--user",
		"65532:65532",
		"--tmpfs",
		"/tmp:size=16m,mode=1777,noexec,nosuid",
		"--env-file",
		env,
		"--mount",
		"type=bind,src=" + dir + ",dst=/data",
		"--mount",
		"type=bind,src=" + report + ",dst=/report,readonly",
	}
}

func (b *Live) waitTrial(ctx context.Context, name string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := b.docker(ctx, "exec", name, "/app/server", "healthcheck"); err == nil {
			return nil
		}
		c, err := b.inspect(ctx, name)
		if err != nil {
			return err
		}
		if !c.State.Running {
			return errors.New("disposable server exited before readiness")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *Live) closeTrial(ctx context.Context, name, run, image string) error {
	c, err := b.inspect(ctx, name)
	if err != nil {
		return err
	}
	if c.Image != image || c.Config.Labels["cortexium.runner.run"] != run {
		return errors.New("disposable resource identity differs")
	}
	if c.State.Running {
		if _, err = b.docker(ctx, "stop", "--time", "15", c.ID); err != nil {
			return err
		}
	}
	return b.removeSettledContainer(ctx, name, run, image)
}

func (b *Live) exportTrial(ctx context.Context, remote, local string) error {
	if _, err := b.host(ctx, nil, "python3", b.Config.BackupHelper, "backup", remote, remote+".snapshot"); err != nil {
		return err
	}
	if _, err := b.host(ctx, nil, "python3", b.Config.BackupHelper, "verify", remote+".snapshot"); err != nil {
		return err
	}
	data, err := b.host(ctx, nil, "cat", remote+".snapshot")
	if err != nil {
		return err
	}
	return writeExclusive(local, data)
}

func (b *Live) rehearseCandidate(ctx context.Context, j *Job) (err error) {
	active := ""
	activeImage := ""
	defer func() {
		if active != "" {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
			defer cancel()
			err = errors.Join(err, b.closeTrial(cleanup, active, j.Request.RunID, activeImage))
		}
	}()
	c := b.Config
	ids, err := b.docker(ctx, "ps", "-a", "--filter", "label=com.docker.compose.project="+c.DeployProject, "--filter", "label=com.docker.compose.service=games", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if len(strings.Fields(string(ids))) != 1 {
		return errors.New("production identity is ambiguous before rehearsal")
	}
	old, err := b.inspect(ctx, strings.Fields(string(ids))[0])
	if err != nil {
		return err
	}
	if !old.State.Running || old.State.Health.Status != "healthy" || !b.dataMount(old) {
		return errors.New("production is not healthy before rehearsal")
	}
	j.Receipt.PriorContainer, j.Receipt.PriorImage = old.ID, old.Image
	stem := j.Request.Key + "-rehearsal"
	if _, err = b.capture(ctx, stem, false, old.ID, old.Image); err != nil {
		return err
	}
	before := filepath.Join(c.OffsiteBackupDir, stem+".db")
	if err = validateMigrationInput(before); err != nil {
		return err
	}
	data, err := readFile(before, 256<<20)
	if err != nil {
		return err
	}
	env := filepath.Join(filepath.Dir(j.Worker), "trial.env")
	if err = writeExclusive(env, []byte(strings.Join(rehearsalEnvironment("/data/games.db", "/report", "https://rehearsal.example.test"), "\n")+"\n")); err != nil {
		return err
	}
	dirs := []string{j.Receipt.Remote + "/rehearsal", j.Receipt.Remote + "/rollback"}
	for _, dir := range dirs {
		if _, err = b.host(ctx, nil, "mkdir", "-m", "0777", dir); err != nil {
			return err
		}
		if _, err = b.host(ctx, data, "tee", dir+"/games.db"); err != nil {
			return err
		}
		if _, err = b.host(ctx, nil, "chmod", "0666", dir+"/games.db"); err != nil {
			return err
		}
	}
	name := "runner-rehearse-migrate-" + j.Request.Key
	args := append([]string{"run"}, b.trialArgs(j, name, dirs[0], j.Receipt.Report, env)...)
	args = append(args, j.Receipt.Image, "--migrate-only")
	if _, err = b.docker(ctx, args...); err != nil {
		return err
	}
	if err = b.removeSettledContainer(ctx, name, j.Request.RunID, j.Receipt.Image); err != nil {
		return err
	}
	after := filepath.Join(j.Receipt.Package, "rehearsal-migrated.db")
	if err = b.exportTrial(ctx, dirs[0]+"/games.db", after); err != nil {
		return err
	}
	if err = verifyMigration(before, after); err != nil {
		return err
	}
	name = "runner-rehearse-server-" + j.Request.Key
	args = append([]string{"run", "-d"}, b.trialArgs(j, name, dirs[0], j.Receipt.Report, env)...)
	args = append(args, j.Receipt.Image)
	if _, err = b.docker(ctx, args...); err != nil {
		return err
	}
	active, activeImage = name, j.Receipt.Image
	if err = b.waitTrial(ctx, name); err != nil {
		return err
	}
	probeName := "runner-runtime-" + j.Request.Key
	runtime, err := b.docker(ctx, "run", "--name", probeName, "--label", "cortexium.runner.run="+j.Request.RunID, "--pull=never", "--network", "container:"+name, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--memory", "128m", "--pids-limit", "32", "--entrypoint", "/probe", "--mount", "type=bind,src="+c.Delivery.Probe+",dst=/probe,readonly", j.Receipt.Image)
	if err != nil {
		return err
	}
	var observed struct {
		Status  string
		Choices int
		Passed  int
	}
	if json.Unmarshal(runtime, &observed) != nil || observed.Status != "passed" || observed.Choices == 0 || observed.Choices != observed.Passed {
		return errors.New("runtime catalogue proof failed")
	}
	if err = writeExclusive(filepath.Join(j.Receipt.Package, "runtime-http.json"), runtime); err != nil {
		return err
	}
	if err = b.removeSettledContainer(ctx, probeName, j.Request.RunID, j.Receipt.Image); err != nil {
		return err
	}
	if err = b.closeTrial(ctx, name, j.Request.RunID, j.Receipt.Image); err != nil {
		return err
	}
	active = ""
	previousEnv, err := readFile(c.Environment, 64<<10)
	if err != nil {
		return err
	}
	previousReport := dotenvValue(previousEnv, "GAMES_NONOGRAM_QUALIFICATION_FILE")
	if previousReport == "" {
		return errors.New("previous qualified report binding is missing")
	}
	name = "runner-rehearse-rollback-" + j.Request.Key
	args = append([]string{"run", "-d"}, b.trialArgs(j, name, dirs[1], previousReport, env)...)
	args = append(args, old.Image)
	if _, err = b.docker(ctx, args...); err != nil {
		return err
	}
	active, activeImage = name, old.Image
	if err = b.waitTrial(ctx, name); err != nil {
		return err
	}
	if err = b.closeTrial(ctx, name, j.Request.RunID, old.Image); err != nil {
		return err
	}
	active = ""
	live, err := b.inspect(ctx, old.ID)
	if err != nil {
		return err
	}
	if live.Image != old.Image || !live.State.Running || live.State.Health.Status != "healthy" {
		return errors.New("production identity/health changed during rehearsal")
	}
	if err = writeJSON(filepath.Join(j.Receipt.Package, "rehearsal.json"), map[string]any{
		"status":                            "passed",
		"backupVerified":                    true,
		"allApplicationTablesPreserved":     true,
		"restoredBackupStartsPreviousImage": true,
		"backup":                            before,
		"backupSHA256":                      Digest(data),
		"productionServiceChanged":          false,
	}); err != nil {
		return err
	}
	return done(j, "Migration, complete advertised runtime catalogue and previous-image backup recovery rehearsed on disposable data; production unchanged")
}
