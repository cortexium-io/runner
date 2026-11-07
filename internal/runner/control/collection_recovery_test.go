package control

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCollectionRecoveryOwnerSchemaCannotGrantExecutionAllowances(t *testing.T) {
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
	for _, tool := range listed.Tools {
		if tool.Name != "runner_recover_collection" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil || !bytes.Contains(data, []byte(`"run_id"`)) || !bytes.Contains(data, []byte(`"attempt_id"`)) || !bytes.Contains(data, []byte(`"request_id"`)) {
			t.Fatal("collection recovery omits exact owner identity", string(data), err)
		}
		for _, forbidden := range []string{"extra_attempts", "max_uses", "profile", "preserve_source", "owner_step"} {
			if bytes.Contains(data, []byte(`"`+forbidden+`"`)) {
				t.Fatal("collection recovery exposes an allowance or arbitrary role", forbidden)
			}
		}
		return
	}
	t.Fatal("owner collection recovery tool is missing")
}
