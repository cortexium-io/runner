# Complete development qualification

The disposable Go website initially lacks `greeting.go`. Fixed tests protect
ordinary greeting behavior and HTML escaping. `proof/main.go` owns bounded actual
Go benchmarks, packaging and a headless browser check against the retained
executable. It contains no workflow scheduling. Browser dependencies and pinned
Codex/Go executables are supplied by the operator, not installed by the fixture.

`TestNativeDevelopmentWorkflow` loads normal on-disk configuration, submits once
over MCP, and runs implementation, verification, two benchmark rounds, independent
review and local candidate integration. It then exercises a saved delivery
approval across service restart, packages the executable and checks desktop/mobile
browser journeys. The fixture's approval is simulated for its disposable target.
No production repository, publication or deployment is involved.

Normal Go gates skip this test. Set `RUNNER_DEVELOPMENT_PROBE=1` for the same
journey with scripted model judgments and real sandboxed commands. Explicit
`RUNNER_NATIVE_DEVELOPMENT=1` instead permits at most four Sol/xhigh turns,
protected by a durable no-repeat receipt in a fresh `RUNNER_WORKFLOW_ROOT`.
Also supply absolute `RUNNER_WORKFLOW_CODEX`, `RUNNER_NODE`,
`RUNNER_BROWSER_SCRIPT`, `RUNNER_PLAYWRIGHT_MODULE` and `RUNNER_CHROMIUM` paths.
Use a standalone Node binary with its required libraries available to the configured
sandbox. The browser uses the previously qualified macOS headless launch flags.

The proof test records selected configuration, all run attempts, the compact
result and retained artifacts. Its model usage covers Runner's roles; the calling
maintenance chat is additional. Comparing two measurements proves the iteration
mechanism; it does not establish an optimization or model-quality advantage.
Native qualification applies to the selected macOS/harness/tool setup. Existing
model-free suites separately own rejection, correction, allowance, stale-proof,
approval replay, invalid-plan, lifecycle and access-boundary behavior.
