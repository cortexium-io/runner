package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestAmendCLIRequiresExactRequirementsAndInteractiveConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	stateDir := t.TempDir()
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", stateDir)
	bin := t.TempDir()
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	callLog := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALL_LOG", callLog)
	repo := t.TempDir()
	runCLIGitTest(t, repo, "init", "-b", "main")
	runCLIGitTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "candidate")
	runCLIGitTest(t, repo, "remote", "add", "origin", "https://github.com/example/repo.git")
	runCLIGitTest(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	cfg := completeCLITestConfig(repo)
	cfg.Harnesses[0].WorkspaceWriteRoot = filepath.Join(t.TempDir(), "worktrees")
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	item := github.WorkItem{ID: "PVTI_amend", Title: "Gutter", Body: "Keep preview wrapping unchanged.\n\n## Proof obligations\n- Same preview width", URL: "https://github.com/example/repo/issues/2", Repository: "example/repo", Status: "Blocked", Phase: "agent_qa", Branch: "runner/retained", QAFailures: 2}
	key := sha256.Sum256([]byte("runner-cli-test-approval-authority"))
	runnerDigest := sha256.Sum256([]byte("runner"))
	keyPath := filepath.Join(stateDir, "approval-authority", hex.EncodeToString(runnerDigest[:12])+".key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key[:], 0o600); err != nil {
		t.Fatal(err)
	}
	item.Approval = signCLITestActionAssertion(cfg.ResolveProject(), item, config.WorkRoleReviewer, "blocked", key[:])
	node := map[string]any{
		"id": item.ID, "status": map[string]any{"name": item.Status}, "phase": map[string]any{"text": item.Phase},
		"approval": map[string]any{"text": item.Approval}, "qaFailures": map[string]any{"number": item.QAFailures}, "branch": map[string]any{"text": item.Branch},
		"content": map[string]any{"title": item.Title, "body": item.Body, "url": item.URL, "repository": map[string]any{"nameWithOwner": item.Repository}},
	}
	encoded, err := json.Marshal(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": []any{node}, "pageInfo": map[string]any{"hasNextPage": false}}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GH_ITEMS_JSON", string(encoded))
	_, err = workspace.NewGitProvider(nil).Prepare(t.Context(), workspace.Request{WorkingDir: repo, WorktreeRoot: cfg.Harnesses[0].WorkspaceWriteRoot,
		WorkID: "assignment_pvti_amend", ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest,
		Repository: item.Repository, BranchName: item.Branch, BaseRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	body := "Reserve an external editor gutter.\n\n## Proof obligations\n- Preview wrapping may change; saved email may not."
	bodyFile := filepath.Join(t.TempDir(), "approved-body.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"amend", "--config", configPath, "--item", item.ID, "--body-file", bodyFile}
	for _, mode := range []string{"--dry-run", "--json", "piped"} {
		var output bytes.Buffer
		args := append([]string(nil), base...)
		if mode != "piped" {
			args = append(args, mode)
		}
		err := run(t.Context(), args, strings.NewReader("yes\n"), &output)
		if mode == "piped" {
			if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
				t.Fatalf("piped confirmation accepted: %v", err)
			}
		} else if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if mode == "--json" {
			var payload struct {
				Applied   bool
				Amendment struct {
					OldProof []string `json:"old_proof_obligations"`
					NewProof []string `json:"new_proof_obligations"`
				}
			}
			if err := json.Unmarshal(output.Bytes(), &payload); err != nil || payload.Applied || len(payload.Amendment.OldProof) != 1 || len(payload.Amendment.NewProof) != 1 {
				t.Fatalf("invalid read-only preview: %v", err)
			}
		} else {
			for _, expected := range []string{item.Body, body, "Retained QA failures: 2", "no existing QA acceptance"} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("preview omitted %q", expected)
				}
			}
		}
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"issue edit", "project item-edit", "updateProjectV2ItemFieldValue"} {
		if strings.Contains(string(calls), forbidden) {
			t.Fatalf("preview wrote through %s", forbidden)
		}
	}
}
