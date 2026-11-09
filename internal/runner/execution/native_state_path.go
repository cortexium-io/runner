package execution

import (
	"errors"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/runner"
)

// ValidateNativeStatePath checks the existing Unix-socket bound against the
// resolved state directory before the service can admit any native work.
// Instances without native Codex execution do not need a socket.
func ValidateNativeStatePath(canonicalRoot string, cfg *runner.CodexSettings) error {
	if cfg != nil && len(filepath.Join(canonicalRoot, "jobs", "codex.sock")) > 100 {
		return errors.New("readiness: use a shorter state directory for the native Unix socket (maximum path 100 bytes)")
	}
	return nil
}
