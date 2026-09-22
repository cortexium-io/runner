package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/verification"
)

// The CLI starts no worker, changes no Project state and grants no capability.
// Its observed receipt is standalone evidence, not plan approval or acceptance.
func runVerification(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlagSet("verify", "cortexium-runner verify --config PATH --entrypoint ID [--directory PATH] [--json] | --recover --dry-run | --recover --expect-token TOKEN", stdout)
	configPath := flags.String("config", "", "trusted operator configuration")
	entryID := flags.String("entrypoint", "", "configured heavy verification entrypoint")
	directory := flags.String("directory", ".", "clean committed worktree; permissions remain those of this process")
	jsonOutput := flags.Bool("json", false, "write observed receipt and bounded command output")
	recover := flags.Bool("recover", false, "inspect or recover an interrupted host-local heavy claim; never kills work")
	dryRun := flags.Bool("dry-run", false, "preview recovery without changing the claim")
	expectToken := flags.String("expect-token", "", "exact recovery preview token")
	proceed, err := parseFlags(flags, args, "verify")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("verify accepts configured entrypoint IDs, not commands or extra arguments")
	}
	if *recover {
		if *entryID != "" || *configPath != "" || *directory != "." || *dryRun && *expectToken != "" || !*dryRun && *expectToken == "" {
			return errors.New("recovery requires --dry-run or --expect-token TOKEN, without an entrypoint/configuration/directory")
		}
		result, err := subprocess.RecoverHeavyVerification(ctx, *expectToken, !*dryRun)
		if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
			return encodeErr
		}
		if err == nil && !*dryRun && !result.Cleared {
			return errors.New("heavy verification claim was not safely recoverable")
		}
		return err
	}
	if *dryRun || *expectToken != "" || *entryID == "" {
		return errors.New("verify requires --entrypoint ID; recovery flags require --recover")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadTrustedConfig(*configPath)
	if err != nil {
		return err
	}
	runtimeConfig, err := cfg.Resolve()
	if err != nil {
		return err
	}
	entry, ok := cfg.Verification[*entryID]
	if !ok {
		return errors.New("verification entrypoint is not configured")
	}
	root, err := filepath.Abs(*directory)
	if err != nil {
		return err
	}
	checkRepository := func(ctx context.Context) error {
		remote, err := subprocess.RunGit(ctx, subprocess.OSRunner{}, []string{"config", "--get", "remote." + runtimeConfig.GitHubProject.RemoteName + ".url"}, root, 10*time.Second)
		if err != nil || remote.ExitCode != 0 {
			return errors.New("verification repository remote is unavailable")
		}
		identity, err := github.RepositoryFromRemote(remote.Stdout)
		if err != nil || !strings.EqualFold(identity, runtimeConfig.GitHubProject.IntakeRepository) {
			return errors.New("verification worktree does not match the configured repository")
		}
		return nil
	}
	if err := checkRepository(ctx); err != nil {
		return err
	}
	ref := runtimeConfig.GitHubProject.RemoteName + "/" + runtimeConfig.GitHubProject.BaseBranch
	base, err := subprocess.RunGit(ctx, subprocess.OSRunner{}, []string{"rev-parse", "--verify", "--end-of-options", ref + "^{commit}"}, root, 30*time.Second)
	if err != nil || base.ExitCode != 0 {
		return errors.New("verification destination base is unavailable")
	}
	baseOID := strings.TrimSpace(base.Stdout)
	request := verification.Request{Entrypoint: *entryID, Entry: entry, Directory: root, Repository: runtimeConfig.GitHubProject.IntakeRepository,
		AttemptID: "standalone-verification", Boundary: execution.VerificationFocused,
		Observe: func(observeCtx context.Context) (verification.Observation, error) {
			current, err := config.LoadTrustedConfig(*configPath)
			if err != nil {
				return verification.Observation{}, err
			}
			if !reflect.DeepEqual(cfg, current) {
				return verification.Observation{}, errors.New("operator configuration changed during verification")
			}
			if err := checkRepository(observeCtx); err != nil {
				return verification.Observation{}, err
			}
			return verification.ObserveCandidate(observeCtx, root, baseOID, "Standalone configured check: "+*entryID, entry)
		},
	}
	result, runErr := verification.Run(ctx, request)
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			return err
		}
	} else {
		if result.Output.Stdout != "" {
			fmt.Fprint(stdout, terminalSafeText(result.Output.Stdout))
		}
		if result.Output.Stderr != "" {
			fmt.Fprint(stdout, terminalSafeText(result.Output.Stderr))
		}
		if result.Digest != "" {
			fmt.Fprintf(stdout, "\nVerification %s; receipt %s (standalone observed evidence, not plan acceptance).\n", result.Receipt.Outcome, result.Digest)
		}
	}
	return runErr
}
