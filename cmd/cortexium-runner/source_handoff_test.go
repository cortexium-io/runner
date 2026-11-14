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

func TestSourceHandoffCLIExactWireAndNoOverrides(t *testing.T) {
	want := runner.AdjudicateSourceHandoffRequest{RecoverUnpublishedCardRequest: runner.RecoverUnpublishedCardRequest{RunID: "root", Revision: 454, GroupAttemptID: "group", ChildRunID: "child", ChildRevision: 61, AttemptID: "blocked", PatchSHA256: strings.Repeat("a", 64)}, ReasonSHA256: strings.Repeat("c", 64)}
	got := make(chan runner.AdjudicateSourceHandoffRequest, 2)
	s := mcp.NewServer(&mcp.Implementation{Name: "handoff-fixture", Version: "1"}, nil)
	for _, name := range []string{"runner_preflight_source_handoff", "runner_adjudicate_source_handoff"} {
		mcp.AddTool(s, &mcp.Tool{Name: name}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.AdjudicateSourceHandoffRequest) (*mcp.CallToolResult, any, error) {
			got <- in
			out := map[string]string{"run_id": in.RunID}
			b, _ := json.Marshal(out)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, out, nil
		})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-owner" {
			t.Error("missing owner authentication")
			http.Error(w, "unauthorized", 401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "owner.token"), []byte("fixture-owner"), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"adjudicate-source-handoff", "--data", dir, "--endpoint", server.URL, "--run", want.RunID, "--revision", "454", "--group-attempt", want.GroupAttemptID, "--child-run", want.ChildRunID, "--child-revision", "61", "--attempt", want.AttemptID, "--patch-sha256", want.PatchSHA256, "--reason-sha256", want.ReasonSHA256}
	if err := run(t.Context(), append(append([]string{}, base...), "--preflight")); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != want {
		t.Fatal("preflight identity changed", v)
	}
	want.BindingsSHA256, want.RequestID = strings.Repeat("b", 64), "handoff-once"
	args := append(append([]string{}, base...), "--bindings-sha256", want.BindingsSHA256, "--request-id", want.RequestID)
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != want {
		t.Fatal("admission identity changed", v)
	}
	for _, extra := range [][]string{{"--extra-attempts", "1"}, {"--profile", "larger"}, {"--reason-sha256", ""}} {
		if err := run(t.Context(), append(append([]string{}, args...), extra...)); err == nil {
			t.Fatal("override reached owner service", extra)
		}
	}
}
