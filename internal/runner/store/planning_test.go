package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

func TestReleaseIsAtomicAcrossFailureAndCompetingConnections(t *testing.T) {
	for _, collision := range []bool{true, false} {
		t.Run(map[bool]string{true: "rollback", false: "competing releases"}[collision], func(t *testing.T) {
			ctx := t.Context()
			dir := filepath.Join(t.TempDir(), "state")
			s, err := Create(ctx, dir, config.Settings{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			other, err := Open(ctx, dir, false)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			body := "## Outcome\nGreeting\n## Scope\nFile\n## Acceptance\nHello\n## Verification\nRead file; expect hello.\n"
			parent := core.Task{ID: "plan", Revision: 1, PlanRequest: true, Proposal: &core.PlanningResult{Body: body, Cards: []core.PlannedCard{{Key: "one", Body: body}, {Key: "two", Body: body}}}}
			if err = s.Add(ctx, parent); err != nil {
				t.Fatal(err)
			}
			parent.Release = &core.PlanRelease{Preview: "approved", Children: []string{"one", "two"}}
			children := []core.Task{{ID: "one", Revision: 1}, {ID: "two", Revision: 1}}
			if collision {
				if err = s.Add(ctx, children[1]); err != nil {
					t.Fatal(err)
				}
				if err = s.Release(ctx, 1, parent, children); err == nil {
					t.Fatal("collision accepted")
				}
				tasks, err := other.Tasks(ctx)
				if err != nil || len(tasks) != 2 {
					t.Fatal("partial batch leaked", tasks, err)
				}
				got, _ := other.Task(ctx, parent.ID)
				if got.Release != nil || got.Revision != 1 {
					t.Fatal("receipt committed without children")
				}
				return
			}
			start, results := make(chan struct{}), make(chan error, 2)
			for _, db := range []*Store{s, other} {
				go func() { <-start; results <- db.Release(ctx, 1, parent, children) }()
			}
			close(start)
			wins := 0
			for range 2 {
				err = <-results
				if err == nil {
					wins++
				} else if !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			}
			tasks, err := s.Tasks(ctx)
			if err != nil || wins != 1 || len(tasks) != 3 {
				t.Fatal("release duplicated or partial", wins, tasks, err)
			}
		})
	}
}

func TestClaimRechecksDependencyAfterEligibilityWasRead(t *testing.T) {
	ctx := t.Context()
	settings := config.Settings{Config: config.Config{Policy: config.Policy{MaxParallelism: 1}}}
	s, err := Create(ctx, filepath.Join(t.TempDir(), "state"), settings)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := core.Task{ID: "base", Revision: 1, Stage: core.Completed, Versions: []core.Version{{Number: 1}}}
	child := core.Task{ID: "child", Revision: 1, Generation: 1, Versions: []core.Version{{Number: 1, Dependencies: []core.Dependency{{TaskID: "base", Version: 1}}}}, Approval: &core.Approval{Generation: 1, Base: "base"}}
	for _, task := range []core.Task{base, child} {
		if err = s.Add(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if core.PrerequisiteWait(child, []core.Task{base}) != "" {
		t.Fatal("fixture not eligible")
	}
	base.Versions[0].Number = 2
	if err = s.Commit(ctx, 1, base, nil, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SetDrained(ctx, false); err != nil {
		t.Fatal(err)
	}
	child.Usage.Invocations = 1
	a := core.Attempt{ID: "attempt", TaskID: child.ID, Version: 1, Generation: 1, Base: "base", PolicyRevision: settings.Config.Revision(), WorkspaceClaim: "child", Invocation: 1, Occupying: true}
	if err = s.Commit(ctx, 1, child, &a, true); !errors.Is(err, ErrConflict) {
		t.Fatal("stale dependency admitted", err)
	}
	got, _ := s.Task(ctx, child.ID)
	attempts, _ := s.Attempts(ctx, child.ID)
	if got.Usage.Invocations != 0 || len(attempts) != 0 {
		t.Fatal("failed claim consumed allowance")
	}
}
