package release

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
)

func validPublicationOrigin(r Receipt) bool {
	return safeID.MatchString(r.PublicationRunID) && safeID.MatchString(r.PublicationKey) && r.PR > 0 &&
		regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(r.Head) && r.Base == r.Source &&
		regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(r.Base)
}

func acceptedSourcePredecessor(j Job) bool {
	r, q := j.Receipt, j.Request
	if !j.Done || j.Failed || j.Outcome != "" || q.Key != r.Key || q.RunID != r.RunID || q.Action != r.Action || q.Source != r.Source || r.Source != r.Base || r.PR < 1 || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(r.Head) {
		return false
	}
	if q.Action == "publish" {
		return q.Candidate == r.Head
	}
	return q.Action == "adopt" && q.SourceContinuation && validPublicationOrigin(r)
}

func (o *Operator) sourceOriginAccepted(r Receipt, project string) bool {
	if !validPublicationOrigin(r) {
		return false
	}
	data, err := readFile(filepath.Join(o.Directory, r.PublicationKey+".json"), 32<<10)
	var origin Job
	if err != nil || json.Unmarshal(data, &origin) != nil || !acceptedSourcePredecessor(origin) {
		return false
	}
	return origin.Request.Project == project && origin.Request.Action == "publish" && origin.Request.Key == r.PublicationKey &&
		origin.Request.RunID == r.PublicationRunID && origin.Receipt.PublicationRunID == "" && origin.Receipt.PublicationKey == "" &&
		origin.Receipt.PR == r.PR && origin.Receipt.Base == r.Base && origin.Receipt.Source == r.Source
}

// Only publication identity crosses the source continuation. Build, runtime,
// backup, deployment and approval evidence must all be produced again.
func sourcePublicationReceipt(r Receipt) Receipt {
	return Receipt{Key: r.Key, RunID: r.RunID, Action: r.Action, Policy: r.Policy,
		Head: r.Head, Base: r.Base, Source: r.Source, PR: r.PR,
		PublicationRunID: r.PublicationRunID, PublicationKey: r.PublicationKey}
}

func (b *Live) publicationBranch(ctx context.Context, j *Job) (string, error) {
	if j.Receipt.PublicationRunID == "" && j.Receipt.PublicationKey == "" {
		return "codex/runner/" + j.Request.RunID, nil
	}
	if !validPublicationOrigin(j.Receipt) {
		return "", &proofFailure{reason: "publication continuation ownership is invalid"}
	}
	if err := b.continuedPR(ctx, j, j.Request.Candidate); err != nil {
		return "", err
	}
	return "codex/runner/" + j.Receipt.PublicationRunID, nil
}

// This read validates the actual owning repository/ref as well as PR/head/base.
// Neither a task nor a compact public handle chooses a different remote branch.
func (b *Live) continuedPR(ctx context.Context, j *Job, head string) error {
	var pr struct {
		State  string
		Merged bool
		Head   struct {
			SHA, Ref string
			Repo     struct {
				FullName string `json:"full_name"`
			}
		}
		Base struct {
			SHA, Ref string
			Repo     struct {
				FullName string `json:"full_name"`
			}
		}
	}
	if err := b.gh(ctx, fmt.Sprintf("repos/%s/pulls/%d", b.Config.Repository, j.Receipt.PR), &pr); err != nil {
		return err
	}
	if !validPublicationOrigin(j.Receipt) || pr.State != "open" || pr.Merged || pr.Head.SHA != head || pr.Head.Ref != "codex/runner/"+j.Receipt.PublicationRunID || pr.Head.Repo.FullName != b.Config.Repository || pr.Base.SHA != j.Request.Source || pr.Base.Ref != b.Config.Branch || pr.Base.Repo.FullName != b.Config.Repository {
		return &proofFailure{reason: "continued publication PR/ref/head/base ownership changed; preserve the original source and PR"}
	}
	return nil
}
