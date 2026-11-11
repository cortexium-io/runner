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

func TestFrontendRecoveryCLIUsesExactSavedEvidence(t *testing.T) {
	requests := make(chan runner.RecoverFrontendCheckRequest, 1)
	s := mcp.NewServer(&mcp.Implementation{Name: "frontend-recovery-fixture", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "runner_recover_frontend_check"}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.RecoverFrontendCheckRequest) (*mcp.CallToolResult, any, error) {
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
	for _, mode := range []string{"ordinary", "serial", "assertion"} {
		t.Run(mode, func(t *testing.T) {
			want := runner.RecoverFrontendCheckRequest{
				RequestID:       "approved-" + mode,
				RunID:           "saved-run",
				AttemptID:       "saved-attempt",
				Revision:        8,
				PatchSHA256:     strings.Repeat("a", 64),
				HelperSHA256:    strings.Repeat("b", 64),
				PublicationHead: strings.Repeat("c", 40),
			}
			args := []string{
				"recover-frontend-check", "--data", data, "--endpoint", server.URL,
				"--run", want.RunID, "--attempt", want.AttemptID, "--revision", "8", "--request-id", want.RequestID,
				"--patch-sha256", want.PatchSHA256, "--helper-sha256", want.HelperSHA256, "--publication-head", want.PublicationHead,
			}
			switch mode {
			case "serial":
				want.Serial = true
				args = append(args, "--serial")
			case "assertion":
				want.AssertionSHA256 = strings.Repeat("d", 64)
				args = append(args, "--assertion-sha256", want.AssertionSHA256)
			}
			if err := run(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			if got := <-requests; !reflect.DeepEqual(got, want) {
				t.Fatalf("recovery request changed: %#v", got)
			}
		})
	}
}

func TestFrontendAssertionRecoveryCLIRejectsOtherCommandsAndSerialMode(t *testing.T) {
	for _, args := range [][]string{
		{"status"}, {"retry"}, {"recover-frontend-timeouts"}, {"recover-frontend-check", "--serial"},
	} {
		args = append(args, "--data", t.TempDir(), "--assertion-sha256", strings.Repeat("d", 64))
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "--assertion-sha256 requires recover-frontend-check without --serial") {
			t.Fatalf("assertion recovery mode reached unrelated command or serial recovery: %v", err)
		}
	}
}

func TestFrontendRecoveryCLIRejectsGrantsAndUnboundRequests(t *testing.T) {
	base := []string{
		"recover-frontend-check", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "blocked-owner",
		"--revision", "8", "--patch-sha256", strings.Repeat("a", 64), "--helper-sha256", strings.Repeat("b", 64),
		"--publication-head", strings.Repeat("c", 40),
	}
	for _, invalid := range [][]string{
		{"--extra-attempts", "1"}, {"--profile", "override"}, {"--max-uses", "3"}, {"--preserve-source"}, {"--cleanup-only"},
		{"--revision", "0"}, {"--patch-sha256", ""}, {"--helper-sha256", ""}, {"--publication-head", ""},
	} {
		args := append(append([]string{}, base...), invalid...)
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "grants no retry or profile override") {
			t.Fatal("unbound request or permission override reached service", invalid, err)
		}
	}
}

func TestFrontendTimeoutRecoveryCLIRequiresPriorCheckAndRejectsAllowanceOverrides(t *testing.T) {
	base := []string{
		"recover-frontend-timeouts", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "blocked-owner",
		"--revision", "8", "--patch-sha256", strings.Repeat("a", 64), "--helper-sha256", strings.Repeat("b", 64),
		"--publication-head", strings.Repeat("c", 40), "--check-attempt", "old-check", "--check-sha256", strings.Repeat("d", 64),
	}
	for _, invalid := range [][]string{
		{"--check-attempt", ""}, {"--check-sha256", ""}, {"--extra-attempts", "1"}, {"--profile", "override"}, {"--max-uses", "3"},
	} {
		args := append(append([]string{}, base...), invalid...)
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "grants no retry or profile override") {
			t.Fatal("unsafe timeout recovery reached service", invalid, err)
		}
	}
}
