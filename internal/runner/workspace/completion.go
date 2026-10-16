package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
	"golang.org/x/sys/unix"
)

var ErrRefMoved = errors.New("expected ref changed")

func (g Git) Ref(ctx context.Context, ref string) (string, error) {
	p, err := profile(ctx, g.Settings.Config.Repository.Path)
	if err != nil {
		return "", err
	}
	return scalar(ctx, p, "rev-parse", "--verify", ref+"^{commit}")
}
func (g Git) TargetAvailable(ctx context.Context, ref string) error {
	p, err := profile(ctx, g.Settings.Config.Repository.Path)
	if err != nil {
		return err
	}
	out, err := scalar(ctx, p, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "branch "+ref {
			return fmt.Errorf("target %s is checked out in a worktree; switch that checkout yourself before local integration", ref)
		}
	}
	return nil
}
func (g Git) Contains(ctx context.Context, tip, commit string) error {
	p, err := profile(ctx, g.Settings.Config.Repository.Path)
	if err != nil {
		return err
	}
	_, err = scalar(ctx, p, "merge-base", "--is-ancestor", commit, tip)
	return err
}

// ApplyRef verifies the source base and compare-and-sets the destination in one
// Git transaction. It never changes a checkout, index or existing result branch.
func (g Git) ApplyRef(ctx context.Context, w core.RefWrite) error {
	if err := g.validate(ctx); err != nil {
		return err
	}
	p, err := profile(ctx, g.Settings.Config.Repository.Path)
	if err != nil {
		return err
	}
	for _, ref := range []string{w.Ref, w.VerifyRef} {
		if ref != "" {
			if _, err = scalar(ctx, p, "check-ref-format", ref); err != nil {
				return err
			}
		}
	}
	if w.Kind == "base" {
		if err = g.TargetAvailable(ctx, w.Ref); err != nil {
			return err
		}
		if err = g.Contains(ctx, w.Commit, w.Old); err != nil {
			return errors.New("local integration is not a fast-forward")
		}
	}
	if w.Old != "" {
		actual, e := g.Ref(ctx, w.Ref)
		if e != nil || actual != w.Old {
			return ErrRefMoved
		}
	}
	if w.VerifyRef != "" {
		actual, e := g.Ref(ctx, w.VerifyRef)
		if e != nil || actual != w.VerifyCommit {
			return ErrRefMoved
		}
	}
	old := w.Old
	if old == "" {
		old = strings.Repeat("0", len(w.Commit))
	}
	input := "start\noption no-deref\n"
	if w.VerifyRef != "" && w.VerifyRef != w.Ref {
		input += "verify " + w.VerifyRef + " " + w.VerifyCommit + "\n"
	}
	input += "update " + w.Ref + " " + w.Commit + " " + old + "\nprepare\ncommit\n"
	r, err := subprocess.RunPrivilegedGitInput(ctx, p, []string{"update-ref", "--stdin"}, strings.NewReader(input), 30*time.Second)
	if err != nil {
		// A normal Git rejection is definitive. Cancellation or a lost process is not.
		if ctx.Err() == nil && r.ExitCode > 0 && strings.Contains(r.Stderr, "cannot lock ref") {
			return ErrRefMoved
		}
		return err
	}
	return nil
}

// Combined retains the frozen Plan source in the parent's ordinary workspace.
// PlanHead is durable before preparation, so restart can recognize only that head.
func (g Git) Combined(ctx context.Context, t core.Task) (core.Candidate, error) {
	var c core.Candidate
	if t.Release == nil || t.PlanHead == "" {
		return c, errors.New("missing frozen Plan head")
	}
	head, err := g.Ref(ctx, t.PlanRef())
	if err != nil || head != t.PlanHead {
		return c, errors.New("private Plan ref differs from recorded inclusion")
	}
	m, err := g.Prepare(ctx, t)
	if err != nil {
		return c, err
	}
	p, err := profile(ctx, m.WorktreePath)
	if err != nil {
		return c, err
	}
	s, err := Snapshot(ctx, m.WorktreePath)
	if err != nil {
		return c, err
	}
	if !s.Clean || s.Head != t.Base() && s.Head != t.PlanHead {
		return c, errors.New("combined workspace changed")
	}
	if s.Head != t.PlanHead {
		if _, err = scalar(ctx, p, "reset", "--hard", t.PlanHead); err != nil {
			return c, err
		}
	}
	s, err = Snapshot(ctx, m.WorktreePath)
	if err != nil {
		return c, err
	}
	d, err := Digest(m.WorktreePath)
	if err != nil {
		return c, err
	}
	c = core.Candidate{Commit: s.Head, Tree: s.Tree, Fingerprint: d, Workspace: m.WorktreePath}
	t.Candidate = &c
	return c, g.ValidateCandidate(ctx, t)
}

var ErrRefsBusy = errors.New("another Runner owns repository ref integration")

func (g Git) LockRefs(ctx context.Context) (*os.File, error) {
	p, err := profile(ctx, g.Settings.Config.Repository.Path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(p.CommonDirectory, "runner-ref.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrRefsBusy
		}
		return nil, err
	}
	return f, nil
}
func (g Git) CheckRefIntent(ctx context.Context, w core.RefWrite) error {
	p, err := profile(ctx, g.Settings.Config.Repository.Path)
	if err != nil {
		return err
	}
	symbolic, err := subprocess.RunPrivilegedGit(ctx, nil, p, []string{"symbolic-ref", "-q", w.Ref}, 30*time.Second)
	if err == nil || symbolic.ExitCode != 1 {
		return errors.New("ref destination must be a direct ref")
	}
	r, err := subprocess.RunPrivilegedGit(ctx, nil, p, []string{"rev-parse", "--verify", "--quiet", w.Ref + "^{commit}"}, 30*time.Second)
	if err != nil && r.ExitCode != 1 {
		return err
	}
	actual := strings.TrimSpace(r.Stdout)
	if w.Old == "" && actual != "" {
		return errors.New("result ref already exists without this intent; preserve it and inspect delivery")
	}
	if actual != w.Old {
		return ErrRefMoved
	}
	if w.VerifyRef != "" {
		actual, err := g.Ref(ctx, w.VerifyRef)
		if err != nil {
			return err
		}
		if actual != w.VerifyCommit {
			return ErrRefMoved
		}
	}
	return nil
}
