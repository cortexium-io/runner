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

func localReleaseGate(t *testing.T, stage string, afterPush, loseReply bool) *captureGitFixture {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	g := adoptionCheckoutGate(t)
	script := "#!/bin/sh\nstage=''\nfor arg do\n case \"$arg\" in archive|push|ls-remote) stage=$arg;; add) if [ \"$previous\" = worktree ]; then stage=checkout; fi;; esac\n previous=$arg\ndone\n"
	script += "if [ -n \"$stage\" ]; then printf '%s\\n' \"$stage\" >> " + testShellQuote(filepath.Join(g.dir, "calls")) + "; fi\n"
	script += "if [ \"$stage\" = " + testShellQuote(stage) + " ] && [ ! -e " + testShellQuote(filepath.Join(g.dir, "blocked")) + " ]; then\n : > " + testShellQuote(filepath.Join(g.dir, "blocked")) + "\n"
	if afterPush {
		script += testShellQuote(real) + " \"$@\"\n status=$?\n if [ \"$status\" != 0 ]; then exit \"$status\"; fi\n"
	}
	script += " printf '%s\\n' \"$stage\" > " + testShellQuote(filepath.Join(g.dir, "ready")) + "\n IFS= read -r release < " + testShellQuote(filepath.Join(g.dir, "hold")) + "\n"
	if afterPush {
		if loseReply {
			script += " exit 255\n"
		} else {
			script += " exit 0\n"
		}
	}
	script += "fi\nexec " + testShellQuote(real) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(g.dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return g
}

func localReadinessFixture(t *testing.T) (*Releases, releaseJob, string) {
	t.Helper()
	r, j := pendingSourceJob(t)
	j.Assignment.Deadline = time.Now().Add(time.Minute)
	j.LocalPreparationOwned = true
	calls := filepath.Join(t.TempDir(), "backend-calls")
	response := "{\"key\":\"" + j.Assignment.AttemptID + "\",\"state\":\"finished\",\"receipt\":{\"source_ref\":\"" + j.SelectedBase + "\"}}"
	script := "#!/bin/sh\nprintf 'backend\\n' >> " + testShellQuote(calls) + "\nprintf '%s\\n' " + testShellQuote(response) + "\n"
	if err := os.WriteFile(j.Command[0], []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.save(j); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r, j, calls
}

func localPublicationFixture(t *testing.T) (*Releases, releaseJob, string, string, string) {
	t.Helper()
	f := newSourceReleaseFixture(t, false)
	adoptSourceFixture(t, f)
	bare := t.TempDir()
	fixtureGit(t, bare, "init", "--bare", "--quiet", "--template=")
	fixtureGit(t, f.a.Repository, "remote", "add", "origin", bare)
	remoteRef := candidateRef("original")
	fixtureGit(t, f.a.Repository, "push", "--quiet", "origin", f.head+":"+remoteRef)
	head := reviewedContinuationCommit(t, f.a.Repository, f.base, f.a.RunID, "local preparation")
	a := runner.Clone(f.a)
	a.AttemptID, a.Setup.Profile.Mode, a.Deadline = "publish_local", "publish", time.Now().Add(time.Minute)
	a.Inputs = map[string]runner.Value{"candidate": runner.Text(candidateRef(a.RunID) + "\n" + head + "\nreviewed"), "previous": {Inline: mustReleaseJSON(t, f.receipt)}}
	receipt := f.receipt
	receipt.Key, receipt.Action, receipt.Head = a.AttemptID, "publish", head
	calls := filepath.Join(t.TempDir(), "backend-calls")
	binary := filepath.Join(t.TempDir(), "operator")
	response := mustReleaseJSON(t, map[string]any{"key": a.AttemptID, "state": "finished", "receipt": receipt})
	script := "#!/bin/sh\nprintf 'backend\\n' >> " + testShellQuote(calls) + "\nprintf '%s\\n' " + testShellQuote(string(response)) + "\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	j := releaseJob{Assignment: a, Command: []string{binary}, GitHub: "/usr/bin/true", LocalPreparationOwned: true}
	if err := f.r.save(j); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.r.Close(context.Background()) })
	return f.r, j, bare, remoteRef, calls
}

func observeHeldPreparation(t *testing.T, r *Releases, j releaseJob, g *captureGitFixture) {
	t.Helper()
	base, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	done := make(chan runner.Observation, 1)
	go func() {
		o, err := r.Observe(captureCutoffContext{base}, j.Assignment.AttemptID)
		if err != nil {
			t.Error(err)
		}
		done <- o
	}()
	select {
	case <-g.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("local preparation did not reach gate")
	}
	cancel(context.DeadlineExceeded)
	select {
	case o := <-done:
		if o.State != runner.Unknown || !o.Reconciling || o.End != nil {
			t.Fatal("observer ended local preparation", o)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not detach")
	}
}

func assertNoBackend(t *testing.T, path string) {
	t.Helper()
	if data, err := os.ReadFile(path); !os.IsNotExist(err) {
		t.Fatal("backend dispatched during local preparation", string(data), err)
	}
}

func settledPreparation(t *testing.T, r *Releases, id string) runner.Observation {
	t.Helper()
	r.adoptionMu.Lock()
	work := r.adoptions[id]
	r.adoptionMu.Unlock()
	if work == nil {
		t.Fatal("saved preparation worker missing")
	}
	select {
	case <-work.done:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation worker did not settle")
	}
	r.adoptionMu.Lock()
	defer r.adoptionMu.Unlock()
	if work.err != nil {
		t.Fatal(work.err)
	}
	return runner.Clone(work.observation)
}

func saveLegacyRelease(t *testing.T, r *Releases, j releaseJob) {
	t.Helper()
	j.LocalPreparationOwned, j.OperatorDispatched, j.PushStarted = false, false, false
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "local_preparation_owned") || strings.Contains(string(raw), "operator_dispatched") || strings.Contains(string(raw), "push_started") {
		t.Fatal("legacy fixture contains new markers")
	}
	if err = r.disk.Write(releaseName(j.Assignment.AttemptID), raw, r.disk.Policy.Database.MaxRecord); err != nil {
		t.Fatal(err)
	}
}

func TestReleasePreparationReadinessJoinsArchiveAndCheckout(t *testing.T) {
	for _, stage := range []string{"archive", "checkout"} {
		t.Run(stage, func(t *testing.T) {
			r, j, calls := localReadinessFixture(t)
			g := localReleaseGate(t, stage, false, false)
			wake := make(chan string, 1)
			r.SetWake(func(id string) { wake <- id })
			observeHeldPreparation(t, r, j, g)
			assertNoBackend(t, calls)
			r.adoptionMu.Lock()
			original := r.adoptions[j.Assignment.AttemptID]
			r.adoptionMu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			o, err := r.Observe(ctx, j.Assignment.AttemptID)
			if err != nil || o.State != runner.Unknown || !o.Reconciling {
				t.Fatal(o, err)
			}
			r.adoptionMu.Lock()
			same := original == r.adoptions[j.Assignment.AttemptID]
			r.adoptionMu.Unlock()
			if !same {
				t.Fatal("observation restarted preparation")
			}
			if err = r.Release(t.Context(), j.Assignment.AttemptID); err == nil {
				t.Fatal("active preparation released")
			}
			if _, err = g.hold.Write([]byte("continue\n")); err != nil {
				t.Fatal(err)
			}
			o, err = r.Observe(t.Context(), j.Assignment.AttemptID)
			if err != nil || o.End == nil || o.End.Kind != "returned" {
				t.Fatal("readiness did not complete", o.End, err)
			}
			select {
			case id := <-wake:
				if id != j.Assignment.AttemptID {
					t.Fatal(id)
				}
			case <-time.After(time.Second):
				t.Fatal("preparation did not wake Core")
			}
			data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
			if strings.Count(string(data), "archive\n") != 1 || strings.Count(string(data), "checkout\n") != 1 {
				t.Fatal("observer replayed source preparation", string(data))
			}
			saved, _ := r.load(j.Assignment.AttemptID)
			if !saved.SourceArchived || !saved.SourcePrepared || !saved.OperatorDispatched || !saved.Assignment.Deadline.Equal(j.Assignment.Deadline) {
				t.Fatal("source checkpoints/deadline differ", saved)
			}
			if err = r.Release(t.Context(), j.Assignment.AttemptID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleasePreparationReadinessRestartReusesArchiveAndStopDoesNotDispatch(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "stop"}[stop], func(t *testing.T) {
			r, j, calls := localReadinessFixture(t)
			g := localReleaseGate(t, "checkout", false, false)
			observeHeldPreparation(t, r, j, g)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if stop {
				if err := r.Stop(ctx, j.Assignment.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Close(ctx); err != nil {
				t.Fatal(err)
			}
			resumed := &Releases{disk: r.disk, workspaces: r.workspaces}
			defer resumed.Close(context.Background())
			o, err := resumed.Observe(t.Context(), j.Assignment.AttemptID)
			if err != nil || o.End == nil {
				t.Fatal(o, err)
			}
			if stop {
				if o.End.Kind != "stopped" {
					t.Fatal(o.End)
				}
				assertNoBackend(t, calls)
			} else if o.End.Kind != "returned" {
				t.Fatal(o.End)
			}
			data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
			if strings.Count(string(data), "archive\n") != 1 {
				t.Fatal("restart repeated completed archive", string(data))
			}
		})
	}
}

func TestReleasePreparationPublicationObserverAndLostReplyRejoinSameCAS(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "slow", true: "lost-reply"}[lost], func(t *testing.T) {
			r, j, bare, ref, calls := localPublicationFixture(t)
			g := localReleaseGate(t, "push", lost, lost)
			observeHeldPreparation(t, r, j, g)
			assertNoBackend(t, calls)
			if _, err := g.hold.Write([]byte("continue\n")); err != nil {
				t.Fatal(err)
			}
			var o runner.Observation
			var err error
			if lost {
				o = settledPreparation(t, r, j.Assignment.AttemptID)
				if o.End != nil || !o.Reconciling {
					t.Fatal("lost push acknowledgement was accepted", o)
				}
			}
			o, err = r.Observe(t.Context(), j.Assignment.AttemptID)
			if err != nil || o.End == nil || o.End.Kind != "returned" {
				t.Fatal("publication did not rejoin", o.End, err)
			}
			saved, _ := r.load(j.Assignment.AttemptID)
			actual := strings.TrimSpace(string(fixtureGit(t, bare, "rev-parse", ref)))
			if saved.Candidate != actual || saved.Candidate == "" || !saved.PushStarted || !saved.OperatorDispatched {
				t.Fatal("publication checkpoints differ", saved, actual)
			}
			data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
			if strings.Count(string(data), "push\n") != 1 {
				t.Fatal("lost acknowledgement repeated CAS", string(data))
			}
			if err = r.Release(t.Context(), j.Assignment.AttemptID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleasePreparationPublicationStopRequiresRemoteEffectWitness(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncertain-old-ref", true: "confirmed-target"}[applied], func(t *testing.T) {
			r, j, _, _, calls := localPublicationFixture(t)
			g := localReleaseGate(t, "push", applied, false)
			observeHeldPreparation(t, r, j, g)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := r.Stop(ctx, j.Assignment.AttemptID); err != nil {
				t.Fatal(err)
			}
			o, err := r.Observe(t.Context(), j.Assignment.AttemptID)
			if err != nil {
				t.Fatal(err)
			}
			if applied {
				if o.End == nil || o.End.Kind != "stopped" {
					t.Fatal("confirmed remote effect did not settle", o)
				}
				saved, _ := r.load(j.Assignment.AttemptID)
				if saved.Candidate == "" || !strings.Contains(o.End.Reason, saved.Candidate) {
					t.Fatal("known effect was omitted", o.End, saved.Candidate)
				}
			} else if o.End != nil || !o.Reconciling {
				t.Fatal("unchanged ref invented push settlement", o)
			}
			assertNoBackend(t, calls)
			data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
			if strings.Count(string(data), "push\n") != 1 {
				t.Fatal("Stop repeated CAS", string(data))
			}
		})
	}
}

func TestReleasePreparationPublicationStopBeforePushAndRestartAfterAppliedPush(t *testing.T) {
	for _, stage := range []string{"ls-remote", "push"} {
		t.Run(stage, func(t *testing.T) {
			r, j, _, _, calls := localPublicationFixture(t)
			g := localReleaseGate(t, stage, stage == "push", false)
			observeHeldPreparation(t, r, j, g)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if stage == "ls-remote" {
				if err := r.Stop(ctx, j.Assignment.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Close(ctx); err != nil {
				t.Fatal(err)
			}
			resumed := &Releases{disk: r.disk, workspaces: r.workspaces}
			defer resumed.Close(context.Background())
			o, err := resumed.Observe(t.Context(), j.Assignment.AttemptID)
			if err != nil || o.End == nil {
				t.Fatal(o, err)
			}
			if stage == "ls-remote" {
				if o.End.Kind != "stopped" {
					t.Fatal(o.End)
				}
				assertNoBackend(t, calls)
			} else if o.End.Kind != "returned" {
				t.Fatal(o.End)
			}
			data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
			want := 0
			if stage == "push" {
				want = 1
			}
			if strings.Count(string(data), "push\n") != want {
				t.Fatal("restart/Stop issued another push", string(data))
			}
		})
	}
}

func TestReleasePreparationPublicationUnexpectedRemoteRefBlocks(t *testing.T) {
	r, j, bare, ref, calls := localPublicationFixture(t)
	g := localReleaseGate(t, "push", true, true)
	observeHeldPreparation(t, r, j, g)
	if _, err := g.hold.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	if o := settledPreparation(t, r, j.Assignment.AttemptID); o.End != nil || !o.Reconciling {
		t.Fatal(o)
	}
	other := j.Assignment.Source.Ref
	fixtureGit(t, bare, "update-ref", ref, other)
	o, err := r.Observe(t.Context(), j.Assignment.AttemptID)
	if err != nil || o.End != nil || o.Reconciling || !strings.Contains(o.Reason, "remote candidate branch differs") {
		t.Fatal("remote drift was accepted", o, err)
	}
	assertNoBackend(t, calls)
	data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
	if strings.Count(string(data), "push\n") != 1 {
		t.Fatal("integrity error rewrote remote", string(data))
	}
}

func TestReleasePreparationLegacyPreparedStopRejoinsSameBackend(t *testing.T) {
	for _, mode := range []string{"readiness", "publish"} {
		t.Run(mode, func(t *testing.T) {
			var r *Releases
			var j releaseJob
			if mode == "readiness" {
				r, j, _ = localReadinessFixture(t)
				j.SourcePrepared = true
			} else {
				var ref, bare string
				r, j, bare, ref, _ = localPublicationFixture(t)
				var candidate string
				if err := json.Unmarshal(j.Assignment.Inputs["candidate"].Inline, &candidate); err != nil {
					t.Fatal(err)
				}
				j.Candidate = strings.Split(candidate, "\n")[1]
				previous := strings.TrimSpace(string(fixtureGit(t, bare, "rev-parse", ref)))
				fixtureGit(t, j.Assignment.Repository, "push", "--quiet", "--force-with-lease="+ref+":"+previous, "origin", j.Candidate+":"+ref)
			}
			j.LocalPreparationOwned = false
			requests := filepath.Join(t.TempDir(), "backend-requests")
			response := "{\"key\":\"" + j.Assignment.AttemptID + "\",\"state\":\"unknown\",\"reason\":\"original backend settlement unconfirmed\"}"
			script := "#!/bin/sh\ncat >> " + testShellQuote(requests) + "\nprintf '\\n' >> " + testShellQuote(requests) + "\nprintf '%s\\n' " + testShellQuote(response) + "\n"
			if err := os.WriteFile(j.Command[0], []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			saveLegacyRelease(t, r, j)
			if err := r.Stop(t.Context(), j.Assignment.AttemptID); err != nil {
				t.Fatal(err)
			}
			o, err := r.Observe(t.Context(), j.Assignment.AttemptID)
			if err != nil || o.End != nil || o.Reconciling || o.Reason != "original backend settlement unconfirmed" {
				t.Fatal("legacy backend effect was guessed", o, err)
			}
			raw, err := os.ReadFile(requests)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var request struct{ Key, Action string }
				if json.Unmarshal([]byte(line), &request) != nil || request.Key != j.Assignment.AttemptID || request.Action != mode {
					t.Fatal("legacy reconciliation changed identity/action", line)
				}
			}
		})
	}
}

func TestReleasePreparationLegacyUnpreparedPublishStopCannotInferNoEffect(t *testing.T) {
	r, j, _, _, calls := localPublicationFixture(t)
	j.LocalPreparationOwned = false
	saveLegacyRelease(t, r, j)
	g := localReleaseGate(t, "none", false, false)
	if err := r.Stop(t.Context(), j.Assignment.AttemptID); err != nil {
		t.Fatal(err)
	}
	o, err := r.Observe(t.Context(), j.Assignment.AttemptID)
	if err != nil || o.End != nil || !o.Reconciling || !strings.Contains(o.Reason, "settlement is unconfirmed") {
		t.Fatal("legacy remote effect was guessed", o, err)
	}
	assertNoBackend(t, calls)
	data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
	if strings.Contains(string(data), "push\n") {
		t.Fatal("legacy Stop initiated push", string(data))
	}
}

func TestReleasePreparationDoesNotExtendOriginalDeadline(t *testing.T) {
	for _, mode := range []string{"readiness", "publish"} {
		t.Run(mode, func(t *testing.T) {
			var r *Releases
			var j releaseJob
			var calls string
			if mode == "readiness" {
				r, j, calls = localReadinessFixture(t)
			} else {
				r, j, _, _, calls = localPublicationFixture(t)
			}
			j.Assignment.Deadline = time.Now().Add(-time.Second)
			if err := r.save(j); err != nil {
				t.Fatal(err)
			}
			o, err := r.Observe(t.Context(), j.Assignment.AttemptID)
			if err != nil || o.End != nil || o.State != runner.Unknown {
				t.Fatal("expired preparation received another allowance", o, err)
			}
			saved, err := r.load(j.Assignment.AttemptID)
			if err != nil || saved.PushStarted || saved.SourceArchived || !saved.Assignment.Deadline.Equal(j.Assignment.Deadline) {
				t.Fatal("expired preparation changed checkpoints/deadline", saved, err)
			}
			assertNoBackend(t, calls)
			if err = r.Stop(t.Context(), j.Assignment.AttemptID); err != nil {
				t.Fatal(err)
			}
			o, err = r.Observe(t.Context(), j.Assignment.AttemptID)
			if err != nil || o.End == nil || o.End.Kind != "stopped" {
				t.Fatal("expired no-dispatch operation did not settle", o, err)
			}
		})
	}
}
