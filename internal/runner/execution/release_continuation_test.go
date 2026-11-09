package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func testShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

type sourceReleaseFixture struct {
	r                *Releases
	a                runner.Assignment
	base, head, tree string
	patch            []byte
	receipt          release.Receipt
	requestPath      string
}

func newSourceReleaseFixture(t *testing.T, baseline bool) *sourceReleaseFixture {
	t.Helper()
	disk, repo := workspaceDisk(t), sourceRepository(t)
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(repo, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "seats.go")
	fixtureGit(t, repo, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "accepted publication")
	head := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	tree := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD^{tree}")))
	patch := fixtureGit(t, repo, "diff", "--binary", "--full-index", base, head, "--")
	a := sourceAssignment(disk, repo, "adopt_source")
	a.ProjectID, a.Source.Ref = "demo", base
	a.Setup.Profile = runner.Profile{Harness: "release", Mode: "source-adopt", Access: "input-only"}
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: "retained/changes.patch"}, "receipt": {Inline: json.RawMessage(`{}`)}}
	w := &Workspaces{disk: disk, read: func(_ context.Context, id string) ([]byte, error) {
		if id != "retained/changes.patch" {
			return nil, errors.New("artifact is not registered yet")
		}
		return append([]byte(nil), patch...), nil
	}}
	if baseline {
		initial := runner.Clone(a)
		initial.AttemptID, initial.Inputs = "pristine_baseline", map[string]runner.Value{}
		initial.Setup.Profile = runner.Profile{Harness: "command", Access: "workspace-read"}
		if _, err := w.Prepare(t.Context(), initial); err != nil {
			t.Fatal(err)
		}
	}
	receipt := release.Receipt{Key: a.AttemptID, RunID: a.RunID, Action: "adopt", Policy: "policy", Head: head, Base: base, Source: base, PR: 334, PublicationRunID: "original", PublicationKey: "published"}
	f := &sourceReleaseFixture{r: &Releases{disk: disk, workspaces: w}, a: a, base: base, head: head, tree: tree, patch: patch, receipt: receipt, requestPath: filepath.Join(t.TempDir(), "request.json")}
	response, _ := json.Marshal(release.Response{Key: a.AttemptID, State: "finished", Receipt: mustReleaseJSON(t, receipt)})
	binary := filepath.Join(t.TempDir(), "operator")
	script := "#!/bin/sh\ncat > " + testShellQuote(f.requestPath) + "\nprintf '%s\\n' " + testShellQuote(string(response)) + "\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.r.cfg = &runner.ReleaseSettings{Command: []string{binary}}
	t.Cleanup(func() { _ = w.ForgetRun(context.Background(), a.RunID) })
	return f
}

func mustReleaseJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func adoptSourceFixture(t *testing.T, f *sourceReleaseFixture) runner.Observation {
	t.Helper()
	o, err := f.r.Start(t.Context(), f.a)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "returned" || o.End.Result.Outcome != "ready" || o.Source == nil {
		workspace, _ := f.r.workspaces.load(f.a.RunID)
		t.Logf("candidate=%+v assignment repository=%q base=%q", workspace.Candidate, f.a.Repository, f.a.Source.Ref)
		t.Fatalf("authenticated source adoption failed: observation=%+v end=%+v error=%v", o, o.End, err)
	}
	return o
}

func TestSourceAdoptAdapterPreservesPristineBaselineAndRetainsExactSource(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		t.Run(fmt.Sprint("baseline-", baseline), func(t *testing.T) {
			f := newSourceReleaseFixture(t, baseline)
			o := adoptSourceFixture(t, f)
			if o.Source.Tree != f.tree || o.Source.Base != f.base || o.End.Result.Outputs["patch"].ArtifactID != f.a.AttemptID+"/changes.patch" {
				t.Fatal("adopted handoff differs from accepted source", o.Source)
			}
			data, _ := os.ReadFile(f.requestPath)
			var q release.Request
			if json.Unmarshal(data, &q) != nil || q.Action != "adopt" || !q.SourceContinuation || q.Source != f.base {
				t.Fatal("source role did not use the authenticated operator boundary", string(data))
			}
			binding, err := f.r.loadPublication(f.a)
			if err != nil || binding.Latest != f.receipt || binding.Source.Tree != f.tree || binding.PatchSHA256 != release.Digest(f.patch) {
				t.Fatal("publication source binding missing", binding, err)
			}
			if err = f.r.Release(t.Context(), f.a.AttemptID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.r.loadPublication(f.a); err != nil {
				t.Fatal("attempt cleanup removed continuation ownership", err)
			}
			if err = f.r.ForgetRun(t.Context(), f.a.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.r.loadPublication(f.a); !os.IsNotExist(err) {
				t.Fatal("explicit retention did not release its own binding", err)
			}
		})
	}
}

func TestSourceAdoptAdapterRejectsMismatchedPatchBeforeHandoff(t *testing.T) {
	f := newSourceReleaseFixture(t, true)
	f.r.workspaces.read = func(context.Context, string) ([]byte, error) { return nil, nil }
	o, err := f.r.Start(t.Context(), f.a)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "failed" || o.Source != nil {
		t.Fatal("different source was emitted as the accepted publication", o, err)
	}
	if _, err = f.r.loadPublication(f.a); !os.IsNotExist(err) {
		t.Fatal("invalid source granted publication authority", err)
	}
	w, err := f.r.workspaces.load(f.a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(w.WorkDir, "seats.go")); !os.IsNotExist(err) {
		t.Fatal("invalid adoption changed the pristine baseline", err)
	}
}

func TestPublicationContinuationCannotSelectRefWithoutItsLocalBinding(t *testing.T) {
	r := &Releases{disk: workspaceDisk(t)}
	a := runner.Assignment{RunID: "new-run", AttemptID: "publish", Inputs: map[string]runner.Value{}}
	for _, p := range []release.Receipt{
		{RunID: a.RunID, Action: "adopt", Head: strings.Repeat("a", 40), PublicationRunID: "original", PublicationKey: "published"},
		{RunID: a.RunID, Action: "publish", Head: strings.Repeat("a", 40), PublicationRunID: "original", PublicationKey: "published"},
		{RunID: a.RunID, Action: "publish"},
	} {
		a.Inputs["previous"] = runner.Value{Inline: mustReleaseJSON(t, p)}
		if _, _, _, err := r.publicationAuthorization(&releaseJob{Assignment: a}); err == nil {
			t.Fatal("untrusted input selected continuation authority or invalid legacy predecessor", p)
		}
	}
	a.Inputs["previous"] = runner.Value{Inline: json.RawMessage(`{}`)}
	if ref, head, continued, err := r.publicationAuthorization(&releaseJob{Assignment: a}); err != nil || ref != candidateRef(a.RunID) || head != "" || continued {
		t.Fatal("fresh owned publication changed", ref, head, continued, err)
	}
}

func reviewedContinuationCommit(t *testing.T, repo, base, run, text string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "seats.go"), []byte(seatsImplementation+"\n// "+text+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "seats.go")
	tree := strings.TrimSpace(string(fixtureGit(t, repo, "write-tree")))
	head := strings.TrimSpace(string(fixtureGit(t, repo, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-p", base, "-m", text)))
	fixtureGit(t, repo, "update-ref", candidateRef(run), head)
	return head
}

func TestContinuedPublicationCASRejoinsLostReplyAndUsesOnlyOriginalOwnedRef(t *testing.T) {
	f := newSourceReleaseFixture(t, false)
	adoptSourceFixture(t, f)
	bare := t.TempDir()
	fixtureGit(t, bare, "init", "--bare", "--quiet", "--template=")
	fixtureGit(t, f.a.Repository, "remote", "add", "origin", bare)
	remoteRef := candidateRef("original")
	fixtureGit(t, f.a.Repository, "push", "--quiet", "origin", f.head+":"+remoteRef)
	head := reviewedContinuationCommit(t, f.a.Repository, f.base, f.a.RunID, "first reviewed repair")
	a := runner.Clone(f.a)
	a.AttemptID, a.Setup.Profile.Mode = "publish_continuation", "publish"
	a.Inputs = map[string]runner.Value{"candidate": runner.Text(candidateRef(a.RunID) + "\n" + head + "\nreviewed"), "previous": {Inline: mustReleaseJSON(t, f.receipt)}}
	j := releaseJob{Assignment: a, GitHub: "/usr/bin/true"}
	if err := f.r.save(j); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "pushes")
	marker := filepath.Join(bin, "lost-once")
	script := "#!/bin/sh\nis_push=0\nfor arg do\n if [ \"$arg\" = push ]; then is_push=1; fi\ndone\nif [ \"$is_push\" = 1 ]; then\n printf 'push\\n' >> " + testShellQuote(calls) + "\n " + testShellQuote(realGit) + " \"$@\"\n status=$?\n if [ \"$status\" = 0 ] && [ ! -e " + testShellQuote(marker) + " ]; then\n  : > " + testShellQuote(marker) + "\n  exit 255\n fi\n exit \"$status\"\nfi\nexec " + testShellQuote(realGit) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err = f.r.publishCandidate(t.Context(), &j); err == nil || j.Candidate != "" {
		t.Fatal("fixture did not lose the CAS acknowledgement", err)
	}
	actual := strings.TrimSpace(string(fixtureGit(t, bare, "rev-parse", remoteRef)))
	if actual != head {
		t.Fatal("CAS did not affect the original owned ref", actual)
	}
	if got, err := f.r.publishCandidate(t.Context(), &j); err != nil || got != head {
		t.Fatal("lost CAS reply did not rejoin", got, err)
	}
	data, _ := os.ReadFile(calls)
	if string(data) != "push\n" {
		t.Fatal("uncertain CAS caused a replacement push", string(data))
	}
	next := f.receipt
	next.Key, next.Action, next.Head = a.AttemptID, "publish", head
	if err = f.r.recordPublication(j, mustReleaseJSON(t, next)); err != nil {
		t.Fatal(err)
	}
	j, err = f.r.load(a.AttemptID) // Sidecar advanced before End was saved.
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.r.publishCandidate(t.Context(), &j); err != nil || got != head {
		t.Fatal("accepted sidecar update broke same-operation replay", got, err)
	}
	if err = f.r.recordPublication(j, mustReleaseJSON(t, next)); err != nil {
		t.Fatal("accepted publication replay changed identity", err)
	}
	bad := j
	bad.Assignment = runner.Clone(j.Assignment)
	forged := next
	forged.Head = strings.Repeat("d", 40)
	bad.Assignment.Inputs["previous"] = runner.Value{Inline: mustReleaseJSON(t, forged)}
	if _, err = f.r.publishCandidate(t.Context(), &bad); err == nil {
		t.Fatal("task-selected predecessor authorized CAS")
	}
	data, _ = os.ReadFile(calls)
	if string(data) != "push\n" {
		t.Fatal("replay or forgery produced another push", string(data))
	}
	if _, err = git(t.Context(), bare, nil, nil, 256, "show-ref", "--verify", candidateRef(a.RunID)); err == nil {
		t.Fatal("continuation created a duplicate remote branch")
	}
}
