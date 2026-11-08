package release

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// An observed nonzero process exit differs from a lost connection. In
// particular SSH 255 and cancelled clients never establish remote settlement.
type commandFailure struct {
	program string
	code    int
}

type commandUncertain struct{ program string }
type connectionUnavailable struct{}

// This fact cannot authorize reconnect by itself. Only isolated readiness may
// promote it after proving that its exact container never started and is gone.
var errStartBeforeAuthentication = errors.New("Docker start failed before SSH authentication")

func (*connectionUnavailable) Error() string { return "SSH connection failed before remote dispatch" }

func (e *commandUncertain) Error() string {
	return "operator " + e.program + " outcome or process settlement is unknown"
}

func (e *commandFailure) Error() string {
	return fmt.Sprintf("operator %s exited %d", e.program, e.code)
}

type proofFailure struct{ reason string }

func (e *proofFailure) Error() string { return e.reason }

func knownFailure(err error) bool {
	var command *commandFailure
	var proof *proofFailure
	var path *os.PathError
	var connection *connectionUnavailable
	return errors.As(err, &command) || errors.As(err, &proof) || errors.As(err, &path) || errors.As(err, &connection)
}

func (b *Live) closedFingerprint(ctx context.Context, j *Job) (string, error) {
	c := b.Config
	data, err := b.host(ctx, nil, filepath.Join(filepath.Dir(c.BackupHelper), "runner-snapshot"),
		"--fingerprint", "--source", filepath.Join(c.DataDir, "games.db"),
		"--engine", c.Engine, "--container", j.Receipt.PriorContainer, "--image", j.Receipt.PriorImage, "--project", c.DeployProject)
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
		return "", errors.New("closed-data fingerprint is invalid")
	}
	return hash, nil
}

// This is compensation for a failed read-only backup, never a database rollback.
// Once migration might have started, this recovery is unavailable.
func (b *Live) recoverBeforeMigration(ctx context.Context, j *Job, save func() error) error {
	beforeIntent := j.Request.Action == "migrate" && j.Phase == "" && j.Receipt.DataSHA == j.StoppedSHA
	backupFailure := j.Request.Action == "stop" && j.Receipt.FinalBackup == ""
	cancelledStop := j.Request.Action == "recover-stop" && j.Receipt.DataSHA == j.StoppedSHA
	if !b.Config.RecoverBeforeMigration || (!beforeIntent && !backupFailure && !cancelledStop) || j.StoppedSHA == "" {
		return fail(j, "pre-migration recovery is not authorized or its boundary differs; owner inspection required")
	}
	current, err := readFile(b.Config.Environment, 64<<10)
	if err != nil {
		return err
	}
	previous, err := readFile(j.Receipt.EnvironmentBackup, 64<<10)
	if err != nil {
		return err
	}
	if string(current) != string(previous) || dotenvValue(current, "GAMES_IMAGE") != j.Receipt.PriorImage {
		return fail(j, "environment changed; previous service restart is blocked")
	}
	// An accepted stop receipt is required for any downstream migration. Also
	// reject externally created migration containers carrying this run identity.
	migrations, err := b.docker(ctx, "ps", "-a", "--filter", "label=cortexium.runner.run="+j.Request.RunID, "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if len(strings.Fields(string(migrations))) != 0 {
		return fail(j, "migration effects may exist; previous service restart is blocked")
	}
	c, err := b.existing(ctx, j)
	if err != nil {
		return err
	}
	if !c.State.Running {
		if j.Recovery == "restart_requested" {
			return errors.New("previous service start acknowledgement is unresolved; retain the recovery operation")
		}
		hash, err := b.closedFingerprint(ctx, j)
		if err != nil {
			return err
		}
		if hash != j.StoppedSHA {
			return fail(j, "database changed after stop; previous service restart is blocked")
		}
		j.Recovery = "restart_requested"
		if err = save(); err != nil {
			return err
		}
		_, err = b.docker(ctx, "start", c.ID)
		return err
	}
	if j.Recovery != "restart_requested" {
		return fail(j, "previous service started outside this recovery; inspect the retained operation")
	}
	if c.State.Health.Status != "healthy" {
		j.Reason = "previous service restarted; waiting for its health check"
		return nil
	}
	j.Receipt.Recovery = "unchanged previous service restored and healthy; no migration or database restoration performed"
	return fail(j, j.Failure+"; "+j.Receipt.Recovery)
}
