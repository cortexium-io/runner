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

func TestAmendRolesCLIPreservesExactOwnerRequestWithoutAllowance(t *testing.T) {
	for _, models := range []bool{false, true} {
		t.Run(fmt.Sprint("models_", models), func(t *testing.T) {
			want := runner.TaskRoleAmendmentRequest{
				RequestID: "owner-amendment-once", RunID: "paused-root", Revision: 42,
				Reason:           "Correct the existing goal's future role scope",
				RoleInstructions: map[string]string{"developer": "Implement only the explicitly assigned non-generated catalogue paths; preserve all other obligations."},
			}
			flagName := "--role-instructions-file"
			selected := want.RoleInstructions
			if models {
				want.RoleModels, want.RoleInstructions = map[string]string{"developer": "gpt-6.2-sol"}, nil
				selected = want.RoleModels
				flagName = "--role-models-file"
			}
			requests := make(chan runner.TaskRoleAmendmentRequest, 1)
			s := mcp.NewServer(&mcp.Implementation{Name: "role-amendment-fixture", Version: "1"}, nil)
			mcp.AddTool(s, &mcp.Tool{Name: "runner_amend_roles"}, func(_ context.Context, _ *mcp.CallToolRequest, in runner.TaskRoleAmendmentRequest) (*mcp.CallToolResult, any, error) {
				requests <- in
				out := runner.Summary{ID: in.RunID, Status: "Waiting"}
				data, _ := json.Marshal(out)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, out, nil
			})
			const token = "owned-role-amendment-fixture"
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("CLI omitted owner authentication")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			root, instructions := t.TempDir(), ""
			instructions = filepath.Join(root, "roles.json")
			data, _ := json.Marshal(selected)
			for file, body := range map[string][]byte{"owner.token": []byte(token), "roles.json": data} {
				if err := os.WriteFile(filepath.Join(root, file), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := run(t.Context(), []string{"amend-roles", "--data", root, "--endpoint", server.URL, "--request-id", want.RequestID, "--run", want.RunID, "--revision", "42", "--reason", want.Reason, flagName, instructions}); err != nil {
				t.Fatal(err)
			}
			if got := <-requests; !reflect.DeepEqual(got, want) {
				t.Fatal("CLI changed the owner amendment", got)
			}
		})
	}
}

func TestAmendRolesCLIRejectsUnboundOrOverrideFlagsBeforeCredentials(t *testing.T) {
	base := []string{"amend-roles", "--run", "root", "--revision", "42", "--reason", "owner scope correction", "--role-instructions-file", "unused.json"}
	for _, extra := range [][]string{{"--role-models-file", "unused-models.json"}, {"--revision", "0"}, {"--reason", ""}, {"--role-instructions-file", ""}, {"--profile", "different"}, {"--extra-attempts", "0"}, {"--max-tasks", "0"}, {"--text", "resume now"}, {"--within-budget"}} {
		args := append(append(append([]string{}, base...), "--data", t.TempDir()), extra...)
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "amend-roles requires") {
			t.Fatal("invalid amendment reached credentials or source files", extra, err)
		}
	}
}
