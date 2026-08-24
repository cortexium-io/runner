package github

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

const minimumSecondaryRateLimitDelay = time.Minute

// RateLimitRetryDelay returns a GitHub-directed cooldown only for errors that
// look like primary or secondary rate-limit responses. The rate-limit endpoint
// uses the separate REST core budget and is queried only after such an error.
func RateLimitRetryDelay(ctx context.Context, run subprocess.Runner, cause error, now time.Time) (time.Duration, bool) {
	if cause == nil {
		return 0, false
	}
	message := strings.ToLower(cause.Error())
	if !strings.Contains(message, "rate limit") && !strings.Contains(message, "too many requests") {
		return 0, false
	}
	if run == nil {
		run = subprocess.OSRunner{}
	}
	result, err := subprocess.RunGitHub(ctx, run, []string{"api", "rate_limit"}, "", 30*time.Second)
	if err != nil {
		return minimumSecondaryRateLimitDelay, true
	}
	var payload struct {
		Resources map[string]struct {
			Remaining int   `json:"remaining"`
			Reset     int64 `json:"reset"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return minimumSecondaryRateLimitDelay, true
	}
	resource := "core"
	if strings.Contains(message, "graphql") {
		resource = "graphql"
	} else if strings.Contains(message, "search") {
		resource = "search"
	}
	reset := int64(0)
	if limit, ok := payload.Resources[resource]; ok && limit.Remaining == 0 {
		reset = limit.Reset
	}
	if reset == 0 {
		for _, limit := range payload.Resources {
			if limit.Remaining == 0 && limit.Reset > reset {
				reset = limit.Reset
			}
		}
	}
	if reset > now.Unix() {
		return time.Unix(reset, 0).Sub(now) + time.Second, true
	}
	return minimumSecondaryRateLimitDelay, true
}
