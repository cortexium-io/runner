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

func TestRecoverCorrectionsCLIUsesExactSavedAttemptAndTotalLimit(t *testing.T) {
	want := runner.RecoverCorrectionsRequest{RequestID: "approved-recovery", RunID: "saved-run", AttemptID: "saved-source-check", MaxUses: 3}
	requests := make(chan runner.RecoverCorrectionsRequest, 1)
	s := mcp.NewServer(&mcp.Implementation{Name: "recovery-fixture", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "runner_recover_corrections"}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.RecoverCorrectionsRequest) (*mcp.CallToolResult, any, error) {
		requests <- in
		out := runner.Summary{ID: in.RunID, Status: "Running", Attempts: 5}
		b, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, out, nil
	})
	token := "disposable-fixture-token"
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
		for _, preserve := range []bool{false, true} {
			if within && preserve {
				continue // Budget mode requires already-settled source.
			}
			want.PreserveSource, want.WithinBudget = preserve, within
			want.RequestID, want.MaxUses = "approved-recovery", 3
			if within {
				want.RequestID, want.MaxUses = "approved-budget-recovery", 0
			}
			if preserve {
				want.RequestID += "-preservation"
			}
			args := []string{"recover-corrections", "--data", data, "--endpoint", server.URL, "--run", want.RunID, "--attempt", want.AttemptID, "--request-id", want.RequestID}
			if within {
				args = append(args, "--within-budget")
			} else {
				args = append(args, "--max-uses", "3")
			}
			if preserve {
				args = append(args, "--preserve-source")
			}
			for range 2 { // A disconnect/retry uses the same request identity and payload.
				if err := run(t.Context(), args); err != nil {
					t.Fatal(err)
				}
				if got := <-requests; !reflect.DeepEqual(got, want) {
					t.Fatalf("recovery request changed: %#v", got)
				}
			}
		}
	}
}

func TestRecoverCorrectionsCLIRejectsConflictingBudgetGrants(t *testing.T) {
	base := []string{
		"recover-corrections", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "saved-check", "--within-budget",
	}
	for _, extra := range [][]string{
		{"--max-uses", "3"}, {"--max-uses", "4"}, {"--max-uses", "-1"},
		{"--extra-attempts", "1"}, {"--profile", "larger"}, {"--cleanup-only"}, {"--preserve-source"}, {"--attempt", ""}, {"--run", ""},
	} {
		args := append(append([]string{}, base...), extra...)
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "or --within-budget without --max-uses") {
			t.Fatalf("conflicting budget recovery reached credentials/service: %v; %v", extra, err)
		}
	}
}

func TestRecoverCorrectionsCLICannotGrantRetryAllowances(t *testing.T) {
	base := []string{"recover-corrections", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "saved-check", "--max-uses", "3"}
	for _, extra := range [][]string{{"--max-uses", "4"}, {"--max-uses", "0"}, {"--extra-attempts", "1"}, {"--profile", "larger"}, {"--cleanup-only"}, {"--attempt", ""}, {"--run", ""}} {
		if err := run(t.Context(), append(append([]string{}, base...), extra...)); err == nil || !strings.Contains(err.Error(), "requires --run, --attempt and --max-uses 3") {
			t.Fatalf("unsupported recovery arguments %v reached credentials/service: %v", extra, err)
		}
	}
}
