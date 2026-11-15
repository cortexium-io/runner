package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/control"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTaskContinuationCLIUsesExactEvidenceAndNoOverrides(t *testing.T) {
	want := control.ContinueTaskArgs{
		RequestID: "owner-handoff", RunID: "retained-root", Revision: 87,
		Project: "preview", Process: "improve-sudoku", SourceAttemptID: "retained-source",
		PatchSHA256: strings.Repeat("a", 64), CheckAttemptID: "passing-check", CheckSHA256: strings.Repeat("b", 64),
		Inputs:    map[string]any{"request": "Visibly improve Sudoku"},
		Artifacts: map[string]string{"patch": "retained-source/changes.patch", "checks": "passing-check/result"},
	}
	requests := make(chan control.ContinueTaskArgs, 1)
	serverMCP := mcp.NewServer(&mcp.Implementation{Name: "continuation-fixture", Version: "1"}, nil)
	mcp.AddTool(serverMCP, &mcp.Tool{Name: "runner_continue_task"}, func(_ context.Context, _ *mcp.CallToolRequest, in control.ContinueTaskArgs) (*mcp.CallToolResult, any, error) {
		requests <- in
		out := map[string]string{"id": "new-role-task-root"}
		data, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
	})
	const token = "disposable-owner-token"
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return serverMCP }, &mcp.StreamableHTTPOptions{Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("continuation omitted owner authentication")
			http.Error(w, "owner token required", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "owner.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"continue-task", "--data", root, "--endpoint", server.URL, "--request-id", want.RequestID,
		"--run", want.RunID, "--revision", "87", "--project", want.Project, "--process", want.Process,
		"--source-attempt", want.SourceAttemptID, "--patch-sha256", want.PatchSHA256,
		"--check-attempt", want.CheckAttemptID, "--check-sha256", want.CheckSHA256,
		"--inputs", `{"request":"Visibly improve Sudoku"}`,
		"--artifacts", `{"patch":"retained-source/changes.patch","checks":"passing-check/result"}`,
	}
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; !reflect.DeepEqual(got, want) {
		t.Fatal("CLI changed continuation authority", got)
	}
	for _, bad := range [][]string{
		{"--profile", "override"}, {"--extra-attempts", "1"}, {"--source-ref", "other-branch"},
		{"--cleanup-only"}, {"--wait"}, {"--revision", "0"}, {"--project", ""}, {"--source-attempt", ""},
	} {
		if err := run(t.Context(), append(append([]string{}, args...), bad...)); err == nil || !strings.Contains(err.Error(), "no command, profile, allowance or approval override") {
			t.Fatal("invalid continuation reached service", bad, err)
		}
	}
}
