package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

type publicationBinding struct {
	RunID        string              `json:"run_id"`
	Repository   string              `json:"repository"`
	Base         string              `json:"base"`
	Adoption     release.Receipt     `json:"adoption"`
	Latest       release.Receipt     `json:"latest"`
	Source       runner.SourceResult `json:"source"`
	PatchSHA256  string              `json:"patch_sha256"`
	LastAttempt  string              `json:"last_attempt,omitempty"`
	LastPrevious release.Receipt     `json:"last_previous,omitzero"`
}

var publicationID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var publicationCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var publicationDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func samePublicationOwner(a, b release.Receipt) bool {
	return a.PublicationRunID == b.PublicationRunID && a.PublicationKey == b.PublicationKey && a.PR == b.PR && a.Base == b.Base && a.Source == b.Source && a.Policy == b.Policy
}

func (r *Releases) loadPublication(a runner.Assignment) (publicationBinding, error) {
	var b publicationBinding
	name, err := publicationBindingName(a.RunID)
	if err != nil {
		return b, err
	}
	data, err := r.disk.Read(name, r.disk.Policy.Database.MaxRecord)
	if err != nil {
		return b, err
	}
	repository, err := filepath.EvalSymlinks(a.Repository)
	if err != nil {
		return b, err
	}
	if json.Unmarshal(data, &b) != nil || b.RunID != a.RunID || b.Repository != repository || a.Source == nil || b.Base != a.Source.Ref {
		return b, errors.New("retained publication binding ownership or source differs")
	}
	if b.Adoption.RunID != a.RunID || b.Adoption.Action != "adopt" || b.Adoption.Policy == "" || !publicationID.MatchString(b.Adoption.Key) || !publicationID.MatchString(b.Adoption.PublicationRunID) || !publicationID.MatchString(b.Adoption.PublicationKey) || b.Adoption.PR < 1 || b.Adoption.Base != b.Base || b.Adoption.Source != b.Base || !publicationCommit.MatchString(b.Adoption.Head) {
		return b, errors.New("retained source adoption identity differs")
	}
	if !samePublicationOwner(b.Adoption, b.Latest) || b.Latest.RunID != a.RunID || (b.Latest.Action != "adopt" && b.Latest.Action != "publish") || !publicationID.MatchString(b.Latest.Key) || !publicationCommit.MatchString(b.Latest.Head) {
		return b, errors.New("retained latest publication identity differs")
	}
	if b.Source.AttemptID != b.Adoption.Key || b.Source.PatchID != b.Adoption.Key+"/changes.patch" || b.Source.Repository != b.Repository || b.Source.Base != b.Base || b.Source.WorkDir != filepath.Join(r.disk.Root, "work", a.RunID, "source") || !publicationCommit.MatchString(b.Source.Tree) || !publicationDigest.MatchString(b.PatchSHA256) {
		return b, errors.New("retained adopted source identity differs")
	}
	return b, nil
}

func (r *Releases) adoptedSource(ctx context.Context, j releaseJob, data json.RawMessage) (*runner.SourceResult, error) {
	a := j.Assignment
	if a.Setup.Profile.Harness != "release" || a.Setup.Profile.Access != "input-only" || a.Setup.Profile.Mode != "source-adopt" || a.Source == nil || !publicationCommit.MatchString(a.Source.Ref) || r.workspaces == nil || r.workspaces.read == nil || a.Inputs["patch"].ArtifactID == "" {
		return nil, errors.New("source adoption requires its configured exact-source artifact contract")
	}
	var receipt release.Receipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Key != a.AttemptID || receipt.RunID != a.RunID || receipt.Action != "adopt" || receipt.Source != a.Source.Ref || receipt.Base != a.Source.Ref || receipt.PR < 1 || !publicationCommit.MatchString(receipt.Head) || !publicationID.MatchString(receipt.PublicationRunID) || !publicationID.MatchString(receipt.PublicationKey) || receipt.Policy == "" {
		return nil, errors.New("operator source adoption receipt identity differs")
	}
	repository, err := filepath.EvalSymlinks(a.Repository)
	if err != nil {
		return nil, err
	}
	// Exact local objects must already belong to the configured source cache.
	// No fetch, checkout, branch or caller-selected remote mutation occurs here.
	parent, err := git(ctx, repository, []string{"GIT_NO_LAZY_FETCH=1"}, nil, 256, "rev-parse", "--verify", receipt.Head+"^")
	if err != nil || strings.TrimSpace(string(parent)) != a.Source.Ref {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("accepted publication parent differs from the original source base")
	}
	tree, err := git(ctx, repository, []string{"GIT_NO_LAZY_FETCH=1"}, nil, 256, "rev-parse", "--verify", receipt.Head+"^{tree}")
	if err != nil {
		return nil, err
	}
	patch, err := r.workspaces.read(ctx, a.Inputs["patch"].ArtifactID)
	if err != nil {
		return nil, err
	}
	if int64(len(patch)) > int64(a.Limits.MaxArtifact) {
		return nil, runner.ErrCapacity
	}
	digest := release.Digest(patch)
	source, err := r.workspaces.prepareAdoptedSource(ctx, a, receipt.Head, strings.TrimSpace(string(tree)), digest)
	if err != nil {
		return nil, err
	}
	if source == nil || source.AttemptID != a.AttemptID || source.PatchID != a.AttemptID+"/changes.patch" || source.Repository != repository || source.Base != a.Source.Ref || source.Tree != strings.TrimSpace(string(tree)) || source.WorkDir != filepath.Join(r.disk.Root, "work", a.RunID, "source") {
		return nil, errors.New("adopted source reconstruction differs from its accepted publication")
	}
	b := publicationBinding{RunID: a.RunID, Repository: repository, Base: a.Source.Ref, Adoption: receipt, Latest: receipt, Source: *source, PatchSHA256: digest}
	if previous, err := r.loadPublication(a); err == nil {
		if previous.Adoption != receipt || previous.Latest != receipt || previous.Source != *source || previous.PatchSHA256 != digest {
			return nil, errors.New("source adoption would replace an existing publication binding")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = r.savePublication(b); err != nil {
		return nil, err
	}
	return source, nil
}

func (r *Releases) finishSourceAdoption(ctx context.Context, j releaseJob, o runner.Observation, response release.Response, outcome string) (runner.Observation, error) {
	if j.Stopped {
		j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "source adoption stopped without publication"}
	} else if response.Failed || outcome != "ready" {
		j.End = &runner.ExecutionEnd{Kind: "failed", Reason: runner.Clip(response.Reason)}
	} else {
		if len(j.AdoptionReceipt) == 0 {
			j.AdoptionReceipt = runner.Clone(response.Receipt)
			if err := r.save(j); err != nil {
				return runner.Observation{}, err
			}
		}
		source, err := r.adoptedSource(ctx, j, j.AdoptionReceipt)
		if err != nil {
			var capture *capturePending
			if captureWasInterrupted(ctx, err) || errors.As(err, &capture) {
				o.State, o.Reconciling = runner.Unknown, true
				o.Reason = "source adoption interrupted; rejoin the same authenticated operation"
				o.NextCheck = time.Now().Add(time.Second)
				return o, nil
			}
			if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
				return o, err
			}
			j.End = &runner.ExecutionEnd{Kind: "failed", Reason: "source adoption: " + runner.Clip(err.Error())}
		} else {
			j.Source = source
			j.End = &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{
				"receipt": {Inline: response.Receipt}, "patch": {ArtifactID: source.PatchID},
			}}}
		}
	}
	if err := r.save(j); err != nil {
		return runner.Observation{}, err
	}
	current, err := r.load(j.Assignment.AttemptID)
	if err != nil {
		return runner.Observation{}, err
	}
	j = current
	o.State, o.End, o.Source = runner.Finished, j.End, j.Source
	return o, nil
}

func (r *Releases) savePublication(b publicationBinding) error {
	name, err := publicationBindingName(b.RunID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return r.disk.Write(name, data, r.disk.Policy.Database.MaxRecord)
}

// publicationAuthorization trusts only the binding created after authenticated
// source adoption. Task input can name neither a PR nor a remote ref.
func (r *Releases) publicationAuthorization(j *releaseJob) (ref, previousHead string, continued bool, err error) {
	a := j.Assignment
	var previous release.Receipt
	value := a.Inputs["previous"].Inline
	hasPrevious := len(value) > 0 && string(value) != "{}"
	if hasPrevious {
		if json.Unmarshal(value, &previous) != nil {
			return "", "", false, errors.New("previous publication format differs")
		}
	}
	b, err := r.loadPublication(a)
	if errors.Is(err, os.ErrNotExist) {
		if previous.PublicationRunID != "" || previous.PublicationKey != "" || previous.Action == "adopt" {
			return "", "", false, errors.New("source continuation has no authenticated local publication binding")
		}
		if hasPrevious && (previous.RunID != a.RunID || previous.Action != "publish" || !publicationCommit.MatchString(previous.Head)) {
			return "", "", false, errors.New("previous publication identity differs")
		}
		return candidateRef(a.RunID), previous.Head, false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	replay := b.LastAttempt == a.AttemptID && b.Latest.Key == a.AttemptID && b.Latest.Action == "publish" && b.LastPrevious == previous && b.Latest.Head == j.Candidate
	if previous != b.Latest && !replay {
		return "", "", false, errors.New("source continuation predecessor differs from its retained publication")
	}
	if j.PublicationPrevious != nil {
		var saved release.Receipt
		if json.Unmarshal(j.PublicationPrevious, &saved) != nil || saved != previous {
			return "", "", false, errors.New("saved publication operation predecessor changed")
		}
	}
	j.PublicationPrevious = runner.Clone(value)
	return candidateRef(b.Adoption.PublicationRunID), previous.Head, true, nil
}

func (r *Releases) recordPublication(j releaseJob, data json.RawMessage) error {
	if len(j.PublicationPrevious) == 0 {
		return nil
	}
	b, err := r.loadPublication(j.Assignment)
	if err != nil {
		return err
	}
	var receipt, previous release.Receipt
	if json.Unmarshal(data, &receipt) != nil || json.Unmarshal(j.PublicationPrevious, &previous) != nil || receipt.Key != j.Assignment.AttemptID || receipt.RunID != j.Assignment.RunID || receipt.Action != "publish" || receipt.Head != j.Candidate || !samePublicationOwner(b.Adoption, receipt) {
		return errors.New("accepted publication differs from its saved continuation operation")
	}
	if b.Latest == receipt && b.LastAttempt == j.Assignment.AttemptID && b.LastPrevious == previous {
		return nil
	}
	if b.Latest != previous {
		return errors.New("publication continuation advanced outside its saved operation")
	}
	b.Latest, b.LastAttempt, b.LastPrevious = receipt, j.Assignment.AttemptID, previous
	return r.savePublication(b)
}

func publicationBindingName(id string) (string, error) {
	if !fs.ValidPath(id) || strings.Contains(id, "/") {
		return "", errors.New("invalid publication run identity")
	}
	return "jobs/" + id + ".publication.json", nil
}

// The binding survives attempt/resource cleanup. Only explicit run retention
// forgets its publication authority alongside the retained run history.
func (r *Releases) ForgetRun(_ context.Context, id string) error {
	name, err := publicationBindingName(id)
	if err != nil {
		return err
	}
	return r.disk.Remove(name)
}
