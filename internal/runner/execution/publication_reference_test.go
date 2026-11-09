//go:build darwin || linux

package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func publicationReferenceFixture(t *testing.T, baseline bool) *sourceReleaseFixture {
	t.Helper()
	f := newSourceReleaseFixture(t, baseline)
	f.a.Setup.Profile.Mode = "publication-reference"
	previous := f.receipt
	previous.Key, previous.RunID, previous.Action = "published", "original", "publish"
	previous.PR, previous.Base, previous.PublicationRunID, previous.PublicationKey = 0, "", "", ""
	f.a.Inputs = map[string]runner.Value{"receipt": {Inline: mustReleaseJSON(t, previous)}}
	f.a.OutputTypes = map[string]string{"receipt": "json"}
	f.r.workspaces = nil // Authority-only adoption requires no workspace provider.
	return f
}

func referencePublicationFixture(t *testing.T, f *sourceReleaseFixture) runner.Observation {
	t.Helper()
	o, err := f.r.Start(t.Context(), f.a)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "returned" || o.End.Result.Outcome != "ready" || o.Source != nil || len(o.End.Result.Outputs) != 1 || len(o.End.Result.Outputs["receipt"].Inline) == 0 {
		t.Fatal("publication reference did not return receipt-only authority", o, err)
	}
	return o
}

func TestPublicationReferenceAuthenticatesWithoutSourceAndPreservesBaseline(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-source", true: "pristine-baseline"}[baseline], func(t *testing.T) {
			f := publicationReferenceFixture(t, baseline)
			before, beforeErr := f.r.disk.Read(workspaceName(f.a.RunID), f.a.Limits.MaxRecord)
			refs := fixtureGit(t, f.a.Repository, "for-each-ref", "refs/runner")
			first := referencePublicationFixture(t, f)
			var q release.Request
			request, err := os.ReadFile(f.requestPath)
			if err != nil || json.Unmarshal(request, &q) != nil || q.Project != f.a.ProjectID || q.Key != f.a.AttemptID || q.RunID != f.a.RunID || q.Action != "adopt" || !q.SourceContinuation || q.Source != f.base || !bytes.Equal(q.Previous, f.a.Inputs["receipt"].Inline) {
				t.Fatal("reference did not use the owning operator authentication boundary", string(request), err)
			}
			binding, err := f.r.loadPublication(f.a)
			if err != nil || !binding.AuthorityOnly || binding.ProjectID != f.a.ProjectID || binding.Latest != f.receipt || binding.Source != (runner.SourceResult{}) || binding.PatchSHA256 != "" {
				t.Fatal("reference gained source authority or lost publication ownership", binding, err)
			}
			after, afterErr := f.r.disk.Read(workspaceName(f.a.RunID), f.a.Limits.MaxRecord)
			if !bytes.Equal(before, after) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) || !bytes.Equal(refs, fixtureGit(t, f.a.Repository, "for-each-ref", "refs/runner")) {
				t.Fatal("publication reference changed source metadata or refs", beforeErr, afterErr)
			}
			if _, err = os.Lstat(filepath.Join(f.r.disk.Root, "artifacts", f.a.AttemptID)); !os.IsNotExist(err) {
				t.Fatal("publication reference created source artifacts", err)
			}
			if !baseline {
				if _, err = os.Lstat(filepath.Join(f.r.disk.Root, "work", f.a.RunID, "source")); !os.IsNotExist(err) {
					t.Fatal("publication reference materialized a checkout", err)
				}
			}
			// The saved result and binding survive restart without another operator
			// call, even if that command is now unavailable.
			job, err := f.r.load(f.a.AttemptID)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(job.Command[0]); err != nil {
				t.Fatal(err)
			}
			resumed := &Releases{disk: f.r.disk}
			again, err := resumed.Start(t.Context(), f.a)
			if err != nil || again.Source != nil || !bytes.Equal(again.End.Result.Outputs["receipt"].Inline, first.End.Result.Outputs["receipt"].Inline) {
				t.Fatal("reference restart changed or repeated authenticated adoption", again, err)
			}
			if err = resumed.Release(t.Context(), f.a.AttemptID); err != nil {
				t.Fatal(err)
			}
			if _, err = resumed.loadPublication(f.a); err != nil {
				t.Fatal("attempt cleanup removed publication authority", err)
			}
		})
	}
}

func TestPublicationReferenceRejectsInvalidContractsAndReceiptsWithoutSource(t *testing.T) {
	for _, fault := range []string{
		"input artifact",
		"extra input",
		"source output",
		"write access",
		"foreign base",
		"not publication",
		"malformed receipt",
		"foreign run",
		"different head",
		"different origin",
		"failed operator",
	} {
		t.Run(fault, func(t *testing.T) {
			f := publicationReferenceFixture(t, false)
			response := release.Response{Key: f.a.AttemptID, State: "finished", Receipt: mustReleaseJSON(t, f.receipt)}
			bad := f.receipt
			contract := true
			switch fault {
			case "input artifact":
				f.a.Inputs["receipt"] = runner.Value{ArtifactID: "foreign/receipt"}
			case "extra input":
				f.a.Inputs["patch"] = runner.Value{ArtifactID: "retained/changes.patch"}
			case "source output":
				f.a.OutputTypes["patch"] = "artifact"
			case "write access":
				f.a.Setup.Profile.Access = "workspace-write"
			case "foreign base":
				f.a.Source.Ref = strings.Repeat("e", 40)
			case "not publication":
				previous := f.receipt
				previous.Action = "qualify"
				f.a.Inputs["receipt"] = runner.Value{Inline: mustReleaseJSON(t, previous)}
			case "malformed receipt":
				contract, response.Receipt = false, json.RawMessage(`"not a receipt"`)
			case "foreign run":
				contract, bad.RunID = false, "foreign"
			case "different head":
				contract, bad.Head = false, strings.Repeat("e", 40)
			case "different origin":
				contract, bad.PublicationRunID = false, "foreign"
			case "failed operator":
				contract, response.Failed, response.Reason = false, true, "live PR ownership changed"
			}
			if fault != "malformed receipt" {
				response.Receipt = mustReleaseJSON(t, bad)
			}
			binary := f.r.cfg.Command[0]
			script := "#!/bin/sh\ncat > " + testShellQuote(f.requestPath) + "\nprintf '%s\\n' " + testShellQuote(string(mustReleaseJSON(t, response))) + "\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			o, err := f.r.Start(t.Context(), f.a)
			if contract {
				if err == nil {
					t.Fatal("invalid reference contract reached the operator", o)
				}
				if _, err = os.Lstat(f.requestPath); !os.IsNotExist(err) {
					t.Fatal("invalid reference dispatched operator work", err)
				}
			} else if err != nil || o.End == nil || o.End.Kind != "failed" || o.Source != nil {
				t.Fatal("invalid operator receipt became authority", o, err)
			}
			if _, err = f.r.loadPublication(f.a); !os.IsNotExist(err) {
				t.Fatal("failed authentication persisted publication authority", err)
			}
			if _, err = os.Lstat(filepath.Join(f.r.disk.Root, "work", f.a.RunID, "source")); !os.IsNotExist(err) {
				t.Fatal("failed authentication materialized source", err)
			}
		})
	}
}

func TestPublicationReferenceBindingCannotAcquireSourceOrChangeProject(t *testing.T) {
	f := publicationReferenceFixture(t, false)
	referencePublicationFixture(t, f)
	binding, err := f.r.loadPublication(f.a)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"source", "patch", "discriminator", "project"} {
		bad := binding
		switch fault {
		case "source":
			bad.Source.AttemptID = f.a.AttemptID
		case "patch":
			bad.PatchSHA256 = strings.Repeat("a", 64)
		case "discriminator":
			bad.AuthorityOnly = false
		case "project":
			bad.ProjectID = "foreign"
		}
		if err = f.r.savePublication(bad); err != nil {
			t.Fatal(err)
		}
		if _, err = f.r.loadPublication(f.a); err == nil {
			t.Fatal("invalid authority-only binding passed", fault)
		}
	}
	if err = f.r.savePublication(binding); err != nil {
		t.Fatal(err)
	}
	foreign := runner.Clone(f.a)
	foreign.ProjectID = "foreign"
	foreign.Inputs = map[string]runner.Value{"previous": {Inline: mustReleaseJSON(t, f.receipt)}}
	if _, _, _, err = f.r.publicationAuthorization(&releaseJob{Assignment: foreign}); err == nil {
		t.Fatal("foreign project acquired pre-push publication authority")
	}
	// Source adoption retains its stronger patch/tree contract; the new binding
	// cannot be silently upgraded to materialized source authority.
	a := runner.Clone(f.a)
	a.Setup.Profile.Mode = "source-adopt"
	a.Inputs["patch"] = runner.Value{ArtifactID: "retained/changes.patch"}
	f.r.workspaces = &Workspaces{disk: f.r.disk, read: func(context.Context, string) ([]byte, error) {
		t.Fatal("reference was used to prepare a source seed")
		return nil, nil
	}}
	if _, err = f.r.adoptedSource(t.Context(), releaseJob{Assignment: a}, mustReleaseJSON(t, f.receipt)); err == nil || !strings.Contains(err.Error(), "authority-only") {
		t.Fatal("source adoption replaced reference-only authority", err)
	}
}

func TestPublicationReferenceContinuesOnlyOriginalPRThroughLostReplyAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart-lost-reply", true: "stopped-known-effect"}[stop], func(t *testing.T) {
			f := publicationReferenceFixture(t, false)
			referencePublicationFixture(t, f)
			bare := t.TempDir()
			fixtureGit(t, bare, "init", "--bare", "--quiet", "--template=")
			fixtureGit(t, f.a.Repository, "remote", "add", "origin", bare)
			ref := candidateRef("original")
			fixtureGit(t, f.a.Repository, "push", "--quiet", "origin", f.head+":"+ref)
			head := reviewedContinuationCommit(t, f.a.Repository, f.base, f.a.RunID, "independently reviewed candidate")
			a := runner.Clone(f.a)
			a.AttemptID, a.Setup.Profile.Mode, a.Deadline = "reference_publish", "publish", time.Now().Add(time.Minute)
			a.Inputs = map[string]runner.Value{
				"candidate": runner.Text(candidateRef(a.RunID) + "\n" + head + "\nreviewed"),
				"previous":  {Inline: mustReleaseJSON(t, f.receipt)},
			}
			next := f.receipt
			next.Key, next.Action, next.Head = a.AttemptID, "publish", head
			calls := filepath.Join(t.TempDir(), "operator-calls")
			script := "#!/bin/sh\ncat >/dev/null\nprintf 'operator\\n' >> " + testShellQuote(calls) + "\nprintf '%s\\n' " + testShellQuote(string(mustReleaseJSON(t, release.Response{Key: a.AttemptID, State: "finished", Receipt: mustReleaseJSON(t, next)}))) + "\n"
			if err := os.WriteFile(f.r.cfg.Command[0], []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			j := releaseJob{Assignment: a, Command: f.r.cfg.Command, GitHub: "/usr/bin/true", LocalPreparationOwned: true}
			if err := f.r.save(j); err != nil {
				t.Fatal(err)
			}
			g := localReleaseGate(t, "push", true, true)
			observeHeldPreparation(t, f.r, j, g)
			assertNoBackend(t, calls)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if stop {
				if err := f.r.Stop(ctx, a.AttemptID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := g.hold.Write([]byte("continue\n")); err != nil {
					t.Fatal(err)
				}
				if o := settledPreparation(t, f.r, a.AttemptID); o.End != nil || !o.Reconciling {
					t.Fatal("lost CAS reply was accepted without ref reconciliation", o)
				}
			}
			if err := f.r.Close(ctx); err != nil {
				t.Fatal(err)
			}
			resumed := &Releases{disk: f.r.disk}
			defer resumed.Close(context.Background())
			o, err := resumed.Observe(t.Context(), a.AttemptID)
			if err != nil || o.End == nil || o.Source != nil {
				t.Fatal("saved publication did not settle after restart", o, err)
			}
			binding, err := resumed.loadPublication(a)
			if err != nil || !binding.AuthorityOnly || binding.Source != (runner.SourceResult{}) || binding.PatchSHA256 != "" {
				t.Fatal("continued publication changed authority-only binding", binding, err)
			}
			if stop {
				if o.End.Kind != "stopped" || binding.Latest != f.receipt {
					t.Fatal("stopped preparation invented accepted publication", o.End, binding)
				}
				assertNoBackend(t, calls)
			} else {
				if o.End.Kind != "returned" || binding.Latest != next || binding.LastAttempt != a.AttemptID {
					t.Fatal("accepted continuation did not advance the same PR", o.End, binding)
				}
				saved, err := resumed.load(a.AttemptID)
				if err != nil || resumed.recordPublication(saved, mustReleaseJSON(t, next)) != nil {
					t.Fatal("accepted continuation replay lost its saved predecessor", err)
				}
			}
			pushes, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
			if strings.Count(string(pushes), "push\n") != 1 || strings.TrimSpace(string(fixtureGit(t, bare, "rev-parse", ref))) != head {
				t.Fatal("continuation repeated or redirected CAS", string(pushes))
			}
			if _, err := git(t.Context(), bare, nil, nil, 256, "show-ref", "--verify", candidateRef(a.RunID)); err == nil {
				t.Fatal("continuation created a duplicate remote branch")
			}
		})
	}
}
