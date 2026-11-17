package execution

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestCorrectionContextUsesProtectedLegacyAndNativeReferences(t *testing.T) {
	d := workspaceDisk(t)
	content := []byte(`{"runtime_interface":"RUNNER_PROJECT_FRONTEND_MODULES","policy":"preserve required labels"}`)
	proof := &runner.CorrectionContext{
		ID:     "run_owner/correction-context.json",
		Bytes:  int64(len(content)),
		SHA256: fmt.Sprintf("%x", sha256.Sum256(content)),
		Roles:  []string{"owner", "review"},
	}
	if err := d.Write("artifacts/"+proof.ID, content, runner.MaxCorrectionContext); err != nil {
		t.Fatal(err)
	}
	a := harnessAssignment(t)
	a.RecoveryContext = proof
	a.Setup.Profile.Harness = "codex"
	a.Setup.Profile.Access = "workspace-write"
	a.Setup.Profile.MaxInput = 8192
	a.Setup.Profile.MaxOutput = 1024
	a.Inputs = map[string]runner.Value{"brief": runner.Text("Keep the original request")}
	if err := d.Write("work/owned/source/.keep", nil, 1); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(d.Root, "work", "owned", "source")
	j := nativeJob{Assignment: a, Workspace: &workspaceRecord{WorkDir: work}}
	c := &Codex{disk: d}
	if err := c.workspaceInputs(j); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, ".runner-tmp", "inputs", a.AttemptID, "recovery-context.json")
	staged, err := os.ReadFile(path)
	if err != nil || string(staged) != string(content) {
		t.Fatal("legacy source owner lacks exact context", err)
	}
	payload, _, err := nativePayloadAt(a, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var request nativePayloadRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil || request.RecoveryContext == nil || request.RecoveryContext.SHA256 != proof.SHA256 || filepath.IsAbs(request.RecoveryContext.Path) || !strings.HasSuffix(request.RecoveryContext.Path, "recovery-context.json") {
		t.Fatal("legacy payload lost its readonly context reference", err)
	}
	if len(request.Inputs) != 1 || strings.Contains(payload, string(content)) {
		t.Fatal("supplement changed original inputs or duplicated context contents")
	}
	a.Setup.Profile.Harness = "native"
	a.Setup.Profile.Access = "workspace-read"
	payload, _, err = harnessPayload(a)
	if err != nil {
		t.Fatal(err)
	}
	var native struct {
		RecoveryContext *recoveryContextReference `json:"recovery_context"`
	}
	if err := json.Unmarshal([]byte(payload), &native); err != nil || native.RecoveryContext == nil || native.RecoveryContext.Path != harnessArtifactFiles(a)[proof.ID] || native.RecoveryContext.SHA256 != proof.SHA256 {
		t.Fatal("native image reviewer lacks exact declared context", err)
	}
	changed := runner.Clone(a)
	changed.RecoveryContext = nil
	if _, exists := harnessArtifactFiles(changed)[proof.ID]; exists {
		t.Fatal("unselected actor gained supplemental artifact access")
	}
	a.RecoveryContext = runner.Clone(proof)
	a.RecoveryContext.SHA256 = strings.Repeat("0", 64)
	if _, err := stagedInputData(d, a); err == nil {
		t.Fatal("changed context bytes/hash were staged")
	}
}
