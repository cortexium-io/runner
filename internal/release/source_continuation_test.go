package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sourceOperatorFixture struct {
	op                                    Operator
	request                               Request
	publication                           Job
	branch, head, base, repository, state string
	reads, creates                        int
	readError                             error
	draft                                 bool
}

func newSourceOperatorFixture(t *testing.T) *sourceOperatorFixture {
	t.Helper()
	f := &sourceOperatorFixture{branch: "codex/runner/original", head: strings.Repeat("b", 40), base: strings.Repeat("a", 40), repository: "owner/repo", state: "open", draft: true}
	dir := filepath.Join(t.TempDir(), "journal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f.publication = Job{Done: true,
		Request: Request{Project: "demo", RunID: "original", Key: "published", Action: "publish", Source: f.base, Candidate: f.head},
		Receipt: Receipt{Key: "published", RunID: "original", Action: "publish", Policy: "policy", Source: f.base, Base: f.base, Head: f.head, PR: 334,
			Image: "old-image", Report: "old-report", Backup: "old-backup", Commit: "old-commit"},
	}
	if err := writeJSON(filepath.Join(dir, "published.json"), f.publication); err != nil {
		t.Fatal(err)
	}
	b := &Live{Config: Config{Project: "demo", Repository: "owner/repo", Branch: "main", GH: "/gh", Delivery: &DeliveryConfig{Ownership: []string{"/ownership"}}}}
	b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "pr create") {
			f.creates++
			return nil, errors.New("continuation must not create a PR")
		}
		if strings.Contains(joined, "/pulls/334") {
			f.reads++
			if f.readError != nil {
				return nil, f.readError
			}
			return json.Marshal(map[string]any{"state": f.state, "merged": false, "draft": f.draft,
				"head": map[string]any{"sha": f.head, "ref": f.branch, "repo": map[string]string{"full_name": f.repository}},
				"base": map[string]any{"sha": f.base, "ref": "main", "repo": map[string]string{"full_name": "owner/repo"}}})
		}
		if strings.Contains(joined, "/pulls?") {
			if !strings.Contains(joined, "head=owner:codex/runner/original") {
				t.Fatalf("selected another publication ref: %s", joined)
			}
			return json.Marshal([]any{map[string]any{"number": 334, "head": map[string]string{"sha": f.head}, "base": map[string]string{"sha": f.base}}})
		}
		if strings.Contains(joined, "/commits/") {
			return json.Marshal(map[string]any{"sha": f.head, "statuses": []any{map[string]string{"context": "Techbase / generated ownership", "state": "success"}}})
		}
		t.Fatalf("unexpected privileged operation: %s", joined)
		return nil, nil
	}
	f.op = Operator{Directory: dir, PolicyHash: "policy", Project: "demo", Backend: b, Delivery: true}
	f.request = Request{Project: "demo", RunID: "continuation", Key: "source_adopt", Action: "adopt", Source: f.base, Previous: mustJSON(t, publicReceipt(f.publication.Receipt, true)), SourceContinuation: true}
	return f
}

func TestSourceContinuationKeepsOwnedPRAndRequiresFreshPublicationProof(t *testing.T) {
	f := newSourceOperatorFixture(t)
	original, _ := os.ReadFile(filepath.Join(f.op.Directory, "published.json"))
	adopted, err := f.op.Call(t.Context(), f.request)
	if err != nil || adopted.State != "finished" || adopted.Failed {
		t.Fatal(adopted, err)
	}
	var handle Receipt
	if json.Unmarshal(adopted.Receipt, &handle) != nil || handle.PublicationRunID != "original" || handle.PublicationKey != "published" || handle.RunID != "continuation" || handle.PR != 334 || handle.Base != f.base {
		t.Fatal("source adoption lost private ownership", handle)
	}
	data, _ := os.ReadFile(filepath.Join(f.op.Directory, "source_adopt.json"))
	var saved Job
	if json.Unmarshal(data, &saved) != nil || saved.Receipt.Image != "" || saved.Receipt.Report != "" || saved.Receipt.Backup != "" || saved.Receipt.Commit != "" {
		t.Fatal("source continuation inherited release evidence", saved)
	}
	if _, err = f.op.Call(t.Context(), f.request); err != nil || f.reads != 1 {
		t.Fatal("adoption replay repeated live work", err, f.reads)
	}
	f.head = strings.Repeat("c", 40)
	q := Request{Project: "demo", RunID: "continuation", Key: "new_publication", Action: "publish", Source: f.base, Candidate: f.head, Previous: adopted.Receipt}
	published, err := f.op.Call(t.Context(), q)
	if err != nil || published.State != "finished" || published.Failed || f.creates != 0 {
		t.Fatal("owned PR continuation failed", published, err, f.creates)
	}
	var next Receipt
	if json.Unmarshal(published.Receipt, &next) != nil || next.Action != "publish" || next.Head != f.head || next.PR != 334 || next.PublicationRunID != "original" || next.RunID != q.RunID {
		t.Fatal("new publication lost original ref ownership", next)
	}
	reads := f.reads
	again, err := f.op.Call(t.Context(), q) // Caller lost the first accepted reply.
	if err != nil || string(again.Receipt) != string(published.Receipt) || f.reads != reads {
		t.Fatal("lost publication reply repeated or changed its operation", err)
	}
	after, _ := os.ReadFile(filepath.Join(f.op.Directory, "published.json"))
	if string(original) != string(after) {
		t.Fatal("source continuation rewrote original history")
	}
	for _, action := range []string{"prepare", "merge", "deploy"} {
		_, err := f.op.Call(t.Context(), Request{Project: "demo", RunID: q.RunID, Key: "skip_" + action, Action: action, Source: f.base, Previous: published.Receipt})
		if err == nil {
			t.Fatal("new publication bypassed fresh qualification/approval ordering", action)
		}
	}
}

func TestSourceContinuationRejectsForgedOrMismatchedSourceBeforeLiveWork(t *testing.T) {
	for _, fault := range []string{"head", "run", "origin", "source", "candidate-journal", "key-journal", "origin-journal", "old-qualification", "wrong-action"} {
		t.Run(fault, func(t *testing.T) {
			f := newSourceOperatorFixture(t)
			q := f.request
			var p Receipt
			json.Unmarshal(q.Previous, &p)
			switch fault {
			case "head":
				p.Head = strings.Repeat("d", 40)
			case "run":
				p.RunID = "foreign"
			case "origin":
				p.PublicationRunID = "foreign"
			case "source":
				q.Source = strings.Repeat("d", 40)
			case "candidate-journal":
				f.publication.Request.Candidate = strings.Repeat("d", 40)
			case "key-journal":
				f.publication.Request.Key = "foreign"
			case "origin-journal":
				f.publication.Receipt.PublicationRunID, f.publication.Receipt.PublicationKey = "foreign", "published"
				p = publicReceipt(f.publication.Receipt, true)
			case "old-qualification":
				p.Action = "qualify"
			case "wrong-action":
				q.Action = "publish"
			}
			if err := writeJSON(filepath.Join(f.op.Directory, "published.json"), f.publication); err != nil {
				t.Fatal(err)
			}
			q.Previous = mustJSON(t, p)
			if _, err := f.op.Call(t.Context(), q); err == nil || f.reads != 0 || f.creates != 0 {
				t.Fatal("forged source continuation reached live authority", err, f.reads)
			}
		})
	}
}

func TestSourceContinuationRequiresUnchangedOwnedLivePR(t *testing.T) {
	for _, fault := range []string{"head", "base", "branch", "repository", "closed", "read-error"} {
		t.Run(fault, func(t *testing.T) {
			f := newSourceOperatorFixture(t)
			switch fault {
			case "head":
				f.head = strings.Repeat("d", 40)
			case "base":
				f.base = strings.Repeat("d", 40)
			case "branch":
				f.branch = "human-branch"
			case "repository":
				f.repository = "foreign/repo"
			case "closed":
				f.state = "closed"
			case "read-error":
				f.readError = &commandUncertain{program: "gh"}
			}
			out, err := f.op.Call(t.Context(), f.request)
			if fault == "read-error" {
				if err != nil || out.State != "unknown" || out.Failed {
					t.Fatal("uncertain PR read became settled source feedback", out, err)
				}
			} else if err != nil || out.State != "finished" || !out.Failed {
				t.Fatal("changed owned PR was adopted", out, err)
			}
			if f.creates != 0 {
				t.Fatal("changed PR caused replacement publication")
			}
		})
	}
}

func retainedPublicationFixture(t *testing.T) *sourceOperatorFixture {
	t.Helper()
	f := newSourceOperatorFixture(t)
	f.publication.Receipt.Policy = transitionOldPolicy
	if err := writeJSON(filepath.Join(f.op.Directory, "published.json"), f.publication); err != nil {
		t.Fatal(err)
	}
	p := f.publication.Receipt
	f.request.Previous = mustJSON(t, publicReceipt(p, true))
	f.op.PublicationTransition = exactPublicationTransition()
	f.op.RetainedPublicationTransition = &RetainedPublicationTransition{
		PublicationTransition: PublicationTransition{PreviousPolicy: p.Policy, PR: p.PR, Head: p.Head, Base: p.Base},
		PreviousRunID:         p.RunID,
		PreviousKey:           p.Key,
	}
	return f
}

func TestRetainedPublicationTransitionResumesOneClaim(t *testing.T) {
	f := retainedPublicationFixture(t)
	path := filepath.Join(f.op.Directory, "published.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A crash after claiming and before saving the new journal allows only
	// the same run and attempt to resume.
	claim := filepath.Join(f.op.Directory, "retained-publication-published.claim")
	if err := claimTransition(claim, transitionClaim{Key: f.request.Key, Policy: f.op.PolicyHash, RunID: f.request.RunID}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []Request{
		{Project: f.request.Project, RunID: "another", Key: f.request.Key, Action: "adopt", Source: f.base, Previous: f.request.Previous, SourceContinuation: true},
		{Project: f.request.Project, RunID: f.request.RunID, Key: "another", Action: "adopt", Source: f.base, Previous: f.request.Previous, SourceContinuation: true},
	} {
		if _, err := f.op.Call(t.Context(), q); err == nil || f.reads != 0 {
			t.Fatal("another adoption reused the claim", err)
		}
	}
	out, err := f.op.Call(t.Context(), f.request)
	var adopted Receipt
	if err != nil || out.State != "finished" || out.Failed || json.Unmarshal(out.Receipt, &adopted) != nil ||
		adopted.Policy != f.op.PolicyHash || adopted.RunID != f.request.RunID || adopted.PublicationKey != f.publication.Request.Key {
		t.Fatal("exact adoption did not issue current-policy source authority", out, err)
	}
	if _, err := f.op.Call(t.Context(), f.request); err != nil || f.reads != 1 {
		t.Fatal("same attempt replayed the live check", err, f.reads)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("prior accepted publication changed", err)
	}
}

func TestRetainedPublicationTransitionRejectsOtherAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sourceOperatorFixture)
	}{
		{"other publish key", func(f *sourceOperatorFixture) { f.op.RetainedPublicationTransition.PreviousKey = "foreign" }},
		{"other previous run", func(f *sourceOperatorFixture) { f.op.RetainedPublicationTransition.PreviousRunID = "foreign" }},
		{"other base", func(f *sourceOperatorFixture) { f.op.RetainedPublicationTransition.Base = strings.Repeat("c", 40) }},
		{"no source continuation", func(f *sourceOperatorFixture) { f.request.SourceContinuation = false }},
		{"qualification", func(f *sourceOperatorFixture) {
			f.request.Action, f.request.SourceContinuation, f.request.RunID = "qualify", false, f.publication.Request.RunID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := retainedPublicationFixture(t)
			tc.change(f)
			if _, err := f.op.Call(t.Context(), f.request); err == nil || f.reads != 0 {
				t.Fatal("unapproved request reached the live repository", err)
			}
		})
	}
}

func TestRetainedPublicationTransitionRequiresDraft(t *testing.T) {
	f := retainedPublicationFixture(t)
	f.draft = false
	out, err := f.op.Call(t.Context(), f.request)
	if err != nil || out.State != "finished" || !out.Failed || f.reads != 1 || f.creates != 0 {
		t.Fatal("open non-draft PR passed the scoped adoption", out, err)
	}
}
