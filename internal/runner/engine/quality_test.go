package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

func artifactCheck(t *testing.T, dir string, c config.QualityCheck, log string) core.CheckObservation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "greeting.txt"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".runner-output/reports/report.json")
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(fmt.Sprintf("%q", string(data))), 0600); err != nil {
		t.Fatal(err)
	}
	output := []byte(c.ID + " check completed\n")
	if err = os.WriteFile(log, output, 0600); err != nil {
		t.Fatal(err)
	}
	return core.CheckObservation{Directory: filepath.Join(dir, c.Directory), CheckID: c.ID, Status: "passed", Stopped: true, Evidence: log, OutputDigest: fmt.Sprintf("%x", sha256.Sum256(output))}
}

func TestReviewRetainsOnlyMatchingArtifactsAndChecksThemAfterReturn(t *testing.T) {
	for _, change := range []string{"none", "subdirectory", "stale binding", "changed before", "changed during", "check changed source"} {
		t.Run(change, func(t *testing.T) {
			e := fixture(t)
			if change == "subdirectory" {
				if err := os.Mkdir(filepath.Join(e.Settings.Config.Repository.Path, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(e.Settings.Config.Repository.Path, "nested/README.txt"), []byte("subdirectory fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				git(t, e.Settings.Config.Repository.Path, "add", ".")
				git(t, e.Settings.Config.Repository.Path, "commit", "-m", "subdirectory check")
			}
			candidate := false
			e.Checker = checkAdapter(func(_ context.Context, dir string, c config.QualityCheck, evidence string, _ func(core.Process) error) core.CheckObservation {
				if candidate && change == "check changed source" {
					if err := os.WriteFile(filepath.Join(dir, "greeting.txt"), []byte("unchecked"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return artifactCheck(t, dir, c, evidence)
			})
			spec := config.QualitySpec{Definition: "Exact greeting and retained report", Checks: []config.QualityCheck{{ID: "greeting", Command: "check", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m", Artifacts: []string{".runner-output/reports"}}}}
			if change == "subdirectory" {
				spec.Checks[0].Directory = "nested"
			}
			preview, err := e.PreviewQuality(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			report, err := e.RunBaseline(t.Context(), spec, preview.Preview)
			if err != nil {
				t.Fatal(err)
			}
			agreement, err := e.PreviewAgreement(t.Context(), report.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.ApplyAgreement(t.Context(), report.ID, nil, agreement.Preview); err != nil {
				t.Fatal(err)
			}
			task := approved(t, e)
			candidate = true
			implemented := e.execute(t.Context(), claim(t, e))
			if change == "check changed source" {
				if implemented.completion.Error == "" || implemented.attempt.Checks[0].Status != "unavailable" || implemented.attempt.Checks[0].Artifacts != nil {
					t.Fatal("changed source produced trusted artifacts", implemented)
				}
				return
			}
			if implemented.completion.Error != "" {
				t.Fatal(implemented.completion.Error)
			}
			if err = e.saveCompletion(t.Context(), implemented); err != nil {
				t.Fatal(err)
			}
			finish(t, e)
			attempts, err := e.Store.Attempts(t.Context(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			old := attempts[0]
			bundle := old.Checks[0].Artifacts
			path := filepath.Join(bundle.Directory, ".runner-output/reports/report.json")
			if change == "stale binding" {
				bundle.SourceDigest = "another source"
				if err = e.mutate(t.Context(), task.ID, func(*core.Task) (*core.Attempt, error) { return &old, nil }); err != nil {
					t.Fatal(err)
				}
			} else if change == "changed before" {
				if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
				called = true
				roots := strings.Join(r.ReadRoots, "\n")
				if !strings.Contains(roots, bundle.Directory) || !strings.Contains(roots, report.Quality.Baseline[0].Artifacts.Directory) || strings.Contains(roots, old.Checks[0].Directory) {
					t.Fatal("current/baseline evidence not narrowly granted", r.ReadRoots)
				}
				for _, check := range append(r.Quality.Baseline, r.QualityEvidence...) {
					data, err := os.ReadFile(filepath.Join(check.Artifacts.Directory, ".runner-output/reports/report.json"))
					if err != nil || (!strings.Contains(string(data), "before") && !strings.Contains(string(data), "hello")) {
						t.Fatal("actual baseline/candidate report missing", err)
					}
				}
				if change == "changed during" {
					if err := os.WriteFile(path, []byte("changed during review"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return pass(r)
			})
			f := e.execute(t.Context(), claim(t, e))
			if (f.completion.Error == "") != (change == "none" || change == "subdirectory") {
				t.Fatal("incorrect artifact acceptance", change, f.completion)
			}
			if called != (change == "none" || change == "subdirectory" || change == "changed during") {
				t.Fatal("invalid proof reached a reviewer", change)
			}
			if f.completion.Error != "" && f.completion.Kind != core.Integrity {
				t.Fatal("changed evidence misclassified as resumable interruption", f.completion)
			}
			if change == "changed during" && (f.completion.Kind != core.Integrity || f.completion.Result != nil) {
				t.Fatal("review accepted changed evidence", f.completion)
			}
		})
	}
}

func TestAgreementAdoptionRevokesOldWorkAndKeepsHistoricalEvidence(t *testing.T) {
	e := fixture(t, func(c *config.Config) {
		c.Delivery.Mode, c.Delivery.Repository = "github-draft", "owner/repo"
	})
	task := approved(t, e)
	a := claim(t, e)
	f := e.execute(t.Context(), a)
	old := task.Current().Quality
	next := e.Settings.Quality
	next.Definition = "New explicit requirement"
	next.RequiredCI = []core.CIRequirement{{Kind: "check", Name: "test", AppID: 15368}}
	if err := e.Store.ApplyQuality(t.Context(), old, next); err != nil {
		t.Fatal(err)
	}
	fresh, err := e.Add(t.Context(), brief)
	if err != nil || fresh.Current().Quality != next.Revision() {
		t.Fatal("new work did not use current default", err)
	}
	v, err := e.Show(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.AdoptQuality(t.Context(), task.ID, 1, "stale"); err == nil {
		t.Fatal("stale adoption accepted")
	}
	if _, err = e.AdoptQuality(t.Context(), task.ID, 1, v.QualityAdoptionPreview); err != nil {
		t.Fatal(err)
	}
	adopted := current(t, e, task.ID)
	if adopted.Current().Number != 2 || adopted.Current().Body != task.Current().Body || adopted.Current().Quality != next.Revision() || adopted.Approval != nil || adopted.Candidate != nil || adopted.Prepared != nil || adopted.Stage != core.Draft {
		t.Fatal("adoption did not create unapproved version", adopted)
	}
	if _, err = e.AdoptQuality(t.Context(), task.ID, 1, v.QualityAdoptionPreview); err != nil {
		t.Fatal(err)
	}
	if current(t, e, task.ID).Revision != adopted.Revision {
		t.Fatal("repeat adoption mutated authority")
	}
	if err = e.saveCompletion(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	finish(t, e)
	as, _ := e.Store.Attempts(t.Context(), task.ID)
	if len(as) != 1 || as[0].State != "superseded" || as[0].Result == nil || as[0].Quality != old {
		t.Fatal("late evidence changed authority", as)
	}
	if !reflect.DeepEqual(current(t, e, task.ID).Versions[0], task.Versions[0]) {
		t.Fatal("historical task version changed")
	}
}

func TestBaselineExceptionAndCandidateProofUseSamePinnedContract(t *testing.T) {
	for _, kind := range []string{"matching", "changed failure", "changed scope", "unavailable", "task proof gap", "changed during checks", "check changed copy"} {
		t.Run(kind, func(t *testing.T) {
			e := fixture(t)
			repo := e.Settings.Config.Repository.Path
			if err := os.WriteFile(filepath.Join(repo, "legacy.txt"), []byte("known defect\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", "legacy.txt")
			git(t, repo, "commit", "-m", "known baseline failure")
			phase := "baseline"
			image := "sha256:" + strings.Repeat("a", 64)
			canonical := ""
			e.Checker = checkAdapter(func(_ context.Context, dir string, c config.QualityCheck, evidence string, _ func(core.Process) error) core.CheckObservation {
				if c.Image != image {
					t.Fatal("pinned check image was not propagated", c.Image)
				}
				r := core.CheckObservation{CheckID: c.ID, Status: "failed", ExitCode: 1, OutputDigest: "known failure", Stopped: true, Evidence: evidence}
				if phase == "candidate" {
					if kind == "changed during checks" {
						if err := os.WriteFile(filepath.Join(canonical, "greeting.txt"), []byte("unchecked change\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if kind == "check changed copy" {
						if err := os.WriteFile(filepath.Join(dir, "greeting.txt"), []byte("weakened\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if kind == "changed failure" {
						r.OutputDigest = "new failure"
					}
					if kind == "unavailable" {
						r.Status = "unavailable"
					}
				}
				if err := os.WriteFile(evidence, []byte(r.OutputDigest), 0600); err != nil {
					t.Fatal(err)
				}
				return r
			})
			spec := config.QualitySpec{CheckImage: image, Definition: "New work must pass; preserve known legacy scope", Checks: []config.QualityCheck{{ID: "legacy", Command: "false", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"}}}
			p, err := e.PreviewQuality(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.RunBaseline(t.Context(), spec, "stale"); err == nil {
				t.Fatal("stale check authorized")
			}
			report, err := e.RunBaseline(t.Context(), spec, p.Preview)
			if err != nil {
				t.Fatal(err)
			}
			p, err = e.PreviewAgreement(t.Context(), report.ID, nil)
			if err != nil || len(p.Blockers) == 0 {
				t.Fatal("failed baseline not blocked", err)
			}
			if err = e.ApplyAgreement(t.Context(), report.ID, nil, p.Preview); err == nil {
				t.Fatal("failed baseline silently applied")
			}
			ex := []config.ExceptionRequest{{CheckID: "legacy", Rationale: "Identified unrelated legacy defect", Scope: []string{"legacy.txt"}}}
			p, err = e.PreviewAgreement(t.Context(), report.ID, ex)
			if err != nil || len(p.Blockers) > 0 {
				t.Fatal(p, err)
			}
			if err = e.ApplyAgreement(t.Context(), report.ID, ex, p.Preview); err != nil {
				t.Fatal(err)
			}
			task := approved(t, e)
			regular := e.Executor
			e.Executor = adapter(func(ctx context.Context, r execution.Request, started func(core.Process) error) execution.Completion {
				result := regular.Execute(ctx, r, started)
				if r.Attempt.Role == "implementer" {
					canonical = r.Dir
					if kind == "changed scope" {
						if err := os.WriteFile(filepath.Join(r.Dir, "legacy.txt"), []byte("different defect\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if kind == "task proof gap" {
						result.Result.Checks[0].Status = "gap"
					}
				} else if len(r.QualityEvidence) != 1 || r.QualityEvidence[0].Status != "failed" {
					t.Error("review did not receive exception evidence")
				}
				return result
			})
			phase = "candidate"
			f := e.execute(t.Context(), claim(t, e))
			if err = e.saveCompletion(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			reopen(t, e)
			e.Checker = checkAdapter(func(context.Context, string, config.QualityCheck, string, func(core.Process) error) core.CheckObservation {
				t.Fatal("saved checks reran")
				return core.CheckObservation{}
			})
			finish(t, e)
			task = current(t, e, task.ID)
			if kind == "matching" || kind == "task proof gap" {
				if task.Stage != core.Reviewing {
					t.Fatal(task)
				}
				if kind == "task proof gap" {
					if len(task.Review) == 0 || task.Review[0].Status != "gap" {
						t.Fatal("handoff hid implementer proof gaps")
					}
					e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
						result := pass(r)
						result.Result.Checks[0].Status = "gap"
						return result
					})
					complete(t, e, claim(t, e))
					finish(t, e)
					if current(t, e, task.ID).NextRole != "verify" {
						t.Fatal("independent audit gap did not request focused proof")
					}
					complete(t, e, claim(t, e))
					finish(t, e)
					if result := current(t, e, task.ID); result.Attention == nil || result.Stage == core.AwaitingDelivery {
						t.Fatal("unproved handoff reached delivery")
					}
					return
				}
				complete(t, e, claim(t, e))
				finish(t, e)
				if current(t, e, task.ID).Stage != core.AwaitingDelivery {
					t.Fatal("matched exception could not progress")
				}
			} else if task.Attention == nil || task.Candidate != nil {
				t.Fatal("exception waived changed/missing proof", task)
			}
		})
	}
}

func TestMovedBaseAndChangedDefaultRejectAgreementPreview(t *testing.T) {
	e := fixture(t)
	spec := config.QualitySpec{Definition: "replacement", Checks: []config.QualityCheck{{ID: "greeting", Command: "test -f greeting.txt", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"}}}
	p, err := e.PreviewQuality(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.RunBaseline(t.Context(), spec, p.Preview)
	if err != nil {
		t.Fatal(err)
	}
	p, err = e.PreviewAgreement(t.Context(), report.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := e.Settings.Quality
	next.Definition = "competing definition"
	if err = e.Store.ApplyQuality(t.Context(), e.Settings.Quality.Revision(), next); err != nil {
		t.Fatal(err)
	}
	if err = e.ApplyAgreement(t.Context(), report.ID, nil, p.Preview); err == nil {
		t.Fatal("stale default applied")
	}
	git(t, e.Settings.Config.Repository.Path, "commit", "--allow-empty", "-m", "moved base")
	if _, err = e.PreviewAgreement(t.Context(), report.ID, nil); err == nil {
		t.Fatal("old baseline accepted on moved base")
	}
}

// The defect is a failed required command, not a model rejection. Recovery must
// preserve the source and spend the existing allowance exactly once.
func TestOperatorCheckRepairRetainsAuthorityEvidenceAndBudgets(t *testing.T) {
	for _, kind := range []string{"repair", "unavailable", "incomplete", "source changed", "log changed", "stopped", "ownership", "missing reason", "repair limit", "invocation limit", "changed after request"} {
		t.Run(kind, func(t *testing.T) {
			e := fixture(t, func(c *config.Config) { c.Policy.MaxRepairs = 1 })
			task := approved(t, e)
			e.Checker = checkAdapter(func(_ context.Context, _ string, c config.QualityCheck, evidence string, _ func(core.Process) error) core.CheckObservation {
				b := []byte("greeting formatting failed\n")
				if err := os.WriteFile(evidence, b, 0600); err != nil {
					t.Fatal(err)
				}
				return core.CheckObservation{CheckID: c.ID, Status: "failed", ExitCode: 1, OutputDigest: fmt.Sprintf("%x", sha256.Sum256(b)), Evidence: evidence, Stopped: true}
			})
			f := e.execute(t.Context(), claim(t, e))
			if err := e.saveCompletion(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			before := current(t, e, task.ID)
			attempts, _ := e.Store.Attempts(t.Context(), task.ID)
			failed := attempts[0]
			switch kind {
			case "unavailable":
				failed.Checks[0].Status = "unavailable"
			case "incomplete":
				failed.Checks = nil
			case "ownership":
				failed.Occupying = true
			case "source changed":
				if err := os.WriteFile(filepath.Join(failed.ExecutionDir, "greeting.txt"), []byte("unchecked\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "log changed":
				if err := os.WriteFile(failed.Checks[0].Evidence, []byte("replaced\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "stopped":
				before.Stage = core.Stopped
				before.Approval = nil
			}
			if err := e.Store.Commit(t.Context(), before.Revision, before, &failed, false); err != nil {
				t.Fatal(err)
			}
			before = current(t, e, task.ID)
			if kind == "repair limit" {
				applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxRepairs = 0 })
			}
			if kind == "invocation limit" {
				applyPolicy(t, e, func(c *config.Config) { n := 1; c.Policy.MaxInvocationsPerTask = &n })
			}
			reason := "Correct the demonstrated greeting formatting defect within the approved scope"
			if kind == "missing reason" {
				reason = ""
			}
			err := e.RepairChecks(t.Context(), task.ID, failed.ID, reason)
			switch kind {
			case "repair", "repair limit", "invocation limit", "changed after request":
				if err != nil {
					t.Fatal(err)
				}
			default:
				if err == nil {
					t.Fatal("ineligible repair authorized")
				}
				return
			}
			queued := current(t, e, task.ID)
			if err = e.RepairChecks(t.Context(), task.ID, failed.ID, reason); err != nil {
				t.Fatal(err)
			}
			if current(t, e, task.ID).Revision != queued.Revision || queued.Usage != before.Usage || !reflect.DeepEqual(queued.Versions, before.Versions) {
				t.Fatal("repair request reset authority or queued twice")
			}
			if kind == "repair limit" {
				waiting(t, e, task.ID, "repair limit")
				return
			}
			if kind == "invocation limit" {
				waiting(t, e, task.ID, "invocation ceiling")
				return
			}
			if kind == "changed after request" {
				if err = os.WriteFile(filepath.Join(failed.ExecutionDir, "greeting.txt"), []byte("unchecked\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			repair := claim(t, e)
			if repair.Kind != "check_repair" || repair.Profile != failed.Profile || repair.Repair != 1 || repair.Invocation != 2 || current(t, e, task.ID).Usage.Escalations != 0 {
				t.Fatal("repair used new budget or escalated", repair)
			}
			// A retry/resume retains diagnostics without requiring the original
			// pre-repair bytes after partial authorized edits.
			origin, retainedReason := checkRepairOrigin(append(attempts, repair), core.Attempt{Kind: "resume", ResumedFrom: repair.ID})
			if origin == nil || origin.ID != failed.ID || retainedReason != reason {
				t.Fatal("recovery lost the operator assignment")
			}
			original := e.Executor
			e.Executor = adapter(func(ctx context.Context, r execution.Request, started func(core.Process) error) execution.Completion {
				if kind == "changed after request" {
					t.Fatal("changed source reached a writer")
				}
				if len(r.QualityEvidence) != 1 || r.QualityEvidence[0].Status != "failed" || r.CheckRepairReason != reason {
					t.Fatal("repair lost diagnostic context")
				}
				return original.Execute(ctx, r, started)
			})
			e.Checker = checkAdapter(func(_ context.Context, _ string, c config.QualityCheck, _ string, _ func(core.Process) error) core.CheckObservation {
				return core.CheckObservation{CheckID: c.ID, Status: "passed", Stopped: true}
			})
			repaired := e.execute(t.Context(), repair)
			if kind == "changed after request" {
				if repaired.completion.Kind != core.Integrity {
					t.Fatal("source change not blocked", repaired.completion)
				}
				return
			}
			if repaired.completion.Error != "" {
				t.Fatal(repaired.completion.Error)
			}
			if err = e.saveCompletion(t.Context(), repaired); err != nil {
				t.Fatal(err)
			}
			finish(t, e)
			result := current(t, e, task.ID)
			if result.Stage != core.Reviewing || result.NextRole != "audit" || result.Usage.Repairs != 1 || result.Usage.Invocations != 2 {
				t.Fatal("repair bypassed review", result)
			}
			records, _ := e.Store.Attempts(t.Context(), task.ID)
			if len(records) != 2 || records[0].SourceDigest != failed.SourceDigest || records[0].Checks[0].Status != "failed" {
				t.Fatal("failed evidence lost")
			}
		})
	}
}
