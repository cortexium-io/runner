//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestFrontendRecoveryAuthenticatesOwnedDraft(t *testing.T) {
	f := newSourceReleaseFixture(t, false)
	adoptSourceFixture(t, f)
	fixtureGit(t, f.a.Repository, "remote", "add", "origin", "https://github.com/example/fixture.git")
	publication := f.receipt
	publication.Action, publication.RunID, publication.Key = "publish", f.receipt.PublicationRunID, f.receipt.PublicationKey
	publication.PublicationRunID, publication.PublicationKey = "", ""
	run := runner.Run{ID: f.a.RunID, ProjectID: f.a.ProjectID, Repository: f.a.Repository, Source: f.a.Source, Inputs: map[string]runner.Value{"publication": {Inline: mustReleaseJSON(t, publication)}}}
	dir := t.TempDir()
	f.r.cfg.GitHub = filepath.Join(dir, "gh")
	output := filepath.Join(dir, "view.json")
	script := `#!/bin/sh
[ "$*" = "pr view 334 --repo github.com/example/fixture --json state,isDraft,isCrossRepository,headRefName,headRefOid,baseRefOid" ] || exit 1
exec /bin/cat ` + testShellQuote(output) + "\n"
	if err := os.WriteFile(f.r.cfg.GitHub, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	view := map[string]any{"state": "OPEN", "isDraft": true, "isCrossRepository": false, "headRefName": "codex/runner/original", "headRefOid": f.head, "baseRefOid": f.base}
	check := func(want bool) {
		t.Helper()
		if err := os.WriteFile(output, mustReleaseJSON(t, view), 0600); err != nil {
			t.Fatal(err)
		}
		if err := f.r.validateFrontendCheckPublication(t.Context(), run, f.head); (err == nil) != want {
			t.Fatalf("draft validation wanted %t, got %v", want, err)
		}
	}
	check(true)
	for _, mismatch := range []struct {
		field string
		value any
	}{{"headRefOid", f.base}, {"baseRefOid", f.head}, {"isDraft", false}, {"isCrossRepository", true}, {"state", "CLOSED"}, {"headRefName", "another-ref"}} {
		old := view[mismatch.field]
		view[mismatch.field] = mismatch.value
		check(false)
		view[mismatch.field] = old
	}
	publication.Key = "other-publication"
	run.Inputs["publication"] = runner.Value{Inline: mustReleaseJSON(t, publication)}
	check(false)

	publication.Key = f.receipt.PublicationKey
	publication.Policy = strings.Repeat("a", 64)
	run.Inputs["publication"] = runner.Value{Inline: mustReleaseJSON(t, publication)}
	check(false)
	run.Process.ID = "deliver-retained-catalogue"
	run.Process.Steps = []runner.Step{{ID: "source-adopt"}}
	check(false)
	adopted, err := f.r.loadPublication(f.a)
	if err != nil {
		t.Fatal(err)
	}
	run.Attempts = []runner.Attempt{{ID: adopted.Adoption.Key, End: &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{
		Outcome: "ready", Outputs: map[string]runner.Value{"receipt": {Inline: mustReleaseJSON(t, adopted.Adoption)}},
	}}}}
	check(true)
	run.Attempts[0].End.Result.Outputs["receipt"] = runner.Value{Inline: mustReleaseJSON(t, publication)}
	check(false)
}

func TestFrontendRecoveryWorkerRefusesChangedHelperBeforeDispatch(t *testing.T) {
	if !commandGroupsSupported {
		t.Skip("requires macOS or Linux")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "runner-frontend-check")
	if err := os.WriteFile(helper, []byte("qualified helper bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	digest := captureDigest([]byte("qualified helper bytes"))
	if err := verifyFrontendCheckHelper(helper, digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("different helper bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	sandbox := filepath.Join(dir, "sandbox")
	marker := filepath.Join(dir, "dispatched")
	if err := os.WriteFile(sandbox, []byte("#!/bin/sh\ntouch "+testShellQuote(marker)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	spec := commandSpec{AttemptID: "attempt_check", Binary: sandbox, Args: []string{helper}, Directory: dir, Control: dir, Deadline: time.Now().Add(time.Minute), MaxLog: 1024, HelperSHA256: digest}
	specFile := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specFile, mustReleaseJSON(t, spec), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stop"), []byte{0}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunCommandWorker(t.Context(), specFile); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	var result commandResult
	if err != nil || json.Unmarshal(data, &result) != nil || result.ExitCode != 1 || !strings.Contains(result.Diagnostic, "approved digest") {
		t.Fatal("changed helper did not produce a settled failure", string(data), err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("worker launched the sandbox with a changed helper", err)
	}
}

func TestFrontendRecoveryDispatchUsesTheStagedQualifiedHelper(t *testing.T) {
	c, a := commandFixture(t)
	defer c.Close()
	if native := os.Getenv("RUNNER_FRONTEND_RECOVERY_SANDBOX"); native != "" {
		c.cfg.Binary = native
	}
	ctx := t.Context()
	writer := runner.Clone(a)
	writer.AttemptID = "attempt_blocked_owner"
	writer.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	w, err := c.workspaces.Prepare(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.WorkDir, "retained.txt"), []byte("saved frontend correction\n"), 0600); err != nil {
		t.Fatal(err)
	}
	patch, err := c.workspaces.Capture(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.workspaces.releaseRun(ctx, a.RunID, true); err != nil {
		t.Fatal(err)
	}
	c.read = func(_ context.Context, id string) ([]byte, error) {
		return c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
	}
	helper := filepath.Join(t.TempDir(), "runner-frontend-check")
	qualified := []byte("#!/bin/sh\n[ \"$2\" = \"--diagnose-timeouts\" ] || exit 8\n/bin/cat \"$1\"\n")
	if err := os.WriteFile(helper, qualified, 0700); err != nil {
		t.Fatal(err)
	}
	launch := c.launchWorker
	c.launchWorker = func(spec string, finished func()) error {
		if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
			return err
		}
		return launch(spec, finished)
	}
	patchBytes, err := c.read(ctx, patch.PatchID)
	if err != nil {
		t.Fatal(err)
	}
	a.FrontendCheckPins = &runner.FrontendCheckPins{PatchSHA256: captureDigest(patchBytes), HelperSHA256: captureDigest(qualified), DiagnoseTimeouts: true}
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: patch.PatchID}}
	a.Setup.Profile.Command = &runner.Command{Args: []string{helper, "{workspace}/retained.txt"}}
	a.Deadline = time.Now().Add(15 * time.Second)
	if _, err := c.Start(ctx, a); err != nil {
		t.Fatal(err)
	}
	end := waitCommandEnd(t, c, a.AttemptID).End
	if end.Kind != "returned" || !strings.Contains(string(end.Result.Outputs["check"].Inline), "saved frontend correction") {
		t.Fatalf("staged checker did not execute against the retained patch: %+v", end)
	}
}
