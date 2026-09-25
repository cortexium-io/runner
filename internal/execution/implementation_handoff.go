package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
)

// Appended after stable guidance/task context so invocation timing cannot
// invalidate the reusable instruction prefix. Paths grant no new host access.
func implementationHandoff(ctx context.Context, cfg config.ExecutionConfig) string {
	var b strings.Builder
	if cfg.Harness.TimeoutSeconds > 0 {
		now := time.Now()
		deadline := now.Add(time.Duration(cfg.Harness.TimeoutSeconds) * time.Second)
		if inherited, ok := ctx.Deadline(); ok && inherited.Before(deadline) {
			deadline = inherited
		}
		remaining := max(deadline.Sub(now), 0).Truncate(time.Second)
		fmt.Fprintf(&b, "\n\nImplementation runtime budget: at most %s remaining at prompt preparation (%s); finish by %s. This is the earlier of the inherited execution deadline and this invocation's configured timeout, not a fresh allowance after setup, repair or a specialist handoff. Reserve time for required verification, any necessary repair and the structured handoff; use observed gate durations rather than starting a gate that cannot fit. Preserve honest partial evidence and remaining work if completion is no longer feasible; do not report success, waive checks, or leave validation running in the background.\n", remaining, now.UTC().Format(time.RFC3339), deadline.UTC().Format(time.RFC3339))
	}
	if len(cfg.ReviewEvidencePaths) > 0 {
		paths, _ := json.Marshal(cfg.ReviewEvidencePaths)
		fmt.Fprintf(&b, "\nOperator-selected QA evidence paths (literal, relative to this worktree): %s. Runner will snapshot only these selections read-only beside the isolated QA checkout. Retain the minimal applicable reports, candidate manifests, setup outcomes and failure/rerun receipts within these selections as work progresses; include a concise index mapping each original result to its tested candidate and final-diff applicability. Do not fabricate receipts, copy credentials, whole dependency trees or unrelated reports, or replace existing operator files. Use the repository's ignored-artifact convention; reports are not product deliverables. Missing or unsafe selections are not successful proof. Keep the structured verification entries self-contained; selection does not attest that a check ran.\n", paths)
	}
	return b.String()
}
