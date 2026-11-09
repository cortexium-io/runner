package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSourceArchivePendingRequiresPositiveObservationDeadline(t *testing.T) {
	ordinary := exec.Command("/bin/sh", "-c", "exit 7").Run()
	signalled := exec.Command("/bin/sh", "-c", "kill -KILL $$").Run()
	var exit *exec.ExitError
	if !errors.As(ordinary, &exit) || exit.ExitCode() != 7 || !errors.As(signalled, &exit) || exit.ExitCode() != -1 {
		t.Fatal("fixture did not produce distinct Git-style exit and signal results", ordinary, signalled)
	}
	deadline, stopDeadline := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stopDeadline()
	cancelled, stopCancelled := context.WithCancel(t.Context())
	stopCancelled()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"deadline killed archive", deadline, signalled, true},
		{"deadline before archive launch", deadline, context.DeadlineExceeded, true},
		{"ordinary exit at deadline", deadline, ordinary, false},
		{"ordinary exit", t.Context(), ordinary, false},
		{"unexplained signal", t.Context(), signalled, false},
		{"cancellation signal", cancelled, signalled, false},
		{"cancellation before launch", cancelled, context.Canceled, false},
		{"missing executable at deadline", deadline, exec.ErrNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := releaseArchiveError(tc.ctx, tc.err)
			var pending *sourceArchivePending
			if errors.As(got, &pending) != tc.want || !errors.Is(got, tc.err) {
				t.Fatal("source preparation hid a real failure or lost its cause", got)
			}
		})
	}
}

func pendingSourceJob(t *testing.T) (*Releases, releaseJob) {
	t.Helper()
	disk, repo := workspaceDisk(t), sourceRepository(t)
	head := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	a := sourceAssignment(disk, repo, "attempt_source_archive")
	a.ProjectID = "demo"
	a.Source.Ref = head
	a.Setup.Profile = runner.Profile{Harness: "release", Mode: "readiness", Access: "input-only"}
	operator := filepath.Join(t.TempDir(), "operator")
	if err := os.WriteFile(operator, []byte("#!/bin/sh\nprintf '%s\\n' '{\"key\":\"attempt_source_archive\",\"state\":\"running\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := &Releases{disk: disk, workspaces: &Workspaces{disk: disk}}
	j := releaseJob{
		Assignment:   a,
		Command:      []string{operator},
		GitHub:       "/fixture-gh",
		SelectedBase: head,
	}
	if err := r.save(j); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.workspaces.ForgetRun(context.Background(), a.RunID) })
	return r, j
}

func TestReleaseSourceDeadlineRejoinsSameSavedPreparation(t *testing.T) {
	r, j := pendingSourceJob(t)
	deadline, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	o, err := r.Observe(deadline, j.Assignment.AttemptID)
	if err != nil || o.State != runner.Unknown || !o.Reconciling || o.End != nil {
		t.Fatal("read-only archive deadline escaped configured self-reconciliation", o, err)
	}
	saved, err := r.load(j.Assignment.AttemptID)
	if err != nil || saved.SourcePrepared || saved.SelectedBase != j.SelectedBase || saved.Assignment.AttemptID != j.Assignment.AttemptID || saved.End != nil {
		t.Fatal("deadline changed the saved source operation", saved, err)
	}
	o, err = r.Observe(t.Context(), j.Assignment.AttemptID)
	if err != nil || o.State != runner.Running || o.Reconciling || o.Source == nil || o.Source.Base != j.SelectedBase {
		t.Fatal("same source operation did not finish preparation after rejoin", o, err)
	}
	saved, err = r.load(j.Assignment.AttemptID)
	if err != nil || !saved.SourcePrepared || saved.SelectedBase != j.SelectedBase || saved.Assignment.AttemptID != j.Assignment.AttemptID || saved.End != nil {
		t.Fatal("rejoin replaced or prematurely finished the saved attempt", saved, err)
	}
}

func TestReleaseSourcePreparationDoesNotHideOtherFailures(t *testing.T) {
	for _, mode := range []string{"invalid setup", "ordinary Git exit", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			r, j := pendingSourceJob(t)
			ctx := t.Context()
			switch mode {
			case "invalid setup":
				j.GitHub = ""
			case "ordinary Git exit":
				j.SelectedBase = strings.Repeat("f", 40)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if err := r.save(j); err != nil {
				t.Fatal(err)
			}
			o, err := r.Observe(ctx, j.Assignment.AttemptID)
			if err != nil || o.State != runner.Unknown || o.Reconciling || o.End != nil || o.Reason == "" {
				t.Fatal("source preparation hid an unresolved failure", mode, o, err)
			}
		})
	}
}
