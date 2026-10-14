package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

func TestTransactionsSurviveReopenAndRejectCompetingClaims(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Create(ctx, dir, config.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	task := core.Task{ID: "one", Revision: 1, Stage: core.Ready}
	if err = s.Add(ctx, task); err != nil {
		t.Fatal(err)
	}
	second := task
	second.ID = "two"
	if err = s.Add(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err = s.SetDrained(ctx, false); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a := core.Attempt{ID: "attempt-one", TaskID: task.ID, State: "reserved", Occupying: true}
	task.Stage = core.Implementing
	if err = s.Commit(ctx, 1, task, &a, true); err != nil {
		t.Fatal(err)
	}
	b := core.Attempt{ID: "attempt-two", TaskID: second.ID, State: "reserved", Occupying: true}
	second.Stage = core.Implementing
	if err = other.Commit(ctx, 1, second, &b, true); err == nil {
		t.Fatal("second connection reserved occupied slot")
	}
	if err = s.Commit(ctx, 1, task, nil, false); err == nil {
		t.Fatal("stale task revision accepted")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Task(ctx, "one")
	if err != nil || got.Stage != core.Implementing || got.Revision != 2 {
		t.Fatalf("committed claim lost: %+v %v", got, err)
	}
	got, err = s.Task(ctx, "two")
	if err != nil || got.Stage != core.Ready || got.Revision != 1 {
		t.Fatalf("failed transaction leaked: %+v %v", got, err)
	}
	attempts, err := s.Attempts(ctx, "")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempt transaction: %+v %v", attempts, err)
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
