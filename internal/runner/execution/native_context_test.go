//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestHarnessLeanContextDisablesActualMCPNames(t *testing.T) {
	spec := leanContextSpec(t)
	script := `#!/bin/sh
case "$*" in
 *'mcp_servers='*) printf '%s' '[{"name":"build.server","enabled":false}]' ;;
 *) printf '%s' '[{"name":"build.server","enabled":true,"transport":{"env":{"secret":"DO_NOT_RETAIN"}}}]' ;;
esac
`
	spec.Binary = filepath.Join(t.TempDir(), "codex-config-probe")
	if err := os.WriteFile(spec.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	facts, err := prepareCodexContext(t.Context(), spec, map[string]bool{})
	if err != nil || !facts.Verified || len(facts.ConfiguredMCP) != 1 || len(facts.EnabledMCP) != 0 {
		t.Fatal("lean policy was not verified", facts, err)
	}
	data, _ := json.Marshal(facts)
	if strings.Contains(string(data), "DO_NOT_RETAIN") || !strings.Contains(strings.Join(facts.Overrides, "\n"), `mcp_servers={"build.server"={enabled=false}}`) {
		t.Fatal("unsafe or incomplete policy facts", string(data))
	}
	spec.ContextOverrides = facts.Overrides
	args := strings.Join(harnessArgs(spec, "schema.json", []byte(`{}`)), "\n")
	for _, expected := range []string{"features.apps=false", "features.plugins=false", "features.skill_search=false", "skills.include_instructions=false", `mcp_servers={"build.server"={enabled=false}}`} {
		if !strings.Contains(args, expected) {
			t.Fatal("missing enforced context setting", expected)
		}
	}
	if strings.Contains(args, "ignore-user-config") || strings.Contains(args, "ignore-rules") {
		t.Fatal("lean mode discarded normal config, guidance or policy")
	}
}

func TestHarnessAllCodexProfilesDisableNativeAgents(t *testing.T) {
	for _, mode := range []string{"", "native", "lean"} {
		a := harnessAssignment(t)
		a.Setup.Profile.Native.Context = mode
		if mode == "lean" {
			a.Setup.Profile.Native.Skills = runner.CapabilitySelection{Mode: "none"}
		}
		args := strings.Join(harnessArgs(harnessSpec{Assignment: a}, "schema.json", []byte(`{}`)), "\x00")
		for _, setting := range []string{"agents.enabled=false", "tools.experimental_request_user_input.enabled=false", "features.multi_agent=false", "features.multi_agent_v2=false"} {
			if strings.Count(args, setting) != 1 {
				t.Fatal("Codex native subagents were not explicitly disabled", mode, setting)
			}
		}
	}
}

func TestHarnessLeanContextNeverFallsBack(t *testing.T) {
	for _, response := range []string{"invalid", `[{"name":"still-active","enabled":true}]`} {
		t.Run(response, func(t *testing.T) {
			spec := leanContextSpec(t)
			spec.Binary = filepath.Join(t.TempDir(), "codex-config-probe")
			if err := os.WriteFile(spec.Binary, []byte("#!/bin/sh\nprintf '%s' '"+response+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareCodexContext(t.Context(), spec, map[string]bool{}); err == nil {
				t.Fatal("invalid or active MCP inventory silently widened lean mode")
			}
		})
	}
}

func TestHarnessDiagnosticItemsAreNotToolCalls(t *testing.T) {
	e := harnessEvents{}
	e.accept([]byte(`{"type":"item.completed","item":{"type":"error","message":"PRIVATE_ERROR_PAYLOAD"}}`))
	if e.Proof.DiagnosticEvents != 1 || len(e.Proof.Used) != 1 || e.Proof.Used[0].Kind != "diagnostic" || !strings.Contains(e.Proof.Used[0].Evidence, "message_sha256") || strings.Contains(e.Proof.Used[0].Evidence, "PRIVATE_ERROR_PAYLOAD") || e.Failed != "" {
		t.Fatal("recoverable diagnostic was mistaken for tool use or final failure", e.Proof)
	}
	e.accept([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"StructuredOutput"}]}}`))
	if len(e.Proof.Used) != 2 || e.Proof.Used[1].Kind != "protocol" {
		t.Fatal("structured result protocol was mistaken for an ordinary tool")
	}
	e.accept([]byte(`{"type":"error","message":"PRIVATE_ERROR_PAYLOAD"}`))
	if e.Proof.DiagnosticEvents != 2 || e.Failed == "" {
		t.Fatal("terminal error event was dropped because message is a string")
	}
}

func TestHarnessLeanContextInstalledCodex(t *testing.T) {
	if os.Getenv("RUNNER_NATIVE_CONTEXT") != "1" {
		t.Skip("set RUNNER_NATIVE_CONTEXT=1 for installed Codex context proof; no model turn")
	}
	spec := leanContextSpec(t)
	var err error
	spec.Binary, err = exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := prepareCodexContext(t.Context(), spec, map[string]bool{})
	if err != nil || !facts.Verified || len(facts.EnabledMCP) != 0 {
		t.Fatal("installed Codex could not enforce lean MCP policy", facts, err)
	}
	t.Logf("configured MCP servers=%d; enabled after overrides=%d", len(facts.ConfiguredMCP), len(facts.EnabledMCP))
	if err := os.WriteFile(filepath.Join(spec.Directory, "AGENTS.md"), []byte("RUNNER_LEAN_PROJECT_GUIDANCE must be retained.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	type size struct{ agents, questions, guidance, input int }
	measurements := map[string]size{}
	for _, mode := range []string{"legacy_flags", "runner_gate"} {
		args := []string{"debug", "prompt-input", "-c", `model="gpt-6.2-sol"`}
		for _, override := range append(runner.Clone(facts.RunnerOverrides), facts.Overrides...) {
			if mode == "legacy_flags" && (override == "agents.enabled=false" || override == "tools.experimental_request_user_input.enabled=false") {
				continue
			}
			args = append(args, "-c", override)
		}
		args = append(args, "Model-free Runner prompt preview")
		cmd := exec.CommandContext(ctx, spec.Binary, args...)
		cmd.Dir = spec.Directory
		out := &boundedBuffer{limit: 1 << 20}
		cmd.Stdout = out
		if err := cmd.Run(); err != nil || !strings.Contains(out.String(), "RUNNER_LEAN_PROJECT_GUIDANCE") {
			t.Fatal("lean configuration lost project AGENTS.md guidance", err)
		}
		var items []struct {
			Tools   []json.RawMessage `json:"tools"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(out.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		measured := size{input: out.Len()}
		for _, item := range items {
			for _, raw := range item.Tools {
				var namespace struct {
					Tools []json.RawMessage `json:"tools"`
				}
				_ = json.Unmarshal(raw, &namespace)
				for _, rawTool := range namespace.Tools {
					var tool struct {
						Name string `json:"name"`
					}
					_ = json.Unmarshal(rawTool, &tool)
					if tool.Name == "spawn_agent" {
						compact, _ := json.Marshal(raw)
						measured.agents += len(compact)
					}
					if tool.Name == "request_user_input" || tool.Name == "request_user_input_async" {
						compact, _ := json.Marshal(rawTool)
						measured.questions += len(compact)
					}
				}
			}
			for _, part := range item.Content {
				if strings.Contains(part.Text, "<multi_agent_") {
					measured.guidance += len(part.Text)
				}
			}
		}
		if mode == "runner_gate" && (measured.agents != 0 || measured.questions != 0 || measured.guidance != 0) {
			t.Fatal("native agents/questions remained in the model-visible executable tool plan", measured)
		}
		measurements[mode] = measured
	}
	before, after := measurements["legacy_flags"], measurements["runner_gate"]
	t.Logf("native-agent tool JSON removed=%dB; direct-question tools removed=%dB; guidance removed=%dB; formatted preview removed=%dB", before.agents-after.agents, before.questions-after.questions, before.guidance-after.guidance, before.input-after.input)
}

func leanContextSpec(t *testing.T) harnessSpec {
	t.Helper()
	a := harnessAssignment(t)
	a.Setup.Profile.Native.Context = "lean"
	a.Setup.Profile.Native.Skills = runner.CapabilitySelection{Mode: "none"}
	return harnessSpec{Assignment: a, Directory: t.TempDir(), Owner: "runner-lean-config-proof"}
}
