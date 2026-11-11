//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Text is one literal argv element. Shell metacharacters and other placeholder
// spellings in a request must not be evaluated or recursively substituted.
func TestConfiguredCommandLiteralText(t *testing.T) {
	c, a := commandFixture(t)
	defer c.Close()
	text := "$(touch /tmp/runner-must-not-create); {workspace}\nsecond line"
	a.Inputs = map[string]runner.Value{"selection": runner.Text(text)}
	a.Setup.Profile.Command = &runner.Command{Args: []string{"/bin/echo", "{text.selection}"}}
	if _, err := c.Start(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	end := waitCommandEnd(t, c, a.AttemptID)
	if end.End.Kind != "returned" || !strings.Contains(string(end.End.Result.Outputs["check"].Inline), "$(touch /tmp/runner-must-not-create); {workspace}") {
		t.Fatalf("text was not passed literally: %+v", end.End)
	}
}

func TestConfiguredCommandPreparedInputMetadataIsLiteralAndTyped(t *testing.T) {
	job := commandJob{
		Workspace: &workspaceRecord{
			WorkDir: "/owned/source",
			Base:    strings.Repeat("a", 40),
		},
		Assignment: runner.Assignment{
			AttemptID: "attempt_check",
			Inputs: map[string]runner.Value{
				"patch":     {ArtifactID: "attempt_owner/changes.patch"},
				"selection": runner.Text("{source.base} {artifact_id.patch} $(echo no)"),
			},
		},
	}
	env := map[string]string{"TMPDIR": "/owned/scratch"}
	for value, want := range map[string]string{
		"{source.base}":           strings.Repeat("a", 40),
		"{artifact_id.patch}":     "attempt_owner/changes.patch",
		"{input.patch}":           "/owned/source/.runner-tmp/attempt_check/inputs/patch",
		"{text.selection}":        "{source.base} {artifact_id.patch} $(echo no)",
		"{source.other}":          "{source.other}",
		"{artifact_id.selection}": "{artifact_id.selection}",
	} {
		if got := commandValue(value, job, env); got != want {
			t.Fatalf("metadata substitution %q = %q, want %q", value, got, want)
		}
	}
	job.Assignment.Inputs["patch"] = runner.Text("forged/artifact")
	if got := commandValue("{artifact_id.patch}", job, env); got != "" {
		t.Fatalf("inline value invented artifact identity: %q", got)
	}
}

// A recovered patch may be measured before a writer runs. Keep that seed
// distinct from a captured candidate, and allow the later writer's new patch.
func TestConfiguredCommandInitialRetainedPatch(t *testing.T) {
	c, a := commandFixture(t)
	defer c.Close()
	ctx := context.Background()
	patch := addedSeedFile("seed.txt", "100644")
	c.read = func(_ context.Context, id string) ([]byte, error) {
		if id == "retained/changes.patch" {
			return patch, nil
		}
		return c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
	}
	c.workspaces.read = c.read
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: "retained/changes.patch"}}
	a.Setup.Profile.Command = &runner.Command{Args: []string{"/bin/cat", "{workspace}/seed.txt"}}
	check := func(want string) {
		t.Helper()
		a.Deadline = time.Now().Add(6 * time.Second)
		if _, err := c.Start(ctx, a); err != nil {
			t.Fatal(err)
		}
		end := waitCommandEnd(t, c, a.AttemptID)
		if end.End.Kind != "returned" || !strings.Contains(string(end.End.Result.Outputs["check"].Inline), want) {
			t.Fatalf("command did not read retained source: %+v", end.End)
		}
		if err := c.Release(ctx, a.AttemptID); err != nil {
			t.Fatal(err)
		}
	}
	check("retained")
	r, err := c.workspaces.load(a.RunID)
	if err != nil || !r.SeedReady || r.Candidate != nil {
		t.Fatalf("seed was not ready or was presented as captured work: %+v: %v", r, err)
	}
	if err := c.workspaces.releaseRun(ctx, a.RunID, true); err != nil {
		t.Fatal("recoverable seed blocked failed cleanup", err)
	}
	a.AttemptID = "attempt_reopened_seed"
	check("retained") // The same patch is restored after owned checkout cleanup.
	writer := a
	writer.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	r, err = c.workspaces.Prepare(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.WorkDir, "seed.txt"), []byte("corrected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	candidate, err := c.workspaces.Capture(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	a.AttemptID = "attempt_new_candidate"
	a.Inputs["patch"] = runner.Value{ArtifactID: candidate.PatchID}
	check("corrected")
	if err := c.workspaces.ForgetRun(ctx, a.RunID); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredCommandSelectsTestsFromPrivateCapture(t *testing.T) {
	c, a := commandFixture(t)
	defer c.Close()
	c.read = func(_ context.Context, id string) ([]byte, error) {
		return c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
	}
	c.workspaces.read = c.read
	writer := a
	writer.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	w, err := c.workspaces.Prepare(t.Context(), writer)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.WorkDir, "frontend", "src")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "captured.test.ts"), []byte("// captured regression\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := c.workspaces.Capture(t.Context(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = git(t.Context(), w.Repository, nil, nil, 4096, "cat-file", "-e", source.Tree); err == nil {
		t.Fatal("regression requires a private capture tree")
	}
	a.AttemptID = "attempt_capture_check"
	a.Inputs = map[string]runner.Value{
		"patch": {ArtifactID: source.PatchID},
		"tests": runner.Text(`["src/selected.test.ts"]`),
	}
	a.Setup.Profile.Command = &runner.Command{Args: []string{"/bin/echo", runner.FrontendCandidateTests}}
	a.Deadline = time.Now().Add(6 * time.Second)
	if _, err = c.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	end := waitCommandEnd(t, c, a.AttemptID)
	if end.End.Kind != "returned" || end.End.Result == nil {
		t.Fatalf("configured private capture check failed: %+v", end.End)
	}
	var output string
	if err = json.Unmarshal(end.End.Result.Outputs["check"].Inline, &output); err != nil || !strings.Contains(output, `["src/selected.test.ts","src/captured.test.ts"]`) {
		t.Fatalf("configured private capture check = %q, error %v", output, err)
	}
	if err = c.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = c.workspaces.ForgetRun(t.Context(), a.RunID); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredCommandNetworkSelection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "default-off"
		if enabled {
			name = "explicit-on"
		}
		t.Run(name, func(t *testing.T) {
			c, a := commandFixture(t)
			defer c.Close()
			a.Setup.Profile.Command = &runner.Command{
				Args:    []string{"/usr/bin/true"},
				Network: enabled,
			}
			if _, err := c.Start(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if end := waitCommandEnd(t, c, a.AttemptID); end.End.Kind != "returned" {
				t.Fatal(end.End)
			}
			config, err := c.disk.Read(commandControl(a.AttemptID)+"/config/config.toml", 16384)
			want := "[permissions.runner-command.network]\nenabled=false\n"
			if enabled {
				want = "[permissions.runner-command.network]\nenabled=true\n"
			}
			if err != nil || !strings.Contains(string(config), want) {
				t.Fatalf("command network selection: %v: %s", err, config)
			}
		})
	}
}

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

// A successful launch can precede the worker's durable startup receipt. Keep
// that interval Running only while this owner still knows the child is alive.
func TestConfiguredCommandBeforeStartupReceipt(t *testing.T) {
	c, a := commandFixture(t)
	defer c.Close()
	launched := make(chan func(), 1)
	c.launchWorker = func(_ string, finished func()) error {
		launched <- finished
		return nil
	}
	if _, err := c.Start(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	var exited func()
	select {
	case exited = <-launched:
	case <-time.After(4 * time.Second):
		t.Fatal("worker did not launch")
	}
	c.setup.Wait() // Preparation has finished; no startup receipt exists yet.
	o, err := c.Observe(context.Background(), a.AttemptID)
	if err != nil || o.State != runner.Running || o.End != nil {
		t.Fatalf("live owned child reported uncertain during startup: %+v, %v", o, err)
	}
	exited() // An exit without a final receipt is still genuinely uncertain.
	o, err = c.Observe(context.Background(), a.AttemptID)
	if err != nil || o.State != runner.Unknown || o.End != nil {
		t.Fatalf("child exit without settlement hidden: %+v, %v", o, err)
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

func TestCommandDomainResults(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		exit                  int
		mapping               map[int]string
		body                  string
		wantKind, wantOutcome string
	}{
		{name: "expected rejection", exit: 1, mapping: map[int]string{1: "changes_requested"}, wantKind: "returned", wantOutcome: "changes_requested"},
		{name: "unexpected failure", exit: 2, mapping: map[int]string{1: "changes_requested"}, wantKind: "failed"},
		{name: "benchmark continue", body: `{"outcome":"continue","reason":"measure another candidate"}`, wantKind: "returned", wantOutcome: "continue"},
		{name: "no worker-selected step", body: `{"outcome":"ready","next":"deploy"}`, wantKind: "failed"},
		{name: "trailing result", body: `{"outcome":"ready"} {}`, wantKind: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, a := commandFixture(t)
			defer c.Close()
			scratch := filepath.Join(c.disk.Root, "work", a.RunID, "source", ".runner-tmp", a.AttemptID)
			if err := os.MkdirAll(scratch, 0700); err != nil {
				t.Fatal(err)
			}
			a.Setup.Profile.Command = &runner.Command{ExitOutcomes: tc.mapping}
			if tc.body != "" {
				a.Setup.Profile.Command.Result = "domain.json"
				if err := os.WriteFile(filepath.Join(scratch, "domain.json"), []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			job := commandJob{Assignment: a, Workspace: &workspaceRecord{WorkDir: filepath.Join(c.disk.Root, "work", a.RunID, "source")}}
			end, err := collectCommand(c.disk, job, commandResult{ExitCode: tc.exit})
			if err != nil || end.Kind != tc.wantKind || tc.wantOutcome != "" && end.Result.Outcome != tc.wantOutcome {
				t.Fatalf("%+v %v", end, err)
			}
		})
	}
}

// Comparing baseline/proposed artifacts uses the same bounded input handoff,
// without granting access to other workspaces or Git history.
func TestConfiguredCommandComparisonInputs(t *testing.T) {
	c, a := commandFixture(t)
	defer c.Close()
	c.read = func(_ context.Context, id string) ([]byte, error) { return []byte(id + "\n"), nil }
	a.Inputs = map[string]runner.Value{"baseline": {ArtifactID: "baseline"}, "proposed": {ArtifactID: "proposed"}}
	a.Setup.Profile.Command = &runner.Command{Args: []string{"/bin/cat", "{input.baseline}", "{input.proposed}"}}
	if _, err := c.Start(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	done := waitCommandEnd(t, c, a.AttemptID)
	if done.End.Kind != "returned" || !strings.Contains(string(done.End.Result.Outputs["check"].Inline), "baseline\\nproposed") {
		t.Fatalf("comparison lost bounded inputs: %+v", done.End)
	}
}
