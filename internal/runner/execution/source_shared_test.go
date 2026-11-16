//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func sourceSharedFixture(t *testing.T) (*Service, runner.Assignment) {
	t.Helper()
	d := workspaceDisk(t)
	s := NewService(d, func(_ context.Context, id string) ([]byte, error) {
		return d.Read("artifacts/"+id, d.Policy.MaxArtifact)
	}, nil)
	t.Cleanup(func() { s.Close(context.Background()) })
	a := sourceAssignment(d, sourceRepository(t), "attempt_source_check")
	a.Deadline = time.Now().Add(30 * time.Second)
	writer := runner.Clone(a)
	writer.AttemptID = "attempt_source_writer"
	writer.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	w, err := s.workspaces.Prepare(t.Context(), writer)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"proof.html", "verify.mjs"} {
		bytes, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "source-shared", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.WorkDir, name), bytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := s.workspaces.Capture(t.Context(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Write("work/other-state/secret", []byte("ungranted coordinator state"), 1024); err != nil {
		t.Fatal(err)
	}
	node := "/usr/bin/true"
	if selected := os.Getenv("RUNNER_SOURCE_SHARED_NODE"); selected != "" {
		node = selected
	}
	a.Setup.Profile = runner.Profile{
		Harness: "command", Mode: "shared", Access: "workspace-read",
		Command: &runner.Command{
			Args:     []string{node, "{workspace}/verify.mjs", "{workspace}", "{scratch}", "{artifact.patch}", filepath.Join(d.Root, "work", "other-state", "secret")},
			Artifact: "report.json", Result: "domain.json",
		},
	}
	a.OutputTypes = map[string]string{"check": "text", "artifact": "artifact"}
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: candidate.PatchID}}
	return s, a
}

func TestSourceSharedAssignmentRequiresCurrentSourceAndGeneralBindingsStayScoped(t *testing.T) {
	s, original := sourceSharedFixture(t)
	for _, kind := range []string{"valid", "missing source", "missing patch", "inline patch", "source write", "goal", "dependency"} {
		t.Run(kind, func(t *testing.T) {
			a := runner.Clone(original)
			switch kind {
			case "missing source":
				a.Source = nil
			case "missing patch":
				delete(a.Inputs, "patch")
			case "inline patch":
				a.Inputs["patch"] = runner.Text("forged/patch")
			case "source write":
				a.Setup.Profile.Access = "workspace-write"
			case "goal":
				a.Setup.Profile.Command.Args = append(a.Setup.Profile.Command.Args, "{goal}")
			case "dependency":
				a.Setup.Profile.Command.Args = append(a.Setup.Profile.Command.Args, "{dependency.import.artifact}")
			}
			prepared, err := sharedAssignment(a)
			if (err == nil) != (kind == "valid") || err == nil && prepared.TaskContext != nil {
				t.Fatal("source shared admission changed task authority", err)
			}
		})
	}
	if _, err := s.workspaces.load(original.RunID); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSharedPreparationRejectsStalePatchBeforeDispatch(t *testing.T) {
	s, a := sourceSharedFixture(t)
	a.Inputs["patch"] = runner.Value{ArtifactID: "stale/changes.patch"}
	launched := make(chan struct{}, 1)
	s.harnesses.launchWorker = func(string, func()) error { launched <- struct{}{}; return nil }
	changed := make(chan string, 8)
	s.SetWake(func(id string) { changed <- id })
	if _, err := s.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	o := waitSourceShared(t, s, a.AttemptID, changed)
	if o.End.Kind != "failed" || !strings.Contains(o.End.Reason, "workspace seed differs") {
		t.Fatal("stale candidate was not rejected", o.End)
	}
	select {
	case <-launched:
		t.Fatal("stale candidate reached command dispatch")
	default:
	}
	if err := s.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSharedPreparationRetainsAuthenticatedReadySeedIdentity(t *testing.T) {
	s, a := sourceSharedFixture(t)
	a.RunID = "run_retained_seed"
	a.AttemptID = "attempt_seed_check"
	w, err := s.workspaces.Prepare(t.Context(), a)
	if err != nil || w.Candidate != nil || !w.SeedReady || w.SeedPatchID != a.Inputs["patch"].ArtifactID {
		t.Fatal("retained source did not become the same authenticated ready seed", err)
	}
	launched := make(chan struct{}, 1)
	s.harnesses.launchWorker = func(string, func()) error { launched <- struct{}{}; return errors.New("fixture dispatch boundary") }
	changed := make(chan string, 8)
	s.SetWake(func(id string) { changed <- id })
	if _, err := s.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	o := waitSourceShared(t, s, a.AttemptID, changed)
	if o.End.Kind != "failed" || !strings.Contains(o.End.Reason, "fixture dispatch boundary") {
		t.Fatal("current ready seed was rejected before qualified command dispatch", o.End)
	}
	select {
	case <-launched:
	default:
		t.Fatal("ready seed did not reach dispatch")
	}
	if err := s.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
}

func waitSourceShared(t *testing.T, s *Service, id string, changed <-chan string) runner.Observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	for {
		o, err := s.Observe(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if o.End != nil {
			return o
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("source shared command did not settle", ctx.Err(), o.Reason)
		}
	}
}

func sourceSharedReport(t *testing.T, s *Service, o runner.Observation) map[string]any {
	t.Helper()
	if o.End.Kind != "returned" || o.End.Result == nil || o.End.Result.Outcome != "ready" {
		t.Fatal("source command failed", o.End)
	}
	bytes, err := s.disk.Read("artifacts/"+o.End.Result.Outputs["artifact"].ArtifactID, s.disk.Policy.MaxArtifact)
	var report map[string]any
	if err != nil || json.Unmarshal(bytes, &report) != nil || report["source_readonly"] != true || report["other_state_denied"] != true {
		t.Fatal("source/other-state policy was not observed", err, string(bytes))
	}
	return report
}
