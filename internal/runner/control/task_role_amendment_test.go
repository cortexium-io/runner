package control

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTaskRoleAmendmentOwnerSchemaHasNoExecutionOrAllowanceOverrides(t *testing.T) {
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
		if tool.Name != "runner_amend_roles" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties           map[string]any `json:"properties"`
			Required             []string       `json:"required"`
			AdditionalProperties bool           `json:"additionalProperties"`
		}
		if err != nil || json.Unmarshal(data, &schema) != nil {
			t.Fatal("role amendment schema could not be read", err)
		}
		want := []string{"reason", "request_id", "revision", "role_instructions", "run_id"}
		var properties []string
		for key := range schema.Properties {
			properties = append(properties, key)
		}
		slices.Sort(properties)
		slices.Sort(schema.Required)
		if !reflect.DeepEqual(properties, want) || !reflect.DeepEqual(schema.Required, want) || schema.AdditionalProperties {
			t.Fatal("role-only schema permits overrides or lacks exact authority", string(data))
		}
		return
	}
	t.Fatal("owner role amendment tool is missing")
}
