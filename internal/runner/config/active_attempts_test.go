package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestActiveAttemptLimitDefaultsOnlyForGeneralProcesses(t *testing.T) {
	for _, tc := range []struct {
		general     bool
		limit, want int
	}{
		{false, 0, 0},
		{false, 4, 4},
		{true, 0, 2},
		{true, 1, 1},
		{true, 32, 32},
	} {
		t.Run(fmt.Sprintf("general-%t-limit-%d", tc.general, tc.limit), func(t *testing.T) {
			loader, dir := fixture(t)
			if tc.general {
				process := runner.Process{
					ID: "greeting", Inputs: map[string]string{"name": "text"},
					Steps:       []runner.Step{{ID: "prepare", Role: "prepare", Inputs: map[string]runner.Binding{"name": {FromInput: "name"}}}},
					Outputs:     map[string]runner.Binding{"result": {FromStep: "prepare", Output: "message"}},
					MaxAttempts: 8, MaxAttemptsPerStep: 2, AttemptTimeout: "30s",
					Tasks: &runner.TaskPolicy{Roles: []string{"prepare"}, MaxTasks: 4, MaxDepth: 2, MaxConcurrent: 2},
				}
				data, err := json.Marshal(process)
				if err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(dir, "projects/demo/processes/greeting.yaml"), string(data))
			}
			if _, err := loader.Load(); err != nil {
				t.Fatal(err)
			}
			resolved, err := loader.Set("instance.yaml", "max_active_attempts", json.RawMessage(fmt.Sprint(tc.limit)))
			if err != nil || resolved.Instance.MaxActiveAttempts != tc.want {
				t.Fatalf("global execution limit/default: got %d want %d: %v", resolved.Instance.MaxActiveAttempts, tc.want, err)
			}
		})
	}
	for _, invalid := range []int{-1, 33} {
		loader, _ := fixture(t)
		if _, err := loader.Load(); err != nil {
			t.Fatal(err)
		}
		if _, err := loader.Set("instance.yaml", "max_active_attempts", json.RawMessage(fmt.Sprint(invalid))); err == nil {
			t.Fatalf("unbounded active attempt limit accepted: %d", invalid)
		}
	}
}
