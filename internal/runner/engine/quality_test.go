package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

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
			if kind == "matching" {
				if task.Stage != core.Reviewing {
					t.Fatal(task)
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
