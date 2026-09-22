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
)

// Observation is freshly collected by Runner, never supplied by the model or
// copied from a receipt. Integrity protects the complete candidate separately
// from the narrower input set that governs executable-check applicability.
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
	// Observe must also revalidate authority, selected configuration and the
	// execution permission boundary. It runs before waiting, at grant and after
	// cleanup. A direct host invocation requires already-approved host access;
	// otherwise the launcher must itself run inside the existing containment.
	Observe func(context.Context) (Observation, error)
}

type Result struct {
	Receipt execution.VerificationReceipt `json:"receipt"`
	Digest  string                        `json:"digest"`
	Output  subprocess.Result             `json:"output"`
}

// Run uses configured literal argv only. Its timeout includes claim waiting;
// cleanup remains bounded by the existing subprocess supervisor. A caller must
// retain Digest in its protected acceptance record: printing a receipt or its
// hash does not confer authority or authenticate a model's evidence claim.
func Run(ctx context.Context, req Request) (Result, error) {
	return run(ctx, req, subprocess.AcquireHeavyVerification)
}

func run(ctx context.Context, req Request, acquire func(context.Context) (context.Context, *subprocess.HeavyClaim, error)) (result Result, err error) {
	if req.Observe == nil || req.Entry.Command == "" || req.Entry.TimeoutSeconds <= 0 || req.Entrypoint == "" {
		return result, errors.New("verification requires a configured entrypoint and authoritative observation")
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
	if before.Integrity == "" {
		return result, errors.New("verification candidate integrity is unavailable")
	}
	if before.Inputs.Configuration != receipt.SettingsDigest {
		return result, errors.New("observed verification settings differ from the selected entrypoint")
	}
	if _, err := receipt.Digest(); err != nil {
		return result, err
	}
	waitStarted := time.Now()
	commandCtx, claim, err := acquire(ctx)
	if err != nil {
		waitMS := time.Since(waitStarted).Milliseconds()
		receipt.WaitMilliseconds = &waitMS
		receipt.FinishedAt = time.Now().UTC()
		receipt.CleanupResolved = true // This invocation never owned execution.
		if errors.Is(err, context.Canceled) {
			receipt.Outcome = "canceled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			receipt.Outcome = "timeout"
		}
		digest, digestErr := receipt.Digest()
		return Result{Receipt: receipt, Digest: digest}, errors.Join(fmt.Errorf("wait for supported heavy verification: %w", err), digestErr)
	}
	finished := false
	defer func() {
		if !finished {
			// Authority or inputs can change after waiting. Preserve the observed
			// wait even when no command was admitted, without inventing run time.
			finishErr := claim.Finish(nil)
			err = errors.Join(err, finishErr)
			receipt.CleanupResolved = finishErr == nil
			receipt.FinishedAt = time.Now().UTC()
			switch {
			case finishErr != nil:
				receipt.Outcome = "cleanup_unresolved"
			case errors.Is(err, context.DeadlineExceeded):
				receipt.Outcome = "timeout"
			case errors.Is(err, context.Canceled):
				receipt.Outcome = "canceled"
			}
			digest, digestErr := receipt.Digest()
			result = Result{Receipt: receipt, Digest: digest}
			err = errors.Join(err, digestErr)
		}
	}()
	waitMS := time.Since(waitStarted).Milliseconds()
	receipt.WaitMilliseconds = &waitMS
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
	executionStarted := time.Now()
	var cleanupStart time.Time
	commandCtx = subprocess.WithCleanupObserver(commandCtx, func() func(error) {
		cleanupStart = time.Now()
		return func(error) {}
	})
	output, runErr := (subprocess.OSRunner{}).RunBoundedHeadTailInput(commandCtx, req.Entry.Command, req.Entry.Args, req.Directory, 0, nil, 64*1024, "\n[verification output truncated]\n")
	finishErr := claim.Finish(runErr)
	finished = true
	runErr = errors.Join(runErr, finishErr)
	completed := time.Now()
	runUntil := completed
	if !cleanupStart.IsZero() {
		runUntil = cleanupStart
		cleanupMS := completed.Sub(cleanupStart).Milliseconds()
		receipt.CleanupMilliseconds = &cleanupMS
	}
	runMS := runUntil.Sub(executionStarted).Milliseconds()
	receipt.RunMilliseconds = &runMS
	runStartedAt, runFinishedAt := executionStarted.UTC(), runUntil.UTC()
	receipt.RunStartedAt, receipt.RunFinishedAt = &runStartedAt, &runFinishedAt
	receipt.FinishedAt = completed.UTC()
	report, _ := json.Marshal(output)
	receipt.ReportDigest = hash(report)
	var cleanup *subprocess.CleanupError
	receipt.CleanupResolved = !errors.As(runErr, &cleanup)
	switch {
	case !receipt.CleanupResolved:
		receipt.Outcome = "cleanup_unresolved"
	case errors.Is(runErr, context.DeadlineExceeded):
		receipt.Outcome = "timeout"
	case errors.Is(runErr, context.Canceled):
		receipt.Outcome = "canceled"
	case runErr == nil && output.ExitCode == 0:
		after, observeErr := req.Observe(ctx)
		if observeErr != nil {
			runErr = observeErr
		} else if !reflect.DeepEqual(before, after) {
			runErr = errors.New("verification candidate or inputs changed during execution")
		} else {
			receipt.Outcome = "passed"
		}
	case runErr == nil:
		runErr = errors.New("verification command failed")
	}
	digest, digestErr := receipt.Digest()
	result = Result{Receipt: receipt, Digest: digest, Output: output}
	return result, errors.Join(runErr, digestErr)
}

func hash(data []byte) string { digest := sha256.Sum256(data); return hex.EncodeToString(digest[:]) }
