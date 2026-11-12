package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestUnpublishedCardRecoveryAtomicAuditAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, e := Open(dir, normalizedPolicy())
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	root := normalizedRun(5)
	root.ID = "root"
	root.Status = "Failed"
	child := normalizedRun(2)
	child.ID = "child"
	for i := range child.Attempts {
		child.Attempts[i].ID = "child_" + child.Attempts[i].ID
	}
	child.Status = "Failed"
	child.Task = &runner.TaskRunLink{Identity: runner.TaskIdentity{RootRunID: root.ID}}
	for _, r := range []runner.Run{root, child} {
		if e = s.Save(t.Context(), r, nil, nil); e != nil {
			t.Fatal(e)
		}
	}
	before, e := s.Load(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	receipt := &runner.Receipt{Key: "one-use", Kind: "recover_unpublished_card", Args: json.RawMessage(`{"exact":true}`), RunID: root.ID}
	root.RecordFormat = 4
	root.UnpublishedCardRecovery = &runner.UnpublishedCardRecovery{Request: runner.RecoverUnpublishedCardRequest{RequestID: receipt.Key, RunID: root.ID, ChildRunID: child.ID}, OriginalRootReason: "deadline", OwnerVisit: 2, Spent: 28, ReservedCompletion: 23}
	root.Status = "Running"
	child.Status = "Queued"
	child.Visit++
	// Fail the second save after the child write, proving transactional rollback.
	broken := runner.Clone(root)
	broken.Reason = strings.Repeat("x", int(s.policy.Database.MaxRecord)+1)
	if e = s.SaveUnpublishedCardRecovery(t.Context(), broken, child, receipt); e == nil {
		t.Fatal("second-write failure committed")
	}
	after, e := s.Load(t.Context())
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("partial recovery persisted", e)
	}
	if _, e = s.FindReceipt(t.Context(), receipt.Key); e == nil {
		t.Fatal("receipt survived rollback")
	}
	if e = s.SaveUnpublishedCardRecovery(t.Context(), root, child, receipt); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"downgrade", "drop", "rewrite"} {
		bad := runner.Clone(root)
		switch kind {
		case "downgrade":
			bad.RecordFormat = 3
		case "drop":
			bad.UnpublishedCardRecovery = nil
		case "rewrite":
			bad.UnpublishedCardRecovery.OwnerVisit++
		}
		if e = s.Save(t.Context(), bad, nil, nil); e == nil {
			t.Fatal("audit could be lost", kind)
		}
	}
	s.Close()
	s = nil
	// Optional actual installed-reader compatibility proof uses ONLY this
	// disposable fixture. There are no active statuses or runnable product jobs.
	if old := os.Getenv("RUNNER_CARD_RECOVERY_OLD_READER"); old != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		probe := exec.CommandContext(ctx, old, "serve", "--config", filepath.Join("..", "..", "..", "examples", "demo", "instance.yaml"), "--data", dir, "--listen", "127.0.0.1:0")
		out, err := probe.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "unsupported run record format") {
			t.Fatalf("old reader did not refuse before dispatch: %s %v", out, err)
		}
	}
	s, e = Open(dir, normalizedPolicy())
	if e != nil {
		t.Fatal(e)
	}
	after, e = s.Load(t.Context())
	if e != nil || len(after) != 2 {
		t.Fatal(e)
	}
	for _, r := range after {
		want := root
		if r.ID == child.ID {
			want = child
		}
		if !reflect.DeepEqual(r, want) {
			t.Fatal("restart lost history or grant", r.ID)
		}
	}
	got, e := s.FindReceipt(t.Context(), receipt.Key)
	if e != nil || !reflect.DeepEqual(got, *receipt) {
		t.Fatal("one-use receipt lost", got, e)
	}
	var version int
	if e = s.db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 3 {
		t.Fatal("schema changed", version, e)
	}
	// Keep an owned closed fixture only when explicitly requested for old-reader
	// qualification; normal tests clean their temporary state automatically.
	if dest := os.Getenv("RUNNER_CARD_RECOVERY_FIXTURE"); dest != "" {
		s.Close()
		s = nil
		if e = os.MkdirAll(dest, 0700); e != nil {
			t.Fatal(e)
		}
		b, err := os.ReadFile(filepath.Join(dir, "process-core.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		if e = os.WriteFile(filepath.Join(dest, "process-core.sqlite"), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
