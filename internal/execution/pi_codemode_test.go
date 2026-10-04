package execution

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestPiExecutorEnablesCodemodeOnlyForOptedInRole(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		run := &piWorkspaceCommandRunner{}
		cfg := config.ExecutionConfig{Codemode: enabled, Harness: config.HarnessConfig{
			Kind: config.HarnessPiCLI, Command: "pi", WorkingDir: t.TempDir(), TimeoutSeconds: 30,
		}}
		assignment := testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]
		output, err := NewAgentExecutor(config.HarnessPiCLI, cfg, run).Execute(t.Context(), assignment)
		if err != nil || output.Outcome != OutcomeSucceeded {
			t.Fatalf("execute with Codemode %t: %#v, %v", enabled, output, err)
		}
		if got := containsCSVValue(argumentValue(run.args, "--tools"), "codemode"); got != enabled {
			t.Fatalf("Codemode %t produced tools %v", enabled, run.args)
		}
		if got := strings.Contains(strings.Join(run.args, " "), piCodemodeExtensionName); got != enabled {
			t.Fatalf("Codemode extension presence = %t, want %t", got, enabled)
		}
		if prompt := argumentValue(run.args, "--append-system-prompt"); !strings.Contains(prompt, piStructuredResultSystemPrompt) || strings.Contains(prompt, piCodemodeSystemPrompt) != enabled {
			t.Fatalf("Codemode replaced result instructions: %q", prompt)
		}
	}
}

func TestPiCodemodeAdmissionPreservesToolProfiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		mode string
		want bool
	}{
		{"read", []string{"--tools", "read,grep,find,ls,cortexium_runner_result"}, config.HarnessConfigModeIsolated, true},
		{"write", []string{"--tools", "read,bash,write,edit"}, config.HarnessConfigModeIsolated, true},
		{"inherit", nil, config.HarnessConfigModeInherit, true},
		{"formatter", []string{"--no-tools"}, config.HarnessConfigModeInherit, false},
		{"result only", []string{"--tools", "cortexium_runner_result"}, config.HarnessConfigModeIsolated, false},
		{"missing allowlist", nil, config.HarnessConfigModeIsolated, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := piInvocationAllowsCodemode(tc.args, tc.mode); got != tc.want {
				t.Fatalf("Codemode admitted = %t, want %t", got, tc.want)
			}
			args, err := addPiCodemodeExtension(tc.args, "/tmp/codemode.ts", tc.mode)
			if !tc.want {
				if err == nil {
					t.Fatal("Codemode added to a no-tools stage")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !containsArgPair(args, "--extension", "/tmp/codemode.ts") {
				t.Fatal("extension omitted")
			}
			if tc.mode == config.HarnessConfigModeInherit && contains(args, "--tools") {
				t.Fatal("inherited loadout overridden")
			}
			for i := 0; i+1 < len(tc.args); i++ {
				if tc.args[i] == "--tools" && !containsArgPair(args, "--tools", tc.args[i+1]+",codemode") {
					t.Fatalf("role tool boundary changed: %v", args)
				}
			}
		})
	}
}

func TestInstalledPiCodemodeBoundaries(t *testing.T) {
	packageDir, browserPath, marker := installedPiBrowserFixture(t, false)
	code, err := createPiCodemodeExtension()
	if err != nil {
		t.Fatal(err)
	}
	defer code.Close()
	schema := []byte(`{"type":"object","properties":{"outcome":{"type":"string"}},"required":["outcome"],"additionalProperties":false}`)
	structured, err := createPiStructuredResultExtension(schema, "prefer")
	if err != nil {
		t.Fatal(err)
	}
	defer structured.Close()
	native, err := createPiNativeStructuredResultExtension(schema, "low", false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	check := exec.CommandContext(t.Context(), "node", "testdata/pi-codemode-check.mjs", packageDir,
		code.Path(piCodemodeExtensionName), browserPath, marker, structured.path, structured.provenance, native.path)
	check.Env = append(os.Environ(), "PI_OFFLINE=1", "PI_EXPERIMENTAL=1")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("installed Pi Codemode boundary check: %v\n%s", err, output)
	}
}
