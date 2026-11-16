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

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/control"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTaskFollowupCLIUsesNormalStartAndExactParent(t *testing.T) {
	want := control.StartArgs{
		RequestID: "owner-followup", Project: "preview", Process: "followup",
		Followup:  &runner.TaskFollowupRequest{RunID: "accepted-root", Revision: 6322},
		Inputs:    map[string]any{"request": "Apply the owner's current corrections"},
		Artifacts: map[string]string{"prior": "accepted-check/result"},
	}
	requests := make(chan control.StartArgs, 1)
	serverMCP := mcp.NewServer(&mcp.Implementation{Name: "followup-fixture", Version: "1"}, nil)
	mcp.AddTool(serverMCP, &mcp.Tool{Name: "runner_start"}, func(_ context.Context, _ *mcp.CallToolRequest, in control.StartArgs) (*mcp.CallToolResult, any, error) {
		requests <- in
		out := map[string]string{"id": "linked-root"}
		data, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
	})
	const token = "disposable-followup-token"
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return serverMCP }, &mcp.StreamableHTTPOptions{Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("follow-up omitted owner authentication")
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
		"start", "--data", root, "--endpoint", server.URL, "--request-id", want.RequestID,
		"--project", want.Project, "--process", want.Process,
		"--followup-run", want.Followup.RunID, "--followup-revision", "6322",
		"--inputs", `{"request":"Apply the owner's current corrections"}`,
		"--artifacts", `{"prior":"accepted-check/result"}`,
	}
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; !reflect.DeepEqual(got, want) {
		t.Fatal("CLI changed follow-up parent or request", got)
	}
	for _, bad := range [][]string{
		{"--followup-revision", "0"}, {"--followup-run", ""}, {"--source-ref", "other"},
		{"--profile", "other"}, {"--extra-attempts", "1"}, {"--cleanup-only"},
	} {
		if err := run(t.Context(), append(append([]string{}, args...), bad...)); err == nil || !strings.Contains(err.Error(), "linked follow-up requires") {
			t.Fatal("invalid follow-up reached the service", bad, err)
		}
	}
	if err := run(t.Context(), []string{"status", "--data", root, "--followup-run", "accepted-root", "--followup-revision", "6322"}); err == nil || !strings.Contains(err.Error(), "linked follow-up requires") {
		t.Fatal("unrelated command silently ignored follow-up authority", err)
	}
}
