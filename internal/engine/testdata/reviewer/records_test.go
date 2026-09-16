package records

import (
	"maps"
	"testing"
)

func TestUpdatePreservesOwnership(t *testing.T) {
	records := map[string]Record{"r1": {Tenant: "alice", Title: "old"}}
	if err := Update(records, "alice", "r1", "new"); err != nil {
		t.Fatal(err)
	}
	if got, want := records["r1"], (Record{Tenant: "alice", Title: "new"}); got != want {
		t.Fatalf("updated record = %#v, want %#v", got, want)
	}
}

func TestUpdateRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, tenant, id, title string
	}{
		{"foreign_tenant", "bob", "r1", "new"},
		{"unknown_record", "alice", "missing", "new"},
		{"blank_title", "alice", "r1", " \t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := map[string]Record{"r1": {Tenant: "alice", Title: "old"}}
			want := map[string]Record{"r1": {Tenant: "alice", Title: "old"}}
			if err := Update(records, tc.tenant, tc.id, tc.title); err == nil {
				t.Errorf("Update(%q, %q, %q) succeeded, want error", tc.tenant, tc.id, tc.title)
			}
			if !maps.Equal(records, want) {
				t.Errorf("records = %#v, want unchanged %#v", records, want)
			}
		})
	}
}
