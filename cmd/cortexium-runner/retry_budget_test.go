package main

import (
	"context"
	"encoding/json"
	"fmt"
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
		} else {
			want.ExtraAttempts = 8
			want.Reason = "Owner approved finishing the retained goal"
			want.Directive = "Use the repaired verifier to finish the existing goal"
			want.Revision = 123
			want.MaxTasks = 64
			want.RoleInstructions = map[string]string{"developer": "Use focused tests and preserve independent acceptance"}
		}
		args := []string{
			"retry", "--data", data, "--endpoint", server.URL, "--request-id", want.RequestID,
			"--run", want.RunID, "--profile", want.Profile, "--extra-attempts", fmt.Sprint(want.ExtraAttempts),
		}
		if within {
			args = append(args, "--within-budget")
		} else {
			args = append(args, "--reason", want.Reason, "--text", want.Directive)
			filename := filepath.Join(data, "role-instructions.json")
			body, err := json.Marshal(want.RoleInstructions)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, body, 0600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--revision", fmt.Sprint(want.Revision), "--max-tasks", "64", "--role-instructions-file", filename)
		}
		if err := run(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		if got := <-requests; !reflect.DeepEqual(got, want) {
			t.Fatalf("retry budget request changed: %#v", got)
		}
	}
}

func TestRetryCLIRejectsUnboundInstructionAmendment(t *testing.T) {
	for _, args := range [][]string{
		{"status", "--role-instructions-file", "unused.json"},
		{"retry", "--run", "saved-run", "--extra-attempts", "8", "--role-instructions-file", "unused.json"},
		{"retry", "--run", "saved-run", "--revision", "12", "--role-instructions-file", "unused.json"},
		{"retry", "--run", "saved-run", "--revision", "12", "--extra-attempts", "1", "--within-budget", "--max-tasks", "64"},
	} {
		args = append(args, "--data", t.TempDir())
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "amendments require retry with exact") {
			t.Fatalf("unbound amendment reached credentials or service: %v", err)
		}
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"developer":{}}`, `{`} {
		dir := t.TempDir()
		filename := filepath.Join(dir, "instructions.json")
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		err := run(t.Context(), []string{
			"retry", "--data", dir, "--run", "saved-run", "--revision", "12",
			"--extra-attempts", "8", "--role-instructions-file", filename,
		})
		if err == nil || !strings.Contains(err.Error(), "nonempty JSON object") {
			t.Fatalf("malformed instruction map reached credentials or service: %v", err)
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
