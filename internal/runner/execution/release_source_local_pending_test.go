//go:build darwin || linux

package execution

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestReadinessLocalGitDeadlineRejoinsSamePreparation(t *testing.T) {
	r, j := pendingSourceJob(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ready, hold := filepath.Join(dir, "ready"), filepath.Join(dir, "hold")
	for _, file := range []string{ready, hold} {
		if out, err := exec.Command("mkfifo", file).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	reader, err := os.OpenFile(ready, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := os.OpenFile(hold, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	script := "#!/bin/sh\ncase \" $* \" in *' update-ref '*)\n if [ ! -e " + testShellQuote(filepath.Join(dir, "blocked")) + " ]; then\n  : > " + testShellQuote(filepath.Join(dir, "blocked")) + "\n  printf 'local-ref\\n' > " + testShellQuote(ready) + "\n  IFS= read -r release < " + testShellQuote(hold) + "\n fi;;\nesac\nexec " + testShellQuote(real) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	entered := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(reader).ReadString('\n'); entered <- strings.TrimSpace(line) }()
	base, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	call := captureCutoffContext{base}
	done := make(chan runner.Observation, 1)
	go func() {
		o, e := r.Observe(call, j.Assignment.AttemptID)
		if e != nil {
			t.Error(e)
		}
		done <- o
	}()
	select {
	case stage := <-entered:
		if stage != "local-ref" {
			t.Fatal(stage)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local preparation did not reach update-ref")
	}
	cancel(context.DeadlineExceeded)
	var o runner.Observation
	select {
	case o = <-done:
	case <-time.After(time.Second):
		t.Fatal("local Git did not settle after observation expiry")
	}
	if o.State != runner.Unknown || !o.Reconciling || o.End != nil || !strings.Contains(o.Reason, "git update-ref:") {
		t.Fatal("local deadline became an owner blocker", o)
	}
	saved, err := r.load(j.Assignment.AttemptID)
	if err != nil || saved.SourcePrepared || saved.End != nil || saved.SelectedBase != j.SelectedBase || saved.Assignment.AttemptID != j.Assignment.AttemptID {
		t.Fatal("local deadline changed saved source intent", saved, err)
	}
	o, err = r.Observe(t.Context(), j.Assignment.AttemptID)
	if err != nil || o.State != runner.Running || o.Reconciling || o.Source == nil || o.Source.Base != j.SelectedBase {
		t.Fatal("same local source preparation did not resume", o, err)
	}
}

func TestReadinessLocalIntegrityFailureRemainsActionable(t *testing.T) {
	r, j := pendingSourceJob(t)
	w, err := r.workspaces.Prepare(t.Context(), j.Assignment)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(w.WorkDir, ".git")
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(t.TempDir(), "unowned"), link); err != nil {
		t.Fatal(err)
	}
	o, err := r.Observe(t.Context(), j.Assignment.AttemptID)
	if err != nil || o.State != runner.Unknown || o.Reconciling || o.End != nil || !strings.Contains(o.Reason, "Git link") {
		t.Fatal("integrity failure was hidden as transient preparation", o, err)
	}
}
