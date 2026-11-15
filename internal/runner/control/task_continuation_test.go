package control

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTaskContinuationOwnerSchemaAndTypedInputs(t *testing.T) {
	server := httptest.NewServer(Handler(nil, "owner-token", nil))
	defer server.Close()
	session, err := Connect(t.Context(), server.URL+"/mcp", "owner-token")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name != "runner_continue_task" {
			continue
		}
		found = true
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, required := range []string{"request_id", "run_id", "revision", "project", "process", "source_attempt_id", "patch_sha256", "check_attempt_id", "check_sha256", "artifacts"} {
			if !bytes.Contains(data, []byte(`"`+required+`"`)) {
				t.Fatal("owner continuation lost exact authority", required, string(data))
			}
		}
		for _, forbidden := range []string{"command", "profile", "model", "extra_attempts", "max_attempts", "approval", "source_ref"} {
			if bytes.Contains(data, []byte(`"`+forbidden+`"`)) {
				t.Fatal("owner continuation exposes execution/allowance override", forbidden)
			}
		}
	}
	if !found {
		t.Fatal("owner continuation tool is missing")
	}
	args := ContinueTaskArgs{Inputs: map[string]any{"request": "visibly improve Sudoku"}, Artifacts: map[string]string{"patch": "source/changes.patch", "checks": "check/result"}}
	req, err := args.Request()
	if err != nil || string(req.Inputs["request"].Inline) != `"visibly improve Sudoku"` || req.Inputs["patch"].ArtifactID != "source/changes.patch" || req.Inputs["checks"].ArtifactID != "check/result" {
		t.Fatal("continuation input conversion changed declared types", req, err)
	}
	args.Inputs["patch"] = "inline patch override"
	if _, err = args.Request(); err == nil {
		t.Fatal("inline/artifact collision silently replaced a source identity")
	}
	r := runner.Run{ID: "root", ProjectID: "preview", Status: "Queued", TaskGraph: &runner.TaskGraphState{RootRunID: "root", Lineage: &runner.TaskContinuationLineage{OriginRunID: "old", PriorAttempts: 36, MaxAttempts: 64}}}
	r.TaskBudget = &runner.TaskAttemptBudget{Limit: 64, PriorAttempts: 36, Spent: 36, Remaining: 28}
	out := compactResult(r, r.Created)
	if out.TaskGraph.Lineage == nil || out.TaskGraph.Lineage.OriginRunID != "old" || out.Attempts != 36 || out.AttemptBudget == nil || out.AttemptBudget.Remaining != 28 {
		t.Fatal("compact continuation result hides history or remaining allowance", out)
	}
}
