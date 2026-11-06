package release

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	transitionOldPolicy = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	transitionHead      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	transitionBase      = "ffffffffffffffffffffffffffffffffffffffff"
)

func TestPublicationTransitionLegacyCanonicalHash(t *testing.T) {
	canonical, err := json.Marshal(Config{Project: "legacy", Repository: "owner/repo", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	// Recorded against Config before publication_transition was introduced.
	if Digest(canonical) != "91f402e79b6e3c0f4216043f2bb5ba32b3017f25c1b499e6be3a41720ef294da" || bytes.Contains(canonical, []byte("publication_transition")) {
		t.Fatal("absent transition changed existing canonical policy identity", string(canonical))
	}
}

func exactPublicationTransition() *PublicationTransition {
	return &PublicationTransition{
		PreviousPolicy: transitionOldPolicy,
		PR:             42,
		Head:           transitionHead,
		Base:           transitionBase,
	}
}

func transitionFixture(t *testing.T) (*Operator, Job, Request, *int) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := Receipt{
		Key:    "attempt_DDDDDDDDDDDDDDDDDDDDDDDDDD",
		RunID:  "run_EEEEEEEEEEEEEEEEEEEEEEEEEE",
		Action: "publish",
		Policy: transitionOldPolicy,
		Head:   transitionHead,
		Base:   transitionBase,
		Source: transitionBase,
		PR:     42,
	}
	saved := Job{
		Request: Request{Project: "demo", Key: p.Key, RunID: p.RunID, Action: "publish", Source: p.Source, Candidate: p.Head},
		Receipt: p,
		Done:    true,
	}
	if err := writeJSON(filepath.Join(dir, p.Key+".json"), saved); err != nil {
		t.Fatal(err)
	}
	reads := new(int)
	b := &Live{Config: Config{Repository: "owner/repo", Branch: "main", Delivery: &DeliveryConfig{}}}
	b.runCommand = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		*reads++
		if len(args) != 4 || args[3] != "repos/owner/repo/pulls/42" {
			t.Fatalf("adoption exceeded its read-only PR identity query: %q", args)
		}
		return json.Marshal(map[string]any{"state": "open", "merged": false, "head": map[string]string{"sha": p.Head}, "base": map[string]string{"sha": p.Base, "ref": "main"}})
	}
	o := &Operator{Directory: dir, PolicyHash: strings.Repeat("e", 64), Project: "demo", Backend: b, Delivery: true, PublicationTransition: exactPublicationTransition()}
	handle := publicReceipt(p, true)
	if handle.PR != 0 || handle.Base != "" {
		t.Fatal("fixture must reproduce the compact publication handle")
	}
	q := Request{Project: "demo", Key: "transition_adopt", RunID: "new_release", Action: "adopt", Source: transitionBase, Previous: mustJSON(t, handle)}
	return o, saved, q, reads
}

func TestPublicationTransitionAuthenticatesRealHandleAndStampsCurrentPolicy(t *testing.T) {
	o, saved, q, reads := transitionFixture(t)
	oldFile := filepath.Join(o.Directory, saved.Request.Key+".json")
	before, _ := os.ReadFile(oldFile)
	response, err := o.Call(t.Context(), q)
	if err != nil || response.State != "finished" || response.Failed || *reads != 1 {
		t.Fatal("exact old publication could not enter current-policy adoption", response, err)
	}
	var adopted Receipt
	if json.Unmarshal(response.Receipt, &adopted) != nil || adopted.Policy != o.PolicyHash || adopted.Action != "adopt" || adopted.RunID != q.RunID || adopted.Head != transitionHead {
		t.Fatal("adoption carried old policy authority forward", adopted)
	}
	data, _ := os.ReadFile(filepath.Join(o.Directory, q.Key+".json"))
	var journal Job
	if json.Unmarshal(data, &journal) != nil || journal.Receipt.PR != 42 || journal.Receipt.Base != transitionBase || journal.Receipt.Policy != o.PolicyHash {
		t.Fatal("new private adoption lost the bound publication identity", string(data))
	}
	if _, err := o.Call(t.Context(), q); err != nil || *reads != 1 {
		t.Fatal("same-key adoption repeated an accepted operation", err)
	}
	after, _ := os.ReadFile(oldFile)
	if !bytes.Equal(before, after) {
		t.Fatal("policy transition rewrote the old publication/history")
	}
	backend := &scriptedBackend{calls: map[string]int{}}
	o.Backend = backend
	fresh, err := o.Call(t.Context(), Request{Project: q.Project, RunID: q.RunID, Key: "new_qualification", Action: "qualify", Source: q.Source, Previous: response.Receipt})
	if err != nil || fresh.State != "finished" || fresh.Failed || backend.calls["qualify"] != 1 {
		t.Fatal("new adoption did not require a current-policy qualification action", fresh, err)
	}
}

func TestPublicationTransitionRefusesDifferentOrUnauthenticatedPublication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Operator, *Job, *Request)
	}{
		{"no bridge", func(o *Operator, _ *Job, _ *Request) { o.PublicationTransition = nil }},
		{"old policy", func(o *Operator, _ *Job, _ *Request) {
			o.PublicationTransition.PreviousPolicy = strings.Repeat("c", 64)
		}},
		{"PR", func(_ *Operator, j *Job, _ *Request) { j.Receipt.PR++ }},
		{"private base", func(_ *Operator, j *Job, _ *Request) { j.Receipt.Base = strings.Repeat("b", 40) }},
		{"head", func(o *Operator, _ *Job, _ *Request) { o.PublicationTransition.Head = strings.Repeat("c", 40) }},
		{"source", func(_ *Operator, _ *Job, q *Request) { q.Source = strings.Repeat("c", 40) }},
		{"request key", func(_ *Operator, j *Job, _ *Request) { j.Request.Key = "other" }},
		{"request run", func(_ *Operator, j *Job, _ *Request) { j.Request.RunID = "other" }},
		{"request source", func(_ *Operator, j *Job, _ *Request) { j.Request.Source = strings.Repeat("c", 40) }},
		{"request candidate", func(_ *Operator, j *Job, _ *Request) { j.Request.Candidate = strings.Repeat("c", 40) }},
		{"unsettled journal", func(_ *Operator, j *Job, _ *Request) { j.Done = false }},
		{"failed journal", func(_ *Operator, j *Job, _ *Request) { j.Failed = true }},
		{"unaccepted outcome", func(_ *Operator, j *Job, _ *Request) { j.Outcome = "changes_requested" }},
		{"forged handle run", func(_ *Operator, _ *Job, q *Request) {
			var p Receipt
			_ = json.Unmarshal(q.Previous, &p)
			p.RunID = "forged"
			q.Previous = mustJSON(t, p)
		}},
		{"forged current policy", func(o *Operator, _ *Job, q *Request) {
			var p Receipt
			_ = json.Unmarshal(q.Previous, &p)
			p.Policy = o.PolicyHash
			q.Previous = mustJSON(t, p)
		}},
		{"forged handle key", func(_ *Operator, _ *Job, q *Request) {
			var p Receipt
			_ = json.Unmarshal(q.Previous, &p)
			p.Key = "forged"
			q.Previous = mustJSON(t, p)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, saved, q, reads := transitionFixture(t)
			originalKey := saved.Request.Key
			tc.change(o, &saved, &q)
			if err := writeJSON(filepath.Join(o.Directory, originalKey+".json"), saved); err != nil {
				t.Fatal(err)
			}
			if _, err := o.Call(t.Context(), q); err == nil || *reads != 0 {
				t.Fatal("invalid bridge reached live adoption or was accepted", err, *reads)
			}
		})
	}
}

func TestPublicationTransitionDoesNotAuthorizeOtherOldPolicyActions(t *testing.T) {
	for _, tc := range []struct{ action, previous string }{
		{"adopt", "adopt"}, {"qualify", "publish"}, {"prepare", "qualify"}, {"merge", "prepare"}, {"stop", "backup"},
		{"migrate", "stop"}, {"deploy", "pin"}, {"inspect", "deploy"}, {"restore", "inspect-migration"}, {"recover-stop", "stop"},
	} {
		t.Run(tc.action+"/"+tc.previous, func(t *testing.T) {
			o, saved, q, reads := transitionFixture(t)
			saved.Request.Action, saved.Receipt.Action = tc.previous, tc.previous
			if err := writeJSON(filepath.Join(o.Directory, saved.Request.Key+".json"), saved); err != nil {
				t.Fatal(err)
			}
			q.Action, q.RunID = tc.action, saved.Request.RunID
			q.Previous = mustJSON(t, publicReceipt(saved.Receipt, true))
			if _, err := o.Call(t.Context(), q); err == nil || *reads != 0 {
				t.Fatal("old policy bypassed current qualification/release boundaries", err)
			}
		})
	}
	o, saved, _, reads := transitionFixture(t)
	if _, err := o.Call(t.Context(), saved.Request); err == nil || *reads != 0 {
		t.Fatal("bridge reconciled an old-policy publish operation", err)
	}
}

func TestPublicationTransitionStillRequiresUnchangedLiveOpenPR(t *testing.T) {
	for _, kind := range []string{"head", "base", "branch", "closed", "merged"} {
		t.Run(kind, func(t *testing.T) {
			o, _, q, _ := transitionFixture(t)
			o.Backend.(*Live).runCommand = func(context.Context, []byte, string, ...string) ([]byte, error) {
				state, head, base, branch, merged := "open", transitionHead, transitionBase, "main", false
				switch kind {
				case "head":
					head = strings.Repeat("c", 40)
				case "base":
					base = strings.Repeat("c", 40)
				case "branch":
					branch = "other"
				case "closed":
					state = "closed"
				case "merged":
					merged = true
				}
				return json.Marshal(map[string]any{"state": state, "merged": merged, "head": map[string]string{"sha": head}, "base": map[string]string{"sha": base, "ref": branch}})
			}
			response, err := o.Call(t.Context(), q)
			if err != nil || response.State != "finished" || !response.Failed {
				t.Fatal("policy bridge bypassed live publication identity proof", response, err)
			}
		})
	}
}

func TestPublicationTransitionLoadAndValidation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "jacks-break", "operator.example.json"))
	var c Config
	if err != nil || json.Unmarshal(data, &c) != nil {
		t.Fatal(err)
	}
	c.PublicationTransition = exactPublicationTransition()
	file := filepath.Join(t.TempDir(), "operator.json")
	if err := writeJSON(file, c); err != nil {
		t.Fatal(err)
	}
	o, err := Load(file)
	canonical, _ := json.Marshal(c)
	if err != nil || !reflect.DeepEqual(o.PublicationTransition, c.PublicationTransition) || o.PolicyHash != Digest(canonical) {
		t.Fatal("loaded transition was not bound into the current policy", err)
	}
	for _, kind := range []string{"policy", "PR", "head", "base", "non-delivery"} {
		t.Run(kind, func(t *testing.T) {
			p := *exactPublicationTransition()
			delivery := true
			switch kind {
			case "policy":
				p.PreviousPolicy = "invalid"
			case "PR":
				p.PR = 0
			case "head":
				p.Head = "invalid"
			case "base":
				p.Base = "invalid"
			case "non-delivery":
				delivery = false
			}
			if err := p.validate(delivery); err == nil {
				t.Fatal("invalid or non-delivery transition accepted")
			}
		})
	}
}
