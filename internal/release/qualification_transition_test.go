package release

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type qualificationBackend struct{ calls int }

func (b *qualificationBackend) Advance(_ context.Context, j *Job, _ func() error) error {
	b.calls++
	return done(j, "fixture accepted")
}

func qualificationTransitionFixture(t *testing.T) (*Operator, Request, Job) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	published := Receipt{
		Key: "published", RunID: "continuation", Action: "publish", Policy: transitionOldPolicy,
		Head: transitionHead, Base: transitionBase, Source: transitionBase, PR: 42,
		PublicationRunID: "original", PublicationKey: "origin",
	}
	origin := published
	origin.RunID, origin.Key = published.PublicationRunID, published.PublicationKey
	origin.PublicationRunID, origin.PublicationKey = "", ""
	save := func(j Job) {
		t.Helper()
		if err := writeJSON(filepath.Join(dir, j.Request.Key+".json"), j); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []Receipt{origin, published} {
		save(Job{Receipt: r, Done: true, Request: Request{Key: r.Key, RunID: r.RunID, Project: "demo", Action: "publish", Source: r.Source, Candidate: r.Head}})
	}
	request := Request{Project: "demo", Key: "new_qualification", RunID: published.RunID, Action: "qualify", Source: published.Source, Previous: mustJSON(t, publicReceipt(published, true))}
	failed := Job{Request: request, Receipt: published, Done: true, Failed: true}
	failed.Request.Key, failed.Receipt.Key, failed.Receipt.Action = "failed_qualification", "failed_qualification", "qualify"
	failed.Receipt.BrowserFailure = BrowserFailureFeedback{Kind: "admission", ReportSHA256: strings.Repeat("c", 64)}
	failed.Receipt.Image, failed.Receipt.Report = "discarded-image", "discarded-report"
	save(failed)
	op := &Operator{
		Directory: dir, PolicyHash: strings.Repeat("d", 64), Project: "demo", Delivery: true,
		Backend: &qualificationBackend{}, PublicationTransition: exactPublicationTransition(),
		QualificationTransition: &QualificationTransition{
			PublicationTransition: *exactPublicationTransition(), RunID: published.RunID, PublicationKey: published.Key,
			FailedQualificationKey: failed.Request.Key, FailureReportSHA256: failed.Receipt.BrowserFailure.ReportSHA256,
		},
	}
	return op, request, failed
}

func TestQualificationTransitionBindsOneFreshSameRunAttempt(t *testing.T) {
	op, request, failed := qualificationTransitionFixture(t)
	backend := op.Backend.(*qualificationBackend)
	failedPath := filepath.Join(op.Directory, failed.Request.Key+".json")
	before, err := os.ReadFile(failedPath)
	if err != nil {
		t.Fatal(err)
	}
	var previous Receipt
	var publication Job
	published, err := os.ReadFile(filepath.Join(op.Directory, "published.json"))
	if err != nil || json.Unmarshal(published, &publication) != nil || json.Unmarshal(request.Previous, &previous) != nil {
		t.Fatal("fixture publication unavailable", err)
	}
	// Simulate an interruption after the claim is durable, before the job file.
	if err := op.claimQualificationTransition(request, previous, publication); err != nil {
		t.Fatal(err)
	}
	first, err := op.Call(t.Context(), request)
	if err != nil || first.State != "finished" || first.Failed || backend.calls != 1 {
		t.Fatal("qualification transition could not dispatch the exact retained candidate", first, err)
	}
	jobData, err := os.ReadFile(filepath.Join(op.Directory, request.Key+".json"))
	var job Job
	if err != nil || json.Unmarshal(jobData, &job) != nil || !job.QualificationHandoff ||
		job.Receipt.Policy != op.PolicyHash || job.Receipt.Head != transitionHead || job.Receipt.Base != transitionBase || job.Receipt.PR != 42 ||
		job.Receipt.Image != "" || job.Receipt.Report != "" || job.Receipt.BrowserFailure != (BrowserFailureFeedback{}) {
		t.Fatal("new qualification did not start with publication identity only", job.Receipt, err)
	}
	if _, err := op.Call(t.Context(), request); err != nil || backend.calls != 1 {
		t.Fatal("same attempt did not rejoin the journal", err)
	}
	request.Key = "second_qualification"
	if _, err := op.Call(t.Context(), request); err == nil || backend.calls != 1 {
		t.Fatal("a different attempt reused the one qualification grant")
	}
	after, err := os.ReadFile(failedPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("qualification transition changed the prior failed journal", err)
	}
}

func TestQualificationTransitionRejectsChangedAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Operator, *Request, *Job)
	}{
		{"different run", func(_ *Operator, q *Request, _ *Job) { q.RunID = "another" }},
		{"different head", func(o *Operator, _ *Request, _ *Job) { o.QualificationTransition.Head = strings.Repeat("1", 40) }},
		{"different publication", func(o *Operator, _ *Request, _ *Job) { o.QualificationTransition.PublicationKey = "another" }},
		{"active qualification", func(_ *Operator, _ *Request, j *Job) { j.Done = false }},
		{"different failed report", func(_ *Operator, _ *Request, j *Job) { j.Receipt.BrowserFailure.ReportSHA256 = strings.Repeat("1", 64) }},
		{"other operation", func(_ *Operator, q *Request, _ *Job) { q.Action = "prepare" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op, request, failed := qualificationTransitionFixture(t)
			tc.change(op, &request, &failed)
			if err := writeJSON(filepath.Join(op.Directory, "failed_qualification.json"), failed); err != nil {
				t.Fatal(err)
			}
			if _, err := op.Call(t.Context(), request); err == nil || op.Backend.(*qualificationBackend).calls != 0 {
				t.Fatal("changed qualification authority reached an effect", err)
			}
		})
	}
}

func TestQualificationTransitionChecksDraftBeforeStartingWorker(t *testing.T) {
	op, request, _ := qualificationTransitionFixture(t)
	live := &Live{Config: Config{Repository: "owner/repo", Branch: "main", Delivery: &DeliveryConfig{}}}
	live.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		if len(args) != 4 || args[3] != "repos/owner/repo/pulls/42" {
			t.Fatalf("qualification exceeded its read-only PR preflight: %q", args)
		}
		repo := map[string]string{"full_name": "owner/repo"}
		return json.Marshal(map[string]any{
			"state": "open", "draft": false,
			"head": map[string]any{"sha": transitionHead, "ref": "codex/runner/original", "repo": repo},
			"base": map[string]any{"sha": transitionBase, "ref": "main", "repo": repo},
		})
	}
	op.Backend = live
	response, err := op.Call(t.Context(), request)
	if err != nil || response.State != "finished" || !response.Failed || !strings.Contains(response.Reason, "ownership changed") {
		t.Fatal("qualification started without its exact draft publication", response, err)
	}
	data, err := os.ReadFile(filepath.Join(op.Directory, request.Key+".json"))
	var rejected Job
	if err != nil || json.Unmarshal(data, &rejected) != nil || rejected.Worker != "" || rejected.Receipt.Package != "" {
		t.Fatal("draft preflight created worker or package authority", rejected, err)
	}
	live.Config.Project, live.Config.StateDir = op.Project, t.TempDir()
	path := filepath.Join(live.Config.StateDir, request.Key+".json")
	if err := writeJSON(path, rejected); err != nil {
		t.Fatal(err)
	}
	packages, jobs, err := live.retainedPackages()
	if err != nil || len(packages) != 0 || len(jobs) != 1 {
		t.Fatal("terminal preflight blocked later retention", packages, jobs, err)
	}
	rejected.QualificationHandoff = false
	if err := writeJSON(path, rejected); err != nil {
		t.Fatal(err)
	}
	if _, _, err := live.retainedPackages(); err == nil {
		t.Fatal("retention accepted an unrecognized workerless qualification")
	}
}

func TestQualificationTransitionFailureKeepsEvidenceWithoutAnotherDispatch(t *testing.T) {
	browser := BrowserFailureFeedback{Kind: "admission", ReportSHA256: strings.Repeat("c", 64)}
	source := &sourceFailure{reason: "fixture browser assertion", browser: browser}
	for _, failure := range []error{source, &connectionUnavailable{}} {
		normal := Job{Request: Request{Action: "qualify"}}
		scoped := Job{Request: normal.Request, QualificationHandoff: true}
		settleDeliveryFailure(&normal, failure)
		settleDeliveryFailure(&scoped, failure)
		if !scoped.Done || !scoped.Failed || scoped.Retryable || scoped.Outcome != "" {
			t.Fatal("scoped qualification authorized another dispatch", scoped)
		}
		if failure == source {
			if scoped.Receipt.BrowserFailure != browser || normal.Outcome != "changes_requested" || normal.Failed {
				t.Fatal("source evidence or ordinary correction routing changed", scoped, normal)
			}
		} else if !normal.Retryable {
			t.Fatal("ordinary infrastructure retry was lost", normal)
		}
	}
}
