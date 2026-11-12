package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Service) ValidateFrontendCheckRecovery(ctx context.Context, run runner.Run, source runner.SourceResult, check runner.Profile, helperSHA, publicationHead string) error {
	if check.Command == nil || len(check.Command.Args) == 0 {
		return errors.New("frontend check recovery has no configured helper")
	}
	if err := verifyFrontendCheckHelper(check.Command.Args[0], helperSHA); err != nil {
		return err
	}
	if err := s.ValidateSourceRecovery(ctx, run, source); err != nil {
		return err
	}
	if run.Process.ID != "deliver-retained-catalogue" {
		if err := verifyFrontendCheckCardPins(ctx, run); err != nil {
			return err
		}
	}
	return s.releases.validateFrontendCheckPublication(ctx, run, publicationHead)
}

func verifyFrontendCheckCardPins(ctx context.Context, run runner.Run) error {
	group := run.TaskGroups["cards"]
	if group == nil || len(group.Children) == 0 {
		return errors.New("frontend check recovery has no accepted card pins")
	}
	args := []string{"show-ref", "--verify"}
	var expected strings.Builder
	for _, child := range group.Children {
		if child.Candidate == nil || !publicationID.MatchString(child.RunID) || !publicationCommit.MatchString(child.Candidate.Commit) {
			return errors.New("frontend check recovery card pin identity differs")
		}
		ref := taskPinRef(child.RunID)
		args = append(args, ref)
		fmt.Fprintf(&expected, "%s %s\n", child.Candidate.Commit, ref)
	}
	actual, err := git(ctx, run.Repository, nil, nil, 8192, args...)
	if err != nil || string(actual) != expected.String() {
		return errors.New("frontend check recovery accepted Git pins changed")
	}
	return nil
}

func verifyFrontendCheckHelper(path, expected string) error {
	_, err := frontendCheckHelperBytes(path, expected)
	return err
}

func frontendCheckHelperBytes(path, expected string) ([]byte, error) {
	return readPinnedCommandHelper(path, expected, "runner-frontend-check", 64<<20)
}

var githubOrigin = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_-]+/[A-Za-z0-9_.-]+)$`)

func (r *Releases) validateFrontendCheckPublication(ctx context.Context, run runner.Run, expectedHead string) error {
	if r.cfg == nil || !filepath.IsAbs(r.cfg.GitHub) || !publicationCommit.MatchString(expectedHead) {
		return errors.New("frontend check recovery lacks configured GitHub or the exact publication head")
	}
	a := runner.Assignment{RunID: run.ID, ProjectID: run.ProjectID, Repository: run.Repository, Source: run.Source}
	b, err := r.loadPublication(a)
	if err != nil {
		return err
	}
	var original release.Receipt
	if b.AuthorityOnly || b.Latest != b.Adoption || b.Adoption.Head != expectedHead || json.Unmarshal(run.Inputs["publication"].Inline, &original) != nil || original.Action != "publish" || original.Head != b.Adoption.Head || original.Source != b.Base {
		return errors.New("frontend check recovery does not own the original adopted publication")
	}
	if run.Process.ID == "deliver-retained-catalogue" || run.Process.ID == "deliver-planned-retained-frontend" {
		// Retained source adoption can cross policies. Authenticate the accepted
		// adoption under the current policy from both durable records.
		if len(run.Attempts) == 0 || len(run.Process.Steps) == 0 || run.Process.Steps[0].ID != "source-adopt" {
			return errors.New("frontend check recovery lacks the saved source adoption")
		}
		attempt := run.Attempts[0]
		var receipt release.Receipt
		if attempt.Step != 0 || attempt.ID != b.Adoption.Key || attempt.Superseded || attempt.End == nil || attempt.End.Kind != "returned" ||
			attempt.End.Result == nil || attempt.End.Result.Outcome != "ready" ||
			json.Unmarshal(attempt.End.Result.Outputs["receipt"].Inline, &receipt) != nil || receipt != b.Adoption {
			return errors.New("frontend check recovery source adoption differs from its saved receipt")
		}
	} else if original.Policy != b.Adoption.Policy {
		return errors.New("frontend check recovery does not own the original adopted publication")
	}
	originRun, originKey := original.PublicationRunID, original.PublicationKey
	if originRun == "" && originKey == "" {
		originRun, originKey = original.RunID, original.Key
	}
	if originRun != b.Adoption.PublicationRunID || originKey != b.Adoption.PublicationKey {
		return errors.New("frontend check recovery publication origin changed")
	}
	return r.validateFrontendDraft(ctx, run, b.Adoption)
}

func (r *Releases) validateFrontendDraft(ctx context.Context, run runner.Run, expected release.Receipt) error {
	origin, err := git(ctx, run.Repository, nil, nil, 1024, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	repository := githubOrigin.FindStringSubmatch(strings.TrimSuffix(strings.TrimSpace(string(origin)), ".git"))
	if repository == nil {
		return errors.New("frontend check recovery requires the owned HTTPS GitHub origin")
	}
	cmd := exec.CommandContext(ctx, r.cfg.GitHub, "pr", "view", strconv.Itoa(expected.PR), "--repo", "github.com/"+repository[1], "--json", "state,isDraft,isCrossRepository,headRefName,headRefOid,baseRefOid")
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=/usr/bin:/bin", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}
	output := &diagnosticTail{limit: 4096}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return errors.New("frontend check recovery could not verify the live draft publication")
	}
	return verifyFrontendCheckDraft(output.bytes(), expected)
}

func verifyFrontendCheckDraft(data []byte, adopted release.Receipt) error {
	var pr struct {
		State             string
		IsDraft           *bool
		IsCrossRepository *bool
		HeadRefName       string
		HeadRefOid        string
		BaseRefOid        string
	}
	if json.Unmarshal(data, &pr) != nil || pr.State != "OPEN" || pr.IsDraft == nil || !*pr.IsDraft || pr.IsCrossRepository == nil || *pr.IsCrossRepository || pr.HeadRefName != strings.TrimPrefix(candidateRef(adopted.PublicationRunID), "refs/heads/") || pr.HeadRefOid != adopted.Head || pr.BaseRefOid != adopted.Base {
		return errors.New("frontend check recovery publication head, base or ownership changed")
	}
	return nil
}
