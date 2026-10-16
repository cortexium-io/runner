package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

// SaveRefWrite persists intent, dispatch, or observation. Receipts, Plan head
// advancement and final lifecycle changes share this transaction.
func (s *Store) SaveRefWrite(ctx context.Context, expected int, w core.RefWrite) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tasks, err := transactionTasks(ctx, tx)
	if err != nil {
		return err
	}
	var t core.Task
	for _, task := range tasks {
		if task.ID == w.TaskID {
			t = task
		}
	}
	if t.ID == "" || t.Revision != expected {
		return ErrConflict
	}
	var raw []byte
	var drained bool
	var settings config.Settings
	if err = tx.QueryRowContext(ctx, "SELECT settings,drained FROM metadata WHERE id=1").Scan(&raw, &drained); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &settings); err != nil {
		return err
	}
	index := -1
	var previous core.RefWrite
	for i, old := range t.RefWrites {
		if old.ID == w.ID {
			index, previous = i, old
			break
		}
	}
	if index < 0 {
		if w.State != "prepared" || w.ID == "" || w.Ref == "" || w.Commit == "" {
			return errors.New("invalid ref intent")
		}
		for _, task := range tasks {
			for _, old := range task.RefWrites {
				if old.Pending() && (old.Ref == w.Ref || task.ID == t.ID) {
					return ErrConflict
				}
			}
		}
	} else {
		if previous.Binding() != w.Binding() {
			return errors.New("ref intent binding changed")
		}
		if previous.State == w.State && !(w.State == "uncertain" && t.LocalInspect) {
			return nil
		}
		if !previous.Pending() {
			return errors.New("settled ref write is immutable")
		}
		if w.State == "confirmed" && previous.State != "dispatched" && previous.State != "uncertain" {
			return errors.New("confirmation needs a dispatched intent")
		}
		if w.State == "dispatched" && previous.State != "prepared" {
			return errors.New("ref write cannot repeat")
		}
	}
	authorized := w.Authorized(t) && core.PrerequisiteWait(t, tasks) == ""
	if t.Release != nil && w.Kind != "plan" {
		authorized = authorized && core.AllIncluded(t, tasks)
	}
	if w.Kind == "inclusion" {
		for _, parent := range tasks {
			if parent.ID == t.ParentID {
				authorized = authorized && parent.Release != nil && parent.Release.Preview == w.Release && parent.PlanHead == w.Old && parent.PlanRef() == w.Ref
			}
		}
	}
	if w.Kind == "base" {
		authorized = authorized && settings.Config.Delivery.Mode == "local" && settings.Config.LocalTarget() == "base" && w.Ref == settings.Config.Repository.BaseRef
		if w.AutomaticPolicy != "" {
			// Disabling policy fences unsent writes, not observation of an
			// already consumed dispatch. Task/proof authority still must match.
			authorized = authorized && (settings.Config.MergePolicy() == "automatic" || w.State == "confirmed" && (previous.State == "dispatched" || previous.State == "uncertain"))
		} else {
			authorized = authorized && t.LocalMerge != nil && core.Digest(t.LocalMerge) == core.Digest(w.Proof)
		}
	}
	if w.Kind == "manual" {
		authorized = authorized && t.LocalInspect && settings.Config.LocalTarget() == "base" && w.Ref == settings.Config.Repository.BaseRef
	}
	if w.Kind == "result" {
		authorized = authorized && settings.Config.Delivery.Mode == "local" && t.ParentID == ""
	}
	if w.State == "prepared" || w.State == "dispatched" {
		if !authorized || drained {
			return ErrConflict
		}
		var active int
		if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM attempts WHERE task_id=? AND occupying=1)+(SELECT count(*) FROM publications WHERE task_id=? AND occupying=1)", t.ID, t.ID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return ErrConflict
		}
	}
	if w.State == "confirmed" {
		if w.ObservedAt.IsZero() {
			return errors.New("ref confirmation lacks observation time")
		}
		if !authorized {
			w.State, w.Detail = "stale", "Effect observed after authority changed; retained without completion."
		} else {
			switch w.Kind {
			case "plan":
				t.PlanHead = w.Commit
			case "inclusion":
				t.Inclusion = &core.PlanInclusion{WriteID: w.ID, Release: w.Release, Version: w.Version, Generation: w.Generation, Previous: w.Old, Candidate: w.Proof.Candidate}
				for _, parent := range tasks {
					if parent.ID == t.ParentID {
						parent.PlanHead = w.Commit
						if err = writeTask(ctx, tx, parent.Revision, parent); err != nil {
							return err
						}
						break
					}
				}
			case "result", "base", "manual":
				if w.Kind != "result" || settings.Config.LocalTarget() == "result" {
					t.Completion = &core.CompletionReceipt{Kind: w.Kind, WriteID: w.ID, Ref: w.Ref, Commit: w.Commit, Tree: w.Proof.Candidate.Tree}
					if err = completeChildren(ctx, tx, t, tasks); err != nil {
						return err
					}
					t.Stage, t.NextRole, t.Pending, t.Attention = core.Completed, "", nil, nil
				}
			}
		}
	}
	if index < 0 {
		t.RefWrites = append(t.RefWrites, w)
	} else {
		t.RefWrites[index] = w
	}
	if w.State == "confirmed" || w.State == "uncertain" || w.State == "stale" {
		t.LocalInspect = false
	}
	if (w.State == "uncertain" || w.State == "stale") && t.Stage != core.Stopped && t.Stage != core.Completed {
		t.Attention = &core.Attention{Kind: "local_delivery", Detail: w.Detail}
	}
	if err = writeTask(ctx, tx, expected, t); err != nil {
		return err
	}
	return tx.Commit()
}

func completeChildren(ctx context.Context, tx *sql.Tx, parent core.Task, tasks []core.Task) error {
	if parent.Release == nil {
		return nil
	}
	if !core.AllIncluded(parent, tasks) || parent.Completion == nil {
		return errors.New("Plan completion requires exact included children and parent delivery proof")
	}
	for _, child := range tasks {
		if !core.Included(child, parent) {
			continue
		}
		child.Stage, child.NextRole, child.Pending, child.Attention = core.Completed, "", nil, nil
		child.Completion = &core.CompletionReceipt{Kind: "plan", ParentID: parent.ID, WriteID: child.Inclusion.WriteID, Commit: parent.Completion.Commit, Tree: parent.Completion.Tree}
		if err := writeTask(ctx, tx, child.Revision, child); err != nil {
			return err
		}
	}
	return nil
}
