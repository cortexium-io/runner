package release

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

func (b *Live) inspectMigration(ctx context.Context, j *Job) error {
	old, err := b.existing(ctx, j)
	if err != nil {
		return err
	}
	if old.State.Running {
		return fail(j, "previous application resumed; stopped-data recovery is blocked")
	}
	migration, err := b.inspect(ctx, "runner-migration-"+j.Request.RunID)
	if err != nil {
		return err
	}
	if migration.State.Running || migration.State.Status != "exited" || migration.Image != j.Receipt.Image || migration.Config.Labels["cortexium.runner.run"] != j.Request.RunID {
		return errors.New("migration is not conclusively settled at the approved image")
	}
	data, err := readFile(j.Receipt.FinalBackup, 256<<20)
	if err != nil {
		return err
	}
	if Digest(data) != j.Receipt.FinalBackupSHA {
		return fail(j, "accepted final recovery snapshot differs")
	}
	if _, err = inspectDatabase(j.Receipt.FinalBackup); err != nil {
		return fail(j, "accepted recovery snapshot is not intact")
	}
	hash, err := b.closedFingerprint(ctx, j)
	if err != nil {
		return err
	}
	j.Receipt.DataSHA = hash
	return done(j, fmt.Sprintf("Migration failed with Games stopped. Approval restores final backup %s (SHA256 %s), replacing inspected data %s, then restarts previous image %s. No deployment or release retry is included.", j.Receipt.FinalBackup, j.Receipt.FinalBackupSHA, hash, j.Receipt.PriorImage))
}

func (b *Live) restorePrevious(ctx context.Context, j *Job, save func() error) error {
	old, err := b.existing(ctx, j)
	if err != nil {
		return err
	}
	current, err := readFile(b.Config.Environment, 64<<10)
	if err != nil {
		return err
	}
	previous, err := readFile(j.Receipt.EnvironmentBackup, 64<<10)
	if err != nil {
		return err
	}
	if string(current) != string(previous) {
		return fail(j, "production environment changed; approved data restoration is blocked")
	}
	if j.Phase == "service_start_requested" {
		if !old.State.Running {
			return errors.New("previous service start is unresolved; do not dispatch another start")
		}
		if old.State.Health.Status != "healthy" {
			j.Reason = "restored previous service is awaiting health"
			return nil
		}
		j.Receipt.Recovery = "Owner-approved final snapshot restored; previous image is healthy. Failed release and migration remain recorded."
		j.Outcome = "restored"
		return done(j, j.Receipt.Recovery)
	}
	if old.State.Running {
		return fail(j, "application is running; approved stopped-data restoration is blocked")
	}
	migration, err := b.inspect(ctx, "runner-migration-"+j.Request.RunID)
	if err != nil {
		return err
	}
	if migration.State.Running || migration.State.Status != "exited" {
		return errors.New("migration has not settled; restoration is blocked")
	}
	name := "runner-restore-" + j.Request.Key
	if j.Phase == "" {
		hash, err := b.closedFingerprint(ctx, j)
		if err != nil {
			return err
		}
		if hash != j.Receipt.DataSHA {
			return fail(j, "data changed after the restoration decision; preserve it")
		}
		data, err := readFile(j.Receipt.FinalBackup, 256<<20)
		if err != nil {
			return err
		}
		if Digest(data) != j.Receipt.FinalBackupSHA {
			return fail(j, "approved recovery snapshot differs")
		}
		remoteBackup := filepath.Join(j.Receipt.Remote, "approved-restore.db")
		if _, err = b.host(ctx, data, "tee", remoteBackup); err != nil {
			return err
		}
		if _, err = b.host(ctx, nil, "chmod", "0444", remoteBackup); err != nil {
			return err
		}
		helper := filepath.Join(j.Receipt.Remote, "approved-restore-helper")
		if _, err = b.host(ctx, nil, "install", "-m", "0555", filepath.Join(filepath.Dir(b.Config.BackupHelper), "runner-snapshot"), helper); err != nil {
			return err
		}
		group, err := b.host(ctx, nil, "stat", "-c", "%g", filepath.Join(b.Config.DataDir, "games.db"))
		if err != nil {
			return err
		}
		gid := strings.TrimSpace(string(group))
		if !regexp.MustCompile(`^[0-9]+$`).MatchString(gid) {
			return fail(j, "database group identity is unavailable")
		}
		if err = intent(j, "restore_create_requested", save); err != nil {
			return err
		}
		_, err = b.docker(ctx, "create", "--name", name, "--label", "cortexium.runner.run="+j.Request.RunID, "--pull=never", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "65532:65532", "--group-add", gid, "--memory", "256m", "--pids-limit", "32", "--entrypoint", "/recovery-helper", "--mount", "type=bind,src="+b.Config.DataDir+",dst=/data", "--mount", "type=bind,src="+remoteBackup+",dst=/backup.db,readonly", "--mount", "type=bind,src="+helper+",dst=/recovery-helper,readonly", j.Receipt.Image, "--restore-from", "/backup.db", "--source", "/data/games.db", "--expected-source-sha", j.Receipt.DataSHA, "--backup-sha", j.Receipt.FinalBackupSHA)
		return err
	}
	recovery, err := b.inspect(ctx, name)
	if err != nil {
		return err
	}
	if recovery.Image != j.Receipt.Image || recovery.Config.Labels["cortexium.runner.run"] != j.Request.RunID {
		return fail(j, "recovery container identity differs")
	}
	if recovery.State.Status == "created" {
		if j.Phase == "restore_start_requested" {
			return errors.New("restoration start acknowledgement is unresolved; retain the same container")
		}
		if err = intent(j, "restore_start_requested", save); err != nil {
			return err
		}
		_, err = b.docker(ctx, "start", name)
		return err
	}
	if recovery.State.Running {
		return nil
	}
	if recovery.State.Status != "exited" || recovery.State.ExitCode != 0 {
		return fail(j, "approved restoration failed; retain its container and all recovery files")
	}
	hash, err := b.closedFingerprint(ctx, j)
	if err != nil {
		return err
	}
	if hash != j.Receipt.FinalBackupSHA {
		return fail(j, "restored database differs from the approved backup")
	}
	if err = intent(j, "service_start_requested", save); err != nil {
		return err
	}
	_, err = b.docker(ctx, "start", old.ID)
	return err
}
