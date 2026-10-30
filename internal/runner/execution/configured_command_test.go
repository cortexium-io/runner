//go:build darwin || linux

package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// The test-only sandbox shim isolates worker lifecycle from native permissions.
// The real sandbox boundary is qualified by the zero-model project exercise.
func TestCommandWorkerProcess(t *testing.T) {
	if file := os.Getenv("RUNNER_COMMAND_WORKER_SPEC"); file != "" {
		if err := RunCommandWorker(context.Background(), file); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Skip("internal worker process")
}
func commandFixture(t *testing.T) (*Commands, runner.Assignment) {
	t.Helper()
	d := workspaceDisk(t)
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.WriteFile(sandbox, []byte("#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &runner.CodexSettings{Binary: sandbox, GoBinary: "go"}
	c := NewCommands(d, cfg, &Workspaces{disk: d}, nil)
	c.launchWorker = func(file string, finished func()) error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCommandWorkerProcess$", "-test.timeout=15s")
		cmd.Env = append(os.Environ(), "RUNNER_COMMAND_WORKER_SPEC="+file)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait(); finished() }()
		return nil
	}
	a := sourceAssignment(d, sourceRepository(t), "attempt_configured")
	a.Setup.Profile = runner.Profile{
		Harness: "command",
		Access:  "workspace-read",
		Command: &runner.Command{
			Args: []string{"/bin/sh", "-c", `mkfifo "$TMPDIR/gate"
printf ready > "$TMPDIR/ready"
IFS= read -r answer < "$TMPDIR/gate"
printf '%s' "$answer" > "$TMPDIR/bundle"`},
			Artifact: "bundle",
		},
	}
	a.Deadline = time.Now().Add(6 * time.Second)
	return c, a
}
func waitCommandFile(t *testing.T, file string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(file); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("command did not reach %s", file)
		case <-tick.C:
		}
	}
}
func waitCommandEnd(t *testing.T, c *Commands, id string) runner.Observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		o, err := c.Observe(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if o.State == runner.Finished {
			return o
		}
		select {
		case <-ctx.Done():
			t.Fatalf("command did not settle: %s", o.Reason)
		case <-tick.C:
		}
	}
}

// One actual worker process covers owner restart, at-most-once dispatch,
// cancellation and normal result retention without duplicating core policy tests.
func TestConfiguredCommandSurvivesOwnerAndSettles(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "completion"
		if stop {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			c, a := commandFixture(t)
			if _, err := c.Start(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			scratch := filepath.Join(c.disk.Root, "work", a.RunID, "source", ".runner-tmp", a.AttemptID)
			waitCommandFile(t, filepath.Join(scratch, "ready"))
			c.Close()
			reopened := NewCommands(c.disk, c.cfg, c.workspaces, nil)
			defer reopened.Close()
			if _, err := reopened.Start(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			// The immutable claim also rejects a duplicate helper, independently
			// of the coordinator's repeated Start protection.
			if err := RunCommandWorker(context.Background(), filepath.Join(c.disk.Root, commandControl(a.AttemptID), "spec.json")); err == nil {
				t.Fatal("duplicate worker was admitted")
			}
			if stop {
				if err := reopened.Stop(context.Background(), a.AttemptID); err != nil {
					t.Fatal(err)
				}
			} else {
				f, err := os.OpenFile(filepath.Join(scratch, "gate"), os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("literal $(not-a-shell)\n")
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			o := waitCommandEnd(t, reopened, a.AttemptID)
			want := "returned"
			if stop {
				want = "stopped"
			}
			if o.End.Kind != want {
				t.Fatalf("got %s: %s", o.End.Kind, o.End.Reason)
			}
			if !stop {
				b, err := c.disk.Read("artifacts/"+a.AttemptID+"/result", 1024)
				if err != nil || string(b) != "literal $(not-a-shell)" {
					t.Fatal("command result lost or interpreted", err)
				}
			}
			if err := reopened.Release(context.Background(), a.AttemptID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCommandCollectionAndUncertainWorker(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
		file string
		want string
	}{
		{"output", 0, "bundle", "returned"},
		{"failure", 2, "bundle", "failed"},
		{"missing output", 0, "", "failed"},
		{"oversized output", 0, "too large for allowance", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := workspaceDisk(t)
			a := sourceAssignment(d, "", "attempt_collection")
			a.Setup.Profile.Command = &runner.Command{Artifact: "bundle"}
			a.Limits.MaxArtifact = 8
			work := filepath.Join(d.Root, "work", "source")
			scratch := filepath.Join(work, ".runner-tmp", a.AttemptID)
			if err := os.MkdirAll(scratch, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(scratch, "bundle"), []byte(tc.file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			end, err := collectCommand(d, commandJob{Assignment: a, Workspace: &workspaceRecord{WorkDir: work}}, commandResult{ExitCode: tc.exit, Kind: "returned"})
			if err != nil || end.Kind != tc.want {
				t.Fatalf("collection=%+v: %v", end, err)
			}
		})
	}
	d := workspaceDisk(t)
	c := NewCommands(d, nil, nil, nil)
	defer c.Close()
	id := "attempt_unknown"
	if err := c.save(commandJob{Assignment: runner.Assignment{AttemptID: id}, Stage: "dispatching"}); err != nil {
		t.Fatal(err)
	}
	o, err := c.Observe(context.Background(), id)
	if err != nil || o.State != runner.Unknown || o.End != nil {
		t.Fatal("missing worker falsely settled", err)
	}
	if err := c.Release(context.Background(), id); err == nil {
		t.Fatal("uncertain resources released")
	}
}

func TestCommandArtifactRejectsSymlinkEscape(t *testing.T) {
	scratch, out := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "file"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(scratch, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := commandArtifact(scratch, "escape/file", 1024); err == nil {
		t.Fatal("artifact escaped scratch")
	}
}
