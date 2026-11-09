package release

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdoptionRequiresAcceptedPublicationAndUnchangedOpenPR(t *testing.T) {
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	dir := filepath.Join(t.TempDir(), "journal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := Receipt{Key: "published", RunID: "original", Action: "publish", Policy: "policy", Source: base, Base: base, Head: head, PR: 332}
	saved := Job{Request: Request{Project: "demo", RunID: p.RunID, Key: p.Key, Action: "publish", Source: base, Candidate: head}, Receipt: p, Done: true}
	file := filepath.Join(dir, p.Key+".json")
	if err := writeJSON(file, saved); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(file)
	currentHead := head
	b := &Live{Config: Config{Repository: "owner/repo", Branch: "main", Delivery: &DeliveryConfig{}}}
	b.runCommand = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return json.Marshal(map[string]any{"state": "open", "merged": false, "head": map[string]string{"sha": currentHead}, "base": map[string]string{"sha": base, "ref": "main"}})
	}
	o := Operator{Directory: dir, PolicyHash: "policy", Project: "demo", Backend: b, Delivery: true}
	handle, _ := json.Marshal(publicReceipt(p, true))
	q := Request{Project: "demo", RunID: "resumed", Key: "adopt", Action: "adopt", Source: base, Previous: handle}
	forged := p
	forged.Head = strings.Repeat("c", 40)
	bad := q
	bad.Previous, _ = json.Marshal(publicReceipt(forged, true))
	if _, err := o.Call(t.Context(), bad); err == nil {
		t.Fatal("forged reviewed head adopted")
	}
	r, err := o.Call(t.Context(), q)
	if err != nil || r.State != "finished" || r.Failed {
		t.Fatal("accepted publication could not be adopted", r, err)
	}
	var adopted Receipt
	if json.Unmarshal(r.Receipt, &adopted) != nil || adopted.Action != "adopt" || adopted.RunID != q.RunID || adopted.Head != head {
		t.Fatal("adoption lost immutable source identity", adopted)
	}
	after, _ := os.ReadFile(file)
	if string(after) != string(original) {
		t.Fatal("adoption rewrote original history")
	}
	currentHead = strings.Repeat("d", 40)
	q.Key = "changed_head"
	r, err = o.Call(t.Context(), q)
	if err != nil || !r.Failed {
		t.Fatal("changed PR was adopted", r, err)
	}
	o.Backend = &scriptedBackend{calls: map[string]int{}}
	_, err = o.Call(t.Context(), Request{Project: "demo", RunID: "wrong_run", Key: "wrong_qualify", Action: "qualify", Source: base, Previous: handle})
	if err == nil {
		t.Fatal("cross-run qualification bypassed adoption")
	}
	r, err = o.Call(t.Context(), Request{Project: "demo", RunID: "resumed", Key: "qualify", Action: "qualify", Source: base, Previous: mustJSON(t, adopted)})
	if err != nil || r.State != "finished" {
		t.Fatal("adopted publication could not reach qualification", r, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
