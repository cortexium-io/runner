package control

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCorrectionRecoveryOwnerToolHasExplicitBoundedContract(t *testing.T) {
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
		if tool.Name != "runner_recover_corrections" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil || !bytes.Contains(data, []byte(`"attempt_id"`)) || !bytes.Contains(data, []byte(`"max_uses"`)) || !bytes.Contains(data, []byte(`"preserve_source"`)) || bytes.Contains(data, []byte(`"extra_attempts"`)) || bytes.Contains(data, []byte(`"profile"`)) {
			t.Fatalf("owner recovery schema changes execution allowance or hides preservation: %s; %v", data, err)
		}
		return
	}
	t.Fatal("audited owner recovery tool is missing")
}
