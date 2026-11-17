package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func evidenceHandoffCLIArgs(data string) []string {
	return []string{"recover-evidence-handoff", "--data", data, "--run", "saved-run", "--revision", "676", "--request-id", "approved-handoff8", "--phase-request-id", "phase8", "--attempt", "blocked-owner", "--source-attempt", "accepted-owner", "--patch-sha256", "patch-sha", "--check-attempt", "failed-check", "--check-sha256", "check-sha", "--evidence-id", "project/result", "--evidence-sha256", "evidence-sha", "--evidence-bytes", "419683", "--evidence-roles", `["owner","review"]`}
}

func TestEvidenceHandoffCLISendsExactOwnerBoundIdentity(t *testing.T) {
	requests := make(chan runner.RecoverEvidenceHandoffRequest, 2)
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "runner_recover_evidence_handoff"}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.RecoverEvidenceHandoffRequest) (*mcp.CallToolResult, any, error) {
		requests <- in
		out := runner.Summary{ID: in.RunID, Status: "Queued"}
		data, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer disposable-owner-token" {
			t.Error("missing owner authentication")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer host.Close()
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "owner.token"), []byte("disposable-owner-token"), 0600); err != nil {
		t.Fatal(err)
	}
	args := append(evidenceHandoffCLIArgs(data), "--endpoint", host.URL)
	want := runner.RecoverEvidenceHandoffRequest{RequestID: "approved-handoff8", RunID: "saved-run", Revision: 676, PhaseRequestID: "phase8", AttemptID: "blocked-owner", SourceAttemptID: "accepted-owner", PatchSHA256: "patch-sha", CheckAttemptID: "failed-check", CheckSHA256: "check-sha", Evidence: runner.CorrectionContext{ID: "project/result", SHA256: "evidence-sha", Bytes: 419683, Roles: []string{"owner", "review"}}}
	for range 2 {
		if err := run(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		if got := <-requests; !reflect.DeepEqual(got, want) {
			t.Fatalf("CLI changed exact saved authority: %#v", got)
		}
	}
}

func TestEvidenceHandoffCLIRejectsOverridesAndMissingBindingsBeforeConnecting(t *testing.T) {
	for _, override := range [][]string{{"--max-uses", "9"}, {"--extra-attempts", "1"}, {"--profile", "other"}, {"--current-route"}, {"--within-budget"}, {"--preserve-source"}, {"--additional-uses", "1"}, {"--config", "other"}, {"--evidence-bytes", "0"}, {"--evidence-roles", "[]"}, {"--evidence-roles", `{}`}} {
		args := append(evidenceHandoffCLIArgs(t.TempDir()), override...)
		if err := run(t.Context(), args); err == nil {
			t.Fatal("handoff accepted override", override)
		}
	}
	required := map[string]bool{"--run": true, "--revision": true, "--phase-request-id": true, "--attempt": true, "--source-attempt": true, "--patch-sha256": true, "--check-attempt": true, "--check-sha256": true, "--evidence-id": true, "--evidence-sha256": true, "--evidence-bytes": true, "--evidence-roles": true}
	for flag := range required {
		args := evidenceHandoffCLIArgs(t.TempDir())
		for i := 1; i < len(args); i += 2 {
			if args[i] == flag {
				args = append(args[:i], args[i+2:]...)
				break
			}
		}
		if err := run(t.Context(), args); err == nil {
			t.Fatal("handoff accepted missing identity", flag)
		}
	}
}
