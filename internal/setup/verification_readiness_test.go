package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestVerificationReadinessNeverExecutesConfiguredTools(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "tool")
	marker := filepath.Join(root, "must-not-run")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Verification: map[string]config.VerificationEntrypoint{"complete": {Command: tool, ToolchainCommands: []string{tool}}}}
	inspector := NewInspector(cfg, nil)
	states, ready := inspector.inspectVerification(t.Context())
	if !ready || len(states) != 1 || states[0].Status != CapabilityAvailable {
		t.Fatalf("readiness: %+v %v", states, ready)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Doctor executed configured verification")
	}
	if err := os.Remove(tool); err != nil {
		t.Fatal(err)
	}
	states, ready = inspector.inspectVerification(t.Context())
	if ready || states[0].Status != CapabilityBlocked {
		t.Fatal("missing verification tool passed readiness")
	}
}

func TestVerificationReadinessUsesConfiguredWholePlanReviewProfile(t *testing.T) {
	workflow := config.WorkflowTemplate(true)
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	roles["custom_review"] = config.RoleConfig{Extends: config.WorkRoleReviewer, Access: config.RoleAccessSandboxed}
	for index := range workflow.Rules {
		if workflow.Rules[index].Action.Role == config.WorkRoleReviewer {
			workflow.Rules[index].Action.Role = "custom_review"
		}
	}
	cfg := config.Config{Roles: roles, Workflow: &workflow, GitHubProject: &config.GitHubProjectConfig{}, PlanDelivery: &config.PlanDeliveryConfig{Enabled: true}}
	inspector := NewInspector(cfg, nil)
	states, ready := inspector.inspectVerification(t.Context())
	if ready || len(states) != 1 || states[0].Status != CapabilityBlocked {
		t.Fatalf("unsupported isolated plan gate passed Doctor: %+v", states)
	}
	roles["custom_review"] = config.RoleConfig{Extends: config.WorkRoleReviewer, Access: config.RoleAccessHost}
	states, ready = inspector.inspectVerification(t.Context())
	if !ready || states[0].Status != CapabilityAvailable {
		t.Fatalf("approved custom host profile not resolved: %+v", states)
	}
	if cfg.Roles[config.WorkRoleReviewer].Access != config.RoleAccessSandboxed {
		t.Fatal("readiness inspection widened base reviewer permissions")
	}
}

func TestVerificationReadinessInspectsPreparationAndRuntimeClosure(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "prepare")
	marker := filepath.Join(root, "must-not-execute")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\ntouch '"+marker+"'"), 0700); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(root, "Browser.app")
	if err := os.MkdirAll(runtimeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeRoot, "engine"), []byte("runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	entry := config.VerificationEntrypoint{Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, Preparation: &config.VerificationPreparation{Command: tool}, RuntimePaths: []string{runtimeRoot}}
	inspector := NewInspector(config.Config{Verification: map[string]config.VerificationEntrypoint{"complete": entry}}, nil)
	if _, ready := inspector.inspectVerification(t.Context()); !ready {
		t.Fatal("readable closure rejected")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Doctor executed preparation")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(runtimeRoot, "unobserved-engine")); err != nil {
		t.Fatal(err)
	}
	if _, ready := inspector.inspectVerification(t.Context()); ready {
		t.Fatal("external runtime link passed readiness")
	}
	if err := os.Remove(tool); err != nil {
		t.Fatal(err)
	}
	states, ready := inspector.inspectVerification(t.Context())
	if ready || !strings.Contains(*states[0].Detail, "configured verification tool") {
		t.Fatal("missing preparation executable passed readiness")
	}
}
