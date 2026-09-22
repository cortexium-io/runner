//go:build darwin || linux

package setup

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestVerificationReadinessRejectsExecutableSpecialFiles(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "tool")
	if err := syscall.Mkfifo(fifo, 0700); err != nil {
		t.Fatal(err)
	}
	link := fifo + "-link"
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{fifo, link} {
		inspector := NewInspector(config.Config{Verification: map[string]config.VerificationEntrypoint{"complete": {Command: command, ToolchainCommands: []string{command}}}}, nil)
		states, ready := inspector.inspectVerification(t.Context())
		if ready || len(states) != 1 || states[0].Status != CapabilityBlocked {
			t.Fatalf("special executable reported ready: %+v", states)
		}
	}
}
