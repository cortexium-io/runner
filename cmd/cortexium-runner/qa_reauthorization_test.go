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

func TestQAOnlyRetryCLIIsPreviewOnlyWithoutTerminalConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", t.TempDir())
	bin := t.TempDir()
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALL_LOG", log)
	repo := t.TempDir()
	runCLIGitTest(t, repo, "init", "-b", "main")
	runCLIGitTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
	runCLIGitTest(t, repo, "remote", "add", "origin", "https://github.com/example/repo.git")
	runCLIGitTest(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	cfg := completeCLITestConfig(repo)
	cfg.Harnesses[0].WorkspaceWriteRoot = filepath.Join(t.TempDir(), "worktrees")
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	item := github.WorkItem{ID: "PVTI_qa_only", Title: "Review retained work", Body: "Old requirements", Repository: "example/repo", Status: "Blocked", Phase: "agent_qa", Branch: "runner/retained", PullRequest: "https://github.com/example/repo/pull/5", QAFailures: 7}
	metadata, err := workspace.NewGitProvider(nil).Prepare(t.Context(), workspace.Request{WorkingDir: repo, WorktreeRoot: cfg.Harnesses[0].WorkspaceWriteRoot, WorkID: "assignment_pvti_qa_only", ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository, BranchName: item.Branch, BaseRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(runCLIGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
	item.Body = "## Proof obligations\n- Current requirements\nOperational pause.\nNo production deployment."
	node := map[string]any{"id": item.ID, "status": map[string]any{"name": item.Status}, "phase": map[string]any{"text": item.Phase}, "qaFailures": map[string]any{"number": item.QAFailures}, "branch": map[string]any{"text": item.Branch}, "pullRequest": map[string]any{"text": item.PullRequest}, "content": map[string]any{"title": item.Title, "body": item.Body, "repository": map[string]any{"nameWithOwner": item.Repository}}}
	encoded, _ := json.Marshal(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": []any{node}, "pageInfo": map[string]any{"hasNextPage": false}}}}})
	t.Setenv("FAKE_GH_ITEMS_JSON", string(encoded))
	pr, _ := json.Marshal(map[string]any{"url": item.PullRequest, "number": 5, "state": "OPEN", "headRepository": map[string]any{"nameWithOwner": item.Repository}, "headRefName": item.Branch, "headRefOid": head, "baseRefName": "main", "baseRefOid": metadata.BaseRevision})
	t.Setenv("FAKE_GH_PR_JSON", string(pr))
	base := []string{"retry", "--config", path, "--item", item.ID, "--reauthorize", "--qa-only"}
	for _, mode := range []string{"--dry-run", "--json", "piped-confirmation"} {
		args := append([]string(nil), base...)
		if mode != "piped-confirmation" {
			args = append(args, mode)
		}
		var output bytes.Buffer
		err := run(t.Context(), args, strings.NewReader("yes\n"), &output)
		if mode == "piped-confirmation" {
			if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
				t.Fatalf("piped consent accepted: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if mode == "--json" {
			var preview struct {
				Applied bool
				QAOnly  json.RawMessage `json:"qa_only"`
			}
			if err := json.Unmarshal(output.Bytes(), &preview); err != nil || preview.Applied || len(preview.QAOnly) == 0 {
				t.Fatalf("invalid preview %s %v", output.String(), err)
			}
		} else {
			for _, want := range []string{head, "CONTENT CHANGED", "Current requirements", "QA failures: 7", "card remains paused", "No production deployment"} {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("missing %q: %s", want, output.String())
				}
			}
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "project item-edit") || strings.Contains(string(calls), "updateProjectV2ItemFieldValue") || strings.Contains(string(calls), "pr merge") {
		t.Fatalf("preview changed state: %s", calls)
	}
	for _, args := range [][]string{{"retry", "--qa-only"}, append(append([]string(nil), base...), "--feedback", "reset")} {
		var output bytes.Buffer
		if err := run(t.Context(), args, strings.NewReader(""), &output); err == nil {
			t.Fatalf("invalid flag combination accepted: %v", args)
		}
	}
}
