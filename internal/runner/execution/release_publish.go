package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

// A read-only archive can hydrate immutable source objects across observation
// calls. Only positive call-deadline evidence permits this same-job rejoin.
type sourceArchivePending struct{ cause error }

func (e *sourceArchivePending) Error() string {
	return "read-only source archive reached its observation deadline; rejoin the same readiness attempt"
}
func (e *sourceArchivePending) Unwrap() error { return e.cause }

func releaseArchiveError(ctx context.Context, err error) error {
	var exit *exec.ExitError
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && (errors.Is(err, context.DeadlineExceeded) || errors.As(err, &exit) && exit.ExitCode() == -1) {
		return &sourceArchivePending{cause: err}
	}
	return fmt.Errorf("source archive did not complete: %w", err)
}

// Publication consumes the model-free retained candidate, never a model's
// branch/SHA claim. The owned branch uses compare-and-swap and cannot overwrite
// unrelated work. Reconnecting inspects that same remote ref first.
func (r *Releases) publishCandidate(ctx context.Context, j *releaseJob) (string, error) {
	remoteRef, previousHead, _, err := r.publicationAuthorization(j)
	if err != nil {
		return "", err
	}
	if j.Candidate != "" {
		return j.Candidate, nil
	}
	a := j.Assignment
	var text string
	if j.GitHub == "" || json.Unmarshal(a.Inputs["candidate"].Inline, &text) != nil {
		return "", errors.New("publication requires a retained candidate and configured GitHub runtime")
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	ref := candidateRef(a.RunID)
	if len(lines) != 3 || lines[0] != ref || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(lines[1]) {
		return "", errors.New("publication candidate identity differs")
	}
	head := lines[1]
	environment := []string{"HOME=" + os.Getenv("HOME"), "GH_PROMPT_DISABLED=1"}
	actual, err := git(ctx, a.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(actual)) != head {
		return "", errors.New("owned candidate branch changed after review")
	}
	parent, err := git(ctx, a.Repository, nil, nil, 256, "rev-parse", head+"^")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(parent)) != r.releaseSource(*j) {
		return "", errors.New("candidate baseline differs from the submitted source")
	}
	helper := "!" + "'" + strings.ReplaceAll(j.GitHub, "'", "'\\''") + "' auth git-credential"
	args := []string{"-c", "credential.helper=" + helper}
	remote, err := git(ctx, a.Repository, environment, nil, 1024, append(args, "ls-remote", "--refs", "origin", remoteRef)...)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(remote))
	if len(fields) > 0 {
		if len(fields) != 2 || fields[1] != remoteRef || (fields[0] != head && fields[0] != previousHead) {
			return "", errors.New("remote candidate branch differs; preserve it")
		}
		if fields[0] == head {
			j.Candidate = head
			return head, r.save(*j)
		}
	} else if previousHead != "" {
		return "", errors.New("previous publication branch disappeared; preserve history")
	}
	// Repeating this CAS after an uncertain reply can only create this same ref.
	// A concurrent/earlier successful creation is inspected on the next observe.
	if _, err = git(ctx, a.Repository, environment, nil, 4096, append(args, "push", "--porcelain", "--force-with-lease="+remoteRef+":"+previousHead, "origin", head+":"+remoteRef)...); err != nil {
		return "", err
	}
	j.Candidate = head
	return head, r.save(*j)
}

func (r *Releases) prepareReleaseSource(ctx context.Context, j *releaseJob) error {
	if j.SourcePrepared {
		return nil
	}
	a := j.Assignment
	if j.GitHub == "" || a.Source == nil || r.workspaces == nil || (a.Source.Ref != "refs/remotes/origin/main" && !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(a.Source.Ref)) {
		return errors.New("delivery readiness requires an exact commit or the configured latest-main ref and GitHub runtime")
	}
	helper := "!'" + strings.ReplaceAll(j.GitHub, "'", "'\\''") + "' auth git-credential"
	environment := []string{"HOME=" + os.Getenv("HOME"), "GH_PROMPT_DISABLED=1"}
	if j.SelectedBase == "" {
		if w, err := r.workspaces.load(a.RunID); err == nil {
			j.SelectedBase = w.Base
		} else {
			if a.Source.Ref == "refs/remotes/origin/main" {
				if _, err = git(ctx, a.Repository, environment, nil, 4096, "-c", "credential.helper="+helper, "fetch", "--no-tags", "origin", "refs/heads/main:refs/remotes/origin/main"); err != nil {
					return err
				}
			}
			base, err := git(ctx, a.Repository, nil, nil, 256, "rev-parse", "--verify", "--end-of-options", a.Source.Ref+"^{commit}")
			if err != nil {
				return err
			}
			j.SelectedBase = strings.TrimSpace(string(base))
		}
		if err := r.save(*j); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper="+helper, "archive", "--format=tar", j.SelectedBase)
	cmd.Dir = a.Repository
	cmd.Env = append(gitEnvironment(), "HOME="+os.Getenv("HOME"), "GH_PROMPT_DISABLED=1")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return releaseArchiveError(ctx, err)
	}
	a.Source = &runner.SourceSettings{Ref: j.SelectedBase}
	if _, err := r.workspaces.Prepare(ctx, a); err != nil {
		return err
	}
	j.SourcePrepared = true
	return r.save(*j)
}

func (r *Releases) releaseSource(j releaseJob) string {
	if j.SelectedBase != "" {
		return j.SelectedBase
	}
	if r.workspaces != nil {
		if w, err := r.workspaces.load(j.Assignment.RunID); err == nil {
			return w.Base
		}
	}
	return j.Assignment.Source.Ref
}
