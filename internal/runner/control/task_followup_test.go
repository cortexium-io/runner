package control

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTaskFollowupStartSchemaAndResultPreserveLink(t *testing.T) {
	server := httptest.NewServer(Handler(nil, "followup-token", nil))
	defer server.Close()
	session, err := Connect(t.Context(), server.URL+"/mcp", "followup-token")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, tool := range listed.Tools {
		if tool.Name != "runner_start" && tool.Name != "runner_run" {
			continue
		}
		found++
		data, err := json.Marshal(tool.InputSchema)
		if err != nil || !bytes.Contains(data, []byte(`"followup"`)) || !bytes.Contains(data, []byte(`"run_id"`)) || !bytes.Contains(data, []byte(`"revision"`)) {
			t.Fatal("normal intake hides exact follow-up authority", tool.Name, err)
		}
	}
	if found != 2 {
		t.Fatal("normal start/run intake missing")
	}
	r := runner.Run{ID: "linked", Status: "Queued", Followup: &runner.TaskFollowupLineage{RunID: "accepted-root", Revision: 6322, RequestID: "owner-followup"}}
	out := compactResult(r, r.Created)
	if out.Followup == nil || *out.Followup != *r.Followup || out.Followup == r.Followup {
		t.Fatal("compact result lost or aliased the accepted-parent lineage")
	}
}
