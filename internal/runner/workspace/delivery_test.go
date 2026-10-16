package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type localTransport struct {
	remote string
	calls  int
	lease  string
	t      *testing.T
}

func (r *localTransport) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	r.calls++
	replaced := false
	args = append([]string{"-c", "protocol.file.allow=always"}, args...)
	for i, a := range args {
		if a == "https://github.com/owner/repo.git" {
			args[i] = r.remote
			replaced = true
		}
	}
	joined := strings.Join(args, " ")
	if !replaced {
		r.t.Fatal("transport did not use the exact destination")
	}
	if strings.Contains(joined, " fetch ") {
		if !strings.Contains(joined, "+refs/heads/main:refs/runner/fetch/") || !strings.Contains(joined, "--no-write-fetch-head") {
			r.t.Fatal("fetch destination escaped private ref")
		}
	} else if !strings.Contains(joined, "--force-with-lease=refs/heads/runner/"+r.lease+" ") {
		r.t.Fatal("push did not use the explicit expected-commit lease")
	}
	return (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
}

func TestDraftPushUsesExactObjectsAndOnlyReplacesExpectedRemoteCommit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, remote := filepath.Join(root, "repo"), filepath.Join(root, "remote.git")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s %v", args, b, e)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	if err = os.WriteFile(filepath.Join(repo, "file.txt"), []byte("accepted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "candidate")
	git("init", "--bare", remote)
	id := core.NewID()
	p := core.PublicationIntent{ID: id, Repository: "owner/repo", Branch: "runner/" + id, Candidate: core.Candidate{Workspace: repo, Commit: git("rev-parse", "HEAD"), Tree: git("rev-parse", "HEAD^{tree}")}}
	r := &localTransport{remote: remote, t: t, lease: id + ":"}
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", "", func() error { return errors.New("revoked") }, r); err == nil || r.calls != 0 {
		t.Fatal("revoked push dispatched")
	}
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", "", func() error { return nil }, r); err != nil {
		t.Fatal(err)
	}
	if got := git("--git-dir="+remote, "rev-parse", "refs/heads/"+p.Branch); got != p.Candidate.Commit {
		t.Fatal("wrong published commit")
	}
	git("push", remote, "HEAD:refs/heads/main")
	base, err := fetchBase(t.Context(), repo, "owner/repo", "main", r)
	if err != nil || base != p.Candidate.Commit || git("rev-parse", "HEAD") != p.Candidate.Commit {
		t.Fatal("fetch changed checkout or returned wrong base", base, err)
	}
	if _, err = os.Stat(filepath.Join(repo, ".git", "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatal("fetch wrote shared FETCH_HEAD", err)
	}
	// A refresh may replace only the exact last confirmed commit.
	previous := p.Candidate.Commit
	git("commit", "--allow-empty", "-m", "refreshed candidate")
	p.Candidate.Commit = git("rev-parse", "HEAD")
	r.lease = id + ":" + previous
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", previous, func() error { return nil }, r); err != nil {
		t.Fatal("exact leased update failed", err)
	}
	if got := git("--git-dir="+remote, "rev-parse", "refs/heads/"+p.Branch); got != p.Candidate.Commit {
		t.Fatal("refreshed commit not published")
	}
	previous = p.Candidate.Commit
	// Another actor changes the remote branch; the exact absent lease refuses
	// even a fast-forward update that an ordinary non-force push would allow.
	git("commit", "--allow-empty", "-m", "external")
	external := git("rev-parse", "HEAD")
	git("push", remote, external+":refs/heads/"+p.Branch)
	git("commit", "--allow-empty", "-m", "later candidate")
	p.Candidate.Commit = git("rev-parse", "HEAD")
	r.lease = id + ":"
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", "", func() error { return nil }, r); err == nil {
		t.Fatal("existing branch overwritten")
	}
	if got := git("--git-dir="+remote, "rev-parse", "refs/heads/"+p.Branch); got != external {
		t.Fatal("remote changed")
	}
	r.lease = id + ":" + previous
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", previous, func() error { return nil }, r); err == nil {
		t.Fatal("external commit overwritten by synchronization")
	}
	if got := git("--git-dir="+remote, "rev-parse", "refs/heads/"+p.Branch); got != external {
		t.Fatal("synchronization changed the external commit")
	}
	git("--git-dir="+remote, "update-ref", "-d", "refs/heads/"+p.Branch)
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", previous, func() error { return nil }, r); err == nil {
		t.Fatal("deleted branch recreated by synchronization")
	}
	if got := git("--git-dir="+remote, "for-each-ref", "--format=%(objectname)", "refs/heads/"+p.Branch); got != "" {
		t.Fatal("deleted branch was recreated")
	}
	before := r.calls
	if err = os.WriteFile(filepath.Join(repo, ".git", "objects", "info", "alternates"), []byte(remote+"/objects\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", "", func() error { return nil }, r); err == nil || r.calls != before {
		t.Fatal("object redirection reached transport")
	}
}
