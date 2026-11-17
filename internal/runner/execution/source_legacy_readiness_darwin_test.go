//go:build darwin

package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSourceLegacyColdPinProtocolAndPermissionsWithoutModelTurn(t *testing.T) {
	binary := os.Getenv("RUNNER_SOURCE_NATIVE_CODEX")
	if binary == "" {
		t.Skip("set RUNNER_SOURCE_NATIVE_CODEX for zero-turn legacy app-server protocol/permission qualification")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "rsr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	policy := runner.DefaultStorage()
	policy.MinFree = 0
	disk, err := runner.OpenDisk(root, policy)
	if err != nil {
		t.Fatal(err)
	}
	goBinary := os.Getenv("RUNNER_SOURCE_LEGACY_GO")
	if goBinary == "" {
		goBinary = "/Users/dan/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.7.darwin-arm64/bin/go"
	}
	moduleCache := t.TempDir() // Concrete read-only cache permission proof; removed with this fixture.
	c := NewCodex(disk, &runner.CodexSettings{Binary: binary, AuthFile: filepath.Join(home, ".codex", "auth.json"), StorageLimit: 32 << 20, GoBinary: goBinary, GoModuleCache: moduleCache, ReadOnlyPaths: []string{"go.mod"}})
	c.workspaces = &Workspaces{disk: disk}
	a := sourceAssignment(disk, sourceRepository(t), "attempt_cold_pin")
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-read", Model: "gpt-6.2-sol", Reasoning: "xhigh", MaxInput: 8 << 10, MaxOutput: 512}
	w, err := c.workspaces.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	job := nativeJob{Assignment: a, Workspace: &w, Stage: "allocated"}
	c.mu.Lock()
	err = c.saveLocked(job)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = c.mutate(a.AttemptID, func(j *nativeJob) {
			j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "model-free protocol and permission fixture; no turn submitted"}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Error("owned protocol fixture did not close", err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	peer, err := c.openPeer(ctx)
	if err != nil {
		t.Fatal("cold configured app-server handshake failed", err)
	}
	if err := checkNativeConfig(ctx, peer); err != nil {
		t.Fatal("cold native feature/empty external-tool configuration differed", err)
	}
	if err := c.checkWorkspaceConfig(ctx, peer); err != nil {
		t.Fatal("cold legacy source permission catalog differed", err)
	}
	if err := c.workspaceReadiness(ctx, peer, job); err != nil {
		t.Fatal("actual legacy edit/read source/evidence/network/cache probes failed", err)
	}
	c.mu.Lock()
	saved, err := c.loadLocked(a.AttemptID)
	c.mu.Unlock()
	if err != nil || saved.TurnID != "" {
		t.Fatal("readiness fixture unexpectedly created a model turn", err)
	}
	t.Logf("cold pin %s: app-server handshake/config plus actual legacy edit/read permission probes passed; no turn/start", binary)
}
