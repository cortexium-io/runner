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
	"os/exec"
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
	// Only the preparation boundary may compare this projection instead of
	// Integrity. Full Integrity remains pinned during waiting and every check.
	PreparationIntegrity string
	Inputs               execution.VerificationInputs
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
	// Receipt/Digest are the heavy check only. Nil means it never ran and no
	// protected historical check was supplied. Historical also describes retained
	// proof on failure, not a claim that this invocation passed or reused it.
	Receipt                *execution.VerificationReceipt                      `json:"receipt,omitempty"`
	Digest                 string                                              `json:"digest,omitempty"`
	Output                 subprocess.Result                                   `json:"output"`
	Historical             bool                                                `json:"historical"`
	Invocation             InvocationObservation                               `json:"invocation"`
	CurrentCandidateCheck  *execution.VerificationCurrentCandidateCheckReceipt `json:"current_candidate_check,omitempty"`
	CurrentCandidateOutput *subprocess.Result                                  `json:"current_candidate_output,omitempty"`
	// A historical check retains its original receipt. Any preparation done in
	// this invocation is reported separately, not spliced into historical proof.
	CurrentPreparation *execution.VerificationPreparationReceipt `json:"current_preparation,omitempty"`
	PreparationOutput  *subprocess.Result                        `json:"preparation_output,omitempty"`
	// Set only after successful supervised preparation and unchanged protected
	// source/control/input observations. This is not passing check evidence.
	PreparedIntegrity string `json:"prepared_integrity,omitempty"`
}

// InvocationObservation describes the present attempt, including refusals that
// ran no check. It must not be confused with historical execution accounting.
type InvocationObservation struct {
	ExecutionID      string    `json:"execution_id"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	Outcome          string    `json:"outcome"`
	CleanupResolved  bool      `json:"cleanup_resolved"`
	WaitMilliseconds *int64    `json:"wait_ms,omitempty"`
}

func (r Result) Evidence() execution.VerificationEnvelope {
	return execution.VerificationEnvelope{Version: 1, Heavy: r.Receipt, CurrentCandidateCheck: r.CurrentCandidateCheck}
}

// finish keeps observation finalization and the failure classification at the
// same boundary, after the claim has actually been released or quarantined.
func (result *Result) finish(err, finishErr, contextErr error, candidateFailure *CheckFailure) error {
	if finishErr != nil {
		var cleanup *subprocess.CleanupError
		if !errors.As(finishErr, &cleanup) {
			finishErr = &subprocess.CleanupError{Err: finishErr}
		}
	}
	err = errors.Join(err, finishErr, contextErr)
	result.Invocation.FinishedAt = time.Now().UTC()
	var cleanup *subprocess.CleanupError
	result.Invocation.CleanupResolved = !errors.As(err, &cleanup)
	// Never relabel historical truth. Only receipts observed by this invocation
	// receive its final cleanup/context failure and enclosing finish time.
	invalidate := finishErr != nil || contextErr != nil
	if r := result.Receipt; r != nil && !result.Historical {
		r.FinishedAt = result.Invocation.FinishedAt
		if invalidate {
			r.Outcome, r.CleanupResolved = verificationOutcome(err), result.Invocation.CleanupResolved
		}
		var digestErr error
		result.Digest, digestErr = r.Digest()
		err = errors.Join(err, digestErr)
		if digestErr != nil {
			candidateFailure = nil
		}
	}
	if r := result.CurrentCandidateCheck; r != nil {
		r.FinishedAt = result.Invocation.FinishedAt
		if invalidate {
			r.Outcome, r.CleanupResolved = verificationOutcome(err), result.Invocation.CleanupResolved
		}
	}
	if result.Receipt != nil || result.CurrentCandidateCheck != nil {
		_, digestErr := result.Evidence().Digest()
		err = errors.Join(err, digestErr)
		if digestErr != nil {
			candidateFailure = nil
		}
	}
	if candidateFailure != nil && !invalidate {
		err = candidateFailure
	}
	result.Invocation.Outcome = verificationOutcome(err)
	return err
}

// CheckFailure is emitted only for a normal nonzero supported check exit after
// authoritative post-observation and resolved claim cleanup. Neither stderr nor
// preparation, start failures, timeout or missing proof produce this marker.
// The coordinator still must protect evidence, revalidate authority and apply
// its existing bounded repair policy; this error grants no mutation authority.
type CheckFailure struct {
	Phase       string
	ExecutionID string
	ExitCode    int
	cause       error
}

func (e *CheckFailure) Error() string {
	return fmt.Sprintf("supported %s check exited %d", e.Phase, e.ExitCode)
}
func (e *CheckFailure) Unwrap() error { return e.cause }

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
	result.Invocation.StartedAt = started
	var claim *subprocess.HeavyClaim
	var candidateFailure *CheckFailure
	defer func() {
		finishErr := claim.Finish(err)
		err = result.finish(err, finishErr, ctx.Err(), candidateFailure)
	}()
	before, err := req.Observe(ctx)
	if err != nil {
		return result, err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, err
	}
	result.Invocation.ExecutionID = "verify_" + hex.EncodeToString(nonce[:])
	receipt := execution.VerificationReceipt{
		Version: 1, ExecutionID: result.Invocation.ExecutionID + "/heavy", AttemptID: req.AttemptID,
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
	if req.PreviousReceipt != nil {
		previous := *req.PreviousReceipt
		result.Receipt, result.Digest, result.Historical = &previous, req.PreviousDigest, true
	}
	waitStarted := time.Now()
	commandCtx, acquired, err := acquire(ctx)
	claim = acquired
	waitMS := time.Since(waitStarted).Milliseconds()
	receipt.WaitMilliseconds = &waitMS
	result.Invocation.WaitMilliseconds = &waitMS
	if err != nil {
		return result, fmt.Errorf("wait for supported heavy verification: %w", err)
	}
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
	// Every actual check, including normal nonzero exits, is followed by the
	// same authoritative observation before it can be considered a check result.
	observeUnchanged := func(observed Observation) error {
		after, err := req.Observe(ctx)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(observed, after) {
			return errors.New("verification authority, candidate or inputs changed during execution")
		}
		return ctx.Err()
	}
	runGuard := func(observed Observation) error {
		guard := req.Entry.CurrentCandidateCheck
		if guard == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		phase, output, runErr := runPhase(commandCtx, guard.Command, guard.Args, req.Directory)
		result.CurrentCandidateOutput = &output
		// Cleanup observation occurs only after a successful process start. A
		// refused start has diagnostic output but creates no executed-check proof.
		if phase.CleanupMilliseconds == nil {
			return errors.Join(runErr, errors.New("current-candidate check has no observed supervised process"))
		}
		r := &execution.VerificationCurrentCandidateCheckReceipt{
			ExecutionID: result.Invocation.ExecutionID + "/current", AttemptID: req.AttemptID,
			Repository: req.Repository, PlanID: req.PlanID, PlanRevision: req.PlanRevision,
			Entrypoint: req.Entrypoint, Boundary: req.Boundary, SourceCommitOID: observed.CommitOID,
			SourceTreeOID: observed.TreeOID, SourceBaseOID: observed.BaseOID, CandidateIntegrity: observed.Integrity,
			SettingsDigest: receipt.SettingsDigest, Command: phase.Command,
			StartedAt: started, FinishedAt: phase.FinishedAt, RunStartedAt: &phase.StartedAt, RunFinishedAt: &phase.RunFinishedAt,
			RunMilliseconds: &phase.RunMilliseconds, CleanupMilliseconds: phase.CleanupMilliseconds,
			Outcome: phase.Outcome, ExitCode: phase.ExitCode, ReportDigest: phase.ReportDigest, CleanupResolved: phase.CleanupResolved,
		}
		result.CurrentCandidateCheck = r
		if runErr != nil && !normalCheckFailure(runErr, phase) {
			return runErr
		}
		if observeErr := observeUnchanged(observed); observeErr != nil {
			r.Outcome = verificationOutcome(observeErr)
			return errors.Join(runErr, observeErr)
		}
		if runErr != nil {
			candidateFailure = &CheckFailure{Phase: "current_candidate_check", ExecutionID: r.ExecutionID, ExitCode: *phase.ExitCode, cause: runErr}
		}
		return runErr
	}
	tryReuse := func(observed Observation) (bool, error) {
		applicable, err := applicablePrevious(req, observed)
		if err != nil || !applicable {
			return false, err
		}
		if err := runGuard(observed); err != nil {
			return false, err
		}
		if req.Entry.CurrentCandidateCheck == nil {
			if err := observeUnchanged(observed); err != nil {
				return false, err
			}
		}
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
		receipt.Preparation, result.CurrentPreparation, result.PreparationOutput = &phase, &phase, &output
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
		withoutDependencies.Integrity = current.Integrity
		if current.PreparationIntegrity == "" || protected != protectedAfter || !reflect.DeepEqual(current, withoutDependencies) {
			return result, errors.New("preparation changed protected source, configuration, runtime or undeclared files")
		}
		result.PreparedIntegrity = after.Integrity
		current, receipt.Inputs = after, after.Inputs
		if reused, err := tryReuse(current); reused || err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := runGuard(current); err != nil {
		return result, err
	}
	phase, output, runErr := runPhase(commandCtx, req.Entry.Command, req.Entry.Args, req.Directory)
	result.Output = output
	if phase.CleanupMilliseconds == nil {
		return result, errors.Join(runErr, errors.New("heavy check has no observed supervised process"))
	}
	result.Receipt, result.Historical = &receipt, false
	receipt.RunStartedAt, receipt.RunFinishedAt = &phase.StartedAt, &phase.RunFinishedAt
	receipt.RunMilliseconds, receipt.CleanupMilliseconds = &phase.RunMilliseconds, phase.CleanupMilliseconds
	receipt.ReportDigest = phase.ReportDigest
	if phase.ExitCode != nil {
		receipt.ExitCode = phase.ExitCode
	}
	receipt.Outcome, receipt.CleanupResolved = phase.Outcome, phase.CleanupResolved
	if runErr != nil && !normalCheckFailure(runErr, phase) {
		return result, runErr
	}
	if observeErr := observeUnchanged(current); observeErr != nil {
		receipt.Outcome = verificationOutcome(observeErr)
		return result, errors.Join(runErr, observeErr)
	}
	if runErr != nil {
		candidateFailure = &CheckFailure{Phase: "heavy", ExecutionID: receipt.ExecutionID, ExitCode: *phase.ExitCode, cause: runErr}
	}
	return result, runErr
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
	if !cleanupStart.IsZero() && output.ExitCode >= 0 {
		code := output.ExitCode
		phase.ExitCode = &code
	}
	if err == nil && output.ExitCode != 0 {
		err = errors.New("verification command failed")
	}
	var cleanup *subprocess.CleanupError
	phase.CleanupResolved = !errors.As(err, &cleanup)
	phase.Outcome = verificationOutcome(err)
	return phase, output, err
}

func normalExitError(err error, code int) bool {
	// A bare OS ExitError identifies normal command termination. Joined cleanup
	// or I/O failures are deliberately ineligible, even if an exit code is known.
	exit, ok := err.(*exec.ExitError)
	return ok && exit.ProcessState != nil && exit.ProcessState.Exited() && exit.ExitCode() == code
}

func normalCheckFailure(err error, phase execution.VerificationPreparationReceipt) bool {
	return phase.Outcome == "failed" && phase.CleanupResolved && phase.ExitCode != nil && *phase.ExitCode > 0 && normalExitError(err, *phase.ExitCode)
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
