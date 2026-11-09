package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func retainedFollowupFixture(t *testing.T) (*sourceOperatorFixture, Request, string, string) {
	t.Helper()
	f := retainedPublicationFixture(t)
	f.op.PolicyHash = strings.Repeat("e", 64)
	consumed := f.request
	out, err := f.op.Call(t.Context(), consumed)
	if err != nil || out.State != "finished" || out.Failed || f.reads != 1 {
		t.Fatal("fixture did not consume the original grant", out, err)
	}
	f.op.RetainedPublicationFollowup = &RetainedPublicationFollowup{
		RetainedPublicationTransition: *f.op.RetainedPublicationTransition,
		ConsumedRunID:                 consumed.RunID,
		ConsumedKey:                   consumed.Key,
		ConsumedPolicy:                f.op.PolicyHash,
	}
	f.op.PolicyHash = strings.Repeat("f", 64)
	f.request.RunID, f.request.Key = "followup", "followup_adopt"
	original := filepath.Join(f.op.Directory, "retained-publication-published.claim")
	followup := filepath.Join(f.op.Directory, "retained-publication-followup-published.claim")
	return f, consumed, original, followup
}

func TestRetainedPublicationFollowupUsesSeparateDurableClaim(t *testing.T) {
	f, consumed, original, followup := retainedFollowupFixture(t)
	kept := map[string][]byte{}
	for _, path := range []string{original, filepath.Join(f.op.Directory, consumed.Key+".json"), filepath.Join(f.op.Directory, "published.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		kept[path] = data
	}
	// Rejoin after the new claim was made durable but before the job was saved.
	if err := f.op.RetainedPublicationFollowup.claim(f.op.Directory, f.request, f.op.PolicyHash); err != nil {
		t.Fatal(err)
	}
	for _, q := range []Request{
		{Project: f.request.Project, RunID: "another", Key: f.request.Key, Action: "adopt", Source: f.base, Previous: f.request.Previous, SourceContinuation: true},
		{Project: f.request.Project, RunID: f.request.RunID, Key: "another", Action: "adopt", Source: f.base, Previous: f.request.Previous, SourceContinuation: true},
	} {
		if _, err := f.op.Call(t.Context(), q); err == nil || f.reads != 1 {
			t.Fatal("another adoption reused the follow-up claim", err)
		}
	}
	out, err := f.op.Call(t.Context(), f.request)
	var receipt Receipt
	if err != nil || out.State != "finished" || out.Failed || json.Unmarshal(out.Receipt, &receipt) != nil ||
		receipt.RunID != f.request.RunID || receipt.Key != f.request.Key || receipt.Policy != f.op.PolicyHash ||
		receipt.PR != f.publication.Receipt.PR || receipt.Base != f.base || receipt.Head != f.head ||
		receipt.PublicationRunID != f.publication.Request.RunID || receipt.PublicationKey != f.publication.Request.Key {
		t.Fatal("follow-up lost the exact current-policy publication identity", out, err)
	}
	data, err := os.ReadFile(filepath.Join(f.op.Directory, f.request.Key+".json"))
	var job Job
	if err != nil || json.Unmarshal(data, &job) != nil || !job.RetainedPublicationHandoff ||
		job.Receipt.Image != "" || job.Receipt.Report != "" || job.Receipt.Backup != "" || job.Receipt.Commit != "" {
		t.Fatal("follow-up inherited release authority or lost its draft check", err)
	}
	data, err = os.ReadFile(followup)
	var claim transitionClaim
	if err != nil || json.Unmarshal(data, &claim) != nil || claim != (transitionClaim{Key: f.request.Key, RunID: f.request.RunID, Policy: f.op.PolicyHash}) {
		t.Fatal("follow-up claim was not retained independently", err)
	}
	reopened := f.op
	if again, err := reopened.Call(t.Context(), f.request); err != nil || !reflect.DeepEqual(out, again) || f.reads != 2 {
		t.Fatal("same operation rejoin repeated the draft check", err, f.reads)
	}
	for path, before := range kept {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("follow-up changed an original claim or operator receipt", filepath.Base(path), err)
		}
	}
}

func TestRetainedPublicationFollowupRefusesMissingOrDifferentOriginalClaim(t *testing.T) {
	for _, fault := range []string{"missing", "different key", "different run", "different policy", "symlink", "extra field"} {
		t.Run(fault, func(t *testing.T) {
			f, _, original, followup := retainedFollowupFixture(t)
			x := f.op.RetainedPublicationFollowup
			claim := transitionClaim{Key: x.ConsumedKey, RunID: x.ConsumedRunID, Policy: x.ConsumedPolicy}
			switch fault {
			case "missing":
				if err := os.Remove(original); err != nil {
					t.Fatal(err)
				}
			case "different key":
				claim.Key = "different"
			case "different run":
				claim.RunID = "different"
			case "different policy":
				claim.Policy = strings.Repeat("a", 64)
			case "symlink":
				foreign := filepath.Join(t.TempDir(), "claim")
				if err := os.Rename(original, foreign); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, original); err != nil {
					t.Fatal(err)
				}
			case "extra field":
				data, err := json.Marshal(claim)
				if err != nil {
					t.Fatal(err)
				}
				data = append(data[:len(data)-1], []byte(`,"extra":true}`)...)
				if err := os.WriteFile(original, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(fault, "different ") {
				if err := writeJSON(original, claim); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.op.Call(t.Context(), f.request); err == nil || !strings.Contains(err.Error(), "exact original consumed claim") || f.reads != 1 {
				t.Fatal("bad original claim reached live draft read or fell back to the old grant", err)
			}
			for _, path := range []string{followup, filepath.Join(f.op.Directory, f.request.Key+".json")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("bad claim consumed the follow-up or created a job", err)
				}
			}
		})
	}
}

func TestRetainedPublicationFollowupConfinesAdoption(t *testing.T) {
	f, consumed, _, followup := retainedFollowupFixture(t)
	for _, q := range []Request{
		{Project: f.request.Project, RunID: consumed.RunID, Key: f.request.Key, Action: "adopt", Source: f.base, Previous: f.request.Previous, SourceContinuation: true},
		{Project: f.request.Project, RunID: f.request.RunID, Key: f.request.Key, Action: "adopt", Source: f.base, Previous: f.request.Previous},
		{Project: f.request.Project, RunID: f.publication.Request.RunID, Key: f.request.Key, Action: "publish", Source: f.base, Previous: f.request.Previous, Candidate: f.head},
		{Project: f.request.Project, RunID: f.publication.Request.RunID, Key: f.request.Key, Action: "qualify", Source: f.base, Previous: f.request.Previous},
	} {
		if _, err := f.op.Call(t.Context(), q); err == nil || f.reads != 1 {
			t.Fatal("follow-up granted unapproved execution", q.Action, err)
		}
	}
	if _, err := os.Lstat(followup); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unapproved actions consumed the fresh adoption", err)
	}
	f.draft = false
	out, err := f.op.Call(t.Context(), f.request)
	if err != nil || out.State != "finished" || !out.Failed || f.reads != 2 || f.creates != 0 {
		t.Fatal("follow-up adopted a non-draft publication", out, err)
	}
}

func TestRetainedPublicationFollowupLoadRequiresMatchingSibling(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "jacks-break", "operator.example.json"))
	var c Config
	if err != nil || json.Unmarshal(data, &c) != nil {
		t.Fatal("read example policy", err)
	}
	c.RetainedPublicationTransition = &RetainedPublicationTransition{
		PublicationTransition: *exactPublicationTransition(),
		PreviousRunID:         "previous",
		PreviousKey:           "published",
	}
	file := filepath.Join(t.TempDir(), "operator.json")
	if err := writeJSON(file, c); err != nil {
		t.Fatal(err)
	}
	before, err := Load(file)
	if err != nil || before.RetainedPublicationFollowup != nil {
		t.Fatal("old policy stopped loading", err)
	}
	c.RetainedPublicationFollowup = &RetainedPublicationFollowup{
		RetainedPublicationTransition: *c.RetainedPublicationTransition,
		ConsumedRunID:                 "consumed",
		ConsumedKey:                   "first_adoption",
		ConsumedPolicy:                before.PolicyHash,
	}
	if err := writeJSON(file, c); err != nil {
		t.Fatal(err)
	}
	after, err := Load(file)
	canonical, _ := json.Marshal(c)
	if err != nil || after.PolicyHash != Digest(canonical) || after.PolicyHash == before.PolicyHash ||
		!reflect.DeepEqual(after.RetainedPublicationTransition, before.RetainedPublicationTransition) ||
		!reflect.DeepEqual(after.RetainedPublicationFollowup, c.RetainedPublicationFollowup) {
		t.Fatal("valid follow-up did not preserve the sibling and alter the canonical policy", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"no original", func(c *Config) { c.RetainedPublicationTransition = nil }},
		{"other publication", func(c *Config) { c.RetainedPublicationFollowup.PreviousKey = "another" }},
		{"other head", func(c *Config) { c.RetainedPublicationFollowup.Head = strings.Repeat("c", 40) }},
		{"consumed predecessor run", func(c *Config) { c.RetainedPublicationFollowup.ConsumedRunID = "previous" }},
		{"consumed publication key", func(c *Config) { c.RetainedPublicationFollowup.ConsumedKey = "published" }},
		{"invalid consumed policy", func(c *Config) { c.RetainedPublicationFollowup.ConsumedPolicy = "invalid" }},
		{"legacy grant overlaps", func(c *Config) { c.PublicationTransition = exactPublicationTransition() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var altered Config
			if err := json.Unmarshal(canonical, &altered); err != nil {
				t.Fatal(err)
			}
			tc.change(&altered)
			if err := writeJSON(file, altered); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(file); err == nil {
				t.Fatal("invalid follow-up policy was accepted")
			}
		})
	}
}
