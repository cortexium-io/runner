package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSerialCardRecoveryAtomicFamilyAndReaderBoundary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	root, child := normalizedRun(4), normalizedRun(2)
	root.ID, root.Status = "serial-root", "Failed"
	child.ID, child.Status = "serial-child", "Failed"
	child.Task = &runner.TaskRunLink{Identity: runner.TaskIdentity{RootRunID: root.ID}}
	for i := range child.Attempts {
		child.Attempts[i].ID = "serial-child-" + child.Attempts[i].ID
	}
	for _, r := range []runner.Run{root, child} {
		if err = s.Save(t.Context(), r, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	var pending []runner.Run
	var originals []runner.UnstartedCardRecovery
	for i := range 5 {
		r := normalizedRun(0)
		r.ID, r.Revision = fmt.Sprintf("serial-pending-%d", i), 3
		r.Task = &runner.TaskRunLink{Identity: runner.TaskIdentity{
			RootRunID: root.ID,
			GroupStep: "cards",
			ItemID:    r.ID,
		}}
		r.Status, r.Reason = "Cancelled", "parent task group is stopping"
		r.CancelRequested, r.ResourcesReleased = true, true
		if err = s.Save(t.Context(), r, nil, nil); err != nil {
			t.Fatal(err)
		}
		originals = append(originals, runner.UnstartedCardRecovery{RunID: r.ID, Revision: r.Revision, Updated: r.Updated})
		r.Status, r.Reason = "Waiting", "waiting for declared task prerequisites"
		r.CancelRequested, r.ResourcesReleased = false, false
		r.Revision++
		pending = append(pending, r)
	}
	before, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	receipt := &runner.Receipt{
		Key:   "serial-one-use",
		Kind:  "recover_unpublished_card",
		Args:  json.RawMessage(`{"serial":true}`),
		RunID: root.ID,
	}
	root.RecordFormat, root.Status = 6, "Running"
	root.UnpublishedCardRecovery = &runner.UnpublishedCardRecovery{
		Request: runner.RecoverUnpublishedCardRequest{
			RequestID:  receipt.Key,
			RunID:      root.ID,
			ChildRunID: child.ID,
		},
		Unstarted:          originals,
		OwnerVisit:         2,
		Spent:              6,
		ReservedCompletion: 38,
	}
	child.Status, child.Visit = "Queued", child.Visit+1
	broken := runner.Clone(root)
	broken.Reason = strings.Repeat("x", int(s.policy.Database.MaxRecord)+1)
	if err = s.SaveUnpublishedCardRecovery(t.Context(), broken, child, pending, receipt); err == nil {
		t.Fatal("root failure committed the preceding six child writes")
	}
	after, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("partial serial grant survived rollback", err)
	}
	if _, err = s.FindReceipt(t.Context(), receipt.Key); err == nil {
		t.Fatal("uncommitted grant retained its receipt")
	}
	if err = s.SaveUnpublishedCardRecovery(t.Context(), root, child, pending, receipt); err != nil {
		t.Fatal(err)
	}
	bad := runner.Clone(root)
	bad.UnpublishedCardRecovery.Unstarted[0].Revision++
	if err = s.Save(t.Context(), bad, nil, nil); err == nil {
		t.Fatal("original cancellation audit could be rewritten")
	}
	s.Close()
	s = nil
	// The real previous reader sees only this disposable fixture and must refuse
	// the new audit before any worker can dispatch. No product run is involved.
	if old := os.Getenv("RUNNER_SERIAL_CARD_RECOVERY_OLD_READER"); old != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		probe := exec.CommandContext(ctx, old, "serve", "--config", filepath.Join("..", "..", "..", "examples", "demo", "instance.yaml"), "--data", dir, "--listen", "127.0.0.1:0")
		out, err := probe.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "unsupported run record format") {
			t.Fatalf("previous reader did not refuse before dispatch: %s %v", out, err)
		}
	}
	s, err = Open(dir, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	after, err = s.Load(t.Context())
	if err != nil || len(after) != 7 {
		t.Fatal("restart lost serial family", err)
	}
	want := map[string]runner.Run{root.ID: root, child.ID: child}
	for _, r := range pending {
		want[r.ID] = r
	}
	for _, r := range after {
		if !reflect.DeepEqual(r, want[r.ID]) {
			t.Fatal("restart changed atomic serial grant or history", r.ID)
		}
	}
	got, err := s.FindReceipt(t.Context(), receipt.Key)
	if err != nil || !reflect.DeepEqual(got, *receipt) {
		t.Fatal("serial recovery receipt lost", err)
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 19 {
		t.Fatal("schema changed", version, err)
	}
}
