package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/securefs"
)

// RepairChecks is an operator classification, never an interpretation of logs by
// the scheduler. It retains the failed attempt and reserves no fresh allowance.
func (e *Engine) RepairChecks(ctx context.Context, id, attempt, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("a concrete in-scope defect reason is required")
	}
	if err := e.validate(ctx); err != nil {
		return err
	}
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return err
	}
	pinned, err := e.taskEngine(ctx, t)
	if err != nil {
		return err
	}
	attempts, err := e.Store.Attempts(ctx, id)
	if err != nil {
		return err
	}
	for _, a := range attempts {
		if a.ID != attempt {
			continue
		}
		if a.Resume == "check_repair" {
			return nil // This exact failure can authorize at most one repair.
		}
		if t.Attention == nil || t.Attention.Attempt != a.ID || t.Pending != nil || t.NextRole != "" || t.Stage != core.Implementing {
			return errors.New("only the current failed implementation check can authorize repair")
		}
		for _, owner := range attempts {
			if owner.Occupying || (owner.Process != nil && !execution.Gone(*owner.Process)) {
				return errors.New("execution ownership is unresolved; run reconciliation first")
			}
		}
		if err := pinned.checkRepairSource(ctx, t, a); err != nil {
			return err
		}
		// Admission rechecks policy and spends these same task-version counters.
		a.Resume = "check_repair"
		t.Pending = &core.Continuation{Kind: "check_repair", FromAttempt: a.ID, Reason: reason, ProfileName: a.ProfileName, Profile: a.Profile}
		t.NextRole, t.Attention = "implementer", nil
		return e.Store.Commit(ctx, t.Revision, t, &a, false)
	}
	return errors.New("attempt not found")
}

// Rechecked before launching the repair, so a queued request cannot bless changed
// source, stale authority, incomplete checks or replaced evidence.
func (e *Engine) checkRepairSource(ctx context.Context, t core.Task, a core.Attempt) error {
	q := e.Settings.Quality.For(t, false)
	if !a.Authorized(t) || a.Quality != t.Current().Quality || a.Quality != e.Settings.Quality.Revision() ||
		a.Role != "implementer" || a.State != "failed" || a.Failure != core.ProofGap || a.Occupying ||
		a.Result == nil || a.Result.Outcome != "succeeded" || a.SourceDigest == "" || a.SourceHead == "" ||
		len(a.Checks) != len(q.Checks) {
		return errors.New("repair requires the current complete failed implementation check evidence")
	}
	if a.Process != nil && !execution.Gone(*a.Process) {
		return errors.New("execution ownership is unresolved")
	}
	failed := false
	for i, c := range a.Checks {
		if c.CheckID != q.Checks[i].ID || !c.Stopped || c.Evidence == "" || c.OutputDigest == "" ||
			(c.Status != "passed" && c.Status != "failed") || (c.Status == "passed" && c.ExitCode != 0) || (c.Status == "failed" && c.ExitCode <= 0) {
			return errors.New("unavailable or incomplete checks cannot authorize source repair")
		}
		b, _, _, err := securefs.ReadFile(c.Evidence, 8<<20)
		if err != nil {
			return fmt.Errorf("check evidence unavailable: %w", err)
		}
		d := sha256.Sum256(b)
		if hex.EncodeToString(d[:]) != c.OutputDigest {
			return errors.New("check evidence changed")
		}
		if c.Status == "failed" && q.Accepts(c, a.CheckFiles) != nil {
			failed = true
		}
	}
	if !failed {
		return errors.New("no failed required check eligible for repair")
	}
	digest, head, err := e.git().Binding(ctx, t)
	if err != nil {
		return err
	}
	if digest != a.SourceDigest || head != a.SourceHead {
		return errors.New("failed implementation source or HEAD changed")
	}
	return nil
}

// Repeated calls retain the original operator assignment and diagnostic receipts;
// partial repair edits are allowed after the first authorized launch.
func checkRepairOrigin(attempts []core.Attempt, a core.Attempt) (*core.Attempt, string) {
	byID := make(map[string]core.Attempt, len(attempts))
	for _, old := range attempts {
		byID[old.ID] = old
	}
	for range len(attempts) {
		if a.Kind == "check_repair" {
			source, ok := byID[a.ResumedFrom]
			if ok {
				return &source, a.Reason
			}
			return nil, ""
		}
		if a.Kind != "retry" && a.Kind != "resume" {
			break
		}
		var ok bool
		a, ok = byID[a.ResumedFrom]
		if !ok {
			break
		}
	}
	return nil, ""
}
