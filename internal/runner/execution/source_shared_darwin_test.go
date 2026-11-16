//go:build darwin

package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func sourceSharedNode(t *testing.T) string {
	t.Helper()
	node := os.Getenv("RUNNER_SOURCE_SHARED_NODE")
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Skip("Node is required for source-owned sandbox qualification")
		}
	}
	canonical, err := filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestSourceSharedCommandRejoinsSameWorkerAndCleansOwnedScratch(t *testing.T) {
	s, a := sourceSharedFixture(t)
	a.Setup.Profile.Command.Args[0] = sourceSharedNode(t)
	a.Setup.Profile.Command.Env = map[string]string{"RUNNER_SOURCE_PROOF_SYNC": "1"}
	events, gate := make(chan byte, 1), make(chan *os.File, 1)
	workerDone := make(chan error, 1)
	observer, finishedWorker := s, false
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = observer.Stop(ctx, a.AttemptID)
		if !finishedWorker {
			select {
			case <-workerDone:
			case <-ctx.Done():
				t.Error("source verifier cleanup did not settle")
			}
		}
		if o, err := observer.Observe(ctx, a.AttemptID); err == nil && o.End != nil {
			_ = observer.Release(ctx, a.AttemptID)
		}
	}()
	s.harnesses.launchWorker = func(spec string, finished func()) error {
		for _, name := range []string{"events", "gate"} {
			if err := syscall.Mkfifo(filepath.Join(filepath.Dir(spec), "tmp", name), 0600); err != nil {
				return err
			}
		}
		ready, err := os.OpenFile(filepath.Join(filepath.Dir(spec), "tmp", "events"), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		resume, err := os.OpenFile(filepath.Join(filepath.Dir(spec), "tmp", "gate"), os.O_RDWR, 0)
		if err != nil {
			ready.Close()
			return err
		}
		t.Cleanup(func() { ready.Close(); resume.Close() })
		gate <- resume
		go func() { var b [1]byte; _, _ = ready.Read(b[:]); events <- b[0] }()
		go func() { workerDone <- RunNativeWorker(context.Background(), spec); finished() }()
		return nil
	}
	changed := make(chan string, 8)
	s.SetWake(func(id string) { changed <- id })
	if _, err := s.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event != 'R' {
			t.Fatal("source command readiness failed", event)
		}
	case <-time.After(10 * time.Second):
		_ = s.Stop(t.Context(), a.AttemptID)
		t.Fatal("source command did not reach deterministic readiness")
	}
	old, err := s.harnesses.load(a.AttemptID)
	if err != nil || old.Workspace == nil || old.Directory != filepath.Join(s.disk.Root, "work", a.RunID, "source") {
		t.Fatal("source command did not retain exact owned workspace", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened := NewService(s.disk, nil, nil)
	observer = reopened
	defer reopened.Close(context.Background())
	reopened.SetWake(func(id string) { changed <- id })
	reopened.harnesses.launchWorker = func(string, func()) error {
		t.Error("recovery started a replacement source verifier")
		return errors.New("replacement forbidden")
	}
	if _, err := reopened.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := RunNativeWorker(t.Context(), filepath.Join(s.disk.Root, harnessControl(a.AttemptID), "spec.json")); err == nil {
		t.Fatal("the active worker's immutable claim allowed a duplicate")
	}
	resume := <-gate
	if _, err := resume.Write([]byte{'G'}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-workerDone:
		finishedWorker = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		_ = reopened.Stop(t.Context(), a.AttemptID)
		t.Fatal("same source command did not finish")
	}
	o := waitSourceShared(t, reopened, a.AttemptID, changed)
	sourceSharedReport(t, reopened, o)
	if err := reopened.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.ScratchDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source command retained disposable scratch", err)
	}
	if _, err := s.disk.Read("artifacts/"+a.Inputs["patch"].ArtifactID, a.Limits.MaxArtifact); err != nil {
		t.Fatal("read-only source check removed candidate proof", err)
	}
	if bytes, err := os.ReadFile(filepath.Join(old.Directory, "proof.html")); err != nil || !strings.Contains(string(bytes), "Current captured source") {
		t.Fatal("read-only verification changed or deleted the owned source", err)
	}
}

func TestSourceSharedCommandPinnedHeadlessCFT(t *testing.T) {
	if os.Getenv("RUNNER_SOURCE_SHARED_BROWSER") != "1" {
		t.Skip("set RUNNER_SOURCE_SHARED_BROWSER=1 for installed pinned CFT qualification")
	}
	s, a := sourceSharedFixture(t)
	a.Setup.Profile.Command.Args[0] = sourceSharedNode(t)
	a.Setup.Profile.Command.Network = true // Explicit broad grant, not a loopback-only security claim.
	a.Setup.Profile.Command.Env = map[string]string{
		"RUNNER_SOURCE_PROOF_PLAYWRIGHT": "/Users/dan/.codex/tools/browser-runtime/node_modules/playwright/index.mjs",
		"RUNNER_SOURCE_PROOF_CHROME":     "/Users/dan/.codex/tools/browser-runtime/chrome",
	}
	done := make(chan error, 1)
	s.harnesses.launchWorker = func(file string, finished func()) error {
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	changed := make(chan string, 8)
	s.SetWake(func(id string) { changed <- id })
	if _, err := s.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	o := waitSourceShared(t, s, a.AttemptID, changed)
	report := sourceSharedReport(t, s, o)
	if len(report["images"].([]any)) != 2 || report["browser"] == "not_requested" {
		t.Fatal("source-bound phone/desktop CFT proof is missing", report)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	job, err := s.harnesses.load(a.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"phone.png", "desktop.png"} {
		if bytes, err := os.ReadFile(filepath.Join(job.ScratchDir, name)); err != nil || len(bytes) < 100 {
			t.Fatal("CFT did not capture the actual viewport", name, err)
		}
	}
	if err := s.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job.ScratchDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("CFT source fixture scratch survived cleanup", err)
	}
}

func TestSourceSharedWorkerRejectsUnownedDirectory(t *testing.T) {
	s, a := sourceSharedFixture(t)
	spec := harnessSpec{Root: s.disk.Root, Directory: s.disk.Root, Assignment: a}
	if _, err := sharedProcess(t.Context(), spec); err == nil {
		t.Fatal("source command accepted a coordinator-root working directory")
	}
}
