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
	if !replaced || !strings.Contains(strings.Join(args, " "), "--force-with-lease=refs/heads/runner-next/") {
		r.t.Fatal("push did not use exact URL and absent-ref lease")
	}
	return (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
}

func TestDraftPushUsesExactObjectsAndNeverReplacesRemoteBranch(t *testing.T) {
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
	p := core.PublicationIntent{ID: id, Repository: "owner/repo", Branch: "runner-next/" + id, Candidate: core.Candidate{Workspace: repo, Commit: git("rev-parse", "HEAD"), Tree: git("rev-parse", "HEAD^{tree}")}}
	r := &localTransport{remote: remote, t: t}
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", func() error { return errors.New("revoked") }, r); err == nil || r.calls != 0 {
		t.Fatal("revoked push dispatched")
	}
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", func() error { return nil }, r); err != nil {
		t.Fatal(err)
	}
	if got := git("--git-dir="+remote, "rev-parse", "refs/heads/"+p.Branch); got != p.Candidate.Commit {
		t.Fatal("wrong published commit")
	}
	// Another actor changes the remote branch; the exact absent lease refuses
	// even a fast-forward update that an ordinary non-force push would allow.
	git("commit", "--allow-empty", "-m", "external")
	external := git("rev-parse", "HEAD")
	git("push", remote, external+":refs/heads/"+p.Branch)
	git("commit", "--allow-empty", "-m", "later candidate")
	p.Candidate.Commit = git("rev-parse", "HEAD")
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", func() error { return nil }, r); err == nil {
		t.Fatal("existing branch overwritten")
	}
	if got := git("--git-dir="+remote, "rev-parse", "refs/heads/"+p.Branch); got != external {
		t.Fatal("remote changed")
	}
	before := r.calls
	if err = os.WriteFile(filepath.Join(repo, ".git", "objects", "info", "alternates"), []byte(remote+"/objects\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = pushDraft(t.Context(), p.Candidate, p.Branch, "https://github.com/owner/repo.git", func() error { return nil }, r); err == nil || r.calls != before {
		t.Fatal("object redirection reached transport")
	}
}
