package main

import (
	"context"
	"encoding/json"
	"github.com/cortexium-io/runner/internal/runner"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverUnpublishedCardCLIExactPreflightAndOwnerAdmission(t *testing.T) {
	want := runner.RecoverUnpublishedCardRequest{RunID: "root", Revision: 658, GroupAttemptID: "group", ChildRunID: "child", ChildRevision: 81, AttemptID: "failed", PatchSHA256: strings.Repeat("a", 64)}
	got := make(chan runner.RecoverUnpublishedCardRequest, 3)
	s := mcp.NewServer(&mcp.Implementation{Name: "card-fixture", Version: "1"}, nil)
	for _, name := range []string{"runner_preflight_unpublished_card_recovery", "runner_recover_unpublished_card"} {
		mcp.AddTool(s, &mcp.Tool{Name: name}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.RecoverUnpublishedCardRequest) (*mcp.CallToolResult, any, error) {
			got <- in
			out := map[string]string{"run_id": in.RunID}
			b, _ := json.Marshal(out)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, out, nil
		})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer disposable-owner" {
			t.Error("missing owner authentication")
			http.Error(w, "unauthorized", 401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "owner.token"), []byte("disposable-owner"), 0600); e != nil {
		t.Fatal(e)
	}
	base := []string{"recover-unpublished-card", "--data", dir, "--endpoint", server.URL, "--run", want.RunID, "--revision", "658", "--group-attempt", want.GroupAttemptID, "--child-run", want.ChildRunID, "--child-revision", "81", "--attempt", want.AttemptID, "--patch-sha256", want.PatchSHA256}
	if e := run(t.Context(), append(append([]string{}, base...), "--preflight")); e != nil {
		t.Fatal(e)
	}
	if v := <-got; v != want {
		t.Fatal("preflight identity changed", v)
	}
	want.BindingsSHA256 = strings.Repeat("b", 64)
	want.RequestID = "approved-once"
	for range 2 {
		if e := run(t.Context(), append(append([]string{}, base...), "--bindings-sha256", want.BindingsSHA256, "--request-id", want.RequestID)); e != nil {
			t.Fatal(e)
		}
		if v := <-got; v != want {
			t.Fatal("request changed", v)
		}
	}
	for _, extra := range [][]string{nil, {"--extra-attempts", "1"}, {"--profile", "larger"}, {"--max-uses", "2"}, {"--preserve-source"}, {"--cleanup-only"}, {"--run", ""}, {"--revision", "0"}} {
		args := append(append([]string{}, base...), extra...)
		if extra != nil {
			args = append(args, "--bindings-sha256", want.BindingsSHA256)
		}
		if e := run(t.Context(), args); e == nil || !strings.Contains(e.Error(), "no overrides") {
			t.Fatal("unsafe arguments reached credentials/service", extra, e)
		}
	}
}
