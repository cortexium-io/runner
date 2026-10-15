package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

// Independent connections start together. One transaction must roll back its
// task state and counters when another wins the last slot or shared resource.
func TestCompetingClaims(t *testing.T) {
	for _, which := range []string{"capacity", "task", "workspace", "publication", "refresh"} {
		t.Run(which, func(t *testing.T) {
			ctx := t.Context()
			settings := config.Settings{Config: config.Config{Policy: config.Policy{MaxParallelism: 2}}}
			if which == "capacity" || which == "publication" || which == "refresh" {
				settings.Config.Policy.MaxParallelism = 1
			}
			if which == "publication" {
				settings.Config.Delivery.Mode = "github-draft"
				settings.Config.Delivery.Repository = "owner/repo"
				settings.Config.Repository.BaseRef = "refs/heads/main"
			}
			dir := filepath.Join(t.TempDir(), "state")
			s, err := Create(ctx, dir, settings)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			other, err := Open(ctx, dir, false)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			tasks := []core.Task{{ID: "one", Revision: 1, Generation: 1, Stage: core.Ready, Versions: []core.Version{{Number: 1}}, Approval: &core.Approval{Generation: 1, Base: "base"}}, {ID: "two", Revision: 1, Generation: 1, Stage: core.Ready, Versions: []core.Version{{Number: 1}}, Approval: &core.Approval{Generation: 1, Base: "base"}}}
			if which == "publication" {
				tasks[0].Stage = core.AwaitingDelivery
				tasks[0].Candidate = &core.Candidate{Commit: "accepted"}
			}
			for _, task := range tasks {
				if err = s.Add(ctx, task); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.SetDrained(ctx, false); err != nil {
				t.Fatal(err)
			}
			if which == "task" {
				tasks[1] = tasks[0]
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, db := range []*Store{s, other} {
				task := tasks[i]
				if which == "publication" && i == 0 {
					p := core.Publication{State: "running", Occupying: true, Intent: core.PublicationIntent{ID: core.NewID(), TaskID: task.ID, Version: 1, Generation: 1, ApprovedBase: "base", Candidate: *task.Candidate, ReviewDigest: core.Digest(task.Review), Repository: "owner/repo", BaseBranch: "main"}}
					go func() { <-start; results <- db.SavePublication(ctx, p, true) }()
					continue
				}
				task.Stage = core.Implementing
				task.Usage.Invocations = 1
				resource := task.ID
				if which == "workspace" {
					resource = "shared"
				}
				a := core.Attempt{ID: core.NewID(), TaskID: task.ID, Version: 1, Generation: 1, Base: "base", PolicyRevision: settings.Config.Revision(), WorkspaceClaim: resource, Invocation: 1, Occupying: true, State: "reserved"}
				if which == "refresh" && i == 0 {
					a.Role = "refresh"
					a.Invocation, task.Usage.Invocations = 0, 0
				}
				go func() { <-start; results <- db.Commit(ctx, 1, task, &a, true) }()
			}
			close(start)
			won := 0
			for range 2 {
				err = <-results
				if err == nil {
					won++
				} else if !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			}
			if won != 1 {
				t.Fatalf("%d competing claims admitted", won)
			}
			s.Close()
			s, err = Open(ctx, dir, true)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			attempts, err := s.Attempts(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			publications, err := s.Publications(ctx, "")
			if err != nil || len(attempts)+len(publications) != 1 {
				t.Fatalf("reservation lost: %+v %v", attempts, err)
			}
			persisted, err := s.Tasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			count, deterministic := 0, 0
			for _, a := range attempts {
				if a.Role == "refresh" {
					deterministic++
				}
			}
			for _, task := range persisted {
				count += task.Usage.Invocations
				extra := 0
				for _, a := range attempts {
					if a.TaskID == task.ID && a.Role == "refresh" {
						extra++
					}
				}
				if task.Revision != 1+task.Usage.Invocations+extra {
					t.Fatal("partial claim leaked", task)
				}
			}
			if count+len(publications)+deterministic != 1 {
				t.Fatal("allowance transaction leaked", count)
			}
		})
	}
}

func TestMissingStoreIsNotCreatedByInspection(t *testing.T) {
	if s, err := Open(t.Context(), filepath.Join(t.TempDir(), "missing"), true); err == nil {
		s.Close()
		t.Fatal("missing store opened")
	}
}

func TestRefreshReservationChecksAllowanceInTransaction(t *testing.T) {
	settings := config.Settings{Config: config.Config{Policy: config.Policy{MaxBaseRefreshes: 0}}}
	s, err := Create(t.Context(), filepath.Join(t.TempDir(), "state"), settings)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := core.Task{ID: "one", Revision: 1, Versions: []core.Version{{Number: 1}}}
	if err = s.Add(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	task.Usage.BaseRefreshes = 1
	task.RefreshTarget = &core.BaseBinding{Commit: "new", Cycle: 1}
	a := core.Attempt{ID: "refresh", TaskID: task.ID, Role: "refresh"}
	if err = s.Commit(t.Context(), 1, task, &a, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale policy admitted a cycle", err)
	}
	got, err := s.Task(t.Context(), task.ID)
	if err != nil || got.RefreshTarget != nil || got.Usage.BaseRefreshes != 0 {
		t.Fatal("failed reservation leaked", got, err)
	}
}
func TestCorruptStoreIsNotReplacedByOpen(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runner.db")
	contents := []byte("not a database")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	for _, readonly := range []bool{true, false} {
		if s, err := Open(context.Background(), dir, readonly); err == nil {
			s.Close()
			t.Fatal("corrupt authority opened")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(contents) {
		t.Fatal("corrupt state replaced")
	}
}

func TestPreviousStoreRequiresFreshStateWithoutModification(t *testing.T) {
	for _, format := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "runner.db")
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			old, err := connect(t.Context(), dir, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = old.db.ExecContext(t.Context(), fmt.Sprintf("CREATE TABLE metadata (id INTEGER, format INTEGER); INSERT INTO metadata VALUES(1,%d)", format)); err != nil {
				t.Fatal(err)
			}
			old.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, readonly := range []bool{true, false} {
				s, err := Open(t.Context(), dir, readonly)
				if err == nil {
					s.Close()
					t.Fatal("old state silently converted")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("old state modified")
			}

		})
	}
}

// Queueing a new update (or reconciling a confirmed one) and queueing refresh
// must be mutually exclusive, even across independent operator/coordinator reads.
func TestDraftSynchronizationAndRefreshQueueAtomically(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(fmt.Sprintf("reconcile=%t", reconcile), func(t *testing.T) {
			ctx := t.Context()
			settings := config.Settings{Config: config.Config{Repository: config.Repository{BaseRef: "refs/heads/main"}}}
			settings.Config.Delivery.Mode, settings.Config.Delivery.Repository = "github-draft", "owner/repo"
			dir := filepath.Join(t.TempDir(), "state")
			s, err := Create(ctx, dir, settings)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			other, err := Open(ctx, dir, false)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			task := core.Task{ID: "task", Revision: 1, Generation: 1, Stage: core.AwaitingDelivery, Versions: []core.Version{{Number: 1}}, Approval: &core.Approval{Generation: 1, Base: "base"}, Candidate: &core.Candidate{Commit: "original"}}
			if err = s.Add(ctx, task); err != nil {
				t.Fatal(err)
			}
			p := core.Publication{State: "published", Intent: core.PublicationIntent{ID: "original", TaskID: task.ID, Version: 1, Generation: 1, ApprovedBase: "base", Candidate: *task.Candidate, ReviewDigest: core.Digest(task.Review), Repository: "owner/repo", BaseBranch: "main", Branch: "runner-next/original"}, Observation: &core.RemotePublication{Number: 7, URL: "https://github.com/owner/repo/pull/7", BranchCommit: "original", State: "OPEN", Draft: true}}
			if err = s.SavePublication(ctx, p, false); err != nil {
				t.Fatal(err)
			}
			p.Revision = 1
			if !reconcile {
				task.Candidate = &core.Candidate{Commit: "refreshed"}
				task.BaseProof = &core.BaseProof{Base: "base", Candidate: *task.Candidate, ReviewDigest: core.Digest(task.Review), ObservedAt: time.Now().UTC()}
				if err = s.Commit(ctx, task.Revision, task, nil, false); err != nil {
					t.Fatal(err)
				}
				task.Revision++
				p.Revision, p.Intent.ID, p.Intent.Candidate = 0, "sync", *task.Candidate
				p.Intent.Sync = &core.DraftSync{PreviousID: "original", PublicationID: "original", PreviousCommit: "original", Number: 7, URL: p.Observation.URL, Base: "base", ObservedAt: task.BaseProof.ObservedAt}
			}
			p.State = "queued"
			task.NextRole, task.BaseProof = "refresh", nil
			start, results := make(chan struct{}), make(chan error, 2)
			go func() { <-start; results <- s.SavePublication(ctx, p, false) }()
			go func() { <-start; results <- other.Commit(ctx, task.Revision, task, nil, false) }()
			close(start)
			won := 0
			for range 2 {
				err = <-results
				if err == nil {
					won++
				} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrPublicationPending) {
					t.Fatal(err)
				}
			}
			if won != 1 {
				t.Fatal("refresh and synchronization both queued", won)
			}
			stored, err := s.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			ps, err := s.Publications(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			queued := ps[len(ps)-1].State == "queued"
			if queued == (stored.NextRole == "refresh") {
				t.Fatal("partial state leaked", stored, ps)
			}
		})
	}
}
