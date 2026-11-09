package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRecoverCollectionCLIUsesOnlyExactApprovedIdentity(t *testing.T) {
	want := runner.RecoverCollectionRequest{RequestID: "approved-collection-repair", RunID: "saved-run", AttemptID: "failed-measure"}
	requests := make(chan runner.RecoverCollectionRequest, 2)
	s := mcp.NewServer(&mcp.Implementation{Name: "collection-fixture", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "runner_recover_collection"}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.RecoverCollectionRequest) (*mcp.CallToolResult, any, error) {
		requests <- in
		out := runner.Summary{ID: in.RunID, Status: "Queued"}
		data, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
	})
	token := "disposable-collection-owner"
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("collection recovery omitted owner authentication")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "owner.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"recover-collection", "--data", dir, "--endpoint", server.URL, "--run", want.RunID, "--attempt", want.AttemptID, "--request-id", want.RequestID}
	for range 2 {
		if err := run(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		if got := <-requests; got != want {
			t.Fatal("collection identity changed across replay", got)
		}
	}
}

func TestRecoverCollectionCLIRejectsAllowancesAndPermissionOverrides(t *testing.T) {
	base := []string{"recover-collection", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "failed-measure"}
	for _, extra := range [][]string{{"--extra-attempts", "1"}, {"--profile", "larger"}, {"--max-uses", "3"}, {"--preserve-source"}, {"--cleanup-only"}, {"--run", ""}, {"--attempt", ""}} {
		if err := run(t.Context(), append(append([]string{}, base...), extra...)); err == nil || !strings.Contains(err.Error(), "grants no retry allowance") {
			t.Fatal("unsupported collection recovery reached credentials/service", extra, err)
		}
	}
}
