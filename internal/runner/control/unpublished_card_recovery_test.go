package control

import (
	"bytes"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
	"testing"
)

func TestUnpublishedCardRecoveryOwnerToolsKeepExactBindingsAndNoOverrides(t *testing.T) {
	server := httptest.NewServer(Handler(nil, "owner-token", nil))
	defer server.Close()
	session, e := Connect(t.Context(), server.URL+"/mcp", "owner-token")
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	listed, e := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, tool := range listed.Tools {
		if tool.Name != "runner_preflight_unpublished_card_recovery" && tool.Name != "runner_recover_unpublished_card" {
			continue
		}
		count++
		data, e := json.Marshal(tool.InputSchema)
		if e != nil {
			t.Fatal(e)
		}
		for _, name := range []string{"run_id", "revision", "group_attempt_id", "child_run_id", "child_revision", "attempt_id", "patch_sha256", "bindings_sha256"} {
			if !bytes.Contains(data, []byte(`"`+name+`"`)) {
				t.Fatal("missing exact binding", name)
			}
		}
		for _, name := range []string{"extra_attempts", "profile", "max_uses", "owner_step", "inputs", "policy"} {
			if bytes.Contains(data, []byte(`"`+name+`"`)) {
				t.Fatal("owner API exposes override", name)
			}
		}
	}
	if count != 2 {
		t.Fatal("missing read-only preflight or owner action", count)
	}
}
