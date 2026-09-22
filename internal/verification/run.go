// Package verification owns supported heavyweight entrypoints. It coordinates
// one host-local resource without changing agent admission or granting access.
package verification

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

// Observation is freshly collected by Runner, never supplied by a model or
// copied from a receipt. Integrity protects the full candidate separately from
// executable-check applicability.
type Observation struct {
	CommitOID string
	TreeOID   string
	BaseOID   string
	Integrity string
	Inputs    execution.VerificationInputs
}

type Request struct {
	Entrypoint   string
	Entry        config.VerificationEntrypoint
	Directory    string
	Repository   string
	AttemptID    string
	PlanID       string
	PlanRevision string
	Boundary     execution.VerificationBoundary
	// Only independently protected coordinator evidence may supply this pair.
	// A missing pair means run; a supplied tampered pair fails closed.
	PreviousReceipt *execution.VerificationReceipt
	PreviousDigest  string
	// Observe revalidates authority, config, containment and the full candidate
	// before waiting, at grant, after preparation and after verification/cleanup.
	// Direct host execution requires already-approved host access.
	Observe func(context.Context) (Observation, error)
}

type Result struct {
	Receipt    execution.VerificationReceipt `json:"receipt"`
	Digest     string                        `json:"digest"`
	Output     subprocess.Result             `json:"output"`
	Historical bool                          `json:"historical"`
	// A historical check retains its original receipt. Any preparation done in
	// this invocation is reported separately, not spliced into historical proof.
	CurrentPreparation *execution.VerificationPreparationReceipt `json:"current_preparation,omitempty"`
	PreparationOutput  *subprocess.Result                        `json:"preparation_output,omitempty"`
}

// Run owns one shared claim and deadline through preparation, check, descendant
// cleanup and final authoritative observation. Neither a receipt nor its printed
// hash confers authority; the coordinator independently protects provenance.
func Run(ctx context.Context, req Request) (Result, error) {
	return run(ctx, req, subprocess.AcquireHeavyVerification)
}

func run(ctx context.Context, req Request, acquire func(context.Context) (context.Context, *subprocess.HeavyClaim, error)) (result Result, err error) {
	if req.Observe == nil || req.Entry.Command == "" || req.Entry.TimeoutSeconds <= 0 || req.Entrypoint == "" {
		return result, errors.New("verification requires a configured entrypoint and authoritative observation")
	}
	if (req.PreviousReceipt == nil) != (req.PreviousDigest == "") {
		return result, errors.New("historical verification requires protected receipt and digest together")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Entry.TimeoutSeconds)*time.Second)
	defer cancel()
	started := time.Now().UTC()
	before, err := req.Observe(ctx)
	if err != nil {
		return result, err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, err
	}
	receipt := execution.VerificationReceipt{
		Version: 1, ExecutionID: "verify_" + hex.EncodeToString(nonce[:]), AttemptID: req.AttemptID,
		StartedAt: started, FinishedAt: started, Repository: req.Repository, PlanID: req.PlanID, PlanRevision: req.PlanRevision,
		SourceCommitOID: before.CommitOID, SourceTreeOID: before.TreeOID, SourceBaseOID: before.BaseOID,
		Entrypoint: req.Entrypoint, Command: append([]string{req.Entry.Command}, req.Entry.Args...),
		SettingsDigest: strings.TrimPrefix(req.Entry.Digest(), "v1:"), Inputs: before.Inputs, Boundary: req.Boundary,
		Outcome: "failed", ReportDigest: hash([]byte("not started")),
	}
	if before.Integrity == "" || before.Inputs.Configuration != receipt.SettingsDigest {
		return result, errors.New("verification candidate integrity or selected settings unavailable")
	}
	if _, err := receipt.Digest(); err != nil {
		return result, err
	}
	if _, err := applicablePrevious(req, before); err != nil {
		return result, err
	}
	waitStarted := time.Now()
	commandCtx, claim, err := acquire(ctx)
	waitMS := time.Since(waitStarted).Milliseconds()
	receipt.WaitMilliseconds = &waitMS
	if err != nil {
		receipt.FinishedAt, receipt.CleanupResolved = time.Now().UTC(), true
		receipt.Outcome = verificationOutcome(err)
		digest, digestErr := receipt.Digest()
		return Result{Receipt: receipt, Digest: digest}, errors.Join(fmt.Errorf("wait for supported heavy verification: %w", err), digestErr)
	}
	defer func() {
		finishErr := claim.Finish(err)
		err = errors.Join(err, finishErr)
		if result.Historical && err == nil {
			return
		}
		result.Historical = false
		receipt.FinishedAt = time.Now().UTC()
		var cleanup *subprocess.CleanupError
		receipt.CleanupResolved = !errors.As(err, &cleanup)
		if err != nil {
			receipt.Outcome = verificationOutcome(err)
		}
		digest, digestErr := receipt.Digest()
		result.Receipt, result.Digest = receipt, digest
		err = errors.Join(err, digestErr)
	}()
	current, err := req.Observe(ctx)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(before, current) {
		return result, errors.New("verification authority or candidate changed while waiting")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	tryReuse := func(observed Observation) (bool, error) {
		applicable, err := applicablePrevious(req, observed)
		if err != nil || !applicable {
			return false, err
		}
		after, err := req.Observe(ctx)
		if err != nil {
			return false, err
		}
		if !reflect.DeepEqual(observed, after) {
			return false, errors.New("verification bindings changed during reuse assessment")
		}
		result.Receipt, result.Digest, result.Historical = *req.PreviousReceipt, req.PreviousDigest, true
		result.CurrentPreparation = receipt.Preparation
		return true, nil
	}
	if reused, err := tryReuse(current); reused || err != nil {
		return result, err
	}
	if prep := req.Entry.Preparation; prep != nil {
		if err := validatePreparationCandidate(ctx, req.Directory, req.Entry); err != nil {
			return result, err
		}
		observeWrites := func() (string, error) {
			return collectContent(ctx, req.Directory, []string{"."}, append([]string{".git"}, req.Entry.DependencyPaths...), workspace.DefaultSnapshotLimits(), true)
		}
		protected, err := observeWrites()
		if err != nil {
			return result, fmt.Errorf("observe preparation write boundary: %w", err)
		}
		phase, output, runErr := runPhase(commandCtx, prep.Command, prep.Args, req.Directory)
		receipt.Preparation, result.PreparationOutput = &phase, &output
		if runErr != nil {
			return result, runErr
		}
		after, err := req.Observe(ctx)
		if err != nil {
			return result, err
		}
		protectedAfter, err := observeWrites()
		if err != nil {
			return result, err
		}
		// Only actual declared dependency contents may change during preparation.
		withoutDependencies := after
		withoutDependencies.Inputs.Dependencies = current.Inputs.Dependencies
		if protected != protectedAfter || !reflect.DeepEqual(current, withoutDependencies) {
			return result, errors.New("preparation changed protected source, configuration, runtime or undeclared files")
		}
		current, receipt.Inputs = after, after.Inputs
		if reused, err := tryReuse(current); reused || err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	phase, output, runErr := runPhase(commandCtx, req.Entry.Command, req.Entry.Args, req.Directory)
	result.Output = output
	receipt.RunStartedAt, receipt.RunFinishedAt = &phase.StartedAt, &phase.RunFinishedAt
	receipt.RunMilliseconds, receipt.CleanupMilliseconds = &phase.RunMilliseconds, phase.CleanupMilliseconds
	receipt.ReportDigest = phase.ReportDigest
	if runErr != nil {
		return result, runErr
	}
	after, err := req.Observe(ctx)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(current, after) {
		return result, errors.New("verification candidate or inputs changed during execution")
	}
	receipt.Outcome = "passed"
	return result, nil
}

func applicablePrevious(req Request, observed Observation) (bool, error) {
	if req.PreviousReceipt == nil {
		return false, nil
	}
	assessment, err := execution.AssessVerificationReceipt(*req.PreviousReceipt, req.PreviousDigest, execution.VerificationTarget{
		Repository: req.Repository, PlanID: req.PlanID, CandidateOID: observed.CommitOID, Entrypoint: req.Entrypoint,
		SettingsDigest: strings.TrimPrefix(req.Entry.Digest(), "v1:"), Inputs: observed.Inputs, Boundary: req.Boundary,
		RequireCurrentCandidate: req.Entry.RequireCurrentCandidate,
	})
	return assessment.Applicable, err
}

func runPhase(ctx context.Context, command string, args []string, directory string) (execution.VerificationPreparationReceipt, subprocess.Result, error) {
	phase := execution.VerificationPreparationReceipt{Command: append([]string{command}, args...), StartedAt: time.Now().UTC()}
	var cleanupStart time.Time
	ctx = subprocess.WithCleanupObserver(ctx, func() func(error) { cleanupStart = time.Now().UTC(); return func(error) {} })
	output, err := (subprocess.OSRunner{}).RunBoundedHeadTailInput(ctx, command, args, directory, 0, nil, 64*1024, "\n[verification output truncated]\n")
	phase.FinishedAt = time.Now().UTC()
	phase.RunFinishedAt = phase.FinishedAt
	if !cleanupStart.IsZero() {
		phase.RunFinishedAt = cleanupStart
		ms := phase.FinishedAt.Sub(cleanupStart).Milliseconds()
		phase.CleanupMilliseconds = &ms
	}
	phase.RunMilliseconds = phase.RunFinishedAt.Sub(phase.StartedAt).Milliseconds()
	encoded, _ := json.Marshal(output)
	phase.ReportDigest = hash(encoded)
	if err == nil && output.ExitCode != 0 {
		err = errors.New("verification command failed")
	}
	var cleanup *subprocess.CleanupError
	phase.CleanupResolved = !errors.As(err, &cleanup)
	phase.Outcome = verificationOutcome(err)
	return phase, output, err
}

func verificationOutcome(err error) string {
	var cleanup *subprocess.CleanupError
	switch {
	case errors.As(err, &cleanup):
		return "cleanup_unresolved"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case err == nil:
		return "passed"
	default:
		return "failed"
	}
}

func hash(data []byte) string { digest := sha256.Sum256(data); return hex.EncodeToString(digest[:]) }
