package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
)

type CheckRunner interface {
	Check(context.Context, string, config.QualityCheck, string, func(core.Process) error) core.CheckObservation
}

// Check executes an owner-approved command, with no model and no ambient secrets.
func (c Codex) Check(ctx context.Context, root string, check config.QualityCheck, evidence string, started func(core.Process) error) core.CheckObservation {
	result := core.CheckObservation{CheckID: check.ID, Status: "unavailable", ExitCode: -1, Evidence: evidence, Stopped: true}
	fail := func(err error) core.CheckObservation {
		result.Status = "unavailable"
		result.Detail = err.Error()
		return result
	}
	if err := securefs.EnsurePrivateDir(filepath.Dir(evidence)); err != nil {
		return fail(err)
	}
	for _, name := range check.Prerequisites {
		found := false
		for _, dir := range filepath.SplitList(check.Path) {
			if !filepath.IsAbs(dir) {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
				found = true
				break
			}
		}
		if !found {
			return fail(fmt.Errorf("missing executable prerequisite %s", name))
		}
	}
	dir := filepath.Join(root, check.Directory)
	result.Directory = dir
	if !config.Within(dir, root) {
		return fail(errors.New("check directory escapes source copy"))
	}
	if err := config.NoSymlinks(dir); err != nil {
		return fail(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fail(err)
	}
	if !info.IsDir() {
		return fail(errors.New("check directory is not a directory"))
	}
	private := filepath.Join(root, ".runner-output", check.ID)
	if err := config.NoSymlinks(private); err != nil {
		return fail(err)
	}
	for _, name := range []string{"home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(private, name), 0700); err != nil {
			return fail(err)
		}
	}
	log, err := os.OpenFile(evidence, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(err)
	}
	defer log.Close()
	output := &limitedWriter{writer: log, remaining: 8 << 20}
	policy := c.policy(root, nil)
	args := []string{"sandbox", "-C", root, "-P", "runner_local"}
	args = append(args, policy[1:]...)
	args = append(args, "--", "/usr/bin/env", "-i", "PATH="+check.Path, "HOME="+filepath.Join(private, "home"), "TMPDIR="+filepath.Join(private, "tmp"), "LANG=C", "LC_ALL=C", "/bin/sh", "-c", `cd "$1" && exec /bin/sh -c "$2"`, "runner-check", dir, check.Command)
	timeout, err := time.ParseDuration(check.Timeout)
	if err != nil || timeout <= 0 {
		return fail(errors.New("invalid check timeout"))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, result.Stopped, err = Run(ctx, c.command(), args, root, "", output, output, started)
	if syncErr := log.Sync(); syncErr != nil {
		return fail(syncErr)
	}
	if !result.Stopped || ctx.Err() != nil {
		if err == nil {
			err = errors.New("unresolved check process ownership")
		}
		return fail(err)
	}
	if output.remaining == 0 {
		return fail(errors.New("check output exceeded evidence limit"))
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return fail(err)
		}
		result.ExitCode = exit.ExitCode()
		if result.ExitCode <= 0 || result.ExitCode >= 125 {
			return fail(fmt.Errorf("check could not run reliably: %w", err))
		}
		result.Status = "failed"
	} else {
		result.ExitCode = 0
		result.Status = "passed"
	}
	data, err := os.ReadFile(evidence)
	if err != nil {
		return fail(err)
	}
	digest := sha256.Sum256(data)
	result.OutputDigest = hex.EncodeToString(digest[:])
	return result
}
