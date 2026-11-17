package main

import (
	"context"
	"crypto/sha256"
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
	content := []byte(`{"policy":"Use explicit labels","runtime":"RUNNER_PROJECT_FRONTEND_MODULES"}`)
	contextPath := filepath.Join(t.TempDir(), "context.json")
	if err := os.WriteFile(contextPath, content, 0600); err != nil {
		t.Fatal(err)
	}
	want = runner.RecoverCorrectionsRequest{
		RequestID:         "approved-single-current-route",
		PreviousRequestID: "prior-explicit-grant",
		RunID:             "saved-run",
		AttemptID:         "saved-source-check",
		CurrentRoute:      true,
		Revision:          407,
		MaxUses:           4,
		PatchSHA256:       strings.Repeat("a", 64),
		CheckSHA256:       strings.Repeat("b", 64),
		Context: &runner.CorrectionContextInput{
			Content: string(content),
			Bytes:   int64(len(content)),
			SHA256:  fmt.Sprintf("%x", sha256.Sum256(content)),
			Roles:   []string{"owner", "review"},
		},
	}
	args := []string{
		"recover-corrections",
		"--data",
		data,
		"--endpoint",
		server.URL,
		"--run",
		want.RunID,
		"--attempt",
		want.AttemptID,
		"--request-id",
		want.RequestID,
		"--current-route",
		"--previous-request-id", want.PreviousRequestID,
		"--revision",
		"407",
		"--max-uses",
		"4",
		"--patch-sha256",
		want.PatchSHA256,
		"--check-sha256",
		want.CheckSHA256,
		"--context-file",
		contextPath,
		"--context-sha256",
		want.Context.SHA256,
		"--context-roles",
		`["owner","review"]`,
	}
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if got := <-requests; !reflect.DeepEqual(got, want) {
		t.Fatal("exact current-route/context request changed", got)
	}
	additional := 3
	want.AdditionalUses, want.MaxUses = &additional, 8
	want.RequestID = "approved-three-use-phase"
	args = append(args, "--additional-uses", "3", "--max-uses", "8", "--request-id", want.RequestID)
	for range 2 {
		if err := run(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		if got := <-requests; !reflect.DeepEqual(got, want) {
			t.Fatal("explicit phase count changed through CLI/MCP", got)
		}
	}
}

func TestRecoverCorrectionsCLIRejectsInvalidPhaseCounts(t *testing.T) {
	base := []string{"recover-corrections", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "saved-check"}
	for _, extra := range [][]string{
		{"--current-route", "--additional-uses", "0"},
		{"--current-route", "--additional-uses", "-1"},
		{"--within-budget", "--additional-uses", "3"},
		{"--max-uses", "3", "--additional-uses", "3"},
	} {
		if err := run(t.Context(), append(append([]string{}, base...), extra...)); err == nil || !strings.Contains(err.Error(), "--additional-uses requires") {
			t.Fatal("invalid count reached credentials/service", extra, err)
		}
	}
	if err := run(t.Context(), []string{"retry", "--data", t.TempDir(), "--additional-uses", "3"}); err == nil || !strings.Contains(err.Error(), "--additional-uses requires") {
		t.Fatal("phase count reached another command", err)
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
