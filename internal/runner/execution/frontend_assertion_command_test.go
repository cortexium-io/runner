//go:build darwin || linux

package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestFrontendAssertionNormalCheckRequiresFreshCaptureDigest(t *testing.T) {
	for _, mode := range []string{"fresh", "old digest", "empty digest", "missing patch", "changed patch bytes"} {
		t.Run(mode, func(t *testing.T) {
			c, a := commandFixture(t)
			defer c.Close()
			if native := os.Getenv("RUNNER_FRONTEND_RECOVERY_SANDBOX"); native != "" {
				c.cfg.Binary = native
			}
			ctx := t.Context()
			writer := runner.Clone(a)
			writer.AttemptID = "attempt_diagnostic_owner"
			writer.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
			w, err := c.workspaces.Prepare(ctx, writer)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(w.WorkDir, "retained.txt")
			if err := os.WriteFile(file, []byte("prior diagnostic repair\n"), 0600); err != nil {
				t.Fatal(err)
			}
			old, err := c.workspaces.Capture(ctx, writer)
			if err != nil {
				t.Fatal(err)
			}
			oldPatch, err := c.disk.Read("artifacts/"+old.PatchID, c.disk.Policy.MaxArtifact)
			if err != nil {
				t.Fatal(err)
			}
			writer.AttemptID = "attempt_assertion_owner"
			const correction = "focused assertion correction\n"
			if err := os.WriteFile(file, []byte(correction), 0600); err != nil {
				t.Fatal(err)
			}
			fresh, err := c.workspaces.Capture(ctx, writer)
			if err != nil {
				t.Fatal(err)
			}
			freshPatch, err := c.disk.Read("artifacts/"+fresh.PatchID, c.disk.Policy.MaxArtifact)
			if err != nil {
				t.Fatal(err)
			}
			if old.PatchID == fresh.PatchID || captureDigest(oldPatch) == captureDigest(freshPatch) {
				t.Fatal("fixture did not capture a distinct new owner patch")
			}
			if err := c.workspaces.releaseRun(ctx, a.RunID, true); err != nil {
				t.Fatal(err)
			}
			c.read = func(_ context.Context, id string) ([]byte, error) {
				return c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
			}
			helper := filepath.Join(t.TempDir(), "runner-frontend-check")
			// A normal check has neither diagnostic nor serial arguments.
			qualified := []byte("#!/bin/sh\n[ \"$#\" -eq 1 ] || exit 8\n/bin/cat \"$1\"\n")
			if err := os.WriteFile(helper, qualified, 0700); err != nil {
				t.Fatal(err)
			}
			a.FrontendCheckPins = &runner.FrontendCheckPins{
				PatchSHA256:  captureDigest(freshPatch),
				HelperSHA256: captureDigest(qualified),
			}
			a.Inputs = map[string]runner.Value{"patch": {ArtifactID: fresh.PatchID}}
			a.Setup.Profile.Command = &runner.Command{Args: []string{helper, "{workspace}/retained.txt"}}
			a.Deadline = time.Now().Add(15 * time.Second)
			switch mode {
			case "old digest":
				a.FrontendCheckPins.PatchSHA256 = captureDigest(oldPatch)
			case "empty digest":
				a.FrontendCheckPins.PatchSHA256 = ""
			case "missing patch":
				a.Inputs = nil
			case "changed patch bytes":
				if err := c.disk.Write("artifacts/"+fresh.PatchID, oldPatch, c.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			}
			launch := c.launchWorker
			var launches atomic.Int32
			c.launchWorker = func(spec string, finished func()) error {
				launches.Add(1)
				return launch(spec, finished)
			}
			if _, err := c.Start(ctx, a); err != nil {
				t.Fatal(err)
			}
			end := waitCommandEnd(t, c, a.AttemptID).End
			if mode == "fresh" {
				if end.Kind != "returned" || end.Result == nil || end.Result.Outcome != "ready" || launches.Load() != 1 ||
					!strings.Contains(string(end.Result.Outputs["check"].Inline), strings.TrimSpace(correction)) {
					t.Fatalf("normal pinned check did not execute against the fresh captured source: %+v, launches=%d", end, launches.Load())
				}
				return
			}
			if end.Kind != "failed" || !strings.Contains(end.Reason, "patch") || launches.Load() != 0 {
				t.Fatalf("unbound normal check reached its helper: %+v, launches=%d", end, launches.Load())
			}
		})
	}
}
