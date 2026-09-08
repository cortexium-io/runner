package execution

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

// This identifies Runner's layout, not a provider cache key or a guarantee
// that the native harness cached a particular request.
const promptLayoutVersion = "stable-first-v1"

func harnessGuidance(kind string, cfg config.ExecutionConfig, includeSkills bool) string {
	var guidance string
	if includeSkills {
		guidance = trustedSkillInstructions(cfg)
	}
	switch kind {
	case config.HarnessCodexCLI:
		guidance += codexMCPPromptForConfig(cfg.MCPServers, cfg.SafeTools, cfg.HarnessConfigMode)
	case config.HarnessClaudeCLI, config.HarnessPiCLI:
		guidance += runnerBrowserPrompt(cfg.SafeTools)
	}
	return guidance + "\n\n"
}

func recordPromptContext(ctx context.Context, guidance string) {
	metrics.RecordPromptContext(ctx, metrics.PromptContext{
		Layout:         promptLayoutVersion,
		GuidanceDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(guidance))),
	})
}
