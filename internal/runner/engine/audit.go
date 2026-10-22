package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

type AuditStatus struct {
	CheckID   string     `json:"check_id"`
	Status    string     `json:"status"`
	QueriedAt *time.Time `json:"queried_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Report    string     `json:"report,omitempty"`
}

func auditStatus(q config.Quality, t core.Task, now time.Time) []AuditStatus {
	var result []AuditStatus
	for _, check := range q.Checks {
		s := AuditStatus{CheckID: check.ID, Status: "needs refresh"}
		if t.Audits != nil && t.Candidate != nil && t.Audits.Candidate == *t.Candidate {
			for _, c := range t.Audits.Checks {
				if c.CheckID == check.ID && c.Audit != nil {
					r := c.Audit.Receipt
					expires := r.QueriedAt.Add(check.NPMAudit.Age())
					s.QueriedAt, s.ExpiresAt, s.Report = &r.QueriedAt, &expires, r.Report
					if r.Fresh(now, check.NPMAudit.Age()) {
						s.Status = c.Status
					}
					if t.Stage == core.Completed {
						s.Status = "historical " + c.Status
					}
				}
			}
		}
		result = append(result, s)
	}
	return result
}

func (e *Engine) runAudit(ctx context.Context, source string, files map[string]string, check config.QualityCheck, evidence string, started func(core.Process) error) core.CheckObservation {
	r := core.CheckObservation{CheckID: check.ID, Status: "unavailable", ExitCode: -1, Stopped: true, Evidence: evidence}
	fail := func(err error) core.CheckObservation { r.Detail = err.Error(); return r }
	input, err := execution.InspectNPMAudit(source, check.Directory)
	if err != nil {
		return fail(err)
	}
	for path, digest := range input.Files {
		if !strings.HasSuffix(files[path], ":"+digest) {
			return fail(errors.New("audit dependency inputs differ from inventoried source"))
		}
	}
	if err = e.Store.AuditClock(ctx, e.now()); err != nil {
		return fail(err)
	}
	previous, err := e.Store.LatestAudit(ctx, input.Digest, check.AuditPolicy(), check.Image)
	if err != nil {
		return fail(err)
	}
	if previous != nil {
		receipt := *previous
		if receipt.Fresh(e.now(), check.NPMAudit.Age()) {
			if err = execution.VerifyAuditReceipt(receipt); err != nil {
				return fail(err)
			}
			r.Status, r.ExitCode, r.OutputDigest, r.Evidence = receipt.Status, 0, receipt.ReportDigest, receipt.Report
			if r.Status == "failed" {
				r.ExitCode = 1
			}
			r.Audit = &core.AuditAssessment{Receipt: receipt, SourceDigest: core.Digest(files)}
			return r
		}
	}
	auditor := e.Auditor
	if auditor == nil {
		auditor = execution.NPMAuditor{}
	}
	r = auditor.Audit(ctx, input, check, core.Digest(files), evidence, started)
	if r.Audit != nil && r.Stopped {
		if err = e.Store.SaveAudit(context.Background(), r.Audit.Receipt); err != nil {
			r.Status, r.Detail = "unavailable", err.Error()
		}
	}
	return r
}

func (e *Engine) validateAuditChecks(ctx context.Context, q config.Quality, results []core.CheckObservation) error {
	for _, check := range q.Checks {
		if check.NPMAudit == nil {
			continue
		}
		if err := e.Store.CheckAuditClock(ctx, e.now()); err != nil {
			return err
		}
		var found *core.CheckObservation
		for i := range results {
			if results[i].CheckID == check.ID {
				found = &results[i]
			}
		}
		if found == nil || found.Audit == nil || !found.Stopped {
			return fmt.Errorf("npm audit %s needs refresh", check.ID)
		}
		r := found.Audit.Receipt
		if r.Policy != check.AuditPolicy() || r.Image != q.CheckImage || r.Adapter != config.NPMAuditAdapter || r.Tool != config.NPMAuditTool || r.Registry != config.NPMRegistry || r.ReportDigest != found.OutputDigest || r.Status != found.Status || !r.Fresh(e.now(), check.NPMAudit.Age()) {
			return fmt.Errorf("npm audit %s expired or its binding changed; audit-only refresh required", check.ID)
		}
		if err := execution.VerifyAuditReceipt(r); err != nil {
			return err
		}
		if found.Status != "passed" {
			return fmt.Errorf("npm audit %s found vulnerabilities at the approved threshold; inspect %s", check.ID, r.Report)
		}
	}
	return nil
}

func (e *Engine) auditsReady(ctx context.Context, t core.Task) error {
	q := e.Settings.Quality
	if q.Revision() != t.Current().Quality {
		var err error
		q, err = e.Store.Quality(ctx, t.Current().Quality)
		if err != nil {
			return err
		}
	}
	q = q.Audits(t)
	if len(q.Checks) == 0 {
		return nil
	}
	if err := e.Store.AuditClock(ctx, e.now()); err != nil {
		return err
	}
	if t.Candidate == nil || t.Audits == nil || t.Audits.Candidate != *t.Candidate {
		return errors.New("candidate requires npm audit receipts")
	}
	for _, c := range t.Audits.Checks {
		if c.Audit == nil || c.Audit.SourceDigest != t.Candidate.Fingerprint {
			return errors.New("npm audit candidate content binding changed")
		}
	}
	return e.validateAuditChecks(ctx, q, t.Audits.Checks)
}

func bindAudits(t *core.Task, checks []core.CheckObservation) {
	if t.Candidate == nil {
		return
	}
	audits := []core.CheckObservation{}
	for _, c := range checks {
		if c.Audit != nil {
			audits = append(audits, c)
		}
	}
	if len(audits) == 0 {
		t.Audits = nil
		return
	}
	t.Audits = &core.AuditProof{Candidate: *t.Candidate, Checks: audits}
}

func queueAuditRefresh(t *core.Task) {
	t.AuditResume, t.AuditPending = t.NextRole, t.Pending
	t.NextRole, t.Pending = "audit_refresh", nil
}

func resumeAfterAudit(t *core.Task) {
	t.NextRole, t.Pending = t.AuditResume, t.AuditPending
	t.AuditResume, t.AuditPending, t.Attention = "", nil, nil
}

// Expiry derives one ordinary deterministic action. It is not a retry loop:
// failed/unavailable refreshes retain attention until explicit recovery.
func (e *Engine) prepareAuditRefreshes(ctx context.Context) error {
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return err
	}
	current := e.Settings.Quality.Revision()
	hasAudits := false
	for _, check := range e.Settings.Quality.Checks {
		if check.NPMAudit != nil {
			hasAudits = true
			break
		}
	}
	candidates := []core.Task{}
	for _, t := range tasks {
		if t.Candidate == nil || t.Approval == nil || t.Attention != nil || t.Inclusion != nil || t.Stage == core.Completed || t.Stage == core.Stopped || (t.NextRole != "" && t.NextRole != "audit" && t.NextRole != "verify") || (t.Current().Quality == current && !hasAudits) {
			continue
		}
		candidates = append(candidates, t)
	}
	if len(candidates) == 0 {
		return nil
	}
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return err
	}
	publications, err := e.Store.Publications(ctx, "")
	if err != nil {
		return err
	}
	for _, t := range candidates {
		busy := false
		for _, a := range attempts {
			if a.TaskID == t.ID && (a.Occupying || a.State == "completed") {
				busy = true
			}
		}
		for _, p := range publications {
			if p.Intent.TaskID == t.ID && p.Occupying {
				busy = true
			}
		}
		if busy || e.auditsReady(ctx, t) == nil {
			continue
		}
		queueAuditRefresh(&t)
		if err = e.Store.Commit(ctx, t.Revision, t, nil, false); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func (e *Engine) executeAuditRefresh(ctx context.Context, t core.Task, a core.Attempt) finished {
	stopped := true
	fail := func(err error) finished {
		return finished{a, execution.Completion{Stopped: stopped, Kind: "npm_audit", Error: err.Error()}}
	}
	if t.Candidate == nil {
		return fail(errors.New("audit candidate missing"))
	}
	if err := e.git().ValidateCandidate(ctx, t); err != nil {
		return fail(err)
	}
	files, err := workspace.Manifest(t.Candidate.Workspace)
	if err != nil {
		return fail(err)
	}
	q := e.Settings.Quality.Audits(t)
	a.Checks, err = e.runChecks(ctx, t.Candidate.Workspace, files, q, filepath.Join(a.EvidenceDir, "quality"), func(p core.Process) error {
		a.Process, a.State = &p, "checking"
		return e.mutate(context.Background(), t.ID, func(current *core.Task) (*core.Attempt, error) {
			if !a.Authorized(*current) {
				return nil, errors.New("audit refresh authority revoked")
			}
			return &a, nil
		})
	})
	for _, check := range a.Checks {
		if !check.Stopped {
			stopped = false
		}
	}
	if err != nil {
		return fail(err)
	}
	if err = q.ValidateResults(a.Checks, files); err != nil {
		return fail(err)
	}
	if err = e.validateAuditChecks(ctx, q, a.Checks); err != nil {
		return fail(err)
	}
	return finished{a, execution.Completion{Stopped: stopped, Result: &core.Result{Outcome: "succeeded", Summary: "Fresh npm audit receipts retained; other evidence and allowances unchanged"}}}
}
