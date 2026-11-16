package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkLimitEnforcementIsOptionalInstancePolicy(t *testing.T) {
	loader, _ := fixture(t)
	baseline, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Instance.EnforceWorkLimits {
		t.Fatal("omitted policy enabled aggregate work limits")
	}
	for _, enforce := range []bool{false, true, false} {
		value, err := json.Marshal(enforce)
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := loader.Set("instance.yaml", "enforce_work_limits", value)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Instance.EnforceWorkLimits != enforce {
			t.Fatalf("enforcement = %t, want %t", loaded.Instance.EnforceWorkLimits, enforce)
		}
		if !reflect.DeepEqual(loaded.Projects, baseline.Projects) || loaded.Instance.Storage != baseline.Instance.Storage || loaded.Instance.Concurrency != baseline.Instance.Concurrency || loaded.Instance.TaskConcurrency != baseline.Instance.TaskConcurrency {
			t.Fatal("changing work enforcement changed frozen project definitions or resource policy")
		}
	}
	if _, err := loader.Set("instance.yaml", "enforce_work_limits", json.RawMessage(`"false"`)); err == nil {
		t.Fatal("non-boolean work enforcement policy accepted")
	}
}
