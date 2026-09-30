package execution

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// This is a local toolchain check, never a model invocation or availability probe.
// Ordinary tests need neither an installed Codex CLI nor sandbox privileges.
func TestImplementationProfileToolchainPreflight(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_IMPLEMENTATION_PREFLIGHT") != "1" {
		t.Skip("opt in to the installed Codex sandbox; no model invocation")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("comparison requires an installed Go toolchain")
	}
	profile, err := ProfileForRole(RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	runtime := config.RuntimeConfig{
		Harnesses:     []config.HarnessConfig{{Kind: config.HarnessCodexCLI, Command: "codex"}},
		Roles:         map[string]config.RoleConfig{config.WorkRoleImplementer: {Harness: config.HarnessCodexCLI}},
		RoleContracts: map[string]string{config.WorkRoleImplementer: config.WorkRoleImplementer},
	}
	cfg := runtime.Execution(config.WorkRoleImplementer, config.HarnessCodexCLI, t.TempDir())
	workspace, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, cfg.Harness.WorkingDir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.cleanup()
	for name, content := range map[string]string{
		"go.mod":            "module example.com/toolchainpreflight\n\ngo 1.25.0\n",
		"toolchain_test.go": "package toolchainpreflight\n\nimport \"testing\"\n\nfunc TestToolchainStartup(t *testing.T) {}\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace.Dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"sandbox", "-P", codexImplementerDevelopmentPermissionProfile, "-C", workspace.Dir}
	policy := codexProfileArgs(profile, workspace, cfg.SafeTools, "codex")
	for i := 0; i+1 < len(policy); i++ {
		if policy[i] == "--config" && strings.HasPrefix(policy[i+1], "permissions.") {
			args = append(args, "--config", policy[i+1])
		}
	}
	// The fixture uses only the standard library. Keep the same filesystem policy
	// with network disabled; no browser server or harness exec is launched.
	args = append(args, "--config", "permissions."+codexImplementerDevelopmentPermissionProfile+".network.enabled=false", "--", "env")
	environment := sandboxEnvironment(workspace)
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, fmt.Sprintf("%s=%s", key, environment[key]))
	}
	args = append(args, goPath, "test", "-count=1", ".")
	result, err := (subprocess.OSRunner{}).Run(t.Context(), "codex", args, workspace.Dir, 45*time.Second)
	if err != nil {
		t.Fatalf("native Go toolchain preflight failed: %v\n%s%s", err, result.Stdout, result.Stderr)
	}
}
