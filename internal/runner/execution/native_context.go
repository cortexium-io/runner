package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Model metadata may select V2 even when both legacy feature flags are false.
// The supported agents gate removes both tool specs and executable handlers.
var runnerCodexOverrides = []string{
	"agents.enabled=false",
	"tools.experimental_request_user_input.enabled=false",
	"features.multi_agent=false",
	"features.multi_agent_v2=false",
	"features.hooks=false",
}

var leanCodexOverrides = []string{
	"features.apps=false",
	"features.plugins=false",
	"features.remote_plugin=false",
	"features.recommended_plugins=false",
	"features.memories=false",
	"features.skill_search=false",
	"features.skill_mcp_dependency_install=false",
	"features.skip_host_skill_discovery=true",
	"skills.include_instructions=false",
	"skills.bundled.enabled=false",
	"cloud.skills.enabled=false",
	"orchestrator.mcp.enabled=false",
}

type codexMCPName struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
}

// A bare mcp_servers={} override merges with lower layers and leaves servers
// active. Disable the actual configured names, then verify the same effective
// configuration. Discovery never starts a model or MCP server.
func prepareCodexContext(ctx context.Context, spec harnessSpec, known map[string]bool) (*runner.NativeContextFacts, error) {
	if spec.Assignment.Setup.Profile.Native.Context != "lean" {
		return &runner.NativeContextFacts{Mode: "native", RunnerOverrides: runner.Clone(runnerCodexOverrides)}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	servers, err := codexMCPInventory(ctx, spec, leanCodexOverrides, known)
	if err != nil {
		return nil, err
	}
	facts := &runner.NativeContextFacts{
		Mode: "lean", Overrides: runner.Clone(leanCodexOverrides),
		RunnerOverrides: runner.Clone(runnerCodexOverrides),
		EnabledMCP:      []string{},
	}
	var disabled []string
	for _, server := range servers {
		facts.ConfiguredMCP = append(facts.ConfiguredMCP, server.Name)
		disabled = append(disabled, strconv.Quote(server.Name)+"={enabled=false}")
	}
	if len(disabled) > 0 {
		// The CLI splits dotted override paths literally; an inline table
		// preserves server names containing dots or hyphens without creating
		// a different, incomplete transport under a quoted name.
		facts.Overrides = append(facts.Overrides, "mcp_servers={"+strings.Join(disabled, ",")+"}")
	}
	effective, err := codexMCPInventory(ctx, spec, facts.Overrides, known)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, server := range effective {
		names = append(names, server.Name)
		if *server.Enabled {
			return nil, errors.New("Codex lean context could not disable every configured MCP server")
		}
	}
	if !slices.Equal(names, facts.ConfiguredMCP) {
		return nil, errors.New("Codex MCP configuration changed during lean readiness")
	}
	// `codex mcp` rejects --strict-config. Validate the generated settings via
	// the supported configuration-only app-server command before model launch.
	args := []string{"app-server", "--strict-config", "--print-startup-config"}
	for _, override := range append(runner.Clone(facts.RunnerOverrides), facts.Overrides...) {
		args = append(args, "-c", override)
	}
	cmd := exec.CommandContext(ctx, spec.Binary, args...)
	cmd.Dir = spec.Directory
	cmd.Env = append(os.Environ(), nativeOwnerVariable+"="+spec.Owner)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return nil, errors.New("Codex lean context strict configuration validation could not start")
	}
	identityErr := rememberNativeRoot(cmd.Process.Pid, known)
	waitErr := cmd.Wait()
	if identityErr != nil || waitErr != nil {
		return nil, errors.New("Codex does not support the generated lean context settings or strict configuration is invalid")
	}
	facts.Verified = true
	return facts, nil
}

func codexMCPInventory(ctx context.Context, spec harnessSpec, overrides []string, known map[string]bool) ([]codexMCPName, error) {
	var args []string
	for _, override := range runnerCodexOverrides {
		args = append(args, "-c", override)
	}
	for _, override := range overrides {
		args = append(args, "-c", override)
	}
	args = append(args, "mcp", "list", "--json")
	cmd := exec.CommandContext(ctx, spec.Binary, args...)
	cmd.Dir = spec.Directory
	cmd.Env = append(os.Environ(), nativeOwnerVariable+"="+spec.Owner)
	out := &boundedBuffer{limit: 256 << 10}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	if err := cmd.Start(); err != nil {
		return nil, errors.New("Codex lean context MCP discovery could not start")
	}
	identityErr := rememberNativeRoot(cmd.Process.Pid, known)
	waitErr := cmd.Wait()
	if identityErr != nil || waitErr != nil || out.err != nil {
		return nil, errors.New("Codex lean context MCP discovery failed or exceeded its bounded output/deadline")
	}
	var servers []codexMCPName
	if json.Unmarshal(out.Bytes(), &servers) != nil || len(servers) > 128 {
		return nil, errors.New("Codex lean context MCP inventory is invalid or exceeds 128 configured servers")
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	for i, server := range servers {
		if server.Name == "" || len(server.Name) > 160 || server.Enabled == nil || i > 0 && servers[i-1].Name == server.Name {
			return nil, errors.New("Codex lean context MCP inventory has invalid names")
		}
	}
	return servers, nil
}
