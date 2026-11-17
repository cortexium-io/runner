package execution

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/runner"
)

// Only the fixed-command adapter calls this, after its bounded stdout capture.
// Native model streams never become diagnostic artifacts.
func writeCommandDiagnostic(a runner.Assignment, data []byte) (*runner.DiagnosticArtifact, error) {
	limit := int(min(a.Limits.MaxLog, a.Limits.MaxArtifact))
	if limit < 1 || len(data) == 0 {
		return nil, nil
	}
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	proof := &runner.DiagnosticArtifact{
		Bytes:   int64(len(data)),
		SHA256:  fmt.Sprintf("%x", sha256.Sum256(data)),
		AtLimit: len(data) == limit,
	}
	proof.ID = a.AttemptID + "/command-" + proof.SHA256 + ".log"
	if err := workerWrite(filepath.Join(a.OutputDir, filepath.Base(proof.ID)), data); err != nil {
		return nil, err
	}
	return proof, nil
}
