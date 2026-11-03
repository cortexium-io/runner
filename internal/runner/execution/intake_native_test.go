//go:build darwin || linux

package execution

import (
	"context"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/control"
)

// Reuse the existing zero-turn inventory probe against the maintained portable
// client profile and an isolated real Runner endpoint, never a live project.
func TestNativePortableIntake(t *testing.T) {
	if os.Getenv("RUNNER_INTAKE_PROFILE_PREFLIGHT") != "1" {
		t.Skip("explicit opt-in: native permissions/tool inventory, zero model turns")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "ri-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	cfg := nativeSnapshot()
	rig := openNativeRig(t, filepath.Join(root, "state"), cfg, nil)
	defer rig.close()
	token := rand.Text() + rand.Text()
	server := httptest.NewServer(control.Handler(rig.core, token, nil))
	defer server.Close()
	clientHome := filepath.Join(root, "client", "home")
	if err = os.MkdirAll(clientHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "client", "empty"), 0500); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../examples/intake/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "http://127.0.0.1:8787/mcp", server.URL+"/mcp"))
	if err = os.WriteFile(filepath.Join(clientHome, "config.toml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(home, ".codex", "auth.json"), filepath.Join(clientHome, "auth.json")); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(root, "owner.token")
	if err = os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNNER_COORDINATOR_TOKEN_FILE", tokenFile)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	qualifyCoordinatorTools(t, ctx, root, os.Getenv("RUNNER_PREFLIGHT_CODEX"), clientHome)
}
