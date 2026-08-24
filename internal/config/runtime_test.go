package config

import "testing"

func TestResolveSeparatesFileRuntimeAndExecutionConfiguration(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.RunnerID = "runner_test"
	cfg.ProjectDir = t.TempDir()
	cfg.GitHubProject.BaseBranch = "dev"
	cfg.GitHubProject.RemoteName = "upstream"

	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if runtime.MaxParallelism != 1 || runtime.GitHubProject.ReadyStatus != "Ready" || runtime.GitHubProject.QAStatus != "Agent QA" {
		t.Fatalf("explicit runtime configuration was not resolved: %#v", runtime)
	}

	execution := runtime.Execution(WorkRoleImplementer, HarnessCodexCLI, "/tmp/worktree")
	if execution.Harness.Kind != HarnessCodexCLI || execution.Harness.WorkingDir != "/tmp/worktree" || execution.Harness.TimeoutSeconds != 7200 {
		t.Fatalf("role execution configuration was not resolved: %#v", execution)
	}
	if execution.WorkspaceBaseRef != "upstream/dev" {
		t.Fatalf("workspace base ref = %q, want upstream/dev", execution.WorkspaceBaseRef)
	}
	if cfg.MaxParallelism != 1 || len(cfg.Harnesses) != 1 {
		t.Fatalf("resolving mutated the persisted config: %#v", cfg)
	}
}
