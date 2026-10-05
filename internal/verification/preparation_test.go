package verification

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
)

func preparationFixture(t *testing.T, prep string) Request {
	t.Helper()
	root, entry := fixture(t)
	writeFixture(t, root, ".gitignore", "deps/\n.runner-npm-cache/\ndist/\ntest-results/\nlocal-ignored/\n")
	git(t, root, "add", ".gitignore")
	git(t, root, "commit", "-m", "ignored runtime outputs")
	entry.DependencyPaths = []string{"deps", ".runner-npm-cache"}
	entry.DependencyExcludePaths = []string{".runner-npm-cache"}
	entry.Preparation = &config.VerificationPreparation{Command: "/bin/sh", Args: []string{"-c", prep}}
	entry.Args = []string{"-c", "test -f deps/prepared && mkdir -p dist test-results && printf output > dist/output && printf receipt > test-results/result"}
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	request := Request{Entrypoint: "complete", Entry: entry, Directory: root, Repository: "owner/repo", AttemptID: "attempt", Boundary: execution.VerificationComplete}
	request.Observe = func(ctx context.Context) (Observation, error) {
		return ObserveCandidate(ctx, root, base, "approved", request.Entry)
	}
	return request
}

func TestPreparationRetainsLocalIntegrityBeforeAuthorityFailure(t *testing.T) {
	for _, reason := range []string{"GitHub HTTP 502", "approval revoked"} {
		t.Run(reason, func(t *testing.T) {
			request := preparationFixture(t, "mkdir -p deps/pkg; printf prepared > deps/prepared; printf '*.js text\\n' > deps/pkg/.gitattributes")
			request.ObservePreparation = request.Observe
			calls := 0
			refusal := errors.New(reason)
			request.Observe = func(ctx context.Context) (Observation, error) {
				if _, err := os.Stat(filepath.Join(request.Directory, "deps/prepared")); err == nil {
					calls++
					return Observation{}, refusal
				}
				return request.ObservePreparation(ctx)
			}
			result, err := run(t.Context(), request, ownedTestGrant)
			var failure *CheckFailure
			if !errors.Is(err, refusal) || errors.As(err, &failure) || calls != 1 || result.PreparedIntegrity == "" || result.Receipt != nil || result.CurrentCandidateCheck != nil || result.Invocation.Outcome != "failed" || !result.Invocation.CleanupResolved || result.CurrentPreparation == nil || result.CurrentPreparation.Outcome != "passed" {
				t.Fatalf("lost validated preparation or granted check authority: %+v %v", result, err)
			}
			observed, err := request.ObservePreparation(t.Context())
			if err != nil || observed.Integrity != result.PreparedIntegrity {
				t.Fatalf("retained preparation is not the actual candidate: %v", err)
			}
			if _, err := os.Stat(filepath.Join(request.Directory, "dist")); !os.IsNotExist(err) {
				t.Fatal("check ran without authority")
			}
		})
	}
}

func TestSplitPreparationStillRefusesProtectedChanges(t *testing.T) {
	for _, command := range []string{"printf bad > src/source.txt", "mkdir -p local-ignored; printf bad > local-ignored/leak", "git config core.filemode false"} {
		t.Run(command, func(t *testing.T) {
			request := preparationFixture(t, command)
			request.ObservePreparation = request.Observe
			calls := 0
			request.Observe = func(ctx context.Context) (Observation, error) { calls++; return request.ObservePreparation(ctx) }
			result, err := run(t.Context(), request, ownedTestGrant)
			if err == nil || calls != 2 || result.PreparedIntegrity != "" || result.Receipt != nil {
				t.Fatalf("protected mutation adopted: %+v %v", result, err)
			}
		})
	}
}

func TestPreparationAuthorityRefreshCannotSubstituteInputs(t *testing.T) {
	request := preparationFixture(t, "mkdir -p deps; printf prepared > deps/prepared")
	request.ObservePreparation = request.Observe
	request.Observe = func(ctx context.Context) (Observation, error) {
		if _, err := os.Stat(filepath.Join(request.Directory, "deps/prepared")); err == nil {
			writeFixture(t, request.Directory, "deps/prepared", "substituted")
		}
		return request.ObservePreparation(ctx)
	}
	result, err := run(t.Context(), request, ownedTestGrant)
	if err == nil || result.PreparedIntegrity == "" || result.Receipt != nil || result.CurrentCandidateCheck != nil {
		t.Fatalf("authority refresh substituted executable inputs: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(request.Directory, "dist")); !os.IsNotExist(err) {
		t.Fatal("check ran against substituted inputs")
	}
}

func TestPreparationBindsActualDependenciesAndAllowsCheckOutputs(t *testing.T) {
	request := preparationFixture(t, "mkdir -p deps .runner-npm-cache; printf prepared > deps/prepared; printf cache > .runner-npm-cache/log")
	before, err := request.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := run(t.Context(), request, ownedTestGrant)
	if err != nil {
		t.Fatal(err)
	}
	if result.Historical || result.Receipt.Outcome != "passed" || result.Receipt.Preparation == nil || result.Receipt.Preparation.Outcome != "passed" {
		t.Fatalf("missing actual preparation/check: %+v", result)
	}
	if before.Inputs.Dependencies == result.Receipt.Inputs.Dependencies {
		t.Fatal("missing dependency baseline certified")
	}
	if result.Receipt.RunStartedAt.Before(result.Receipt.Preparation.FinishedAt) {
		t.Fatal("check overlapped preparation/cleanup")
	}
	// A cache log and ordinary ignored output are not executable inputs. Reuse
	// returns the original receipt and performs neither preparation nor check.
	writeFixture(t, request.Directory, ".runner-npm-cache/log", "new log")
	writeFixture(t, request.Directory, "dist/output", "unchanged applicability")
	request.PreviousReceipt, request.PreviousDigest = result.Receipt, result.Digest
	reused, err := run(t.Context(), request, ownedTestGrant)
	if err != nil || !reused.Historical || !reflect.DeepEqual(reused.Receipt, result.Receipt) || reused.PreparationOutput != nil || reused.Output.Stdout != "" {
		t.Fatalf("historical reuse: %+v %v", reused, err)
	}
	contents, _ := os.ReadFile(filepath.Join(request.Directory, "dist/output"))
	if string(contents) != "unchanged applicability" {
		t.Fatal("reused gate ran again")
	}
}

func TestPreparationAllowsPackageGitControlsButBindsThemDuringChecks(t *testing.T) {
	request := preparationFixture(t, "mkdir -p deps/lib; printf prepared > deps/prepared; printf '*.js text\\n' > deps/lib/.gitattributes; printf 'build/\\n' > deps/lib/.gitignore; printf '# package metadata\\n' > deps/lib/.gitmodules")
	result, err := run(t.Context(), request, ownedTestGrant)
	if err != nil || result.Receipt == nil || result.Invocation.Outcome != "passed" {
		t.Fatalf("declared package preparation refused: %v; %+v", err, result)
	}
	request.Entry.CurrentCandidateCheck = &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", "printf changed > deps/lib/.gitattributes"}}
	bindCurrentCandidateObservation(t, &request)
	// Changing installed metadata during a check is not preparation authority.
	changed, err := run(t.Context(), request, ownedTestGrant)
	var failure *CheckFailure
	if err == nil || errors.As(err, &failure) || changed.CurrentCandidateCheck == nil || changed.CurrentCandidateCheck.Outcome != "failed" || changed.Invocation.Outcome == "passed" {
		t.Fatalf("check changed dependency metadata without refusal: %v; %+v", err, changed)
	}
}

func TestPreparationRefusesUndeclaredChangesAndFailures(t *testing.T) {
	for _, tc := range []struct{ name, command string }{
		{"ignored", "mkdir -p local-ignored; printf bad > local-ignored/leak; printf ok > deps/prepared"},
		{"tracked", "printf bad > src/source.txt; printf ok > deps/prepared"},
		{"outside control", "mkdir -p local-ignored; printf '*.txt -diff' > local-ignored/.gitattributes; printf ok > deps/prepared"},
		{"Git config", "git config core.filemode false; printf ok > deps/prepared"},
		{"nested administration", "mkdir -p deps/pkg/.git; printf ok > deps/prepared"},
		{"failed", "printf partial > deps/prepared; exit 7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := preparationFixture(t, tc.command)
			result, err := run(t.Context(), request, ownedTestGrant)
			var failure *CheckFailure
			if err == nil || errors.As(err, &failure) || result.Receipt != nil || result.Digest != "" || result.CurrentPreparation == nil {
				t.Fatalf("preparation failure certified: %+v %v", result, err)
			}
			if _, err := os.Stat(filepath.Join(request.Directory, "dist")); !os.IsNotExist(err) {
				t.Fatal("check ran after invalid preparation")
			}
		})
	}
}

func TestPreparationRefusesTrackedRootsAndExcludedCacheEscapes(t *testing.T) {
	t.Run("tracked", func(t *testing.T) {
		request := preparationFixture(t, "exit 0")
		git(t, request.Directory, "add", "-f", "deps/lib/index.js")
		git(t, request.Directory, "commit", "-m", "tracked dependency")
		if _, err := request.Observe(t.Context()); err == nil {
			t.Fatal("tracked file made mutable")
		}
	})
	t.Run("excluded cache symlink", func(t *testing.T) {
		request := preparationFixture(t, "exit 0")
		if err := os.Symlink(t.TempDir(), filepath.Join(request.Directory, ".runner-npm-cache")); err != nil {
			t.Fatal(err)
		}
		if _, err := request.Observe(t.Context()); err == nil {
			t.Fatal("cache exclusion hid external write root")
		}
	})
}

func TestPreparationReassessesProtectedProofAfterRestoringDependencies(t *testing.T) {
	request := preparationFixture(t, "mkdir -p deps; printf prepared > deps/prepared")
	first, err := run(t.Context(), request, ownedTestGrant)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(request.Directory, "deps/prepared")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, request.Directory, "dist/output", "must remain")
	request.PreviousReceipt, request.PreviousDigest = first.Receipt, first.Digest
	reused, err := run(t.Context(), request, ownedTestGrant)
	if err != nil || !reused.Historical || reused.CurrentPreparation == nil || !reflect.DeepEqual(first.Receipt, reused.Receipt) {
		t.Fatalf("prepared reuse: %+v %v", reused, err)
	}
	contents, _ := os.ReadFile(filepath.Join(request.Directory, "dist/output"))
	if string(contents) != "must remain" {
		t.Fatal("applicable gate executed twice")
	}
}

func TestPreparationTimeoutSharesDeadlineAndDoesNotLaunchCheck(t *testing.T) {
	request := preparationFixture(t, "sleep 60")
	request.Entry.TimeoutSeconds = 1
	base := strings.TrimSpace(git(t, request.Directory, "rev-parse", "HEAD"))
	// This test exercises supervised command expiry, not the variable cost of
	// Git/runtime collection. Pin the real, unchanged fixture before starting
	// its one-second budget. Observation expiry is covered separately below.
	observed, err := ObserveCandidate(t.Context(), request.Directory, base, "approved", request.Entry)
	if err != nil {
		t.Fatal(err)
	}
	request.Observe = func(context.Context) (Observation, error) {
		return observed, nil
	}
	result, err := run(t.Context(), request, ownedTestGrant)
	if !errors.Is(err, context.DeadlineExceeded) || result.Invocation.Outcome != "timeout" || result.Receipt != nil || result.CurrentPreparation == nil || result.CurrentPreparation.Outcome != "timeout" || !result.Invocation.CleanupResolved {
		t.Fatalf("deadline/cleanup accounting: %+v %v", result, err)
	}
}

func TestPreparationDeadlineIncludesInitialObservation(t *testing.T) {
	request := preparationFixture(t, "exit 0")
	request.Entry.TimeoutSeconds = 1
	request.Observe = func(ctx context.Context) (Observation, error) {
		<-ctx.Done()
		return Observation{}, ctx.Err()
	}
	result, err := run(t.Context(), request, ownedTestGrant)
	if !errors.Is(err, context.DeadlineExceeded) || result.Invocation.Outcome != "timeout" || result.CurrentPreparation != nil || result.Receipt != nil || !result.Invocation.CleanupResolved {
		t.Fatalf("observation escaped shared deadline or launched preparation: %+v %v", result, err)
	}
}

func TestReuseHonorsCatalogCurrentCandidatePolicyAndRefusesTampering(t *testing.T) {
	for _, currentOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(currentOnly), func(t *testing.T) {
			request := preparationFixture(t, "mkdir -p deps; printf prepared > deps/prepared")
			request.Entry.RequireCurrentCandidate = currentOnly
			base := strings.TrimSpace(git(t, request.Directory, "rev-parse", "HEAD"))
			request.Observe = func(ctx context.Context) (Observation, error) {
				return ObserveCandidate(ctx, request.Directory, base, "approved", request.Entry)
			}
			first, err := run(t.Context(), request, ownedTestGrant)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, request.Directory, "docs/proof.txt", "renewed receipt only")
			git(t, request.Directory, "add", "docs/proof.txt")
			git(t, request.Directory, "commit", "-m", "proof only")
			request.PreviousReceipt, request.PreviousDigest = first.Receipt, first.Digest
			second, err := run(t.Context(), request, ownedTestGrant)
			if err != nil || second.Historical == currentOnly {
				t.Fatalf("catalog policy ignored: historical=%v %v", second.Historical, err)
			}
			request.PreviousReceipt.ReportDigest = strings.Repeat("0", 64)
			if _, err := run(t.Context(), request, ownedTestGrant); err == nil {
				t.Fatal("tampered provenance became execution/reuse permission")
			}
		})
	}
}
