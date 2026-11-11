package control

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFrontendRecoveryOwnerSchemaKeepsAssertionProofOptional(t *testing.T) {
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
		if tool.Name != "runner_recover_frontend_check" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(data, &schema); err != nil || schema.Properties["assertion_sha256"] == nil {
			t.Fatal("frontend recovery omits assertion report identity", string(data), err)
		}
		for _, field := range schema.Required {
			if field == "assertion_sha256" {
				t.Fatal("ordinary frontend recovery requires assertion mode")
			}
		}
		for _, forbidden := range []string{"extra_attempts", "max_uses", "profile", "preserve_source", "owner_step"} {
			if bytes.Contains(data, []byte(`"`+forbidden+`"`)) {
				t.Fatal("frontend recovery exposes an allowance or arbitrary role", forbidden)
			}
		}
		return
	}
	t.Fatal("owner frontend recovery tool is missing")
}
