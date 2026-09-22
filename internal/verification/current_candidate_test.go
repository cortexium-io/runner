package verification

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func bindCurrentCandidateObservation(t *testing.T, request *Request) {
	t.Helper()
	base := strings.TrimSpace(git(t, request.Directory, "rev-parse", "HEAD"))
	request.Observe = func(ctx context.Context) (Observation, error) {
		return ObserveCandidate(ctx, request.Directory, base, "approved", request.Entry)
	}
}

func TestCurrentCandidateCheckRunsBeforeBothHistoricalReusePaths(t *testing.T) {
	for _, restoreDependencies := range []bool{false, true} {
		t.Run(map[bool]string{false: "at grant", true: "after preparation"}[restoreDependencies], func(t *testing.T) {
			request := preparationFixture(t, "mkdir -p deps; printf prepared > deps/prepared")
			marker := filepath.Join(t.TempDir(), "guard-runs")
			request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", "printf current >> \"$1\"", "fixture", marker}}
			bindCurrentCandidateObservation(t, &request)
			first, err := run(t.Context(), request, ownedTestGrant)
			if err != nil {
				t.Fatal(err)
			}
			original, _ := json.Marshal(first.Receipt)
			writeFixture(t, request.Directory, "docs/proof.txt", "new screenshots and receipts, no executable change")
			git(t, request.Directory, "add", "docs/proof.txt")
			git(t, request.Directory, "commit", "-m", "new evidence")
			if restoreDependencies {
				if err := os.Remove(filepath.Join(request.Directory, "deps/prepared")); err != nil {
					t.Fatal(err)
				}
			}
			writeFixture(t, request.Directory, "dist/output", "heavy must not repeat")
			request.PreviousReceipt, request.PreviousDigest = first.Receipt, first.Digest
			grants := 0
			second, err := run(t.Context(), request, func(ctx context.Context) (context.Context, *subprocess.HeavyClaim, error) {
				grants++
				return ownedTestGrant(ctx)
			})
			if err != nil || !second.Historical || grants != 1 || second.CurrentCandidateCheck == nil {
				t.Fatalf("reuse guard missing: %+v %v grants=%d", second, err, grants)
			}
			after, _ := json.Marshal(second.Receipt)
			if string(original) != string(after) || first.Digest != second.Digest {
				t.Fatal("guard/reuse rewrote historical heavy truth")
			}
			if second.CurrentCandidateCheck.ExecutionID == first.CurrentCandidateCheck.ExecutionID || second.CurrentCandidateCheck.SourceCommitOID == first.Receipt.SourceCommitOID {
				t.Fatal("historical guard reused or old candidate mislabeled current")
			}
			if (second.CurrentPreparation != nil) != restoreDependencies {
				t.Fatal("preparation unexpectedly repeated or skipped")
			}
			count, _ := os.ReadFile(marker)
			output, _ := os.ReadFile(filepath.Join(request.Directory, "dist/output"))
			if string(count) != "currentcurrent" || string(output) != "heavy must not repeat" {
				t.Fatalf("execution count mismatch guard=%s heavy=%s", count, output)
			}
			observed, err := request.Observe(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			envelope := second.Evidence()
			digest, err := envelope.Digest()
			if err != nil {
				t.Fatal(err)
			}
			guard := second.CurrentCandidateCheck
			assessment, err := execution.AssessVerificationEnvelope(envelope, digest, execution.VerificationEnvelopeTarget{
				VerificationTarget: execution.VerificationTarget{Repository: request.Repository, PlanID: request.PlanID, Entrypoint: request.Entrypoint, CandidateOID: observed.CommitOID, Boundary: request.Boundary, SettingsDigest: guard.SettingsDigest, Inputs: observed.Inputs},
				PlanRevision:       request.PlanRevision, TreeOID: observed.TreeOID, BaseOID: observed.BaseOID, CandidateIntegrity: observed.Integrity, CurrentCandidateCommand: guard.Command,
			})
			if err != nil || !assessment.Applicable {
				t.Fatalf("protected pair refused: %+v %v", assessment, err)
			}
		})
	}
}

func TestCurrentCandidateFailurePreservesHistoricalHeavyOrAbsence(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "no heavy", true: "historical heavy"}[historical], func(t *testing.T) {
			request := preparationFixture(t, "mkdir -p deps; printf prepared > deps/prepared")
			request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", "if test -f docs/rejected; then printf 'guard diagnostic' >&2; exit 7; fi"}}
			bindCurrentCandidateObservation(t, &request)
			var original []byte
			if historical {
				first, err := run(t.Context(), request, ownedTestGrant)
				if err != nil {
					t.Fatal(err)
				}
				request.PreviousReceipt, request.PreviousDigest = first.Receipt, first.Digest
				original, _ = json.Marshal(first.Receipt)
			}
			writeFixture(t, request.Directory, "docs/rejected", "current broad-scope guard must see this")
			git(t, request.Directory, "add", "docs/rejected")
			git(t, request.Directory, "commit", "-m", "guard finding outside heavy inputs")
			result, err := run(t.Context(), request, ownedTestGrant)
			var failure *CheckFailure
			if !errors.As(err, &failure) || failure.Phase != "current_candidate_check" || failure.ExitCode != 7 || failure.ExecutionID != result.CurrentCandidateCheck.ExecutionID {
				t.Fatalf("normal observed failure lost: %+v %v", result, err)
			}
			if result.CurrentCandidateCheck.Outcome != "failed" || !result.CurrentCandidateCheck.CleanupResolved || result.CurrentCandidateOutput.Stderr != "guard diagnostic" {
				t.Fatal("guard failure identity/output lost")
			}
			if historical {
				after, _ := json.Marshal(result.Receipt)
				if string(original) != string(after) || result.Digest != request.PreviousDigest || !result.Historical {
					t.Fatal("guard failure overwrote heavy proof")
				}
			} else if result.Receipt != nil || result.Digest != "" || result.Historical {
				t.Fatal("guard failure invented a heavy execution")
			}
			if _, err := result.Evidence().Digest(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckFailureRequiresUnchangedPostCommandAuthority(t *testing.T) {
	for _, phase := range []string{"heavy", "current_candidate_check"} {
		for _, revoked := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "/stable", true: "/revoked"}[revoked], func(t *testing.T) {
				request := requestFixture(t, "-c", "exit 7")
				if phase == "current_candidate_check" {
					request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", "exit 7"}}
				}
				entry := request.Entry
				base := strings.TrimSpace(git(t, request.Directory, "rev-parse", "HEAD"))
				calls := 0
				request.Observe = func(ctx context.Context) (Observation, error) {
					calls++
					if calls == 3 && revoked {
						return Observation{}, errors.New("operator revoked authority")
					}
					return ObserveCandidate(ctx, request.Directory, base, "approved", entry)
				}
				result, err := run(t.Context(), request, ownedTestGrant)
				var failure *CheckFailure
				if calls != 3 || errors.As(err, &failure) == revoked {
					t.Fatalf("post-observe/classification calls=%d failure=%+v error=%v", calls, failure, err)
				}
				if !result.Invocation.CleanupResolved {
					t.Fatal("fixture cleanup failed")
				}
			})
		}
	}
}

func TestCurrentCandidateMutationAndCancellationNeverAuthorizeRepair(t *testing.T) {
	for _, tc := range []struct{ name, command string }{
		{"mutates source and exits", "printf bad > src/source.txt; exit 7"},
		{"mutates excluded tracked proof", "printf bad > docs/proof.txt"},
		{"timeout", "sleep 60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := preparationFixture(t, "mkdir -p deps; printf prepared > deps/prepared")
			request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", tc.command}}
			if tc.name == "timeout" {
				request.Entry.TimeoutSeconds = 1
			}
			bindCurrentCandidateObservation(t, &request)
			result, err := run(t.Context(), request, ownedTestGrant)
			var failure *CheckFailure
			if err == nil || errors.As(err, &failure) || result.Receipt != nil {
				t.Fatalf("unsafe failure classified for repair: %+v %v", result, err)
			}
			if tc.name == "timeout" && (!errors.Is(err, context.DeadlineExceeded) || result.Invocation.Outcome != "timeout") {
				t.Fatal("guard did not share entry deadline")
			}
		})
	}
	t.Run("cancel after observed exit", func(t *testing.T) {
		request := requestFixture(t, "-c", "exit 7")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		observe := request.Observe
		calls := 0
		request.Observe = func(ctx context.Context) (Observation, error) {
			calls++
			observed, err := observe(ctx)
			if calls == 3 {
				cancel()
			}
			return observed, err
		}
		result, err := run(ctx, request, ownedTestGrant)
		var failure *CheckFailure
		if !errors.Is(err, context.Canceled) || errors.As(err, &failure) || result.Receipt.Outcome != "canceled" {
			t.Fatalf("cancel became repair: %+v %v", result, err)
		}
	})
}

func TestCurrentCandidateSetupFailureNeverInventsExecution(t *testing.T) {
	request := requestFixture(t)
	request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: filepath.Join(t.TempDir(), "missing")}
	// This deterministic boundary fixture supplies an observed baseline so the
	// real process-start failure can be distinguished from a nonzero exit.
	observed, err := request.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	observed.Inputs.Configuration = strings.TrimPrefix(request.Entry.Digest(), "v1:")
	request.Observe = func(context.Context) (Observation, error) { return observed, nil }
	result, err := run(t.Context(), request, ownedTestGrant)
	var failure *CheckFailure
	if err == nil || errors.As(err, &failure) || result.Receipt != nil || result.CurrentCandidateCheck != nil {
		t.Fatalf("setup fabricated check: %+v %v", result, err)
	}
}

func TestCurrentCandidateSharesGrantDeadlineAndPreparationBudget(t *testing.T) {
	request := preparationFixture(t, "mkdir -p deps; printf prepared > deps/prepared")
	request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", "exit 0"}}
	bindCurrentCandidateObservation(t, &request)
	deadline := time.Now().Add(10 * time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	grants := 0
	result, err := run(ctx, request, func(ctx context.Context) (context.Context, *subprocess.HeavyClaim, error) {
		grants++
		actual, ok := ctx.Deadline()
		if !ok || !actual.Equal(deadline) {
			t.Fatalf("deadline renewed: %v", actual)
		}
		return ownedTestGrant(ctx)
	})
	if err != nil || grants != 1 || result.CurrentPreparation == nil || result.CurrentCandidateCheck == nil {
		t.Fatalf("shared claim failed: %+v %v", result, err)
	}
	if result.CurrentCandidateCheck.RunStartedAt.Before(result.CurrentPreparation.FinishedAt) || result.Receipt.RunStartedAt.Before(*result.CurrentCandidateCheck.RunFinishedAt) {
		t.Fatal("phase execution overlapped")
	}
	if reflect.DeepEqual(result.Receipt.ExecutionID, result.CurrentCandidateCheck.ExecutionID) {
		t.Fatal("distinct commands collapsed into same execution")
	}
}

func TestCheckFailureFinalizationRequiresResolvedClaimAndValidEvidence(t *testing.T) {
	request := requestFixture(t, "-c", "exit 7")
	original, err := run(t.Context(), request, ownedTestGrant)
	var observedFailure *CheckFailure
	if !errors.As(err, &observedFailure) {
		t.Fatalf("real normal nonzero fixture: %v", err)
	}
	encoded, _ := json.Marshal(original)
	for _, tc := range []struct {
		name                  string
		finishErr, contextErr error
		invalidReceipt        bool
	}{
		{"claim cleanup unresolved", &subprocess.CleanupError{Err: errors.New("owned descendant remains")}, nil, false},
		{"claim finish uncertain", errors.New("claim identity changed"), nil, false},
		{"deadline at finish", nil, context.DeadlineExceeded, false},
		{"cancel at finish", nil, context.Canceled, false},
		{"invalid observed proof", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result Result
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			if tc.invalidReceipt {
				result.Receipt.ReportDigest = "unavailable"
			}
			err := result.finish(observedFailure.cause, tc.finishErr, tc.contextErr, observedFailure)
			var forbidden *CheckFailure
			if err == nil || errors.As(err, &forbidden) {
				t.Fatalf("unresolved finalization authorized repair: %v", err)
			}
			if tc.finishErr != nil && (result.Invocation.CleanupResolved || result.Receipt.CleanupResolved || result.Receipt.Outcome != "cleanup_unresolved") {
				t.Fatalf("claim uncertainty not quarantined: %+v", result)
			}
		})
	}
}

func TestCurrentCandidateCleanupFailureKeepsHistoricalReceiptUnchanged(t *testing.T) {
	request := requestFixture(t)
	request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", "exit 0"}}
	bindCurrentCandidateObservation(t, &request)
	first, err := run(t.Context(), request, ownedTestGrant)
	if err != nil {
		t.Fatal(err)
	}
	request.PreviousReceipt, request.PreviousDigest = first.Receipt, first.Digest
	reused, err := run(t.Context(), request, ownedTestGrant)
	if err != nil || !reused.Historical {
		t.Fatalf("reused fixture: %+v %v", reused, err)
	}
	original, _ := json.Marshal(reused.Receipt)
	err = reused.finish(nil, errors.New("claim could not be finalized"), nil, nil)
	after, _ := json.Marshal(reused.Receipt)
	if err == nil || string(original) != string(after) || reused.Digest != first.Digest || reused.CurrentCandidateCheck.Outcome != "cleanup_unresolved" || reused.CurrentCandidateCheck.CleanupResolved {
		t.Fatalf("final cleanup overwrote original truth: %+v %v", reused, err)
	}
}

func TestCurrentCandidateExecutableBytesAreObservedInputs(t *testing.T) {
	_, entry := fixture(t)
	tool := filepath.Join(t.TempDir(), "guard")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: tool}
	before, err := observeEnvironment(t.Context(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n# updated guard runtime\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	after, err := observeEnvironment(t.Context(), entry)
	if err != nil || before == after {
		t.Fatalf("guard command not observed: %v", err)
	}
}

func TestCurrentCandidateOutputIsBoundedAndSeparateFromReceipt(t *testing.T) {
	request := requestFixture(t)
	request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", "i=0; while test $i -lt 3000; do printf 'bounded-diagnostic-not-a-secret-00000000000000000000\\n' >&2; i=$((i+1)); done; exit 7"}}
	bindCurrentCandidateObservation(t, &request)
	result, err := run(t.Context(), request, ownedTestGrant)
	var failure *CheckFailure
	if !errors.As(err, &failure) || result.CurrentCandidateOutput == nil {
		t.Fatalf("normal guard failure missing: %v", err)
	}
	if len(result.CurrentCandidateOutput.Stderr) > 64*1024+len("\n[verification output truncated]\n") || !strings.Contains(result.CurrentCandidateOutput.Stderr, "[verification output truncated]") {
		t.Fatal("unbounded or unmarked diagnostic")
	}
	encoded, _ := json.Marshal(result.Evidence())
	if strings.Contains(string(encoded), `"Stderr"`) || strings.Contains(string(encoded), "[verification output truncated]") {
		t.Fatal("protected receipt stores raw output instead of report digest")
	}
	output, _ := json.Marshal(result.CurrentCandidateOutput)
	if result.CurrentCandidateCheck.ReportDigest != hash(output) {
		t.Fatal("receipt does not bind the selected bounded diagnostic")
	}
}
