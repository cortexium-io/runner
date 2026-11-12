package execution

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Model-free topology proof: independent roots require independent instances,
// not task_concurrency2 (which is only sibling cards inside one active family).
func TestSeparateInstancesProgressWithDisjointSourceStateAndCleanup(t *testing.T) {
	repo1, repo2 := sourceRepository(t), sourceRepository(t)
	cfg1, cfg2 := workspaceSnapshot(t, repo1), workspaceSnapshot(t, repo2)
	if cfg1.Instance.Concurrency != 1 || cfg2.Instance.Concurrency != 1 {
		t.Fatal("fixture must preserve one active root per instance")
	}
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	first := &scriptedPeer{turn: goodTurn(), gate: gate, entered: entered}
	second := &scriptedPeer{turn: goodTurn()}
	write := func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
			t.Error(err)
		}
	}
	first.onTurn, second.onTurn = write, write
	r1 := openNativeRig(t, filepath.Join(t.TempDir(), "state-one"), cfg1, first)
	defer r1.close()
	r2 := openNativeRig(t, filepath.Join(t.TempDir(), "state-two"), cfg2, second)
	secondClosed := false
	defer func() {
		if !secondClosed {
			r2.close()
		}
	}()
	a, err := r1.core.Start(runner.StartRequest{RequestID: "held_instance", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first owned source did not reach held readiness")
	}
	w1, err := r1.service.workspaces.load(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(w1.WorkDir, "doc.go"))
	if err != nil {
		t.Fatal(err)
	}
	lock1, err := os.Stat(filepath.Join(r1.disk.Root, "instance.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock2, err := os.Stat(filepath.Join(r2.disk.Root, "instance.lock"))
	if err != nil || os.SameFile(lock1, lock2) {
		t.Fatal("instances share owning lease", err)
	}
	if strings.HasPrefix(w1.WorkDir, r2.disk.Root+string(os.PathSeparator)) {
		t.Fatal("first workspace belongs to second instance")
	}
	b, err := r2.core.Start(runner.StartRequest{RequestID: "independent_instance", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	done := waitSourceRun(t, r2.core, b.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) }, 10*time.Second)
	if done.Status != "Completed" {
		t.Fatal("second root did not progress independently", done.Status, done.Reason)
	}
	r2.close()
	secondClosed = true
	live, err := r1.core.Get(a.ID)
	if err != nil || live.Terminal() {
		t.Fatal("independent cleanup changed held root", live, err)
	}
	after, err := os.ReadFile(filepath.Join(w1.WorkDir, "doc.go"))
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("second cleanup touched first owned source", err)
	}
	release()
	done = waitSourceRun(t, r1.core, a.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) }, 10*time.Second)
	if done.Status != "Completed" {
		t.Fatal(done.Status, done.Reason)
	}
}
