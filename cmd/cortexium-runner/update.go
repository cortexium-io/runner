package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cortexium-io/runner/internal/updater"
)

func runUpdate(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlagSet("update", "cortexium-runner update [--check] [--version vMAJOR.MINOR.PATCH]", stdout)
	check := flags.Bool("check", false, "check for an update without changing the installed binary")
	target := flags.String("version", "", "install a specific release version instead of the latest")
	proceed, err := parseFlags(flags, args, "update")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("update does not accept positional arguments")
	}
	if *check && strings.TrimSpace(*target) != "" {
		return errors.New("--check and --version cannot be used together")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}
	releasesURL := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_RELEASES_URL"))
	result, err := updater.Run(ctx, updater.Options{
		CurrentVersion: buildVersion(), TargetVersion: *target, ExecutablePath: executable,
		ReleasesURL: releasesURL, CheckOnly: *check,
	})
	if err != nil {
		return err
	}
	if *check {
		if result.UpdateAvailable {
			fmt.Fprintf(stdout, "Update available: %s -> %s\n", result.CurrentVersion, result.TargetVersion)
		} else {
			fmt.Fprintf(stdout, "cortexium-runner %s is up to date.\n", result.CurrentVersion)
		}
		return nil
	}
	if !result.Updated {
		fmt.Fprintf(stdout, "cortexium-runner %s is already up to date.\n", result.CurrentVersion)
		return nil
	}
	fmt.Fprintf(stdout, "Updated cortexium-runner %s -> %s at %s\n", result.CurrentVersion, result.TargetVersion, result.ExecutablePath)
	return nil
}
