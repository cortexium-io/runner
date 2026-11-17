package control

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEvidenceHandoffOwnerSchemaRequiresIdentityAndGrantsNoOverrides(t *testing.T) {
	host := httptest.NewServer(Handler(nil, "owner-token", nil))
	defer host.Close()
	session, err := Connect(t.Context(), host.URL+"/mcp", "owner-token")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "runner_recover_evidence_handoff" {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"revision", "phase_request_id", "attempt_id", "source_attempt_id", "check_attempt_id", "patch_sha256", "check_sha256", "evidence", "roles", "sha256", "bytes"} {
			if !bytes.Contains(schema, []byte(`"`+field+`"`)) {
				t.Fatal("owner schema omits exact identity", field)
			}
		}
		for _, field := range []string{"max_uses", "additional_uses", "extra_attempts", "profile", "preserve_source", "content"} {
			if bytes.Contains(schema, []byte(`"`+field+`"`)) {
				t.Fatal("owner schema exposes override or owner-supplied evidence", field)
			}
		}
		return
	}
	t.Fatal("owner evidence handoff operation missing")
}
