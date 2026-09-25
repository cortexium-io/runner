package execution

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
)

func TestImplementationHandoffBudgetUsesEarlierDeadline(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inherited  time.Duration
		min, max   time.Duration
		wantParent bool
	}{
		{name: "direct invocation", min: 2 * time.Hour, max: 2 * time.Hour},
		{name: "original budget partly spent", inherited: 10 * time.Minute, min: 9 * time.Minute, max: 10 * time.Minute, wantParent: true},
		{name: "longer parent cannot enlarge timeout", inherited: 3 * time.Hour, min: 2 * time.Hour, max: 2 * time.Hour},
		{name: "expired budget is zero", inherited: -time.Minute, wantParent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background() // No test-runner deadline in the direct-invocation case.
			deadline := time.Now().Add(tc.inherited)
			if tc.inherited != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, deadline)
				defer cancel()
			}
			cfg := config.ExecutionConfig{Harness: config.HarnessConfig{TimeoutSeconds: 7200}}
			before := time.Now()
			prompt := implementationHandoff(ctx, cfg)
			after := time.Now()
			_, budget, found := strings.Cut(prompt, "Implementation runtime budget: at most ")
			budget, _, _ = strings.Cut(budget, " remaining")
			remaining, err := time.ParseDuration(budget)
			if !found || err != nil || remaining < tc.min || remaining > tc.max {
				t.Fatalf("remaining budget = %q (%v), want %s..%s", budget, err, tc.min, tc.max)
			}
			_, finish, _ := strings.Cut(prompt, "finish by ")
			finish, _, _ = strings.Cut(finish, ". ")
			gotDeadline, err := time.Parse(time.RFC3339, finish)
			if err != nil {
				t.Fatalf("missing UTC deadline: %v", err)
			}
			if tc.wantParent {
				if !gotDeadline.Equal(deadline.Truncate(time.Second)) {
					t.Fatalf("deadline = %s, want inherited %s", gotDeadline, deadline)
				}
			} else if gotDeadline.Before(before.Add(2*time.Hour).Truncate(time.Second)) || gotDeadline.After(after.Add(2*time.Hour)) {
				t.Fatalf("deadline %s did not respect invocation timeout", gotDeadline)
			}
			if strings.Contains(prompt, "from launch") || !strings.Contains(prompt, "not a fresh allowance") || !strings.Contains(prompt, "do not report success, waive checks") {
				t.Fatal("handoff renewed the budget or lost incomplete-proof safeguards")
			}
		})
	}
}
