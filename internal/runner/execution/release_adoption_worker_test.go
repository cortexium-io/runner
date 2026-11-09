//go:build darwin || linux

package execution

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func adoptionCheckoutGate(t *testing.T) *captureGitFixture {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready, hold := filepath.Join(dir, "ready"), filepath.Join(dir, "hold")
	for _, name := range []string{ready, hold} {
		if out, err := exec.Command("mkfifo", name).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	reader, err := os.OpenFile(ready, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(hold, os.O_RDWR, 0600)
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	g := &captureGitFixture{dir: dir, ready: make(chan string, 4), fifo: reader, hold: writer}
	t.Cleanup(func() { reader.Close(); writer.Close() })
	go func() {
		r := bufio.NewReader(reader)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			g.ready <- strings.TrimSpace(line)
		}
	}()
	script := "#!/bin/sh\ncase \" $* \" in *' worktree add '*)\n printf 'checkout\\n' >> " + testShellQuote(filepath.Join(dir, "calls")) + "\n if [ ! -e " + testShellQuote(filepath.Join(dir, "blocked")) + " ]; then\n  : > " + testShellQuote(filepath.Join(dir, "blocked")) + "\n  printf 'checkout\\n' > " + testShellQuote(ready) + "\n  IFS= read -r release < " + testShellQuote(hold) + "\n fi;;\nesac\nexec " + testShellQuote(real) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return g
}

func startHeldAdoption(t *testing.T, f *sourceReleaseFixture, g *captureGitFixture) {
	t.Helper()
	f.a.Deadline = time.Now().Add(time.Minute)
	base, cancel := context.WithCancelCause(t.Context())
	call := captureCutoffContext{base}
	done := make(chan runner.Observation, 1)
	go func() {
		o, err := f.r.Start(call, f.a)
		if err != nil {
			t.Error(err)
		}
		done <- o
	}()
	select {
	case <-g.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("materialization did not reach its owned gate")
	}
	cancel(context.DeadlineExceeded)
	select {
	case o := <-done:
		if o.State != runner.Unknown || !o.Reconciling || o.End != nil {
			t.Fatal("observer expiry ended adoption", o)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not detach")
	}
	j, err := f.r.load(f.a.AttemptID)
	if err != nil || len(j.AdoptionReceipt) == 0 || !j.Assignment.Deadline.Equal(f.a.Deadline) {
		t.Fatal("authentication/deadline checkpoint changed", j, err)
	}
	// Rejoining local preparation must not invoke even this model-free operator again.
	if err = os.WriteFile(j.Command[0], []byte("#!/bin/sh\nexit 94\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestSourceAdoptionWorkerSurvivesObservationExpiryWithoutRebuildingCheckout(t *testing.T) {
	f := newSourceReleaseFixture(t, false)
	g := adoptionCheckoutGate(t)
	wake := make(chan string, 1)
	f.r.SetWake(func(id string) { wake <- id })
	startHeldAdoption(t, f, g)
	f.r.adoptionMu.Lock()
	original := f.r.adoptions[f.a.AttemptID]
	f.r.adoptionMu.Unlock()
	call, cancel := context.WithCancel(t.Context())
	cancel()
	o, err := f.r.Observe(call, f.a.AttemptID)
	if err != nil || o.State != runner.Unknown || !o.Reconciling {
		t.Fatal(o, err)
	}
	f.r.adoptionMu.Lock()
	same := f.r.adoptions[f.a.AttemptID] == original
	f.r.adoptionMu.Unlock()
	if !same {
		t.Fatal("observer replaced the original source worker")
	}
	if err = f.r.Release(t.Context(), f.a.AttemptID); err == nil {
		t.Fatal("active source preparation was released")
	}
	if _, err = g.hold.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	o, err = f.r.Observe(t.Context(), f.a.AttemptID)
	if err != nil || o.End == nil || o.End.Kind != "returned" || o.Source == nil || o.Source.Tree != f.tree {
		t.Fatal("adoption did not finish exact source", o.End, err)
	}
	select {
	case id := <-wake:
		if id != f.a.AttemptID {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("completed source did not wake Core")
	}
	data, err := os.ReadFile(filepath.Join(g.dir, "calls"))
	if err != nil || string(data) != "checkout\n" {
		t.Fatal("short observations restarted materialization", string(data), err)
	}
	if err = f.r.Release(t.Context(), f.a.AttemptID); err != nil {
		t.Fatal(err)
	}
}

func TestSourceAdoptionWorkerCloseRestartAndStopSettleSameAttempt(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "stop"}[stop], func(t *testing.T) {
			f := newSourceReleaseFixture(t, false)
			g := adoptionCheckoutGate(t)
			startHeldAdoption(t, f, g)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if stop {
				if err := f.r.Stop(ctx, f.a.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.r.Close(ctx); err != nil {
				t.Fatal("source worker did not settle on close", err)
			}
			resumed := &Releases{disk: f.r.disk, workspaces: f.r.workspaces, cfg: f.r.cfg}
			defer resumed.Close(context.Background())
			o, err := resumed.Observe(t.Context(), f.a.AttemptID)
			if stop {
				if err != nil || o.End == nil || o.End.Kind != "stopped" || o.Source != nil {
					t.Fatal("durable stop restarted source work", o.End, err)
				}
			} else if err != nil || o.End == nil || o.End.Kind != "returned" || o.Source == nil || o.Source.Tree != f.tree {
				t.Fatal("restart lost authenticated source/deadline", o.End, err)
			}
			j, err := resumed.load(f.a.AttemptID)
			if err != nil || j.Assignment.AttemptID != f.a.AttemptID || !j.Assignment.Deadline.Equal(f.a.Deadline) {
				t.Fatal("restart granted a new identity/deadline", err)
			}
		})
	}
}

func TestSourceAdoptionWorkerCloseDuringAcceptedObjectReadRetainsAuthentication(t *testing.T) {
	for _, suffix := range []string{"^{tree}", "^"} {
		t.Run(suffix, func(t *testing.T) {
			f := newSourceReleaseFixture(t, false)
			real, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			g := adoptionCheckoutGate(t)
			// The adapter reads each accepted object once before the workspace
			// helper validates it again. Interrupt that second, helper-owned read.
			script := "#!/bin/sh\nlast=''\nfor arg do last=$arg; done\n"
			script += "if [ \"$last\" = " + testShellQuote(f.head+suffix) + " ]; then\n"
			script += " if [ -e " + testShellQuote(filepath.Join(g.dir, "seen")) + " ] && [ ! -e " + testShellQuote(filepath.Join(g.dir, "blocked")) + " ]; then\n"
			script += "  : > " + testShellQuote(filepath.Join(g.dir, "blocked")) + "\n  printf 'object-read\\n' > " + testShellQuote(filepath.Join(g.dir, "ready")) + "\n  IFS= read -r release < " + testShellQuote(filepath.Join(g.dir, "hold")) + "\n"
			script += " fi\n : > " + testShellQuote(filepath.Join(g.dir, "seen")) + "\nfi\nexec " + testShellQuote(real) + " \"$@\"\n"
			if err = os.WriteFile(filepath.Join(g.dir, "git"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			startHeldAdoption(t, f, g)
			shutdown, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err = f.r.Close(shutdown); err != nil {
				t.Fatal("accepted-object reader did not settle on close", err)
			}
			saved, err := f.r.load(f.a.AttemptID)
			if err != nil || saved.End != nil || saved.Source != nil || len(saved.AdoptionReceipt) == 0 || !saved.Assignment.Deadline.Equal(f.a.Deadline) {
				t.Fatal("interrupted read became a source mismatch or lost its authentication", saved, err)
			}
			resumed := &Releases{disk: f.r.disk, workspaces: f.r.workspaces, cfg: f.r.cfg}
			defer resumed.Close(context.Background())
			o, err := resumed.Observe(t.Context(), f.a.AttemptID)
			if err != nil || o.End == nil || o.End.Kind != "returned" || o.Source == nil || o.Source.Tree != f.tree {
				t.Fatal("same authenticated adoption did not resume", o.End, err)
			}
		})
	}
}

func TestSourceAdoptionWorkerDoesNotExtendExpiredAssignment(t *testing.T) {
	f := newSourceReleaseFixture(t, false)
	f.a.Deadline = time.Now().Add(-time.Second)
	o, err := f.r.Start(t.Context(), f.a)
	if err != nil || o.State != runner.Unknown || o.End != nil || o.Source != nil {
		t.Fatal("expired source preparation received another allowance", o.End, err)
	}
	if _, err = f.r.workspaces.load(f.a.RunID); !os.IsNotExist(err) {
		t.Fatal("expired assignment materialized source", err)
	}
	j, err := f.r.load(f.a.AttemptID)
	if err != nil || !j.Assignment.Deadline.Equal(f.a.Deadline) {
		t.Fatal("expired deadline was changed", err)
	}
	if err = f.r.Stop(t.Context(), f.a.AttemptID); err != nil {
		t.Fatal(err)
	}
	o, err = f.r.Observe(t.Context(), f.a.AttemptID)
	if err != nil || o.End == nil || o.End.Kind != "stopped" || o.Source != nil {
		t.Fatal("expired operation did not settle without materialization", o.End, err)
	}
}

func TestSourceAdoptionWorkerIncludesAuthenticationAcrossExpiredObservers(t *testing.T) {
	f := newSourceReleaseFixture(t, false)
	f.a.Deadline = time.Now().Add(time.Minute)
	g := adoptionCheckoutGate(t)
	if err := os.WriteFile(filepath.Join(g.dir, "blocked"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	original := f.r.cfg.Command[0]
	wrapped := filepath.Join(t.TempDir(), "held-operator")
	script := "#!/bin/sh\nprintf 'auth\\n' >> " + testShellQuote(filepath.Join(g.dir, "calls")) + "\nprintf 'auth\\n' > " + testShellQuote(filepath.Join(g.dir, "ready")) + "\nIFS= read -r release < " + testShellQuote(filepath.Join(g.dir, "hold")) + "\nexec " + testShellQuote(original) + " \"$@\"\n"
	if err := os.WriteFile(wrapped, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.r.cfg.Command = []string{wrapped}
	base, cancel := context.WithCancelCause(t.Context())
	done := make(chan runner.Observation, 1)
	go func() {
		o, err := f.r.Start(captureCutoffContext{base}, f.a)
		if err != nil {
			t.Error(err)
		}
		done <- o
	}()
	select {
	case <-g.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("authentication did not reach gate")
	}
	cancel(context.DeadlineExceeded)
	select {
	case o := <-done:
		if o.State != runner.Unknown || !o.Reconciling {
			t.Fatal("observer killed read-only authentication", o)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not detach")
	}
	j, err := f.r.load(f.a.AttemptID)
	if err != nil || len(j.AdoptionReceipt) != 0 {
		t.Fatal("authentication was not actually pending", err)
	}
	call, stop := context.WithCancel(t.Context())
	stop()
	if o, err := f.r.Observe(call, f.a.AttemptID); err != nil || o.State != runner.Unknown || !o.Reconciling {
		t.Fatal(o, err)
	}
	if _, err = g.hold.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	o, err := f.r.Observe(t.Context(), f.a.AttemptID)
	if err != nil || o.End == nil || o.End.Kind != "returned" || o.Source == nil {
		t.Fatal("same authentication did not finish", o.End, err)
	}
	data, err := os.ReadFile(filepath.Join(g.dir, "calls"))
	if err != nil || strings.Count(string(data), "auth\n") != 1 || strings.Count(string(data), "checkout\n") != 1 {
		t.Fatal("observer restarted authentication/materialization", string(data), err)
	}
}
