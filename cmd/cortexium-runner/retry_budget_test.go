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
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRetryCLIUsesAuthorizedBudgetMode(t *testing.T) {
	requests := make(chan runner.RetryRequest, 1)
	s := mcp.NewServer(&mcp.Implementation{Name: "retry-budget-fixture", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "runner_retry"}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.RetryRequest) (*mcp.CallToolResult, any, error) {
		requests <- in
		out := runner.Summary{ID: in.RunID, Status: "Queued"}
		b, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, out, nil
	})
	const token = "disposable-fixture-token"
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("CLI omitted owner authentication")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "owner.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	for _, within := range []bool{false, true} {
		want := runner.RetryRequest{
			RequestID:     "authorized-retry",
			RunID:         "saved-run",
			Profile:       "configured-profile",
			ExtraAttempts: 1,
			WithinBudget:  within,
		}
		if within {
			want.RequestID = "authorized-within-budget-retry"
		}
		args := []string{
			"retry", "--data", data, "--endpoint", server.URL, "--request-id", want.RequestID,
			"--run", want.RunID, "--profile", want.Profile, "--extra-attempts", "1",
		}
		if within {
			args = append(args, "--within-budget")
		}
		if err := run(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		if got := <-requests; !reflect.DeepEqual(got, want) {
			t.Fatalf("retry budget request changed: %#v", got)
		}
	}
}

func TestRetryCLIRejectsInvalidWithinBudgetUse(t *testing.T) {
	for _, args := range [][]string{
		{"retry"},
		{"retry", "--extra-attempts", "0"},
		{"retry", "--extra-attempts", "-1"},
		{"retry", "--extra-attempts", "1", "--cleanup-only"},
		{"status", "--extra-attempts", "1"},
		{"recover-corrections", "--extra-attempts", "1"},
		{"recover-collection", "--extra-attempts", "1"},
		{"recover-planning", "--extra-attempts", "1"},
		{"recover-frontend-check", "--extra-attempts", "1"},
		{"recover-frontend-timeouts", "--extra-attempts", "1"},
	} {
		args = append(args, "--data", t.TempDir(), "--within-budget")
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "--within-budget requires retry with positive --extra-attempts and without --cleanup-only") {
			t.Fatalf("invalid budget mode reached credentials or service: %v", err)
		}
	}
}
