package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestRetryReauthorizationCLIRequiresExactPreviewAndTerminalConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", t.TempDir())
	bin := t.TempDir()
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	callLog := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALL_LOG", callLog)
	repo := t.TempDir()
	runCLIGitTest(t, repo, "init", "-b", "main")
	runCLIGitTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
	runCLIGitTest(t, repo, "remote", "add", "origin", "https://github.com/example/repo.git")
	runCLIGitTest(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	cfg := completeCLITestConfig(repo)
	cfg.Harnesses[0].WorkspaceWriteRoot = filepath.Join(t.TempDir(), "worktrees")
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	item := github.WorkItem{ID: "PVTI_recover", Title: "Retry retained work", Body: "Exact approved criteria", URL: "https://github.com/example/repo/issues/2", Repository: "example/repo", Status: "Needs assessment", Phase: "ready", Branch: "runner/retained", QAFailures: 2}
	node := map[string]any{
		"id": item.ID, "status": map[string]any{"name": item.Status}, "phase": map[string]any{"text": item.Phase},
		"qaFailures": map[string]any{"number": item.QAFailures}, "branch": map[string]any{"text": item.Branch},
		"content": map[string]any{"title": item.Title, "body": item.Body, "url": item.URL, "repository": map[string]any{"nameWithOwner": item.Repository}},
	}
	encoded, err := json.Marshal(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": []any{node}, "pageInfo": map[string]any{"hasNextPage": false}}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GH_ITEMS_JSON", string(encoded))
	_, err = workspace.NewGitProvider(nil).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: cfg.Harnesses[0].WorkspaceWriteRoot, WorkID: "assignment_pvti_recover", ItemID: item.ID,
		DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository, BranchName: item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"retry", "--config", configPath, "--item", item.ID, "--reauthorize"}
	for _, mode := range []string{"--dry-run", "--json", "confirmation"} {
		var output bytes.Buffer
		args := append([]string(nil), base...)
		if mode != "confirmation" {
			args = append(args, mode)
		}
		err := run(t.Context(), args, strings.NewReader("yes\n"), &output)
		if mode == "confirmation" {
			if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
				t.Fatalf("piped approval accepted: %v", err)
			}
		} else if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if mode == "--json" {
			if !strings.Contains(output.String(), `"applied": false`) {
				t.Fatal("JSON reauthorization was not a preview")
			}
		} else {
			for _, expected := range []string{item.Body, item.ID, "QA failures: 2", "Destination: Ready", "no QA acceptance or batch approval"} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("preview omitted %q: %s", expected, output.String())
				}
			}
		}
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "project item-edit") || strings.Contains(string(calls), "updateProjectV2ItemFieldValue") {
		t.Fatal("preview or piped confirmation changed Project state")
	}
	var output bytes.Buffer
	if err := run(t.Context(), append(base, "--feedback", "reset"), strings.NewReader(""), &output); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("reauthorization accepted feedback reset: %v", err)
	}
}
