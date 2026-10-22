package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

type auditAdapter func(context.Context, execution.AuditInput, config.QualityCheck, string, string, func(core.Process) error) core.CheckObservation

func TestAuditRefreshPreservesPendingReview(t *testing.T) {
	pending := &core.Continuation{Kind: "retry", FromAttempt: "review", ProfileName: "pinned-review", Profile: core.Profile{Model: "pinned-model", Reasoning: "high"}, Retry: 1, NotBefore: time.Now().UTC()}
	task := core.Task{NextRole: "verify", Pending: pending, Usage: core.Usage{Invocations: 3, Repairs: 1}}
	queueAuditRefresh(&task)
	if task.Pending != nil || task.NextRole != "audit_refresh" || task.AuditPending != pending {
		t.Fatal("review continuation spent on audit", task)
	}
	resumeAfterAudit(&task)
	if task.NextRole != "verify" || task.Pending != pending || task.Usage.Invocations != 3 || task.Usage.Repairs != 1 || task.AuditPending != nil || task.AuditResume != "" {
		t.Fatal("review profile or allowances lost", task)
	}
}

func (f auditAdapter) Audit(ctx context.Context, input execution.AuditInput, check config.QualityCheck, source, evidence string, started func(core.Process) error) core.CheckObservation {
	return f(ctx, input, check, source, evidence, started)
}

func auditObservation(t *testing.T, input execution.AuditInput, check config.QualityCheck, source, evidence string, now time.Time, failed bool) core.CheckObservation {
	t.Helper()
	status, exit := "passed", 0
	if failed {
		status, exit = "failed", 1
	}
	data := []byte(fmt.Sprintf(`{"auditReportVersion":2,"findings":%t}`, failed))
	if err := os.WriteFile(evidence, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	r := core.AuditReceipt{ID: core.NewID(), Inputs: input.Digest, Policy: check.AuditPolicy(), Adapter: config.NPMAuditAdapter, Tool: config.NPMAuditTool, Registry: config.NPMRegistry, Image: check.Image, QueriedAt: now, FinishedAt: now, SourceDigest: source, Status: status, Report: evidence, ReportDigest: digest}
	return core.CheckObservation{CheckID: check.ID, Status: status, ExitCode: exit, Stopped: true, Evidence: evidence, OutputDigest: digest, Audit: &core.AuditAssessment{Receipt: r, SourceDigest: source}}
}

func TestAuditBaselineRefreshPreservesOfflineEvidence(t *testing.T) {
	e := fixture(t)
	now := time.Now().UTC()
	e.clock = func() time.Time { return now }
	repo := e.Settings.Config.Repository.Path
	for name, data := range map[string]string{"package.json": `{}`, "package-lock.json": `{"lockfileVersion":3,"packages":{"":{}}}`} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "baseline audit")
	queries, commands := 0, 0
	e.Auditor = auditAdapter(func(_ context.Context, input execution.AuditInput, c config.QualityCheck, source, evidence string, _ func(core.Process) error) core.CheckObservation {
		queries++
		return auditObservation(t, input, c, source, evidence, now, false)
	})
	checker := e.Checker
	e.Checker = checkAdapter(func(ctx context.Context, root string, c config.QualityCheck, evidence string, started func(core.Process) error) core.CheckObservation {
		commands++
		return checker.Check(ctx, root, c, evidence, started)
	})
	spec := config.QualitySpec{Definition: "offline and audit", CheckImage: "sha256:" + strings.Repeat("a", 64), Checks: append(append([]config.QualityCheck(nil), e.Settings.Quality.Checks...), config.QualityCheck{ID: "audit", Directory: ".", Timeout: "1m", NPMAudit: &config.NPMAuditPolicy{Level: "high"}})}
	p, err := e.PreviewQuality(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	original, err := e.RunBaseline(t.Context(), spec, p.Preview)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	p, err = e.PreviewAgreement(t.Context(), original.ID, nil)
	if err != nil || len(p.Blockers) == 0 {
		t.Fatal(p, err)
	}
	refreshed, err := e.RefreshBaseline(t.Context(), original.ID, p.Preview)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 2 || commands != 1 || original.ID == refreshed.ID || core.Digest(original.Quality.Baseline[0]) != core.Digest(refreshed.Quality.Baseline[0]) || original.Quality.Revision() != refreshed.Quality.Revision() {
		t.Fatal("baseline renewal repeated checks or changed policy", queries, commands, refreshed)
	}
	p, err = e.PreviewAgreement(t.Context(), refreshed.ID, nil)
	if err != nil || len(p.Blockers) != 0 {
		t.Fatal(p, err)
	}
}

func TestAuditFreshnessPreservesWorkAndBlocksCompletion(t *testing.T) {
	e := fixture(t)
	now := time.Now().UTC()
	e.clock = func() time.Time { return now }
	repo := e.Settings.Config.Repository.Path
	for name, data := range map[string]string{"package.json": `{"name":"local-project"}`, "package-lock.json": `{"lockfileVersion":3,"packages":{"":{}}}`} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "audit fixture")
	q := e.Settings.Quality
	q.CheckImage = "sha256:" + strings.Repeat("a", 64)
	q.Base = git(t, repo, "rev-parse", "HEAD")
	q.Checks = append(q.Checks, config.QualityCheck{ID: "audit", Directory: ".", Timeout: "1m", NPMAudit: &config.NPMAuditPolicy{Level: "high"}})
	if err := e.Store.ApplyQuality(t.Context(), e.Settings.Quality.Revision(), q); err != nil {
		t.Fatal(err)
	}
	e.Settings.Quality = q
	queries, commands, models := 0, 0, 0
	failed := false
	e.Auditor = auditAdapter(func(_ context.Context, input execution.AuditInput, check config.QualityCheck, source, evidence string, _ func(core.Process) error) core.CheckObservation {
		queries++
		return auditObservation(t, input, check, source, evidence, now, failed)
	})
	oldCheck := e.Checker
	e.Checker = checkAdapter(func(ctx context.Context, root string, c config.QualityCheck, evidence string, started func(core.Process) error) core.CheckObservation {
		commands++
		return oldCheck.Check(ctx, root, c, evidence, started)
	})
	oldExec := e.Executor
	e.Executor = adapter(func(ctx context.Context, r execution.Request, started func(core.Process) error) execution.Completion {
		models++
		return oldExec.Execute(ctx, r, started)
	})
	task := approved(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	// Expiry after reconciliation is checked again before charging review admission.
	now = now.Add(61 * time.Minute)
	beforeReview := claim(t, e)
	if beforeReview.Role != "audit_refresh" || beforeReview.Invocation != 1 {
		t.Fatal("stale audit spent a review invocation", beforeReview)
	}
	complete(t, e, beforeReview)
	now = now.Add(61 * time.Minute)
	reopen(t, e)
	finish(t, e)
	stale, _ := e.Store.Task(t.Context(), task.ID)
	if stale.Attention == nil || stale.Attention.Kind != "saved_result" {
		t.Fatal("stale saved query accepted", stale)
	}
	if _, err := e.Resume(t.Context(), task.ID, beforeReview.ID, false); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	task, _ = e.Store.Task(t.Context(), task.ID)
	if task.Stage != core.AwaitingDelivery || queries != 3 || commands != 1 || models != 2 {
		t.Fatal(task, queries, commands, models)
	}
	original := task
	first := task.Audits.Checks[0].Audit.Receipt
	if err := e.prepareAuditRefreshes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if next, _ := e.Store.Task(t.Context(), task.ID); next.NextRole != "" {
		t.Fatal("fresh receipt not reused", next)
	}
	// Restart retains the receipt and the clock high-water mark.
	reopen(t, e)
	if err := e.Store.SaveAudit(t.Context(), first); err == nil {
		t.Fatal("immutable audit receipt overwritten")
	}
	now = now.Add(61 * time.Minute)
	intent := core.PublicationIntent{ID: "pending", TaskID: task.ID, Version: task.Current().Number, Generation: task.Generation, ApprovedBase: task.Approval.Base, Quality: task.Current().Quality, Candidate: *task.Candidate, ReviewDigest: core.Digest(task.Review)}
	if err := e.publicationAuthority(t.Context(), intent); err == nil || !strings.Contains(err.Error(), "npm audit") {
		t.Fatal("expired publication admitted", err)
	}
	if err := e.mergeAuthority(t.Context(), core.Publication{Intent: intent}); err == nil || !strings.Contains(err.Error(), "npm audit") {
		t.Fatal("expired merge admitted", err)
	}
	if err := e.prepareAuditRefreshes(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, _ := e.Store.Task(t.Context(), task.ID)
	if queued.NextRole != "audit_refresh" || queued.Stage != core.AwaitingDelivery {
		t.Fatal(queued)
	}
	a := claim(t, e)
	if a.Role != "audit_refresh" {
		t.Fatal(a)
	}
	complete(t, e, a)
	finish(t, e)
	task, _ = e.Store.Task(t.Context(), task.ID)
	if task.Stage != original.Stage || task.NextRole != "" || task.Usage != original.Usage || *task.Candidate != *original.Candidate || core.Digest(task.Review) != core.Digest(original.Review) || queries != 4 || commands != 1 || models != 2 {
		t.Fatal("refresh repeated unrelated work", task, queries, commands, models)
	}
	if task.Audits.Checks[0].Audit.Receipt.QueriedAt.Equal(first.QueriedAt) || !first.QueriedAt.Equal(original.Audits.Checks[0].Audit.Receipt.QueriedAt) {
		t.Fatal("query time relabelled")
	}
	// A new advisory on unchanged dependencies blocks delivery with no retry loop.
	failed = true
	now = now.Add(61 * time.Minute)
	if err := e.prepareAuditRefreshes(t.Context()); err != nil {
		t.Fatal(err)
	}
	f := e.execute(t.Context(), claim(t, e))
	if f.completion.Kind != "npm_audit" || f.attempt.Checks[0].Status != "failed" {
		t.Fatal(f)
	}
	if err := e.saveCompletion(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	if err := e.prepareCompletions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.prepareAuditRefreshes(t.Context()); err != nil {
		t.Fatal(err)
	}
	task, _ = e.Store.Task(t.Context(), task.ID)
	if task.Attention == nil || task.Completion != nil || queries != 5 || commands != 1 || models != 2 || task.Usage != original.Usage {
		t.Fatal(task, queries, commands, models)
	}
	view, err := e.Show(t.Context(), task.ID)
	if err != nil || !strings.Contains(view.NextAction, "task resume") {
		t.Fatal(view.NextAction, err)
	}
	if err = e.Store.AuditClock(t.Context(), now.Add(-time.Minute)); err == nil {
		t.Fatal("backwards clock extended audit validity")
	}
	// Changed dependency metadata cannot reuse even a still-valid receipt.
	source, err := workspace.BaselineCopy(t.Context(), e.Settings.Config, q.Base)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(source)
	for name, data := range map[string]string{
		"package.json":      `{"dependencies":{"is-number":"7.0.0"}}`,
		"package-lock.json": `{"lockfileVersion":3,"packages":{"":{"dependencies":{"is-number":"7.0.0"}},"node_modules/is-number":{"version":"7.0.0","resolved":"https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz","integrity":"sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng=="}}}`,
	} {
		if err = os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := workspace.Manifest(source)
	if err != nil {
		t.Fatal(err)
	}
	check := q.Checks[1]
	check.Image = q.CheckImage
	changed := e.runAudit(t.Context(), source, files, check, filepath.Join(e.Settings.Config.StateDir, "changed-dependencies.log"), func(core.Process) error { return nil })
	if changed.Audit == nil || changed.Audit.Receipt.Inputs == first.Inputs || queries != 6 {
		t.Fatal("changed dependencies reused old receipt", changed, queries)
	}

	// A slow refresh owns its attempt while unrelated work can still be admitted.
	now = now.Add(61 * time.Minute)
	if _, err = e.Resume(t.Context(), task.ID, f.attempt.ID, false); err != nil {
		t.Fatal(err)
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxParallelism = 2 })
	other := approved(t, e)
	entered := make(chan struct{})
	block := make(chan struct{}, 1)
	block <- struct{}{}
	baseAuditor := e.Auditor
	e.Auditor = auditAdapter(func(ctx context.Context, input execution.AuditInput, c config.QualityCheck, source, evidence string, started func(core.Process) error) core.CheckObservation {
		select {
		case <-block:
			close(entered)
			<-ctx.Done()
			return core.CheckObservation{CheckID: c.ID, Status: "unavailable", Stopped: true, Detail: ctx.Err().Error()}
		default:
			return baseAuditor.Audit(ctx, input, c, source, evidence, started)
		}
	})
	refresh := claim(t, e)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan finished, 1)
	go func() { done <- e.execute(ctx, refresh) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("slow audit did not start")
	}
	second := claim(t, e)
	if second.TaskID != other.ID {
		t.Fatal("unrelated work not admitted", second)
	}
	failed = false
	complete(t, e, second)
	cancel()
	select {
	case stopped := <-done:
		if !stopped.completion.Stopped {
			t.Fatal("query did not settle")
		}
		if err = e.saveCompletion(t.Context(), stopped); err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("slow audit did not cancel")
	}
}
