//go:build darwin || linux

package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeBuiltinAttributeAndInlineEvidenceImmutability(t *testing.T) {
	if os.Getenv("RUNNER_ATTRIBUTE_PREFLIGHT") != "1" {
		t.Skip("explicit opt-in for actual edit-profile attributes/evidence permission checks; zero model turns")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "runner-attribute-permission-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	cfg := workspaceSnapshot(t, sourceRepositoryAt(t, filepath.Join(root, "repository")))
	cfg.Instance.Codex.Binary = os.Getenv("RUNNER_PREFLIGHT_CODEX")
	cfg.Instance.Codex.GoBinary = os.Getenv("RUNNER_PREFLIGHT_GO")
	if !filepath.IsAbs(cfg.Instance.Codex.Binary) || !filepath.IsAbs(cfg.Instance.Codex.GoBinary) {
		t.Fatal("explicit qualified Codex and Go runtimes required")
	}
	for _, runtime := range []struct {
		binary string
		args   []string
		want   string
	}{
		{cfg.Instance.Codex.Binary, []string{"--version"}, "codex-cli 0.164.0"},
		{cfg.Instance.Codex.GoBinary, []string{"version"}, "go version go1.26.7 "},
	} {
		cmd := exec.CommandContext(t.Context(), runtime.binary, runtime.args...)
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), runtime.want) {
			t.Fatalf("unexpected qualified runtime: %v: %s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	}
	for _, path := range cfg.Instance.Codex.ReadOnlyPaths {
		if path == ".gitattributes" {
			t.Fatal("fixture must prove the automatic attribute rule rather than an operator-configured read-only path")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	qualifyWorkspacePermissions(t, ctx, root, cfg)
	t.Log("actual workspace-write/read profiles preserve source edit/scratch authority while denying root attribute mutation and staged artifact/inline evidence write, rename and delete; zero model turns")
}
