package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

// Independent connections start together. One transaction must roll back its
// task state and counters when another wins the last slot or shared resource.
func TestCompetingClaims(t *testing.T) {
	for _, which := range []string{"capacity", "task", "workspace"} {
		t.Run(which, func(t *testing.T) {
			ctx := t.Context()
			settings := config.Settings{Config: config.Config{Policy: config.Policy{MaxParallelism: 2}}}
			if which == "capacity" {
				settings.Config.Policy.MaxParallelism = 1
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
				task.Stage = core.Implementing
				task.Usage.Invocations = 1
				resource := task.ID
				if which == "workspace" {
					resource = "shared"
				}
				a := core.Attempt{ID: core.NewID(), TaskID: task.ID, Version: 1, Generation: 1, Base: "base", PolicyRevision: settings.Config.Revision(), WorkspaceClaim: resource, Invocation: 1, Occupying: true, State: "reserved"}
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
			if err != nil || len(attempts) != 1 {
				t.Fatalf("reservation lost: %+v %v", attempts, err)
			}
			persisted, err := s.Tasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, task := range persisted {
				count += task.Usage.Invocations
				if task.Revision != 1+task.Usage.Invocations {
					t.Fatal("partial claim leaked", task)
				}
			}
			if count != 1 {
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
	for _, format := range []int{1, 2, 3} {
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
